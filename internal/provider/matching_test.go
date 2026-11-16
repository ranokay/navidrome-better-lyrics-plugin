package provider

import (
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

func matchingCandidate(text, format string, info lyric.RecordingInfo) lyric.Candidate {
	return lyric.NewCandidate(lyrics.LyricsText{Text: text}, "stub", format).WithRecordingInfo(info)
}

func TestValidatedRecordingBeatsRicherWrongRecording(t *testing.T) {
	t.Parallel()
	var calls []string
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist", Album: "Album", Duration: 194}
	sources := []source.Source{
		stubSource("wrong", &calls, func() ([]lyric.Candidate, source.Health, error) {
			return []lyric.Candidate{matchingCandidate(
				"[00:01.00]<00:01.00>rich<00:02.00>word", "elrc",
				lyric.RecordingInfo{Title: "Song (Live)", Artist: "Artist", Album: "Live", Duration: 196},
			)}, source.Healthy, nil
		}),
		stubSource("right", &calls, func() ([]lyric.Candidate, source.Health, error) {
			return []lyric.Candidate{matchingCandidate(
				"[00:01.00]line", "lrc",
				lyric.RecordingInfo{Title: "Song", Artist: "Artist", Album: "Album", Duration: 194},
			)}, source.Healthy, nil
		}),
	}
	p := newTestProviderWithSources(nil, newFakeLyricsCache(), time.Now, sources, nil)

	result, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: track})
	if err != nil {
		t.Fatal(err)
	}
	if text, _ := winnerText(t, result); text != "[00:01.00]line" {
		t.Fatalf("winner = %q, want the validated line result to beat richer ambiguous word timing", text)
	}
	if len(result.Lyrics) != 1 {
		t.Fatalf("v1 entries = %d, want only the validated playback result", len(result.Lyrics))
	}
	if want := []string{"wrong", "right"}; !equalStrings(calls, want) {
		t.Fatalf("calls = %v; ambiguous synchronized result must not stop the walk before validated sources", calls)
	}
	v2, err := p.GetLyricsV2(lyrics.GetLyricsRequest{Track: track})
	if err != nil {
		t.Fatal(err)
	}
	if len(v2.Entries) != 2 || v2.Entries[0].Match.Status != lyric.MatchValidated || v2.Entries[1].Match.Status != lyric.MatchAmbiguous {
		t.Fatalf("v2 entries = %#v, want validated winner and ambiguous suggestion", v2.Entries)
	}
	if !v2.Entries[0].Match.AutoSave || v2.Entries[1].Match.AutoSave {
		t.Fatal("only the validated recording may be auto-save eligible")
	}
}

func TestOnlyAmbiguousResultsAreSuggestionsNotPlayback(t *testing.T) {
	t.Parallel()
	var calls []string
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist", Album: "Album", Duration: 194}
	sources := []source.Source{
		stubSource("wrong", &calls, func() ([]lyric.Candidate, source.Health, error) {
			return []lyric.Candidate{matchingCandidate(
				"[00:01.00]line", "lrc",
				lyric.RecordingInfo{Title: "Song (Live)", Artist: "Artist", Album: "Live", Duration: 196},
			)}, source.Healthy, nil
		}),
	}
	p := newTestProviderWithSources(nil, newFakeLyricsCache(), time.Now, sources, nil)

	result, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: track})
	if err != nil || len(result.Lyrics) != 0 {
		t.Fatalf("v1 = %#v/%v, want no automatic playback for an ambiguous-only result", result, err)
	}
	v2, err := p.GetLyricsV2(lyrics.GetLyricsRequest{Track: track})
	if err != nil || len(v2.Entries) != 1 || v2.Entries[0].Match.Status != lyric.MatchAmbiguous {
		t.Fatalf("v2 = %#v/%v, want one ambiguous suggestion", v2, err)
	}
	listing := p.GetLyricsVariants(track)
	if len(listing) != 1 || listing[0].Match.Status != lyric.MatchAmbiguous || listing[0].Match.AutoSave {
		t.Fatalf("listing = %#v, want one non-save suggestion", listing)
	}
}

func TestInstrumentalEvidenceSurvivesAndDoesNotVetoLyrics(t *testing.T) {
	t.Parallel()
	var calls []string
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist", Album: "Album", Duration: 194}
	assertion := lyric.Candidate{
		RecordingInfo: lyric.RecordingInfo{Title: "Song", Artist: "Artist", Album: "Album", Duration: 194},
		Evidence:      []lyric.Evidence{{Kind: lyric.EvidenceInstrumental, Provider: "LRCLib", Recording: "1"}},
	}
	sources := []source.Source{
		stubSource("instrumental", &calls, func() ([]lyric.Candidate, source.Health, error) {
			return []lyric.Candidate{assertion}, source.Healthy, nil
		}),
		stubSource("lyrics", &calls, func() ([]lyric.Candidate, source.Health, error) {
			return []lyric.Candidate{matchingCandidate(
				"[00:01.00]line", "lrc",
				lyric.RecordingInfo{Title: "Song", Artist: "Artist", Album: "Album", Duration: 194},
			)}, source.Healthy, nil
		}),
	}
	p := newTestProviderWithSources(nil, newFakeLyricsCache(), time.Now, sources, nil)

	result, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: track})
	if err != nil || len(result.Lyrics) != 1 || result.Lyrics[0].Text != "[00:01.00]line" {
		t.Fatalf("v1 = %#v/%v, want lyrics despite the instrumental assertion", result, err)
	}
	v2, err := p.GetLyricsV2(lyrics.GetLyricsRequest{Track: track})
	if err != nil || len(v2.Evidence) != 1 || v2.Evidence[0].Provider != "LRCLib" {
		t.Fatalf("v2 evidence = %#v/%v, want attributed evidence beside lyrics", v2, err)
	}
	cached, err := p.GetLyricsV2(lyrics.GetLyricsRequest{Track: track})
	if err != nil || len(cached.Evidence) != 1 {
		t.Fatalf("cached evidence = %#v/%v, want evidence to survive the cache", cached, err)
	}
}

func TestInstrumentalEvidenceOnlyProducesTypedEvidence(t *testing.T) {
	t.Parallel()
	var calls []string
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist", Album: "Album", Duration: 194}
	sources := []source.Source{
		stubSource("instrumental", &calls, func() ([]lyric.Candidate, source.Health, error) {
			return []lyric.Candidate{{
				RecordingInfo: lyric.RecordingInfo{Title: "Song", Artist: "Artist", Album: "Album", Duration: 194},
				Evidence:      []lyric.Evidence{{Kind: lyric.EvidenceInstrumental, Provider: "LRCLib", Recording: "1"}},
			}}, source.Healthy, nil
		}),
	}
	p := newTestProviderWithSources(nil, newFakeLyricsCache(), time.Now, sources, nil)

	result, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: track})
	if err != nil || len(result.Lyrics) != 0 {
		t.Fatalf("v1 = %#v/%v, want no lyric entry for evidence-only", result, err)
	}
	v2, err := p.GetLyricsV2(lyrics.GetLyricsRequest{Track: track})
	if err != nil || len(v2.Entries) != 0 || len(v2.Evidence) != 1 {
		t.Fatalf("v2 = %#v/%v, want typed evidence separate from entries", v2, err)
	}
	cached, err := p.GetLyricsV2(lyrics.GetLyricsRequest{Track: track})
	if err != nil || len(cached.Evidence) != 1 {
		t.Fatalf("cached evidence = %#v/%v, want evidence to survive the negative cache", cached, err)
	}
}

func TestEmptyAndErrorResponsesDoNotAssertInstrumental(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		fn   func() ([]lyric.Candidate, source.Health, error)
	}{
		{name: "empty", fn: func() ([]lyric.Candidate, source.Health, error) { return nil, source.Healthy, nil }},
		{name: "error", fn: func() ([]lyric.Candidate, source.Health, error) { return nil, source.Degraded, errTestOutage }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			track := lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 194}
			p := newTestProviderWithSources(nil, newFakeLyricsCache(), time.Now, []source.Source{
				stubSource("one", &calls, test.fn),
			}, nil)
			v2, err := p.GetLyricsV2(lyrics.GetLyricsRequest{Track: track})
			if test.name == "error" {
				if err == nil {
					t.Fatal("want the outage error preserved")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if len(v2.Evidence) != 0 {
				t.Fatalf("evidence = %#v, want none for empty/error", v2.Evidence)
			}
		})
	}
}

var errTestOutage = &testOutageError{}

type testOutageError struct{}

func (*testOutageError) Error() string { return "outage" }

func seededMatchingCandidate(text, format string, match lyric.RecordingMatch) lyric.Candidate {
	candidate := lyric.NewCandidate(lyrics.LyricsText{Text: text}, "stub", format)
	candidate.Match = match
	return candidate
}

func TestCachedRankingIsValidityFirst(t *testing.T) {
	t.Parallel()
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist", Album: "Album", Duration: 194}
	unassessed := seededMatchingCandidate("[00:01.00]<00:01.00>rich<00:02.00>word", "elrc", lyric.RecordingMatch{Status: lyric.MatchUnassessed})
	validated := seededMatchingCandidate("[00:01.00]validated line", "lrc", lyric.RecordingMatch{Status: lyric.MatchValidated, AutoSave: true})
	sources := []source.Source{{ID: "one"}, {ID: "two"}}
	p := newTestProviderWithSources(nil, newFakeLyricsCache(), time.Now, sources, nil)
	p.cacheWinner(track, sources, unassessed, false)
	p.cacheSourceCandidate(track, "two", validated)

	result, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: track})
	if err != nil || result.Lyrics[0].Text != "[00:01.00]validated line" {
		t.Fatalf("cached winner re-rank = %#v/%v, want validated line over richer unassessed", result, err)
	}
	winner, _, ok, _ := p.readCachedWinner(track, sources)
	if !ok || winner.Match.Status != lyric.MatchValidated {
		t.Fatalf("promoted winner = %#v, want validated candidate", winner)
	}
}

func TestColdWinnerUsesValidityFirstSourceCacheOrder(t *testing.T) {
	t.Parallel()
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist", Album: "Album", Duration: 194}
	unassessed := seededMatchingCandidate("[00:01.00]<00:01.00>rich<00:02.00>word", "elrc", lyric.RecordingMatch{Status: lyric.MatchUnassessed})
	validated := seededMatchingCandidate("[00:01.00]validated line", "lrc", lyric.RecordingMatch{Status: lyric.MatchValidated, AutoSave: true})
	for _, order := range [][]string{{"one", "two"}, {"two", "one"}} {
		sources := []source.Source{{ID: order[0]}, {ID: order[1]}}
		p := newTestProviderWithSources(nil, newFakeLyricsCache(), time.Now, sources, nil)
		p.cacheSourceCandidate(track, "one", unassessed)
		p.cacheSourceCandidate(track, "two", validated)
		result, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: track})
		if err != nil || result.Lyrics[0].Text != "[00:01.00]validated line" {
			t.Fatalf("cold cached winner order %v = %#v/%v, want validated candidate", order, result, err)
		}
	}
}

func TestAssertionSurvivesAnotherSourceOutage(t *testing.T) {
	t.Parallel()
	var assertionCalls, outageCalls int
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist", Album: "Album", Duration: 194}
	assertion := lyric.Candidate{
		RecordingInfo: lyric.RecordingInfo{Title: "Song", Artist: "Artist", Album: "Album", Duration: 194},
		Evidence:      []lyric.Evidence{{Kind: lyric.EvidenceInstrumental, Provider: "LRCLib", Recording: "1"}},
	}
	sources := []source.Source{
		stubSource("assertion", &[]string{}, func() ([]lyric.Candidate, source.Health, error) {
			assertionCalls++
			return []lyric.Candidate{assertion}, source.Healthy, nil
		}),
		stubSource("outage", &[]string{}, func() ([]lyric.Candidate, source.Health, error) {
			outageCalls++
			return nil, source.Degraded, errTestOutage
		}),
	}
	p := newTestProviderWithSources(nil, newFakeLyricsCache(), time.Now, sources, nil)
	v2, err := p.GetLyricsV2(lyrics.GetLyricsRequest{Track: track})
	if err != nil || len(v2.Evidence) != 1 {
		t.Fatalf("v2 = %#v/%v, want the assertion despite the other outage", v2, err)
	}
	second, err := p.GetLyricsV2(lyrics.GetLyricsRequest{Track: track})
	if err != nil || len(second.Evidence) != 1 {
		t.Fatalf("cached v2 = %#v/%v, want evidence", second, err)
	}
	if assertionCalls != 1 {
		t.Fatalf("assertion source requests = %d, want the cached evidence to replay once", assertionCalls)
	}
	if outageCalls == 0 {
		t.Fatal("want the outage source attempted")
	}
}

func TestAmbiguousSuggestionSurvivesSourceOutage(t *testing.T) {
	t.Parallel()
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist", Album: "Album", Duration: 194}
	ambiguous := matchingCandidate("[00:01.00]live", "lrc",
		lyric.RecordingInfo{Title: "Song (Live)", Artist: "Artist", Album: "Live", Duration: 196})
	sources := []source.Source{
		stubSource("ambiguous", &[]string{}, func() ([]lyric.Candidate, source.Health, error) {
			return []lyric.Candidate{ambiguous}, source.Degraded, errTestOutage
		}),
	}
	p := newTestProviderWithSources(nil, newFakeLyricsCache(), time.Now, sources, nil)
	v2, err := p.GetLyricsV2(lyrics.GetLyricsRequest{Track: track})
	if err != nil || len(v2.Entries) != 1 || v2.Entries[0].Match.Status != lyric.MatchAmbiguous {
		t.Fatalf("v2 = %#v/%v, want the ambiguous suggestion despite the outage", v2, err)
	}
	v1, _ := p.GetLyrics(lyrics.GetLyricsRequest{Track: track})
	if len(v1.Lyrics) != 0 {
		t.Fatalf("v1 = %#v, want no auto-playback", v1.Lyrics)
	}
}
