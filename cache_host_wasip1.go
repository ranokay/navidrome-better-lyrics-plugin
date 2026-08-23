//go:build wasip1

package main

import "github.com/navidrome/navidrome/plugins/pdk/go/host"

type hostLyricsCache struct{}

func (hostLyricsCache) Get(key string) ([]byte, bool, error) {
	return host.KVStoreGet(key)
}

func (hostLyricsCache) SetWithTTL(key string, value []byte, ttlSeconds int64) error {
	return host.KVStoreSetWithTTL(key, value, ttlSeconds)
}

func defaultLyricsCache() lyricsCacheStore {
	return hostLyricsCache{}
}
