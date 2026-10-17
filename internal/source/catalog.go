package source

// Catalog is the full source list in default config order. Sourced display names
// match the labels users see in the Navidrome plugins page and, later, in the
// lyrics UI.
var Catalog = []Source{
	{ID: "better-lyrics", Display: "Better Lyrics", Hosts: []string{"lyrics-api.boidu.dev"}, DefaultEnabled: true, Fetch: fetchBetterLyrics},
	{ID: "unison", Display: "Unison", Hosts: []string{"unison.boidu.dev"}, DefaultEnabled: true, Fetch: fetchUnison},
	{ID: "binimum", Display: "BiniLyrics", Hosts: []string{"lyrics-api.binimum.org", "lrc.red"}, DefaultEnabled: true, Fetch: fetchBinimum},
	{ID: "amll", Display: "AMLL", Hosts: []string{"api.amll.dev"}, DefaultEnabled: true, Fetch: fetchAMLL},
	{ID: "luna-beat", Display: "LunaBeat", Hosts: []string{"2755337087.github.io"}, DefaultEnabled: false, Fetch: fetchLunaBeat},
	{ID: "apple-lyrically", Display: "Apple Music (Lyrically)", Hosts: []string{"itunes.apple.com", "lyrics.paxsenix.org"}, DefaultEnabled: false, Fetch: fetchLyrically},
	{ID: "better-lyrics-portato", Display: "Better Lyrics Portato", Hosts: []string{"lyrics-api.boidu.dev"}, DefaultEnabled: true, Fetch: fetchPortato},
	{ID: "musixmatch", Display: "Musixmatch", Hosts: []string{"apic.musixmatch.com"}, DefaultEnabled: true, Fetch: fetchMusixmatch},
	{ID: "netease", Display: "NetEase", Hosts: []string{"music.163.com"}, DefaultEnabled: true, Fetch: fetchNetEase},
	{ID: "luna", Display: "Soda (Luna)", Hosts: []string{"api.qishui.com", "beta-luna.douyin.com"}, DefaultEnabled: true, Fetch: fetchLuna},
	{ID: "petitlyrics", Display: "PetitLyrics", Hosts: []string{"p0.petitlyrics.com"}, DefaultEnabled: true, Fetch: fetchPetitLyrics},
	{ID: "lrclib", Display: "LRCLib", Hosts: []string{"lrclib.net"}, DefaultEnabled: true, Fetch: fetchLRCLib},
	{ID: "better-lyrics-legato", Display: "Better Lyrics Legato", Hosts: []string{"lyrics-api.boidu.dev"}, DefaultEnabled: true, Fetch: fetchLegato},
	{ID: "lyricify", Display: "Lyricify", Hosts: []string{"api.credits.fm", "api.lyricify.app"}, DefaultEnabled: true, Fetch: fetchLyricify},
	{ID: "lyricsplus", Display: "LyricsPlus", Hosts: []string{"lyricsplus.binimum.org", "lyricsplus.atomix.one"}, DefaultEnabled: true, Fetch: fetchLyricsPlus},
	{ID: "yt-music", Display: "YouTube Music", Hosts: []string{"music.youtube.com"}, DefaultEnabled: false, Fetch: fetchYTMusic},
	{ID: "genius", Display: "Genius", Hosts: []string{"genius.com"}, DefaultEnabled: false, Fetch: fetchGenius},
}

// ByID resolves a catalog entry by its source id.
func ByID(id string) (Source, bool) {
	for _, source := range Catalog {
		if source.ID == id {
			return source, true
		}
	}
	return Source{}, false
}

// Defaults lists the catalog entries enabled out of the box, in catalog order.
func Defaults() []Source {
	sources := make([]Source, 0, len(Catalog))
	for _, source := range Catalog {
		if source.DefaultEnabled {
			sources = append(sources, source)
		}
	}
	return sources
}

// IDs lists the source ids in the given order; the provider's winner cache uses
// them as the fingerprint input.
func IDs(sources []Source) []string {
	ids := make([]string, 0, len(sources))
	for _, source := range sources {
		ids = append(ids, source.ID)
	}
	return ids
}
