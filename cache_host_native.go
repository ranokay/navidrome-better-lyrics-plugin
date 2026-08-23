//go:build !wasip1

package main

func defaultLyricsCache() lyricsCacheStore {
	return noopLyricsCache{}
}
