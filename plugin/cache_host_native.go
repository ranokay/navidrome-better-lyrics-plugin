//go:build !wasip1

package main

func logCacheDebug(string) {}

func defaultLyricsCache() lyricsCacheStore {
	return noopLyricsCache{}
}
