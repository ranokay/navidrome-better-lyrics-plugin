//go:build !wasip1

package main

import (
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

// defaultSweepEnqueuer is a no-op in native test builds; tests that exercise the
// enqueue path inject their own recorder.
func defaultSweepEnqueuer() func(lyrics.TrackInfo) error {
	return func(lyrics.TrackInfo) error { return nil }
}
