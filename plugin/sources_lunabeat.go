package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

const lunaBeatBase = "https://2755337087.github.io/ttml-hub"

const (
	lunaBeatIndexKey = "lunabeat:index:v1"
	lunaBeatIndexTTL = int64(24 * time.Hour / time.Second)
)

// fetchLunaBeatLyricsCandidates resolves Apple-style TTML from the LunaBeat
// community hub: a revision-gated songs index (cached gzipped in the KV store)
// plus one TTML fetch for the matched file.
func fetchLunaBeatLyricsCandidates(p *lyricsProvider, track lyrics.TrackInfo, deadline time.Time) ([]lyricsCandidate, lookupHealth, error) {
	title := strings.TrimSpace(track.Title)
	artist := trackArtist(track)
	if title == "" || artist == "" {
		return nil, lookupHealthy, nil
	}

	songs, err := p.readLunaBeatIndex(deadline)
	if err != nil {
		// Index acquisition is operational: a timeout, HTTP error, malformed
		// payload, or oversized index must stay a degraded lookup so sweeps
		// cannot mark the source covered while the hub is actually down
		// (Codex). A valid index with no matching song stays the only healthy
		// miss.
		return nil, lookupDegraded, err
	}

	wantedTitle := normalizeLunaBeatTitle(title, "")
	var entry lunaBeatSong
	found := false
	bestAlbum := false
	for _, candidate := range songs.Songs {
		if !lunaBeatRowMatches(candidate, wantedTitle, artist) {
			continue
		}
		albumMatch := strings.TrimSpace(track.Album) != "" &&
			strings.EqualFold(strings.TrimSpace(candidate.Album), strings.TrimSpace(track.Album))
		if !found {
			entry, found, bestAlbum = candidate, true, albumMatch
			continue
		}
		// The tiebreak is one-way: the first album-matched row stays the winner,
		// so duplicate rows cannot dethrone it.
		if !bestAlbum && albumMatch {
			entry, bestAlbum = candidate, true
		}
	}
	if !found || strings.TrimSpace(entry.Path) == "" {
		return nil, lookupHealthy, nil
	}

	ttmlURL := strings.TrimSuffix(lunaBeatBase, "/") + "/" + entry.Path
	response, err := p.sendRequest("LunaBeat lyrics", ttmlURL, lunaBeatHeaders(), deadline)
	if err != nil {
		return nil, lookupDegraded, err
	}
	switch response.StatusCode {
	case 200:
		// pass to the candidate build
	case 404:
		return nil, lookupHealthy, nil
	case 429:
		retryAfter := retryAfterDuration(response.Headers, p.now())
		p.rememberCooldown(sourceGlobalCooldownKey("luna-beat"), retryAfter)
		return nil, lookupDegraded, rateLimitResponseError("LunaBeat lyrics", response, retryAfter)
	default:
		return nil, lookupDegraded, providerResponseError("LunaBeat lyrics", response)
	}

	text := strings.TrimSpace(string(response.Body))
	if text == "" {
		return nil, lookupHealthy, nil
	}
	candidate := newLyricsCandidate(lyrics.LyricsText{Text: text}, "LunaBeat", "ttml")
	if !candidate.present() {
		return nil, lookupDegraded, fmt.Errorf("LunaBeat lyrics returned malformed TTML")
	}
	return []lyricsCandidate{candidate}, lookupHealthy, nil
}

// readLunaBeatIndex returns the cached songs index when the live manifest revision
// matches, otherwise refetches the index (and re-caches it gzipped). Manifest
// failures fall through to a direct index refetch (best-effort revision
// tracking); index acquisition failures themselves degrade the lookup (Codex).
func (p *lyricsProvider) readLunaBeatIndex(deadline time.Time) (lunaBeatIndexEnvelope, error) {
	manifestResponse, err := p.sendRequest("LunaBeat manifest", lunaBeatBase+"/api/v1/manifest.json", lunaBeatHeaders(), deadline)
	if err == nil && manifestResponse.StatusCode == 200 {
		var manifest struct {
			Revision string `json:"revision"`
		}
		if json.Unmarshal(manifestResponse.Body, &manifest) == nil {
			if raw, exists, _ := p.cache.Get(lunaBeatIndexKey); exists && len(raw) > 0 {
				if value, unpackErr := gunzipString(raw); unpackErr == nil {
					var envelope lunaBeatIndexEnvelope
					if json.Unmarshal(value, &envelope) == nil && envelope.Revision == manifest.Revision {
						return envelope, nil
					}
				}
			}
		}
	}

	indexResponse, err := p.sendRequest("LunaBeat index", lunaBeatBase+"/api/v1/songs.json", lunaBeatHeaders(), deadline)
	if err != nil {
		return lunaBeatIndexEnvelope{}, err
	}
	switch indexResponse.StatusCode {
	case 200:
		// fall through to the envelope decode
	case 429:
		retryAfter := retryAfterDuration(indexResponse.Headers, p.now())
		p.rememberCooldown(sourceGlobalCooldownKey("luna-beat"), retryAfter)
		return lunaBeatIndexEnvelope{}, rateLimitResponseError("LunaBeat index", indexResponse, retryAfter)
	default:
		return lunaBeatIndexEnvelope{}, providerResponseError("LunaBeat index", indexResponse)
	}
	var envelope lunaBeatIndexEnvelope
	if json.Unmarshal(indexResponse.Body, &envelope) != nil || envelope.Revision == "" {
		return lunaBeatIndexEnvelope{}, errors.New("decode LunaBeat index response")
	}
	if raw := gzipString(indexResponse.Body); len(raw) <= maxCachedLyricsEntryBytes {
		p.cache.SetWithTTL(lunaBeatIndexKey, raw, lunaBeatIndexTTL)
		return envelope, nil
	}
	// An index larger than the per-entry cap would evict the whole store;
	// that is an operational failure too, not a miss.
	return lunaBeatIndexEnvelope{}, errors.New("LunaBeat index exceeds the cache entry cap")
}

type lunaBeatIndexEnvelope struct {
	SchemaVersion int            `json:"schemaVersion"`
	Revision      string         `json:"revision"`
	Songs         []lunaBeatSong `json:"songs"`
}

type lunaBeatSong struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Artists []string `json:"artists"`
	Album   string   `json:"album"`
	Path    string   `json:"path"`
}

// lunaBeatRowMatches compares the collapsed title and artist aliases
// individually with complete-name equality: the hub index lists several alias
// names for one artist (赵雷/趙雷/Lei Zhao), so joining them would never match a
// track tagged with a single alias (Greptile). Substring matching would flag
// unrelated artists ("pink|" inside "blackpink|"), so only full names compare;
// multi-artist tracks split on common separators so every member can
// alias-match a hub row.
func lunaBeatRowMatches(candidate lunaBeatSong, wantedTitle, trackArtist string) bool {
	if normalizeLunaBeatTitle(candidate.Title, "") != wantedTitle {
		return false
	}
	if len(candidate.Artists) == 0 {
		return strings.TrimSpace(trackArtist) == ""
	}
	wanted := lunaBeatArtistNames(trackArtist)
	for _, alias := range candidate.Artists {
		rowArtist := strings.ToLower(onlyLettersAndDigits(strings.TrimSpace(alias)))
		if rowArtist == "" {
			continue
		}
		for _, name := range wanted {
			if name == rowArtist {
				return true
			}
		}
	}
	return false
}

// lunaBeatArtistNames splits a track's artist field on common collaboration
// separators and collapses each name so alias rows can compare complete names.
func lunaBeatArtistNames(trackArtist string) []string {
	names := make([]string, 0, 2)
	for _, raw := range strings.FieldsFunc(trackArtist, func(r rune) bool {
		switch r {
		case '&', ',', '、', '/', '|', ';', '·':
			return true
		}
		return false
	}) {
		if collapsed := strings.ToLower(onlyLettersAndDigits(strings.TrimSpace(raw))); collapsed != "" {
			names = append(names, collapsed)
		}
	}
	return names
}

// normalizeLunaBeatTitle folds NFKC-ish differences down: lowercase, strip all
// whitespace and punctuation. Good enough to match the hub's index rows.
func normalizeLunaBeatTitle(title, artist string) string {
	return strings.ToLower(onlyLettersAndDigits(title)) + "|" + strings.ToLower(onlyLettersAndDigits(artist))
}

// onlyLettersAndDigits keeps letters and digits for matching while dropping
// whitespace and punctuation. It is unicode-aware because the hub index is
// mostly non-Latin titles (CJK), which ASCII range checks would erase entirely.
func onlyLettersAndDigits(value string) string {
	var kept []rune
	for _, letter := range value {
		if unicode.IsLetter(letter) || unicode.IsDigit(letter) {
			kept = append(kept, letter)
		}
	}
	return string(kept)
}

func lunaBeatHeaders() map[string]string {
	return map[string]string{
		"Accept":     "application/json",
		"User-Agent": pluginUserAgent(),
	}
}

func gzipString(value []byte) []byte {
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	_, _ = writer.Write(value)
	_ = writer.Close()
	return buffer.Bytes()
}

func gunzipString(raw []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	var buffer bytes.Buffer
	if _, err := buffer.ReadFrom(reader); err != nil {
		return nil, fmt.Errorf("gunzip luna-beat index: %w", err)
	}
	return buffer.Bytes(), nil
}
