//go:build wasip1

package main

import (
	pdk "github.com/extism/go-pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/provider"
)

type hostStore struct{}

func (hostStore) Get(key string) ([]byte, bool, error) {
	return host.KVStoreGet(key)
}

func (hostStore) SetWithTTL(key string, value []byte, ttlSeconds int64) error {
	return host.KVStoreSetWithTTL(key, value, ttlSeconds)
}

func (hostStore) StorageUsed() (int64, error) {
	return host.KVStoreGetStorageUsed()
}

func defaultStore() provider.Store {
	return hostStore{}
}

func defaultConfigLookup() provider.ConfigLookup {
	return host.ConfigGet
}

func createSweepQueue(name string, concurrency int32) error {
	return host.TaskCreateQueue(name, host.QueueConfig{
		Concurrency: concurrency,
		MaxRetries:  0,
		BackoffMs:   0,
		DelayMs:     0,
	})
}

func enqueueTask(queue string, payload []byte) error {
	_, err := host.TaskEnqueue(queue, payload)
	return err
}

func logDebug(message string) {
	pdk.Log(pdk.LogDebug, message)
}
