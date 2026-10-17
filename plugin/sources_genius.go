package main

import (
	"encoding/json"
	"fmt"
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

	pageURL, searchErr := pickGeniusSongURL(response.Body, track)
	if searchErr != nil {
		// Malformed 200s (transient HTML challenges, truncated replies) are
		// provider failures, not cacheable misses (Codex).
		return nil, lookupDegraded, fmt.Errorf("decode Genius search response: %w", searchErr)
	}
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
func pickGeniusSongURL(body []byte, track lyrics.TrackInfo) (string, error) {
	var envelope geniusSearchEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return "", err
	}
	titleTokens := normalizedLyricTokens(track.Title)
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
		if !geniusArtistsMatch(hit.Result.ArtistNames, trackArtist(track)) {
			continue
		}
		return hit.Result.URL, nil
	}
	return "", nil
}

// geniusArtistsMatch accepts the request when any returned artist name matches
// the track artist completely, with collaboration splitting on both sides.
func geniusArtistsMatch(returned, wanted string) bool {
	return artistTokensMatch(returned, wanted)
}

func geniusPageHeaders() map[string]string {
	return map[string]string{
		"Accept":     "text/html,application/xhtml+xml",
		"User-Agent": geniusBrowseUserAgent,
	}
}

// geniusLyricsText extracts the plain text of the song's lyric sections: the
// leaf `data-lyrics-container` elements inside the lyrics root. Extracting the
// containers themselves (rather than rendering everything between the first
// container and the footer) keeps recommendation sidebars and other page
// chrome out of the body (Devin), while the header container — whose first
// line is the contributors chip — is dropped wholesale.
func geniusLyricsText(page []byte, title string) string {
	document := string(page)
	ranges := geniusLeafContainers(document)
	sections := []string{}
	for _, bounds := range ranges {
		// The contributors/language/song-bio header rides inside the first
		// container's subtree: strip those elements before rendering.
		fragment := removeGeniusChromeByElements(document[bounds[0]:bounds[1]])
		lines := geniusFragmentLines(fragment)
		if len(lines) == 0 {
			continue
		}
		if geniusContainerChrome(lines[0]) {
			continue
		}
		sections = append(sections, strings.Join(lines, "\n"))
	}
	body := strings.Join(sections, "\n\n")
	if title != "" {
		header := title + " Lyrics"
		kept := []string{}
		for _, line := range strings.Split(body, "\n") {
			if strings.TrimSpace(line) == header {
				continue
			}
			kept = append(kept, line)
		}
		body = strings.Join(kept, "\n")
	}
	return strings.TrimSpace(body)
}

// geniusLeafContainers returns the byte ranges of the innermost lyric
// containers: pages nest containers, and only the leaves carry one section's
// text each.
func geniusLeafContainers(document string) [][2]int {
	all := [][2]int{}
	search := 0
	for {
		index := strings.Index(document[search:], "<div ")
		if index < 0 {
			break
		}
		start := search + index
		tagEnd := strings.IndexByte(document[start:], '>')
		if tagEnd < 0 {
			break
		}
		if strings.Contains(document[start:start+tagEnd+1], `data-lyrics-container="true"`) {
			end := findGeniusElementEnd(document, start)
			all = append(all, [2]int{start, end})
		}
		search = start + len("<div ")
	}
	leaves := [][2]int{}
	for _, outer := range all {
		nested := false
		for _, inner := range all {
			if inner[0] > outer[0] && inner[0] < outer[1] {
				nested = true
				break
			}
		}
		if !nested {
			leaves = append(leaves, outer)
		}
	}
	return leaves
}

// geniusContainerChrome reports a container that is page chrome, not lyrics:
// the contributors/language header and the read-more song-bio rail.
func geniusContainerChrome(firstLine string) bool {
	return strings.Contains(firstLine, "Contributors") || strings.Contains(firstLine, "Read More")
}

// geniusFragmentLines renders one markup fragment as plain text lines:
// line breaks come from <br> and block closes, entities decode, the embed's
// escaped angular brackets unfold, and blank lines drop.
func geniusFragmentLines(fragment string) []string {
	text := geniusScriptPattern.ReplaceAllString(fragment, "")
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
		lines = append(lines, line)
	}
	return lines
}

// geniusChromePattern matches the class attributes of the pure-chrome
// containers: the contributors/language header, the song-bio expansion, and
// the editor rails. Removal is element-scoped (balanced scan) so real lyric
// words are never filtered by substring (Greptile).
var geniusChromePattern = regexp.MustCompile(`class="[^"]*(?:LyricsHeader__Container|SongBioPreview|LyricsEdit-desktop|LyricsEditExplainer|ContributorsCreditSong)`)

// removeGeniusChromeByElements cuts every geniusChromePattern `<div>` subtree
// with a balanced scan so the surrounding lyrics keep their markup order. The
// matched anchor must live inside a `<div ...>` opening tag.
func removeGeniusChromeByElements(document string) string {
	searchFrom := 0
	for {
		loc := findGeniusChromeDiv(document, searchFrom)
		if loc < 0 {
			return document
		}
		end := findGeniusElementEnd(document, loc)
		if end <= loc {
			return document
		}
		document = document[:loc] + document[end:]
		searchFrom = loc
	}
}

// findGeniusChromeDiv locates the next chrome anchor whose owning tag is an
// opening `<div ...>`; anchors that ride other elements get skipped without
// cutting.
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
