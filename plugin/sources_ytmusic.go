package main

import (
	"encoding/json"
	"fmt"
	"regexp"
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
	// A search hit pairs its videoId with a rendered "m:ss"/"h:mm:ss"
	// duration; matching on duration keeps the plain text honest.
	ytmusicVideoIDPattern = regexp.MustCompile(`"videoId":"([A-Za-z0-9_-]{11})"[^[]*?"lengthText":\{"runs":\[\{"text":"(\d+:\d+(?::\d+)?)"`)
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
	if err := p.checkInnertubeOK("YouTube Music search", response); err != nil {
		return nil, lookupDegraded, err
	}
	videoID := pickYTMusicVideo(response.Body, track)
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
	if err := p.checkInnertubeOK("YouTube Music watch", response); err != nil {
		return nil, lookupDegraded, err
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
	if err := p.checkInnertubeOK("YouTube Music lyrics", response); err != nil {
		return nil, lookupDegraded, err
	}

	text := strings.Join(ytmusicDescriptionLines(response.Body), "\n")
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

// pickYTMusicVideo pairs each search-hit videoId with its rendered duration
// ("m:ss" or "h:mm:ss") and returns the first entry within two seconds of the
// requested duration. Guessing past a duration miss returns wrong plain
// lyrics, so unmatched durations stay unmatched.
func pickYTMusicVideo(body []byte, track lyrics.TrackInfo) string {
	target := int64(0)
	if track.Duration > 0 {
		target = int64(float64(track.Duration)*1000.0 + 0.5)
	}
	for _, match := range ytmusicVideoIDPattern.FindAllSubmatch(body, 32) {
		videoID := string(match[1])
		length, err := parseYTMusicDuration(string(match[2]))
		if err != nil || length <= 0 {
			continue
		}
		delta := length - target
		if target > 0 && (delta < -2000 || delta > 2000) {
			continue
		}
		return videoID
	}
	return ""
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
func ytmusicDescriptionLines(body []byte) []string {
	var tree any
	if err := json.Unmarshal(body, &tree); err != nil {
		return nil
	}
	shelf := ytmusicFindShelf(tree)
	if shelf == nil {
		return nil
	}
	description, _ := shelf["description"].(map[string]any)
	if description == nil {
		return nil
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
	return texts
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
