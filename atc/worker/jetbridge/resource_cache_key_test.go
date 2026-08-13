package jetbridge

import (
	"strings"
	"testing"
)

func TestIsResourceCacheKey(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		{"rc-0", true},
		{"rc-1", true},
		{"rc-42", true},
		{"rc-1234567890", true},

		// The content-key form, which is what a cache with a durable_key
		// produces. Narrowing the pattern back to digits stops resource caches
		// being probed at all, silently.
		{"rc-" + strings.Repeat("a", 64), true},
		{"rc-" + strings.Repeat("0123456789abcdef", 4), true},

		{"", false},
		{"rc-", false},
		{"rc", false},
		{"rc-abc", false},
		{"rc-1a", false},
		{"rc-" + strings.Repeat("a", 63), false},
		{"rc-" + strings.Repeat("a", 65), false},
		{"rc-" + strings.Repeat("A", 64), false},
		{"rc-" + strings.Repeat("g", 64), false},
		{"foo-rc-1", false},
		{"rc-1-suffix", false},
		{"RC-1", false},
		{"rc--1", false},
		{"rc-1/dir", false},
		{"artifact-handle-1", false},
	}

	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			if got := isResourceCacheKey(tc.key); got != tc.want {
				t.Errorf("isResourceCacheKey(%q) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}

// TestIsResourceCacheKey_MatchesResourceCacheKeyOutput ensures the predicate
// keeps in sync with the ResourceCacheKey generator: whatever ResourceCacheKey
// produces must be recognised by isResourceCacheKey.
func TestIsResourceCacheKey_MatchesResourceCacheKeyOutput(t *testing.T) {
	sha := "rc-" + strings.Repeat("ab", 32)

	for _, tc := range []struct {
		id         int
		durableKey string
	}{
		{0, ""}, {1, ""}, {7, ""}, {42, ""}, {9999, ""},
		{42, sha}, {1, sha},
	} {
		key := resourceCacheKey(tc.id, tc.durableKey)
		if !isResourceCacheKey(key) {
			t.Errorf("resourceCacheKey(%d, %q) produced %q, which isResourceCacheKey rejects", tc.id, tc.durableKey, key)
		}
	}
}

// A cache carrying a content key must be addressed by it. Falling back to the
// id would file a permanent copy under a Postgres sequence, which is the defect
// the durable key exists to prevent.
func TestResourceCacheKeyPrefersTheContentKey(t *testing.T) {
	sha := "rc-" + strings.Repeat("cd", 32)

	if got := resourceCacheKey(42, sha); got != sha {
		t.Errorf("resourceCacheKey(42, %q) = %q, want the content key", sha, got)
	}
	if got := resourceCacheKey(42, ""); got != "rc-42" {
		t.Errorf("resourceCacheKey(42, \"\") = %q, want \"rc-42\"", got)
	}
}
