//go:build wasip1

package main

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"

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

func (hostStore) List(prefix string) ([]string, error) {
	return host.KVStoreList(prefix)
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

var errControlMergeUnsupported = errors.New("control merge unsupported")

func (hostStore) ControlMerge() (bool, error) {
	raw, exists, err := host.KVStoreGet(publicationKeyPrefix + "active")
	if err != nil || !exists {
		return false, err
	}
	var response struct {
		ControlMerge bool `json:"controlMerge"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return false, nil
	}
	return response.ControlMerge, nil
}

func (hostStore) ReduceFailure(sourceID, operation string, failures, until, updatedAt, resetAt, ttlSeconds int64) error {
	target := "source-failure:v1:" + sourceID
	request, err := json.Marshal(struct {
		Operation string `json:"operation"`
		Failures  int64  `json:"failures,omitempty"`
		Until     int64  `json:"until,omitempty"`
		UpdatedAt int64  `json:"updatedAt,omitempty"`
		ResetAt   int64  `json:"resetAt,omitempty"`
		TTL       int64  `json:"ttl"`
	}{Operation: operation, Failures: failures, Until: until, UpdatedAt: updatedAt, ResetAt: resetAt, TTL: ttlSeconds})
	if err != nil {
		return err
	}
	if err := host.KVStoreSetWithTTL(publicationKeyPrefix+"control:"+target, request, ttlSeconds+60); err != nil {
		return err
	}
	raw, exists, err := host.KVStoreGet(target)
	if err != nil {
		return err
	}
	if !exists {
		return errControlMergeUnsupported
	}
	var state struct {
		Failures  int64 `json:"failures"`
		Until     int64 `json:"until"`
		UpdatedAt int64 `json:"updatedAt"`
		ResetAt   int64 `json:"resetAt"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return errControlMergeUnsupported
	}
	if operation == "reset" {
		if state.ResetAt < resetAt {
			return errControlMergeUnsupported
		}
		return nil
	}
	if updatedAt < state.ResetAt {
		return nil
	}
	if state.Failures < failures || state.Until < until || state.UpdatedAt < updatedAt {
		return errControlMergeUnsupported
	}
	return nil
}

func (hostStore) MergeMax(key string, value, ttlSeconds int64) error {
	if key == "" || strings.HasPrefix(key, publicationKeyPrefix) {
		return errControlMergeUnsupported
	}
	request, err := json.Marshal(struct {
		Value int64 `json:"value"`
		TTL   int64 `json:"ttl"`
	}{Value: value, TTL: ttlSeconds})
	if err != nil {
		return err
	}
	if err := host.KVStoreSetWithTTL(publicationKeyPrefix+"control:"+key, request, ttlSeconds+60); err != nil {
		return err
	}
	raw, exists, err := host.KVStoreGet(key)
	if err != nil || !exists {
		return errControlMergeUnsupported
	}
	stored, parseErr := strconv.ParseInt(string(raw), 10, 64)
	if parseErr != nil || stored < value {
		return errControlMergeUnsupported
	}
	return nil
}

func (hostStore) Probe() (bool, error) {
	// A capable host rejects unknown reserved keys; an older host stores the
	// probe as an ordinary value.
	err := host.KVStoreSetWithTTL(publicationKeyPrefix+"probe", []byte("1"), 60)
	if err != nil {
		return true, nil
	}
	return false, nil
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
