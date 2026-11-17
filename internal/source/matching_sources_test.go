package source

import (
	"testing"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func TestBinimumPrefersValidatedRowOverEarlierAmbiguous(t *testing.T) {
	rows := []binimumResult{
		{TrackName: "Song (Live)", ArtistName: "Artist", AlbumName: "Live", Duration: 200, LyricsURL: "https://lrc.red/live.ttml"},
		{TrackName: "Song", ArtistName: "Artist", AlbumName: "Album", Duration: 200, LyricsURL: "https://lrc.red/studio.ttml"},
	}
	got, ok := pickBinimumResult(rows, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Album: "Album", Duration: 200})
	if !ok || got.TrackName != "Song" {
		t.Fatalf("selected %#v/%v, want exact studio row", got, ok)
	}
}

func TestNetEasePrefersValidatedRowOverEarlierAmbiguous(t *testing.T) {
	songs := []neteaseSong{
		{ID: 1, Name: "Song (Live)", Artists: []neteaseArtist{{Name: "Artist"}}, Duration: 200000},
		{ID: 2, Name: "Song", Artists: []neteaseArtist{{Name: "Artist"}}, Duration: 200000},
	}
	got, ok := pickNetEaseSong(songs, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 200})
	if !ok || got.Name != "Song" {
		t.Fatalf("selected %#v/%v, want exact studio row", got, ok)
	}
}

func TestLunaPrefersExactTitleOverEarlierContainment(t *testing.T) {
	groups := []lunaResultGroup{
		{Data: []lunaGroupDatum{{Entity: struct {
			Track lunaSearchTrack `json:"track"`
		}{Track: lunaSearchTrack{ID: "1", Name: "Song Live Oasis", Artists: []lunaSearchArtist{{Name: "Artist"}}, Duration: 200000}}}}},
		{Data: []lunaGroupDatum{{Entity: struct {
			Track lunaSearchTrack `json:"track"`
		}{Track: lunaSearchTrack{ID: "2", Name: "Song", Artists: []lunaSearchArtist{{Name: "Artist"}}, Duration: 200000}}}}},
	}
	got, ok := pickLunaTrackInfo(groups, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 200})
	if !ok || string(got.ID) != "2" {
		t.Fatalf("selected %#v/%v, want exact title row", got, ok)
	}
}

func TestBinimumValidatedRowOutsideTwoSecondsBeatsAmbiguous(t *testing.T) {
	rows := []binimumResult{
		{TrackName: "Song (Live)", ArtistName: "Artist", AlbumName: "Live", Duration: 200, LyricsURL: "https://lrc.red/live.ttml"},
		{TrackName: "Song", ArtistName: "Artist", AlbumName: "Album", Duration: 203, LyricsURL: "https://lrc.red/studio.ttml"},
	}
	got, ok := pickBinimumResult(rows, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Album: "Album", Duration: 200})
	if !ok || got.TrackName != "Song" {
		t.Fatalf("selected %#v/%v, want validated album row outside the tight band", got, ok)
	}
}
