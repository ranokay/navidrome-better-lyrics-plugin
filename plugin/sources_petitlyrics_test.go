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
