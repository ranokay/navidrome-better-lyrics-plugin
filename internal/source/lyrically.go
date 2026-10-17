package source

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
)

const appleLyricallyEndpoint = "https://itunes.apple.com/search"
const lyricallyMirrorEndpoint = "https://lyrics.paxsenix.org/apple-music/lyrics"

// appleLyricallyArtistMatches accepts any collaborating artist entry whose
// complete name matches the requested artist.
func appleLyricallyArtistMatches(artistName, wanted string) bool {
	return lyric.ArtistMatch(artistName, wanted)
}

// fetchLyrically resolves Apple-sourced lyrics without any
// Apple credential: iTunes search picks the duration-tightest track, then the
// Lyrically mirror returns word-timed enhanced LRC, line TTML, or plain text.
func fetchLyrically(rt Runtime, track lyrics.TrackInfo, deadline time.Time) ([]lyric.Candidate, Health, error) {
	title := strings.TrimSpace(track.Title)
	artist := lyric.TrackArtist(track)
	if title == "" || artist == "" {
		return nil, Healthy, nil
	}

	searchURL := appleLyricallyEndpoint + "?" + url.Values{
		"term":   {title + " " + artist},
		"media":  {"music"},
		"entity": {"song"},
		"limit":  {"5"},
	}.Encode()
	response, err := get(rt, "Apple search", searchURL, rt.DefaultHeaders(), deadline)
	if err != nil {
		return nil, Degraded, err
	}
	switch response.StatusCode {
	case 200:
		// pass to the payload decode
	case 404:
		return nil, Healthy, nil
	case 429:
		// Apple throttles too; persist the cooldown so repeated lookups stop
		// contacting the throttled endpoint.
		return nil, Degraded, rateLimitFailure(rt, "apple-lyrically", "Apple search", response)
	default:
		return nil, Degraded, providerResponseError("Apple search", response)
	}

	var search struct {
		Results []struct {
			TrackID         int64  `json:"trackId"`
			TrackName       string `json:"trackName"`
			ArtistName      string `json:"artistName"`
			TrackTimeMillis int64  `json:"trackTimeMillis"`
		} `json:"results"`
	}
	if err := json.Unmarshal(response.Body, &search); err != nil {
		return nil, Degraded, fmt.Errorf("decode Apple search response: %w", err)
	}
	target := int64(math.Round(float64(track.Duration) * 1000.0))
	titleTokens := lyric.NormalizedTokens(track.Title)
	var trackID int64
	bestDelta := int64(math.MaxInt64)
	found := false
	for _, result := range search.Results {
		if result.TrackID <= 0 {
			continue
		}
		// Duration alone does not identify a recording: covers and live
		// versions share runtimes, so the returned identity gates first and
		// the duration ranks the survivors.
		if !lyric.TokensMatch(lyric.NormalizedTokens(result.TrackName), titleTokens) {
			continue
		}
		if !appleLyricallyArtistMatches(result.ArtistName, lyric.TrackArtist(track)) {
			continue
		}
		if track.Duration > 0 {
			// The duration gate applies only when the request carries one.
			delta := int64(math.Abs(float64(result.TrackTimeMillis - target)))
			if delta > 2000 || delta >= bestDelta {
				continue
			}
			trackID, bestDelta, found = result.TrackID, delta, true
			continue
		}
		if !found {
			trackID, bestDelta, found = result.TrackID, 0, true
		}
	}
	if !found {
		return nil, Healthy, nil
	}

	mirrorURL := lyricallyMirrorEndpoint + "?id=" + strconv.FormatInt(trackID, 10)
	response, err = get(rt, "Lyrically lyrics", mirrorURL, rt.DefaultHeaders(), deadline)
	if err != nil {
		return nil, Degraded, err
	}
	switch response.StatusCode {
	case 200:
		// pass to the payload decode
	case 404:
		return nil, Healthy, nil
	case 429:
		return nil, Degraded, rateLimitFailure(rt, "apple-lyrically", "Lyrically lyrics", response)
	default:
		return nil, Degraded, providerResponseError("Lyrically lyrics", response)
	}

	var mirror struct {
		ELRC        string `json:"elrc"`
		TTMLContent string `json:"ttmlContent"`
		Plain       string `json:"plain"`
	}
	if err := json.Unmarshal(response.Body, &mirror); err != nil {
		return nil, Degraded, fmt.Errorf("decode Lyrically lyrics response: %w", err)
	}

	if elrc := strings.TrimSpace(mirror.ELRC); elrc != "" {
		candidate := lyric.NewCandidate(lyrics.LyricsText{Text: elrc}, "Apple Music (Lyrically)", "elrc")
		if candidate.Present() {
			return []lyric.Candidate{candidate}, Healthy, nil
		}
	}
	if ttml := strings.TrimSpace(mirror.TTMLContent); ttml != "" {
		candidate := lyric.NewCandidate(lyrics.LyricsText{Text: ttml}, "Apple Music (Lyrically)", "ttml")
		if candidate.Present() {
			return []lyric.Candidate{candidate}, Healthy, nil
		}
	}
	if plain := strings.TrimSpace(mirror.Plain); plain != "" {
		candidate := lyric.NewCandidate(lyrics.LyricsText{Text: plain}, "Apple Music (Lyrically)", "plain")
		if candidate.Present() {
			return []lyric.Candidate{candidate}, Healthy, nil
		}
	}
	return nil, Healthy, nil
}
