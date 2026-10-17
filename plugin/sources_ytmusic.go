package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

const (
	// The public innertube web-client key that YouTube Music pages ship; it is
	// not a secret. Calls are unauthenticated, mirroring how the YTM web app
	// itself loads.
	ytmusicAPIKey         = "AIzaSyC9XL3ZjWddXya6X74dJoCTL-WEYFDNX30"
	ytmusicSearchEndpoint = "https://music.youtube.com/youtubei/v1/search"
	ytmusicNextEndpoint   = "https://music.youtube.com/youtubei/v1/next"
	ytmusicBrowseEndpoint = "https://music.youtube.com/youtubei/v1/browse"
	ytmusicProviderName   = "YouTube Music"
	ytmusicClientName     = "WEB_REMIX"
	ytmusicClientVersion  = "1.20261001.01.00"
)

var (
	// The watch-next response carries the song's playlist id; browsing it
	// yields the lyrics description shelf.
	ytmusicBrowseIDPattern = regexp.MustCompile(`"browseId":"(MPLYt_[A-Za-z0-9_-]+)"`)
)

// fetchYTMusicLyricsCandidates resolves the plain, edited lyric text of the
// official YouTube Music "Lyrics" tab for the best duration-tight video:
// search, then the watch-next playlist id of the matched song, then the
// description shelf that carries the lyric body.
func fetchYTMusicLyricsCandidates(p *lyricsProvider, track lyrics.TrackInfo, deadline time.Time) ([]lyricsCandidate, lookupHealth, error) {
	title := strings.TrimSpace(track.Title)
	artist := trackArtist(track)
	if title == "" || artist == "" {
		return nil, lookupHealthy, nil
	}

	searchBody, err := json.Marshal(map[string]any{
		"context": innertubeContext(),
		"query":   title + " " + artist,
	})
	if err != nil {
		return nil, lookupDegraded, err
	}
	response, err := p.sendRequestHelper("YouTube Music search", "POST", ytmusicSearchEndpoint+"?key="+ytmusicAPIKey+"&prettyPrint=false", searchBody, innertubeHeaders(), deadline)
	if err != nil {
		return nil, lookupDegraded, err
	}
	if response.StatusCode == 404 {
		// A normal 404 carries an empty/non-JSON body: decoding it would turn
		// the clean miss into a degraded decode failure (Codex).
		return nil, lookupHealthy, nil
	}
	if err := p.checkInnertubeOK("YouTube Music search", response); err != nil {
		return nil, lookupDegraded, err
	}
	videoID, searchErr := pickYTMusicVideo(response.Body, track)
	if searchErr != nil {
		// A malformed 200 is a provider failure, not a cacheable miss
		// (Codex): the sweep must not mark the source covered on it.
		return nil, lookupDegraded, fmt.Errorf("decode YouTube Music search response: %w", searchErr)
	}
	if videoID == "" {
		return nil, lookupHealthy, nil
	}

	nextBody, err := json.Marshal(map[string]any{
		"context": innertubeContext(),
		"videoId": videoID,
	})
	if err != nil {
		return nil, lookupDegraded, err
	}
	response, err = p.sendRequestHelper("YouTube Music watch", "POST", ytmusicNextEndpoint+"?key="+ytmusicAPIKey+"&prettyPrint=false", nextBody, innertubeHeaders(), deadline)
	if err != nil {
		return nil, lookupDegraded, err
	}
	if response.StatusCode == 404 {
		return nil, lookupHealthy, nil
	}
	if err := p.checkInnertubeOK("YouTube Music watch", response); err != nil {
		return nil, lookupDegraded, err
	}
	var watchTree any
	if err := json.Unmarshal(response.Body, &watchTree); err != nil {
		// A malformed 200 must stay retryable; only a well-formed body without
		// a lyrics browse id is a cacheable miss (Codex round 13).
		return nil, lookupDegraded, fmt.Errorf("decode YouTube Music watch response: %w", err)
	}
	playlistID := firstStringMatch(response.Body, ytmusicBrowseIDPattern)
	if playlistID == "" {
		return nil, lookupHealthy, nil
	}

	browseBody, err := json.Marshal(map[string]any{
		"context":  innertubeContext(),
		"browseId": playlistID,
	})
	if err != nil {
		return nil, lookupDegraded, err
	}
	response, err = p.sendRequestHelper("YouTube Music lyrics", "POST", ytmusicBrowseEndpoint+"?key="+ytmusicAPIKey+"&prettyPrint=false", browseBody, innertubeHeaders(), deadline)
	if err != nil {
		return nil, lookupDegraded, err
	}
	if response.StatusCode == 404 {
		return nil, lookupHealthy, nil
	}
	if err := p.checkInnertubeOK("YouTube Music lyrics", response); err != nil {
		return nil, lookupDegraded, err
	}

	lines, browseErr := ytmusicDescriptionLines(response.Body)
	if browseErr != nil {
		// A malformed 200 browse body is a provider failure, not a cacheable
		// miss (Codex).
		return nil, lookupDegraded, fmt.Errorf("decode YouTube Music browse response: %w", browseErr)
	}
	text := strings.Join(lines, "\n")
	if strings.TrimSpace(text) == "" {
		return nil, lookupHealthy, nil
	}
	candidate := newLyricsCandidate(lyrics.LyricsText{Text: text}, ytmusicProviderName, "plain")
	if !candidate.present() {
		return nil, lookupHealthy, nil
	}
	return []lyricsCandidate{candidate}, lookupHealthy, nil
}

// checkInnertubeOK maps the endpoint status codes: misses leave quietly, rate
// limits record the cooldown and degrade, anything else degrades. The
// innertube endpoints report playlist states through HTTP statuses like any
// other source.
func (p *lyricsProvider) checkInnertubeOK(source string, response *host.HTTPResponse) error {
	if response == nil {
		return fmt.Errorf("%s returned no response", source)
	}
	switch response.StatusCode {
	case 200:
		return nil
	case 404:
		return nil
	case 429:
		retryAfter := retryAfterDuration(response.Headers, p.now())
		p.rememberCooldown(sourceGlobalCooldownKey("yt-music"), retryAfter)
		return rateLimitResponseError(source, response, retryAfter)
	default:
		return providerResponseError(source, response)
	}
}

func innertubeContext() map[string]any {
	return map[string]any{
		"client": map[string]any{
			"clientName":    ytmusicClientName,
			"clientVersion": ytmusicClientVersion,
			"hl":            "en",
		},
	}
}

func innertubeHeaders() map[string]string {
	return map[string]string{
		"Accept":                   "application/json",
		"Content-Type":             "application/json",
		"User-Agent":               pluginUserAgent(),
		"X-YouTube-Client-Name":    "67",
		"X-YouTube-Client-Version": ytmusicClientVersion,
	}
}

// ytmusicSearchHit is one decoded search result: the video, its rendered
// duration, and the title/artist strings from the same renderer.
type ytmusicSearchHit struct {
	videoID    string
	durationMs int64
	title      string
	artists    string
}

// pickYTMusicVideo decodes the search payload and walks song renderers, reading
// the video id, rendered duration, and identity from the same hit — serialized
// field order (arrays between videoId and lengthText) must not matter (Devin).
// A hit must match the requested identity before its duration qualifies; the
// first qualifying hit wins and unmatched searches stay misses.
func pickYTMusicVideo(body []byte, track lyrics.TrackInfo) (string, error) {
	target := int64(0)
	if track.Duration > 0 {
		target = int64(float64(track.Duration)*1000.0 + 0.5)
	}
	titleTokens := normalizedLyricTokens(track.Title)
	hits, err := ytmusicSearchHits(body)
	if err != nil {
		return "", err
	}
	for _, hit := range hits {
		if hit.videoID == "" || hit.durationMs <= 0 {
			continue
		}
		if !lyricTokensMatch(normalizedLyricTokens(hit.title), titleTokens) {
			continue
		}
		if !ytmusicArtistsMatch(hit.artists, trackArtist(track)) {
			continue
		}
		if target > 0 {
			delta := hit.durationMs - target
			if delta < -2000 || delta > 2000 {
				continue
			}
		}
		return hit.videoID, nil
	}
	return "", nil
}

// ytmusicArtistsMatch accepts the renderer when any listed artist matches the
// request completely.
func ytmusicArtistsMatch(artists, wanted string) bool {
	return artistTokensMatch(strings.ReplaceAll(artists, " • ", " & "), wanted)
}

// ytmusicSearchHits decodes the payload and collects song renderers in
// deterministic order: arrays keep index order and map keys are visited in
// sorted order, so the traversal cannot drift between runs.
func ytmusicSearchHits(body []byte) ([]ytmusicSearchHit, error) {
	var tree any
	if err := json.Unmarshal(body, &tree); err != nil {
		return nil, err
	}
	hits := []ytmusicSearchHit{}
	ytmusicWalk(tree, func(renderer map[string]any) {
		switch {
		case renderer["musicResponsiveListItemRenderer"] != nil:
			row, ok := renderer["musicResponsiveListItemRenderer"].(map[string]any)
			if !ok {
				return
			}
			hits = append(hits, ytmusicRenderHit(row))
		case renderer["videoRenderer"] != nil:
			row, ok := renderer["videoRenderer"].(map[string]any)
			if !ok {
				return
			}
			hits = append(hits, ytmusicRenderHit(row))
		}
	})
	return hits, nil
}

func ytmusicWalk(node any, visit func(map[string]any)) {
	switch cast := node.(type) {
	case map[string]any:
		visit(cast)
		keys := make([]string, 0, len(cast))
		for key := range cast {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			ytmusicWalk(cast[key], visit)
		}
	case []any:
		for _, value := range cast {
			ytmusicWalk(value, visit)
		}
	}
}

// ytmusicRenderHit reads one renderer's fields: the video id, the rendered
// length, the title column, and the artist column.
func ytmusicRenderHit(renderer map[string]any) ytmusicSearchHit {
	hit := ytmusicSearchHit{videoID: ytmusicFindString(renderer, "videoId", 6)}
	if lengthText, ok := renderer["lengthText"].(map[string]any); ok {
		if text := ytmusicFirstRunText(lengthText); text != "" {
			if parsed, err := parseYTMusicDuration(text); err == nil {
				hit.durationMs = parsed
			}
		}
	}
	if columns, ok := renderer["flexColumns"].([]any); ok {
		if len(columns) > 0 {
			hit.title = ytmusicFlexColumnText(columns[0])
		}
		if len(columns) > 1 {
			hit.artists = ytmusicFlexColumnText(columns[1])
		}
	}
	if hit.title == "" {
		if title, ok := renderer["title"].(map[string]any); ok {
			hit.title = ytmusicFirstRunText(title)
		}
	}
	return hit
}

// ytmusicFindString reaches for a nested string key (bounded depth) so the
// several places a renderer hides its video id all resolve.
func ytmusicFindString(node any, key string, depth int) string {
	if depth < 0 {
		return ""
	}
	switch cast := node.(type) {
	case map[string]any:
		if value, ok := cast[key].(string); ok && value != "" {
			return value
		}
		keys := make([]string, 0, len(cast))
		for name := range cast {
			keys = append(keys, name)
		}
		sort.Strings(keys)
		for _, name := range keys {
			if found := ytmusicFindString(cast[name], key, depth-1); found != "" {
				return found
			}
		}
	case []any:
		for _, value := range cast {
			if found := ytmusicFindString(value, key, depth-1); found != "" {
				return found
			}
		}
	}
	return ""
}

func ytmusicFlexColumnText(column any) string {
	columnMap, ok := column.(map[string]any)
	if !ok {
		return ""
	}
	renderer, ok := columnMap["musicResponsiveListItemFlexColumnRenderer"].(map[string]any)
	if !ok {
		return ""
	}
	text, ok := renderer["text"].(map[string]any)
	if !ok {
		return ""
	}
	var parts []string
	if runs, ok := text["runs"].([]any); ok {
		for _, run := range runs {
			if runMap, ok := run.(map[string]any); ok {
				if value, ok := runMap["text"].(string); ok {
					parts = append(parts, value)
				}
			}
		}
	}
	return strings.Join(parts, "")
}

func ytmusicFirstRunText(node map[string]any) string {
	runs, ok := node["runs"].([]any)
	if !ok || len(runs) == 0 {
		return ""
	}
	first, ok := runs[0].(map[string]any)
	if !ok {
		return ""
	}
	text, _ := first["text"].(string)
	return text
}

func parseYTMusicDuration(rendered string) (int64, error) {
	parts := strings.Split(rendered, ":")
	if len(parts) != 2 && len(parts) != 3 {
		return 0, fmt.Errorf("unsupported duration %q", rendered)
	}
	milliseconds := int64(0)
	for _, part := range parts {
		seconds, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return 0, fmt.Errorf("decode duration %q: %w", rendered, err)
		}
		milliseconds = milliseconds*60 + int64(seconds)
	}
	return milliseconds * 1000, nil
}

// firstStringMatch applies a compiled pattern to a body and returns the first
// captured group, or "" when nothing matched.
func firstStringMatch(body []byte, pattern *regexp.Regexp) string {
	if match := pattern.FindSubmatch(body); match != nil {
		return string(match[1])
	}
	return ""
}

// ytmusicDescriptionLines extracts the plain lyric body from the browse shelf:
// the lyric text rides musicDescriptionShelfRenderer.description.runs[].text.
// The shelf chrome (editor label, source attribution) rides the same runs and
// gets trimmed. Deeply nested responses decode generically so upstream
// re-parenting cannot break the extraction silently.
func ytmusicDescriptionLines(body []byte) ([]string, error) {
	var tree any
	if err := json.Unmarshal(body, &tree); err != nil {
		return nil, err
	}
	shelf := ytmusicFindShelf(tree)
	if shelf == nil {
		return nil, nil
	}
	description, _ := shelf["description"].(map[string]any)
	if description == nil {
		return nil, nil
	}
	var texts []string
	if runs, ok := description["runs"].([]any); ok {
		for _, run := range runs {
			if runMap, ok := run.(map[string]any); ok {
				if text, ok := runMap["text"].(string); ok && text != "" {
					texts = append(texts, text)
				}
			}
		}
	}
	for len(texts) > 0 && strings.HasPrefix(texts[len(texts)-1], "Source:") {
		texts = texts[:len(texts)-1]
	}
	for len(texts) > 0 && ytmusicChromeLine(texts[0]) {
		texts = texts[1:]
	}
	return texts, nil
}

func ytmusicFindShelf(node any) map[string]any {
	switch cast := node.(type) {
	case map[string]any:
		if shelf, ok := cast["musicDescriptionShelfRenderer"].(map[string]any); ok {
			return shelf
		}
		for _, value := range cast {
			if found := ytmusicFindShelf(value); found != nil {
				return found
			}
		}
	case []any:
		for _, value := range cast {
			if found := ytmusicFindShelf(value); found != nil {
				return found
			}
		}
	}
	return nil
}

func ytmusicChromeLine(line string) bool {
	switch line {
	case "Lyrics", "Source", "Location", "Follow on", "Shazam", "Genius", "Musixmatch", "Wikipedia":
		return true
	}
	return false
}
