package main

import (
	"fmt"

	"github.com/navidrome/navidrome/plugins/pdk/go/lyrics"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/lyric"
	"github.com/ranokay/navidrome-better-lyrics-plugin/internal/provider"
)

// The optional lyrics-variant exports are written against the host's
// lyricsvariants capability. They intentionally do not depend on a newer PDK:
// the plugin pins the released PDK and the host probes these functions
// independently of the required GetLyrics export.

type variantWire struct {
	Version   int    `json:"version"`
	Instance  string `json:"instance,omitempty"`
	Recording string `json:"recording,omitempty"`
	Language  string `json:"language,omitempty"`
	Role      string `json:"role,omitempty"`
	Format    string `json:"format,omitempty"`
}

type originalWire struct {
	Available  bool   `json:"available"`
	Format     string `json:"format,omitempty"`
	Provenance string `json:"provenance,omitempty"`
	MediaType  string `json:"mediaType,omitempty"`
	Extension  string `json:"extension,omitempty"`
	Size       int    `json:"size,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

type lyricsSourceWire struct {
	Provider string `json:"provider,omitempty"`
	Format   string `json:"format,omitempty"`
}

type matchWire struct {
	Status        string   `json:"status"`
	Reasons       []string `json:"reasons,omitempty"`
	DurationDelta float64  `json:"durationDelta,omitempty"`
	Recording     string   `json:"recording,omitempty"`
	AutoSave      bool     `json:"autoSaveEligible"`
}

type evidenceWire struct {
	Kind      string     `json:"kind"`
	Provider  string     `json:"provider,omitempty"`
	Recording string     `json:"recording,omitempty"`
	Match     *matchWire `json:"match,omitempty"`
	Reason    string     `json:"reason,omitempty"`
}

type lyricsTextWire struct {
	Lang    string            `json:"lang,omitempty"`
	Text    string            `json:"text"`
	Source  *lyricsSourceWire `json:"source,omitempty"`
	Variant *variantWire      `json:"variant,omitempty"`
	Match   *matchWire        `json:"match,omitempty"`
}

type publicationWire struct {
	Generation string `json:"generation,omitempty"`
	Token      string `json:"token,omitempty"`
	CallID     string `json:"callId,omitempty"`
	Kind       string `json:"kind,omitempty"`
}

type discoveryWire struct {
	Revision string `json:"revision,omitempty"`
	Complete bool   `json:"complete,omitempty"`
}

type getLyricsV2RequestWire struct {
	Track       lyrics.TrackInfo `json:"track"`
	Refresh     bool             `json:"refresh,omitempty"`
	Publication *publicationWire `json:"publication,omitempty"`
}

type getLyricsV2ResponseWire struct {
	Lyrics    []lyricsTextWire  `json:"lyrics"`
	Source    *lyricsSourceWire `json:"source,omitempty"`
	Evidence  []evidenceWire    `json:"evidence,omitempty"`
	Discovery *discoveryWire    `json:"discovery,omitempty"`
}

type getLyricsPublishedRequestWire struct {
	Track       lyrics.TrackInfo `json:"track"`
	Publication *publicationWire `json:"publication,omitempty"`
}

type getLyricsPublishedResponseWire struct {
	Lyrics    []lyricsTextWire  `json:"lyrics"`
	Source    *lyricsSourceWire `json:"source,omitempty"`
	Discovery *discoveryWire    `json:"discovery,omitempty"`
}

type getVariantsRequestWire struct {
	Track lyrics.TrackInfo `json:"track"`
}

type variantSummaryWire struct {
	Identity      variantWire  `json:"identity"`
	Provider      string       `json:"provider,omitempty"`
	Language      string       `json:"language,omitempty"`
	Role          string       `json:"role,omitempty"`
	Format        string       `json:"format,omitempty"`
	PlaybackIndex int          `json:"playbackIndex"`
	Original      originalWire `json:"original"`
	Match         *matchWire   `json:"match,omitempty"`
}

type getVariantsResponseWire struct {
	Variants []variantSummaryWire `json:"variants"`
	Evidence []evidenceWire       `json:"evidence,omitempty"`
}

type getOriginalRequestWire struct {
	Track       lyrics.TrackInfo `json:"track"`
	Identity    variantWire      `json:"identity"`
	Publication *publicationWire `json:"publication,omitempty"`
}

type getOriginalResponseWire struct {
	Identity          variantWire `json:"identity"`
	Available         bool        `json:"available"`
	Format            string      `json:"format,omitempty"`
	Provenance        string      `json:"provenance,omitempty"`
	MediaType         string      `json:"mediaType,omitempty"`
	Extension         string      `json:"extension,omitempty"`
	Document          []byte      `json:"document,omitempty"`
	UnavailableReason string      `json:"unavailableReason,omitempty"`
}

func variantToWire(identity lyric.VariantIdentity) variantWire {
	return variantWire{
		Version:   identity.Version,
		Instance:  identity.Instance,
		Recording: identity.Recording,
		Language:  identity.Language,
		Role:      identity.Role,
		Format:    identity.Format,
	}
}

func matchToWire(match lyric.RecordingMatch) *matchWire {
	if match.Status == "" {
		return nil
	}
	return &matchWire{
		Status: string(match.Status), Reasons: match.Reasons,
		DurationDelta: match.DurationDelta, Recording: match.Recording,
		AutoSave: match.AutoSave,
	}
}

func evidenceToWire(evidence lyric.Evidence) evidenceWire {
	return evidenceWire{
		Kind: string(evidence.Kind), Provider: evidence.Provider, Recording: evidence.Recording,
		Match: matchToWire(evidence.Match), Reason: evidence.Reason,
	}
}

func variantFromWire(wire variantWire) lyric.VariantIdentity {
	return lyric.VariantIdentity{
		Version:   wire.Version,
		Instance:  wire.Instance,
		Recording: wire.Recording,
		Language:  wire.Language,
		Role:      wire.Role,
		Format:    wire.Format,
	}
}

func originalToWire(info lyric.OriginalInfo) originalWire {
	return originalWire{
		Available:  info.Available,
		Format:     info.Format,
		Provenance: string(info.Provenance),
		MediaType:  info.MediaType,
		Extension:  info.Extension,
		Size:       info.Size,
		Reason:     info.Reason,
	}
}

func publicationFromWire(wire *publicationWire) provider.Publication {
	if wire == nil {
		return provider.Publication{}
	}
	return provider.Publication{
		Generation: wire.Generation, Token: wire.Token, CallID: wire.CallID, Kind: wire.Kind,
	}
}

func discoveryToWire(discovery provider.Discovery) *discoveryWire {
	if discovery.Revision == "" {
		return nil
	}
	return &discoveryWire{Revision: discovery.Revision, Complete: discovery.Complete}
}

func handleLyricsV2(input getLyricsV2RequestWire) (getLyricsV2ResponseWire, error) {
	if providerInstance == nil {
		return getLyricsV2ResponseWire{}, fmt.Errorf("lyrics provider is not initialized")
	}
	response, err := providerInstance.GetLyricsV2Published(
		lyrics.GetLyricsRequest{Track: input.Track}, publicationFromWire(input.Publication), input.Refresh,
	)
	if err != nil {
		return getLyricsV2ResponseWire{}, err
	}
	output := getLyricsV2ResponseWire{
		Lyrics:    make([]lyricsTextWire, 0, len(response.Entries)),
		Discovery: discoveryToWire(response.Discovery),
	}
	if response.Source != nil {
		output.Source = &lyricsSourceWire{Provider: response.Source.Provider, Format: response.Source.Format}
	}
	for _, entry := range response.Entries {
		text := lyricsTextWire{Lang: entry.Text.Lang, Text: entry.Text.Text}
		if entry.Text.Source != nil {
			text.Source = &lyricsSourceWire{Provider: entry.Text.Source.Provider, Format: entry.Text.Source.Format}
		} else if output.Source != nil {
			text.Source = output.Source
		}
		if entry.Identity != nil {
			identity := variantToWire(*entry.Identity)
			text.Variant = &identity
		}
		text.Match = matchToWire(entry.Match)
		output.Lyrics = append(output.Lyrics, text)
	}
	for _, evidence := range response.Evidence {
		output.Evidence = append(output.Evidence, evidenceToWire(evidence))
	}
	return output, nil
}

func handleLyricsPublished(input getLyricsPublishedRequestWire) (getLyricsPublishedResponseWire, error) {
	if providerInstance == nil {
		return getLyricsPublishedResponseWire{}, fmt.Errorf("lyrics provider is not initialized")
	}
	response, err := providerInstance.GetLyricsPublished(
		lyrics.GetLyricsRequest{Track: input.Track}, publicationFromWire(input.Publication),
	)
	if err != nil {
		return getLyricsPublishedResponseWire{}, err
	}
	output := getLyricsPublishedResponseWire{Lyrics: make([]lyricsTextWire, 0, len(response.Lyrics))}
	if response.Source != nil {
		output.Source = &lyricsSourceWire{Provider: response.Source.Provider, Format: response.Source.Format}
	}
	for _, entry := range response.Lyrics {
		text := lyricsTextWire{Lang: entry.Lang, Text: entry.Text}
		if entry.Source != nil {
			text.Source = &lyricsSourceWire{Provider: entry.Source.Provider, Format: entry.Source.Format}
		} else if output.Source != nil {
			text.Source = output.Source
		}
		output.Lyrics = append(output.Lyrics, text)
	}
	return output, nil
}

func handleVariants(input getVariantsRequestWire) (getVariantsResponseWire, error) {
	if providerInstance == nil {
		return getVariantsResponseWire{}, fmt.Errorf("lyrics provider is not initialized")
	}
	summaries := providerInstance.GetLyricsVariants(input.Track)
	output := getVariantsResponseWire{Variants: make([]variantSummaryWire, 0, len(summaries))}
	for _, summary := range summaries {
		output.Variants = append(output.Variants, variantSummaryWire{
			Identity:      variantToWire(summary.Identity),
			Provider:      summary.Provider,
			Language:      summary.Language,
			Role:          summary.Role,
			Format:        summary.Format,
			PlaybackIndex: summary.PlaybackIndex,
			Original:      originalToWire(summary.Original),
			Match:         matchToWire(summary.Match),
		})
	}
	for _, evidence := range providerInstance.GetLyricsEvidence(input.Track) {
		output.Evidence = append(output.Evidence, evidenceToWire(evidence))
	}
	return output, nil
}

func handleOriginal(input getOriginalRequestWire) (getOriginalResponseWire, error) {
	if providerInstance == nil {
		return getOriginalResponseWire{}, fmt.Errorf("lyrics provider is not initialized")
	}
	identity := variantFromWire(input.Identity)
	document, reason := providerInstance.GetLyricsOriginalPublished(input.Track, identity, publicationFromWire(input.Publication))
	output := getOriginalResponseWire{Identity: input.Identity, Available: reason == "", UnavailableReason: reason}
	if reason != "" {
		return output, nil
	}
	output.Format = document.Format
	output.Provenance = string(document.Provenance)
	output.MediaType = lyric.OriginalMediaType(document.Format)
	output.Extension = lyric.OriginalExtension(document.Format)
	output.Document = document.Bytes
	return output, nil
}
