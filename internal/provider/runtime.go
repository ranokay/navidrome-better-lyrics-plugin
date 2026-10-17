package provider

import (
	"time"

	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/source"
)

// Provider implements source.Runtime: the slice of runtime services that
// source fetches may use.
var _ source.Runtime = (*Provider)(nil)

func (p *Provider) Now() time.Time {
	return p.now()
}

func (p *Provider) Cache() source.Store {
	return p.cache
}

func (p *Provider) BetterLyricsAPIKey() string {
	return readProviderConfig(p.configLookup).BetterLyricsAPIKey
}

func (p *Provider) MusixmatchUserToken() string {
	return readProviderConfig(p.configLookup).MusixmatchUserToken
}
