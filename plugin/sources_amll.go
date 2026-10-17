package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

const amllEndpoint = "https://api.amll.dev"

// amllSearchEnvelope models the degenerately-simple helper API used by the AMLL
// community TTML database. The search step exposes ids; the lyric fetch returns a
// full Apple-style TTML string Navidrome already understands.
type amllSearchEnvelope struct {
	Status int       `json:"status"`
	Data   amllItems `json:"data"`
}

type amllItems struct {
	Items []amllItem `json:"items"`
}

// amllItemID accepts both forms observed live: JSON numbers and strings.
type amllItemID string

func (i *amllItemID) UnmarshalJSON(raw []byte) error {
	*i = amllItemID(strings.Trim(strings.TrimSpace(string(raw)), `"`))
	return nil
}

type amllItem struct {
	ID          amllItemID `json:"id"`
	MusicNames  []string   `json:"musicNames"`
	ArtistNames []string   `json:"artistNames"`
	AlbumNames  []string   `json:"albumNames"`
}

// amllEnvelopeStatusFailure classifies an application-level status the HTTP
// layer already answered 200 for: no-match 404 keeps the quiet miss contract,
// an envelope rate limit activates the provider cooldown, and any other
// non-200 status is a degraded failure instead of a healthy miss (Codex).
func amllEnvelopeStatusFailure(p *lyricsProvider, source string, status int) (lookupHealth, error) {
	switch status {
	case 200, 404:
		return lookupHealthy, nil
	case 429:
		p.rememberCooldown(sourceGlobalCooldownKey("amll"), defaultRateLimitCooldown)
		return lookupDegraded, &providerHTTPError{source: source, status: 429, globalRateLimit: true}
	default:
		return lookupDegraded, &providerHTTPError{source: source, status: int32(status)}
	}
}

// fetchAMLLyricsCandidates resolves lyrics through the AMLL community DB:
// first search by name/artist/album, then fetch the matched TTML.
func fetchAMLLyricsCandidates(p *lyricsProvider, track lyrics.TrackInfo, deadline time.Time) ([]lyricsCandidate, lookupHealth, error) {
	searchURL, ok := amllSearchURL(track)
	if !ok {
		return nil, lookupHealthy, nil
	}
	headers := amllHeaders()
	response, err := p.sendRequest("AMLL search", searchURL, headers, deadline)
	if err != nil {
		return nil, lookupDegraded, err
	}

	switch response.StatusCode {
	case 200:
		// pass through to the envelope decode
	case 404:
		return nil, lookupHealthy, nil
	case 429:
		retryAfter := retryAfterDuration(response.Headers, p.now())
		p.rememberCooldown(sourceGlobalCooldownKey("amll"), retryAfter)
		return nil, lookupDegraded, rateLimitResponseError("AMLL search", response, retryAfter)
	default:
		return nil, lookupDegraded, providerResponseError("AMLL search", response)
	}

	var envelope amllSearchEnvelope
	if err := json.Unmarshal(response.Body, &envelope); err != nil {
		return nil, lookupDegraded, fmt.Errorf("decode AMLL search response: %w", err)
	}
	if envelope.Status == 404 {
		return nil, lookupHealthy, nil
	}
	if health, err := amllEnvelopeStatusFailure(p, "AMLL search envelope", envelope.Status); err != nil {
		return nil, health, err
	}
	if len(envelope.Data.Items) == 0 {
		return nil, lookupHealthy, nil
	}
	matched, ok := pickAMLLItem(envelope.Data.Items, track)
	if !ok {
		// Response order is not identity: only a normalized title/artist
		// match may be fetched (Codex).
		return nil, lookupHealthy, nil
	}

	lyricsURL := fmt.Sprintf("%s/v1/lyrics/get?id=%s&format=ttml", amllEndpoint, url.QueryEscape(string(matched.ID)))
	lyricsResponse, err := p.sendRequest("AMLL lyrics", lyricsURL, headers, deadline)
	if err != nil {
		return nil, lookupDegraded, err
	}

	switch lyricsResponse.StatusCode {
	case 200:
		// pass through to the payload decode
	case 404:
		return nil, lookupHealthy, nil
	case 429:
		retryAfter := retryAfterDuration(lyricsResponse.Headers, p.now())
		p.rememberCooldown(sourceGlobalCooldownKey("amll"), retryAfter)
		return nil, lookupDegraded, rateLimitResponseError("AMLL lyrics", lyricsResponse, retryAfter)
	default:
		return nil, lookupDegraded, providerResponseError("AMLL lyrics", lyricsResponse)
	}

	var getEnvelope struct {
		Status int `json:"status"`
		Data   struct {
			Lyrics string `json:"lyrics"`
		} `json:"data"`
	}
	if err := json.Unmarshal(lyricsResponse.Body, &getEnvelope); err != nil {
		return nil, lookupDegraded, fmt.Errorf("decode AMLL lyrics response: %w", err)
	}
	if getEnvelope.Status == 404 {
		// The lyrics endpoint answering "no match" is a clean miss; only a
		// 200 with a missing body is malformed (Devin).
		return nil, lookupHealthy, nil
	}
	if health, err := amllEnvelopeStatusFailure(p, "AMLL lyrics envelope", getEnvelope.Status); err != nil {
		return nil, health, err
	}
	if strings.TrimSpace(getEnvelope.Data.Lyrics) == "" {
		return nil, lookupDegraded, fmt.Errorf("AMLL lyrics returned malformed TTML")
	}

	lyricsBody := getEnvelope.Data.Lyrics
	candidate := newLyricsCandidate(lyrics.LyricsText{Text: lyricsBody}, "AMLL", "ttml")
	if !candidate.present() {
		if emptyLyricsDocument(lyricsBody, "ttml") {
			return nil, lookupHealthy, nil
		}
		return nil, lookupDegraded, fmt.Errorf("AMLL lyrics returned unparseable TTML")
	}
	return []lyricsCandidate{candidate}, lookupHealthy, nil
}

// pickAMLLItem selects the search row whose collapsed title and artist match
// the request; the album breaks ties between otherwise-qualified rows.
func pickAMLLItem(items []amllItem, track lyrics.TrackInfo) (amllItem, bool) {
	titleTokens := normalizedLyricTokens(track.Title)
	artistTokens := normalizedLyricTokens(trackArtist(track))
	album := strings.TrimSpace(track.Album)

	var best amllItem
	found := false
	bestAlbum := false
	for _, item := range items {
		if !amllNamesMatch(item.MusicNames, titleTokens) {
			continue
		}
		if !amllNamesMatch(item.ArtistNames, artistTokens) {
			continue
		}
		albumMatch := album != "" && amllNamesContain(item.AlbumNames, normalizedLyricTokens(album))
		if !found || (!bestAlbum && albumMatch) {
			best, found, bestAlbum = item, true, bestAlbum || albumMatch
		}
	}
	return best, found
}

// amllNamesMatch accepts any listed name whose collapsed tokens match.
func amllNamesMatch(names []string, wanted []string) bool {
	if len(wanted) == 0 {
		return true
	}
	for _, name := range names {
		if lyricTokensMatch(normalizedLyricTokens(name), wanted) {
			return true
		}
	}
	return false
}

func amllNamesContain(names []string, tokens []string) bool {
	for _, name := range names {
		if lyricTokensMatch(normalizedLyricTokens(name), tokens) {
			return true
		}
	}
	return false
}

func amllSearchURL(track lyrics.TrackInfo) (string, bool) {
	title := strings.TrimSpace(track.Title)
	artist := trackArtist(track)
	if title == "" || artist == "" {
		return "", false
	}
	query := url.Values{"musicName": {title}, "artistName": {artist}}
	if album := strings.TrimSpace(track.Album); album != "" {
		query.Set("albumName", album)
	}
	return amllEndpoint + "/v1/lyrics/search?" + query.Encode(), true
}

func amllHeaders() map[string]string {
	return map[string]string{
		"Accept":     "application/json",
		"User-Agent": pluginUserAgent(),
	}
}
