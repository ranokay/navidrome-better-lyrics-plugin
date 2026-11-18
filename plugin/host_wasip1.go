//go:build wasip1

package main

import (
	"encoding/json"

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

func (hostStore) DeleteByPrefix(prefix string) (int64, error) {
	return host.KVStoreDeleteByPrefix(prefix)
}

const publicationKeyPrefix = "@navidrome/publication/"

func (hostStore) Admit(trackKey string, request []byte) ([]byte, error) {
	key := publicationKeyPrefix + "admission:" + trackKey
	if err := host.KVStoreSetWithTTL(key, request, 1800); err != nil {
		return nil, err
	}
	response, exists, err := host.KVStoreGet(key)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	return response, nil
}

func (hostStore) Commit(trackKey string, request []byte) error {
	return host.KVStoreSetWithTTL(publicationKeyPrefix+"commit:"+trackKey, request, 1800)
}

func (hostStore) Current(trackKey string) ([]byte, bool, error) {
	return host.KVStoreGet(publicationKeyPrefix + "state:" + trackKey)
}

func (hostStore) Active() (string, bool, error) {
	value, exists, err := host.KVStoreGet(publicationKeyPrefix + "active")
	if err != nil || !exists {
		return "", false, err
	}
	var active struct {
		Handled    bool   `json:"handled"`
		Generation string `json:"generation"`
	}
	if err := json.Unmarshal(value, &active); err != nil || !active.Handled || active.Generation == "" {
		return "", false, nil
	}
	return active.Generation, true, nil
}

func defaultStore() provider.Store {
	return hostStore{}
}

func defaultPublisher() provider.Publisher {
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
