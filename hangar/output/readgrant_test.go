package output

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar"
)

func readWarrantClaim() ClaimRecord {
	acquired := NewTimestamp(time.Date(2026, 9, 8, 21, 52, 0, 0, time.UTC))
	expires := NewTimestamp(acquired.Add(15 * time.Minute))
	return ClaimRecord{
		ClaimID: "5f3d2a19-8c47-4e60-b1a2-0d9e8f7c6b5a",
		Ref: hangar.TreeRef{Scope: "0192b3c4d5e6f708",
			Digest:     "sha256:9f2c0c4a6a1a7f7f6b1d0f2f3e4d5c6b7a8998a7b6c5d4e3f201122334455667",
			Generation: 1725830823000001},
		ConsumerBindingID: "reader-binding",
		AcquiredAt:        acquired,
		ExpiresAt:         &expires,
	}
}

func readWarrantDestination() ReadDestination {
	return ReadDestination{Handle: "task-handle", Volume: "input-0"}
}

// A read warrant is the one Hangar warrant under its own purpose, bound to
// the committed claim: its window is the claim's and it carries no nonce.
func TestAReadWarrantIsBoundToItsClaimAndCarriesNoNonce(t *testing.T) {
	warrant, err := ReadWarrant(readWarrantClaim(), readWarrantDestination(), "node-a")
	if err != nil {
		t.Fatal(err)
	}
	claim := readWarrantClaim()
	if warrant.Purpose != hangar.PurposeReadResult || warrant.ClaimID != string(claim.ClaimID) ||
		warrant.Ref != claim.Ref || warrant.Handle != "task-handle" || warrant.Volume != "input-0" ||
		warrant.NodeUID != "node-a" || warrant.Nonce != "" ||
		warrant.IssuedAt != claim.AcquiredAt.UnixNano() || warrant.ExpiresAt != claim.ExpiresAt.UnixNano() {
		t.Fatalf("read warrant = %+v", warrant)
	}
	signer, err := hangar.NewSigner(make([]byte, hangar.WarrantKeyBytes), time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	minter := ReadWarrantMinter{Signer: signer}
	first, err := minter.Sign(readWarrantClaim(), readWarrantDestination(), "node-a")
	if err != nil {
		t.Fatal(err)
	}
	second, err := minter.Sign(readWarrantClaim(), readWarrantDestination(), "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("two mints of one committed claim differ")
	}
	verifier, err := hangar.NewVerifier(make([]byte, hangar.WarrantKeyBytes), hangar.MaxWarrantTTL,
		func() time.Time { return claim.AcquiredAt.Add(time.Minute) })
	if err != nil {
		t.Fatal(err)
	}
	bound, err := verifier.Verify(first, hangar.Warrant{Purpose: hangar.PurposeReadResult, Ref: claim.Ref,
		Handle: "task-handle", Volume: "input-0", NodeUID: "node-a"})
	if err != nil {
		t.Fatal(err)
	}
	if bound.ClaimID != string(claim.ClaimID) {
		t.Fatalf("the verifier handed back claim %q", bound.ClaimID)
	}
	if _, err := verifier.Verify(first, hangar.Warrant{Purpose: hangar.PurposeReadResult, Ref: claim.Ref,
		Handle: "task-handle", Volume: "input-0", NodeUID: "node-b"}); !errors.Is(err, hangar.ErrUnauthorized) {
		t.Fatalf("a read warrant for node-a verified on node-b: %v", err)
	}
	if (ReadWarrantMinter{}).Signer != nil {
		t.Fatal("unreachable")
	}
	if _, err := (ReadWarrantMinter{}).Sign(readWarrantClaim(), readWarrantDestination(), "node-a"); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("a minter with no signer: %v", err)
	}
}

func TestReadWarrantRefusesAClaimThatIsNotALiveReadersHold(t *testing.T) {
	consumer := readWarrantClaim()
	consumer.ExpiresAt = nil
	if _, err := ReadWarrant(consumer, readWarrantDestination(), "node-a"); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("a consumer's hold (no expiry): %v", err)
	}
	released := readWarrantClaim()
	at := NewTimestamp(released.AcquiredAt.Add(time.Second))
	released.ReleasedAt = &at
	if _, err := ReadWarrant(released, readWarrantDestination(), "node-a"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a released claim: %v", err)
	}
	if _, err := ReadWarrant(readWarrantClaim(), readWarrantDestination(), ""); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("no node: %v", err)
	}
	if _, err := ReadWarrant(readWarrantClaim(), ReadDestination{Handle: "../x", Volume: "v"}, "node-a"); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("a path destination: %v", err)
	}
	inverted := readWarrantClaim()
	before := NewTimestamp(inverted.AcquiredAt.Add(-time.Second))
	inverted.ExpiresAt = &before
	if _, err := ReadWarrant(inverted, readWarrantDestination(), "node-a"); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("expiry before acquisition: %v", err)
	}
}

// The destination segment rule is the foundation's. The two copies are
// cross-checked here so neither can widen without the other.
func TestAReadDestinationSegmentIsTheFoundationsPathSegmentRule(t *testing.T) {
	ref := readWarrantClaim().Ref
	for segment, valid := range map[string]bool{
		"a": true, "task-handle": true, "input_0.tar": true, "A1": true,
		"": false, "-leading": false, "trailing.": false, "with space": false, "slash/inside": false,
		"..": false, "dots..inside": true, strings.Repeat("x", 128): true, strings.Repeat("x", 129): false,
		"unicodé": false, "tab\t": false,
	} {
		got := validDestinationSegment(segment)
		if got != valid {
			t.Errorf("validDestinationSegment(%q) = %v, want %v", segment, got, valid)
		}
		foundation := hangar.Warrant{Purpose: hangar.PurposeMaterializeInput, Ref: ref, Handle: segment, Volume: "v",
			Version: 1, IssuedAt: 1, ExpiresAt: 2, Nonce: "AAAAAAAAAAAAAAAAAAAAAA"}
		if foundationValid := foundation.Validate() == nil; foundationValid != valid {
			t.Errorf("hangar accepts handle %q = %v, this package = %v", segment, foundationValid, valid)
		}
	}
}

func TestAReadDestinationIsNeverAPath(t *testing.T) {
	for _, destination := range []ReadDestination{
		{Handle: "/etc", Volume: "v"},
		{Handle: "h", Volume: "../../steps"},
		{Handle: "h/../x", Volume: "v"},
		{Handle: "", Volume: "v"},
		{Handle: "h", Volume: ""},
	} {
		if err := destination.Validate(); !errors.Is(err, ErrInvalidIdentity) {
			t.Errorf("%+v validated: %v", destination, err)
		}
	}
	if err := readWarrantDestination().Validate(); err != nil {
		t.Fatal(err)
	}
}
