package jetbridge

import "testing"

func TestIsResourceCacheKey(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		{"rc-0", true},
		{"rc-1", true},
		{"rc-42", true},
		{"rc-1234567890", true},

		{"", false},
		{"rc-", false},
		{"rc", false},
		{"rc-abc", false},
		{"rc-1a", false},
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
	for _, id := range []int{0, 1, 7, 42, 99, 100, 9999} {
		key := ResourceCacheKey(id)
		if !isResourceCacheKey(key) {
			t.Errorf("ResourceCacheKey(%d) produced %q, which isResourceCacheKey rejects", id, key)
		}
	}
}
