package main

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func TestMusixmatchCandidates_WordRichsync(t *testing.T) {
	t.Parallel()

	token := "test-user-token"
	richsync := `[{"ts":12.4,"te":13.9,"l":[{"c":"He","o":0},{"c":"llo","o":120}]}]`
	searchBody := `{"message":{"header":{"status_code":200},"body":{"track_list":[{"track":{"track_id":360230953,"track_length":234}}]}}}`
	subtitlesBody := `{"message":{"body":{"macro_calls":{"track.richsync.get":{"header":{"status_code":200},"body":{"richsync_body":` + strconv.Quote(richsync) + `}}}}}}`

	minted := 0
	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "/ws/1.1/token.get"):
			minted++
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"header":{"status_code":200},"body":{"user_token":"` + token + `"}}}`)}, nil
		case strings.Contains(request.URL, "/ws/1.1/track.search"):
			if !strings.Contains(request.URL, "usertoken="+token) {
				t.Fatalf("search request lacks the minted token: %s", request.URL)
			}
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(searchBody)}, nil
		case strings.Contains(request.URL, "/ws/1.1/macro.subtitles.get"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(subtitlesBody)}, nil
		default:
			t.Fatalf("unexpected request URL: %s", request.URL)
			return nil, nil
		}
	})

	candidates, health, err := fetchMusixmatchLyricsCandidates(provider, lyrics.TrackInfo{
		Title: "Song", Artist: "Artist", Duration: 234,
	}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchMusixmatchLyricsCandidates() error = %v, want nil", err)
	}
	if health != lookupHealthy {
		t.Fatalf("health = %v, want healthy", health)
	}
	if len(candidates) != 1 || candidates[0].quality != timingWord {
		t.Fatalf("candidates = %#v, want one word candidate", candidates)
	}
	assertLyricsSource(t, candidates[0].response.Source, "Musixmatch", "elrc")
	if minted != 1 {
		t.Fatalf("token mints = %d, want 1", minted)
	}
}

func TestMusixmatchCandidates_ConfiguredTokenSkipsMinting(t *testing.T) {
	t.Parallel()

	richsync := `[{"ts":1.0,"te":2.0,"l":[{"c":"hey","o":0}]}]`
	provider := newLyricsProviderWithDeps(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "/ws/1.1/token.get"):
			t.Fatal("configured token must bypass token.get")
			return nil, nil
		case strings.Contains(request.URL, "/ws/1.1/track.search"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"header":{"status_code":200},"body":{"track_list":[{"track":{"track_id":7,"track_length":10}}]}}}`)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"body":{"macro_calls":{"track.richsync.get":{"header":{"status_code":200},"body":{"richsync_body":` + strconv.Quote(richsync) + `}}}}}}`)}, nil
		}
	}, newFakeLyricsCache(), time.Now, func(key string) (string, bool) {
		if key == "musixmatchUserToken" {
			return "pasted-token", true
		}
		return "", false
	}, nil)

	candidates, _, err := fetchMusixmatchLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 10}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchMusixmatchLyricsCandidates() error = %v, want nil", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want one", candidates)
	}
}

func TestMusixmatchCandidates_CaptchaDropsTokenWithoutError(t *testing.T) {
	t.Parallel()

	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "/ws/1.1/token.get") {
			return &host.HTTPResponse{StatusCode: 401, Body: []byte(`{"message":{"header":{"status_code":401,"hint":"captcha"}}}`)}, nil
		}
		t.Fatalf("search should be skipped after a challenge: %s", request.URL)
		return nil, nil
	})

	candidates, _, err := fetchMusixmatchLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 10}, time.Now().Add(stageOneLookupBudget))
	if err != nil || len(candidates) != 0 {
		t.Fatalf("result = %#v/%v, want clean miss on challenge", candidates, err)
	}
}

func TestMusixmatchCandidates_LineSubtitleFallback(t *testing.T) {
	t.Parallel()

	searchBody := `{"message":{"header":{"status_code":200},"body":{"track_list":[{"track":{"track_id":42,"track_length":10}}]}}}`
	subtitlesBody := `{"message":{"body":{"macro_calls":{"track.subtitles.get":{"header":{"status_code":200},"body":{"subtitle_list":[{"subtitle_body":"[00:01.00]synced subtitle"}]}}}}}}`

	provider := newLyricsProvider(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "/ws/1.1/token.get"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"header":{"status_code":200},"body":{"user_token":"tok"}}}`)}, nil
		case strings.Contains(request.URL, "/ws/1.1/track.search"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(searchBody)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(subtitlesBody)}, nil
		}
	})

	candidates, _, err := fetchMusixmatchLyricsCandidates(provider, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 10}, time.Now().Add(stageOneLookupBudget))
	if err != nil {
		t.Fatalf("fetchMusixmatchLyricsCandidates() error = %v, want nil", err)
	}
	if len(candidates) != 1 || candidates[0].quality != timingLine {
		t.Fatalf("candidates = %#v, want line candidate", candidates)
	}
	assertLyricsSource(t, candidates[0].response.Source, "Musixmatch", "lrc")
}
