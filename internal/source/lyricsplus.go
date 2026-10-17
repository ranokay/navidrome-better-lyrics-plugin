package source

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
)

const (
	lyricsPlusEndpoint     = "https://lyricsplus.binimum.org"
	lyricsPlusMirrorOne    = "https://lyricsplus.atomix.one"
	lyricsPlusSourceOrder  = "apple,lyricsplus,musixmatch"
	lyricsPlusProviderName = "LyricsPlus"
)

// fetchLyricsPlus resolves Apple-style word-timed lyrics through
// the LyricsPlus community mirrors (no key required). The `source` parameter
// holds the backend priority: Apple Music timing is the syllable-grade store
// the mirrors back onto. Mirror rotation only proceeds when the primary fails
// to carry the response; a clean miss is authoritative everywhere.
func fetchLyricsPlus(rt Runtime, track lyrics.TrackInfo, deadline time.Time) ([]lyric.Candidate, Health, error) {
	title := strings.TrimSpace(track.Title)
	artist := lyric.TrackArtist(track)
	if title == "" || artist == "" {
		return nil, Healthy, nil
	}

	query := url.Values{
		"title":    {title},
		"artist":   {artist},
		"duration": {strconv.FormatInt(int64(math.Round(float64(track.Duration))), 10)},
		"source":   {lyricsPlusSourceOrder},
	}
	if album := strings.TrimSpace(track.Album); album != "" {
		query.Set("album", album)
	}

	var lookupErrors []error
	cooledMirrors := 0
	for i, endpoint := range []string{lyricsPlusEndpoint, lyricsPlusMirrorOne} {
		// Host-scoped cooldowns only remove the affected mirror from this
		// lookup; the healthy sibling still serves.
		if host := lyricsPlusEndpointHost(endpoint); rt.Cooldown(GlobalCooldownKey(host)) > 0 {
			cooledMirrors++
			continue
		}
		requestURL := endpoint + "/v2/lyrics/get?" + query.Encode()
		response, err := get(rt, "LyricsPlus lyrics", requestURL, rt.DefaultHeaders(), deadline)
		if err != nil {
			lookupErrors = append(lookupErrors, err)
			if len(lookupErrors) > i {
				continue
			}
			continue
		}
		switch response.StatusCode {
		case 200:
			// pass to the payload decode below
		case 404:
			// "Lyrics not found" is the mirror's own miss: the payload
			// sources share their upstream cache, so a miss here is a miss
			// generally.
			return nil, Healthy, nil
		case 429:
			// The mirrors are separate deployments: cool the THROTTLED HOST
			// down and let the next mirror serve this lookup.
			retryAfter := retryAfterDuration(response.Headers, rt.Now())
			if parsed, parseErr := url.Parse(requestURL); parseErr == nil && parsed.Host != "" {
				rt.RememberCooldown(GlobalCooldownKey(parsed.Hostname()), retryAfter)
			}
			lookupErrors = append(lookupErrors, rateLimitResponseError("LyricsPlus", response, retryAfter))
			continue
		default:
			// A mirror-level failure rotates onward; mirrors ride separate
			// deployments.
			lookupErrors = append(lookupErrors, providerResponseError("LyricsPlus", response))
			continue
		}

		var payload lyricsPlusPayload
		if err := json.Unmarshal(response.Body, &payload); err != nil {
			lookupErrors = append(lookupErrors, fmt.Errorf("decode LyricsPlus response: %w", err))
			continue
		}
		candidates := lyricsPlusCandidates(payload)
		if len(candidates) == 0 {
			// A decoded 200 payload with nothing usable is the mirror's own
			// clean miss: rotating to the next mirror would turn it into a
			// provider failure.
			return nil, Healthy, nil
		}
		return candidates, Healthy, nil
	}
	if len(lookupErrors) == 0 && cooledMirrors > 0 {
		// Every mirror is cooling down: degrade so sweeps do not mark the
		// source covered while it is simply waiting out its Retry-After.
		return nil, Degraded, &providerHTTPError{source: "LyricsPlus mirrors", status: 429}
	}
	if err := errors.Join(lookupErrors...); err != nil {
		return nil, Degraded, err
	}
	return nil, Healthy, nil
}

// lyricsPlusEndpointHost extracts the hostname of a mirror endpoint.
func lyricsPlusEndpointHost(endpoint string) string {
	if parsed, err := url.Parse(endpoint); err == nil {
		return parsed.Hostname()
	}
	return endpoint
}

type lyricsPlusPayload struct {
	Type     string `json:"type"`
	Metadata struct {
		Source   string `json:"source"`
		Language string `json:"language"`
	} `json:"metadata"`
	Lyrics []lyricsPlusLine `json:"lyrics"`
}

type lyricsPlusLine struct {
	Time     int64            `json:"time"`
	Duration int64            `json:"duration"`
	Text     string           `json:"text"`
	Syllabus []lyricsPlusWord `json:"syllabus"`
}

type lyricsPlusWord struct {
	Time     int64  `json:"time"`
	Duration int64  `json:"duration"`
	Text     string `json:"text"`
}

// lyricsPlusCandidates flattens the payload into one enhanced-LRC candidate:
// type Word payloads keep their per-token syllabus; type Line renders line
// stamps only. The source field is preserved for provenance display.
func lyricsPlusCandidates(payload lyricsPlusPayload) []lyric.Candidate {
	if len(payload.Lyrics) == 0 {
		return nil
	}
	lines := make([]lyric.TimedLine, 0, len(payload.Lyrics))
	for _, row := range payload.Lyrics {
		if row.Time < 0 || strings.TrimSpace(row.Text) == "" {
			continue
		}
		line := lyric.TimedLine{
			StartMs: row.Time,
			EndMs:   row.Time + row.Duration,
			Text:    strings.TrimSpace(row.Text),
		}
		if line.EndMs < line.StartMs {
			line.EndMs = line.StartMs
		}
		for _, word := range row.Syllabus {
			if strings.TrimSpace(word.Text) == "" || word.Duration <= 0 {
				continue
			}
			line.Words = append(line.Words, lyric.TimedWord{
				StartMs: word.Time,
				EndMs:   word.Time + word.Duration,
				Text:    word.Text,
			})
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return nil
	}
	text := lyric.EnhancedLRC(lines)
	candidate := lyric.NewCandidate(lyrics.LyricsText{Text: text}, lyricsPlusProviderName, "elrc")
	if !candidate.Present() {
		return nil
	}
	return []lyric.Candidate{candidate}
}
