package main

import (
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

const petitlyricsEndpoint = "https://p0.petitlyrics.com/api/GetPetitLyricsData.php"

const (
	petitlyricsClientAppID = "p1110417"
	petitlyricsTerminal    = "10"
)

// petitlyricsLyricsTypes mirrors the API's terminal tiers: 3 = word timings,
// 2 = line timings, 1 = plain text.
const (
	petitLyricsTypeWord  = "3"
	petitLyricsTypeLine  = "2"
	petitLyricsTypePlain = "1"
)

// fetchPetitLyricsCandidates resolves lyrics from PetitLyrics: word WSY first,
// then plain text. The line-level LSY binary decode is intentionally not
// implemented this release, so its tier falls straight through to plain.
func fetchPetitLyricsCandidates(p *lyricsProvider, track lyrics.TrackInfo, deadline time.Time) ([]lyricsCandidate, lookupHealth, error) {
	tiers := []string{petitLyricsTypeWord, petitLyricsTypeLine, petitLyricsTypePlain}
	for _, tier := range tiers {
		response, health, err := p.fetchPetitLyricsTier(track, tier, deadline)
		if err != nil {
			return nil, health, err
		}
		candidate, miss := petitLyricsCandidate(tier, response)
		if miss {
			continue
		}
		return []lyricsCandidate{candidate}, health, nil
	}
	return nil, lookupHealthy, nil
}

// petitLyricsCandidate converts one tier payload into a candidate. The bool
// result reports a clean miss so the tier loop can fall through.
func petitLyricsCandidate(tier string, payload petitlyricsPayload) (lyricsCandidate, bool) {
	data := strings.TrimSpace(payload.LyricsData)
	if data == "" {
		return lyricsCandidate{}, true
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return lyricsCandidate{}, true
	}
	content := string(raw)

	switch tier {
	case petitLyricsTypeWord:
		lines, parseErr := parsePetitWSY(raw)
		if parseErr != nil || len(lines) == 0 {
			return lyricsCandidate{}, true
		}
		candidate := newLyricsCandidate(lyrics.LyricsText{Text: enhancedLRCFromTimedLines(lines)}, "PetitLyrics", "elrc")
		if candidate.present() {
			return candidate, false
		}
		return lyricsCandidate{}, true
	case petitLyricsTypeLine:
		// LSY binary payloads carry timings only and are skipped this release;
		// plain rows (rare) fall through to the next tier.
		if strings.HasPrefix(content, "MHDR") {
			return lyricsCandidate{}, true
		}
		candidate := newLyricsCandidate(lyrics.LyricsText{Text: content}, "PetitLyrics", "lrc")
		if candidate.present() && candidate.quality > timingUnsynced {
			return candidate, false
		}
		return lyricsCandidate{}, true
	default:
		text := strings.TrimSpace(content)
		if text == "" {
			return lyricsCandidate{}, true
		}
		candidate := newLyricsCandidate(lyrics.LyricsText{Text: text}, "PetitLyrics", "plain")
		if candidate.present() {
			return candidate, false
		}
		return lyricsCandidate{}, true
	}
}

type petitlyricsPayload struct {
	LyricsType string `json:"-"`
	LyricsData string `xml:"lyricsData"`
}

// fetchPetitLyricsTier performs one form POST of the GetPetitLyricsData API.
func (p *lyricsProvider) fetchPetitLyricsTier(track lyrics.TrackInfo, tier string, deadline time.Time) (petitlyricsPayload, lookupHealth, error) {
	title := strings.TrimSpace(track.Title)
	artist := trackArtist(track)
	if title == "" || artist == "" {
		return petitlyricsPayload{}, lookupHealthy, nil
	}
	form := url.Values{
		"clientAppId":  {petitlyricsClientAppID},
		"terminalType": {petitlyricsTerminal},
		"lyricsType":   {tier},
		"key_title":    {title},
		"key_artist":   {artist},
	}
	if album := strings.TrimSpace(track.Album); album != "" {
		form.Set("key_album", album)
	}
	response, err := p.postForm("PetitLyrics", petitlyricsEndpoint, form, petitLyricsHeaders(), deadline)
	if err != nil {
		return petitlyricsPayload{}, lookupDegraded, err
	}

	switch response.StatusCode {
	case 200:
		// fall to the XML parsing below
	case 404:
		return petitlyricsPayload{}, lookupHealthy, nil
	case 429:
		retryAfter := retryAfterDuration(response.Headers, p.now())
		p.rememberCooldown(sourceGlobalCooldownKey("petitlyrics"), retryAfter)
		return petitlyricsPayload{}, lookupDegraded, rateLimitResponseError("PetitLyrics", response, retryAfter)
	default:
		return petitlyricsPayload{}, lookupDegraded, providerResponseError("PetitLyrics", response)
	}

	var envelope petitlyricsEnvelope
	if err := xml.Unmarshal(response.Body, &envelope); err != nil {
		return petitlyricsPayload{}, lookupDegraded, fmt.Errorf("decode PetitLyrics response: %w", err)
	}
	if strings.TrimSpace(envelope.Status) == "" || envelope.Status != "00000000" {
		return petitlyricsPayload{}, lookupHealthy, nil
	}
	if len(envelope.Songs.Songs) == 0 {
		return petitlyricsPayload{}, lookupHealthy, nil
	}
	return petitlyricsPayload{LyricsData: envelope.Songs.Songs[0].LyricsData}, lookupHealthy, nil
}

type petitlyricsEnvelope struct {
	Status string          `xml:"status"`
	Songs  petitlyricsSong `xml:"songs"`
}

type petitlyricsSong struct {
	Songs []petitlyricsSongEntry `xml:"song"`
}

type petitlyricsSongEntry struct {
	LyricsData string `xml:"lyricsData"`
}

func petitLyricsHeaders() map[string]string {
	return map[string]string{
		"Accept":     "text/xml, application/xml",
		"User-Agent": pluginUserAgent(),
	}
}

const appleLyricallyEndpoint = "https://itunes.apple.com/search"
const lyricallyMirrorEndpoint = "https://lyrics.paxsenix.org/apple-music/lyrics"

// fetchLyricallyLyricsCandidates resolves Apple-sourced lyrics without any
// Apple credential: iTunes search picks the duration-tightest track, then the
// Lyrically mirror returns word-timed enhanced LRC, line TTML, or plain text.
func fetchLyricallyLyricsCandidates(p *lyricsProvider, track lyrics.TrackInfo, deadline time.Time) ([]lyricsCandidate, lookupHealth, error) {
	title := strings.TrimSpace(track.Title)
	artist := trackArtist(track)
	if title == "" || artist == "" {
		return nil, lookupHealthy, nil
	}

	searchURL := appleLyricallyEndpoint + "?" + url.Values{
		"term":   {title + " " + artist},
		"media":  {"music"},
		"entity": {"song"},
		"limit":  {"5"},
	}.Encode()
	response, err := p.sendRequest("Apple search", searchURL, lyricallyHeaders(), deadline)
	if err != nil {
		return nil, lookupDegraded, err
	}
	switch response.StatusCode {
	case 200:
		// pass to the payload decode
	case 404:
		return nil, lookupHealthy, nil
	default:
		return nil, lookupDegraded, providerResponseError("Apple search", response)
	}

	var search struct {
		Results []struct {
			TrackID         int64  `json:"trackId"`
			TrackName       string `json:"trackName"`
			TrackTimeMillis int64  `json:"trackTimeMillis"`
		} `json:"results"`
	}
	if err := json.Unmarshal(response.Body, &search); err != nil {
		return nil, lookupDegraded, fmt.Errorf("decode Apple search response: %w", err)
	}
	target := int64(math.Round(float64(track.Duration) * 1000.0))
	var trackID int64
	bestDelta := int64(math.MaxInt64)
	found := false
	for _, result := range search.Results {
		if result.TrackID <= 0 {
			continue
		}
		if delta := int64(math.Abs(float64(result.TrackTimeMillis - target))); delta <= 2000 && delta < bestDelta {
			trackID, bestDelta, found = result.TrackID, delta, true
		}
	}
	if !found {
		return nil, lookupHealthy, nil
	}

	mirrorURL := lyricallyMirrorEndpoint + "?id=" + strconv.FormatInt(trackID, 10)
	response, err = p.sendRequest("Lyrically lyrics", mirrorURL, lyricallyHeaders(), deadline)
	if err != nil {
		return nil, lookupDegraded, err
	}
	switch response.StatusCode {
	case 200:
		// pass to the payload decode
	case 404:
		return nil, lookupHealthy, nil
	case 429:
		retryAfter := retryAfterDuration(response.Headers, p.now())
		p.rememberCooldown(sourceGlobalCooldownKey("apple-lyrically"), retryAfter)
		return nil, lookupDegraded, rateLimitResponseError("Lyrically lyrics", response, retryAfter)
	default:
		return nil, lookupDegraded, providerResponseError("Lyrically lyrics", response)
	}

	var mirror struct {
		ELRC        string `json:"elrc"`
		TTMLContent string `json:"ttmlContent"`
		Plain       string `json:"plain"`
	}
	if err := json.Unmarshal(response.Body, &mirror); err != nil {
		return nil, lookupDegraded, fmt.Errorf("decode Lyrically lyrics response: %w", err)
	}

	if elrc := strings.TrimSpace(mirror.ELRC); elrc != "" {
		candidate := newLyricsCandidate(lyrics.LyricsText{Text: elrc}, "Apple Music (Lyrically)", "elrc")
		if candidate.present() {
			return []lyricsCandidate{candidate}, lookupHealthy, nil
		}
	}
	if ttml := strings.TrimSpace(mirror.TTMLContent); ttml != "" {
		candidate := newLyricsCandidate(lyrics.LyricsText{Text: ttml}, "Apple Music (Lyrically)", "ttml")
		if candidate.present() {
			return []lyricsCandidate{candidate}, lookupHealthy, nil
		}
	}
	if plain := strings.TrimSpace(mirror.Plain); plain != "" {
		candidate := newLyricsCandidate(lyrics.LyricsText{Text: plain}, "Apple Music (Lyrically)", "plain")
		if candidate.present() {
			return []lyricsCandidate{candidate}, lookupHealthy, nil
		}
	}
	return nil, lookupHealthy, nil
}

func lyricallyHeaders() map[string]string {
	return map[string]string{
		"Accept":     "application/json",
		"User-Agent": pluginUserAgent(),
	}
}

func hostXMLResponse(body string) *host.HTTPResponse {
	return &host.HTTPResponse{StatusCode: 200, Body: []byte(body)}
}

func base64Encode(value string) string {
	return base64.StdEncoding.EncodeToString([]byte(value))
}
