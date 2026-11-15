package provider

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func TestCredentialRequestsDoNotFollowRedirects(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		headers    map[string]string
		noRedirect bool
	}{
		{"anonymous", nil, false},
		{"configured key", map[string]string{"X-API-Key": "fixture"}, true},
		{"case insensitive key", map[string]string{"x-api-key": "fixture"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := New(Deps{Send: func(request host.HTTPRequest) (*host.HTTPResponse, error) {
				if request.NoFollowRedirects != tc.noRedirect {
					t.Fatalf("NoFollowRedirects=%v, want %v for credential isolation", request.NoFollowRedirects, tc.noRedirect)
				}
				return &host.HTTPResponse{StatusCode: 200}, nil
			}})
			if _, err := p.Do("fixture", "GET", "https://lyrics-api.boidu.dev", nil, tc.headers, time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTransportErrorsDoNotExposeCredentials(t *testing.T) {
	for _, serialized := range []bool{false, true} {
		t.Run(map[bool]string{false: "typed", true: "host string"}[serialized], func(t *testing.T) {
			const token = "audit-secret+/value"
			cause := errors.New("proxy rejected X-API-Key " + token)
			var transportErr error
			p := providerForCatalog(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
				transportErr = &url.Error{Op: "Get", URL: request.URL, Err: cause}
				if serialized {
					transportErr = errors.New(transportErr.Error())
				}
				return nil, transportErr
			}, "better-lyrics")
			p.configLookup = func(key string) (string, bool) {
				return token, key == "betterLyricsApiKey"
			}
			_, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}})
			if err == nil || !strings.Contains(err.Error(), "Better Lyrics") {
				t.Fatalf("expected an actionable source failure, got %v", err)
			}
			for _, secret := range []string{token, url.QueryEscape(token)} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("diagnostic exposes credential: %s", err)
				}
			}
			if !errors.Is(err, transportErr) {
				t.Fatal("redaction must preserve the error chain")
			}
		})
	}
}

func TestProviderDiagnosticsRedactEncodedRelativeQueryEchoes(t *testing.T) {
	const token = "audit-secret+/value"
	const encoded = "audit-secret%2b%2fvalue"
	var diagnostics []string
	p := providerForCatalog(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		return &host.HTTPResponse{StatusCode: 500, Body: []byte(`{"error":"/v1/lyrics?api%5fkey=audit-secret%2b%2fvalue"}`)}, nil
	}, "better-lyrics")
	p.configLookup = func(key string) (string, bool) { return token, key == "betterLyricsApiKey" }
	p.logf = func(message string) { diagnostics = append(diagnostics, message) }
	_, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}})
	if err == nil {
		t.Fatal("expected provider failure")
	}
	diagnostics = append(diagnostics, err.Error())
	for _, message := range diagnostics {
		if strings.Contains(message, token) || strings.Contains(message, encoded) {
			t.Fatalf("credential echo in diagnostic: %s", message)
		}
	}
}
