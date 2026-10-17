package source

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func TestFormPostsDeclareTheirContentType(t *testing.T) {
	t.Parallel()

	var seen []string
	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		seen = append(seen, request.Headers["Content-Type"])
		return &host.HTTPResponse{StatusCode: 404}, nil
	})

	deadline := time.Now().Add(testLookupBudget)
	if _, err := postForm(rt, "Test", "https://example.test/post", url.Values{"s": {"value"}}, map[string]string{}, deadline); err != nil {
		t.Fatalf("postForm() error = %v, want nil", err)
	}

	if len(seen) != 1 || seen[0] != "application/x-www-form-urlencoded" {
		t.Fatalf("Content-Type headers = %v, want exactly one form-urlencoded POST", seen)
	}
}

func TestNetEaseFormContentType(t *testing.T) {
	t.Parallel()

	var contentType string
	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "cloudsearch/pc") {
			contentType = request.Headers["Content-Type"]
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"result":{"songs":[{"id":1,"duration":100000}]}}`)}, nil
		}
		return &host.HTTPResponse{StatusCode: 404}, nil
	})

	if _, _, err := fetchNetEase(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 100}, time.Now().Add(testLookupBudget)); err != nil {
		t.Fatalf("fetchNetEase() error = %v, want nil", err)
	}
	if contentType != "application/x-www-form-urlencoded" {
		t.Fatalf("NetEase search Content-Type = %q, want form-urlencoded (opaque bodies break server-side form parsing)", contentType)
	}
}
