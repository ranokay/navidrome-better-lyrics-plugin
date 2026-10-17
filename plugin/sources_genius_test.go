package main

import (
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func TestGeniusChain_ScapesMatchedSong(t *testing.T) {
	t.Parallel()

	searchFixture, err := os.ReadFile("testdata/genius/search.json")
	if err != nil {
		t.Skipf("fixture unavailable: %v", err)
	}
	pageFixture, err := os.ReadFile("testdata/genius/page.html")
	if err != nil {
		t.Skipf("fixture unavailable: %v", err)
	}
	requests := 0
	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		requests++
		switch {
		case strings.Contains(request.URL, "api/search"):
			return &host.HTTPResponse{StatusCode: 200, Body: searchFixture}, nil
		case strings.Contains(request.URL, "genius.com/"):
			return &host.HTTPResponse{StatusCode: 200, Body: pageFixture}, nil
		default:
			t.Errorf("unexpected request %q", request.URL)
			return &host.HTTPResponse{StatusCode: 404}, nil
		}
	})

	candidates, health, err := fetchGeniusLyricsCandidates(provider, lyrics.TrackInfo{Title: "Shape of You", Artist: "Ed Sheeran"}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchGeniusLyricsCandidates() error = %v, want nil", err)
	}
	if health != lookupHealthy {
		t.Fatalf("health = %v, want healthy", health)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want one plain candidate", candidates)
	}
	assertLyricsSource(t, candidates[0].response.Source, "Genius", "plain")
	text := candidates[0].response.Lyrics[0].Text
	if !strings.Contains(text, "A club isn't the best place") {
		t.Fatalf("text sample = %q, want the lyric body from the page containers", text[0:120])
	}
	if strings.Contains(text, "Contributors") || strings.Contains(text, "Read More") {
		t.Fatalf("text = %q, want header containers dropped", text[0:120])
	}

	// A music-video hit is skipped: only songs carry the plain body.
	excluded := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"response":{"hits":[{"type":"song","result":{"_type":"song","url":"https://genius.com/Other-artist-song-lyrics","title":"Different Title","title_with_featured":"Different Title","artist_names":"Somebody Else"}}]}}`)}, nil
	})
	if candidates, hitHealth, err2 := fetchGeniusLyricsCandidates(excluded, lyrics.TrackInfo{Title: "Shape of You", Artist: "Ed Sheeran"}, time.Now().Add(stageOneLookupBudget)); err2 != nil || len(candidates) != 0 || hitHealth != lookupHealthy {
		t.Fatalf("gated = %#v/%v/%v, want a clean miss without a metadata match", candidates, hitHealth, err2)
	}
}

func TestGeniusSearchEscapesQueryPunctuation(t *testing.T) {
	t.Parallel()

	// Song metadata reaches the query verbatim: "&", "#", and "+" in titles
	// break or truncate the search, so every new provider must encode its
	// query. The raw query must carry exactly the one encoded
	// parameter, or the old concatenation could pass unnoticed.
	var capturedURL string
	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		capturedURL = request.URL
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"response":{"hits":[]}}`)}, nil
	})

	if _, _, err := fetchGeniusLyricsCandidates(provider, lyrics.TrackInfo{Title: "Love & Hate", Artist: "Artist #1"}, time.Now().Add(stageOneLookupBudget)); err != nil {
		t.Fatalf("fetchGeniusLyricsCandidates() error = %v, want nil", err)
	}
	parsed, err := url.Parse(capturedURL)
	if err != nil {
		t.Fatalf("parse captured URL %q: %v", capturedURL, err)
	}
	wantQuery := url.Values{"q": {"Love & Hate Artist #1"}}.Encode()
	if parsed.RawQuery != wantQuery {
		t.Fatalf("raw query = %q, want %q (exactly one encoded parameter)", parsed.RawQuery, wantQuery)
	}
	if parsed.Fragment != "" {
		t.Fatalf("fragment = %q, want none: a # in the title must not truncate the URL", parsed.Fragment)
	}
	if got := parsed.Query().Get("q"); got != "Love & Hate Artist #1" {
		t.Fatalf("q = %q, want the full unescaped search text", got)
	}
}

func TestGeniusKeepsLyricLinesWithChromeWords(t *testing.T) {
	t.Parallel()

	// Chrome removal works on elements, not lyric words: a line containing
	// "Genius" is real lyrical content and must survive.
	page := []byte(`<!DOCTYPE html><html><body><div data-lyrics-container="true" class="Lyrics__Container">
		<div class="LyricsHeader__Container"><button class="ContributorsCreditSong__Container">441 Contributors</button>
		<h1>Love &amp; Hate Lyrics</h1>
		<div class="SongBioPreview"><p>The song was written in a rush <a href="#">Read More</a></p></div>
		<ul><li>Türkçe</li><li>Other</li></ul>
		<p>Some Metadata</p>
		</div>
		<div class="Lyrics__Container-sc" data-lyrics-container="true">[Pre-Chorus]<br/>Genius of love<br/>we keep on</div>
		<aside class="LyricsFooter__Container">Source</aside></body></html>`)

	text := geniusLyricsText(page, "Love & Hate")
	for _, want := range []string{"[Pre-Chorus]", "Genius of love", "we keep on"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text = %q, want the lyric line %q kept (chrome removal must be element-scoped)", text, want)
		}
	}
	for _, gone := range []string{"441 Contributors", "Türkçe", "Some Metadata", "Love & Hate Lyrics", "Read More"} {
		if strings.Contains(text, gone) {
			t.Fatalf("text = %q, want the page chrome %q removed", text, gone)
		}
	}
}

func TestGeniusExcludesRecommendationSidebarText(t *testing.T) {
	t.Parallel()

	// The first container's byte range spans recommendation sidebars; only
	// the lyric containers may contribute text.
	page := []byte(`<!DOCTYPE html><html><body>
	<div data-lyrics-container="true" class="Lyrics__Root">
	<div class="RightSidebar__Container"><h2>You might also like</h2><p>Other Song</p></div>
	<div data-lyrics-container="true" class="Lyrics__Container">[Verse 1]<br/>A club isn't the best place</div>
	</div></body></html>`)
	text := geniusLyricsText(page, "Shape of You")
	if strings.Contains(text, "You might also like") || strings.Contains(text, "Other Song") {
		t.Fatalf("text = %q, want recommendation chrome excluded", text)
	}
	if !strings.Contains(text, "A club isn't the best place") {
		t.Fatalf("text = %q, want the lyric line kept", text)
	}
}

func TestGeniusFixtureExcludesSidebar(t *testing.T) {
	t.Parallel()

	page, err := os.ReadFile("testdata/genius/page.html")
	if err != nil {
		t.Skipf("fixture unavailable: %v", err)
	}
	text := geniusLyricsText(page, "Shape of You")
	if strings.Contains(text, "You might also like") {
		t.Fatalf("text = %q, want the recommendation sidebar excluded from the real page", text[:160])
	}
	if !strings.Contains(text, "A club isn't") {
		t.Fatalf("text = %q, want the real lyrics kept", text[:160])
	}
}
