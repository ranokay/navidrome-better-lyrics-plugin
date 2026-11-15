package source

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
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

// fetchYTMusic resolves the plain, edited lyric text of the
// official YouTube Music "Lyrics" tab for the best duration-tight video:
// search, then the watch-next playlist id of the matched song, then the
// description shelf that carries the lyric body.
func fetchYTMusic(rt Runtime, track lyrics.TrackInfo, deadline time.Time) ([]lyric.Candidate, Health, error) {
	title := strings.TrimSpace(track.Title)
	artist := lyric.TrackArtist(track)
	if title == "" || artist == "" {
		return nil, Healthy, nil
	}

	searchBody, err := json.Marshal(map[string]any{
		"context": innertubeContext(),
		"query":   title + " " + artist,
		// The songs filter includes recording duration in metadata runs.
		"params": "EgWKAQIIAWoMEA4QChADEAQQCRAF",
	})
	if err != nil {
		return nil, Degraded, err
	}
	response, err := rt.Do("YouTube Music search", "POST", ytmusicSearchEndpoint+"?key="+ytmusicAPIKey+"&prettyPrint=false", searchBody, innertubeHeaders(rt), deadline)
	if err != nil {
		return nil, Degraded, err
	}
	if response.StatusCode == 404 {
		// A normal 404 carries an empty/non-JSON body: decoding it would turn
		// the clean miss into a degraded decode failure.
		return nil, Healthy, nil
	}
	if err := checkInnertubeOK(rt, "YouTube Music search", response); err != nil {
		return nil, Degraded, err
	}
	videoID, searchErr := pickYTMusicVideo(response.Body, track)
	if searchErr != nil {
		// A malformed 200 is a provider failure, not a cacheable miss:
		// the sweep must not mark the source covered on it.
		return nil, Degraded, fmt.Errorf("decode YouTube Music search response: %w", searchErr)
	}
	if videoID == "" {
		return nil, Healthy, nil
	}

	nextBody, err := json.Marshal(map[string]any{
		"context": innertubeContext(),
		"videoId": videoID,
	})
	if err != nil {
		return nil, Degraded, err
	}
	response, err = rt.Do("YouTube Music watch", "POST", ytmusicNextEndpoint+"?key="+ytmusicAPIKey+"&prettyPrint=false", nextBody, innertubeHeaders(rt), deadline)
	if err != nil {
		return nil, Degraded, err
	}
	if response.StatusCode == 404 {
		return nil, Healthy, nil
	}
	if err := checkInnertubeOK(rt, "YouTube Music watch", response); err != nil {
		return nil, Degraded, err
	}
	var watchTree any
	if err := json.Unmarshal(response.Body, &watchTree); err != nil {
		// A malformed 200 must stay retryable; only a well-formed body without
		// a lyrics browse id is a cacheable miss.
		return nil, Degraded, fmt.Errorf("decode YouTube Music watch response: %w", err)
	}
	playlistID := ""
	ytmusicWalk(watchTree, func(node map[string]any) {
		if id, ok := node["browseId"].(string); ok && playlistID == "" && strings.HasPrefix(id, "MPLYt_") && len(id) > len("MPLYt_") {
			playlistID = id
		}
	})
	if playlistID == "" {
		return nil, Healthy, nil
	}

	browseBody, err := json.Marshal(map[string]any{
		"context":  innertubeContext(),
		"browseId": playlistID,
	})
	if err != nil {
		return nil, Degraded, err
	}
	response, err = rt.Do("YouTube Music lyrics", "POST", ytmusicBrowseEndpoint+"?key="+ytmusicAPIKey+"&prettyPrint=false", browseBody, innertubeHeaders(rt), deadline)
	if err != nil {
		return nil, Degraded, err
	}
	if response.StatusCode == 404 {
		return nil, Healthy, nil
	}
	if err := checkInnertubeOK(rt, "YouTube Music lyrics", response); err != nil {
		return nil, Degraded, err
	}

	lines, browseErr := ytmusicDescriptionLines(response.Body)
	if browseErr != nil {
		// A malformed 200 browse body is a provider failure, not a cacheable
		// miss.
		return nil, Degraded, fmt.Errorf("decode YouTube Music browse response: %w", browseErr)
	}
	text := strings.Join(lines, "\n")
	if strings.TrimSpace(text) == "" {
		return nil, Healthy, nil
	}
	candidate := lyric.NewCandidate(lyrics.LyricsText{Text: text}, ytmusicProviderName, "plain")
	if !candidate.Present() {
		return nil, Healthy, nil
	}
	return []lyric.Candidate{candidate}, Healthy, nil
}

// checkInnertubeOK maps the endpoint status codes: misses leave quietly, rate
// limits record the cooldown and degrade, anything else degrades. The
// innertube endpoints report playlist states through HTTP statuses like any
// other source.
func checkInnertubeOK(rt Runtime, source string, response *host.HTTPResponse) error {
	if response == nil {
		return fmt.Errorf("%s returned no response", source)
	}
	switch response.StatusCode {
	case 200:
		return nil
	case 404:
		return nil
	case 429:
		retryAfter := retryAfterDuration(response.Headers, rt.Now())
		rt.RememberCooldown(GlobalCooldownKey("yt-music"), retryAfter)
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

func innertubeHeaders(rt Runtime) map[string]string {
	return map[string]string{
		"Accept":                   "application/json",
		"Content-Type":             "application/json",
		"User-Agent":               rt.DefaultHeaders()["User-Agent"],
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
// field order (arrays between videoId and lengthText) must not matter.
// A hit must match the requested identity before its duration qualifies; the
// first qualifying hit wins and unmatched searches stay misses.
func pickYTMusicVideo(body []byte, track lyrics.TrackInfo) (string, error) {
	target := int64(0)
	if track.Duration > 0 {
		target = int64(float64(track.Duration)*1000.0 + 0.5)
	}
	titleTokens := lyric.NormalizedTokens(track.Title)
	hits, err := ytmusicSearchHits(body)
	if err != nil {
		return "", err
	}
	for _, hit := range hits {
		if hit.videoID == "" || hit.durationMs <= 0 {
			continue
		}
		if !lyric.TokensMatch(lyric.NormalizedTokens(hit.title), titleTokens) {
			continue
		}
		if !ytmusicArtistsMatch(hit.artists, lyric.TrackArtist(track)) {
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
	return lyric.ArtistMatch(strings.ReplaceAll(artists, " • ", " & "), wanted)
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
			// Older renderer shapes used a single plain artist label. Current
			// metadata combines artist, album and duration, so linked roles must
			// determine identity instead of matching the concatenated column.
			artistRuns := ytmusicFlexColumnRuns(columns[1])
			if len(artistRuns) == 1 {
				run, _ := artistRuns[0].(map[string]any)
				if run["navigationEndpoint"] == nil {
					plain := ytmusicFlexColumnText(columns[1])
					if !strings.Contains(plain, " • ") {
						hit.artists = plain
					}
				}
			}
			var artists []string
			for _, column := range columns[1:] {
				for _, raw := range ytmusicFlexColumnRuns(column) {
					run, _ := raw.(map[string]any)
					value, _ := run["text"].(string)
					endpoint, _ := run["navigationEndpoint"].(map[string]any)
					browse, _ := endpoint["browseEndpoint"].(map[string]any)
					id, _ := browse["browseId"].(string)
					if strings.HasPrefix(id, "UC") {
						artists = append(artists, value)
					}
					if hit.durationMs == 0 && run["navigationEndpoint"] == nil {
						if duration, err := parseYTMusicDuration(value); err == nil {
							hit.durationMs = duration
						}
					}
				}
			}
			if len(artists) > 0 {
				hit.artists = strings.Join(artists, " & ")
			}
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

func ytmusicFlexColumnRuns(column any) []any {
	columnMap, ok := column.(map[string]any)
	if !ok {
		return nil
	}
	renderer, ok := columnMap["musicResponsiveListItemFlexColumnRenderer"].(map[string]any)
	if !ok {
		return nil
	}
	text, ok := renderer["text"].(map[string]any)
	if !ok {
		return nil
	}
	runs, _ := text["runs"].([]any)
	return runs
}

func ytmusicFlexColumnText(column any) string {
	var parts []string
	for _, run := range ytmusicFlexColumnRuns(column) {
		if runMap, ok := run.(map[string]any); ok {
			if value, ok := runMap["text"].(string); ok {
				parts = append(parts, value)
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

var errYTMusicDuration = errors.New("invalid YouTube Music duration")

func parseYTMusicDuration(rendered string) (int64, error) {
	parts := strings.Split(rendered, ":")
	if len(parts) != 2 && len(parts) != 3 {
		return 0, errYTMusicDuration
	}
	const maximumSeconds = uint64(math.MaxInt64 / 1000)
	var seconds uint64
	for i, part := range parts {
		component, err := strconv.ParseUint(strings.TrimSpace(part), 10, 63)
		if err != nil || component > maximumSeconds || i > 0 && component >= 60 || seconds > (maximumSeconds-component)/60 {
			return 0, errYTMusicDuration
		}
		seconds = seconds*60 + component
	}
	return int64(seconds * 1000), nil
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
