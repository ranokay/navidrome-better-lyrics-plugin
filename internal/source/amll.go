package source

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
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
// non-200 status is a degraded failure instead of a healthy miss.
func amllEnvelopeStatusFailure(rt Runtime, source string, status int) (Health, error) {
	switch status {
	case 200, 404:
		return Healthy, nil
	case 429:
		rt.RememberCooldown(GlobalCooldownKey("amll"), defaultRateLimitCooldown)
		return Degraded, &providerHTTPError{source: source, status: 429, globalRateLimit: true}
	default:
		return Degraded, &providerHTTPError{source: source, status: int32(status)}
	}
}

// fetchAMLL resolves lyrics through the AMLL community DB:
// first search by name/artist/album, then fetch the matched TTML.
func fetchAMLL(rt Runtime, track lyrics.TrackInfo, deadline time.Time) ([]lyric.Candidate, Health, error) {
	searchURL, ok := amllSearchURL(track)
	if !ok {
		return nil, Healthy, nil
	}
	headers := rt.DefaultHeaders()
	response, err := get(rt, "AMLL search", searchURL, headers, deadline)
	if err != nil {
		return nil, Degraded, err
	}

	switch response.StatusCode {
	case 200:
		// pass through to the envelope decode
	case 404:
		return nil, Healthy, nil
	case 429:
		return nil, Degraded, rateLimitFailure(rt, "amll", "AMLL search", response)
	default:
		return nil, Degraded, providerResponseError("AMLL search", response)
	}

	var envelope amllSearchEnvelope
	if err := json.Unmarshal(response.Body, &envelope); err != nil {
		return nil, Degraded, fmt.Errorf("decode AMLL search response: %w", err)
	}
	if envelope.Status == 404 {
		return nil, Healthy, nil
	}
	if health, err := amllEnvelopeStatusFailure(rt, "AMLL search envelope", envelope.Status); err != nil {
		return nil, health, err
	}
	if len(envelope.Data.Items) == 0 {
		return nil, Healthy, nil
	}
	matched, ok := pickAMLLItem(envelope.Data.Items, track)
	if !ok {
		// Response order is not identity: only a normalized title/artist
		// match may be fetched.
		return nil, Healthy, nil
	}

	lyricsURL := fmt.Sprintf("%s/v1/lyrics/get?id=%s&format=ttml", amllEndpoint, url.QueryEscape(string(matched.ID)))
	lyricsResponse, err := get(rt, "AMLL lyrics", lyricsURL, headers, deadline)
	if err != nil {
		return nil, Degraded, err
	}

	switch lyricsResponse.StatusCode {
	case 200:
		// pass through to the payload decode
	case 404:
		return nil, Healthy, nil
	case 429:
		return nil, Degraded, rateLimitFailure(rt, "amll", "AMLL lyrics", lyricsResponse)
	default:
		return nil, Degraded, providerResponseError("AMLL lyrics", lyricsResponse)
	}

	var getEnvelope struct {
		Status int `json:"status"`
		Data   struct {
			Lyrics string `json:"lyrics"`
		} `json:"data"`
	}
	if err := json.Unmarshal(lyricsResponse.Body, &getEnvelope); err != nil {
		return nil, Degraded, fmt.Errorf("decode AMLL lyrics response: %w", err)
	}
	if getEnvelope.Status == 404 {
		// The lyrics endpoint answering "no match" is a clean miss; only a
		// 200 with a missing body is malformed.
		return nil, Healthy, nil
	}
	if health, err := amllEnvelopeStatusFailure(rt, "AMLL lyrics envelope", getEnvelope.Status); err != nil {
		return nil, health, err
	}
	if strings.TrimSpace(getEnvelope.Data.Lyrics) == "" {
		return nil, Degraded, fmt.Errorf("AMLL lyrics returned malformed TTML")
	}

	lyricsBody := getEnvelope.Data.Lyrics
	candidate := lyric.NewCandidate(lyrics.LyricsText{Text: lyricsBody}, "AMLL", "ttml")
	if !candidate.Present() {
		if lyric.EmptyDocument(lyricsBody, "ttml") {
			return nil, Healthy, nil
		}
		return nil, Degraded, fmt.Errorf("AMLL lyrics returned unparseable TTML")
	}
	return []lyric.Candidate{candidate}, Healthy, nil
}

// pickAMLLItem selects the search row whose collapsed title and artist match
// the request; the album breaks ties between otherwise-qualified rows.
func pickAMLLItem(items []amllItem, track lyrics.TrackInfo) (amllItem, bool) {
	titleTokens := lyric.NormalizedTokens(track.Title)
	album := strings.TrimSpace(track.Album)

	var best amllItem
	found := false
	bestAlbum := false
	for _, item := range items {
		if !amllNamesMatch(item.MusicNames, titleTokens) {
			continue
		}
		if !amllArtistsMatch(item.ArtistNames, lyric.TrackArtist(track)) {
			continue
		}
		albumMatch := album != "" && amllNamesContain(item.AlbumNames, lyric.NormalizedTokens(album))
		if !found || (!bestAlbum && albumMatch) {
			best, found, bestAlbum = item, true, bestAlbum || albumMatch
		}
	}
	return best, found
}

// amllNamesMatch accepts any listed name whose collapsed tokens match (title
// prefix tolerance allowed by the callers) — artist callers pass complete-name
// semantics via lyric.ArtistMatch instead. An empty wanted list means the
// request carried no verifiable title tokens, so nothing may match.
func amllNamesMatch(names []string, wanted []string) bool {
	if len(wanted) == 0 {
		return false
	}
	for _, name := range names {
		if lyric.TokensMatch(lyric.NormalizedTokens(name), wanted) {
			return true
		}
	}
	return false
}

// amllArtistsMatch applies complete-name semantics to the artist column.
func amllArtistsMatch(names []string, wanted string) bool {
	if strings.TrimSpace(wanted) == "" {
		return true
	}
	for _, name := range names {
		if lyric.ArtistMatch(name, wanted) {
			return true
		}
	}
	return false
}

func amllNamesContain(names []string, tokens []string) bool {
	for _, name := range names {
		if lyric.TokensMatch(lyric.NormalizedTokens(name), tokens) {
			return true
		}
	}
	return false
}

func amllSearchURL(track lyrics.TrackInfo) (string, bool) {
	title := strings.TrimSpace(track.Title)
	artist := lyric.TrackArtist(track)
	if title == "" || artist == "" {
		return "", false
	}
	query := url.Values{"musicName": {title}, "artistName": {artist}}
	if album := strings.TrimSpace(track.Album); album != "" {
		query.Set("albumName", album)
	}
	return amllEndpoint + "/v1/lyrics/search?" + query.Encode(), true
}
