package output

// What an output read warrant binds, and what it refuses.
//
// The control row is FIRST in every table below, because a verifier that
// refused everything would pass a table made only of refusals. Each refusal row
// changes exactly one field of a warrant the control row proved verifies, so a
// red row names the field that is not inside the signature rather than "a warrant
// did not verify".
//
// The two separations that are not tamper rows are the ones that matter most:
// the foundation's strict-input warrant must not verify here even when both keys
// hold the same 32 bytes (the domain is inside the signed bytes), and the
// receipt's Ed25519 key must not be constructible into a read warrant signer at
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

var readWarrantKey = []byte("0123456789abcdef0123456789abcdef")

func readWarrantLease() ReadLease {
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

func readWarrantDestination() ReadDestination {
	return ReadDestination{Handle: "task-handle", Volume: "input-0"}
}

// insideTheWindow is a clock inside the lease's own window, which is the
// warrant's window too.
func insideTheWindow() ClockFunc {
	return func() time.Time { return readWarrantLease().GrantedAt.Add(time.Minute) }
}

func mustSignReadWarrant(t *testing.T) string {
	t.Helper()

	signer, err := NewReadWarrantSigner(readWarrantKey)
	if err != nil {
		t.Fatalf("new read warrant signer: %v", err)
	}
	nonce, err := NewReadWarrantNonce(strings.NewReader("0123456789abcdef"))
	if err != nil {
		t.Fatalf("new read warrant nonce: %v", err)
	}
	token, err := signer.Sign(readWarrantLease(), readWarrantDestination(), nonce)
	if err != nil {
		t.Fatalf("sign read warrant: %v", err)
	}

	return token
}

func TestAnOutputReadWarrantVerifiesForItsOwnLeaseRefAndDestination(t *testing.T) {
	verifier, err := NewReadWarrantVerifier(readWarrantKey, insideTheWindow())
	if err != nil {
		t.Fatalf("new read warrant verifier: %v", err)
	}

	lease := readWarrantLease()
	claims, err := verifier.Verify(mustSignReadWarrant(t), lease.Ref, readWarrantDestination())
	if err != nil {
		t.Fatalf("a warrant minted for this lease did not verify: %v", err)
	}

	if claims.ReadLeaseID != lease.ReadLeaseID {
		t.Errorf("read lease id = %q, want %q", claims.ReadLeaseID, lease.ReadLeaseID)
	}
	if claims.ClaimID != lease.ClaimID {
		t.Errorf("claim id = %q, want %q", claims.ClaimID, lease.ClaimID)
	}
	if claims.ActivationEpoch != lease.ActivationEpoch {
		t.Errorf("activation epoch = %d, want %d", claims.ActivationEpoch, lease.ActivationEpoch)
	}
	if claims.Destination != readWarrantDestination() {
		t.Errorf("destination = %+v, want %+v", claims.Destination, readWarrantDestination())
	}
	if !claims.ExpiresAt.Equal(lease.ExpiresAt.Time) {
		t.Errorf("expiry = %s, want the lease's own %s", claims.ExpiresAt, lease.ExpiresAt)
	}
}

// Every bound field, one row each.
//
// The warrant is decoded, one field is edited, and it is re-encoded with the
// ORIGINAL MAC. That is the shape of the attack the signature exists to stop --
// a token whose body was rewritten in flight -- and it is why each row names a
// field rather than "a corrupt token".
func TestAnEditedOutputReadWarrantDoesNotVerify(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*ReadWarrantClaims)
	}{
		{"read lease id", func(c *ReadWarrantClaims) {
			c.ReadLeaseID = ReadLeaseID("99999999-2e1f-4a0b-9c8d-7e6f5a4b3c2d")
		}},
		{"claim id", func(c *ReadWarrantClaims) {
			c.ClaimID = ClaimID("99999999-5e6f-4a7b-8c9d-0e1f2a3b4c5d")
		}},
		{"ref scope", func(c *ReadWarrantClaims) { c.Ref.Scope = "another-ns" }},
		{"ref digest", func(c *ReadWarrantClaims) {
			c.Ref.Digest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		}},
		{"ref generation", func(c *ReadWarrantClaims) { c.Ref.Generation++ }},
		{"destination handle", func(c *ReadWarrantClaims) { c.Destination.Handle = "other-handle" }},
		{"destination volume", func(c *ReadWarrantClaims) { c.Destination.Volume = "input-9" }},
		{"activation epoch", func(c *ReadWarrantClaims) { c.ActivationEpoch++ }},
		{"issued at", func(c *ReadWarrantClaims) {
			c.IssuedAt = NewTimestamp(c.IssuedAt.Add(-time.Hour))
		}},
		{"expires at", func(c *ReadWarrantClaims) {
			c.ExpiresAt = NewTimestamp(c.ExpiresAt.Add(24 * time.Hour))
		}},
		{"nonce", func(c *ReadWarrantClaims) { c.Nonce = "AAAAAAAAAAAAAAAAAAAAAA" }},
		{"domain", func(c *ReadWarrantClaims) { c.Domain = hangarStrictInputDomain }},
		{"version", func(c *ReadWarrantClaims) { c.Version = "2" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			verifier, err := NewReadWarrantVerifier(readWarrantKey, insideTheWindow())
			if err != nil {
				t.Fatalf("new read warrant verifier: %v", err)
			}

			token := editReadWarrant(t, mustSignReadWarrant(t), test.edit)

			// The rows that move the ref or the destination are asked for
			// under the EDITED value, so a row cannot pass merely because the
			// caller asked for something the warrant never named.
			var edited ReadWarrantClaims
			decodeReadWarrantPayload(t, token, &edited)

			if _, err := verifier.Verify(token, edited.Ref,
				edited.Destination); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("a warrant whose %s was edited verified: %v", test.name, err)
			}
		})
	}
}

// The window is the lease's, and it is checked at both ends.
func TestAnOutputReadWarrantIsRefusedOutsideTheLeaseWindow(t *testing.T) {
	lease := readWarrantLease()

	for _, test := range []struct {
		name string
		at   time.Time
	}{
		{"before the lease was granted", lease.GrantedAt.Add(-time.Second)},
		{"at the moment the lease expires", lease.ExpiresAt.Time},
		{"after the lease expires", lease.ExpiresAt.Add(time.Hour)},
	} {
		t.Run(test.name, func(t *testing.T) {
			verifier, err := NewReadWarrantVerifier(readWarrantKey,
				ClockFunc(func() time.Time { return test.at }))
			if err != nil {
				t.Fatalf("new read warrant verifier: %v", err)
			}
			if _, err := verifier.Verify(mustSignReadWarrant(t), lease.Ref,
				readWarrantDestination()); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("a warrant verified %s: %v", test.name, err)
			}
		})
	}
}

// A warrant presented for content or a destination it does not name.
func TestAnOutputReadWarrantIsRefusedForAnotherRefOrDestination(t *testing.T) {
	lease := readWarrantLease()
	other := lease.Ref
	other.Generation++

	for _, test := range []struct {
		name        string
		ref         hangar.TreeRef
		destination ReadDestination
	}{
		{"another generation", other, readWarrantDestination()},
		{"another handle", lease.Ref, ReadDestination{Handle: "other", Volume: "input-0"}},
		{"another volume", lease.Ref, ReadDestination{Handle: "task-handle", Volume: "input-1"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			verifier, err := NewReadWarrantVerifier(readWarrantKey, insideTheWindow())
			if err != nil {
				t.Fatalf("new read warrant verifier: %v", err)
			}
			if _, err := verifier.Verify(mustSignReadWarrant(t), test.ref,
				test.destination); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("a warrant verified for %s: %v", test.name, err)
			}
		})
	}
}

// Domain separation, with the keys deliberately IDENTICAL.
//
// This is the row that proves the separation is in the signed bytes rather than
// in the key material. A deployment that reused one key would still not be able
// to present a strict input warrant as a managed-output read.
func TestAStrictInputWarrantDoesNotVerifyAsAnOutputReadWarrant(t *testing.T) {
	lease := readWarrantLease()

	strict, err := hangar.NewWarrantSigner(readWarrantKey, hangar.MaxWarrantTTL, func() time.Time {
		return lease.GrantedAt.Time
	})
	if err != nil {
		t.Fatalf("new strict-input warrant signer: %v", err)
	}
	token, err := strict.Sign(lease.Ref, "task-handle", "input-0")
	if err != nil {
		t.Fatalf("sign strict-input warrant: %v", err)
	}

	verifier, err := NewReadWarrantVerifier(readWarrantKey, insideTheWindow())
	if err != nil {
		t.Fatalf("new read warrant verifier: %v", err)
	}
	if _, err := verifier.Verify(token, lease.Ref, readWarrantDestination()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("a strict-input materialization warrant verified as an output read warrant: %v", err)
	}

	// And the other way: the foundation's verifier must not accept an output
	// read warrant either, or a managed read would be presentable as an input.
	strictVerifier, err := hangar.NewWarrantVerifier(readWarrantKey, hangar.MaxWarrantTTL, func() time.Time {
		return lease.GrantedAt.Add(time.Minute)
	})
	if err != nil {
		t.Fatalf("new strict-input warrant verifier: %v", err)
	}
	if err := strictVerifier.Verify(mustSignReadWarrant(t), lease.Ref, "task-handle",
		"input-0"); !errors.Is(err, hangar.ErrUnauthorized) {
		t.Fatalf("an output read warrant verified as a strict-input warrant: %v", err)
	}
}

// The receipt key cannot become a read warrant signer.
func TestTheReceiptKeyCannotSignAnOutputReadWarrant(t *testing.T) {
	_, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate an Ed25519 key: %v", err)
	}
	if _, err := NewReadWarrantSigner(private); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("an Ed25519 receipt private key was accepted as a read warrant key: %v", err)
	}
	// Exact, not a floor: a key with a stray byte on the end is a different key.
	if _, err := NewReadWarrantSigner(append(append([]byte{}, readWarrantKey...),
		'\n')); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("a 33-byte read warrant key was accepted: %v", err)
	}
}

// The replay rule: two mints of one committed lease are the same bytes.
func TestReMintingOneLeasesWarrantIsByteIdentical(t *testing.T) {
	first, second := mustSignReadWarrant(t), mustSignReadWarrant(t)
	if first != second {
		t.Fatal("two mints of one committed lease produced different warrants; requirement 37's " +
			"replay would have to create a second lease to be answerable")
	}

	// And a different nonce is a different warrant, so the nonce is genuinely
	// inside the signature rather than decorative.
	signer, err := NewReadWarrantSigner(readWarrantKey)
	if err != nil {
		t.Fatalf("new read warrant signer: %v", err)
	}
	other, err := NewReadWarrantNonce(strings.NewReader("fedcba9876543210"))
	if err != nil {
		t.Fatalf("new read warrant nonce: %v", err)
	}
	token, err := signer.Sign(readWarrantLease(), readWarrantDestination(), other)
	if err != nil {
		t.Fatalf("sign read warrant: %v", err)
	}
	if token == first {
		t.Fatal("a warrant minted with another nonce is byte-identical; the nonce is not bound")
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
	if err := readWarrantDestination().Validate(); err != nil {
		t.Errorf("the ordinary destination was refused: %v", err)
	}
}

// hangarStrictInputDomain is the foundation's domain, spelled here so the
// tamper row can put it where the output domain belongs.
const hangarStrictInputDomain = "hangar-materialize-v1"

func editReadWarrant(t *testing.T, token string, edit func(*ReadWarrantClaims)) string {
	t.Helper()

	var claims ReadWarrantClaims
	mac := decodeReadWarrantPayload(t, token, &claims)
	edit(&claims)

	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("re-encoding an edited warrant: %v", err)
	}

	return encodeReadWarrant(append(payload, mac...))
}

// decodeReadWarrantPayload splits a warrant into its rendered claims and its MAC.
func decodeReadWarrantPayload(t *testing.T, token string, claims *ReadWarrantClaims) []byte {
	t.Helper()

	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil {
		t.Fatalf("decoding a warrant: %v", err)
	}
	if len(raw) <= sha256.Size {
		t.Fatalf("a warrant is %d bytes, which is not a payload and a MAC", len(raw))
	}
	payload, mac := raw[:len(raw)-sha256.Size], raw[len(raw)-sha256.Size:]
	if err := json.Unmarshal(payload, claims); err != nil {
		t.Fatalf("decoding a warrant's claims: %v", err)
	}

	return mac
}

func encodeReadWarrant(raw []byte) string {
	return base64.RawURLEncoding.EncodeToString(raw)
}
