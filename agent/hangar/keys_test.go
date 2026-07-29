package hangar_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/hangar"
)

func TestKindValidate(t *testing.T) {
	t.Parallel()

	for _, kind := range []hangar.Kind{hangar.KindSnapshot, hangar.KindCheckpoint} {
		kind := kind
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			if err := kind.Validate(); err != nil {
				t.Fatalf("expected %q to be valid: %v", kind, err)
			}
		})
	}

	for _, kind := range []hangar.Kind{"", "snapshot", "snapshots/../../secrets", "unknown"} {
		kind := kind
		t.Run("reject_"+string(kind), func(t *testing.T) {
			t.Parallel()
			if err := kind.Validate(); err == nil {
				t.Fatalf("expected %q to be rejected", kind)
			}
		})
	}
}

func TestDigestValidate(t *testing.T) {
	t.Parallel()

	valid := hangar.Digest("sha256:" + strings.Repeat("a", 64))
	if err := valid.Validate(); err != nil {
		t.Fatalf("expected digest to be valid: %v", err)
	}

	for name, digest := range map[string]hangar.Digest{
		"empty":           "",
		"missing prefix":  hangar.Digest(strings.Repeat("a", 64)),
		"wrong algorithm": hangar.Digest("sha512:" + strings.Repeat("a", 64)),
		"short":           hangar.Digest("sha256:" + strings.Repeat("a", 63)),
		"long":            hangar.Digest("sha256:" + strings.Repeat("a", 65)),
		"uppercase":       hangar.Digest("sha256:" + strings.Repeat("A", 64)),
		"non hexadecimal": hangar.Digest("sha256:" + strings.Repeat("g", 64)),
		"traversal":       hangar.Digest("sha256:../../" + strings.Repeat("a", 55)),
	} {
		name, digest := name, digest
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := digest.Validate(); err == nil {
				t.Fatalf("expected %q to be rejected", digest)
			}
		})
	}
}

func TestKey(t *testing.T) {
	t.Parallel()

	hex := strings.Repeat("a", 64)
	digest := hangar.Digest("sha256:" + hex)

	for _, tc := range []struct {
		kind hangar.Kind
		want string
	}{
		{kind: hangar.KindSnapshot, want: "hangar/v1/snapshots/sha256/" + hex + ".tar.zst"},
		{kind: hangar.KindCheckpoint, want: "hangar/v1/checkpoints/sha256/" + hex + ".tar.zst"},
	} {
		got, err := hangar.Key(tc.kind, digest)
		if err != nil {
			t.Fatalf("Key(%q): %v", tc.kind, err)
		}
		if got != tc.want {
			t.Fatalf("Key(%q) = %q, want %q", tc.kind, got, tc.want)
		}
	}

	if _, err := hangar.Key("snapshots/../../secrets", digest); err == nil {
		t.Fatal("expected traversal kind to be rejected")
	}
	if _, err := hangar.Key(hangar.KindSnapshot, "sha256:../../escape"); err == nil {
		t.Fatal("expected traversal digest to be rejected")
	}
}

func TestNewObjectRef(t *testing.T) {
	t.Parallel()

	hex := strings.Repeat("a", 64)
	ref, err := hangar.NewObjectRef(hangar.KindSnapshot, hangar.Digest("sha256:"+hex), 17)
	if err != nil {
		t.Fatalf("NewObjectRef: %v", err)
	}
	if ref.Kind != hangar.KindSnapshot {
		t.Fatalf("Kind = %q, want %q", ref.Kind, hangar.KindSnapshot)
	}
	if ref.Digest != hangar.Digest("sha256:"+hex) {
		t.Fatalf("Digest = %q", ref.Digest)
	}
	if ref.Key != "hangar/v1/snapshots/sha256/"+hex+".tar.zst" {
		t.Fatalf("Key = %q", ref.Key)
	}
	if ref.Generation != 17 {
		t.Fatalf("Generation = %d, want 17", ref.Generation)
	}

	for _, generation := range []int64{0, -1} {
		if _, err := hangar.NewObjectRef(hangar.KindSnapshot, hangar.Digest("sha256:"+hex), generation); err == nil {
			t.Fatalf("expected generation %d to be rejected", generation)
		}
	}
}
