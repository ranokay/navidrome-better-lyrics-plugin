package source

// Catalog is the full source list in default config order. Sourced display names
// match the labels users see in the Navidrome plugins page and, later, in the
// lyrics UI. Originals records, per adapter, whether a genuine source-native
// document can be preserved; entries marked unavailable expose only an API
// envelope or a reconstruction.
var Catalog = []Source{
	{ID: "better-lyrics", Display: "Better Lyrics", Hosts: []string{"lyrics-api.boidu.dev"}, DefaultEnabled: true, Originals: OriginalsNative, OriginalsNote: "TTML document from the API ttML field", Fetch: fetchBetterLyrics},
	{ID: "unison", Display: "Unison", Hosts: []string{"unison.boidu.dev"}, DefaultEnabled: true, Originals: OriginalsNative, OriginalsNote: "document in the declared ttml/lrc/plain format", Fetch: fetchUnison},
	{ID: "binimum", Display: "BiniLyrics", Hosts: []string{"lyrics-api.binimum.org", "lrc.red"}, DefaultEnabled: true, Originals: OriginalsNative, OriginalsNote: "raw TTML response body", Fetch: fetchBinimum},
	{ID: "amll", Display: "AMLL", Hosts: []string{"api.amll.dev"}, DefaultEnabled: true, Originals: OriginalsNative, OriginalsNote: "TTML document from data.lyrics", Fetch: fetchAMLL},
	{ID: "luna-beat", Display: "LunaBeat", Hosts: []string{"2755337087.github.io"}, DefaultEnabled: false, Originals: OriginalsNative, OriginalsNote: "raw TTML response body", Fetch: fetchLunaBeat},
	{ID: "apple-lyrically", Display: "Apple Music (Lyrically)", Hosts: []string{"itunes.apple.com", "lyrics.paxsenix.org"}, DefaultEnabled: false, Originals: OriginalsConditional, OriginalsNote: "Apple TTML when the mirror supplies ttmlContent; ELRC/plain are mirror conversions", Fetch: fetchLyrically},
	{ID: "better-lyrics-portato", Display: "Better Lyrics Portato", Hosts: []string{"lyrics-api.boidu.dev"}, DefaultEnabled: true, Originals: OriginalsNative, OriginalsNote: "QQ QRC XML container (QrcInfos) or raw QRC rows from the lyrics field", Fetch: fetchPortato},
	{ID: "netease", Display: "NetEase", Hosts: []string{"music.163.com"}, DefaultEnabled: true, Originals: OriginalsNative, OriginalsNote: "YRC or LRC document from the lyric envelope", Fetch: fetchNetEase},
	{ID: "luna", Display: "Soda (Luna)", Hosts: []string{"api.qishui.com", "beta-luna.douyin.com"}, DefaultEnabled: true, Originals: OriginalsNative, OriginalsNote: "KRC or LRC document from lyric.content", Fetch: fetchLuna},
	{ID: "petitlyrics", Display: "PetitLyrics", Hosts: []string{"p0.petitlyrics.com"}, DefaultEnabled: true, Originals: OriginalsNative, OriginalsNote: "decoded WSY bytes, LRC, or plain lyricsData", Fetch: fetchPetitLyrics},
	{ID: "lrclib", Display: "LRCLib", Hosts: []string{"lrclib.net"}, DefaultEnabled: true, Originals: OriginalsNative, OriginalsNote: "lyricsfile, syncedLyrics, or plainLyrics field", Fetch: fetchLRCLib},
	{ID: "better-lyrics-legato", Display: "Better Lyrics Legato", Hosts: []string{"lyrics-api.boidu.dev"}, DefaultEnabled: true, Originals: OriginalsNative, OriginalsNote: "LRC document from the lyrics field", Fetch: fetchLegato},
	{ID: "lyricify", Display: "Lyricify", Hosts: []string{"api.credits.fm", "api.lyricify.app"}, DefaultEnabled: true, Originals: OriginalsNative, OriginalsNote: "LYL document before offset conversion", Fetch: fetchLyricify},
	{ID: "lyricsplus", Display: "LyricsPlus", Hosts: []string{"lyricsplus.binimum.org", "lyricsplus.atomix.one"}, DefaultEnabled: true, Originals: OriginalsUnavailable, OriginalsNote: "structured JSON rows only; ELRC is reconstructed", Fetch: fetchLyricsPlus},
	{ID: "yt-music", Display: "YouTube Music", Hosts: []string{"music.youtube.com"}, DefaultEnabled: false, Originals: OriginalsUnavailable, OriginalsNote: "plain text extracted from the browse JSON envelope", Fetch: fetchYTMusic},
	{ID: "genius", Display: "Genius", Hosts: []string{"genius.com"}, DefaultEnabled: false, Originals: OriginalsUnavailable, OriginalsNote: "plain text extracted from the song HTML page", Fetch: fetchGenius},
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
