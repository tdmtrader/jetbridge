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

func TestPeerURL_ConformingKeysAreByteIdenticalToSprintf(t *testing.T) {
	// Every real key shape, measured from the suite.
	for _, key := range []string{
		"build-42-output.tar",
		"steps/build-99",
		"steps/build-42/result",
		"caches/job-42/build-abc.tar",
		"handle/output",
		"rc-42",
	} {
		want := fmt.Sprintf("%s://%s:%d/stream-in/%s", "https", "10.0.0.5", 7780, key)
		got := peerURL("https", "10.0.0.5", 7780, "/stream-in/", key)
		if got != want {
			t.Errorf("key %q: rolling upgrade would break\n  sprintf: %s\n  urlpkg : %s", key, want, got)
		}
	}
}

// A key cannot inject path structure into a peer's route. This is the property
// Sprintf did not have: the peer's own TrimPrefix would decode whatever we sent.
func TestPeerURL_EscapesCharactersThatWouldInjectStructure(t *testing.T) {
	got := peerURL("http", "10.0.0.5", 7780, "/stream-in/", "a b?c#d")
	for _, bad := range []string{" ", "?", "#"} {
		if containsRune(got[len("http://10.0.0.5:7780/stream-in/"):], bad) {
			t.Errorf("unescaped %q survived into the path: %s", bad, got)
		}
	}
}

func containsRune(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
