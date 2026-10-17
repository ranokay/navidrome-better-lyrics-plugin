package main

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
)

const (
	lyricsPlusEndpoint     = "https://lyricsplus.binimum.org"
	lyricsPlusMirrorOne    = "https://lyricsplus.atomix.one"
	lyricsPlusSourceOrder  = "apple,lyricsplus,musixmatch"
	lyricsPlusProviderName = "LyricsPlus"
)

// fetchLyricsPlusLyricsCandidates resolves Apple-style word-timed lyrics through
// the LyricsPlus community mirrors (no key required). The `source` parameter
// holds the backend priority: Apple Music timing is the syllable-grade store
// the mirrors back onto. Mirror rotation only proceeds when the primary fails
// to carry the response; a clean miss is authoritative everywhere.
func fetchLyricsPlusLyricsCandidates(p *lyricsProvider, track lyrics.TrackInfo, deadline time.Time) ([]lyricsCandidate, lookupHealth, error) {
	title := strings.TrimSpace(track.Title)
	artist := trackArtist(track)
	if title == "" || artist == "" {
		return nil, lookupHealthy, nil
	}

	query := url.Values{
		"title":    {title},
		"artist":   {artist},
		"duration": {strconv.FormatInt(int64(math.Round(float64(track.Duration)+0.5)), 10)},
		"source":   {lyricsPlusSourceOrder},
	}
	if album := strings.TrimSpace(track.Album); album != "" {
		query.Set("album", album)
	}

	var lookupErrors []error
	for i, endpoint := range []string{lyricsPlusEndpoint, lyricsPlusMirrorOne} {
		requestURL := endpoint + "/v2/lyrics/get?" + query.Encode()
		response, err := p.sendRequest("LyricsPlus lyrics", requestURL, lyricsPlusHeaders(), deadline)
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
			return nil, lookupHealthy, nil
		case 429:
			retryAfter := retryAfterDuration(response.Headers, p.now())
			p.rememberCooldown(sourceGlobalCooldownKey("lyricsplus"), retryAfter)
			return nil, lookupDegraded, rateLimitResponseError("LyricsPlus", response, retryAfter)
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
		if len(candidates) > 0 {
			return candidates, lookupHealthy, nil
		}
	}
	if err := errors.Join(lookupErrors...); err != nil {
		return nil, lookupDegraded, err
	}
	return nil, lookupHealthy, nil
}

func lyricsPlusHeaders() map[string]string {
	return map[string]string{
		"Accept":     "application/json",
		"User-Agent": pluginUserAgent(),
	}
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
func lyricsPlusCandidates(payload lyricsPlusPayload) []lyricsCandidate {
	if len(payload.Lyrics) == 0 {
		return nil
	}
	lines := make([]timedLine, 0, len(payload.Lyrics))
	for _, row := range payload.Lyrics {
		if row.Time < 0 || strings.TrimSpace(row.Text) == "" {
			continue
		}
		line := timedLine{
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
			line.Words = append(line.Words, timedWord{
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
	text := enhancedLRCFromTimedLines(lines)
	candidate := newLyricsCandidate(lyrics.LyricsText{Text: text}, lyricsPlusProviderName, "elrc")
	if !candidate.present() {
		return nil
	}
	return []lyricsCandidate{candidate}
}
