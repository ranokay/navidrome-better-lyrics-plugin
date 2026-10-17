package main

import (
	"encoding/json"
	"html"
	"regexp"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

const (
	geniusSearchEndpoint = "https://genius.com/api/search"
	geniusProviderName   = "Genius"
	// The genius page serves its lyric body to real browsers; other agents
	// land on shell pages without the lyric containers.
	geniusBrowseUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36"
)

var (
	geniusScriptPattern     = regexp.MustCompile(`(?s)<script[^>]*>.*?</script>|<style[^>]*>.*?</style>`)
	geniusBreakPattern      = regexp.MustCompile(`<br\s*/?>`)
	geniusBlockClosePattern = regexp.MustCompile(`</(?:p|div|h\d[^>]*)>`)
	geniusTagPattern        = regexp.MustCompile(`<[^>]*>`)
)

// fetchGeniusLyricsCandidates resolves the plain, community-curated lyric body
// from Genius: a keyless metadata search, then a scrape of the matched song
// page's lyric containers. Section tags ([Verse 1] and friends) stay because
// they are part of the published lyric layout.
func fetchGeniusLyricsCandidates(p *lyricsProvider, track lyrics.TrackInfo, deadline time.Time) ([]lyricsCandidate, lookupHealth, error) {
	title := strings.TrimSpace(track.Title)
	artist := trackArtist(track)
	if title == "" || artist == "" {
		return nil, lookupHealthy, nil
	}

	searchURL := geniusSearchEndpoint + "?q=" + title + " " + artist
	response, err := p.sendRequest("Genius search", searchURL, geniusPageHeaders(), deadline)
	if err != nil {
		return nil, lookupDegraded, err
	}
	switch response.StatusCode {
	case 200:
		// fall through to the hit selection below
	case 404:
		return nil, lookupHealthy, nil
	case 429:
		retryAfter := retryAfterDuration(response.Headers, p.now())
		p.rememberCooldown(sourceGlobalCooldownKey("genius"), retryAfter)
		return nil, lookupDegraded, rateLimitResponseError("Genius search", response, retryAfter)
	default:
		return nil, lookupDegraded, providerResponseError("Genius search", response)
	}

	pageURL := pickGeniusSongURL(response.Body, track)
	if pageURL == "" {
		return nil, lookupHealthy, nil
	}

	response, err = p.sendRequest("Genius lyrics", pageURL, geniusPageHeaders(), deadline)
	if err != nil {
		return nil, lookupDegraded, err
	}
	switch response.StatusCode {
	case 200:
		// pass to the page scrape below
	case 404:
		return nil, lookupHealthy, nil
	case 429:
		retryAfter := retryAfterDuration(response.Headers, p.now())
		p.rememberCooldown(sourceGlobalCooldownKey("genius"), retryAfter)
		return nil, lookupDegraded, rateLimitResponseError("Genius lyrics", response, retryAfter)
	default:
		return nil, lookupDegraded, providerResponseError("Genius lyrics", response)
	}

	text := geniusLyricsText(response.Body, strings.TrimSpace(title))
	if strings.TrimSpace(text) == "" {
		return nil, lookupHealthy, nil
	}
	candidate := newLyricsCandidate(lyrics.LyricsText{Text: text}, geniusProviderName, "plain")
	if !candidate.present() {
		return nil, lookupHealthy, nil
	}
	return []lyricsCandidate{candidate}, lookupHealthy, nil
}

type geniusSearchEnvelope struct {
	Response struct {
		Hits []struct {
			Type   string `json:"type"`
			Result struct {
				Title             string `json:"title"`
				TitleWithFeatured string `json:"title_with_featured"`
				ArtistNames       string `json:"artist_names"`
				URL               string `json:"url"`
			} `json:"result"`
		} `json:"hits"`
	} `json:"response"`
}

// pickGeniusSongURL returns the song-page URL of the first song hit whose
// collapsed title and artist name both match the request. Genius search is
// famously fuzzy; the metadata gate keeps unrelated songs out of the plain
// cache.
func pickGeniusSongURL(body []byte, track lyrics.TrackInfo) string {
	var envelope geniusSearchEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ""
	}
	titleTokens := normalizedLyricTokens(track.Title)
	artistTokens := normalizedLyricTokens(trackArtist(track))
	for _, hit := range envelope.Response.Hits {
		if hit.Type != "song" {
			continue
		}
		if hit.Result.URL == "" || !strings.HasPrefix(hit.Result.URL, "https://genius.com/") {
			continue
		}
		if !lyricTokensMatch(normalizedLyricTokens(hit.Result.TitleWithFeatured), titleTokens) &&
			!lyricTokensMatch(normalizedLyricTokens(hit.Result.Title), titleTokens) {
			continue
		}
		if !geniusArtistsMatch(hit.Result.ArtistNames, artistTokens) {
			continue
		}
		return hit.Result.URL
	}
	return ""
}

// geniusArtistsMatch accepts the request when any returned artist name matches
// the track artist tokens fully (prefix-token tolerant, mirroring the other
// metadata gates; genius splits collaborations on " & ").
func geniusArtistsMatch(returned string, wanted []string) bool {
	if len(wanted) == 0 {
		return true
	}
	for _, name := range strings.Split(returned, " & ") {
		if lyricTokensMatch(normalizedLyricTokens(name), wanted) {
			return true
		}
	}
	return false
}

func geniusPageHeaders() map[string]string {
	return map[string]string{
		"Accept":     "text/html,application/xhtml+xml",
		"User-Agent": geniusBrowseUserAgent,
	}
}

// geniusLyricsText extracts the plain text from the song page: the lyric body
// rides the Lyrics__Root segment between the first lyric container and the
// footer. Header containers ("Contributors", language lists, "Read More")
// ride the same attribute and get dropped; line breaks come from <br> and
// block closes; entities decode; the embed's escaped angular brackets unfold.
func geniusLyricsText(page []byte, title string) string {
	document := string(page)
	start := strings.Index(document, `data-lyrics-container="true"`)
	if start < 0 {
		return ""
	}
	segment := document[start:]
	if marker := strings.Index(segment, "LyricsFooter"); marker > 0 {
		segment = segment[:marker]
	}

	text := geniusScriptPattern.ReplaceAllString(segment, "")
	text = geniusBreakPattern.ReplaceAllString(text, "\n")
	text = geniusBlockClosePattern.ReplaceAllString(text, "\n")
	text = geniusTagPattern.ReplaceAllString(text, "")
	text = html.UnescapeString(text)

	lines := []string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		line = strings.ReplaceAll(line, "\\u003c", "<")
		line = strings.ReplaceAll(line, "\\u003e", ">")
		if line == "" || linesIsChrome(line) {
			continue
		}
		if title != "" && line == title+" Lyrics" {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// linesIsChrome drops the editor sparkline around the lyric body: contributor
// credits, the language picker, and the read-more expansion rail never belong
// to the published lyric text.
func linesIsChrome(line string) bool {
	for _, marker := range []string{"Contributors", "Read More", "Translations", "Read more", "Add an annotation", "Genius"} {
		if strings.Contains(line, marker) {
			return true
		}
	}
	return false
}
