package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func TestLRCLibCandidates_PrefersWordSyncedLyricsFile(t *testing.T) {
	t.Parallel()

	fixture, err := os.ReadFile("testdata/lrclib/get.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	provider := providerForCatalog(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "/api/get") {
			return &host.HTTPResponse{StatusCode: 200, Body: fixture}, nil
		}
		t.Fatalf("unexpected request: %s", request.URL)
		return nil, nil
	})

	candidates, health, err := fetchLRCLibLyricsCandidates(provider, lyrics.TrackInfo{
		Title: "Shape of You", Artist: "Ed Sheeran", Duration: 234,
	}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchLRCLibLyricsCandidates() error = %v, want nil", err)
	}
	if health != lookupHealthy {
		t.Fatalf("health = %v, want healthy", health)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want one", candidates)
	}
	candidate := candidates[0]
	if candidate.quality != timingWord && candidate.quality != timingLine {
		t.Fatalf("quality = %v, want synchronized", candidate.quality)
	}
	assertLyricsSource(t, candidate.response.Source, "LRCLib", candidate.response.Source.Format)
}

func TestLRCLibCandidates_FallsBackToSearchOnExactMiss(t *testing.T) {
	t.Parallel()

	searchFixture, err := os.ReadFile("testdata/lrclib/search.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	requests := 0
	provider := providerForCatalog(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		if strings.Contains(request.URL, "/api/get") {
			return &host.HTTPResponse{StatusCode: 404}, nil
		}
		return &host.HTTPResponse{StatusCode: 200, Body: searchFixture}, nil
	})

	candidates, health, err := fetchLRCLibLyricsCandidates(provider, lyrics.TrackInfo{
		Title: "Live Forever", Artist: "Oasis", Duration: 281,
	}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchLRCLibLyricsCandidates() error = %v, want nil", err)
	}
	if health != lookupHealthy {
		t.Fatalf("health = %v, want healthy", health)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want one", candidates)
	}
	if requests != 2 {
		t.Fatalf("request count = %d, want get then search", requests)
	}
}

func TestLRCLibCandidates_SearchMissWhenNoNearDuration(t *testing.T) {
	t.Parallel()

	searchFixture, err := os.ReadFile("testdata/lrclib/search.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	provider := providerForCatalog(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "/api/get") {
			return &host.HTTPResponse{StatusCode: 404}, nil
		}
		return &host.HTTPResponse{StatusCode: 200, Body: searchFixture}, nil
	})

	candidates, health, err := fetchLRCLibLyricsCandidates(provider, lyrics.TrackInfo{
		Title: "Live Forever", Artist: "Oasis", Duration: 9999,
	}, time.Now().Add(stageOneLookupBudget))
	if err != nil || len(candidates) != 0 || health != lookupHealthy {
		t.Fatalf("result = %#v/%v/%v; want clean miss", candidates, health, err)
	}
}

func TestLRCLibCandidates_ThrottleLeadsCooldown(t *testing.T) {
	t.Parallel()

	throttles := 0
	provider := providerForCatalog(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		if throttles == 0 {
			throttles++
			return &host.HTTPResponse{
				StatusCode: 429,
				Headers:    map[string]string{"Retry-After": "30"},
				Body:       []byte(`{"name":"RateLimited"}`),
			}, nil
		}
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"syncedLyrics":"[00:01.00]line","plainLyrics":"plain"}`)}, nil
	})

	_, health, err := fetchLRCLibLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
	if err == nil || !strings.Contains(err.Error(), "HTTP 429") {
		t.Fatalf("error = %v, want HTTP 429", err)
	}
	if remaining := provider.activeCooldown(sourceGlobalCooldownKey("lrclib")); remaining <= 0 {
		t.Fatalf("lrclib cooldown = %v, want active", remaining)
	}
	if health != lookupDegraded {
		t.Fatalf("health = %v, want degraded", health)
	}
}

func TestLRCLibCandidates_PlainFallback(t *testing.T) {
	t.Parallel()

	provider := providerForCatalog(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"trackName":"Song","artistName":"Artist","plainLyrics":"just plain lyrics","syncedLyrics":""}`)}, nil
	})

	candidates, health, err := fetchLRCLibLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchLRCLibLyricsCandidates() error = %v, want nil", err)
	}
	if len(candidates) != 1 || candidates[0].quality != timingUnsynced {
		t.Fatalf("candidates = %#v, want one unsynced candidate", candidates)
	}
	assertLyricsSource(t, candidates[0].response.Source, "LRCLib", "plain")
	if health != lookupHealthy {
		t.Fatalf("health = %v, want healthy", health)
	}
}

func TestLRCLibWordSeparatorsSurviveParsing(t *testing.T) {
	t.Parallel()

	// LRCLib stores spacing only in the line text; token rendering must not
	// fuse the words into "firstline".
	const yamlDoc = "version: '1.0'\n" +
		"lines:\n" +
		"- start_ms: 1000\n" +
		"  end_ms: 2500\n" +
		"  text: first line\n" +
		"  words:\n" +
		"  - text: first\n" +
		"    start_ms: 1000\n" +
		"    end_ms: 1500\n" +
		"  - text: line\n" +
		"    start_ms: 1500\n" +
		"    end_ms: 2500\n"

	lines, err := parseLRCLibLyricsFile([]byte(yamlDoc))
	if err != nil {
		t.Fatalf("parseLRCLibLyricsFile() error = %v, want nil", err)
	}
	rendered := enhancedLRCFromTimedLines(lines)
	if !strings.Contains(rendered, "<00:01.00>first <00:01.50>line") {
		t.Fatalf("rendered = %q, want the separator restored between the word tokens", rendered)
	}
	if strings.Contains(rendered, "firstline") {
		t.Fatalf("rendered = %q, fused the word tokens", rendered)
	}
}

func TestLRCLibIgnoresMismatchedExactRecord(t *testing.T) {
	t.Parallel()

	// /api/get is fuzzy too: an unrelated usable record must not become the
	// winner; the flow falls through to search.
	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "/api/get") {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"trackName":"Other Song","artistName":"Other Artist","duration":280,"plainLyrics":"wrong","syncedLyrics":"[00:01.00]wrong"}`)}, nil
		}
		return &host.HTTPResponse{StatusCode: 404}, nil
	})

	candidates, health, err := fetchLRCLibLyricsCandidates(provider, lyrics.TrackInfo{Title: "Live Forever", Artist: "Oasis", Duration: 280}, time.Now().Add(stageOneLookupBudget))
	if err != nil || len(candidates) != 0 || health != lookupHealthy {
		t.Fatalf("result = %#v/%v/%v; want a clean miss rather than the unrelated record", candidates, health, err)
	}
}

func TestLRCLibMalformedExactRecordStaysDegraded(t *testing.T) {
	t.Parallel()

	// A malformed 200 from /api/get is a retryable provider failure, not a
	// metadata mismatch that melts into a cached miss.
	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "/api/get") {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"trackName": oops`)}, nil
		}
		return &host.HTTPResponse{StatusCode: 404}, nil
	})

	_, health, err := fetchLRCLibLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 200}, time.Now().Add(stageOneLookupBudget))
	if err == nil || health != lookupDegraded {
		t.Fatalf("result = (%v, %v); want a degraded decode failure", err, health)
	}
}

func TestLRCLibAlignmentFailureDoesNotDuplicate(t *testing.T) {
	t.Parallel()

	// A LAST token that cannot be found verbatim forfeits the remainder
	// append: appending the untouched line after the token rendered it twice.
	rendered := renderLRCLibFixture(t, "version: '1.0'\n"+
		"lines:\n"+
		"- start_ms: 1000\n"+
		"  end_ms: 2500\n"+
		"  text: first line\n"+
		"  words:\n"+
		"  - text: first\n"+
		"    start_ms: 1000\n"+
		"    end_ms: 1500\n"+
		"  - text: line!\n"+
		"    start_ms: 1500\n"+
		"    end_ms: 2500\n")
	if got := strings.Count(rendered, "line"); got != 1 {
		t.Fatalf("rendered = %q, want the unaligned line rendered once (line appears %d times)", rendered, got)
	}
}

func renderLRCLibFixture(t *testing.T, document string) string {
	t.Helper()
	lines, err := parseLRCLibLyricsFile([]byte(document))
	if err != nil {
		t.Fatalf("parseLRCLibLyricsFile() error = %v, want nil", err)
	}
	return enhancedLRCFromTimedLines(lines)
}

func TestLRCLibMalformedLyricsFileStaysDegraded(t *testing.T) {
	t.Parallel()

	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "/api/get") {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"trackName":"Song","artistName":"Artist","duration":200,"hasWordSync":true,"lyricsfile":"not: [valid","plainLyrics":"","syncedLyrics":""}`)}, nil
		}
		return &host.HTTPResponse{StatusCode: 404}, nil
	})

	_, health, err := fetchLRCLibLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 200}, time.Now().Add(stageOneLookupBudget))
	if err == nil || health != lookupDegraded {
		t.Fatalf("result = (%v, %v); want the malformed lyricsfile to degrade without a fallback", err, health)
	}
}

func TestLRCLibCandidates_ExactInstrumentalFallsBackToSearch(t *testing.T) {
	t.Parallel()

	requests := 0
	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		if strings.Contains(request.URL, "/api/get") {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"id":10209922,"trackName":"Live Forever","artistName":"Oasis","duration":280,"instrumental":true,"hasWordSync":false,"plainLyrics":null,"syncedLyrics":null,"lyricsfile":""}`)}, nil
		}
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`[{"id":5,"trackName":"Live Forever","artistName":"Oasis","instrumental":false,"hasWordSync":false,"duration":280,"plainLyrics":"plain rows","syncedLyrics":"[00:01.00]synced","lyricsfile":""}]`)}, nil
	})

	candidates, health, err := fetchLRCLibLyricsCandidates(provider, lyrics.TrackInfo{
		Title: "Live Forever", Artist: "Oasis", Duration: 280,
	}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchLRCLibLyricsCandidates() error = %v, want nil", err)
	}
	if health != lookupHealthy {
		t.Fatalf("health = %v, want healthy", health)
	}
	if len(candidates) != 1 || candidates[0].quality != timingLine {
		t.Fatalf("candidates = %#v, want the search fallback line candidate", candidates)
	}
	assertLyricsSource(t, candidates[0].response.Source, "LRCLib", "lrc")
	if requests != 2 {
		t.Fatalf("requests = %d, want get then search", requests)
	}
}

func TestLRCLibCandidates_InstrumentalSearchMatchIsCleanMiss(t *testing.T) {
	t.Parallel()

	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "/api/get") {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"id":10209922,"instrumental":true}`)}, nil
		}
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`[{"id":5,"instrumental":true,"duration":280}]`)}, nil
	})

	candidates, health, err := fetchLRCLibLyricsCandidates(provider, lyrics.TrackInfo{
		Title: "Live Forever", Artist: "Oasis", Duration: 280,
	}, time.Now().Add(stageOneLookupBudget))
	if err != nil || len(candidates) != 0 || health != lookupHealthy {
		t.Fatalf("result = %#v/%v/%v; want clean miss when the only search match is instrumental", candidates, health, err)
	}
}
