package main

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func catalogSources(ids []string) []lyricsSource {
	sources := make([]lyricsSource, 0, len(ids))
	for _, id := range ids {
		if source, ok := catalogSource(id); ok {
			sources = append(sources, source)
		}
	}
	return sources
}

func TestAllSources_ImplementedFetchesAreAttached(t *testing.T) {
	t.Parallel()

	// Sources whose Fetch is nil are only unreachable skeletons: they must be a
	// deliberately bounded set, not silent wiring drops.
	for _, source := range allSources {
		if _, wired := sourceFetches[source.ID]; !wired {
			t.Fatalf("source %q has no Fetch wiring", source.ID)
		}
	}
}

func TestLiveSearchFixturesDecodeWithRealWireNames(t *testing.T) {
	t.Parallel()

	// Pin the captured wire shapes: the live payloads disagree with earlier
	// assumptions (NetEase `dt`, Musixmatch `track_length`, quoted Luna ids)
	// and each mismatch silently neutralized the source.
	netease := jsonUnmarshal[neteaseSearchEnvelope](t, "testdata/netease/search.json")
	song, ok := pickNetEaseSong(netease.Result.Songs, lyrics.TrackInfo{Title: "Live Forever", Artist: "Oasis", Duration: 277})
	if !ok {
		t.Fatalf("pickNetEaseSong() over the live wire missed; songs = %#v", netease.Result.Songs[:1])
	}
	if song.ID != 17822787 || song.Duration != 276866 {
		t.Fatalf("netease song = %#v, want id 17822787 dur 276866 from `dt`", song)
	}

	musixmatch := jsonUnmarshal[musixmatchEnvelope](t, "testdata/musixmatch/search.json")
	if len(musixmatch.Message.Body.TrackList) == 0 {
		t.Fatal("live Musixmatch fixture has no track_list")
	}
	track := musixmatch.Message.Body.TrackList[0].Track
	if track.TrackID != 360230953 || track.Duration != 234 {
		t.Fatalf("musixmatch track = %#v, want id 360230953 dur 234 from `track_length`", track)
	}

	luna := jsonUnmarshal[lunaSearchEnvelope](t, "testdata/luna/search.json")
	id, found := pickLunaTrack(luna.ResultGroups, lyrics.TrackInfo{Title: "Live Forever", Artist: "Oasis", Duration: 130})
	if !found || id != "7425699317225293865" {
		t.Fatalf("pickLunaTrack() = (%q, %v); want the live quoted id via the collapsed metadata gate", id, found)
	}
}

func jsonUnmarshal[T any](t *testing.T, name string) T {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return value
}

func TestMalformedProviderJSONStaysDegraded(t *testing.T) {
	t.Parallel()

	// A 200 carrying garbage is a provider failure for each new source, not
	// cacheable coverage.
	cases := []struct {
		name  string
		fetch func(*lyricsProvider) (lookupHealth, error)
	}{
		{
			name: "youtube music search",
			fetch: func(p *lyricsProvider) (lookupHealth, error) {
				_, health, err := fetchYTMusicLyricsCandidates(p, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
				return health, err
			},
		},
		{
			name: "genius search",
			fetch: func(p *lyricsProvider) (lookupHealth, error) {
				_, health, err := fetchGeniusLyricsCandidates(p, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(stageOneLookupBudget))
				return health, err
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
				return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"broken": tru`)}, nil
			})
			health, err := tc.fetch(provider)
			if err == nil || health != lookupDegraded {
				t.Fatalf("result = (%v, %v); want a degraded decode failure", err, health)
			}
		})
	}
}
