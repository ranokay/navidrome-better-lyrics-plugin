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

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

const (
	apiEndpoint             = "https://lyrics-api.boidu.dev/getLyrics"
	kugouEndpoint           = "https://lyrics-api.boidu.dev/kugou/getLyrics"
	unisonEndpoint          = "https://unison.boidu.dev/lyrics"
	httpTimeoutMilliseconds = int32(6_000)
	pluginRepository        = "https://github.com/ranokay/navidrome-better-lyrics-plugin"
	maximumAPIErrorRunes    = 160
)

//go:embed manifest.json
var manifestJSON []byte

type requestSender func(host.HTTPRequest) (*host.HTTPResponse, error)

type betterLyricsProvider struct {
	send requestSender
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
	return &betterLyricsProvider{send: send}
}

// GetLyrics prefers rich TTML, then synchronized community/provider results,
// while retaining Unison plain text as the final fallback.
func (p *betterLyricsProvider) GetLyrics(input lyrics.GetLyricsRequest) (lyrics.GetLyricsResponse, error) {
	requestURL, ok := lyricsRequestURL(input.Track)
	if !ok {
		return lyrics.GetLyricsResponse{}, nil
	}

	headers := map[string]string{
		"Accept":     "application/json",
		"User-Agent": pluginUserAgent(),
	}

	if p.send == nil {
		return lyrics.GetLyricsResponse{}, fmt.Errorf("better lyrics request sender is not configured")
	}

	var lookupErrors []error
	betterLyricsAvailable := true
	text, retryWithoutOptionalMetadata, err := p.fetchBetterLyricsTTML(requestURL, headers)
	if err != nil {
		lookupErrors = append(lookupErrors, err)
		betterLyricsAvailable = false
	} else if text != "" {
		return lyrics.GetLyricsResponse{Lyrics: []lyrics.LyricsText{{Text: text}}}, nil
	}

	minimalURL, _ := lyricsRequestURLWithoutOptionalMetadata(input.Track)
	if betterLyricsAvailable && retryWithoutOptionalMetadata && minimalURL != requestURL {
		text, _, err = p.fetchBetterLyricsTTML(minimalURL, headers)
		if err != nil {
			lookupErrors = append(lookupErrors, err)
			betterLyricsAvailable = false
		} else if text != "" {
			return lyrics.GetLyricsResponse{Lyrics: []lyrics.LyricsText{{Text: text}}}, nil
		}
	}

	var plainFallback lyrics.LyricsText
	unisonURL, _ := unisonRequestURL(input.Track)
	unisonLyrics, unisonFormat, err := p.fetchUnisonLyrics(unisonURL, headers)
	if err != nil {
		lookupErrors = append(lookupErrors, err)
	} else if unisonLyrics.Text != "" {
		if unisonFormat != "plain" {
			return lyrics.GetLyricsResponse{Lyrics: []lyrics.LyricsText{unisonLyrics}}, nil
		}
		plainFallback = unisonLyrics
	}

	if betterLyricsAvailable {
		kugouURL, _ := kugouRequestURL(input.Track)
		kugouLyrics, err := p.fetchKugouLyrics(kugouURL, headers)
		if err != nil {
			lookupErrors = append(lookupErrors, err)
		} else if kugouLyrics.Text != "" {
			return lyrics.GetLyricsResponse{Lyrics: []lyrics.LyricsText{kugouLyrics}}, nil
		}
	}

	if plainFallback.Text != "" {
		return lyrics.GetLyricsResponse{Lyrics: []lyrics.LyricsText{plainFallback}}, nil
	}
	if len(lookupErrors) > 0 {
		return lyrics.GetLyricsResponse{}, errors.Join(lookupErrors...)
	}
	return lyrics.GetLyricsResponse{}, nil
}

func (p *betterLyricsProvider) fetchBetterLyricsTTML(requestURL string, headers map[string]string) (string, bool, error) {
	response, err := p.send(host.HTTPRequest{
		Method:    "GET",
		URL:       requestURL,
		Headers:   headers,
		TimeoutMs: httpTimeoutMilliseconds,
	})
	if err != nil {
		return "", false, fmt.Errorf("request Better Lyrics API: %w", err)
	}
	if response == nil {
		return "", false, fmt.Errorf("better lyrics API returned no response")
	}

	switch response.StatusCode {
	case 200:
		var payload apiResponse
		if err := json.Unmarshal(response.Body, &payload); err != nil {
			return "", false, fmt.Errorf("decode Better Lyrics API response: %w", err)
		}
		if strings.TrimSpace(payload.TTML) == "" {
			return "", false, nil
		}
		return payload.TTML, false, nil
	case 401, 404:
		return "", true, nil
	default:
		return "", false, apiResponseError(response)
	}
}

func (p *betterLyricsProvider) fetchUnisonLyrics(requestURL string, headers map[string]string) (lyrics.LyricsText, string, error) {
	response, err := p.sendRequest("Unison API", requestURL, headers)
	if err != nil {
		return lyrics.LyricsText{}, "", err
	}

	switch response.StatusCode {
	case 200:
		var payload unisonAPIResponse
		if err := json.Unmarshal(response.Body, &payload); err != nil {
			return lyrics.LyricsText{}, "", fmt.Errorf("decode Unison API response: %w", err)
		}
		if !payload.Success || payload.Data == nil {
			return lyrics.LyricsText{}, "", fmt.Errorf("decode Unison API response: missing successful lyrics data")
		}
		if strings.TrimSpace(payload.Data.Lyrics) == "" {
			return lyrics.LyricsText{}, "", nil
		}

		format := strings.ToLower(strings.TrimSpace(payload.Data.Format))
		switch format {
		case "ttml", "lrc", "plain":
			return lyrics.LyricsText{
				Lang: strings.TrimSpace(payload.Data.Language),
				Text: payload.Data.Lyrics,
			}, format, nil
		default:
			return lyrics.LyricsText{}, "", fmt.Errorf("unsupported Unison lyrics format %q", payload.Data.Format)
		}
	case 404:
		return lyrics.LyricsText{}, "", nil
	default:
		return lyrics.LyricsText{}, "", providerResponseError("Unison API", response)
	}
}

func (p *betterLyricsProvider) fetchKugouLyrics(requestURL string, headers map[string]string) (lyrics.LyricsText, error) {
	response, err := p.sendRequest("Better Lyrics Kugou API", requestURL, headers)
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
	default:
		return lyrics.LyricsText{}, providerResponseError("Better Lyrics Kugou API", response)
	}
}

func (p *betterLyricsProvider) sendRequest(source, requestURL string, headers map[string]string) (*host.HTTPResponse, error) {
	response, err := p.send(host.HTTPRequest{
		Method:    "GET",
		URL:       requestURL,
		Headers:   headers,
		TimeoutMs: httpTimeoutMilliseconds,
	})
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", source, err)
	}
	if response == nil {
		return nil, fmt.Errorf("%s returned no response", source)
	}
	return response, nil
}

func lyricsRequestURL(track lyrics.TrackInfo) (string, bool) {
	return buildBetterLyricsRequestURL(track, true)
}

func lyricsRequestURLWithoutOptionalMetadata(track lyrics.TrackInfo) (string, bool) {
	return buildBetterLyricsRequestURL(track, false)
}

func kugouRequestURL(track lyrics.TrackInfo) (string, bool) {
	return buildBetterLyricsProviderRequestURL(kugouEndpoint, track, false)
}

func buildBetterLyricsRequestURL(track lyrics.TrackInfo, includeOptionalMetadata bool) (string, bool) {
	return buildBetterLyricsProviderRequestURL(apiEndpoint, track, includeOptionalMetadata)
}

func buildBetterLyricsProviderRequestURL(endpoint string, track lyrics.TrackInfo, includeOptionalMetadata bool) (string, bool) {
	title := strings.TrimSpace(track.Title)
	artist := trackArtist(track)
	if title == "" || artist == "" {
		return "", false
	}

	query := url.Values{
		"a": {artist},
		"s": {title},
	}
	if includeOptionalMetadata {
		if album := strings.TrimSpace(track.Album); album != "" {
			query.Set("al", album)
		}
		if duration := int64(math.Round(float64(track.Duration))); duration > 0 {
			query.Set("d", strconv.FormatInt(duration, 10))
		}
	}

	return endpoint + "?" + query.Encode(), true
}

func unisonRequestURL(track lyrics.TrackInfo) (string, bool) {
	title := strings.TrimSpace(track.Title)
	artist := trackArtist(track)
	if title == "" || artist == "" {
		return "", false
	}

	query := url.Values{
		"artist": {artist},
		"song":   {title},
	}
	if duration := int64(math.Round(float64(track.Duration))); duration > 0 {
		query.Set("duration", strconv.FormatInt(duration, 10))
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

func providerResponseError(source string, response *host.HTTPResponse) error {
	var payload apiErrorResponse
	_ = json.Unmarshal(response.Body, &payload)

	detail := strings.TrimSpace(payload.Error)
	if detail == "" {
		detail = strings.TrimSpace(payload.Message)
	}
	if detail == "" {
		return fmt.Errorf("%s returned HTTP %d", source, response.StatusCode)
	}

	detailRunes := []rune(detail)
	if len(detailRunes) > maximumAPIErrorRunes {
		detail = string(detailRunes[:maximumAPIErrorRunes]) + "…"
	}
	return fmt.Errorf("%s returned HTTP %d: %s", source, response.StatusCode, detail)
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
