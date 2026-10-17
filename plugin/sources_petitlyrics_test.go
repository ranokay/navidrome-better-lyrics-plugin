package main

import (
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func TestPetitLyricsCandidates_WordFromWSY(t *testing.T) {
	t.Parallel()

	wsyXML := `<wsy><linenum>2</linenum>` +
		`<line><linestring>hello world</linestring><wordnum>2</wordnum>` +
		`<word><starttime>100</starttime><endtime>150</endtime><wordstring>hello</wordstring></word>` +
		`<word><starttime>150</starttime><endtime>400</endtime><wordstring>world</wordstring></word></line>` +
		`<line><linestring>plain row</linestring></line></wsy>`
	formCalls := 0
	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		formCalls++
		if !strings.Contains(request.URL, "p0.petitlyrics.com/api/GetPetitLyricsData.php") {
			t.Fatalf("unexpected request URL: %s", request.URL)
		}
		if formCalls == 1 {
			xmlEnvelope := `<?xml version='1.0' encoding='UTF-8'?>
<response><status>00000000</status><songs><song><lyricsType>3</lyricsType><lyricsData>` + base64Encode(wsyXML) + `</lyricsData></song></songs></response>`
			return hostXMLResponse(xmlEnvelope), nil
		}
		t.Fatalf("word payload should be used without a plain-tier call")
		return nil, nil
	})

	candidates, health, err := fetchPetitLyricsCandidates(provider, lyrics.TrackInfo{Title: "Live Forever", Artist: "Oasis"}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchPetitLyricsCandidates() error = %v, want nil", err)
	}
	if health != lookupHealthy {
		t.Fatalf("health = %v, want healthy", health)
	}
	if len(candidates) != 1 || candidates[0].quality != timingWord {
		t.Fatalf("candidates = %#v, want one word candidate", candidates)
	}
	assertLyricsSource(t, candidates[0].response.Source, "PetitLyrics", "elrc")
}

func TestPetitLyricsCandidates_PlainFallbackTier(t *testing.T) {
	t.Parallel()

	calls := 0
	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		calls++
		if strings.Contains(string(request.Body), "lyricsType=3") {
			return hostXMLResponse(`<?xml version='1.0' encoding='UTF-8'?><response><status>00000000</status><songs><song><lyricsType>2</lyricsType><lyricsData>` + base64Encode("LSYbinaryjunk") + `</lyricsData></song></songs></response>`), nil
		}
		return hostXMLResponse(`<?xml version='1.0' encoding='UTF-8'?><response><status>00000000</status><songs><song><lyricsType>1</lyricsType><lyricsData>` + base64Encode("plain lyric text") + `</lyricsData></song></songs></response>`), nil
	})

	candidates, health, err := fetchPetitLyricsCandidates(provider, lyrics.TrackInfo{Title: "Rare Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchPetitLyricsCandidates() error = %v, want nil", err)
	}
	if len(candidates) != 1 || candidates[0].quality != timingUnsynced {
		t.Fatalf("candidates = %#v, want one plain candidate", candidates)
	}
	assertLyricsSource(t, candidates[0].response.Source, "PetitLyrics", "plain")
	if calls != 3 {
		t.Fatalf("calls = %d, want word, line and plain tiers", calls)
	}
	if health != lookupHealthy {
		t.Fatalf("health = %v, want healthy", health)
	}
}

func TestPetitLyricsCandidates_LSYBinarySkipsToPlain(t *testing.T) {
	t.Parallel()

	calls := 0
	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		calls++
		if strings.Contains(string(request.Body), "lyricsType=3") {
			return hostXMLResponse(`<?xml version='1.0' encoding='UTF-8'?><response><status>00000000</status><songs><song><lyricsType>2</lyricsType><lyricsData>` + base64Encode("MHDR OBJT MLRC binary") + `</lyricsData></song></songs></response>`), nil
		}
		return hostXMLResponse(`<?xml version='1.0' encoding='UTF-8'?><response><status>00000000</status><songs><song><lyricsType>1</lyricsType><lyricsData>` + base64Encode("plain rows") + `</lyricsData></song></songs></response>`), nil
	})

	candidates, _, err := fetchPetitLyricsCandidates(provider, lyrics.TrackInfo{Title: "Rare Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchPetitLyricsCandidates() error = %v, want nil", err)
	}
	if len(candidates) != 1 || candidates[0].quality != timingUnsynced {
		t.Fatalf("candidates = %#v, want plain via tier fallback", candidates)
	}
}

func TestPetitLyricsTierSelectsTheRequestedSong(t *testing.T) {
	t.Parallel()

	// The lookup echoes each matched row's own metadata; the first row is not
	// automatically the requested track, so the selection must gate on the
	// row's title/artist instead of taking songs[0].
	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if !strings.Contains(string(request.Body), "lyricsType=1") {
			t.Fatalf("unexpected tier request: %s", request.Body)
		}
		return hostXMLResponse(`<?xml version='1.0' encoding='UTF-8'?><response><status>00000000</status><songs>` +
			`<song><title>Different Song</title><artist>Somebody Else</artist><lyricsType>1</lyricsType><lyricsData>` + base64Encode("wrong words") + `</lyricsData></song>` +
			`<song><title>Live Forever</title><artist>Oasis</artist><lyricsType>1</lyricsType><lyricsData>` + base64Encode("right words") + `</lyricsData></song>` +
			`</songs></response>`), nil
	})

	payload, health, err := provider.fetchPetitLyricsTier(lyrics.TrackInfo{Title: "Live Forever", Artist: "Oasis"}, petitLyricsTypePlain, time.Now().Add(time.Second))
	if err != nil || health != lookupHealthy {
		t.Fatalf("health/err = %v/%v, want a healthy lookup", health, err)
	}
	if payload.LyricsData != base64Encode("right words") {
		t.Fatalf("lyricsData = %q, want the row matching the requested song", payload.LyricsData)
	}
}

func TestPetitLyricsTierRefusesUnmatchedRows(t *testing.T) {
	t.Parallel()

	request := lyrics.TrackInfo{Title: "Live Forever", Artist: "Oasis"}

	// A row for another song must not be served on the strength of its order.
	unrelated := newLyricsProvider(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		return hostXMLResponse(`<?xml version='1.0' encoding='UTF-8'?><response><status>00000000</status><songs>` +
			`<song><title>Different Song</title><artist>Somebody Else</artist><lyricsType>1</lyricsType><lyricsData>` + base64Encode("wrong words") + `</lyricsData></song>` +
			`</songs></response>`), nil
	})
	payload, health, err := unrelated.fetchPetitLyricsTier(request, petitLyricsTypePlain, time.Now().Add(time.Second))
	if err != nil || health != lookupHealthy {
		t.Fatalf("unrelated row: health/err = %v/%v, want a healthy miss", health, err)
	}
	if strings.TrimSpace(payload.LyricsData) != "" {
		t.Fatalf("unrelated row: lyricsData = %q, want it refused", payload.LyricsData)
	}

	// A row carrying a different lyrics tier must not be decoded as the
	// requested one.
	mistiered := newLyricsProvider(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		return hostXMLResponse(`<?xml version='1.0' encoding='UTF-8'?><response><status>00000000</status><songs>` +
			`<song><title>Live Forever</title><artist>Oasis</artist><lyricsType>2</lyricsType><lyricsData>` + base64Encode("line payload") + `</lyricsData></song>` +
			`</songs></response>`), nil
	})
	payload, health, err = mistiered.fetchPetitLyricsTier(request, petitLyricsTypePlain, time.Now().Add(time.Second))
	if err != nil || health != lookupHealthy {
		t.Fatalf("mismatched tier: health/err = %v/%v, want a healthy miss", health, err)
	}
	if strings.TrimSpace(payload.LyricsData) != "" {
		t.Fatalf("mismatched tier: lyricsData = %q, want it refused", payload.LyricsData)
	}
}

func TestPetitLyricsNonZeroStatusDegrades(t *testing.T) {
	t.Parallel()

	// Missing songs answer status 00000000 with zero rows (verified live), so
	// any other status is a provider failure that must stay retryable instead
	// of caching as a clean miss.
	provider := newLyricsProvider(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		return hostXMLResponse(`<?xml version='1.0' encoding='UTF-8'?><response><status>00000001</status><songs></songs></response>`), nil
	})

	_, health, err := provider.fetchPetitLyricsTier(lyrics.TrackInfo{Title: "Live Forever", Artist: "Oasis"}, petitLyricsTypePlain, time.Now().Add(time.Second))
	if err == nil || health != lookupDegraded {
		t.Fatalf("health/err = %v/%v, want a degraded provider failure for an error status", health, err)
	}
}

func TestLyricallyCandidates_ITunesSearchThenMirror(t *testing.T) {
	t.Parallel()

	itunesBody := `{"resultCount":2,"results":[{"trackId":1,"trackName":"Live Forever","artistName":"Oasis","trackTimeMillis":278000},{"trackId":2,"trackName":"Live Forever (Other)","artistName":"Artist","trackTimeMillis":999000}]}`
	mirrorBody := `{"type":"cache","lrc":"[00:01.00]plain","elrc":"[00:01.00]<00:01.10>maybe<00:01.90>line","elrcMultiPerson":"","ttmlContent":"<tt></tt>","plain":"maybe","source":"local_cache","cached_at":"2026-10-17T00:00:00Z"}`

	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "itunes.apple.com/search"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(itunesBody)}, nil
		case strings.Contains(request.URL, "lyrics.paxsenix.org/apple-music/lyrics"):
			if !strings.Contains(request.URL, "id=1") {
				t.Fatalf("mirror request should carry the duration-matched iTunes id: %s", request.URL)
			}
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(mirrorBody)}, nil
		default:
			t.Fatalf("unexpected request URL: %s", request.URL)
			return nil, nil
		}
	})

	candidates, health, err := fetchLyricallyLyricsCandidates(provider, lyrics.TrackInfo{
		Title: "Live Forever", Artist: "Oasis", Duration: 278,
	}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchLyricallyLyricsCandidates() error = %v, want nil", err)
	}
	if health != lookupHealthy {
		t.Fatalf("health = %v, want healthy", health)
	}
	if len(candidates) != 1 || candidates[0].quality != timingWord {
		t.Fatalf("candidates = %#v, want one word candidate from elrc", candidates)
	}
	assertLyricsSource(t, candidates[0].response.Source, "Apple Music (Lyrically)", "elrc")
}

func TestLyricallyCandidates_TTMLFallbackWhenNoELRC(t *testing.T) {
	t.Parallel()

	itunesBody := `{"resultCount":1,"results":[{"trackId":9,"trackName":"Song","artistName":"Artist","trackTimeMillis":200000}]}`
	mirrorBody := `{"ttmlContent":"<tt xmlns:itunes=\"urn:itunes\" itunes:timing=\"Line\"><p begin=\"1\">line</p></tt>"}`

	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "itunes.apple.com/search") {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(itunesBody)}, nil
		}
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(mirrorBody)}, nil
	})

	candidates, _, err := fetchLyricallyLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 200}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchLyricallyLyricsCandidates() error = %v, want nil", err)
	}
	if len(candidates) != 1 || candidates[0].quality != timingLine {
		t.Fatalf("candidates = %#v, want line candidate from TTML", candidates)
	}
}

func TestLyricallyCandidates_DurationMissIsClean(t *testing.T) {
	t.Parallel()

	provider := newLyricsProvider(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"resultCount":1,"results":[{"trackId":3,"trackName":"Song","artistName":"Artist","trackTimeMillis":250000}]}`)}, nil
	})

	candidates, health, err := fetchLyricallyLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 99}, time.Now().Add(stageOneLookupBudget))
	if err != nil || len(candidates) != 0 || health != lookupHealthy {
		t.Fatalf("result = %#v/%v/%v; want clean miss", candidates, health, err)
	}
}

func TestAppleSearch429RecordsCooldown(t *testing.T) {
	t.Parallel()

	cache := newFakeLyricsCache()
	provider := newLyricsProviderWithDeps(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		return &host.HTTPResponse{StatusCode: 429, Headers: map[string]string{"Retry-After": "9"}}, nil
	}, cache, time.Now, nil, nil)

	_, _, err := fetchLyricallyLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
	if err == nil {
		t.Fatal("429 converted to a miss; want a degraded error")
	}
	if cooldown := provider.activeCooldown(sourceGlobalCooldownKey("apple-lyrically")); cooldown < 9*time.Second || cooldown > 10*time.Second {
		t.Fatalf("cooldown = %v, want the Retry-After value honored", cooldown)
	}
}

func TestPetitLyricsMalformedPayloadDegrades(t *testing.T) {
	t.Parallel()

	// Nonempty invalid base64 across every tier degrades instead of caching
	// negative coverage.
	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`<?xml version="1.0"?><response><status>00000000</status><songs><song><lyricsData>!!!not-base64!!!</lyricsData></song></songs></response>`)}, nil
	})

	candidates, health, err := fetchPetitLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
	if err == nil || health != lookupDegraded || len(candidates) != 0 {
		t.Fatalf("result = %#v/%v/%v; want a degraded malformed payload", candidates, health, err)
	}
}
