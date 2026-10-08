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
// a 64-byte Ed25519 private key must not be constructible into a read warrant signer at
// all (it is not 32 bytes, and the length check is exact rather than a floor).

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar"
)

var readWarrantKey = []byte("0123456789abcdef0123456789abcdef")

// readWarrantClaim is the committed reader's claim every warrant below is
// minted over: an expiring hold on one registered generation.
func readWarrantClaim() ClaimRecord {
	acquired := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	expires := NewTimestamp(acquired.Add(20 * time.Minute))

	return ClaimRecord{
		ClaimID: ClaimID("1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"),
		Ref: hangar.TreeRef{
			Scope:      "deployment-ns",
			Digest:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Generation: 7,
		},
		ConsumerBindingID: "result-read:task-handle",
		AcquiredAt:        NewTimestamp(acquired),
		ExpiresAt:         &expires,
	}
}

func readWarrantDestination() ReadDestination {
	return ReadDestination{Handle: "task-handle", Volume: "input-0"}
}

// insideTheWindow is a clock inside the claim's own window, which is the
// warrant's window too.
func insideTheWindow() ClockFunc {
	return func() time.Time { return readWarrantClaim().AcquiredAt.Add(time.Minute) }
}

func mustSignReadWarrant(t *testing.T) string {
	t.Helper()

	signer, err := NewReadWarrantSigner(readWarrantKey)
	if err != nil {
		t.Fatalf("new read warrant signer: %v", err)
	}
	token, err := signer.Sign(readWarrantClaim(), readWarrantDestination(), "node-1")
	if err != nil {
		t.Fatalf("sign read warrant: %v", err)
	}

	return token
}

func TestAnOutputReadWarrantVerifiesForItsOwnClaimRefAndDestination(t *testing.T) {
	verifier, err := NewReadWarrantVerifier(readWarrantKey, insideTheWindow())
	if err != nil {
		t.Fatalf("new read warrant verifier: %v", err)
	}

	claim := readWarrantClaim()
	claims, err := verifier.Verify(mustSignReadWarrant(t), claim.Ref, readWarrantDestination())
	if err != nil {
		t.Fatalf("a warrant minted for this claim did not verify: %v", err)
	}

	if claims.ClaimID != claim.ClaimID {
		t.Errorf("claim id = %q, want %q", claims.ClaimID, claim.ClaimID)
	}
	if claims.NodeUID != "node-1" {
		t.Errorf("node = %q, want node-1", claims.NodeUID)
	}
	if claims.Destination != readWarrantDestination() {
		t.Errorf("destination = %+v, want %+v", claims.Destination, readWarrantDestination())
	}
	// The window is the claim's: issued when it was acquired, expiring when
	// it lapses. Nothing in the token comes from the instant it was minted.
	if !claims.IssuedAt.Equal(claim.AcquiredAt.Time) {
		t.Errorf("issued at = %s, want the claim's own acquired-at %s", claims.IssuedAt, claim.AcquiredAt)
	}
	if !claims.ExpiresAt.Equal(claim.ExpiresAt.Time) {
		t.Errorf("expiry = %s, want the claim's own %s", claims.ExpiresAt, claim.ExpiresAt)
	}
}

// Sign takes the committed claim, and refuses one a warrant cannot be minted
// over: a consumer's hold has no window, and a released claim is no hold.
func TestSignRefusesAClaimThatIsNotALiveReadersHold(t *testing.T) {
	signer, err := NewReadWarrantSigner(readWarrantKey)
	if err != nil {
		t.Fatalf("new read warrant signer: %v", err)
	}

	consumers := readWarrantClaim()
	consumers.ExpiresAt = nil
	if _, err := signer.Sign(consumers, readWarrantDestination(), "node-1"); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("a consumer's claim, with no expiry, was signed into a read warrant: %v", err)
	}

	released := readWarrantClaim()
	at := NewTimestamp(released.AcquiredAt.Add(time.Minute))
	released.ReleasedAt = &at
	if _, err := signer.Sign(released, readWarrantDestination(), "node-1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a released claim was signed into a read warrant: %v", err)
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
		{"node", func(c *ReadWarrantClaims) { c.NodeUID = "another-node" }},
		{"issued at", func(c *ReadWarrantClaims) {
			c.IssuedAt = NewTimestamp(c.IssuedAt.Add(-time.Hour))
		}},
		{"expires at", func(c *ReadWarrantClaims) {
			c.ExpiresAt = NewTimestamp(c.ExpiresAt.Add(24 * time.Hour))
		}},
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

// The window is the claim's, and it is checked at both ends.
func TestAnOutputReadWarrantIsRefusedOutsideTheClaimWindow(t *testing.T) {
	claim := readWarrantClaim()

	for _, test := range []struct {
		name string
		at   time.Time
	}{
		{"before the claim was acquired", claim.AcquiredAt.Add(-time.Second)},
		{"at the moment the claim expires", claim.ExpiresAt.Time},
		{"after the claim expires", claim.ExpiresAt.Add(time.Hour)},
	} {
		t.Run(test.name, func(t *testing.T) {
			verifier, err := NewReadWarrantVerifier(readWarrantKey,
				ClockFunc(func() time.Time { return test.at }))
			if err != nil {
				t.Fatalf("new read warrant verifier: %v", err)
			}
			if _, err := verifier.Verify(mustSignReadWarrant(t), claim.Ref,
				readWarrantDestination()); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("a warrant verified %s: %v", test.name, err)
			}
			// The binding alone is true forever: whether the claim is still
			// a live hold is the database's question, on its own clock.
			if _, err := verifier.VerifyBinding(mustSignReadWarrant(t), claim.Ref,
				readWarrantDestination()); err != nil {
				t.Fatalf("the binding did not verify %s: %v", test.name, err)
			}
		})
	}
}

// A warrant presented for content or a destination it does not name.
func TestAnOutputReadWarrantIsRefusedForAnotherRefOrDestination(t *testing.T) {
	claim := readWarrantClaim()
	other := claim.Ref
	other.Generation++

	for _, test := range []struct {
		name        string
		ref         hangar.TreeRef
		destination ReadDestination
	}{
		{"another generation", other, readWarrantDestination()},
		{"another handle", claim.Ref, ReadDestination{Handle: "other", Volume: "input-0"}},
		{"another volume", claim.Ref, ReadDestination{Handle: "task-handle", Volume: "input-1"}},
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
			if _, err := verifier.VerifyBinding(mustSignReadWarrant(t), test.ref,
				test.destination); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("a warrant's binding verified for %s: %v", test.name, err)
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
	claim := readWarrantClaim()

	strict, err := hangar.NewWarrantSigner(readWarrantKey, hangar.MaxWarrantTTL, func() time.Time {
		return claim.AcquiredAt.Time
	})
	if err != nil {
		t.Fatalf("new strict-input warrant signer: %v", err)
	}
	token, err := strict.Sign(claim.Ref, "task-handle", "input-0")
	if err != nil {
		t.Fatalf("sign strict-input warrant: %v", err)
	}

	verifier, err := NewReadWarrantVerifier(readWarrantKey, insideTheWindow())
	if err != nil {
		t.Fatalf("new read warrant verifier: %v", err)
	}
	if _, err := verifier.Verify(token, claim.Ref, readWarrantDestination()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("a strict-input materialization warrant verified as an output read warrant: %v", err)
	}

	// And the other way: the foundation's verifier must not accept an output
	// read warrant either, or a managed read would be presentable as an input.
	strictVerifier, err := hangar.NewWarrantVerifier(readWarrantKey, hangar.MaxWarrantTTL, func() time.Time {
		return claim.AcquiredAt.Add(time.Minute)
	})
	if err != nil {
		t.Fatalf("new strict-input warrant verifier: %v", err)
	}
	if err := strictVerifier.Verify(mustSignReadWarrant(t), claim.Ref, "task-handle",
		"input-0"); !errors.Is(err, hangar.ErrUnauthorized) {
		t.Fatalf("an output read warrant verified as a strict-input warrant: %v", err)
	}
}

// An asymmetric key cannot become a read warrant signer: the warrant key is
// exactly 32 bytes, not whatever happens to be lying around.
func TestAnEd25519KeyCannotSignAnOutputReadWarrant(t *testing.T) {
	_, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate an Ed25519 key: %v", err)
	}
	if _, err := NewReadWarrantSigner(private); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("an Ed25519 private key was accepted as a read warrant key: %v", err)
	}
	// Exact, not a floor: a key with a stray byte on the end is a different key.
	if _, err := NewReadWarrantSigner(append(append([]byte{}, readWarrantKey...),
		'\n')); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("a 33-byte read warrant key was accepted: %v", err)
	}
}

// The replay rule: two mints of one committed claim are the same bytes, so a
// mint whose commit answer was lost is answered by acquiring the same claim
// again and minting again, never by a second claim.
func TestReMintingOneClaimsWarrantIsByteIdentical(t *testing.T) {
	first, second := mustSignReadWarrant(t), mustSignReadWarrant(t)
	if first != second {
		t.Fatal("two mints of one committed claim produced different warrants; a lost commit " +
			"answer would have to create a second claim to be answerable")
	}
}

// The canonical bytes carry no identity but the claim's: no lease id and no
// nonce, so nothing in the signature comes from the instant of minting.
func TestTheCanonicalReadWarrantBytesCarryNoLeaseIDOrNonce(t *testing.T) {
	claim := readWarrantClaim()
	claims := WarrantClaimsFor(claim, readWarrantDestination(), "node-1")

	canonical, err := CanonicalReadWarrantBytes(claims)
	if err != nil {
		t.Fatalf("canonical read warrant bytes: %v", err)
	}

	fields := strings.Split(strings.TrimSuffix(string(canonical), "|"), "|")
	want := []string{
		MaterializeDomain,
		readWarrantVersion,
		string(claim.ClaimID),
		string(claim.Ref.Scope),
		string(claim.Ref.Digest),
		"7",
		"task-handle",
		"input-0",
		"node-1",
		claim.AcquiredAt.UTC().Format(time.RFC3339Nano),
		claim.ExpiresAt.UTC().Format(time.RFC3339Nano),
	}
	if len(fields) != len(want) {
		t.Fatalf("the canonical form has %d fields, want %d: %q", len(fields), len(want), fields)
	}
	for index, field := range fields {
		length, value, found := strings.Cut(field, ":")
		if !found || length != strconv.Itoa(len(value)) || value != want[index] {
			t.Errorf("canonical field %d = %q, want %d:%s", index, field, len(want[index]), want[index])
		}
	}

	rendered, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("rendering the claims: %v", err)
	}
	for _, absent := range []string{"read_lease_id", "nonce", "lease"} {
		if strings.Contains(string(rendered), absent) {
			t.Errorf("the rendered warrant carries %q: %s", absent, rendered)
		}
	}
}

// The destination-segment rule is the foundation's rule, restated: whatever
// the strict-input signer refuses as a handle or a volume, a read destination
// refuses too, and whatever it accepts, a read destination accepts.
func TestAReadDestinationSegmentIsTheFoundationsPathSegmentRule(t *testing.T) {
	signer, err := hangar.NewWarrantSigner(readWarrantKey, hangar.MaxWarrantTTL, func() time.Time {
		return readWarrantClaim().AcquiredAt.Time
	})
	if err != nil {
		t.Fatalf("new strict-input warrant signer: %v", err)
	}
	ref := readWarrantClaim().Ref

	for _, segment := range []string{
		"task-handle", "input-0", "a", "A.b_c-d9", strings.Repeat("a", 128),
		"", ".", "..", "../escape", "/absolute", "nested/volume", "-leading", "trailing.",
		"with space", strings.Repeat("a", 129), "ünïcode",
	} {
		_, foundationErr := signer.Sign(ref, segment, "input-0")
		if accepted := validDestinationSegment(segment); accepted != (foundationErr == nil) {
			t.Errorf("segment %q: read destination accepts = %t, the foundation's strict-input "+
				"signer accepts = %t (%v); both must be one rule", segment, accepted,
				foundationErr == nil, foundationErr)
		}
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
