package executioncontrol

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

const captureFacetForTest Facet = "durable-output-capture"

func capabilityPair(t *testing.T, at *time.Time) (*CapabilityMinter, *CapabilityVerifier) {
	t.Helper()

	secret := bytes.Repeat([]byte{7}, CapabilityKeyBytes)
	clock := func() time.Time { return *at }

	minter, err := NewCapabilityMinter(secret, time.Minute, clock)
	if err != nil {
		t.Fatalf("building the minter: %v", err)
	}
	verifier, err := NewCapabilityVerifier(secret, time.Minute, clock)
	if err != nil {
		t.Fatalf("building the verifier: %v", err)
	}

	return minter, verifier
}

func baseClaims() CapabilityClaims {
	return CapabilityClaims{
		Facet:     BaseFacet,
		Operation: "classify",
		Identity: Identity{
			ExecutionID: "33333333-3333-4333-8333-333333333333",
			Fence:       4,
		},
		ActivationEpoch: 7,
	}
}

// The control is asserted first: a capability minted for an operation
// authorizes that operation. "Everything is refused" is what a broken verifier
// looks like, and it would pass every row below on its own.
func TestACapabilityAuthorizesTheOperationItWasMintedForAndNoOther(t *testing.T) {
	now := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	minter, verifier := capabilityPair(t, &now)

	token, err := minter.Mint(baseClaims(), "nonce-control")
	if err != nil {
		t.Fatalf("minting: %v", err)
	}
	if err := verifier.Verify(token, baseClaims()); err != nil {
		t.Fatalf("the control capability was refused: %v", err)
	}

	for name, differ := range map[string]func(*CapabilityClaims){
		// The facet row is the one the route table stands on: a base control
		// capability cannot hold, seal or publish, however valid it is.
		"another facet":            func(c *CapabilityClaims) { c.Facet = captureFacetForTest },
		"another operation":        func(c *CapabilityClaims) { c.Operation = "stop" },
		"another execution":        func(c *CapabilityClaims) { c.Identity.ExecutionID = "44444444-4444-4444-8444-444444444444" },
		"another fence":            func(c *CapabilityClaims) { c.Identity.Fence++ },
		"another activation epoch": func(c *CapabilityClaims) { c.ActivationEpoch++ },
	} {
		expected := baseClaims()
		differ(&expected)

		fresh, err := minter.Mint(baseClaims(), "nonce-"+strings.ReplaceAll(name, " ", "-"))
		if err != nil {
			t.Fatalf("minting: %v", err)
		}
		if err := verifier.Verify(fresh, expected); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("a capability minted for the base classify operation was admitted for %s: %v",
				name, err)
		}
	}
}

func TestACapabilityIsSpentOnceAndExpires(t *testing.T) {
	now := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	minter, verifier := capabilityPair(t, &now)

	token, err := minter.Mint(baseClaims(), "nonce-replay")
	if err != nil {
		t.Fatalf("minting: %v", err)
	}
	if err := verifier.Verify(token, baseClaims()); err != nil {
		t.Fatalf("the first presentation was refused: %v", err)
	}
	if err := verifier.Verify(token, baseClaims()); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("the same capability was admitted twice: %v", err)
	} else if !strings.Contains(err.Error(), "nonce-replay") {
		t.Errorf("the replay refusal does not name the nonce: %v", err)
	}

	// A second, unspent capability still works, so the replay refusal is about
	// this nonce and not about the verifier having given up.
	fresh, err := minter.Mint(baseClaims(), "nonce-fresh")
	if err != nil {
		t.Fatalf("minting: %v", err)
	}
	if err := verifier.Verify(fresh, baseClaims()); err != nil {
		t.Errorf("an unspent capability was refused after a replay: %v", err)
	}

	// And expiry, which is the other half: a captured token stops working.
	later, err := minter.Mint(baseClaims(), "nonce-expiring")
	if err != nil {
		t.Fatalf("minting: %v", err)
	}
	now = now.Add(2 * time.Minute)
	if err := verifier.Verify(later, baseClaims()); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("an expired capability was admitted: %v", err)
	}
}

func TestACapabilityMintedUnderAnotherSecretIsRefused(t *testing.T) {
	now := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	foreign, err := NewCapabilityMinter(bytes.Repeat([]byte{9}, CapabilityKeyBytes), time.Minute, clock)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	_, verifier := capabilityPair(t, &now)

	token, err := foreign.Mint(baseClaims(), "nonce-foreign")
	if err != nil {
		t.Fatalf("minting: %v", err)
	}
	if err := verifier.Verify(token, baseClaims()); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("a capability from another cohort was admitted: %v", err)
	}

	// Malformed shapes fail closed rather than panicking.
	for _, malformed := range []ControlCapability{"", "no-separators", "a|b", "a|b|c|d", "a|not-a-number|c"} {
		if err := verifier.Verify(malformed, baseClaims()); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("the malformed capability %q was admitted: %v", malformed, err)
		}
	}
}

func TestACapabilityKeyAndTTLAreBounded(t *testing.T) {
	clock := func() time.Time { return time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC) }
	good := bytes.Repeat([]byte{7}, CapabilityKeyBytes)

	// The control.
	if _, err := NewCapabilityMinter(good, time.Minute, clock); err != nil {
		t.Fatalf("the valid minter did not build: %v", err)
	}

	for name, build := range map[string]func() error{
		"a short key":      func() error { _, err := NewCapabilityMinter(good[:16], time.Minute, clock); return err },
		"a long key":       func() error { _, err := NewCapabilityMinter(append(good, 1), time.Minute, clock); return err },
		"a zero TTL":       func() error { _, err := NewCapabilityMinter(good, 0, clock); return err },
		"a negative TTL":   func() error { _, err := NewCapabilityMinter(good, -time.Second, clock); return err },
		"an hour-long TTL": func() error { _, err := NewCapabilityMinter(good, time.Hour, clock); return err },
		"a short verifier key": func() error {
			_, err := NewCapabilityVerifier(good[:16], time.Minute, clock)

			return err
		},
		"an hour-long verifier TTL": func() error {
			_, err := NewCapabilityVerifier(good, time.Hour, clock)

			return err
		},
	} {
		if err := build(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
