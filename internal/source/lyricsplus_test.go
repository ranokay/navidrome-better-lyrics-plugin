package source

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
)

func TestLyricsPlusCandidatesRetainCompleteText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		line    lyricsPlusLine
		want    string
		quality lyric.TimingQuality
	}{
		{
			name: "standalone space and zero duration punctuation",
			line: lyricsPlusLine{Time: 1000, Duration: 1000, Text: "Hello, world!", Syllabus: []lyricsPlusWord{
				{Time: 1000, Duration: 400, Text: "Hello"},
				{Time: 1400, Text: ","},
				{Time: 1400, Text: " "},
				{Time: 1500, Duration: 500, Text: "world!"},
			}},
			want:    "[00:01.00]<00:01.00>Hello<00:01.40>,<00:01.40> <00:01.50>world!<00:02.00>\n",
			quality: lyric.TimingWord,
		},
		{
			name: "separators live only in complete line",
			line: lyricsPlusLine{Time: 1000, Duration: 1000, Text: "Hello, world!", Syllabus: []lyricsPlusWord{
				{Time: 1000, Duration: 500, Text: "Hello"},
				{Time: 1500, Duration: 500, Text: "world"},
			}},
			want:    "[00:01.00]<00:01.00>Hello, <00:01.50>world!<00:02.00>\n",
			quality: lyric.TimingWord,
		},
		{
			name: "mismatching tokens fall back to complete line",
			line: lyricsPlusLine{Time: 1000, Duration: 1000, Text: "Hello, world!", Syllabus: []lyricsPlusWord{
				{Time: 1000, Duration: 500, Text: "different"},
			}},
			want:    "[00:01.00]Hello, world!\n",
			quality: lyric.TimingLine,
		},
		{
			name: "missing words fall back to complete line",
			line: lyricsPlusLine{Time: 1000, Duration: 1000, Text: "Hello bright world!", Syllabus: []lyricsPlusWord{
				{Time: 1000, Duration: 500, Text: "Hello"},
				{Time: 1500, Duration: 500, Text: "world"},
			}},
			want:    "[00:01.00]Hello bright world!\n",
			quality: lyric.TimingLine,
		},
		{
			name: "invalid token timing falls back to complete line",
			line: lyricsPlusLine{Time: 1000, Duration: 1000, Text: "Hello", Syllabus: []lyricsPlusWord{
				{Time: -100, Duration: 500, Text: "Hello"},
			}},
			want:    "[00:01.00]Hello\n",
			quality: lyric.TimingLine,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			candidates := lyricsPlusCandidates(lyricsPlusPayload{Type: "Word", Lyrics: []lyricsPlusLine{test.line}})
			if len(candidates) != 1 {
				t.Fatalf("candidates = %#v, want one", candidates)
			}
			candidate := candidates[0]
			if got := candidate.Response.Lyrics[0].Text; got != test.want {
				t.Fatalf("text = %q, want %q", got, test.want)
			}
			if candidate.Quality != test.quality {
				t.Fatalf("quality = %v, want %v", candidate.Quality, test.quality)
			}
		})
	}
}

func TestLyricsPlusLiveFixturePreservesEveryLine(t *testing.T) {
	t.Parallel()

	fixture, err := os.ReadFile("testdata/lyricsplus/get.json")
	if err != nil {
		t.Fatal(err)
	}
	var payload lyricsPlusPayload
	if err := json.Unmarshal(fixture, &payload); err != nil {
		t.Fatal(err)
	}
	candidates := lyricsPlusCandidates(payload)
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want one", candidates)
	}
	rows := strings.Split(strings.TrimSuffix(candidates[0].Response.Lyrics[0].Text, "\n"), "\n")
	if len(rows) != len(payload.Lyrics) {
		t.Fatalf("rendered %d lines, want %d", len(rows), len(payload.Lyrics))
	}
	for i, line := range rows {
		// Remove generated line and word markers to verify the display text.
		var plain strings.Builder
		insideMarker := false
		for _, char := range line {
			switch char {
			case '[', '<':
				insideMarker = true
			case ']', '>':
				insideMarker = false
			default:
				if !insideMarker {
					plain.WriteRune(char)
				}
			}
		}
		if got := plain.String(); got != payload.Lyrics[i].Text {
			t.Fatalf("line %d = %q, want %q", i, got, payload.Lyrics[i].Text)
		}
	}
}

func TestLyricsPlusCandidates_WordTimingFromLiveFixture(t *testing.T) {
	t.Parallel()

	fixture, err := os.ReadFile("testdata/lyricsplus/get.json")
	if err != nil {
		t.Skipf("fixture unavailable: %v", err)
	}
	requests := 0
	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		if strings.Contains(request.URL, "v2/lyrics/get") {
			return &host.HTTPResponse{StatusCode: 200, Body: fixture}, nil
		}
		return &host.HTTPResponse{StatusCode: 404}, nil
	})

	candidates, health, err := fetchLyricsPlus(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 234}, time.Now().Add(testLookupBudget))
	if err != nil {
		t.Fatalf("fetchLyricsPlus() error = %v, want nil", err)
	}
	if health != Healthy {
		t.Fatalf("health = %v, want healthy", health)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want one enhanced candidate", candidates)
	}
	assertLyricsSource(t, candidates[0].Response.Source, "LyricsPlus", "elrc")
	if candidates[0].Quality != lyric.TimingWord {
		t.Fatalf("quality = %v, want the apple word timing from the syllabus", candidates[0].Quality)
	}

	// The payload's provenance must read from the probe sources.
	missProvider := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		return &host.HTTPResponse{StatusCode: 404}, nil
	})
	missCandidates, missHealth, missErr := fetchLyricsPlus(missProvider, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(testLookupBudget))
	if missErr != nil || len(missCandidates) != 0 || missHealth != Healthy {
		t.Fatalf("miss = %#v/%v/%v, want a clean miss on 404", missCandidates, missHealth, missErr)
	}
}

func TestLyricsPlusDurationRounds(t *testing.T) {
	t.Parallel()

	// Fractional seconds must not round up into the next whole second.
	// 234.2 belongs to 234.
	var captured string
	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		captured = request.URL
		return &host.HTTPResponse{StatusCode: 404}, nil
	})
	fetchLyricsPlus(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 234.2}, time.Now().Add(testLookupBudget))
	if !strings.Contains(captured, "duration=234") || strings.Contains(captured, "duration=235") {
		t.Fatalf("URL = %q, want duration=234", captured)
	}
}

func TestLyricsPlusEmptyPrimaryIsCleanMiss(t *testing.T) {
	t.Parallel()

	// A decoded empty 200 payload is the mirror's clean miss; a mirror failure
	// must not turn it into a degraded lookup.
	requests := 0
	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		if strings.Contains(request.URL, lyricsPlusEndpoint) {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"type":"Line","lyrics":[]}`)}, nil
		}
		return &host.HTTPResponse{StatusCode: 500, Body: []byte(`kaput`)}, nil
	})

	candidates, health, err := fetchLyricsPlus(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(testLookupBudget))
	if err != nil || len(candidates) != 0 || health != Healthy {
		t.Fatalf("result = %#v/%v/%v, want a clean miss without mirror rotation", candidates, health, err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want only the primary mirror", requests)
	}
}

func TestLyricsPlusRotatesPastAThrottledMirror(t *testing.T) {
	t.Parallel()

	// The mirrors are independent deployments: a 429 on the primary must not
	// discard a healthy secondary.
	requests := 0
	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		if strings.Contains(request.URL, lyricsPlusEndpoint) {
			return &host.HTTPResponse{StatusCode: 429, Headers: map[string]string{"Retry-After": "30"}}, nil
		}
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"type":"Line","lyrics":[{"time":1000,"duration":500,"text":"mirror line"}]}`)}, nil
	})

	candidates, health, err := fetchLyricsPlus(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(testLookupBudget))
	if err != nil || health != Healthy || len(candidates) != 1 {
		t.Fatalf("result = %#v/%v/%v, want the secondary mirror to serve", candidates, health, err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want the throttled primary then the secondary", requests)
	}
}

func TestLyricsPlusCandidates_SkipsCooledMirrors(t *testing.T) {
	t.Parallel()

	// Mirrors carry independent host cooldowns: cooling the primary down must
	// route the fetch to the healthy sibling, not suppress the source.
	cache := newFakeStore()
	requests := 0
	var requestURL string
	rt := newFakeRuntimeWithCache(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		requestURL = request.URL
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"type":"Line","lyrics":[{"time":1000,"duration":500,"text":"line"}]}`)}, nil
	}, cache, time.Now)
	rt.RememberCooldown(GlobalCooldownKey("lyricsplus.binimum.org"), 5*time.Minute)

	candidates, health, err := fetchLyricsPlus(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(testLookupBudget))
	if err != nil || health != Healthy || len(candidates) != 1 {
		t.Fatalf("result = %#v/%v/%v, want the healthy sibling to serve", candidates, health, err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want only the non-cooled mirror queried", requests)
	}
	if !strings.Contains(requestURL, "lyricsplus.atomix.one") {
		t.Fatalf("request = %q, want the non-cooled mirror", requestURL)
	}
}
