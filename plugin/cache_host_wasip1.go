//go:build wasip1

package main

import (
	pdk "github.com/extism/go-pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
)

type hostLyricsCache struct{}

func (hostLyricsCache) Get(key string) ([]byte, bool, error) {
	return host.KVStoreGet(key)
}

func (hostLyricsCache) SetWithTTL(key string, value []byte, ttlSeconds int64) error {
	return host.KVStoreSetWithTTL(key, value, ttlSeconds)
}

func (hostLyricsCache) StorageUsed() (int64, error) {
	return host.KVStoreGetStorageUsed()
}

func logCacheDebug(message string) {
	pdk.Log(pdk.LogDebug, message)
}

func defaultLyricsCache() lyricsCacheStore {
	return hostLyricsCache{}
}
