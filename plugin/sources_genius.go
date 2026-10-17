package main

import (
	"encoding/json"
	"html"
	"net/url"
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
	geniusBlockClosePattern = regexp.MustCompile(`</(?:p|div|h[1-6][^>]*|li|ul|ol)>`)
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

	searchURL := geniusSearchEndpoint + "?" + url.Values{"q": {title + " " + artist}}.Encode()
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
// footer. Page chrome (the contributors/language/song-bio header block and
// the editor rails) is removed by its HTML elements with a balanced scan —
// never by line-text substrings, which would eat real lyric lines ("Genius
// of love" — Greptile). Line breaks come from <br> and block closes; entities
// decode; the embed's escaped angular brackets unfold.
func geniusLyricsText(page []byte, title string) string {
	document := string(page)
	start := strings.Index(document, `<div data-lyrics-container="true"`)
	if start < 0 {
		return ""
	}
	segment := document[start:]
	if marker := strings.Index(segment, "LyricsFooter"); marker > 0 {
		segment = segment[:marker]
	}
	segment = removeGeniusChromeByElements(segment)

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
		if line == "" {
			continue
		}
		if title != "" && line == title+" Lyrics" {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// geniusChromeClassName walks those containers whose entire subgroup is page
// chrome: the contributors/language/song-bio header and the editor rails. The
// footer is excluded from the cut list because the lyrics slice already ends
// at the footer marker; anywhere else the Balanced scan keeps real lyric
// words inside intact. The matched anchor must live inside a `<div ...>`
// opening tag, so `<aside>`-hosted chrome never eats other elements.
var geniusChromePattern = regexp.MustCompile(`class="[^"]*(?:LyricsHeader__Container|SongBioPreview|LyricsEdit-desktop|LyricsEditExplainer|ContributorsCreditSong)`)

// removeGeniusChromeByElements cuts every geniusChromePattern `<div>` subtree
// with a balanced scan so the surrounding lyrics keep their markup order. The
// scan walks open/close tags with a name stack; self-closing and void tags
// skip. A mis-shapen page loses at most a bounded tail instead of looping.
func removeGeniusChromeByElements(document string) string {
	searchFrom := 0
	for {
		loc := findGeniusChromeDiv(document, searchFrom)
		if loc < 0 {
			return document
		}
		end := findGeniusElementEnd(document, loc)
		document = document[:loc] + document[end:]
		searchFrom = loc
	}
}

// findGeniusChromeDiv locates the next chrome anchor whose owning tag is an
// opening `<div ...>`; anchors that ride non-div elements (an `<aside class>`
// footer forscénario) get skipped without cutting.
func findGeniusChromeDiv(document string, from int) int {
	for search := from; search <= len(document); {
		anchor := geniusChromePattern.FindStringSubmatchIndex(document[search:])
		if anchor == nil {
			return -1
		}
		absStart := search + anchor[0]
		tagStart := strings.LastIndex(document[:absStart], "<div ")
		if tagStart >= 0 && !strings.Contains(document[tagStart:absStart], ">") {
			return tagStart
		}
		if anchor[1] == 0 {
			return -1
		}
		search = absStart + 1
	}
	return -1
}

var geniusTokenPattern = regexp.MustCompile(`<(/?)([a-zA-Z0-9]+)[^>]*?(/?)>`)

var geniusVoidTags = map[string]bool{
	"img": true, "br": true, "hr": true, "input": true,
	"meta": true, "link": true, "source": true, "circle": true,
	"path": true, "rect": true, "line": true, "use": true,
}

// findGeniusElementEnd returns the index just past the `</div>` that closes
// the element opened at openStart, counting only div opens/closes — genius's
// chrome containers are div-only trees, and `<br/>`/`<path/>` interiors hold
// no divs in the captured pages.
func findGeniusElementEnd(document string, openStart int) int {
	depth := 0
	cursor := openStart
	for cursor < len(document) {
		nextOpen := strings.Index(document[cursor:], "<div")
		nextClose := strings.Index(document[cursor:], "</div")
		switch {
		case nextClose >= 0 && (nextOpen < 0 || nextClose <= nextOpen):
			at := cursor + nextClose
			depth--
			after := at + len("</div")
			if depth == 0 {
				closeEnd := strings.IndexByte(document[after:], '>')
				if closeEnd < 0 {
					return len(document)
				}
				return after + closeEnd + 1
			}
			cursor = after
		case nextOpen >= 0:
			depth++
			cursor += nextOpen + len("<div")
		default:
			return len(document)
		}
	}
	return len(document)
}
