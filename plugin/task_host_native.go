//go:build !wasip1

package main

import (
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

// createSweepQueue is inert in native test builds (the PDK's native host stubs route
// through an unset testify mock). OnInit fails open regardless, so a build-tagged
// no-op keeps unit tests deterministic.
func createSweepQueue() error { return nil }

// defaultSweepEnqueuer is a no-op in native test builds; tests that exercise the
// enqueue path inject their own recorder.
func defaultSweepEnqueuer() func(lyrics.TrackInfo) error {
	return func(lyrics.TrackInfo) error { return nil }
}
