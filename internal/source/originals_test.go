package source

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
)

func TestCatalogOriginalInventoryIsExplicit(t *testing.T) {
	t.Parallel()

	want := map[string]OriginalSupport{
		"better-lyrics":         OriginalsNative,
		"unison":                OriginalsNative,
		"binimum":               OriginalsNative,
		"amll":                  OriginalsNative,
		"luna-beat":             OriginalsNative,
		"apple-lyrically":       OriginalsConditional,
		"better-lyrics-portato": OriginalsNative,
		"netease":               OriginalsNative,
		"luna":                  OriginalsNative,
		"petitlyrics":           OriginalsNative,
		"lrclib":                OriginalsNative,
		"better-lyrics-legato":  OriginalsNative,
		"lyricify":              OriginalsNative,
		"lyricsplus":            OriginalsUnavailable,
		"yt-music":              OriginalsUnavailable,
		"genius":                OriginalsUnavailable,
	}
	if len(Catalog) != len(want) {
		t.Fatalf("catalog has %d adapters, want the pinned %d", len(Catalog), len(want))
	}
	for _, src := range Catalog {
		support, ok := want[src.ID]
		if !ok {
			t.Fatalf("adapter %q is missing from the originals inventory", src.ID)
		}
		if src.Originals != support {
			t.Fatalf("adapter %q originals = %q, want %q", src.ID, src.Originals, support)
		}
		if strings.TrimSpace(src.OriginalsNote) == "" {
			t.Fatalf("adapter %q needs an explicit originals note", src.ID)
		}
		delete(want, src.ID)
	}
	if len(want) != 0 {
		t.Fatalf("inventory names unknown adapters: %v", want)
	}
}

func TestNativeDocumentFixturesMatchProvenanceHashes(t *testing.T) {
	t.Parallel()

	cases := []struct{ path, hash string }{
		{"testdata/portato/lyrics.qrc", "729723cb9d31592d522a0142bb52403680eef513bed0b2180e32df0541048f56"},
		{"testdata/luna/lyrics.krc", "8ac90086413f01c475a326b48aa85ff3822e8d5dab0325b8572356997b1b83df"},
		{"testdata/binimum/lyrics.ttml", "cf2a75ae7a6f75e14a19cc022fa968e5477b708b07e03bbc568232a81a5a3303"},
	}
	for _, tc := range cases {
		document, err := os.ReadFile(tc.path)
		if err != nil {
			t.Fatalf("read %s: %v", tc.path, err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(document)); got != tc.hash {
			t.Fatalf("%s SHA-256 = %s, want %s", tc.path, got, tc.hash)
		}
	}
}

func TestPortatoOriginalIsTheQRCContainer(t *testing.T) {
	t.Parallel()

	envelope, err := os.ReadFile("testdata/betterlyrics/qq.txt")
	if err != nil {
		t.Fatalf("read envelope: %v", err)
	}
	native, err := os.ReadFile("testdata/portato/lyrics.qrc")
	if err != nil {
		t.Fatalf("read native fixture: %v", err)
	}
	var payload struct {
		Lyrics string `json:"lyrics"`
	}
	if err := json.Unmarshal(envelope, &payload); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if string(native) != payload.Lyrics {
		t.Fatal("native fixture must be the exact lyrics field from the envelope")
	}
	extracted, err := portatoQRCContent(payload.Lyrics)
	if err != nil {
		t.Fatalf("extract QRC rows: %v", err)
	}

	rt := newFakeRuntime(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		return &host.HTTPResponse{StatusCode: 200, Body: envelope}, nil
	})
	candidates, _, err := fetchPortato(rt, lyrics.TrackInfo{Title: "Shape of You", Artist: "Ed Sheeran", Duration: 234}, time.Now().Add(testLookupBudget))
	if err != nil || len(candidates) != 1 {
		t.Fatalf("fetchPortato() = %#v/%v, want one candidate", candidates, err)
	}
	original := candidates[0].Original
	if original == nil || original.Format != "qrc" || original.Provenance != lyric.OriginalSupplied {
		t.Fatalf("original = %#v, want the supplied QRC container", original)
	}
	if string(original.Bytes) != string(native) {
		t.Fatalf("original SHA-256 = %x, want the exact QRC container", sha256.Sum256(original.Bytes))
	}
	if string(original.Bytes) == string(envelope) {
		t.Fatal("the outer API envelope must never be the original")
	}
	if string(original.Bytes) == extracted {
		t.Fatal("the extracted LyricContent rows must not be labelled the original")
	}
	if strings.Contains(candidates[0].Response.Lyrics[0].Text, "QrcInfos") {
		t.Fatal("playback must be the converted ELRC, not the QRC container")
	}
}

func TestPortatoOriginalIsRawQRCWithoutContainer(t *testing.T) {
	t.Parallel()

	raw := "[0,2380]Shape(0,476) (476,95)of(571,190)\n"
	rt := newFakeRuntime(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"lyrics":` + fmt.Sprintf("%q", raw) + `}`)}, nil
	})
	candidates, _, err := fetchPortato(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(testLookupBudget))
	if err != nil || len(candidates) != 1 {
		t.Fatalf("fetchPortato() = %#v/%v", candidates, err)
	}
	if candidates[0].Original == nil || string(candidates[0].Original.Bytes) != raw {
		t.Fatalf("original = %#v, want the exact raw QRC rows", candidates[0].Original)
	}
}

func TestLunaOriginalIsKRCContent(t *testing.T) {
	t.Parallel()

	native, err := os.ReadFile("testdata/luna/lyrics.krc")
	if err != nil {
		t.Fatalf("read native fixture: %v", err)
	}
	search, _ := os.ReadFile("testdata/luna/search.json")
	detail, _ := os.ReadFile("testdata/luna/detail.json")
	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "api.qishui.com/luna/search/track") {
			return &host.HTTPResponse{StatusCode: 200, Body: search}, nil
		}
		return &host.HTTPResponse{StatusCode: 200, Body: detail}, nil
	})
	candidates, _, err := fetchLuna(rt, lyrics.TrackInfo{Title: "Live Forever", Artist: "Oasis", Duration: 130}, time.Now().Add(testLookupBudget))
	if err != nil || len(candidates) != 1 {
		t.Fatalf("fetchLuna() = %#v/%v", candidates, err)
	}
	if candidates[0].Original == nil || candidates[0].Original.Format != "krc" || string(candidates[0].Original.Bytes) != string(native) {
		t.Fatalf("original = %#v, want the exact lyric.content KRC bytes", candidates[0].Original)
	}
	if candidates[0].Recording == "" || !strings.HasPrefix(candidates[0].Recording, "7425699317225293865") {
		t.Fatalf("recording = %q, want the Luna track id", candidates[0].Recording)
	}
}

func TestPetitLyricsOriginalIsDecodedWSY(t *testing.T) {
	t.Parallel()

	wsy, err := os.ReadFile("../lyric/testdata/petitlyrics/word.xml")
	if err != nil {
		t.Fatalf("read WSY fixture: %v", err)
	}
	envelope := fmt.Sprintf(`<response><status>00000000</status><songs><song><lyricsType>3</lyricsType><lyricsData>%s</lyricsData><title>Song</title><artist>Artist</artist></song></songs></response>`, base64.StdEncoding.EncodeToString(wsy))
	rt := newFakeRuntime(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(envelope)}, nil
	})
	candidates, _, err := fetchPetitLyrics(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(testLookupBudget))
	if err != nil || len(candidates) != 1 {
		t.Fatalf("fetchPetitLyrics() = %#v/%v", candidates, err)
	}
	if candidates[0].Original == nil || candidates[0].Original.Format != "wsy" || string(candidates[0].Original.Bytes) != string(wsy) {
		t.Fatalf("original = %#v, want the decoded WSY bytes", candidates[0].Original)
	}
}

func TestLyricifyOriginalIsPreOffsetLYL(t *testing.T) {
	t.Parallel()

	resolve, _ := os.ReadFile("testdata/lyricify/resolve.json")
	lyricsBody, _ := os.ReadFile("testdata/lyricify/lyrics.json")
	var payload struct {
		Text   string `json:"text"`
		Offset int64  `json:"offset"`
	}
	if err := json.Unmarshal(lyricsBody, &payload); err != nil {
		t.Fatalf("decode lyrics fixture: %v", err)
	}
	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "api.credits.fm") {
			return &host.HTTPResponse{StatusCode: 200, Body: resolve}, nil
		}
		return &host.HTTPResponse{StatusCode: 200, Body: lyricsBody}, nil
	})
	candidates, _, err := fetchLyricify(rt, lyrics.TrackInfo{Title: "Shape of You", Artist: "Ed Sheeran"}, time.Now().Add(testLookupBudget))
	if err != nil || len(candidates) != 1 {
		t.Fatalf("fetchLyricify() = %#v/%v", candidates, err)
	}
	if candidates[0].Original == nil || candidates[0].Original.Format != "lyl" || string(candidates[0].Original.Bytes) != payload.Text {
		t.Fatalf("original = %#v, want the exact pre-offset LYL document", candidates[0].Original)
	}
	if strings.Contains(candidates[0].Response.Lyrics[0].Text, "[type:LyricifyLines]") {
		t.Fatal("playback must be converted ELRC, not the LYL document")
	}
	if candidates[0].Recording != "GBAHS1600463" {
		t.Fatalf("recording = %q, want the ISRC", candidates[0].Recording)
	}
}

func TestBetterLyricsAndBinimumOriginalsAreExactTTML(t *testing.T) {
	t.Parallel()

	ttml := "<tt xmlns=\"http://www.w3.org/ns/ttml\"><body><p begin=\"1\" end=\"2\">line</p></body></tt>"
	rt := newFakeRuntime(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"ttml":` + fmt.Sprintf("%q", ttml) + `}`)}, nil
	})
	candidates, _, err := fetchBetterLyrics(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(testLookupBudget))
	if err != nil || len(candidates) != 1 {
		t.Fatalf("fetchBetterLyrics() = %#v/%v", candidates, err)
	}
	if candidates[0].Original == nil || string(candidates[0].Original.Bytes) != ttml {
		t.Fatalf("original = %#v, want the exact TTML field", candidates[0].Original)
	}
}

func TestLyricallyOriginalsFollowProvenance(t *testing.T) {
	t.Parallel()

	search := `{"results":[{"trackId":9,"trackName":"Song","artistName":"Artist","trackTimeMillis":200000}]}`
	run := func(mirror string) []lyric.Candidate {
		t.Helper()
		rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
			if strings.Contains(request.URL, "itunes.apple.com") {
				return &host.HTTPResponse{StatusCode: 200, Body: []byte(search)}, nil
			}
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(mirror)}, nil
		})
		candidates, _, err := fetchLyrically(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 200}, time.Now().Add(testLookupBudget))
		if err != nil || len(candidates) != 1 {
			t.Fatalf("fetchLyrically() = %#v/%v", candidates, err)
		}
		return candidates
	}

	withTTML := run(`{"elrc":"[00:01.00]line","ttmlContent":"  <tt><body><p begin=\"1\" end=\"2\">line</p></body></tt>  "}`)
	if withTTML[0].Original == nil || withTTML[0].Original.Format != "ttml" || string(withTTML[0].Original.Bytes) != "  <tt><body><p begin=\"1\" end=\"2\">line</p></body></tt>  " {
		t.Fatalf("original = %#v, want the exact untrimmed Apple TTML", withTTML[0].Original)
	}
	if withTTML[0].Original.Provenance != lyric.OriginalSupplied {
		t.Fatalf("provenance = %q, want the supplied document", withTTML[0].Original.Provenance)
	}
	if withTTML[0].Original.Format == withTTML[0].Format() {
		t.Fatal("source and playback formats must stay distinct")
	}

	convertedOnly := run(`{"elrc":"[00:01.00]line"}`)
	if convertedOnly[0].Original != nil {
		t.Fatalf("original = %#v, want unavailable for a mirror conversion only", convertedOnly[0].Original)
	}
	if got := convertedOnly[0].OriginalDetails().Reason; got != lyric.OriginalReasonConversionOnly {
		t.Fatalf("reason = %q, want the explicit conversion reason", got)
	}
}

func TestLyricsPlusOriginalIsUnavailable(t *testing.T) {
	t.Parallel()

	body := `{"lyrics":[{"time":1000,"duration":2000,"text":"line","syllabus":[]}]}`
	rt := newFakeRuntime(func(host.HTTPRequest) (*host.HTTPResponse, error) {
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(body)}, nil
	})
	candidates, _, err := fetchLyricsPlus(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(testLookupBudget))
	if err != nil || len(candidates) != 1 {
		t.Fatalf("fetchLyricsPlus() = %#v/%v", candidates, err)
	}
	if candidates[0].Original != nil {
		t.Fatalf("original = %#v, want unavailable for structured JSON rows", candidates[0].Original)
	}
	if got := candidates[0].OriginalDetails().Reason; got != lyric.OriginalReasonReconstructed {
		t.Fatalf("reason = %q, want the explicit reconstruction reason", got)
	}
}
