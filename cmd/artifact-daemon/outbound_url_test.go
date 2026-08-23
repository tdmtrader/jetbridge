package main

// R8: outbound peer URLs are built from validated, ESCAPED keys.
//
// The rolling-upgrade guarantee is byte-identity: a conforming key must
// produce exactly what the previous fmt.Sprintf produced, so an escaping
// daemon and a non-escaping one agree on the wire. Only keys that
// validateRequestKey refuses differ — and those are refused before they are
// ever sent.

import (
	"fmt"
	"testing"
)

// Byte-identity is now a PROPERTY over the whole accepted set, not six
// hand-picked strings. The first version asserted identity for a sample while a
// second test asserted that "a b?c#d" — which the validator then accepted —
// was escaped. The two contradicted each other; the charset restriction in
// validateRequestKey is what makes the property true.
func TestPeerURL_ByteIdenticalForEveryAcceptedKey(t *testing.T) {
	candidates := []string{
		// real shapes
		"build-42-output.tar", "steps/build-99", "steps/build-42/result",
		"caches/job-42/build-abc.tar", "handle/output", "rc-42",
		// the ones the first cut accepted and rendered differently
		"a b", "a?b", "a#b", "a%2fb", "a\\b", "a[1]", "a|b", "a'b", "a\"b", "café",
		// traversal and degenerate forms
		"..", ".", "./", "a/../b", "steps",
	}

	var checked int
	for _, key := range candidates {
		if validateRequestKey(key) != nil {
			continue // refused before it could ever be sent
		}
		checked++
		want := fmt.Sprintf("%s://%s:%d/stream-in/%s", "https", "10.0.0.5", 7780, key)
		got := peerURL("https", "10.0.0.5", 7780, "/stream-in/", key)
		if got != want {
			t.Errorf("accepted key %q renders differently — a mixed-version rolling upgrade "+
				"would land it under two different keys:\n  sprintf: %s\n  urlpkg : %s",
				key, want, got)
		}
	}

	// The guard must be able to fail: if the validator ever rejects everything,
	// this test would pass vacuously.
	if checked == 0 {
		t.Fatal("no candidate key was accepted — this test asserted nothing")
	}
	t.Logf("byte-identity verified over %d accepted keys of %d candidates", checked, len(candidates))
}
