package lyric

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// VariantIdentityVersion is the only identity schema this plugin emits or
// accepts. Retrieval rejects other versions instead of guessing at fields.
const VariantIdentityVersion = 1

// Identity field bounds keep listings, cache keys, and API tokens bounded.
const (
	MaxVariantInstanceBytes  = 192
	MaxVariantRecordingBytes = 128
	MaxVariantLanguageBytes  = 35
	MaxVariantRoleBytes      = 32
	MaxVariantFormatBytes    = 16
)

const (
	VariantRoleMain          = "main"
	VariantRoleTranslation   = "translation"
	VariantRolePronunciation = "pronunciation"
)

// VariantIdentity names one addressable playback representation. Instance is
// an opaque, credential-free source-instance fingerprint; Recording is the
// provider record ID or a content digest when the provider supplies none.
type VariantIdentity struct {
	Version   int    `json:"version"`
	Instance  string `json:"instance,omitempty"`
	Recording string `json:"recording,omitempty"`
	Language  string `json:"language,omitempty"`
	Role      string `json:"role,omitempty"`
	Format    string `json:"format,omitempty"`
}

// NewVariantIdentity builds a current-version identity with a normalized role.
func NewVariantIdentity(instance, recording, language, role, format string) VariantIdentity {
	if strings.TrimSpace(role) == "" {
		role = VariantRoleMain
	}
	return VariantIdentity{
		Version:   VariantIdentityVersion,
		Instance:  strings.TrimSpace(instance),
		Recording: strings.TrimSpace(recording),
		Language:  strings.TrimSpace(language),
		Role:      strings.TrimSpace(role),
		Format:    strings.ToLower(strings.TrimSpace(format)),
	}
}

// Valid reports whether the identity is a supported, bounded, exact address.
func (identity VariantIdentity) Valid() bool {
	if identity.Version != VariantIdentityVersion {
		return false
	}
	if identity.Instance == "" || identity.Recording == "" || identity.Format == "" {
		return false
	}
	if len(identity.Instance) > MaxVariantInstanceBytes ||
		len(identity.Recording) > MaxVariantRecordingBytes ||
		len(identity.Language) > MaxVariantLanguageBytes ||
		len(identity.Role) > MaxVariantRoleBytes ||
		len(identity.Format) > MaxVariantFormatBytes {
		return false
	}
	switch identity.Role {
	case VariantRoleMain, VariantRoleTranslation, VariantRolePronunciation:
	default:
		return false
	}
	return playbackFormat(identity.Format)
}

// InstanceSourceID extracts the source id an instance fingerprint belongs to.
func (identity VariantIdentity) InstanceSourceID() (string, bool) {
	index := strings.LastIndex(identity.Instance, ":")
	if index <= 0 || index == len(identity.Instance)-1 {
		return "", false
	}
	return identity.Instance[:index], true
}

// Equal compares every identity field: retrieval only serves an exact match.
func (identity VariantIdentity) Equal(other VariantIdentity) bool {
	return identity == other
}

// Key is the bounded cache-key discriminator for this identity.
func (identity VariantIdentity) Key() string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%s\x00%s\x00%s\x00%s",
		identity.Version, identity.Instance, identity.Recording, identity.Language, identity.Role, identity.Format)))
	return fmt.Sprintf("%x", digest[:16])
}

// playbackFormat lists the representations Navidrome can parse as playback.
func playbackFormat(format string) bool {
	switch format {
	case "ttml", "lrc", "elrc", "plain":
		return true
	}
	return false
}
