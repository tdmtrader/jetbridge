package output

// What an output read grant binds, and what it refuses.
//
// The control row is FIRST in every table below, because a verifier that
// refused everything would pass a table made only of refusals. Each refusal row
// changes exactly one field of a grant the control row proved verifies, so a
// red row names the field that is not inside the signature rather than "a grant
// did not verify".
//
// The two separations that are not tamper rows are the ones that matter most:
// the foundation's strict-input grant must not verify here even when both keys
// hold the same 32 bytes (the domain is inside the signed bytes), and the
// receipt's Ed25519 key must not be constructible into a read grant signer at
// all (it is not 32 bytes, and the length check is exact rather than a floor).

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
)

var readGrantKey = []byte("0123456789abcdef0123456789abcdef")

func readGrantLease() ReadLease {
	granted := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

	return ReadLease{
		ProtocolVersion: ProtocolVersion,
		ReadLeaseID:     ReadLeaseID("6a5b4c3d-2e1f-4a0b-9c8d-7e6f5a4b3c2d"),
		ClaimID:         ClaimID("1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"),
		Ref: hangar.TreeRef{
			Scope:      "deployment-ns",
			Digest:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Generation: 7,
		},
		ActivationEpoch: executioncontrol.ActivationEpoch(3),
		LeaseFence:      LeaseFence(1),
		GrantedAt:       NewTimestamp(granted),
		ExpiresAt:       NewTimestamp(granted.Add(20 * time.Minute)),
	}
}

func readGrantDestination() ReadDestination {
	return ReadDestination{Handle: "task-handle", Volume: "input-0"}
}

// insideTheWindow is a clock inside the lease's own window, which is the
// grant's window too.
func insideTheWindow() ClockFunc {
	return func() time.Time { return readGrantLease().GrantedAt.Add(time.Minute) }
}

func mustSignReadGrant(t *testing.T) string {
	t.Helper()

	signer, err := NewReadGrantSigner(readGrantKey)
	if err != nil {
		t.Fatalf("new read grant signer: %v", err)
	}
	nonce, err := NewReadGrantNonce(strings.NewReader("0123456789abcdef"))
	if err != nil {
		t.Fatalf("new read grant nonce: %v", err)
	}
	token, err := signer.Sign(readGrantLease(), readGrantDestination(), nonce)
	if err != nil {
		t.Fatalf("sign read grant: %v", err)
	}

	return token
}

func TestAnOutputReadGrantVerifiesForItsOwnLeaseRefAndDestination(t *testing.T) {
	verifier, err := NewReadGrantVerifier(readGrantKey, insideTheWindow())
	if err != nil {
		t.Fatalf("new read grant verifier: %v", err)
	}

	lease := readGrantLease()
	claims, err := verifier.Verify(mustSignReadGrant(t), lease.Ref, readGrantDestination())
	if err != nil {
		t.Fatalf("a grant minted for this lease did not verify: %v", err)
	}

	if claims.ReadLeaseID != lease.ReadLeaseID {
		t.Errorf("read lease id = %q, want %q", claims.ReadLeaseID, lease.ReadLeaseID)
	}
	if claims.LeaseFence != lease.LeaseFence {
		t.Errorf("lease fence = %d, want %d", claims.LeaseFence, lease.LeaseFence)
	}
	if claims.ClaimID != lease.ClaimID {
		t.Errorf("claim id = %q, want %q", claims.ClaimID, lease.ClaimID)
	}
	if claims.ActivationEpoch != lease.ActivationEpoch {
		t.Errorf("activation epoch = %d, want %d", claims.ActivationEpoch, lease.ActivationEpoch)
	}
	if claims.Destination != readGrantDestination() {
		t.Errorf("destination = %+v, want %+v", claims.Destination, readGrantDestination())
	}
	if !claims.ExpiresAt.Equal(lease.ExpiresAt.Time) {
		t.Errorf("expiry = %s, want the lease's own %s", claims.ExpiresAt, lease.ExpiresAt)
	}
}

// Every bound field, one row each.
//
// The grant is decoded, one field is edited, and it is re-encoded with the
// ORIGINAL MAC. That is the shape of the attack the signature exists to stop --
// a token whose body was rewritten in flight -- and it is why each row names a
// field rather than "a corrupt token".
func TestAnEditedOutputReadGrantDoesNotVerify(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*ReadGrantClaims)
	}{
		{"read lease id", func(c *ReadGrantClaims) {
			c.ReadLeaseID = ReadLeaseID("99999999-2e1f-4a0b-9c8d-7e6f5a4b3c2d")
		}},
		{"lease fence", func(c *ReadGrantClaims) { c.LeaseFence = c.LeaseFence + 1 }},
		{"claim id", func(c *ReadGrantClaims) {
			c.ClaimID = ClaimID("99999999-5e6f-4a7b-8c9d-0e1f2a3b4c5d")
		}},
		{"ref scope", func(c *ReadGrantClaims) { c.Ref.Scope = "another-ns" }},
		{"ref digest", func(c *ReadGrantClaims) {
			c.Ref.Digest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		}},
		{"ref generation", func(c *ReadGrantClaims) { c.Ref.Generation++ }},
		{"destination handle", func(c *ReadGrantClaims) { c.Destination.Handle = "other-handle" }},
		{"destination volume", func(c *ReadGrantClaims) { c.Destination.Volume = "input-9" }},
		{"activation epoch", func(c *ReadGrantClaims) { c.ActivationEpoch++ }},
		{"issued at", func(c *ReadGrantClaims) {
			c.IssuedAt = NewTimestamp(c.IssuedAt.Add(-time.Hour))
		}},
		{"expires at", func(c *ReadGrantClaims) {
			c.ExpiresAt = NewTimestamp(c.ExpiresAt.Add(24 * time.Hour))
		}},
		{"nonce", func(c *ReadGrantClaims) { c.Nonce = "AAAAAAAAAAAAAAAAAAAAAA" }},
		{"domain", func(c *ReadGrantClaims) { c.Domain = hangarStrictInputDomain }},
		{"version", func(c *ReadGrantClaims) { c.Version = "2" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			verifier, err := NewReadGrantVerifier(readGrantKey, insideTheWindow())
			if err != nil {
				t.Fatalf("new read grant verifier: %v", err)
			}

			token := editReadGrant(t, mustSignReadGrant(t), test.edit)

			// The rows that move the ref or the destination are asked for
			// under the EDITED value, so a row cannot pass merely because the
			// caller asked for something the grant never named.
			var edited ReadGrantClaims
			decodeReadGrantPayload(t, token, &edited)

			if _, err := verifier.Verify(token, edited.Ref,
				edited.Destination); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("a grant whose %s was edited verified: %v", test.name, err)
			}
		})
	}
}

// The window is the lease's, and it is checked at both ends.
func TestAnOutputReadGrantIsRefusedOutsideTheLeaseWindow(t *testing.T) {
	lease := readGrantLease()

	for _, test := range []struct {
		name string
		at   time.Time
	}{
		{"before the lease was granted", lease.GrantedAt.Add(-time.Second)},
		{"at the moment the lease expires", lease.ExpiresAt.Time},
		{"after the lease expires", lease.ExpiresAt.Add(time.Hour)},
	} {
		t.Run(test.name, func(t *testing.T) {
			verifier, err := NewReadGrantVerifier(readGrantKey,
				ClockFunc(func() time.Time { return test.at }))
			if err != nil {
				t.Fatalf("new read grant verifier: %v", err)
			}
			if _, err := verifier.Verify(mustSignReadGrant(t), lease.Ref,
				readGrantDestination()); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("a grant verified %s: %v", test.name, err)
			}
		})
	}
}

// A grant presented for content or a destination it does not name.
func TestAnOutputReadGrantIsRefusedForAnotherRefOrDestination(t *testing.T) {
	lease := readGrantLease()
	other := lease.Ref
	other.Generation++

	for _, test := range []struct {
		name        string
		ref         hangar.TreeRef
		destination ReadDestination
	}{
		{"another generation", other, readGrantDestination()},
		{"another handle", lease.Ref, ReadDestination{Handle: "other", Volume: "input-0"}},
		{"another volume", lease.Ref, ReadDestination{Handle: "task-handle", Volume: "input-1"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			verifier, err := NewReadGrantVerifier(readGrantKey, insideTheWindow())
			if err != nil {
				t.Fatalf("new read grant verifier: %v", err)
			}
			if _, err := verifier.Verify(mustSignReadGrant(t), test.ref,
				test.destination); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("a grant verified for %s: %v", test.name, err)
			}
		})
	}
}

// Domain separation, with the keys deliberately IDENTICAL.
//
// This is the row that proves the separation is in the signed bytes rather than
// in the key material. A deployment that reused one key would still not be able
// to present a strict input grant as a managed-output read.
func TestAStrictInputGrantDoesNotVerifyAsAnOutputReadGrant(t *testing.T) {
	lease := readGrantLease()

	strict, err := hangar.NewGrantSigner(readGrantKey, hangar.MaxGrantTTL, func() time.Time {
		return lease.GrantedAt.Time
	})
	if err != nil {
		t.Fatalf("new strict-input grant signer: %v", err)
	}
	token, err := strict.Sign(lease.Ref, "task-handle", "input-0")
	if err != nil {
		t.Fatalf("sign strict-input grant: %v", err)
	}

	verifier, err := NewReadGrantVerifier(readGrantKey, insideTheWindow())
	if err != nil {
		t.Fatalf("new read grant verifier: %v", err)
	}
	if _, err := verifier.Verify(token, lease.Ref, readGrantDestination()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("a strict-input materialization grant verified as an output read grant: %v", err)
	}

	// And the other way: the foundation's verifier must not accept an output
	// read grant either, or a managed read would be presentable as an input.
	strictVerifier, err := hangar.NewGrantVerifier(readGrantKey, hangar.MaxGrantTTL, func() time.Time {
		return lease.GrantedAt.Add(time.Minute)
	})
	if err != nil {
		t.Fatalf("new strict-input grant verifier: %v", err)
	}
	if err := strictVerifier.Verify(mustSignReadGrant(t), lease.Ref, "task-handle",
		"input-0"); !errors.Is(err, hangar.ErrUnauthorized) {
		t.Fatalf("an output read grant verified as a strict-input grant: %v", err)
	}
}

// The receipt key cannot become a read grant signer.
func TestTheReceiptKeyCannotSignAnOutputReadGrant(t *testing.T) {
	_, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate an Ed25519 key: %v", err)
	}
	if _, err := NewReadGrantSigner(private); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("an Ed25519 receipt private key was accepted as a read grant key: %v", err)
	}
	// Exact, not a floor: a key with a stray byte on the end is a different key.
	if _, err := NewReadGrantSigner(append(append([]byte{}, readGrantKey...),
		'\n')); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("a 33-byte read grant key was accepted: %v", err)
	}
}

// The replay rule: two mints of one committed lease are the same bytes.
func TestReMintingOneLeasesGrantIsByteIdentical(t *testing.T) {
	first, second := mustSignReadGrant(t), mustSignReadGrant(t)
	if first != second {
		t.Fatal("two mints of one committed lease produced different grants; requirement 37's " +
			"replay would have to create a second lease to be answerable")
	}

	// And a different nonce is a different grant, so the nonce is genuinely
	// inside the signature rather than decorative.
	signer, err := NewReadGrantSigner(readGrantKey)
	if err != nil {
		t.Fatalf("new read grant signer: %v", err)
	}
	other, err := NewReadGrantNonce(strings.NewReader("fedcba9876543210"))
	if err != nil {
		t.Fatalf("new read grant nonce: %v", err)
	}
	token, err := signer.Sign(readGrantLease(), readGrantDestination(), other)
	if err != nil {
		t.Fatalf("sign read grant: %v", err)
	}
	if token == first {
		t.Fatal("a grant minted with another nonce is byte-identical; the nonce is not bound")
	}
}

// A destination is a handle and a volume, never a path.
func TestAReadDestinationIsNeverAPath(t *testing.T) {
	for _, destination := range []ReadDestination{
		{Handle: "../escape", Volume: "input-0"},
		{Handle: "task-handle", Volume: "/absolute"},
		{Handle: "task-handle", Volume: "nested/volume"},
		{Handle: "", Volume: "input-0"},
		{Handle: "task-handle", Volume: ""},
		{Handle: strings.Repeat("a", 129), Volume: "input-0"},
	} {
		if err := destination.Validate(); err == nil {
			t.Errorf("%+v was accepted as a read destination", destination)
		}
	}
	if err := readGrantDestination().Validate(); err != nil {
		t.Errorf("the ordinary destination was refused: %v", err)
	}
}

// hangarStrictInputDomain is the foundation's domain, spelled here so the
// tamper row can put it where the output domain belongs.
const hangarStrictInputDomain = "hangar-materialize-v1"

func editReadGrant(t *testing.T, token string, edit func(*ReadGrantClaims)) string {
	t.Helper()

	var claims ReadGrantClaims
	mac := decodeReadGrantPayload(t, token, &claims)
	edit(&claims)

	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("re-encoding an edited grant: %v", err)
	}

	return encodeReadGrant(append(payload, mac...))
}

// decodeReadGrantPayload splits a grant into its rendered claims and its MAC.
func decodeReadGrantPayload(t *testing.T, token string, claims *ReadGrantClaims) []byte {
	t.Helper()

	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil {
		t.Fatalf("decoding a grant: %v", err)
	}
	if len(raw) <= sha256.Size {
		t.Fatalf("a grant is %d bytes, which is not a payload and a MAC", len(raw))
	}
	payload, mac := raw[:len(raw)-sha256.Size], raw[len(raw)-sha256.Size:]
	if err := json.Unmarshal(payload, claims); err != nil {
		t.Fatalf("decoding a grant's claims: %v", err)
	}

	return mac
}

func encodeReadGrant(raw []byte) string {
	return base64.RawURLEncoding.EncodeToString(raw)
}
