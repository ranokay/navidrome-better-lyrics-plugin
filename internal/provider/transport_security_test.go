package provider

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func TestTransportErrorsDoNotExposeCredentials(t *testing.T) {
	for _, serialized := range []bool{false, true} {
		t.Run(map[bool]string{false: "typed", true: "host string"}[serialized], func(t *testing.T) {
			const token = "audit-secret+/value"
			cause := errors.New("synthetic network failure")
			var transportErr error
			p := providerForCatalog(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
				transportErr = &url.Error{Op: "Get", URL: request.URL, Err: cause}
				if serialized {
					transportErr = errors.New(transportErr.Error())
				}
				return nil, transportErr
			}, "musixmatch")
			p.configLookup = func(key string) (string, bool) {
				return token, key == "musixmatchUserToken"
			}
			_, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: lyrics.TrackInfo{Title: "Song", Artist: "Artist"}})
			if err == nil || !strings.Contains(err.Error(), "Musixmatch") {
				t.Fatalf("expected an actionable source failure, got %v", err)
			}
			for _, secret := range []string{token, url.QueryEscape(token), "usertoken="} {
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
		return &host.HTTPResponse{StatusCode: 500, Body: []byte(`{"error":"/ws/1.1/track.search?user%74oken=audit-secret%2b%2fvalue"}`)}, nil
	}, "musixmatch")
	p.configLookup = func(key string) (string, bool) { return token, key == "musixmatchUserToken" }
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
