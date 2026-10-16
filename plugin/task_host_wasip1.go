//go:build wasip1

package main

import (
	"encoding/json"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
)

func createSweepQueue() error {
	return host.TaskCreateQueue("lyrics-sweep", host.QueueConfig{
		Concurrency: 1,
		MaxRetries:  0,
		BackoffMs:   0,
		DelayMs:     0,
	})
}

func defaultSweepEnqueuer() func(lyrics.TrackInfo) error {
	return func(track lyrics.TrackInfo) error {
		payload, err := json.Marshal(sweepTaskPayload{Track: track})
		if err != nil {
			return err
		}
		_, err = host.TaskEnqueue("lyrics-sweep", payload)
		return err
	}
}
