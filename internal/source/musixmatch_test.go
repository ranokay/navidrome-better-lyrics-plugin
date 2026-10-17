package source

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
)

func TestMusixmatchCandidates_WordRichsync(t *testing.T) {
	t.Parallel()

	token := "test-user-token"
	richsync := `[{"ts":12.4,"te":13.9,"l":[{"c":"He","o":0},{"c":"llo","o":120}]}]`
	searchBody := `{"message":{"header":{"status_code":200},"body":{"track_list":[{"track":{"track_id":360230953,"track_name":"Shape of You","artist_name":"Ed Sheeran","track_length":234}}]}}}`
	subtitlesBody := `{"message":{"body":{"macro_calls":{"track.richsync.get":{"header":{"status_code":200},"body":{"richsync_body":` + strconv.Quote(richsync) + `}}}}}}`

	minted := 0
	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
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

	candidates, health, err := fetchMusixmatch(rt, lyrics.TrackInfo{
		Title: "Shape of You", Artist: "Ed Sheeran", Duration: 234,
	}, time.Now().Add(testLookupBudget))
	if err != nil {
		t.Fatalf("fetchMusixmatch() error = %v, want nil", err)
	}
	if health != Healthy {
		t.Fatalf("health = %v, want healthy", health)
	}
	if len(candidates) != 1 || candidates[0].Quality != lyric.TimingWord {
		t.Fatalf("candidates = %#v, want one word candidate", candidates)
	}
	assertLyricsSource(t, candidates[0].Response.Source, "Musixmatch", "elrc")
	if minted != 1 {
		t.Fatalf("token mints = %d, want 1", minted)
	}
}

func TestMusixmatchCandidates_ConfiguredTokenSkipsMinting(t *testing.T) {
	t.Parallel()

	richsync := `[{"ts":1.0,"te":2.0,"l":[{"c":"hey","o":0}]}]`
	rt := newFakeRuntimeWithCache(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "/ws/1.1/token.get"):
			t.Fatal("configured token must bypass token.get")
			return nil, nil
		case strings.Contains(request.URL, "/ws/1.1/track.search"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"header":{"status_code":200},"body":{"track_list":[{"track":{"track_id":7,"track_name":"Song","artist_name":"Artist","track_length":10}}]}}}`)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"body":{"macro_calls":{"track.richsync.get":{"header":{"status_code":200},"body":{"richsync_body":` + strconv.Quote(richsync) + `}}}}}}`)}, nil
		}
	}, newFakeStore(), time.Now)
	rt.musixmatchUserToken = "pasted-token"

	candidates, _, err := fetchMusixmatch(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 10}, time.Now().Add(testLookupBudget))
	if err != nil {
		t.Fatalf("fetchMusixmatch() error = %v, want nil", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want one", candidates)
	}
}

func TestMusixmatchCandidates_CaptchaDropsTokenWithoutError(t *testing.T) {
	t.Parallel()

	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "/ws/1.1/token.get") {
			return &host.HTTPResponse{StatusCode: 401, Body: []byte(`{"message":{"header":{"status_code":401,"hint":"captcha"}}}`)}, nil
		}
		t.Fatalf("search should be skipped after a challenge: %s", request.URL)
		return nil, nil
	})

	candidates, _, err := fetchMusixmatch(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 10}, time.Now().Add(testLookupBudget))
	if err != nil || len(candidates) != 0 {
		t.Fatalf("result = %#v/%v, want clean miss on challenge", candidates, err)
	}
}

func TestMusixmatchCandidates_LineSubtitleFallback(t *testing.T) {
	t.Parallel()

	searchBody := `{"message":{"header":{"status_code":200},"body":{"track_list":[{"track":{"track_id":42,"track_name":"Song","artist_name":"Artist","track_length":10}}]}}}`
	subtitlesBody := `{"message":{"body":{"macro_calls":{"track.subtitles.get":{"header":{"status_code":200},"body":{"subtitle_list":[{"subtitle_body":"[00:01.00]synced subtitle"}]}}}}}}`

	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "/ws/1.1/token.get"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"header":{"status_code":200},"body":{"user_token":"tok"}}}`)}, nil
		case strings.Contains(request.URL, "/ws/1.1/track.search"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(searchBody)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(subtitlesBody)}, nil
		}
	})

	candidates, _, err := fetchMusixmatch(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 10}, time.Now().Add(testLookupBudget))
	if err != nil {
		t.Fatalf("fetchMusixmatch() error = %v, want nil", err)
	}
	if len(candidates) != 1 || candidates[0].Quality != lyric.TimingLine {
		t.Fatalf("candidates = %#v, want line candidate", candidates)
	}
	assertLyricsSource(t, candidates[0].Response.Source, "Musixmatch", "lrc")
}

func TestMusixmatchCandidates_TrackLengthDuration(t *testing.T) {
	t.Parallel()

	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "/ws/1.1/token.get"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"header":{"status_code":200},"body":{"user_token":"tok"}}}`)}, nil
		case strings.Contains(request.URL, "/ws/1.1/track.search"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"header":{"status_code":200},"body":{"track_list":[{"track":{"track_id":42,"track_name":"Song","artist_name":"Artist","track_length":10}}]}}}`)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"body":{"macro_calls":{"track.richsync.get":{"header":{"status_code":404}},"track.subtitles.get":{"header":{"status_code":200},"body":{"subtitle_list":[{"subtitle_body":"[00:01.00]line"}]}}}}}}`)}, nil
		}
	})

	candidates, _, err := fetchMusixmatch(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 10}, time.Now().Add(testLookupBudget))
	if err != nil {
		t.Fatalf("fetchMusixmatch() error = %v, want nil", err)
	}
	if len(candidates) != 1 || candidates[0].Quality != lyric.TimingLine {
		t.Fatalf("candidates = %#v, want the line candidate matched via track_length", candidates)
	}
	assertLyricsSource(t, candidates[0].Response.Source, "Musixmatch", "lrc")
}

func TestMusixmatchMintFailuresStayDegraded(t *testing.T) {
	t.Parallel()

	t.Run("transport failure propagates", func(t *testing.T) {
		rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
			return nil, errors.New("segmentation unreachable in the token.get")
		})
		_, health, err := fetchMusixmatch(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(testLookupBudget))
		if err == nil {
			t.Fatal("transport failure converted to a healthy clean miss; want a degraded error")
		}
		if health == Healthy {
			t.Fatalf("health = %v, want degraded on an operational failure", health)
		}
	})

	t.Run("http error propagates", func(t *testing.T) {
		rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
			return &host.HTTPResponse{StatusCode: 503, Body: []byte("kaput")}, nil
		})
		_, health, err := fetchMusixmatch(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(testLookupBudget))
		if err == nil {
			t.Fatal("5xx mint converted to a healthy clean miss; want a degraded error")
		}
		if health == Healthy {
			t.Fatalf("health = %v, want degraded on an operational failure", health)
		}
	})

	t.Run("token challenge stays quiet", func(t *testing.T) {
		rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
			return &host.HTTPResponse{StatusCode: 401, Body: []byte("denied")}, nil
		})
		_, health, err := fetchMusixmatch(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(testLookupBudget))
		if err != nil || health != TemporarilySkipped {
			t.Fatalf("challenge = (%v, %v); want a temporary skip, not a covered miss", err, health)
		}
	})
}

func TestMusixmatchEnvelopeStatusesClassified(t *testing.T) {
	t.Parallel()

	facade := func(searchEnvelope, macroEnvelope string) func(host.HTTPRequest) (*host.HTTPResponse, error) {
		return func(request host.HTTPRequest) (*host.HTTPResponse, error) {
			switch {
			case strings.Contains(request.URL, "/ws/1.1/token.get"):
				return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"header":{"status_code":200},"body":{"user_token":"tok"}}}`)}, nil
			case strings.Contains(request.URL, "/ws/1.1/track.search"):
				return &host.HTTPResponse{StatusCode: 200, Body: []byte(searchEnvelope)}, nil
			default:
				return &host.HTTPResponse{StatusCode: 200, Body: []byte(macroEnvelope)}, nil
			}
		}
	}
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 10}

	t.Run("server envelope failures degrade instead of missing", func(t *testing.T) {
		rt := newFakeRuntime(facade(`{"message":{"header":{"status_code":500}}}`, `{}`))
		_, health, err := fetchMusixmatch(rt, track, time.Now().Add(testLookupBudget))
		if err == nil || health == Healthy {
			t.Fatalf("result = (%v, %v); want degraded on envelope 500", err, health)
		}
	})

	t.Run("envelope 429 activates the provider cooldown", func(t *testing.T) {
		cache := newFakeStore()
		rt := newFakeRuntimeWithCache(facade(`{"message":{"header":{"status_code":429}}}`, `{}`), cache, time.Now)
		_, health, err := fetchMusixmatch(rt, track, time.Now().Add(testLookupBudget))
		if err == nil || health == Healthy {
			t.Fatalf("result = (%v, %v); want degraded on envelope 429", err, health)
		}
		if rt.Cooldown(GlobalCooldownKey("musixmatch")) <= 0 {
			t.Fatal("envelope 429 activated no cooldown")
		}
	})

	t.Run("envelope 404 and no-match stay quiet misses", func(t *testing.T) {
		rt := newFakeRuntime(facade(`{"message":{"header":{"status_code":404}}}`, `{}`))
		candidates, health, err := fetchMusixmatch(rt, track, time.Now().Add(testLookupBudget))
		if err != nil || len(candidates) != 0 || health != Healthy {
			t.Fatalf("result = %#v/%v/%v; want clean miss on envelope 404", candidates, health, err)
		}
	})

	t.Run("rate-limited macro payloads degrade the lookup", func(t *testing.T) {
		rt := newFakeRuntime(facade(
			`{"message":{"header":{"status_code":200},"body":{"track_list":[{"track":{"track_id":42,"track_name":"Song","artist_name":"Artist","track_length":10}}]}}}`,
			`{"message":{"header":{"status_code":200},"body":{"macro_calls":{"track.richsync.get":{"header":{"status_code":429}}}}}}`))
		_, health, err := fetchMusixmatch(rt, track, time.Now().Add(testLookupBudget))
		if err == nil || health == Healthy {
			t.Fatalf("result = (%v, %v); want a degraded lookup when a rate-limited macro spoils the pass", err, health)
		}
	})
}

func TestMusixmatchMintEnvelopeStatusesClassified(t *testing.T) {
	t.Parallel()

	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist"}

	t.Run("mint envelope 429 keeps no cooldown creativity", func(t *testing.T) {
		cache := newFakeStore()
		rt := newFakeRuntimeWithCache(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"header":{"status_code":429}}}`)}, nil
		}, cache, time.Now)
		_, _, err := fetchMusixmatch(rt, track, time.Now().Add(testLookupBudget))
		if err == nil {
			t.Fatal("token.get envelope 429 converted to a miss; want an error")
		}
		if rt.Cooldown(GlobalCooldownKey("musixmatch")) <= 0 {
			t.Fatal("token.get envelope 429 activated no cooldown")
		}
	})

	t.Run("mint envelope challenge stays quiet", func(t *testing.T) {
		rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"header":{"status_code":401,"hint":"captcha"}}}`)}, nil
		})
		_, health, err := fetchMusixmatch(rt, track, time.Now().Add(testLookupBudget))
		if err != nil || health != TemporarilySkipped {
			t.Fatalf("result = (%v, %v); want the challenge kept as a temporary skip", err, health)
		}
	})
}

func TestMusixmatchTokenRejectionsStaySkippable(t *testing.T) {
	t.Parallel()

	// Expired-token rejections must not persist as clean misses: the next
	// lookup has to remint and retry, and the sweep must not mark the source
	// covered.
	facade := func(t *testing.T, searchBody, macroBody string) func(host.HTTPRequest) (*host.HTTPResponse, error) {
		return func(request host.HTTPRequest) (*host.HTTPResponse, error) {
			switch {
			case strings.Contains(request.URL, "/ws/1.1/token.get"):
				return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"header":{"status_code":200},"body":{"user_token":"tok"}}}`)}, nil
			case strings.Contains(request.URL, "/ws/1.1/track.search"):
				return &host.HTTPResponse{StatusCode: 200, Body: []byte(searchBody)}, nil
			default:
				return &host.HTTPResponse{StatusCode: 200, Body: []byte(macroBody)}, nil
			}
		}
	}
	track := lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 10}

	t.Run("rejected search token stays skippable", func(t *testing.T) {
		cache := newFakeStore()
		rt := newFakeRuntimeWithCache(facade(t, `{"message":{"header":{"status_code":401}}}`, `{}`), cache, time.Now)
		_, health, err := fetchMusixmatch(rt, track, time.Now().Add(testLookupBudget))
		if err != nil || health == Healthy {
			t.Fatalf("result = (%v, %v); want a temporary skip health", err, health)
		}
		if _, exists, _ := cache.Get(musixmatchTokenKey); !exists {
			t.Fatal("a rejected token was not dropped for the next remint")
		}
	})

	t.Run("rejected macro token stays skippable", func(t *testing.T) {
		search := `{"message":{"header":{"status_code":200},"body":{"track_list":[{"track":{"track_id":42,"track_name":"Song","artist_name":"Artist","track_length":10}}]}}}`
		cache := newFakeStore()
		rt := newFakeRuntimeWithCache(facade(t, search, `{"message":{"header":{"status_code":200},"body":{"macro_calls":{"track.richsync.get":{"header":{"status_code":401}}}}}}`), cache, time.Now)
		_, health, err := fetchMusixmatch(rt, track, time.Now().Add(testLookupBudget))
		if err != nil || health == Healthy {
			t.Fatalf("result = (%v, %v); want a temporary skip health", err, health)
		}
		if _, exists, _ := cache.Get(musixmatchTokenKey); !exists {
			t.Fatal("a rejected macro token was not dropped for the next remint")
		}
	})
}

func TestMusixmatchToken429RecordsCooldown(t *testing.T) {
	t.Parallel()

	cache := newFakeStore()
	rt := newFakeRuntimeWithCache(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		return &host.HTTPResponse{StatusCode: 429, Headers: map[string]string{"Retry-After": "7"}}, nil
	}, cache, time.Now)

	_, _, err := fetchMusixmatch(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(testLookupBudget))
	if err == nil {
		t.Fatal("token.get 429 converted to a miss; want an error")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Fatalf("error = %v, want the rate-limit error surfaced", err)
	}
	if cooldown := rt.Cooldown(GlobalCooldownKey("musixmatch")); cooldown < 7*time.Second || cooldown > 8*time.Second {
		t.Fatalf("cooldown = %v, want the Retry-After value honored", cooldown)
	}
}

func TestMusixmatchSkipsEmptySubtitleBodies(t *testing.T) {
	t.Parallel()

	// Restricted lyrics arrive as an empty first subtitle; the plain-lyrics
	// fallback must serve instead of a blank line candidate.
	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "/ws/1.1/token.get"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"header":{"status_code":200},"body":{"user_token":"tok"}}}`)}, nil
		case strings.Contains(request.URL, "/ws/1.1/track.search"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"header":{"status_code":200},"body":{"track_list":[{"track":{"track_id":42,"track_name":"Song","artist_name":"Artist","track_length":10}}]}}}`)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"body":{"macro_calls":{"track.subtitles.get":{"header":{"status_code":200},"body":{"subtitle_list":[{"subtitle_body":""}]}},"track.lyrics.get":{"header":{"status_code":200},"body":{"lyrics_body":"[00:01.00]plain words"}}}}}}`)}, nil
		}
	})

	candidates, _, err := fetchMusixmatch(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 10}, time.Now().Add(testLookupBudget))
	if err != nil {
		t.Fatalf("fetchMusixmatch() error = %v, want nil", err)
	}
	if len(candidates) != 1 || candidates[0].Quality != lyric.TimingUnsynced {
		t.Fatalf("candidates = %#v, want the plain fallback rather than the empty subtitle", candidates)
	}
	if !strings.Contains(candidates[0].Response.Lyrics[0].Text, "plain words") {
		t.Fatalf("text = %q, want the plain fallback served", candidates[0].Response.Lyrics[0].Text)
	}
}

func TestMusixmatchConfiguredTokenRejectionGetsCooldown(t *testing.T) {
	t.Parallel()

	// A rejected configured token is the only credential available; retries
	// must be bounded instead of hammering it every lookup.
	cache := newFakeStore()
	rt := newFakeRuntimeWithCache(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "/ws/1.1/track.search"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"header":{"status_code":401,"hint":"captcha"}}}`)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{}`)}, nil
		}
	}, cache, time.Now)
	rt.musixmatchUserToken = "configured-token"

	_, health, err := fetchMusixmatch(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, time.Now().Add(testLookupBudget))
	if err != nil || health == Healthy {
		t.Fatalf("result = (%v, %v); want a temporary skip", err, health)
	}
	if rt.Cooldown(GlobalCooldownKey("musixmatch")) <= 0 {
		t.Fatal("no cooldown recorded for the rejected configured token")
	}
}

func TestMusixmatchKeepsFallbackWhenMacroFails(t *testing.T) {
	t.Parallel()

	// richsync 500 must not discard the usable subtitles macro.
	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "/ws/1.1/token.get"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"header":{"status_code":200},"body":{"user_token":"tok"}}}`)}, nil
		case strings.Contains(request.URL, "/ws/1.1/track.search"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"header":{"status_code":200},"body":{"track_list":[{"track":{"track_id":42,"track_name":"Song","artist_name":"Artist","track_length":10}}]}}}`)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"body":{"macro_calls":{"track.richsync.get":{"header":{"status_code":500}},"track.subtitles.get":{"header":{"status_code":200},"body":{"subtitle_list":[{"subtitle_body":"[00:01.00]usable line"}]}}}}}}`)}, nil
		}
	})

	candidates, health, err := fetchMusixmatch(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 10}, time.Now().Add(testLookupBudget))
	if err != nil || health != Healthy || len(candidates) != 1 {
		t.Fatalf("result = %#v/%v/%v, want the subtitles fallback served despite the richsync failure", candidates, health, err)
	}
	if !strings.Contains(candidates[0].Response.Lyrics[0].Text, "usable line") {
		t.Fatalf("text = %q, want the subtitles payload", candidates[0].Response.Lyrics[0].Text)
	}
}

func TestMusixmatchMalformedRichsyncStaysDegraded(t *testing.T) {
	t.Parallel()

	rt := newFakeRuntime(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "/ws/1.1/token.get"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"header":{"status_code":200},"body":{"user_token":"tok"}}}`)}, nil
		case strings.Contains(request.URL, "/ws/1.1/track.search"):
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"header":{"status_code":200},"body":{"track_list":[{"track":{"track_id":42,"track_name":"Song","artist_name":"Artist","track_length":10}}]}}}`)}, nil
		default:
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"body":{"macro_calls":{"track.richsync.get":{"header":{"status_code":200},"body":{"richsync_body":"{not json"}}}}}}`)}, nil
		}
	})

	_, health, err := fetchMusixmatch(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 10}, time.Now().Add(testLookupBudget))
	if err == nil || health != Degraded {
		t.Fatalf("result = (%v, %v); want malformed richsync to degrade without fallbacks", err, health)
	}
}

func TestMusixmatchCandidates_MintStageStaysInsideCallerDeadline(t *testing.T) {
	t.Parallel()

	now := time.Unix(3000, 0)
	var mintTimeout int32
	rt := newFakeRuntimeWithCache(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "/ws/1.1/token.get") {
			mintTimeout = request.TimeoutMs
			return &host.HTTPResponse{StatusCode: 401, Body: []byte(`{"message":{"header":{"status_code":401,"hint":"captcha"}}}`)}, nil
		}
		return &host.HTTPResponse{StatusCode: 404}, nil
	}, newFakeStore(), func() time.Time { return now })

	deadline := now.Add(3 * time.Second)
	if _, _, err := fetchMusixmatch(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist"}, deadline); err != nil {
		t.Fatalf("fetchMusixmatch() error = %v, want nil", err)
	}
	if mintTimeout == 0 || mintTimeout > 3000 {
		t.Fatalf("mint request timeout = %d, want <= caller deadline (3000, the mint must honor the lookup budget)", mintTimeout)
	}
}

func TestMusixmatchCandidates_EnvelopeStatusDropsStaleToken(t *testing.T) {
	t.Parallel()

	rejectedSearchBody := `{"message":{"header":{"status_code":401,"hint":"captcha"}}}`
	facade := func(capturedSearchBody, macroBody string) func(host.HTTPRequest) (*host.HTTPResponse, error) {
		return func(request host.HTTPRequest) (*host.HTTPResponse, error) {
			if strings.Contains(request.URL, "/ws/1.1/token.get") {
				return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"header":{"status_code":200},"body":{"user_token":"stale-token"}}}`)}, nil
			}
			if strings.Contains(request.URL, "/ws/1.1/track.search") {
				return &host.HTTPResponse{StatusCode: 200, Body: []byte(capturedSearchBody)}, nil
			}
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(macroBody)}, nil
		}
	}

	cache := newFakeStore()
	rt := newFakeRuntimeWithCache(facade(rejectedSearchBody, rejectedSearchBody), cache, time.Now)
	candidates, _, err := fetchMusixmatch(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 10}, time.Now().Add(testLookupBudget))
	if err != nil || len(candidates) != 0 {
		t.Fatalf("result = %#v/%v; want clean miss when the search envelope rejects the token", candidates, err)
	}
	if raw, _, _ := cache.Get(musixmatchTokenKey); len(raw) != 0 {
		t.Fatalf("stale token remains cached: %q, want the entry dropped", raw)
	}

	macroBody := `{"message":{"header":{"status_code":200},"body":{"macro_calls":{"track.richsync.get":{"header":{"status_code":401}}}}}}`
	validSearch := `{"message":{"header":{"status_code":200},"body":{"track_list":[{"track":{"track_id":42,"track_name":"Song","artist_name":"Artist","track_length":10}}]}}}`
	cache2 := newFakeStore()
	provider2 := newFakeRuntimeWithCache(facade(validSearch, macroBody), cache2, time.Now)
	candidates, _, err = fetchMusixmatch(provider2, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 10}, time.Now().Add(testLookupBudget))
	if err != nil || len(candidates) != 0 {
		t.Fatalf("macro result = %#v/%v; want clean miss when the macro envelope rejects the token", candidates, err)
	}
	if raw, _, _ := cache2.Get(musixmatchTokenKey); len(raw) != 0 {
		t.Fatalf("stale token remains cached: %q, want the entry dropped", raw)
	}
}

func TestMusixmatchCandidates_Search429RecordsCooldown(t *testing.T) {
	t.Parallel()

	rt := newFakeRuntimeWithCache(func(request host.HTTPRequest) (*host.HTTPResponse, error) {
		if strings.Contains(request.URL, "/ws/1.1/token.get") {
			return &host.HTTPResponse{StatusCode: 200, Body: []byte(`{"message":{"header":{"status_code":200},"body":{"user_token":"tok"}}}`)}, nil
		}
		return &host.HTTPResponse{
			StatusCode: 429,
			Headers:    map[string]string{"Retry-After": "60"},
			Body:       []byte(`{"message":{"header":{"status_code":429}}}`),
		}, nil
	}, newFakeStore(), time.Now)

	_, _, err := fetchMusixmatch(rt, lyrics.TrackInfo{Title: "Song", Artist: "Artist", Duration: 10}, time.Now().Add(testLookupBudget))
	if err == nil || !strings.Contains(err.Error(), "HTTP 429") {
		t.Fatalf("error = %v, want HTTP 429 (Review: a search-path 429 must surface and cool down)", err)
	}
	if remaining := rt.Cooldown(GlobalCooldownKey("musixmatch")); remaining <= 0 {
		t.Fatalf("musixmatch cooldown = %v, want active so the sweep skips hammering", remaining)
	}
}
