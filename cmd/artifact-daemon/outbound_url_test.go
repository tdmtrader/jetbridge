package main

// R8: outbound peer URLs are built from validated, ESCAPED keys.
//
// The rolling-upgrade guarantee is byte-identity: a conforming key must
// produce exactly what the previous fmt.Sprintf produced, so an escaping
// daemon and a non-escaping one agree on the wire. Only keys that
// validateRequestKey refuses differ — and those are refused before they are
// ever sent.

import (
	"net/url"
	"strings"
	"testing"
)

// What peerURL actually guarantees — and what it does not.
//
// Two earlier versions of this file were wrong. The first asserted byte
// identity with fmt.Sprintf over six hand-picked keys while the validator
// accepted "a b" and "a%2fb", which render differently. The second made the
// property true by narrowing the accepted charset — and that narrowing was a
// regression that 400'd legal Concourse identifiers, so it was reverted.
//
// The reasoning was also at the wrong layer: net/http escapes URL.EscapedPath()
// on the wire regardless of how the string was built, so Sprintf and url.URL
// were never going to differ in what a peer RECEIVES for an ordinary key. The
// real divergence was in PARSING a key that already contained a percent
// sequence.
//
// So the guarantee is the narrow, true one: round-tripping. Whatever peerURL
// builds, a peer's TrimPrefix on the decoded path recovers exactly the key we
// meant — which is the property that matters, because a peer that recovers a
// different key stores the artifact somewhere else.
func TestPeerURL_KeyRoundTripsThroughAPeersDecoding(t *testing.T) {
	var checked int
	for _, key := range []string{
		"build-42-output.tar", "steps/build-99", "steps/build-42/result",
		"caches/job-42/build-abc.tar", "handle/output", "rc-42",
		// legal Concourse identifiers the narrow charset used to refuse
		"café", "_out", "-leading-dash", ".git",
		// the percent case that actually diverged
		"a%2fb", "a b",
	} {
		if validateRequestKey(key) != nil {
			continue
		}
		checked++

		raw := peerURL("https", "10.0.0.5", 7780, "/stream-in/", key)
		u, err := url.Parse(raw)
		if err != nil {
			t.Errorf("key %q produced an unparseable URL %q: %v", key, raw, err)
			continue
		}
		// This is what the receiving daemon does.
		got := strings.TrimPrefix(u.Path, "/stream-in/")
		if got != key {
			t.Errorf("key %q does not round-trip: peer would recover %q from %s", key, got, raw)
		}
	}
	if checked == 0 {
		t.Fatal("no candidate key was accepted — this test asserted nothing")
	}
	t.Logf("round-trip verified for %d accepted keys", checked)
}
