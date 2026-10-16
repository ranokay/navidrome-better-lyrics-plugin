package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"strings"
	"time"

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

	songs, _, ok := p.readLunaBeatIndex(deadline)
	if !ok {
		// A broken or oversized index skips the source for this lookup without
		// failing the whole chain (Review Focus #4).
		return nil, lookupHealthy, nil
	}

	wanted := normalizeLunaBeatTitle(title, artist)
	var entry lunaBeatSong
	found := false
	bestAlbum := false
	for _, candidate := range songs.Songs {
		if normalizeLunaBeatTitle(candidate.Title, strings.Join(candidate.Artists, ", ")) != wanted {
			continue
		}
		if !found {
			entry, found = candidate, true
			continue
		}
		if strings.EqualFold(strings.TrimSpace(candidate.Album), strings.TrimSpace(track.Album)) {
			entry, bestAlbum = candidate, true
		}
	}
	if !found || (found && strings.TrimSpace(entry.Path) == "") {
		return nil, lookupHealthy, nil
	}
	_ = bestAlbum

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
// matches, otherwise refetches the index (and re-caches it gzipped).
func (p *lyricsProvider) readLunaBeatIndex(deadline time.Time) (lunaBeatIndexEnvelope, string, bool) {
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
						return envelope, envelope.Revision, true
					}
				}
			}
		}
	}

	indexResponse, err := p.sendRequest("LunaBeat index", lunaBeatBase+"/api/v1/songs.json", lunaBeatHeaders(), deadline)
	if err != nil {
		return lunaBeatIndexEnvelope{}, "", false
	}
	if indexResponse.StatusCode == 200 {
		var envelope lunaBeatIndexEnvelope
		if json.Unmarshal(indexResponse.Body, &envelope) == nil && envelope.Revision != "" {
			if raw := gzipString(indexResponse.Body); len(raw) <= maxCachedLyricsEntryBytes {
				p.cache.SetWithTTL(lunaBeatIndexKey, raw, lunaBeatIndexTTL)
				return envelope, envelope.Revision, true
			}
			// An index larger than the per-entry cap would evict the whole store;
			// skip the source instead.
			return lunaBeatIndexEnvelope{}, "", false
		}
	}
	return lunaBeatIndexEnvelope{}, "", false
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

// normalizeLunaBeatTitle folds NFKC-ish differences down: lowercase, strip all
// whitespace and punctuation. Good enough to match the hub's index rows.
func normalizeLunaBeatTitle(title, artist string) string {
	return strings.ToLower(onlyLettersAndDigits(title)) + "|" + strings.ToLower(onlyLettersAndDigits(artist))
}

func onlyLettersAndDigits(value string) string {
	var kept []rune
	for _, letter := range value {
		if letter >= 'a' && letter <= 'z' || letter >= 'A' && letter <= 'Z' || letter >= '0' && letter <= '9' {
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
