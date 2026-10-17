package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	token, err := p.musixmatchToken(deadline)
	if err != nil {
		// A mint challenge means this lookup skips Musixmatch quietly and the
		// next lookup can try again. Operational failures (transport, timeout,
		// HTTP 5xx) must stay degraded so sweep coverage cannot mark the
		// source done while minting is actually down (Codex).
		if errors.Is(err, errMusixmatchTokenChallenge) {
			// The challenge answer can clear itself next lookup; skip quietly
			// without letting the walkers persist it as a miss (Greptile).
			return nil, lookupTemporarilySkipped, nil
		}
		return nil, lookupDegraded, err
	}

	searchURL, ok := musixmatchSearchURL(track, token)
	if !ok {
		return nil, lookupHealthy, nil
	}
	searchResponse, err := p.musixmatchRequest("track.search", searchURL, deadline)
	if err != nil {
		return nil, lookupDegraded, err
	}
	switch searchResponse.StatusCode {
	case 200:
		// pass to the envelope decode
	case 401:
		// The cached token went stale: drop it and try one fresh mint next
		// lookup. A temporary skip must not persist as a clean miss, or this
		// source stays suppressed when its token simply aged out (Greptile).
		p.noteMusixmatchTokenRejection()
		return nil, lookupTemporarilySkipped, nil
	case 404:
		return nil, lookupHealthy, nil
	case 429:
		retryAfter := retryAfterDuration(searchResponse.Headers, p.now())
		p.rememberCooldown(sourceGlobalCooldownKey("musixmatch"), retryAfter)
		return nil, lookupDegraded, rateLimitResponseError("Musixmatch search", searchResponse, retryAfter)
	default:
		return nil, lookupDegraded, providerResponseError("Musixmatch search", searchResponse)
	}
	if searchResponse.StatusCode == 401 {
		// The cached token went stale: drop it and try one fresh mint next lookup.
		p.noteMusixmatchTokenRejection()
		return nil, lookupHealthy, nil
	}

	var searchEnvelope musixmatchEnvelope
	if err := json.Unmarshal(searchResponse.Body, &searchEnvelope); err != nil {
		return nil, lookupDegraded, fmt.Errorf("decode Musixmatch search response: %w", err)
	}
	if musixmatchTokenRejected(searchEnvelope.Message.Header) {
		// Musixmatch sometimes reports the rejected token only in the JSON
		// envelope; drop it so the next lookup remints instead of missing for up
		// to ten days. A temporary skip must not persist as a missed song.
		p.noteMusixmatchTokenRejection()
		return nil, lookupTemporarilySkipped, nil
	}
	if health, err := p.musixmatchEnvelopeStatusFailure("Musixmatch track.search", searchEnvelope.Message.Header.StatusCode); err != nil {
		return nil, health, err
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
	if musixmatchTokenRejected(macroEnvelope.Message.Header) {
		p.noteMusixmatchTokenRejection()
		return nil, lookupTemporarilySkipped, nil
	}
	if health, err := p.musixmatchEnvelopeStatusFailure("Musixmatch macro.subtitles.get", macroEnvelope.Message.Header.StatusCode); err != nil {
		return nil, health, err
	}
	var macroFailures []error
	for name, macro := range macroEnvelope.Message.Body.MacroCalls {
		// The richsync/subtitles responses can reject the token while the outer
		// envelope still says HTTP 200 (Codex: stale tokens must be dropped).
		if musixmatchTokenRejected(macro.Header) {
			p.noteMusixmatchTokenRejection()
			return nil, lookupTemporarilySkipped, nil
		}
		if _, err := p.musixmatchEnvelopeStatusFailure("Musixmatch macro payload", macro.Header.StatusCode); err != nil {
			// One failed optional macro must not discard the usable payloads
			// the same response carries (Codex): record it and let the
			// candidate ranking below decide.
			macroFailures = append(macroFailures, fmt.Errorf("musixmatch macro %s: %w", name, err))
			continue
		}
	}
	candidates, health, err := musixmatchCandidate(macroEnvelope.Message.Body.MacroCalls)
	if len(candidates) > 0 {
		return candidates, health, err
	}
	if len(macroFailures) > 0 {
		return nil, lookupDegraded, errors.Join(macroFailures...)
	}
	return candidates, health, err
}

// musixmatchTokenRejected reports a JSON-envelope rejection of the current token:
// expired tokens sometimes arrive only in the header (HTTP 200 outside).
// musixmatchEnvelopeStatusFailure classifies a JSON header status that the HTTP
// layer already answered 200 for. Token rejections are handled by the caller and
// plain no-match (404) keeps the quiet miss contract; an envelope rate limit
// activates the provider cooldown, and any other non-success status is a
// degraded failure instead of a healthy miss (Codex/CodeRabbit).
func (p *lyricsProvider) musixmatchEnvelopeStatusFailure(source string, status int) (lookupHealth, error) {
	switch status {
	case 0, 200, 201, 204, 404:
		return lookupHealthy, nil
	case 429:
		p.rememberCooldown(sourceGlobalCooldownKey("musixmatch"), defaultRateLimitCooldown)
		return lookupDegraded, &providerHTTPError{source: source, status: 429, globalRateLimit: true}
	default:
		return lookupDegraded, &providerHTTPError{source: source, status: int32(status)}
	}
}

// noteMusixmatchTokenRejection bounds retry pressure when a rejection hit:
// anonymous tokens are dropped so the next lookup remints, while a configured
// token — the only credential available — records a short provider cooldown
// so foreground lookups stop hammering the rejected setting (Codex). The
// cooldown lapses quickly, and changing the setting re-authorizes the source
// as soon as it does.
func (p *lyricsProvider) noteMusixmatchTokenRejection() {
	config := readProviderConfig(p.configLookup)
	if config.MusixmatchUserToken != "" {
		p.rememberCooldown(sourceGlobalCooldownKey("musixmatch"), defaultRateLimitCooldown)
		return
	}
	_ = p.cache.SetWithTTL(musixmatchTokenKey, []byte(""), 1)
}

func musixmatchTokenRejected(header struct {
	StatusCode int    `json:"status_code"`
	Hint       string `json:"hint"`
}) bool {
	return header.StatusCode == 401 || header.StatusCode == 403 ||
		strings.EqualFold(strings.TrimSpace(header.Hint), "captcha")
}

// musixmatchCandidate ranks the available payloads: richsync (word) before LRC
// subtitles (line) before plain lyrics.
func musixmatchCandidate(macroCalls map[string]musixmatchMacroBody) ([]lyricsCandidate, lookupHealth, error) {
	var richsyncParseErr error
	if richsyncBody := macroCalls["track.richsync.get"].Body.RichsyncBody; strings.TrimSpace(richsyncBody) != "" {
		if lines, err := parseRichsync(richsyncBody); err == nil && len(lines) > 0 {
			text := lyrics.LyricsText{Text: enhancedLRCFromTimedLines(lines)}
			if candidate := newLyricsCandidate(text, musixmatchProviderName, "elrc"); candidate.present() {
				return []lyricsCandidate{candidate}, lookupHealthy, nil
			}
		} else {
			richsyncParseErr = err
		}
	}
	if subtitles := macroCalls["track.subtitles.get"].Body.SubtitleList; len(subtitles) > 0 {
		// Restricted or unavailable subtitles arrive with an empty body;
		// serving them would cache a blank lyric ahead of usable fallbacks
		// (Codex).
		if body := strings.TrimSpace(subtitles[0].SubtitleBody); body != "" {
			if candidate := newLyricsCandidate(lyrics.LyricsText{Text: body}, musixmatchProviderName, "lrc"); candidate.present() {
				return []lyricsCandidate{candidate}, lookupHealthy, nil
			}
		}
	}
	if plainLyrics := macroCalls["track.lyrics.get"].Body.LyricsBody; strings.TrimSpace(plainLyrics) != "" {
		text := lyrics.LyricsText{Text: plainLyrics}
		if candidate := newLyricsCandidate(text, musixmatchProviderName, "plain"); candidate.present() {
			return []lyricsCandidate{candidate}, lookupHealthy, nil
		}
	}
	if richsyncParseErr != nil {
		// Malformed richsync with no usable fallback is a provider failure,
		// not cacheable coverage (Codex round 13).
		return nil, lookupDegraded, fmt.Errorf("parse Musixmatch richsync: %w", richsyncParseErr)
	}
	return nil, lookupHealthy, nil
}

// musixmatchToken returns the configured admin token when present, otherwise the
// minted-and-cached anonymous token (minting once per attempt, dropping the cache
// entry when the API challenges it). The caller's deadline governs the network
// mint so a late-hop mint cannot blow the lookup budget.
func (p *lyricsProvider) musixmatchToken(deadline time.Time) (string, error) {
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
	response, err := p.musixmatchRequest("token.get", requestURL, deadline)
	if err != nil {
		return "", err
	}
	if response.StatusCode == 401 {
		return "", errMusixmatchTokenChallenge
	}
	if response.StatusCode == 429 {
		// Rate-limited minting must cool the provider down; hammering token.get
		// while it throttles only extends the block (Greptile).
		retryAfter := retryAfterDuration(response.Headers, p.now())
		p.rememberCooldown(sourceGlobalCooldownKey("musixmatch"), retryAfter)
		return "", rateLimitResponseError("Musixmatch token", response, retryAfter)
	}
	if response.StatusCode != 200 && response.StatusCode != 201 {
		return "", providerResponseError("Musixmatch token", response)
	}

	var envelope musixmatchTokenEnvelope
	if err := json.Unmarshal(response.Body, &envelope); err != nil {
		return "", fmt.Errorf("decode Musixmatch token response: %w", err)
	}
	if musixmatchTokenRejected(envelope.Message.Header) {
		return "", errMusixmatchTokenChallenge
	}
	if _, err := p.musixmatchEnvelopeStatusFailure("Musixmatch token.get", envelope.Message.Header.StatusCode); err != nil {
		return "", err
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
	titleTokens := normalizedLyricTokens(track.Title)
	artistTokens := normalizedLyricTokens(trackArtist(track))
	var best int64
	bestDelta := int64(math.MaxInt64)
	found := false
	for _, candidate := range candidates {
		id := int64(candidate.Track.TrackID)
		if id <= 0 {
			continue
		}
		// The fuzzy track.search rows only qualify when their identity
		// matches; duration then ranks the qualifying rows (Devin).
		if !lyricTokensMatch(normalizedLyricTokens(candidate.Track.TrackName), titleTokens) {
			continue
		}
		if !musixmatchArtistsMatch(candidate.Track.ArtistName, artistTokens) {
			continue
		}
		if track.Duration <= 0 {
			// The duration gate applies only when the request carries one:
			// the search already omitted q_duration, so the broader result
			// set must stay usable (Codex).
			if !found {
				best, bestDelta, found = id, 0, true
			}
			continue
		}
		duration := candidate.Track.Duration
		delta := int64(math.Abs(float64(int64(duration) - target)))
		if delta > 2 {
			continue
		}
		if delta < bestDelta || !found {
			best, bestDelta, found = id, delta, true
		}
	}
	return best, found
}

// musixmatchArtistsMatch accepts the row when any collaborating artist name
// matches the requested artist completely.
func musixmatchArtistsMatch(artistName string, wanted []string) bool {
	if len(wanted) == 0 {
		return true
	}
	return artistTokensMatch(artistName, strings.Join(wanted, " "))
}

// errMusixmatchTokenChallenge marks the anti-bot captcha/rejection the desktop
// endpoint answers with when anonymous minting is blocked; unlike transport or
// HTTP errors it is a deliberate skip, not an operational failure.
var errMusixmatchTokenChallenge = errors.New("musixmatch token challenge")

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
			StatusCode int    `json:"status_code"`
			Hint       string `json:"hint"`
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
		TrackID    int64  `json:"track_id"`
		TrackName  string `json:"track_name"`
		ArtistName string `json:"artist_name"`
		Duration   int64  `json:"track_length"` // seconds (live search wire name)
	} `json:"track"`
}

type musixmatchMacroBody struct {
	Header struct {
		StatusCode int    `json:"status_code"`
		Hint       string `json:"hint"`
	} `json:"header"`
	Body struct {
		RichsyncBody string          `json:"richsync_body"`
		LyricsBody   string          `json:"lyrics_body"`
		SubtitleList []subtitleEntry `json:"subtitle_list"`
	} `json:"body"`
}

type subtitleEntry struct {
	SubtitleBody string `json:"subtitle_body"`
}
