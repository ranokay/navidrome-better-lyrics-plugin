package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

const (
	lunaSearchEndpoint = "https://api.qishui.com/luna/search/track"
	lunaDetailEndpoint = "https://beta-luna.douyin.com/luna/h5/seo_track"
)

// fetchLunaLyricsCandidates resolves lyrics from Soda/Luna (汽水音乐): a spoofed
// Android-style search, then a track detail whose `lyric` payload is plaintext KRC
// (word) or LRC (line).
func fetchLunaLyricsCandidates(p *lyricsProvider, track lyrics.TrackInfo, deadline time.Time) ([]lyricsCandidate, lookupHealth, error) {
	searchURL, ok := lunaSearchURL(track, p.now())
	if !ok {
		return nil, lookupHealthy, nil
	}
	response, err := p.sendRequest("Luna search", searchURL, lunaSearchHeaders(), deadline)
	if err != nil {
		return nil, lookupDegraded, err
	}
	switch response.StatusCode {
	case 200:
		// pass to the envelope decode
	case 404:
		return nil, lookupHealthy, nil
	case 429:
		retryAfter := retryAfterDuration(response.Headers, p.now())
		p.rememberCooldown(sourceGlobalCooldownKey("luna"), retryAfter)
		return nil, lookupDegraded, rateLimitResponseError("Luna search", response, retryAfter)
	default:
		return nil, lookupDegraded, providerResponseError("Luna search", response)
	}

	var search lunaSearchEnvelope
	if err := json.Unmarshal(response.Body, &search); err != nil {
		return nil, lookupDegraded, fmt.Errorf("decode Luna search response: %w", err)
	}
	trackID, found := pickLunaTrack(search.ResultGroups, track)
	if !found {
		return nil, lookupHealthy, nil
	}

	query := url.Values{"track_id": {trackID}, "device_platform": {"web"}}
	response, err = p.sendRequest("Luna track", lunaDetailEndpoint+"?"+query.Encode(), lunaDetailHeaders(), deadline)
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
		p.rememberCooldown(sourceGlobalCooldownKey("luna"), retryAfter)
		return nil, lookupDegraded, rateLimitResponseError("Luna track", response, retryAfter)
	default:
		return nil, lookupDegraded, providerResponseError("Luna track", response)
	}

	var detail lunaDetailEnvelope
	if err := json.Unmarshal(response.Body, &detail); err != nil {
		return nil, lookupDegraded, fmt.Errorf("decode Luna track response: %w", err)
	}

	content := strings.TrimSpace(detail.Lyric.Content)
	if content == "" {
		return nil, lookupHealthy, nil
	}
	switch strings.ToLower(strings.TrimSpace(detail.Lyric.Type)) {
	case "krc":
		lines, parseErr := parseKFamilyLyrics(wordSyncKRC, content)
		if parseErr == nil && len(lines) > 0 {
			candidate := newLyricsCandidate(lyrics.LyricsText{Text: enhancedLRCFromTimedLines(lines)}, "Soda (Luna)", "elrc")
			if candidate.present() {
				return []lyricsCandidate{candidate}, lookupHealthy, nil
			}
		}
		return nil, lookupHealthy, nil
	case "lrc":
		candidate := newLyricsCandidate(lyrics.LyricsText{Text: content}, "Soda (Luna)", "lrc")
		if candidate.present() {
			return []lyricsCandidate{candidate}, lookupHealthy, nil
		}
		return nil, lookupHealthy, nil
	default:
		// Unknown payload type: return whatever is usable rather than discard
		// otherwise valid lyrics (Codex: unknown dialects must not be dropped).
		candidate := newLyricsCandidate(lyrics.LyricsText{Text: content}, "Soda (Luna)", "plain")
		if candidate.present() {
			return []lyricsCandidate{candidate}, lookupHealthy, nil
		}
		return nil, lookupHealthy, nil
	}
}

type lunaSearchEnvelope struct {
	ResultGroups []lunaResultGroup `json:"result_groups"`
}

type lunaResultGroup struct {
	Data []lunaGroupDatum `json:"data"`
}

type lunaGroupDatum struct {
	Entity struct {
		Track struct {
			ID       lunaTrackID `json:"id"`
			Duration int64       `json:"duration"` // ms
		} `json:"track"`
	} `json:"entity"`
}

// lunaTrackID accepts the quoted ids the live Luna search payload ships (they
// exceed numeric precision anyway) so string ids do not abort the decode.
type lunaTrackID string

func (id *lunaTrackID) UnmarshalJSON(data []byte) error {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	switch value := raw.(type) {
	case string:
		*id = lunaTrackID(strings.TrimSpace(value))
	case float64:
		*id = lunaTrackID(strconv.FormatInt(int64(value), 10))
	}
	return nil
}

func pickLunaTrack(groups []lunaResultGroup, track lyrics.TrackInfo) (string, bool) {
	target := int64(float64(track.Duration)*1000.0 + 0.5)
	for _, group := range groups {
		for _, datum := range group.Data {
			trackInfo := datum.Entity.Track
			if trackInfo.ID == "" {
				continue
			}
			if delta := trackInfo.Duration - target; delta > -2000 && delta < 2000 {
				return string(trackInfo.ID), true
			}
		}
	}
	return "", false
}

type lunaDetailEnvelope struct {
	Lyric struct {
		Content string `json:"content"`
		Type    string `json:"type"`
	} `json:"lyric"`
}

func lunaSearchURL(track lyrics.TrackInfo, now time.Time) (string, bool) {
	title := strings.TrimSpace(track.Title)
	artist := trackArtist(track)
	if title == "" || artist == "" {
		return "", false
	}
	query := url.Values{
		"aid":          {"386088"},
		"app_name":     {"luna"},
		"version_code": {"100198030"},
		"package":      {"com.luna.music"},
		"device_id":    {randomLunaDeviceID(now)},
		"iid":          {randomLunaDeviceID(now)},
		"q":            {title + " " + artist},
		"cursor":       {"0"},
		"count":        {"20"},
		"_rticket":     {strconv.FormatInt(now.UnixNano()/int64(time.Millisecond), 10)},
	}
	return lunaSearchEndpoint + "?" + query.Encode(), true
}

func randomLunaDeviceID(now time.Time) string {
	raw := make([]byte, 10, 20)
	_, _ = rand.Read(raw)
	return fmt.Sprintf("%x%04x", raw, uint16(now.UnixNano()))
}

func lunaSearchHeaders() map[string]string {
	return map[string]string{
		"Accept": "application/json",
		// Luna's search gate expects its own Android client UA.
		"User-Agent": "com.luna.music/100198030 (Linux; U; Android 15) -",
	}
}

func lunaDetailHeaders() map[string]string {
	return map[string]string{
		"Accept": "application/json",
		// The detail endpoint runs through the SEO H5 route; a desktop browser UA works.
		"User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36",
	}
}

// lunaQuery deltas keep the Math import used for duration clamps.
