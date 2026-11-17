package provider

import (
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

// The host renders native bytes through the plugin's optional original export;
// this pins exact TTML and proprietary QRC bytes below that boundary.
func TestProvider_NativeOriginalsKeepExactBytesAndTrustedHeaders(t *testing.T) {
	t.Parallel()

	cache := seededCache(t)
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist", Album: "Album", Duration: 200}
	ttml := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<tt xmlns="http://www.w3.org/ns/ttml" xmlns:itunes="http://music.apple.com/lyric-ttml-internal">
  <head><metadata><iTunesMetadata xmlns="http://music.apple.com/lyric-ttml-internal">
    <translations><translation xml:lang="es"><text for="L1">Hola</text></translation></translations>
  </iTunesMetadata></metadata></head>
  <body xml:lang="en"><div><p begin="00:01.000" end="00:02.000" itunes:key="L1">Hello</p></div></body>
</tt>`)
	qrc := []byte("<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<QrcInfos>\n<QrcHeadInfo SaveTime=\"231\" Version=\"100\"/>\n<LyricInfo LyricCount=\"1\">\n<Lyric_1 LyricType=\"1\" LyricContent=\"[0,1000]line(0,500) (500,500)one\"/>\n</LyricInfo>\n</QrcInfos>\n")
	src := source.Source{
		ID: "native", Display: "Native", Hosts: []string{"native.test"}, DefaultEnabled: true,
		Originals: source.OriginalsNative,
		Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
			return []lyric.Candidate{
				lyric.NewCandidate(lyrics.LyricsText{Text: string(ttml)}, "TTML Provider", "ttml").
					WithOriginal("ttml", lyric.OriginalSupplied, ttml).
					WithRecording("rec-ttml"),
				lyric.NewCandidate(lyrics.LyricsText{Text: "[00:01.00]line"}, "QRC Provider", "elrc").
					WithOriginal("qrc", lyric.OriginalSupplied, qrc).
					WithRecording("rec-qrc"),
				lyric.NewCandidate(lyrics.LyricsText{Text: "[00:01.00]reconstructed"}, "Converted Provider", "elrc").
					WithOriginal("qrc", lyric.OriginalReconstructed, qrc).
					WithRecording("rec-converted"),
			}, source.Healthy, nil
		},
	}
	p := newTestProviderWithSources(nil, cache, time.Now, []source.Source{src}, nil)
	if _, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: track}); err != nil {
		t.Fatalf("GetLyrics() error = %v", err)
	}

	byOriginal := map[string]VariantSummary{}
	for _, summary := range p.GetLyricsVariants(track) {
		if summary.Original.Available {
			byOriginal[summary.Original.Format] = summary
		}
	}
	for _, format := range []string{"ttml", "qrc"} {
		summary, ok := byOriginal[format]
		if !ok {
			t.Fatalf("listing has no available %s original: %#v", format, p.GetLyricsVariants(track))
		}
		document, reason := p.GetLyricsOriginal(track, summary.Identity)
		want := map[string][]byte{"ttml": ttml, "qrc": qrc}[format]
		if reason != "" || string(document.Bytes) != string(want) || document.Format != format {
			t.Fatalf("%s original = %#v/%q, want exact supplied bytes", format, document, reason)
		}
		if summary.Original.MediaType == "" || summary.Original.Extension == "" {
			t.Fatalf("%s original headers = %#v", format, summary.Original)
		}
	}
	if _, ok := byOriginal["reconstructed"]; ok {
		t.Fatal("a reconstructed document must never be listed as an original")
	}
	var converted VariantSummary
	for _, summary := range p.GetLyricsVariants(track) {
		if summary.Provider == "Converted Provider" {
			converted = summary
		}
	}
	if converted.Identity.Recording == "" {
		t.Fatal("converted candidate was not listed")
	}
	if converted.Original.Available || converted.Original.Reason == "" {
		t.Fatalf("converted original = %#v, want explicit unavailable", converted.Original)
	}
	document, reason := p.GetLyricsOriginal(track, converted.Identity)
	if reason == "" || len(document.Bytes) != 0 {
		t.Fatalf("converted original retrieval = %#v/%q, want unavailable with no bytes", document, reason)
	}
}
