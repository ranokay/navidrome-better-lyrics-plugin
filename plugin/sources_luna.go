package main

import (
	"bytes"
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
		return nil, lookupDegraded, p.rateLimitFailure("luna", "Luna search", response)
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
		return nil, lookupDegraded, p.rateLimitFailure("luna", "Luna track", response)
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
		if parseErr != nil {
			// Nonempty KRC that no longer parses is a provider failure, not
			// cacheable negative coverage.
			return nil, lookupDegraded, fmt.Errorf("parse Luna KRC payload: %w", parseErr)
		}
		if len(lines) > 0 {
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
		// otherwise valid lyrics (unknown dialects must not be dropped).
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
		Track lunaSearchTrack `json:"track"`
	} `json:"entity"`
}

type lunaSearchArtist struct {
	Name string `json:"name"`
}

// lunaTrackID accepts the quoted ids the live Luna search payload ships (they
// exceed numeric precision anyway) so string ids do not abort the decode.
type lunaTrackID string

func (id *lunaTrackID) UnmarshalJSON(data []byte) error {
	text := bytes.TrimSpace(data)
	if len(text) > 0 && text[0] == '"' {
		var quoted string
		if err := json.Unmarshal(text, &quoted); err != nil {
			return err
		}
		*id = lunaTrackID(strings.TrimSpace(quoted))
		return nil
	}
	// json.Number keeps the original digits: unmarshalling through `any`
	// would round ids above 2^53 onto a neighboring float64.
	var digits json.Number
	if err := json.Unmarshal(text, &digits); err != nil {
		return err
	}
	*id = lunaTrackID(strings.TrimSpace(digits.String()))
	return nil
}

func pickLunaTrack(groups []lunaResultGroup, track lyrics.TrackInfo) (string, bool) {
	target := int64(float64(track.Duration)*1000.0 + 0.5)
	titleNorm := strings.ToLower(onlyLettersAndDigits(track.Title))
	for _, group := range groups {
		for _, datum := range group.Data {
			trackInfo := datum.Entity.Track
			if trackInfo.ID == "" {
				continue
			}
			// The fuzzy hub search can return unrelated rows within the
			// duration window: require normalized metadata before the
			// duration gate. Collapsed containment tolerates UGC rows that
			// concatenate name and artist without separators
			// ("liveforeverOasis"), as the live fixture does. A title without
			// alphanumerics verifies nothing and matches no row.
			if titleNorm == "" || !strings.Contains(strings.ToLower(onlyLettersAndDigits(trackInfo.Name)), titleNorm) {
				continue
			}
			if !lunaArtistsMatch(trackInfo, track) {
				continue
			}
			if track.Duration > 0 {
				// The duration gate applies only when the request carries one.
				delta := trackInfo.Duration - target
				if delta <= -2000 || delta >= 2000 {
					continue
				}
			}
			return string(trackInfo.ID), true
		}
	}
	return "", false
}

type lunaSearchTrack struct {
	ID       lunaTrackID        `json:"id"`
	Name     string             `json:"name"`
	Artists  []lunaSearchArtist `json:"artists"`
	Duration int64              `json:"duration"` // ms
}

// lunaArtistsMatch accepts the requested artist from any artists-array entry
// with COMPLETE normalized-name equality, or from a narrowly scoped UGC
// fallback: hub rows that concatenate title+artist without separators
// ("liveforeverOasis") when the remainder after the collapsed title is
// exactly the artist. An artist without alphanumerics verifies nothing.
func lunaArtistsMatch(trackInfo lunaSearchTrack, track lyrics.TrackInfo) bool {
	artistNorm := strings.ToLower(onlyLettersAndDigits(trackArtist(track)))
	if artistNorm == "" {
		return false
	}
	for _, candidateArtist := range trackInfo.Artists {
		if strings.ToLower(onlyLettersAndDigits(candidateArtist.Name)) == artistNorm {
			return true
		}
	}
	nameNorm := strings.ToLower(onlyLettersAndDigits(trackInfo.Name))
	titleNorm := strings.ToLower(onlyLettersAndDigits(track.Title))
	if titleNorm != "" && strings.HasPrefix(nameNorm, titleNorm) {
		return strings.TrimPrefix(nameNorm, titleNorm) == artistNorm
	}
	return false
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
	// A 20-hex-digit device id with the millisecond timestamp appended, as the
	// Android client ships it.
	return randomHex(10) + fmt.Sprintf("%04x", uint16(now.UnixNano()))
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
