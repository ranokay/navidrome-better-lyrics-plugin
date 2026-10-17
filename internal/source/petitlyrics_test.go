package source

import (
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
)

func TestPetitLyricsCandidates_WordFromWSY(t *testing.T) {
	t.Parallel()

	wsyXML := `<wsy><linenum>2</linenum>` +
		`<line><linestring>hello world</linestring><wordnum>2</wordnum>` +
		`<word><starttime>100</starttime><endtime>150</endtime><wordstring>hello</wordstring></word>` +
		`<word><starttime>150</starttime><endtime>400</endtime><wordstring>world</wordstring></word></line>` +
		`<line><linestring>plain row</linestring></line></wsy>`
	formCalls := 0
	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
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

	candidates, health, err := fetchPetitLyrics(rt, lyrics.TrackInfo{Title: "Live Forever", Artist: "Oasis"}, time.Now().Add(testLookupBudget))
	if err != nil {
		t.Fatalf("fetchPetitLyrics() error = %v, want nil", err)
	}
	if health != Healthy {
		t.Fatalf("health = %v, want healthy", health)
	}
	if len(candidates) != 1 || candidates[0].Quality != lyric.TimingWord {
		t.Fatalf("candidates = %#v, want one word candidate", candidates)
	}
	assertLyricsSource(t, candidates[0].Response.Source, "PetitLyrics", "elrc")
}

func TestPetitLyricsCandidates_PlainFallbackTier(t *testing.T) {
	t.Parallel()

	calls := 0
	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		calls++
		if strings.Contains(string(request.Body), "lyricsType=3") {
			return hostXMLResponse(`<?xml version='1.0' encoding='UTF-8'?><response><status>00000000</status><songs><song><lyricsType>2</lyricsType><lyricsData>` + base64Encode("LSYbinaryjunk") + `</lyricsData></song></songs></response>`), nil
		}
		return hostXMLResponse(`<?xml version='1.0' encoding='UTF-8'?><response><status>00000000</status><songs><song><lyricsType>1</lyricsType><lyricsData>` + base64Encode("plain lyric text") + `</lyricsData></song></songs></response>`), nil
	})

	candidates, health, err := fetchPetitLyrics(rt, lyrics.TrackInfo{Title: "Rare Song", Artist: "Artist"}, time.Now().Add(testLookupBudget))
	if err != nil {
		t.Fatalf("fetchPetitLyrics() error = %v, want nil", err)
	}
	if len(candidates) != 1 || candidates[0].Quality != lyric.TimingUnsynced {
		t.Fatalf("candidates = %#v, want one plain candidate", candidates)
	}
	assertLyricsSource(t, candidates[0].Response.Source, "PetitLyrics", "plain")
	if calls != 3 {
		t.Fatalf("calls = %d, want word, line and plain tiers", calls)
	}
	if health != Healthy {
		t.Fatalf("health = %v, want healthy", health)
	}
}

func TestPetitLyricsCandidates_LSYBinarySkipsToPlain(t *testing.T) {
	t.Parallel()

	calls := 0
	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		calls++
		if strings.Contains(string(request.Body), "lyricsType=3") {
			return hostXMLResponse(`<?xml version='1.0' encoding='UTF-8'?><response><status>00000000</status><songs><song><lyricsType>2</lyricsType><lyricsData>` + base64Encode("MHDR OBJT MLRC binary") + `</lyricsData></song></songs></response>`), nil
		}
		return hostXMLResponse(`<?xml version='1.0' encoding='UTF-8'?><response><status>00000000</status><songs><song><lyricsType>1</lyricsType><lyricsData>` + base64Encode("plain rows") + `</lyricsData></song></songs></response>`), nil
	})

	candidates, _, err := fetchPetitLyrics(rt, lyrics.TrackInfo{Title: "Rare Song", Artist: "Artist"}, time.Now().Add(testLookupBudget))
	if err != nil {
		t.Fatalf("fetchPetitLyrics() error = %v, want nil", err)
	}
	if len(candidates) != 1 || candidates[0].Quality != lyric.TimingUnsynced {
		t.Fatalf("candidates = %#v, want plain via tier fallback", candidates)
	}
}

func TestPetitLyricsTierSelectsTheRequestedSong(t *testing.T) {
	t.Parallel()

	// The lookup echoes each matched row's own metadata; the first row is not
	// automatically the requested track, so the selection must gate on the
	// row's title/artist instead of taking songs[0].
	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if !strings.Contains(string(request.Body), "lyricsType=1") {
			t.Fatalf("unexpected tier request: %s", request.Body)
		}
		return hostXMLResponse(`<?xml version='1.0' encoding='UTF-8'?><response><status>00000000</status><songs>` +
			`<song><title>Different Song</title><artist>Somebody Else</artist><lyricsType>1</lyricsType><lyricsData>` + base64Encode("wrong words") + `</lyricsData></song>` +
			`<song><title>Live Forever</title><artist>Oasis</artist><lyricsType>1</lyricsType><lyricsData>` + base64Encode("right words") + `</lyricsData></song>` +
			`</songs></response>`), nil
	})

	payload, health, err := fetchPetitLyricsTier(rt, lyrics.TrackInfo{Title: "Live Forever", Artist: "Oasis"}, petitLyricsTypePlain, time.Now().Add(time.Second))
	if err != nil || health != Healthy {
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
	unrelated := newFakeRuntime(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		return hostXMLResponse(`<?xml version='1.0' encoding='UTF-8'?><response><status>00000000</status><songs>` +
			`<song><title>Different Song</title><artist>Somebody Else</artist><lyricsType>1</lyricsType><lyricsData>` + base64Encode("wrong words") + `</lyricsData></song>` +
			`</songs></response>`), nil
	})
	payload, health, err := fetchPetitLyricsTier(unrelated, request, petitLyricsTypePlain, time.Now().Add(time.Second))
	if err != nil || health != Healthy {
		t.Fatalf("unrelated row: health/err = %v/%v, want a healthy miss", health, err)
	}
	if strings.TrimSpace(payload.LyricsData) != "" {
		t.Fatalf("unrelated row: lyricsData = %q, want it refused", payload.LyricsData)
	}

	// A row carrying a different lyrics tier must not be decoded as the
	// requested one.
	mistiered := newFakeRuntime(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		return hostXMLResponse(`<?xml version='1.0' encoding='UTF-8'?><response><status>00000000</status><songs>` +
			`<song><title>Live Forever</title><artist>Oasis</artist><lyricsType>2</lyricsType><lyricsData>` + base64Encode("line payload") + `</lyricsData></song>` +
			`</songs></response>`), nil
	})
	payload, health, err = fetchPetitLyricsTier(mistiered, request, petitLyricsTypePlain, time.Now().Add(time.Second))
	if err != nil || health != Healthy {
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
	rt := newFakeRuntime(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		return hostXMLResponse(`<?xml version='1.0' encoding='UTF-8'?><response><status>00000001</status><songs></songs></response>`), nil
	})

	_, health, err := fetchPetitLyricsTier(rt, lyrics.TrackInfo{Title: "Live Forever", Artist: "Oasis"}, petitLyricsTypePlain, time.Now().Add(time.Second))
	if err == nil || health != Degraded {
		t.Fatalf("health/err = %v/%v, want a degraded provider failure for an error status", health, err)
	}
}
func TestPetitLyricsMalformedPayloadDegrades(t *testing.T) {
	t.Parallel()

	// Nonempty invalid base64 across every tier degrades instead of caching
	// negative coverage.
	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`<?xml version="1.0"?><response><status>00000000</status><songs><song><lyricsData>!!!not-base64!!!</lyricsData></song></songs></response>`)}, nil
	})

	candidates, health, err := fetchPetitLyrics(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(testLookupBudget))
	if err == nil || health != Degraded || len(candidates) != 0 {
		t.Fatalf("result = %#v/%v/%v; want a degraded malformed payload", candidates, health, err)
	}
}
