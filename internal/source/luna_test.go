package source

import (
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
)

func TestLunaPickFailsClosedWithoutNormalizedIdentity(t *testing.T) {
	t.Parallel()

	// The collapsed-containment gate treats an empty needle as matching every
	// row (strings.Contains(x, "") is true), and an artist that collapses to
	// nothing used to accept any name.
	row := lunaGroupDatum{Entity: struct {
		Track lunaSearchTrack `json:"track"`
	}{Track: lunaSearchTrack{ID: "42", Name: "Live Forever", Artists: []lunaSearchArtist{{Name: "Oasis"}}, Duration: 240000}}}
	groups := []lunaResultGroup{{Data: []lunaGroupDatum{row}}}

	if id, found := pickLunaTrack(groups, lyrics.TrackInfo{Title: "🎵", Artist: "Oasis"}); found {
		t.Fatalf("pickLunaTrack accepted %q for an unverifiable title", id)
	}
	if id, found := pickLunaTrack(groups, lyrics.TrackInfo{Title: "Live Forever", Artist: "???"}); found {
		t.Fatalf("pickLunaTrack accepted %q for an unverifiable artist", id)
	}
}

func TestLunaCandidates_WordsFromPlainKRC(t *testing.T) {
	t.Parallel()

	searchBody := `{"result_groups":[{"data":[{"entity":{"track":{"id":7425699317225293865,"name":"Live Forever (Live)","artists":[{"name":"Oasis"}],"duration":280000}}}]}]}`
	detailBody := `{"lyric":{"content":"[400,4900]<0,1040,0>Beneath <1040,280,0>an <1320,240,0>empty\n","type":"krc"}}`
	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "api.qishui.com/luna/search/track"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(searchBody)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(detailBody)}, nil
		}
	})

	candidates, _, err := fetchLuna(rt, lyrics.TrackInfo{
		Title: "Live Forever", Artist: "Oasis", Duration: 280,
	}, time.Now().Add(testLookupBudget))
	if err != nil {
		t.Fatalf("fetchLuna() error = %v, want nil", err)
	}
	if len(candidates) != 1 || candidates[0].Quality != lyric.TimingWord {
		t.Fatalf("candidates = %#v, want one word candidate", candidates)
	}
	assertLyricsSource(t, candidates[0].Response.Source, "Soda (Luna)", "elrc")
}

func TestLunaCandidates_LineLRCWithoutWordData(t *testing.T) {
	t.Parallel()

	searchBody := `{"result_groups":[{"data":[{"entity":{"track":{"id":1,"name":"Live Forever","artists":[{"name":"Oasis"}],"duration":280000}}}]}]}`
	detailBody := `{"lyric":{"content":"[00:04.00]Beneath an empty\n","type":"lrc"}}`
	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "api.qishui.com/luna/search/track"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(searchBody)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(detailBody)}, nil
		}
	})

	candidates, _, err := fetchLuna(rt, lyrics.TrackInfo{
		Title: "Live Forever", Artist: "Oasis", Duration: 280,
	}, time.Now().Add(testLookupBudget))
	if err != nil {
		t.Fatalf("fetchLuna() error = %v, want nil", err)
	}
	if len(candidates) != 1 || candidates[0].Quality != lyric.TimingLine {
		t.Fatalf("candidates = %#v, want one line candidate", candidates)
	}
}

func TestLunaCandidates_QuotedTrackIDs(t *testing.T) {
	t.Parallel()

	// Live Luna responses quote every track id; unmarshalling them into int64
	// used to abort the whole search decode before any candidate selection.
	var detailRequest string
	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, lunaSearchEndpoint):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"result_groups":[{"data":[{"entity":{"track":{"id":"7425699317225293865","name":"liveforeverOasis","artists":[{"name":"又山"}],"duration":130032}}}]}]}`)}, nil
		default:
			detailRequest = request.URL
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"lyric":{"content":"[00:04.00]Beneath an empty","type":"lrc"}}`)}, nil
		}
	})

	candidates, health, err := fetchLuna(rt, lyrics.TrackInfo{
		Title: "Live Forever", Artist: "Oasis", Duration: 130,
	}, time.Now().Add(testLookupBudget))
	if err != nil {
		t.Fatalf("fetchLuna() error = %v, want nil (quoted ids must not abort the decode)", err)
	}
	if health != Healthy {
		t.Fatalf("health = %v, want healthy", health)
	}
	if len(candidates) != 1 || candidates[0].Quality != lyric.TimingLine {
		t.Fatalf("candidates = %#v, want one line candidate sourced from the quoted id", candidates)
	}
	if !strings.Contains(detailRequest, "track_id=7425699317225293865") {
		t.Fatalf("detail request = %q, want the quoted id carried through", detailRequest)
	}
}

func TestLunaTrackID_PreservesLargeNumericIDs(t *testing.T) {
	t.Parallel()

	var numeric lunaTrackID
	if err := numeric.UnmarshalJSON([]byte(`7425699317225293865`)); err != nil {
		t.Fatalf("UnmarshalJSON(numeric) error = %v, want nil", err)
	}
	if numeric != "7425699317225293865" {
		t.Fatalf("numeric id = %q, want the exact digit string (no float64 rounding)", string(numeric))
	}

	var quoted lunaTrackID
	if err := quoted.UnmarshalJSON([]byte(`"7425699317225293865"`)); err != nil {
		t.Fatalf("UnmarshalJSON(quoted) error = %v, want nil", err)
	}
	if quoted != "7425699317225293865" {
		t.Fatalf("quoted id = %q, want the stripped value", string(quoted))
	}
}

func TestLunaMalformedKRCDegrades(t *testing.T) {
	t.Parallel()

	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, lunaSearchEndpoint) {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"result_groups":[{"data":[{"entity":{"track":{"id":"5","name":"Song","artists":[{"name":"Artist"}],"duration":240000}}}]}]}`)}, nil
		}
		return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"lyric":{"content":"(definitely not a KRC payload","type":"krc"}}`)}, nil
	})

	_, health, err := fetchLuna(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 240}, time.Now().Add(testLookupBudget))
	if err == nil || health != Degraded {
		t.Fatalf("result = (%v, %v); want the malformed KRC to degrade", err, health)
	}
}

func TestLunaCandidates_UnknownTypeReturnsPlainCandidate(t *testing.T) {
	t.Parallel()

	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "api.qishui.com/luna/search/track"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"result_groups":[{"data":[{"entity":{"track":{"id":1,"name":"Live Forever","artists":[{"name":"Oasis"}],"duration":280000}}}]}]}`)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"lyric":{"content":"[00:04.00]Beneath an empty","type":"brand-new-dialect"}}`)}, nil
		}
	})

	candidates, _, err := fetchLuna(rt, lyrics.TrackInfo{Title: "Live Forever", Artist: "Oasis", Duration: 280}, time.Now().Add(testLookupBudget))
	if err != nil {
		t.Fatalf("fetchLuna() error = %v, want nil", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want the usable payload returned as a candidate", candidates)
	}
	assertLyricsSource(t, candidates[0].Response.Source, "Soda (Luna)", "plain")
}
