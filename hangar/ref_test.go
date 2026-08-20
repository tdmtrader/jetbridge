package hangar_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/hangar"
)

func TestScopeValidate(t *testing.T) {
	t.Parallel()

	for _, scope := range []hangar.Scope{"ci", "a", "cache.v1", "team_42", "release-2026", hangar.Scope("a" + strings.Repeat("x", 62))} {
		scope := scope
		t.Run(string(scope), func(t *testing.T) {
			t.Parallel()
			if err := scope.Validate(); err != nil {
				t.Fatalf("Validate() = %v, want valid scope", err)
			}
		})
	}

	for name, scope := range map[string]hangar.Scope{
		"empty":              "",
		"too long":           hangar.Scope("a" + strings.Repeat("x", 63)),
		"uppercase":          "Cache",
		"starts punctuation": "-cache",
		"slash":              "cache/other",
		"space":              "cache scope",
		"non ASCII":          "caché",
		"non ASCII low byte": "cš",
	} {
		name, scope := name, scope
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := scope.Validate(); err == nil {
				t.Fatal("Validate() succeeded, want invalid scope rejection")
			}
		})
	}
}

func TestDigestValidate(t *testing.T) {
	t.Parallel()

	valid := hangar.Digest("sha256:" + strings.Repeat("a", 64))
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want valid digest", err)
	}

	for name, digest := range map[string]hangar.Digest{
		"empty":           "",
		"wrong algorithm": hangar.Digest("sha512:" + strings.Repeat("a", 64)),
		"short":           hangar.Digest("sha256:" + strings.Repeat("a", 63)),
		"long":            hangar.Digest("sha256:" + strings.Repeat("a", 65)),
		"uppercase":       hangar.Digest("sha256:" + strings.Repeat("A", 64)),
		"non hexadecimal": hangar.Digest("sha256:" + strings.Repeat("g", 64)),
		"path traversal":  hangar.Digest("sha256:../../" + strings.Repeat("a", 55)),
	} {
		name, digest := name, digest
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := digest.Validate(); err == nil {
				t.Fatal("Validate() succeeded, want invalid digest rejection")
			}
		})
	}
}

func TestTreeRefValidate(t *testing.T) {
	t.Parallel()

	ref := hangar.TreeRef{
		Scope:      "ci",
		Digest:     hangar.Digest("sha256:" + strings.Repeat("b", 64)),
		Generation: 17,
	}
	if err := ref.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want valid tree ref", err)
	}

	for name, invalid := range map[string]hangar.TreeRef{
		"invalid scope":       {Scope: "CI", Digest: ref.Digest, Generation: 17},
		"invalid digest":      {Scope: ref.Scope, Digest: "sha256:no", Generation: 17},
		"zero generation":     {Scope: ref.Scope, Digest: ref.Digest, Generation: 0},
		"negative generation": {Scope: ref.Scope, Digest: ref.Digest, Generation: -1},
	} {
		name, invalid := name, invalid
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := invalid.Validate(); err == nil {
				t.Fatal("Validate() succeeded, want invalid tree ref rejection")
			}
		})
	}
}

func TestTreeKey(t *testing.T) {
	t.Parallel()

	digest := hangar.Digest("sha256:" + strings.Repeat("c", 64))
	for _, tc := range []struct {
		name   string
		prefix string
		want   string
	}{
		{
			name: "without deployment prefix",
			want: "hangar/v1/scopes/ci/trees/sha256/" + strings.Repeat("c", 64) + ".tar.zst",
		},
		{
			name:   "with deployment prefix",
			prefix: "durable/production",
			want:   "durable/production/hangar/v1/scopes/ci/trees/sha256/" + strings.Repeat("c", 64) + ".tar.zst",
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := hangar.TreeKey(tc.prefix, "ci", digest)
			if err != nil {
				t.Fatalf("TreeKey() = %v", err)
			}
			if got != tc.want {
				t.Fatalf("TreeKey() = %q, want %q", got, tc.want)
			}
		})
	}

	for name, prefix := range map[string]string{
		"absolute":       "/durable",
		"traversal":      "durable/../secrets",
		"empty segment":  "durable//production",
		"dot segment":    "durable/./production",
		"backslash":      `durable\\production`,
		"leading space":  " durable",
		"trailing space": "durable ",
	} {
		name, prefix := name, prefix
		t.Run("reject prefix "+name, func(t *testing.T) {
			t.Parallel()
			if _, err := hangar.TreeKey(prefix, "ci", digest); err == nil {
				t.Fatal("TreeKey() succeeded, want invalid deployment prefix rejection")
			}
		})
	}

	if _, err := hangar.TreeKey("", "CI", digest); err == nil {
		t.Fatal("TreeKey() succeeded, want invalid scope rejection")
	}
	if _, err := hangar.TreeKey("", "ci", "sha256:no"); err == nil {
		t.Fatal("TreeKey() succeeded, want invalid digest rejection")
	}
}
