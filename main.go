package main

import (
	_ "embed"
	"encoding/json"
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
	httpTimeoutMilliseconds = int32(10_000)
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

// GetLyrics returns Better Lyrics TTML unchanged so Navidrome can parse every
// timing and metadata layer through its normal lyrics pipeline.
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

	response, err := p.send(host.HTTPRequest{
		Method:    "GET",
		URL:       requestURL,
		Headers:   headers,
		TimeoutMs: httpTimeoutMilliseconds,
	})
	if err != nil {
		return lyrics.GetLyricsResponse{}, fmt.Errorf("request Better Lyrics API: %w", err)
	}
	if response == nil {
		return lyrics.GetLyricsResponse{}, fmt.Errorf("better lyrics API returned no response")
	}

	switch response.StatusCode {
	case 200:
		var payload apiResponse
		if err := json.Unmarshal(response.Body, &payload); err != nil {
			return lyrics.GetLyricsResponse{}, fmt.Errorf("decode Better Lyrics API response: %w", err)
		}
		if strings.TrimSpace(payload.TTML) == "" {
			return lyrics.GetLyricsResponse{}, nil
		}
		return lyrics.GetLyricsResponse{
			Lyrics: []lyrics.LyricsText{{Text: payload.TTML}},
		}, nil
	case 401, 404:
		return lyrics.GetLyricsResponse{}, nil
	default:
		return lyrics.GetLyricsResponse{}, apiResponseError(response)
	}
}

func lyricsRequestURL(track lyrics.TrackInfo) (string, bool) {
	title := strings.TrimSpace(track.Title)
	artist := trackArtist(track)
	if title == "" || artist == "" {
		return "", false
	}

	query := url.Values{
		"a": {artist},
		"s": {title},
	}
	if album := strings.TrimSpace(track.Album); album != "" {
		query.Set("al", album)
	}
	if duration := int64(math.Round(float64(track.Duration))); duration > 0 {
		query.Set("d", strconv.FormatInt(duration, 10))
	}

	return apiEndpoint + "?" + query.Encode(), true
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
	var payload apiErrorResponse
	_ = json.Unmarshal(response.Body, &payload)

	detail := strings.TrimSpace(payload.Error)
	if detail == "" {
		detail = strings.TrimSpace(payload.Message)
	}
	if detail == "" {
		return fmt.Errorf("better lyrics API returned HTTP %d", response.StatusCode)
	}

	detailRunes := []rune(detail)
	if len(detailRunes) > maximumAPIErrorRunes {
		detail = string(detailRunes[:maximumAPIErrorRunes]) + "…"
	}
	return fmt.Errorf("better lyrics API returned HTTP %d: %s", response.StatusCode, detail)
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
