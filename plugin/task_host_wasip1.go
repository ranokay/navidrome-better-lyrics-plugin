//go:build wasip1

package main

import (
	"encoding/json"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func defaultSweepEnqueuer() func(lyrics.TrackInfo) error {
	return func(track lyrics.TrackInfo) error {
		payload, err := json.Marshal(track)
		if err != nil {
			return err
		}
		_, err = host.TaskEnqueue("lyrics-sweep", payload)
		return err
	}
}
