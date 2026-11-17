package provider

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

type publicCorpusCase struct {
	ID        string              `json:"id"`
	Label     string              `json:"label"`
	Timing    string              `json:"timing"`
	Evidence  bool                `json:"evidence"`
	Synthetic bool                `json:"synthetic"`
	Request   lyric.RecordingInfo `json:"request"`
	Returned  lyric.RecordingInfo `json:"returned"`
	Expect    struct {
		Status   lyric.MatchStatus `json:"status"`
		AutoSave bool              `json:"autoSave"`
	} `json:"expect"`
}

func TestPublicMatchingCorpusBehavior(t *testing.T) {
	raw, err := os.ReadFile("../lyric/testdata/matching/corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Cases []publicCorpusCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	if len(corpus.Cases) < 16 {
		t.Fatalf("corpus cases = %d", len(corpus.Cases))
	}
	started := time.Now()
	totalRequests := 0
	for _, test := range corpus.Cases {
		t.Run(test.ID, func(t *testing.T) {
			calls := 0
			candidate := corpusCandidate(test)
			src := source.Source{
				ID: "corpus", Display: "Corpus", Hosts: []string{"corpus.test"}, DefaultEnabled: true,
				Fetch: func(source.Runtime, lyrics.TrackInfo, time.Time) ([]lyric.Candidate, source.Health, error) {
					calls++
					return []lyric.Candidate{candidate}, source.Healthy, nil
				},
			}
			p := newTestProviderWithSources(nil, newFakeLyricsCache(), time.Now, []source.Source{src}, nil)
			track := lyrics.TrackInfo{
				Title: test.Request.Title, Artist: test.Request.Artist,
				Album: test.Request.Album, Duration: float32(test.Request.Duration),
			}
			v1, err := p.GetLyrics(lyrics.GetLyricsRequest{Track: track})
			if err != nil {
				t.Fatal(err)
			}
			v2, err := p.GetLyricsV2(lyrics.GetLyricsRequest{Track: track})
			if err != nil {
				t.Fatal(err)
			}
			listing := p.GetLyricsVariants(track)
			second, err := p.GetLyricsV2(lyrics.GetLyricsRequest{Track: track})
			if err != nil {
				t.Fatal(err)
			}
			totalRequests += calls
			if calls != 1 {
				t.Fatalf("source requests = %d, want the cache to bound a replay to the first fetch", calls)
			}
			if len(second.Entries) != len(v2.Entries) {
				t.Fatalf("cached entries = %d, want %d", len(second.Entries), len(v2.Entries))
			}
			switch test.Expect.Status {
			case lyric.MatchValidated:
				if len(v1.Lyrics) == 0 || len(v2.Entries) == 0 {
					t.Fatalf("validated case produced no playback: v1=%#v v2=%#v", v1, v2)
				}
				if v2.Entries[0].Match.Status != lyric.MatchValidated || !v2.Entries[0].Match.AutoSave {
					t.Fatalf("v2 match = %#v, want validated auto-save", v2.Entries[0].Match)
				}
				if len(listing) == 0 || listing[0].Match.Status != lyric.MatchValidated {
					t.Fatalf("listing = %#v, want validated", listing)
				}
			case lyric.MatchAmbiguous:
				if len(v1.Lyrics) != 0 {
					t.Fatalf("ambiguous case auto-played: %#v", v1.Lyrics)
				}
				if len(v2.Entries) != 1 || v2.Entries[0].Match.Status != lyric.MatchAmbiguous || v2.Entries[0].Match.AutoSave {
					t.Fatalf("v2 = %#v, want one non-save suggestion", v2.Entries)
				}
				if len(listing) != 1 || listing[0].Match.Status != lyric.MatchAmbiguous {
					t.Fatalf("listing = %#v, want one suggestion", listing)
				}
			case lyric.MatchMismatch:
				if len(v1.Lyrics) != 0 || len(v2.Entries) != 0 || len(listing) != 0 {
					t.Fatalf("mismatch was exposed: v1=%#v v2=%#v listing=%#v", v1, v2, listing)
				}
			default:
				t.Fatalf("unexpected expected status %q", test.Expect.Status)
			}
			if test.Evidence && len(v2.Evidence) == 0 {
				t.Fatalf("v2 = %#v, want typed evidence", v2)
			}
			if !test.Evidence && len(v2.Evidence) != 0 {
				t.Fatalf("v2 evidence = %#v, want none", v2.Evidence)
			}
		})
	}
	t.Logf("public corpus cases=%d sourceRequests=%d elapsed=%s", len(corpus.Cases), totalRequests, time.Since(started))
}

func corpusCandidate(test publicCorpusCase) lyric.Candidate {
	text := "plain line"
	format := "plain"
	switch test.Timing {
	case "line":
		text, format = "[00:01.00]line", "lrc"
	case "word":
		text, format = "[00:01.00]<00:01.00>line<00:01.50>word", "elrc"
	}
	candidate := lyric.NewCandidate(lyrics.LyricsText{Text: text}, "Corpus", format).
		WithRecordingInfo(test.Returned)
	if test.Evidence {
		candidate.Evidence = []lyric.Evidence{{
			Kind: lyric.EvidenceInstrumental, Provider: "Corpus", Recording: test.ID,
		}}
	}
	return candidate
}
