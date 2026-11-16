package lyric

import (
	"strings"
	"testing"
)

func TestVariantIdentityRequiresVersionedBoundedFields(t *testing.T) {
	t.Parallel()

	valid := NewVariantIdentity("lrclib:0123456789abcdef0123456789abcdef", "1250", "en", "", "lrc")
	if !valid.Valid() {
		t.Fatalf("identity = %#v, want valid", valid)
	}
	if valid.Version != VariantIdentityVersion || valid.Role != VariantRoleMain {
		t.Fatalf("identity = %#v, want version %d and main role", valid, VariantIdentityVersion)
	}

	cases := []struct {
		name     string
		identity VariantIdentity
	}{
		{"unsupported version", VariantIdentity{Version: 2, Instance: "x:0123456789abcdef0123456789abcdef", Recording: "r", Role: "main", Format: "lrc"}},
		{"missing instance", VariantIdentity{Version: 1, Recording: "r", Role: "main", Format: "lrc"}},
		{"missing recording", VariantIdentity{Version: 1, Instance: "x:0123456789abcdef0123456789abcdef", Role: "main", Format: "lrc"}},
		{"missing format", VariantIdentity{Version: 1, Instance: "x:0123456789abcdef0123456789abcdef", Recording: "r", Role: "main"}},
		{"unknown role", VariantIdentity{Version: 1, Instance: "x:0123456789abcdef0123456789abcdef", Recording: "r", Role: "chorus", Format: "lrc"}},
		{"non-playback format", VariantIdentity{Version: 1, Instance: "x:0123456789abcdef0123456789abcdef", Recording: "r", Role: "main", Format: "qrc"}},
		{"oversized instance", VariantIdentity{Version: 1, Instance: strings.Repeat("a", MaxVariantInstanceBytes+1), Recording: "r", Role: "main", Format: "lrc"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.identity.Valid() {
				t.Fatalf("identity %#v must be rejected", tc.identity)
			}
		})
	}
}

func TestVariantIdentityInstanceAndKey(t *testing.T) {
	t.Parallel()

	identity := NewVariantIdentity("lrclib:0123456789abcdef0123456789abcdef", "1250", "", "", "lrc")
	if sourceID, ok := identity.InstanceSourceID(); !ok || sourceID != "lrclib" {
		t.Fatalf("source id = %q/%v, want lrclib", sourceID, ok)
	}
	if _, ok := (VariantIdentity{Instance: "nocolon"}).InstanceSourceID(); ok {
		t.Fatal("instance without a source id must not parse")
	}

	other := identity
	other.Format = "plain"
	other.Recording = "1251"
	if identity.Key() == other.Key() {
		t.Fatal("different identities must have different cache keys")
	}
	if identity.Key() != NewVariantIdentity(identity.Instance, identity.Recording, "", "", "lrc").Key() {
		t.Fatal("identity keys must be deterministic")
	}
	if !identity.Equal(NewVariantIdentity(identity.Instance, identity.Recording, "", "", "lrc")) {
		t.Fatal("equivalent identities must compare equal")
	}
	if identity.Equal(other) {
		t.Fatal("different identities must not compare equal")
	}
}
