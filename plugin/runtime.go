package main

import (
	"time"

	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

// lyricsProvider implements source.Runtime: the slice of runtime services that
// source fetches may use.
var _ source.Runtime = (*lyricsProvider)(nil)

func (p *lyricsProvider) DefaultHeaders() map[string]string {
	return defaultHeaders()
}

func (p *lyricsProvider) Now() time.Time {
	return p.now()
}

func (p *lyricsProvider) Cache() source.Store {
	return p.cache
}

func (p *lyricsProvider) BetterLyricsAPIKey() string {
	return readProviderConfig(p.configLookup).BetterLyricsAPIKey
}

func (p *lyricsProvider) MusixmatchUserToken() string {
	return readProviderConfig(p.configLookup).MusixmatchUserToken
}
