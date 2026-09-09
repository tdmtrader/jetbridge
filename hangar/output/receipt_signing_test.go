package output

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
)

// The receipt tables.
//
// Every case here is a pure function over bytes and a pinned key, which is why
// they are Go and not scenarios: the tamper vectors are one mutation of one
// field each, and the challenge cases need a clock that can be moved, which no
// scenario runner has.

var receiptInstant = time.Date(2026, 5, 6, 7, 8, 9, 123456789, time.UTC)

type testClock struct{ at time.Time }

func (clock *testClock) Now() time.Time { return clock.at }

func (clock *testClock) advance(by time.Duration) { clock.at = clock.at.Add(by) }

func keyPair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()

	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating a key pair: %v", err)
	}

	return public, private
}

func sampleClaims() ReceiptClaims {
	ref := hangar.TreeRef{
		Scope:      "o0123456789abcdef0123456789abcdef01234567",
		Digest:     hangar.Digest("sha256:" + strings.Repeat("ab", 32)),
		Generation: 1725830823000001,
	}

	return ReceiptClaims{
		ProtocolVersion: ProtocolVersion,
		ReceiptVersion:  ReceiptDomain,
		Execution: executioncontrol.Identity{
			ExecutionID: "33333333-3333-4333-8333-333333333333",
			Fence:       2,
		},
		ActivationEpoch:      7,
		HandoffID:            "11111111-1111-4111-8111-111111111111",
		ProducerCheckpointID: "opaque-checkpoint",
		ReservationID:        "44444444-4444-4444-8444-444444444444",
		ChallengeNonce:       "nonce-0123456789abcdef",
		ChallengeIssuedAt:    NewTimestamp(receiptInstant),
		Incarnation: SourceIncarnation{
			ExecutionID:      "33333333-3333-4333-8333-333333333333",
			NodeUID:          "node-1",
			HandleGeneration: 3,
			Output:           "result",
		},
		Output:        "result",
		CaptureFence:  5,
		WriterFence:   9,
		Ref:           ref,
		Attributes:    TreeAttributes{Ref: ref, StoredBytes: 2048, LogicalBytes: 4096, CreatedAt: NewTimestamp(receiptInstant)},
		MarkerVersion: MarkerVersion,
		SignedAt:      NewTimestamp(receiptInstant),
	}
}

// sampleChallenge is the challenge the claims say they answer. The nonce and
// the issued-at come from the claims rather than being restated here, because a
// verifier's whole job is to compare the two and a fixture that spelled them
// twice would be comparing this file with itself.
func sampleChallenge(claims ReceiptClaims, notAfter time.Time) StatChallenge {
	return StatChallenge{
		Nonce:           claims.ChallengeNonce,
		HandoffID:       claims.HandoffID,
		ReservationID:   claims.ReservationID,
		ActivationEpoch: claims.ActivationEpoch,
		Ref:             claims.Ref,
		CaptureFence:    claims.CaptureFence,
		IssuedAt:        claims.ChallengeIssuedAt,
		NotAfter:        NewTimestamp(notAfter),
	}
}

// signed builds a signer, a matching verifier and one valid receipt, so that
// every negative case below is one deviation from a green.
func signed(t *testing.T) (*ReceiptSigner, *ReceiptSignatureVerifier, Receipt, StatChallenge, *testClock) {
	signer, verifier, receipt, challenge, clock, _ := signedWithKey(t)

	return signer, verifier, receipt, challenge, clock
}

func signedWithKey(t *testing.T) (*ReceiptSigner, *ReceiptSignatureVerifier, Receipt, StatChallenge, *testClock, ed25519.PublicKey) {
	t.Helper()

	public, private := keyPair(t)
	clock := &testClock{at: receiptInstant}

	signer, err := NewReceiptSigner("receipt-key-1", 7, private, clock)
	if err != nil {
		t.Fatalf("building the signer: %v", err)
	}

	ring, err := NewReceiptKeyRing(EpochKey{
		KeyID:      "receipt-key-1",
		Epoch:      7,
		PublicKey:  public,
		ValidFrom:  NewTimestamp(receiptInstant.Add(-24 * time.Hour)),
		ValidUntil: NewTimestamp(receiptInstant.Add(720 * time.Hour)),
	})
	if err != nil {
		t.Fatalf("pinning the key ring: %v", err)
	}
	verifier, err := NewReceiptSignatureVerifier(ring, clock)
	if err != nil {
		t.Fatalf("building the verifier: %v", err)
	}

	receipt, err := signer.Sign(sampleClaims())
	if err != nil {
		t.Fatalf("signing: %v", err)
	}

	return signer, verifier, receipt, sampleChallenge(receipt.Claims, receiptInstant.Add(5*time.Minute)), clock, public
}

func TestAReceiptVerifiesUnderTheActivationPinnedKeyAndUnderNoOther(t *testing.T) {
	_, verifier, receipt, challenge, _ := signed(t)

	// The control, first.
	if err := verifier.Verify(receipt, challenge); err != nil {
		t.Fatalf("a receipt this cohort signed does not verify: %v", err)
	}
	if receipt.Algorithm != ReceiptAlgorithm {
		t.Errorf("the receipt names algorithm %q", receipt.Algorithm)
	}
	if receipt.KeyID != "receipt-key-1" {
		t.Errorf("the receipt names key %q", receipt.KeyID)
	}

	// Another key entirely: a well-formed receipt signed by somebody else.
	otherPublic, otherPrivate := keyPair(t)
	clock := &testClock{at: receiptInstant}
	impostor, err := NewReceiptSigner("receipt-key-1", 7, otherPrivate, clock)
	if err != nil {
		t.Fatalf("building the impostor signer: %v", err)
	}
	forged, err := impostor.Sign(sampleClaims())
	if err != nil {
		t.Fatalf("signing with the other key: %v", err)
	}
	if err := verifier.Verify(forged, challenge); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("a receipt signed by another private key verified as %v, expected ErrUnauthorized", err)
	}

	// And a public key nobody pinned, which is the same statement from the
	// other side: the ring is the authority, not the receipt.
	strangerRing, err := NewReceiptKeyRing(EpochKey{
		KeyID:      "receipt-key-9",
		Epoch:      7,
		PublicKey:  otherPublic,
		ValidFrom:  NewTimestamp(receiptInstant.Add(-time.Hour)),
		ValidUntil: NewTimestamp(receiptInstant.Add(time.Hour)),
	})
	if err != nil {
		t.Fatalf("pinning the stranger ring: %v", err)
	}
	strangerVerifier, err := NewReceiptSignatureVerifier(strangerRing, clock)
	if err != nil {
		t.Fatalf("building the stranger verifier: %v", err)
	}
	if err := strangerVerifier.Verify(receipt, challenge); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("a receipt whose key id is not pinned verified as %v", err)
	}
}

func TestEverySignedFieldIsCoveredBySignature(t *testing.T) {
	// The tamper table. Each row alters one signed claim on an otherwise valid
	// receipt, leaving the signature alone; every one must fail. A field that
	// survives its row is a field the signature does not cover, which is the
	// defect this table exists for -- and the plan names exactly one of them
	// as its Reddened by: mutation, Generation.
	for field, tamper := range map[string]func(*ReceiptClaims){
		"protocol version": func(c *ReceiptClaims) { c.ProtocolVersion = "hangar-output-v0" },
		"receipt version":  func(c *ReceiptClaims) { c.ReceiptVersion = "hangar-output-receipt-v0" },
		// Both execution ids move together, because Validate requires them
		// equal: a row that moved one alone would be refused by Validate and
		// never reach the signature check at all, so it would pass whether the
		// signature covered the field or not.
		"execution id": func(c *ReceiptClaims) {
			c.Execution.ExecutionID = "99999999-9999-4999-8999-999999999999"
			c.Incarnation.ExecutionID = "99999999-9999-4999-8999-999999999999"
		},
		"execution fence":        func(c *ReceiptClaims) { c.Execution.Fence = 3 },
		"activation epoch":       func(c *ReceiptClaims) { c.ActivationEpoch = 8 },
		"handoff id":             func(c *ReceiptClaims) { c.HandoffID = "22222222-2222-4222-8222-222222222222" },
		"producer checkpoint":    func(c *ReceiptClaims) { c.ProducerCheckpointID = "another-checkpoint" },
		"reservation id":         func(c *ReceiptClaims) { c.ReservationID = "55555555-5555-4555-8555-555555555555" },
		"incarnation node uid":   func(c *ReceiptClaims) { c.Incarnation.NodeUID = "node-2" },
		"incarnation generation": func(c *ReceiptClaims) { c.Incarnation.HandleGeneration = 4 },
		// Validate ties Output to Incarnation.Output, so a row that moved only
		// one of them would be refused before the signature was ever checked.
		"output": func(c *ReceiptClaims) {
			c.Output = "another-output"
			c.Incarnation.Output = "another-output"
		},
		"capture fence": func(c *ReceiptClaims) { c.CaptureFence = 6 },
		"writer fence":  func(c *ReceiptClaims) { c.WriterFence = 10 },
		"ref scope": func(c *ReceiptClaims) {
			c.Ref.Scope = "o9999999999999999999999999999999999999999"
			c.Attributes.Ref = c.Ref
		},
		"ref digest": func(c *ReceiptClaims) {
			c.Ref.Digest = hangar.Digest("sha256:" + strings.Repeat("cd", 32))
			c.Attributes.Ref = c.Ref
		},
		"ref generation": func(c *ReceiptClaims) {
			c.Ref.Generation = 1725830823000002
			c.Attributes.Ref = c.Ref
		},
		"stored bytes":  func(c *ReceiptClaims) { c.Attributes.StoredBytes = 4096 },
		"logical bytes": func(c *ReceiptClaims) { c.Attributes.LogicalBytes = 8192 },
		"created at":    func(c *ReceiptClaims) { c.Attributes.CreatedAt = NewTimestamp(receiptInstant.Add(time.Second)) },
		"signed at":     func(c *ReceiptClaims) { c.SignedAt = NewTimestamp(receiptInstant.Add(time.Second)) },
	} {
		_, verifier, receipt, challenge, _, public := signedWithKey(t)

		tampered := receipt
		claims := receipt.Claims
		tamper(&claims)
		tampered.Claims = claims

		// The signature itself, not the whole of Verify. Verify also binds the
		// claims to the challenge, and a binding check would pass this row for
		// a field the signature does not cover at all -- which is exactly the
		// hole the plan's Reddened by: mutation opens by dropping Generation
		// from the canonical form.
		canonical, err := CanonicalReceiptBytes(tampered.Claims, tampered.KeyID)
		if err == nil {
			signature, decodeErr := base64.StdEncoding.DecodeString(tampered.Signature)
			if decodeErr != nil {
				t.Fatalf("decoding the signature: %v", decodeErr)
			}
			if ed25519.Verify(public, canonical, signature) {
				t.Errorf("the signature still checks out after altering the %s. Every fact a "+
					"later verifier must not have to trust the caller for is inside the "+
					"signature; a field that survives this row is a field an attacker may "+
					"choose while keeping a valid signature", field)
			}
		}

		if err := verifier.Verify(tampered, challenge); err == nil {
			t.Errorf("altering the %s left the receipt verifiable", field)
		}
	}

	// The key id is signed too, so a receipt cannot be re-pointed at another
	// pinned key.
	//
	// Verify alone cannot say that. It fails on the ring lookup for an id this
	// deployment never pinned, whatever the signature covers -- so the
	// assertion is over the canonical bytes directly: the signature this key
	// made must not check out under any other key id.
	_, verifier, receipt, challenge, _, public := signedWithKey(t)
	repointed := receipt
	repointed.KeyID = "receipt-key-2"
	if err := verifier.Verify(repointed, challenge); err == nil {
		t.Error("a receipt whose key id was edited still verified")
	}

	repointedBytes, err := CanonicalReceiptBytes(receipt.Claims, "receipt-key-2")
	if err != nil {
		t.Fatalf("canonicalizing under another key id: %v", err)
	}
	original, err := base64.StdEncoding.DecodeString(receipt.Signature)
	if err != nil {
		t.Fatalf("decoding the signature: %v", err)
	}
	if ed25519.Verify(public, repointedBytes, original) {
		t.Error("the signature checks out under another key id, so the key id is outside it. A " +
			"receipt whose key id could be edited without breaking the signature would let a " +
			"verifier be pointed at a key of the attacker's choosing")
	}

	// And the signature itself.
	flipped := receipt
	raw := append([]byte(nil), original...)
	raw[0] ^= 0x01
	flipped.Signature = base64.StdEncoding.EncodeToString(raw)
	if err := verifier.Verify(flipped, challenge); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("a receipt with one flipped signature bit verified as %v", err)
	}
}

func TestTheReceiptDomainIsSeparateFromTheMaterializationDomain(t *testing.T) {
	if ReceiptDomain == MaterializeDomain {
		t.Fatal("the receipt and materialization domains are the same string. A receipt says " +
			"these bytes were published and a grant says you may read them; one domain means " +
			"one authority")
	}

	claims := sampleClaims()
	canonical, err := CanonicalReceiptBytes(claims, "receipt-key-1")
	if err != nil {
		t.Fatalf("canonicalizing: %v", err)
	}
	if !strings.HasPrefix(string(canonical), "24:hangar-output-receipt-v1|") {
		t.Errorf("the canonical form does not begin with the length-prefixed receipt domain: %q",
			canonical[:40])
	}
	if strings.Contains(string(canonical), MaterializeDomain) {
		t.Error("the canonical receipt contains the materialization domain")
	}
}

func TestTheCanonicalFormIsLengthPrefixedAndBounded(t *testing.T) {
	// Two different claim sets must not canonicalize to the same bytes by
	// moving a delimiter from one field into the next. This is what the length
	// prefixes buy, and a table with a shared suffix is how it is checked.
	first := sampleClaims()
	first.ProducerCheckpointID = OpaqueID("ab")
	first.Output = "cd"
	first.Incarnation.Output = "cd"

	second := sampleClaims()
	second.ProducerCheckpointID = OpaqueID("a")
	second.Output = "bcd"
	second.Incarnation.Output = "bcd"

	firstBytes, err := CanonicalReceiptBytes(first, "receipt-key-1")
	if err != nil {
		t.Fatalf("canonicalizing the first: %v", err)
	}
	secondBytes, err := CanonicalReceiptBytes(second, "receipt-key-1")
	if err != nil {
		t.Fatalf("canonicalizing the second: %v", err)
	}
	if string(firstBytes) == string(secondBytes) {
		t.Error("two different claim sets canonicalized to the same bytes. Without a length " +
			"prefix per field, one signature covers both, and an attacker chooses which")
	}

	// The bound. An unbounded canonical form is an unbounded allocation on the
	// verifying side, before any signature has been checked.
	oversized := sampleClaims()
	oversized.ProducerCheckpointID = OpaqueID(strings.Repeat("x", MaxOpaqueIDBytes))
	if _, err := CanonicalReceiptBytes(oversized, strings.Repeat("k", MaxKeyIDBytes+1)); !errors.Is(err, ErrLimitExceeded) {
		t.Errorf("an over-long key id canonicalized as %v, expected ErrLimitExceeded", err)
	}
	if _, err := CanonicalReceiptBytes(sampleClaims(), ""); !errors.Is(err, ErrIncomplete) {
		t.Errorf("canonicalizing under no key id returned %v", err)
	}
}

func TestAChallengeIsOneUseBoundAndTimeBounded(t *testing.T) {
	_, verifier, receipt, challenge, clock := signed(t)

	// Control.
	if err := verifier.Verify(receipt, challenge); err != nil {
		t.Fatalf("the control did not verify: %v", err)
	}

	// Expired. The five-minute window is the caller's; what this asserts is
	// that the verifier reads the clock it was given rather than the wall.
	clock.advance(5*time.Minute + time.Second)
	if err := verifier.Verify(receipt, challenge); !errors.Is(err, ErrTimeout) {
		t.Errorf("a receipt checked after the challenge's not-after returned %v, expected ErrTimeout", err)
	}
	clock.at = receiptInstant

	// Wrong: each bound fact, one at a time, so a failure names which binding
	// was not enforced.
	for name, mutate := range map[string]func(*StatChallenge){
		"another handoff":     func(c *StatChallenge) { c.HandoffID = "22222222-2222-4222-8222-222222222222" },
		"another reservation": func(c *StatChallenge) { c.ReservationID = "55555555-5555-4555-8555-555555555555" },
		"another epoch":       func(c *StatChallenge) { c.ActivationEpoch = 8 },
		"another fence":       func(c *StatChallenge) { c.CaptureFence = 6 },
		"another generation":  func(c *StatChallenge) { c.Ref.Generation = 1725830823000002 },
		"another digest": func(c *StatChallenge) {
			c.Ref.Digest = hangar.Digest("sha256:" + strings.Repeat("cd", 32))
		},
	} {
		mutated := challenge
		mutate(&mutated)
		if err := verifier.Verify(receipt, mutated); !errors.Is(err, ErrConflict) {
			t.Errorf("a receipt checked against a challenge for %s returned %v, expected "+
				"ErrConflict", name, err)
		}
	}

	// A missing challenge is refused as incomplete rather than accepted as
	// "no constraint".
	if err := verifier.Verify(receipt, StatChallenge{}); err == nil {
		t.Error("a receipt verified against no challenge at all")
	}
}

func TestAReceiptAnswersTheChallengeItWasSignedAgainstAndNoOther(t *testing.T) {
	// The facts a receipt binds were all true once. What makes them true *now*
	// is the challenge: a one-use nonce the verifier issued, with an instant the
	// stat has to post-date. A receipt bound to the facts alone answers every
	// later challenge for the same facts, which is the replay Req 25 forbids and
	// which the DB's one-use row cannot catch, because it consumes a nonce the
	// receipt never named.
	signer, verifier, receipt, challenge, clock := signed(t)

	if err := verifier.Verify(receipt, challenge); err != nil {
		t.Fatalf("the control did not verify: %v", err)
	}

	// Another nonce for the same facts. Every bound field agrees; only the
	// challenge is a different one.
	another := challenge
	another.Nonce = "nonce-fedcba9876543210"
	if err := verifier.Verify(receipt, another); !errors.Is(err, ErrConflict) {
		t.Errorf("a receipt signed against one challenge answered another with the same facts: "+
			"%v, expected ErrConflict", err)
	}

	// And the same nonce twice. One use is the whole point of a nonce.
	if err := verifier.Verify(receipt, challenge); !errors.Is(err, ErrConflict) {
		t.Errorf("a challenge nonce was accepted twice: %v, expected ErrConflict", err)
	}

	// A stat that predates the challenge. The receipt is signed at the daemon's
	// clock and the challenge is issued at the database's, so a receipt signed
	// before the challenge existed cannot be the fresh observation the challenge
	// demanded, whatever its facts say. Every other bound fact agrees, and the
	// nonce agrees too, so this row reaches the freshness check and nothing else.
	backdated := sampleClaims()
	backdated.ChallengeNonce = "nonce-0f0f0f0f0f0f0f0f"
	backdated.ChallengeIssuedAt = NewTimestamp(receiptInstant.Add(time.Minute))
	staleReceipt, err := signer.Sign(backdated)
	if err != nil {
		t.Fatalf("signing the backdated capture: %v", err)
	}
	if !staleReceipt.Claims.SignedAt.Before(staleReceipt.Claims.ChallengeIssuedAt.Time) {
		t.Fatalf("the fixture does not predate its challenge: signed %s, issued %s",
			staleReceipt.Claims.SignedAt.UTC(), staleReceipt.Claims.ChallengeIssuedAt.UTC())
	}
	stale := sampleChallenge(staleReceipt.Claims, receiptInstant.Add(5*time.Minute))
	if err := verifier.Verify(staleReceipt, stale); !errors.Is(err, ErrConflict) {
		t.Errorf("a receipt signed before its challenge was issued verified as %v; its stat "+
			"cannot be the fresh proof the challenge demanded", err)
	}

	// A window wider than five minutes is not a challenge this cohort issues.
	wide := sampleChallenge(receipt.Claims, challenge.IssuedAt.Add(time.Hour))
	if err := verifier.Verify(receipt, wide); !errors.Is(err, ErrLimitExceeded) {
		t.Errorf("a challenge with an hour-long window verified as %v; the bound is five "+
			"minutes, the same bound the schema's CHECK carries", err)
	}

	_ = clock
}

func TestDeduplicatedBytesGetTheirOwnReceipt(t *testing.T) {
	signer, verifier, first, firstChallenge, _ := signed(t)

	// A second capture of the same bytes: same ref, different reservation,
	// different handoff, different fence. Req 25 says the receipt is newly
	// signed for that capture and cannot be replayed for another.
	second := sampleClaims()
	second.ReservationID = "55555555-5555-4555-8555-555555555555"
	second.HandoffID = "22222222-2222-4222-8222-222222222222"
	second.CaptureFence = 6
	second.ChallengeNonce = "nonce-aaaabbbbccccdddd"

	secondReceipt, err := signer.Sign(second)
	if err != nil {
		t.Fatalf("signing the deduplicated capture: %v", err)
	}
	if secondReceipt.Signature == first.Signature {
		t.Error("two captures of identical bytes produced the same signature; a receipt for " +
			"deduplicated bytes is newly signed for its own capture")
	}

	secondChallenge := sampleChallenge(secondReceipt.Claims, receiptInstant.Add(5*time.Minute))
	if err := verifier.Verify(secondReceipt, secondChallenge); err != nil {
		t.Fatalf("the second capture's receipt does not verify: %v", err)
	}

	// And neither is the other's evidence.
	if err := verifier.Verify(first, secondChallenge); !errors.Is(err, ErrConflict) {
		t.Errorf("the first capture's receipt satisfied the second capture's challenge: %v", err)
	}
	if err := verifier.Verify(secondReceipt, firstChallenge); !errors.Is(err, ErrConflict) {
		t.Errorf("the second capture's receipt satisfied the first capture's challenge: %v", err)
	}
}

func TestRotationIsANewEpochAndAKeyIsNeverReplacedInPlace(t *testing.T) {
	outgoingPublic, outgoingPrivate := keyPair(t)
	incomingPublic, incomingPrivate := keyPair(t)
	clock := &testClock{at: receiptInstant}

	outgoing, err := NewReceiptSigner("receipt-key-1", 7, outgoingPrivate, clock)
	if err != nil {
		t.Fatalf("building the outgoing signer: %v", err)
	}
	incoming, err := NewReceiptSigner("receipt-key-2", 8, incomingPrivate, clock)
	if err != nil {
		t.Fatalf("building the incoming signer: %v", err)
	}

	// Both public keys are pinned at once, which is what "rotation overlaps"
	// means: an outgoing epoch's key stays while any unsettled capture from it
	// may still need a receipt.
	ring, err := NewReceiptKeyRing(
		EpochKey{KeyID: "receipt-key-1", Epoch: 7, PublicKey: outgoingPublic,
			ValidFrom: NewTimestamp(receiptInstant.Add(-24 * time.Hour)), ValidUntil: NewTimestamp(receiptInstant.Add(time.Hour))},
		EpochKey{KeyID: "receipt-key-2", Epoch: 8, PublicKey: incomingPublic,
			ValidFrom: NewTimestamp(receiptInstant.Add(-time.Hour)), ValidUntil: NewTimestamp(receiptInstant.Add(720 * time.Hour))},
	)
	if err != nil {
		t.Fatalf("pinning both epochs: %v", err)
	}
	verifier, err := NewReceiptSignatureVerifier(ring, clock)
	if err != nil {
		t.Fatalf("building the verifier: %v", err)
	}

	oldClaims := sampleClaims()
	oldReceipt, err := outgoing.Sign(oldClaims)
	if err != nil {
		t.Fatalf("signing under the outgoing epoch: %v", err)
	}
	if err := verifier.Verify(oldReceipt, sampleChallenge(oldReceipt.Claims, receiptInstant.Add(5*time.Minute))); err != nil {
		t.Errorf("an unsettled capture from the outgoing epoch cannot get its receipt checked: %v", err)
	}

	newClaims := sampleClaims()
	newClaims.ActivationEpoch = 8
	newClaims.ChallengeNonce = "nonce-eeeeffff00001111"
	newReceipt, err := incoming.Sign(newClaims)
	if err != nil {
		t.Fatalf("signing under the incoming epoch: %v", err)
	}
	if err := verifier.Verify(newReceipt, sampleChallenge(newReceipt.Claims, receiptInstant.Add(5*time.Minute))); err != nil {
		t.Errorf("the incoming epoch's receipt does not verify: %v", err)
	}

	// A signer will not sign across the line.
	if _, err := incoming.Sign(oldClaims); !errors.Is(err, ErrConflict) {
		t.Errorf("the incoming epoch's signer signed the outgoing epoch's claims: %v", err)
	}

	// Once the outgoing key's window closes, its receipts stop verifying --
	// which is why the schema, not elapsed time alone, authorizes removing it.
	clock.advance(2 * time.Hour)
	expired := sampleChallenge(oldReceipt.Claims, clock.at.Add(5*time.Minute))
	expired.IssuedAt = NewTimestamp(clock.at)
	if err := verifier.Verify(oldReceipt, expired); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("a receipt from a key past its validity window verified as %v", err)
	}

	// And one key id may not be pinned for two epochs.
	if _, err := NewReceiptKeyRing(
		EpochKey{KeyID: "receipt-key-1", Epoch: 7, PublicKey: outgoingPublic,
			ValidFrom: NewTimestamp(receiptInstant), ValidUntil: NewTimestamp(receiptInstant.Add(time.Hour))},
		EpochKey{KeyID: "receipt-key-1", Epoch: 8, PublicKey: incomingPublic,
			ValidFrom: NewTimestamp(receiptInstant), ValidUntil: NewTimestamp(receiptInstant.Add(time.Hour))},
	); !errors.Is(err, ErrConflict) {
		t.Errorf("one key id pinned for two epochs was accepted: %v", err)
	}
}

func TestASignerRefusesMaterialThatIsNotAnEd25519Key(t *testing.T) {
	clock := &testClock{at: receiptInstant}
	_, private := keyPair(t)

	for name, build := range map[string]func() error{
		"no key id":    func() error { _, err := NewReceiptSigner("", 7, private, clock); return err },
		"no epoch":     func() error { _, err := NewReceiptSigner("k", 0, private, clock); return err },
		"no clock":     func() error { _, err := NewReceiptSigner("k", 7, private, nil); return err },
		"a short key":  func() error { _, err := NewReceiptSigner("k", 7, private[:16], clock); return err },
		"an empty key": func() error { _, err := NewReceiptSigner("k", 7, nil, clock); return err },
	} {
		if err := build(); err == nil {
			t.Errorf("a signer was built with %s", name)
		}
	}

	if _, err := NewReceiptKeyRing(); !errors.Is(err, ErrIncomplete) {
		t.Errorf("an empty key ring was accepted: %v", err)
	}
}

func TestTheSignerIsTheOnlyHolderOfThePrivateKey(t *testing.T) {
	// The type-level half of Req 24: a ReceiptSigner hands out its public key
	// and its key id, and there is no accessor for the private half. This is
	// checked by compilation -- the field is unexported and the package's own
	// architecture guard forbids adding an exported one -- and asserted here
	// so the intent is written down beside the tables.
	public, private := keyPair(t)
	signer, err := NewReceiptSigner("receipt-key-1", 7, private, &testClock{at: receiptInstant})
	if err != nil {
		t.Fatalf("building the signer: %v", err)
	}

	if !signer.PublicKey().Equal(public) {
		t.Error("the signer's public key is not the pair's public key")
	}
	if len(signer.PublicKey()) != ed25519.PublicKeySize {
		t.Errorf("the signer handed out %d bytes as a public key", len(signer.PublicKey()))
	}
}

func TestAReceiptBodyIsTheClaimsItDecodesTo(t *testing.T) {
	_, verifier, receipt, challenge, _ := signed(t)

	body := `{"claims":` + mustJSON(t, receipt.Claims) + `,"key_id":"receipt-key-1","algorithm":"ed25519","signature":"` + receipt.Signature + `"}`
	decoded, err := ReceiptEnvelopeIsUnaltered([]byte(body))
	if err != nil {
		t.Fatalf("the receipt body does not decode: %v", err)
	}
	if err := verifier.Verify(decoded, challenge); err != nil {
		t.Errorf("a receipt decoded from its own wire form does not verify: %v", err)
	}

	if _, err := ReceiptEnvelopeIsUnaltered([]byte(`{"key_id":"receipt-key-1"}`)); err == nil {
		t.Error("a body with no claims and no signature decoded as a receipt")
	}
	if _, err := ReceiptEnvelopeIsUnaltered([]byte(`not json`)); !errors.Is(err, ErrCorrupt) {
		t.Error("a body that is not JSON decoded as a receipt")
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()

	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}

	return string(encoded)
}
