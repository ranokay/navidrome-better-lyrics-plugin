package source

import (
	"bytes"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
)

func TestLunaBeatCacheExpansionHasABound(t *testing.T) {
	oversized := gzipString(bytes.Repeat([]byte(" "), (16<<20)+1))
	if raw, err := gunzipString(oversized); err == nil || raw != nil {
		t.Fatalf("oversized cache expansion returned %d bytes, %v; want no value and an error", len(raw), err)
	}
	valid := bytes.Repeat([]byte(" "), 2<<20)
	decoded, err := gunzipString(gzipString(valid))
	if err != nil || !bytes.Equal(decoded, valid) {
		t.Fatalf("ordinary compressed index failed: bytes=%d error=%v", len(decoded), err)
	}
}

func TestLunaBeatLegacyCacheMayExceedTheHTTPResponseBudget(t *testing.T) {
	raw := append([]byte(`{"revision":"legacy","songs":[]}`), bytes.Repeat([]byte(" "), 9<<20)...)
	requests := 0
	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		if request.URL != lunaBeatBase+"/api/v1/manifest.json" {
			t.Fatalf("legacy cache unexpectedly refetched index: %s", request.URL)
		}
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"revision":"legacy"}`)}, nil
	})
	if err := rt.Cache().SetWithTTL(lunaBeatIndexKey, gzipString(raw), lunaBeatIndexTTL); err != nil {
		t.Fatal(err)
	}
	index, err := readLunaBeatIndex(rt, time.Now().Add(time.Minute))
	if err != nil || index.Revision != "legacy" || requests != 1 {
		t.Fatalf("legacy index revision=%q requests=%d error=%v", index.Revision, requests, err)
	}
}

func TestYTMusicDurationRejectsOverflowAndInvalidComponents(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"-1:00", "1:60", "1:-1", "9223372036854775807:00", "153722867280912:56"} {
		if parsed, err := parseYTMusicDuration(value); err == nil {
			t.Errorf("duration %q = %d, want invalid/overflow rejection", value, parsed)
		}
	}
	if parsed, err := parseYTMusicDuration("1:02:03"); err != nil || parsed != 3_723_000 {
		t.Fatalf("valid duration = %d, %v", parsed, err)
	}
}
