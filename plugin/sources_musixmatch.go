package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

const musixmatchEndpoint = "https://apic.musixmatch.com/ws/1.1"

const (
	musixmatchAppID     = "android-player-v1.0"
	musixmatchTokenKey  = "musixmatch:token:v1"
	musixmatchTokenTTL  = int64(10 * 24 * time.Hour / time.Second)
	musixmatchUserAgent = "Dalvik/2.1.0 (Linux; U; Android 10)"
)

// musixmatchProviderName is the display name reported in provenance.
const musixmatchProviderName = "Musixmatch"

// fetchMusixmatchLyricsCandidates resolves lyrics through Musixmatch's desktop
// API: mint (or reuse a configured) anonymous user token, search the track by
// duration drift, then fetch the richest usable payload (word richsync, line
// subtitles, or plain lyrics).
func fetchMusixmatchLyricsCandidates(p *lyricsProvider, track lyrics.TrackInfo, deadline time.Time) ([]lyricsCandidate, lookupHealth, error) {
	token, err := p.musixmatchToken()
	if err != nil {
		// Minting is best-effort; a challenge means this lookup skips Musixmatch
		// quietly and the next lookup can try again.
		return nil, lookupHealthy, nil
	}

	searchURL, ok := musixmatchSearchURL(track, token)
	if !ok {
		return nil, lookupHealthy, nil
	}
	searchResponse, err := p.musixmatchRequest("track.search", searchURL, deadline)
	if err != nil {
		return nil, lookupDegraded, err
	}
	if searchResponse.StatusCode == 401 {
		// The cached token went stale: drop it and try one fresh mint next lookup.
		p.cache.SetWithTTL(musixmatchTokenKey, nil, 1)
		return nil, lookupHealthy, nil
	}
	if searchResponse.StatusCode != 200 {
		return nil, lookupDegraded, providerResponseError("Musixmatch search", searchResponse)
	}

	var searchEnvelope musixmatchEnvelope
	if err := json.Unmarshal(searchResponse.Body, &searchEnvelope); err != nil {
		return nil, lookupDegraded, fmt.Errorf("decode Musixmatch search response: %w", err)
	}
	trackID, found := pickMusixmatchTrack(searchEnvelope.Message.Body.TrackList, track)
	if !found {
		return nil, lookupHealthy, nil
	}

	macroURL, ok := musixmatchMacroURL(strconv.FormatInt(trackID, 10), token)
	if !ok {
		return nil, lookupHealthy, nil
	}
	macroResponse, err := p.musixmatchRequest("macro.subtitles.get", macroURL, deadline)
	if err != nil {
		return nil, lookupDegraded, err
	}

	switch macroResponse.StatusCode {
	case 200:
		// pass through to the envelope decode
	case 404:
		return nil, lookupHealthy, nil
	case 429:
		retryAfter := retryAfterDuration(macroResponse.Headers, p.now())
		p.rememberCooldown(sourceGlobalCooldownKey("musixmatch"), retryAfter)
		return nil, lookupDegraded, rateLimitResponseError("Musixmatch subtitles", macroResponse, retryAfter)
	default:
		return nil, lookupDegraded, providerResponseError("Musixmatch subtitles", macroResponse)
	}

	var macroEnvelope musixmatchEnvelope
	if err := json.Unmarshal(macroResponse.Body, &macroEnvelope); err != nil {
		return nil, lookupDegraded, fmt.Errorf("decode Musixmatch subtitles response: %w", err)
	}
	return musixmatchCandidate(macroEnvelope.Message.Body.MacroCalls)
}

// musixmatchCandidate ranks the available payloads: richsync (word) before LRC
// subtitles (line) before plain lyrics.
func musixmatchCandidate(macroCalls map[string]musixmatchMacroBody) ([]lyricsCandidate, lookupHealth, error) {
	if richsyncBody := macroCalls["track.richsync.get"].Body.RichsyncBody; strings.TrimSpace(richsyncBody) != "" {
		if lines, err := parseRichsync(richsyncBody); err == nil && len(lines) > 0 {
			text := lyrics.LyricsText{Text: enhancedLRCFromTimedLines(lines)}
			if candidate := newLyricsCandidate(text, musixmatchProviderName, "elrc"); candidate.present() {
				return []lyricsCandidate{candidate}, lookupHealthy, nil
			}
		}
	}
	if subtitles := macroCalls["track.subtitles.get"].Body.SubtitleList; len(subtitles) > 0 {
		if candidate := newLyricsCandidate(lyrics.LyricsText{Text: subtitles[0].SubtitleBody}, musixmatchProviderName, "lrc"); candidate.present() {
			return []lyricsCandidate{candidate}, lookupHealthy, nil
		}
	}
	if plainLyrics := macroCalls["track.lyrics.get"].Body.LyricsBody; strings.TrimSpace(plainLyrics) != "" {
		text := lyrics.LyricsText{Text: plainLyrics}
		if candidate := newLyricsCandidate(text, musixmatchProviderName, "plain"); candidate.present() {
			return []lyricsCandidate{candidate}, lookupHealthy, nil
		}
	}
	return nil, lookupHealthy, nil
}

// musixmatchToken returns the configured admin token when present, otherwise the
// minted-and-cached anonymous token (minting once per attempt, dropping the cache
// entry when the API challenges it).
func (p *lyricsProvider) musixmatchToken() (string, error) {
	if config := readProviderConfig(p.configLookup).MusixmatchUserToken; config != "" {
		return config, nil
	}
	if cached, exists, _ := p.cache.Get(musixmatchTokenKey); exists && len(cached) > 0 {
		if token := strings.TrimSpace(string(cached)); token != "" {
			return token, nil
		}
	}

	nonce := randomMusixmatchNonce()
	requestURL := fmt.Sprintf("%s/token.get?app_id=%s&user_language=en&t=%s", musixmatchEndpoint, musixmatchAppID, nonce)
	response, err := p.musixmatchRequest("token.get", requestURL, p.now().Add(time.Minute))
	if err != nil {
		return "", err
	}
	if response.StatusCode == 401 {
		return "", fmt.Errorf("musixmatch token challenge")
	}
	if response.StatusCode != 200 && response.StatusCode != 201 {
		return "", providerResponseError("Musixmatch token", response)
	}

	var envelope musixmatchTokenEnvelope
	if err := json.Unmarshal(response.Body, &envelope); err != nil {
		return "", fmt.Errorf("decode Musixmatch token response: %w", err)
	}
	token := strings.TrimSpace(envelope.Message.Body.UserToken)
	if token == "" {
		return "", fmt.Errorf("musixmatch token response has no user token")
	}
	p.cache.SetWithTTL(musixmatchTokenKey, []byte(token), musixmatchTokenTTL)
	return token, nil
}

// musixmatchRequest issues one Musixmatch GET with the documented desktop UA and
// no-op AWS load balancer cookies.
func (p *lyricsProvider) musixmatchRequest(label, requestURL string, deadline time.Time) (*host.HTTPResponse, error) {
	timeout, err := p.requestTimeoutMilliseconds(deadline)
	if err != nil {
		return nil, err
	}
	response, err := p.send(host.HTTPRequest{
		Method:    "GET",
		URL:       requestURL,
		Headers:   musixmatchHeaders(),
		TimeoutMs: timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("request Musixmatch %s: %w", label, err)
	}
	if response == nil {
		return nil, fmt.Errorf("Musixmatch %s returned no response", label)
	}
	return response, nil
}

func musixmatchHeaders() map[string]string {
	return map[string]string{
		"Accept":     "application/json",
		"User-Agent": musixmatchUserAgent,
		"Cookie":     "AWSELB=0; AWSELBCORS=0",
	}
}

func randomMusixmatchNonce() string {
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}

func musixmatchSearchURL(track lyrics.TrackInfo, token string) (string, bool) {
	title := strings.TrimSpace(track.Title)
	artist := trackArtist(track)
	if title == "" || artist == "" {
		return "", false
	}
	query := url.Values{
		"q_track":        {title},
		"q_artist":       {artist},
		"page_size":      {"5"},
		"s_track_rating": {"desc"},
		"usertoken":      {token},
		"format":         {"json"},
		"app_id":         {musixmatchAppID},
	}
	if duration := int64(math.Round(float64(track.Duration))); duration > 0 {
		query.Set("q_duration", strconv.FormatInt(duration, 10))
	}
	return musixmatchEndpoint + "/track.search?" + query.Encode(), true
}

func musixmatchMacroURL(trackID, token string) (string, bool) {
	if trackID == "" || token == "" {
		return "", false
	}
	query := url.Values{
		"namespace":                       {"lyrics_richsynched"},
		"optional_calls":                  {"track.richsync"},
		"subtitle_format":                 {"lrc"},
		"track_id":                        {trackID},
		"f_subtitle_length_max_deviation": {"40"},
		"usertoken":                       {token},
		"format":                          {"json"},
		"app_id":                          {musixmatchAppID},
	}
	return musixmatchEndpoint + "/macro.subtitles.get?" + query.Encode(), true
}

// pickMusixmatchTrack selects the duration-tightest candidate within 2 s.
func pickMusixmatchTrack(candidates []musixmatchTrackEntry, track lyrics.TrackInfo) (int64, bool) {
	target := int64(math.Round(float64(track.Duration)))
	var best int64
	bestDelta := int64(math.MaxInt64)
	found := false
	for _, candidate := range candidates {
		id := int64(candidate.Track.TrackID)
		duration := candidate.Track.Duration
		delta := int64(math.Abs(float64(int64(duration) - target)))
		if id <= 0 {
			continue
		}
		if delta > 2 {
			continue
		}
		if delta < bestDelta || !found {
			best, bestDelta, found = id, delta, true
		}
	}
	return best, found
}

type musixmatchEnvelope struct {
	Message struct {
		Header struct {
			StatusCode int    `json:"status_code"`
			Hint       string `json:"hint"`
		} `json:"header"`
		Body musixmatchBody `json:"body"`
	} `json:"message"`
}

type musixmatchTokenEnvelope struct {
	Message struct {
		Header struct {
			StatusCode int `json:"status_code"`
		} `json:"header"`
		Body struct {
			UserToken string `json:"user_token"`
		} `json:"body"`
	} `json:"message"`
}

type musixmatchBody struct {
	TrackList  []musixmatchTrackEntry         `json:"track_list"`
	MacroCalls map[string]musixmatchMacroBody `json:"macro_calls"`
}

type musixmatchTrackEntry struct {
	Track struct {
		TrackID  int64 `json:"track_id"`
		Duration int64 `json:"track_duration"`
	} `json:"track"`
}

type musixmatchMacroBody struct {
	Body struct {
		RichsyncBody string          `json:"richsync_body"`
		LyricsBody   string          `json:"lyrics_body"`
		SubtitleList []subtitleEntry `json:"subtitle_list"`
	} `json:"body"`
}

type subtitleEntry struct {
	SubtitleBody string `json:"subtitle_body"`
}
