// Command provider-probe checks public source contracts without storing lyrics.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/provider"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

type reply struct {
	Host   string   `json:"host"`
	Path   string   `json:"path"`
	Status int32    `json:"status"`
	Bytes  int      `json:"bytes"`
	Fields []string `json:"fields,omitempty"`
}

type result struct {
	Source     string   `json:"source"`
	CheckedAt  string   `json:"checkedAt"`
	ElapsedMS  int64    `json:"elapsedMs"`
	Health     string   `json:"health"`
	Candidates int      `json:"candidates"`
	Formats    []string `json:"formats,omitempty"`
	Failed     bool     `json:"failed"`
	Replies    []reply  `json:"replies"`
}

type entry struct {
	value   []byte
	expires time.Time
}
type store map[string]entry

func (s store) Get(key string) ([]byte, bool, error) {
	e, ok := s[key]
	return e.value, ok && time.Now().Before(e.expires), nil
}
func (s store) SetWithTTL(key string, value []byte, ttl int64) error {
	s[key] = entry{bytes.Clone(value), time.Now().Add(time.Duration(ttl) * time.Second)}
	return nil
}

func main() {
	includeDisabled := flag.Bool("include-disabled", false, "also contact opt-in sources")
	only := flag.String("source", "", "probe one catalog source ID")
	title := flag.String("title", "Shape of You", "public track title sent to sources")
	artist := flag.String("artist", "Ed Sheeran", "public artist sent to sources")
	album := flag.String("album", "÷ (Deluxe)", "public album sent to sources")
	duration := flag.Float64("duration", 234, "track duration in seconds")
	flag.Parse()
	if *only != "" {
		selected, ok := source.ByID(*only)
		if !ok {
			fmt.Fprintln(os.Stderr, "unknown source ID")
			os.Exit(2)
		}
		if !selected.DefaultEnabled && !*includeDisabled {
			fmt.Fprintln(os.Stderr, "opt-in sources require -include-disabled")
			os.Exit(2)
		}
	}
	allowed := make(map[string]bool)
	for _, src := range source.Catalog {
		for _, hostname := range src.Hosts {
			allowed[hostname] = true
		}
	}
	validate := func(u *url.URL) error {
		if u.Scheme != "https" || !allowed[u.Hostname()] {
			return fmt.Errorf("probe target is outside the HTTPS catalog allowlist")
		}
		return nil
	}
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("redirect limit exceeded")
		}
		return validate(req.URL)
	}}
	cache := make(store)
	encoder := json.NewEncoder(os.Stdout)
	for _, src := range source.Catalog {
		if *only != "" && src.ID != *only {
			continue
		}
		if !src.DefaultEnabled && !*includeDisabled {
			continue
		}
		out := result{Source: src.ID, CheckedAt: time.Now().UTC().Format(time.RFC3339), Replies: []reply{}}
		runtime := provider.New(provider.Deps{Store: cache, UserAgent: "NavidromeBetterLyricsPlugin/contract-probe (+https://github.com/ranokay/navidrome-better-lyrics-plugin)",
			Send: func(input host.HTTPRequest) (*host.HTTPResponse, error) {
				ctx, cancel := context.WithTimeout(context.Background(), time.Duration(input.TimeoutMs)*time.Millisecond)
				defer cancel()
				req, err := http.NewRequestWithContext(ctx, input.Method, input.URL, bytes.NewReader(input.Body))
				if err != nil {
					return nil, err
				}
				if err := validate(req.URL); err != nil {
					return nil, err
				}
				for key, value := range input.Headers {
					req.Header.Set(key, value)
				}
				response, err := client.Do(req)
				if err != nil {
					return nil, err
				}
				defer response.Body.Close()
				// Match the host ceiling; the provider imposes its smaller decode cap.
				body, err := io.ReadAll(io.LimitReader(response.Body, 10*1024*1024+1))
				if err != nil {
					return nil, err
				}
				wire := reply{Host: response.Request.URL.Hostname(), Path: response.Request.URL.Path, Status: int32(response.StatusCode), Bytes: len(body)}
				var shape map[string]json.RawMessage
				if json.Unmarshal(body, &shape) == nil {
					// Emit only recognized contract field names, never arbitrary
					// upstream strings that might themselves contain lyric text.
					for _, key := range []string{"albumName", "artistName", "code", "content", "contents", "data", "duration", "error", "hasWordSync", "id", "instrumental", "lyrics", "lyricsfile", "name", "plainLyrics", "result", "results", "song", "songs", "status", "success", "syncedLyrics", "trackName", "ttml", "type", "yrc"} {
						if _, ok := shape[key]; ok {
							wire.Fields = append(wire.Fields, key)
						}
					}
					slices.Sort(wire.Fields)
				}
				out.Replies = append(out.Replies, wire)
				headers := make(map[string]string)
				for key, values := range response.Header {
					headers[key] = strings.Join(values, ", ")
				}
				return &host.HTTPResponse{StatusCode: int32(response.StatusCode), Headers: headers, Body: body}, nil
			}})
		started := time.Now()
		candidates, health, err := src.Fetch(runtime, lyrics.TrackInfo{Title: *title, Artist: *artist, Album: *album, Duration: float32(*duration)}, started.Add(12*time.Second))
		out.ElapsedMS = time.Since(started).Milliseconds()
		out.Health = healthName(health)
		out.Candidates = len(candidates)
		out.Failed = err != nil
		for _, candidate := range candidates {
			if candidate.Response.Source != nil {
				out.Formats = append(out.Formats, candidate.Response.Source.Format)
			}
		}
		// Never emit request queries, bodies, headers, lyric text or raw errors.
		if err := encoder.Encode(out); err != nil {
			fmt.Fprintln(os.Stderr, "cannot write probe results")
			os.Exit(1)
		}
	}
}

func healthName(health source.Health) string {
	switch health {
	case source.Healthy:
		return "healthy"
	case source.Uncertain:
		return "uncertain"
	case source.Degraded:
		return "degraded"
	case source.TemporarilySkipped:
		return "temporarily-skipped"
	default:
		return "unknown"
	}
}
