package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

const (
	apiEndpoint              = "https://lyrics-api.boidu.dev/getLyrics"
	kugouEndpoint            = "https://lyrics-api.boidu.dev/kugou/getLyrics"
	unisonEndpoint           = "https://unison.boidu.dev/lyrics"
	httpTimeoutMilliseconds  = int32(6_000)
	lyricsLookupTimeout      = 15 * time.Second
	pluginRepository         = "https://github.com/ranokay/navidrome-better-lyrics-plugin"
	maximumAPIErrorRunes     = 160
	betterLyricsProviderName = "ttml"
)

var errLyricsLookupBudgetExhausted = errors.New("lyrics lookup time budget exhausted")

//go:embed manifest.json
var manifestJSON []byte

type requestSender func(host.HTTPRequest) (*host.HTTPResponse, error)

type betterLyricsProvider struct {
	send  requestSender
	cache lyricsCacheStore
	now   func() time.Time
}

type apiResponse struct {
	TTML string `json:"ttml"`
}

type providerAPIResponse struct {
	Lyrics string `json:"lyrics"`
}

type unisonAPIResponse struct {
	Success bool               `json:"success"`
	Data    *unisonLyricsEntry `json:"data"`
}

type unisonLyricsEntry struct {
	Lyrics   string `json:"lyrics"`
	Format   string `json:"format"`
	Language string `json:"language"`
}

type apiErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

var _ lyrics.Lyrics = (*betterLyricsProvider)(nil)

func init() {
	lyrics.Register(newBetterLyricsProvider(host.HTTPSend))
}

func main() {}

func newBetterLyricsProvider(send requestSender) *betterLyricsProvider {
	return newBetterLyricsProviderWithDependencies(send, defaultLyricsCache(), time.Now)
}

// GetLyrics prefers rich TTML, then synchronized community/provider results,
// while retaining Unison plain text as the final fallback.
func (p *betterLyricsProvider) GetLyrics(input lyrics.GetLyricsRequest) (lyrics.GetLyricsResponse, error) {
	if cached, ok := p.readCachedLyrics(input.Track); ok {
		return cached, nil
	}

	response, health, err := p.fetchLyrics(input)
	if err == nil {
		p.cacheLyrics(input.Track, response, health)
	}
	return response, err
}

func (p *betterLyricsProvider) fetchLyrics(input lyrics.GetLyricsRequest) (lyrics.GetLyricsResponse, lookupHealth, error) {
	fullURL, ok := lyricsRequestURL(input.Track)
	if !ok {
		return lyrics.GetLyricsResponse{}, lookupHealthy, nil
	}

	headers := map[string]string{
		"Accept":     "application/json",
		"User-Agent": pluginUserAgent(),
	}

	if p.send == nil {
		return lyrics.GetLyricsResponse{}, lookupDegraded, fmt.Errorf("better lyrics request sender is not configured")
	}

	deadline := p.now().Add(lyricsLookupTimeout)
	var lookupErrors []error
	health := lookupHealthy
	betterLyricsGlobalCooldownReported := false
	var best lyricsCandidate

	betterURLs := uniqueRequestURLs(
		fullURL,
		mustRequestURL(lyricsRequestURLWithoutAlbum(input.Track)),
		mustRequestURL(lyricsRequestURLWithoutOptionalMetadata(input.Track)),
	)
	retryBroader := true
	for i, requestURL := range betterURLs {
		if i > 0 && !retryBroader {
			break
		}
		text, retry, uncertain, err := p.fetchBetterLyricsTTML(requestURL, headers, deadline)
		retryBroader = retry
		if uncertain {
			health = health.merge(lookupUncertain)
		}
		if err != nil {
			lookupErrors = append(lookupErrors, err)
			health = health.merge(lookupDegraded)
			betterLyricsGlobalCooldownReported = betterLyricsGlobalCooldownReported || isGlobalRateLimitError(err)
			continue
		}
		if text == "" {
			continue
		}

		candidate := newLyricsCandidate(lyrics.LyricsText{Text: text}, betterLyricsProviderName, "ttml")
		if !candidate.present() {
			lookupErrors = append(lookupErrors, fmt.Errorf("better lyrics API returned malformed TTML"))
			health = health.merge(lookupDegraded)
			continue
		}
		best = preferLyricsCandidate(best, candidate)
		if best.quality == timingSyllable {
			return best.response, health, nil
		}
	}

	unisonURLs := uniqueRequestURLs(
		mustRequestURL(unisonRequestURLWithFullMetadata(input.Track)),
		mustRequestURL(unisonRequestURL(input.Track)),
		mustRequestURL(unisonRequestURLWithoutOptionalMetadata(input.Track)),
	)
	retryBroader = true
	for i, requestURL := range unisonURLs {
		if i > 0 && !retryBroader {
			break
		}
		unisonLyrics, unisonFormat, retry, err := p.fetchUnisonLyrics(requestURL, headers, deadline)
		retryBroader = retry
		if err != nil {
			lookupErrors = append(lookupErrors, err)
			health = health.merge(lookupDegraded)
			continue
		}
		if unisonLyrics.Text == "" {
			continue
		}

		candidate := newLyricsCandidate(unisonLyrics, "unison", unisonFormat)
		if !candidate.present() {
			lookupErrors = append(lookupErrors, fmt.Errorf("unison API returned malformed %s lyrics", strings.ToUpper(unisonFormat)))
			health = health.merge(lookupDegraded)
			continue
		}
		best = preferLyricsCandidate(best, candidate)
		if best.quality == timingSyllable {
			return best.response, health, nil
		}
	}

	if (!best.present() || best.quality < timingLine) && !betterLyricsGlobalCooldownReported {
		kugouURL, _ := kugouRequestURL(input.Track)
		kugouLyrics, err := p.fetchKugouLyrics(kugouURL, headers, deadline)
		if err != nil {
			lookupErrors = append(lookupErrors, err)
			health = health.merge(lookupDegraded)
		} else if kugouLyrics.Text != "" {
			best = preferLyricsCandidate(best, newLyricsCandidate(kugouLyrics, "kugou", "lrc"))
		}
	}

	if best.present() {
		return best.response, health, nil
	}
	if len(lookupErrors) > 0 {
		return lyrics.GetLyricsResponse{}, health, errors.Join(lookupErrors...)
	}
	return lyrics.GetLyricsResponse{}, health, nil
}

func uniqueRequestURLs(urls ...string) []string {
	seen := make(map[string]struct{}, len(urls))
	result := make([]string, 0, len(urls))
	for _, requestURL := range urls {
		if requestURL == "" {
			continue
		}
		if _, exists := seen[requestURL]; exists {
			continue
		}
		seen[requestURL] = struct{}{}
		result = append(result, requestURL)
	}
	return result
}

func mustRequestURL(requestURL string, ok bool) string {
	if !ok {
		return ""
	}
	return requestURL
}

func sourcedLyricsResponse(text lyrics.LyricsText, provider, format string) lyrics.GetLyricsResponse {
	return lyrics.GetLyricsResponse{
		Lyrics: []lyrics.LyricsText{text},
		Source: &lyrics.LyricsSource{
			Provider: provider,
			Format:   format,
		},
	}
}

func (p *betterLyricsProvider) fetchBetterLyricsTTML(requestURL string, headers map[string]string, deadline time.Time) (string, bool, bool, error) {
	if cooldownErr := p.activeBetterLyricsCooldown(requestURL); cooldownErr != nil {
		return "", !isGlobalRateLimitError(cooldownErr), false, cooldownErr
	}
	timeout, err := p.requestTimeoutMilliseconds(deadline)
	if err != nil {
		return "", false, false, err
	}
	response, err := p.send(host.HTTPRequest{
		Method:    "GET",
		URL:       requestURL,
		Headers:   headers,
		TimeoutMs: timeout,
	})
	if err != nil {
		return "", false, false, fmt.Errorf("request Better Lyrics API: %w", err)
	}
	if response == nil {
		return "", false, false, fmt.Errorf("better lyrics API returned no response")
	}

	switch response.StatusCode {
	case 200:
		var payload apiResponse
		if err := json.Unmarshal(response.Body, &payload); err != nil {
			return "", false, false, fmt.Errorf("decode Better Lyrics API response: %w", err)
		}
		if strings.TrimSpace(payload.TTML) == "" {
			// An empty TTML payload is a clean miss for this query, so broader
			// metadata tiers may still find a usable cached entry.
			return "", true, false, nil
		}
		return payload.TTML, false, false, nil
	case 401:
		return "", true, true, nil
	case 404:
		return "", true, false, nil
	case 429:
		retryAfter := p.rememberRateLimit(response, requestURL)
		return "", !globalRateLimit(response), false, rateLimitResponseError("better lyrics API", response, retryAfter)
	default:
		return "", false, false, apiResponseError(response)
	}
}

func (p *betterLyricsProvider) fetchUnisonLyrics(requestURL string, headers map[string]string, deadline time.Time) (lyrics.LyricsText, string, bool, error) {
	if cooldownErr := p.activeUnisonCooldown(); cooldownErr != nil {
		return lyrics.LyricsText{}, "", false, cooldownErr
	}
	response, err := p.sendRequest("Unison API", requestURL, headers, deadline)
	if err != nil {
		return lyrics.LyricsText{}, "", false, err
	}

	switch response.StatusCode {
	case 200:
		var payload unisonAPIResponse
		if err := json.Unmarshal(response.Body, &payload); err != nil {
			return lyrics.LyricsText{}, "", false, fmt.Errorf("decode Unison API response: %w", err)
		}
		if !payload.Success || payload.Data == nil {
			return lyrics.LyricsText{}, "", false, fmt.Errorf("decode Unison API response: missing successful lyrics data")
		}
		if strings.TrimSpace(payload.Data.Lyrics) == "" {
			return lyrics.LyricsText{}, "", true, nil
		}

		format := strings.ToLower(strings.TrimSpace(payload.Data.Format))
		switch format {
		case "ttml", "lrc", "plain":
			return lyrics.LyricsText{
				Lang: strings.TrimSpace(payload.Data.Language),
				Text: payload.Data.Lyrics,
			}, format, false, nil
		default:
			return lyrics.LyricsText{}, "", false, fmt.Errorf("unsupported Unison lyrics format %q", payload.Data.Format)
		}
	case 404:
		return lyrics.LyricsText{}, "", true, nil
	case 429:
		retryAfter := p.rememberUnisonRateLimit(response)
		return lyrics.LyricsText{}, "", false, providerRateLimitResponseError("Unison API", response, retryAfter)
	default:
		return lyrics.LyricsText{}, "", false, providerResponseError("Unison API", response)
	}
}

func (p *betterLyricsProvider) fetchKugouLyrics(requestURL string, headers map[string]string, deadline time.Time) (lyrics.LyricsText, error) {
	if cooldownErr := p.activeBetterLyricsCooldown(requestURL); cooldownErr != nil {
		return lyrics.LyricsText{}, cooldownErr
	}
	response, err := p.sendRequest("Better Lyrics Kugou API", requestURL, headers, deadline)
	if err != nil {
		return lyrics.LyricsText{}, err
	}

	switch response.StatusCode {
	case 200:
		var payload providerAPIResponse
		if err := json.Unmarshal(response.Body, &payload); err != nil {
			return lyrics.LyricsText{}, fmt.Errorf("decode Better Lyrics Kugou API response: %w", err)
		}
		if strings.TrimSpace(payload.Lyrics) == "" {
			return lyrics.LyricsText{}, nil
		}
		return lyrics.LyricsText{Text: payload.Lyrics}, nil
	case 401, 404:
		return lyrics.LyricsText{}, nil
	case 429:
		retryAfter := p.rememberRateLimit(response, requestURL)
		return lyrics.LyricsText{}, rateLimitResponseError("Better Lyrics Kugou API", response, retryAfter)
	default:
		return lyrics.LyricsText{}, providerResponseError("Better Lyrics Kugou API", response)
	}
}

func (p *betterLyricsProvider) sendRequest(source, requestURL string, headers map[string]string, deadline time.Time) (*host.HTTPResponse, error) {
	timeout, err := p.requestTimeoutMilliseconds(deadline)
	if err != nil {
		return nil, err
	}
	response, err := p.send(host.HTTPRequest{
		Method:    "GET",
		URL:       requestURL,
		Headers:   headers,
		TimeoutMs: timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", source, err)
	}
	if response == nil {
		return nil, fmt.Errorf("%s returned no response", source)
	}
	return response, nil
}

func (p *betterLyricsProvider) requestTimeoutMilliseconds(deadline time.Time) (int32, error) {
	remaining := deadline.Sub(p.now())
	if remaining <= 0 {
		return 0, errLyricsLookupBudgetExhausted
	}
	perRequest := time.Duration(httpTimeoutMilliseconds) * time.Millisecond
	if remaining < perRequest {
		perRequest = remaining
	}
	milliseconds := (perRequest + time.Millisecond - 1) / time.Millisecond
	if milliseconds < 1 {
		milliseconds = 1
	}
	return int32(milliseconds), nil
}

func lyricsRequestURL(track lyrics.TrackInfo) (string, bool) {
	return buildBetterLyricsProviderRequestURLWithMetadata(apiEndpoint, track, true, true)
}

func lyricsRequestURLWithoutAlbum(track lyrics.TrackInfo) (string, bool) {
	return buildBetterLyricsProviderRequestURLWithMetadata(apiEndpoint, track, false, true)
}

func lyricsRequestURLWithoutOptionalMetadata(track lyrics.TrackInfo) (string, bool) {
	return buildBetterLyricsProviderRequestURLWithMetadata(apiEndpoint, track, false, false)
}

func kugouRequestURL(track lyrics.TrackInfo) (string, bool) {
	return buildBetterLyricsProviderRequestURLWithMetadata(kugouEndpoint, track, false, false)
}

func buildBetterLyricsRequestURL(track lyrics.TrackInfo, includeOptionalMetadata bool) (string, bool) {
	return buildBetterLyricsProviderRequestURL(apiEndpoint, track, includeOptionalMetadata)
}

func buildBetterLyricsProviderRequestURL(endpoint string, track lyrics.TrackInfo, includeOptionalMetadata bool) (string, bool) {
	return buildBetterLyricsProviderRequestURLWithMetadata(endpoint, track, includeOptionalMetadata, includeOptionalMetadata)
}

func buildBetterLyricsProviderRequestURLWithMetadata(endpoint string, track lyrics.TrackInfo, includeAlbum, includeDuration bool) (string, bool) {
	title := strings.TrimSpace(track.Title)
	artist := trackArtist(track)
	if title == "" || artist == "" {
		return "", false
	}

	query := url.Values{
		"a": {artist},
		"s": {title},
	}
	if includeAlbum {
		if album := strings.TrimSpace(track.Album); album != "" {
			query.Set("al", album)
		}
	}
	if includeDuration {
		if duration := int64(math.Round(float64(track.Duration))); duration > 0 {
			query.Set("d", strconv.FormatInt(duration, 10))
		}
	}

	return endpoint + "?" + query.Encode(), true
}

// unisonRequestURL preserves the pre-existing duration-only query shape. The
// provider first tries unisonRequestURLWithFullMetadata, then this broader form.
func unisonRequestURL(track lyrics.TrackInfo) (string, bool) {
	return buildUnisonRequestURL(track, false, true)
}

func unisonRequestURLWithFullMetadata(track lyrics.TrackInfo) (string, bool) {
	return buildUnisonRequestURL(track, true, true)
}

func unisonRequestURLWithoutOptionalMetadata(track lyrics.TrackInfo) (string, bool) {
	return buildUnisonRequestURL(track, false, false)
}

func buildUnisonRequestURL(track lyrics.TrackInfo, includeAlbum, includeDuration bool) (string, bool) {
	title := strings.TrimSpace(track.Title)
	artist := trackArtist(track)
	if title == "" || artist == "" {
		return "", false
	}

	query := url.Values{
		"artist": {artist},
		"song":   {title},
	}
	if includeAlbum {
		if album := strings.TrimSpace(track.Album); album != "" {
			query.Set("album", album)
		}
	}
	if includeDuration {
		if duration := int64(math.Round(float64(track.Duration))); duration > 0 {
			query.Set("duration", strconv.FormatInt(duration, 10))
		}
	}
	return unisonEndpoint + "?" + query.Encode(), true
}

func trackArtist(track lyrics.TrackInfo) string {
	if artist := strings.TrimSpace(track.Artist); artist != "" {
		return artist
	}

	artists := make([]string, 0, len(track.Artists))
	for _, artist := range track.Artists {
		if name := strings.TrimSpace(artist.Name); name != "" {
			artists = append(artists, name)
		}
	}
	return strings.Join(artists, ", ")
}

func apiResponseError(response *host.HTTPResponse) error {
	return providerResponseError("better lyrics API", response)
}

type providerHTTPError struct {
	source            string
	status            int32
	detail            string
	retryAfterSeconds int64
	globalRateLimit   bool
}

func (e *providerHTTPError) Error() string {
	detail := ""
	if e.detail != "" {
		detail = ": " + e.detail
	}
	retry := ""
	if e.retryAfterSeconds > 0 {
		retry = fmt.Sprintf("; retry after %ds", e.retryAfterSeconds)
	}
	return fmt.Sprintf("%s returned HTTP %d%s%s", e.source, e.status, detail, retry)
}

func providerResponseError(source string, response *host.HTTPResponse) error {
	var payload apiErrorResponse
	_ = json.Unmarshal(response.Body, &payload)

	detail := strings.TrimSpace(payload.Error)
	if detail == "" {
		detail = strings.TrimSpace(payload.Message)
	}
	detailRunes := []rune(detail)
	if len(detailRunes) > maximumAPIErrorRunes {
		detail = string(detailRunes[:maximumAPIErrorRunes]) + "…"
	}
	return &providerHTTPError{source: source, status: response.StatusCode, detail: detail}
}

func rateLimitResponseError(source string, response *host.HTTPResponse, retryAfter time.Duration) error {
	err := providerRateLimitResponseError(source, response, retryAfter)
	responseErr, ok := err.(*providerHTTPError)
	if ok {
		responseErr.globalRateLimit = globalRateLimit(response)
	}
	return err
}

func providerRateLimitResponseError(source string, response *host.HTTPResponse, retryAfter time.Duration) error {
	err := providerResponseError(source, response)
	responseErr, ok := err.(*providerHTTPError)
	if ok {
		responseErr.retryAfterSeconds = durationSecondsCeil(retryAfter)
	}
	return err
}

func isGlobalRateLimitError(err error) bool {
	var responseErr *providerHTTPError
	if errors.As(err, &responseErr) {
		return responseErr.status == 429 && responseErr.globalRateLimit
	}
	var cooldownErr *rateLimitCooldownError
	return errors.As(err, &cooldownErr) && cooldownErr.global
}

func pluginUserAgent() string {
	return "NavidromeBetterLyricsPlugin/" + pluginVersion() + " (+" + pluginRepository + ")"
}

func pluginVersion() string {
	var manifest struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil || manifest.Version == "" {
		return "0.0.0"
	}
	return manifest.Version
}
