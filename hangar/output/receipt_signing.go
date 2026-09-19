package output

import (
	"bytes"
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/concourse/concourse/hangar/executioncontrol"
)

// Signing and verifying a per-capture receipt.
//
// Three rules shape everything below.
//
// **The bytes that are signed are not the bytes that are sent.** A signature
// over an encoder's output is a signature over that encoder's field ordering,
// omitempty behaviour and float formatting, and a verifier in another language
// has no way to reproduce it. So signing runs over a canonical, length-prefixed
// encoding built here, and the JSON on the wire is a rendering of the same
// claims that the verifier re-canonicalizes before checking.
//
// **The domain is part of the message.** A receipt and a materialization warrant
// are different authorities -- one says "these bytes were published", the other
// says "you may read them" -- and a signer that could be persuaded to produce
// one while believing it produced the other is a signer with one authority.
// Every signature covers ReceiptDomain, and the verifier will not accept bytes
// canonicalized under any other.
//
// **A signature proves the facts were once true.** That is why nothing here is
// authority on its own: the receipt is diagnostic evidence, and the caller's
// transaction consumes a one-use, database-clock-bounded stat challenge while
// revalidating every bound fact. VerifyReceipt below is the signature and
// binding half; the nonce consumption is atc/db's, in the same transaction.

// MaxCanonicalReceiptBytes bounds a canonical receipt.
//
// A bound is required because the canonical form is length-prefixed and a
// verifier reads those lengths: without a ceiling, a hostile receipt could ask
// a verifier to allocate whatever it liked before a single signature check.
// Four kilobytes is roughly six times the largest legitimate receipt, which
// leaves room for a longer opaque checkpoint without leaving room for a
// payload.
const MaxCanonicalReceiptBytes = 4096

// MaxKeyIDBytes bounds a key id. It is an identifier, not a place to put data.
const MaxKeyIDBytes = 128

// CanonicalReceiptBytes is the exact byte string a receipt's signature covers.
//
// Every field is emitted as a length-prefixed value in a fixed order, so no two
// distinct claim sets can produce the same bytes by moving a delimiter from one
// field into another. The domain and the key id are inside the signature, not
// beside it: a receipt whose key id could be edited without breaking the
// signature would let a verifier be pointed at a key of the attacker's choosing.
func CanonicalReceiptBytes(claims ReceiptClaims, keyID string) ([]byte, error) {
	if err := claims.Validate(); err != nil {
		return nil, err
	}
	if keyID == "" {
		return nil, fmt.Errorf("%w: a receipt is signed under a named key", ErrIncomplete)
	}
	if len(keyID) > MaxKeyIDBytes {
		return nil, fmt.Errorf("%w: key id is %d bytes, the bound is %d",
			ErrLimitExceeded, len(keyID), MaxKeyIDBytes)
	}

	var canonical []byte
	field := func(value string) {
		canonical = append(canonical, fmt.Sprintf("%d:", len(value))...)
		canonical = append(canonical, value...)
		canonical = append(canonical, '|')
	}
	number := func(value int64) { field(fmt.Sprintf("%d", value)) }

	// The domain first, so that bytes canonicalized for another purpose can
	// never be a prefix of these.
	field(ReceiptDomain)
	field(claims.ProtocolVersion)
	field(claims.ReceiptVersion)
	field(keyID)
	field(string(claims.Execution.ExecutionID))
	number(int64(claims.Execution.Fence))
	number(int64(claims.ActivationEpoch))
	field(string(claims.HandoffID))
	field(string(claims.ProducerCheckpointID))
	field(string(claims.ReservationID))
	field(claims.ChallengeNonce)
	field(claims.ChallengeIssuedAt.UTC().Format(time.RFC3339Nano))
	field(string(claims.Incarnation.ExecutionID))
	// The node is part of the source incarnation's identity -- Validate
	// refuses an incarnation without one -- so it is inside the signature.
	// Leaving it out meant a receipt whose node had been edited kept a valid
	// signature and verified, and nothing downstream stores a node to catch it.
	field(string(claims.Incarnation.NodeUID))
	field(string(claims.Incarnation.Output))
	number(int64(claims.Incarnation.HandleGeneration))
	field(string(claims.Output))
	number(int64(claims.CaptureFence))
	number(int64(claims.WriterFence))
	field(string(claims.Ref.Scope))
	field(string(claims.Ref.Digest))
	number(claims.Ref.Generation)
	number(claims.Attributes.StoredBytes)
	number(claims.Attributes.LogicalBytes)
	field(claims.Attributes.CreatedAt.UTC().Format(time.RFC3339Nano))
	field(claims.MarkerVersion)
	field(claims.SignedAt.UTC().Format(time.RFC3339Nano))

	if len(canonical) > MaxCanonicalReceiptBytes {
		return nil, fmt.Errorf("%w: the canonical receipt is %d bytes, the bound is %d",
			ErrLimitExceeded, len(canonical), MaxCanonicalReceiptBytes)
	}

	return canonical, nil
}

// ReceiptSigner holds one epoch's private key. It exists only in the output
// daemon.
//
// The private key is a value here rather than a path or a provider because the
// architecture rule that matters is about which *process* has it: the control
// plane, the web node, the existing artifact daemon, the controllers, the
// control init container, the task and the sidecar never construct one of
// these, and the import graph is what says so.
type ReceiptSigner struct {
	keyID string
	epoch executioncontrol.ActivationEpoch
	key   ed25519.PrivateKey
	clock Clock
}

// NewReceiptSigner builds a signer for one epoch.
func NewReceiptSigner(keyID string, epoch executioncontrol.ActivationEpoch, private ed25519.PrivateKey, clock Clock) (*ReceiptSigner, error) {
	if keyID == "" {
		return nil, fmt.Errorf("%w: a signing key needs an id, because a receipt names the key "+
			"that can check it", ErrIncomplete)
	}
	if len(keyID) > MaxKeyIDBytes {
		return nil, fmt.Errorf("%w: key id is %d bytes, the bound is %d",
			ErrLimitExceeded, len(keyID), MaxKeyIDBytes)
	}
	if epoch == 0 {
		return nil, fmt.Errorf("%w: a signer belongs to an activation epoch", ErrIncomplete)
	}
	if len(private) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%w: an Ed25519 private key is %d bytes, this one is %d",
			ErrIncomplete, ed25519.PrivateKeySize, len(private))
	}
	if clock == nil {
		return nil, fmt.Errorf("%w: a signer needs a clock; a receipt carries the instant it was "+
			"signed at and a challenge has a not-after", ErrIncomplete)
	}

	return &ReceiptSigner{keyID: keyID, epoch: epoch, key: private, clock: clock}, nil
}

// KeyID is the id a receipt this signer produced will name.
func (signer *ReceiptSigner) KeyID() string { return signer.keyID }

// PublicKey is what the activation epoch pins. It is the only thing about the
// key material this type will hand out.
func (signer *ReceiptSigner) PublicKey() ed25519.PublicKey {
	return signer.key.Public().(ed25519.PublicKey)
}

// Sign produces the receipt for one capture.
//
// SignedAt is taken from the clock rather than from the caller: a caller that
// could choose it could sign a receipt dated inside a challenge window it had
// already missed.
func (signer *ReceiptSigner) Sign(claims ReceiptClaims) (Receipt, error) {
	if claims.ActivationEpoch != signer.epoch {
		return Receipt{}, fmt.Errorf("%w: the claims name epoch %d and this signer holds epoch "+
			"%d's key. Rotation creates a new epoch rather than replacing a key in place, so a "+
			"receipt signed across that line could not be checked by either epoch's verifier",
			ErrConflict, claims.ActivationEpoch, signer.epoch)
	}

	claims.SignedAt = NewTimestamp(signer.clock.Now().UTC())

	canonical, err := CanonicalReceiptBytes(claims, signer.keyID)
	if err != nil {
		return Receipt{}, err
	}

	return Receipt{
		Claims:    claims,
		KeyID:     signer.keyID,
		Algorithm: ReceiptAlgorithm,
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(signer.key, canonical)),
	}, nil
}

// EpochKey is one epoch's pinned public key and its validity window.
//
// The window is here because rotation overlaps by design: an outgoing epoch's
// public key stays while any reservation, receipt or recovery row references
// it, and removing it earlier would make a settled capture unverifiable.
type EpochKey struct {
	KeyID      string
	Epoch      executioncontrol.ActivationEpoch
	PublicKey  ed25519.PublicKey
	ValidFrom  Timestamp
	ValidUntil Timestamp
}

func (key EpochKey) Validate() error {
	if key.KeyID == "" {
		return fmt.Errorf("%w: a pinned key needs an id", ErrIncomplete)
	}
	if key.Epoch == 0 {
		return fmt.Errorf("%w: a pinned key belongs to an activation epoch", ErrIncomplete)
	}
	if len(key.PublicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: an Ed25519 public key is %d bytes, this one is %d",
			ErrIncomplete, ed25519.PublicKeySize, len(key.PublicKey))
	}
	if err := key.ValidFrom.Validate(); err != nil {
		return err
	}
	if err := key.ValidUntil.Validate(); err != nil {
		return err
	}
	if !key.ValidUntil.After(key.ValidFrom.Time) {
		return fmt.Errorf("%w: the key's validity window ends before it starts", ErrIncomplete)
	}

	return nil
}

// ReceiptKeyRing is the set of public keys a verifier will accept, pinned by
// the activation epoch.
//
// It is keyed on key id *and* epoch. A receipt names both, and a verifier that
// looked up only the id would accept a receipt that claimed the wrong epoch for
// a key that really is that id -- which is exactly the replay Req 25 forbids.
type ReceiptKeyRing struct {
	byKeyID map[string]EpochKey
}

// NewReceiptKeyRing pins a set of keys.
func NewReceiptKeyRing(keys ...EpochKey) (*ReceiptKeyRing, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: a verifier with no pinned key accepts nothing and would "+
			"report every receipt as forged", ErrIncomplete)
	}

	ring := &ReceiptKeyRing{byKeyID: map[string]EpochKey{}}
	for _, key := range keys {
		if err := key.Validate(); err != nil {
			return nil, err
		}
		if existing, clash := ring.byKeyID[key.KeyID]; clash {
			return nil, fmt.Errorf("%w: key id %q is pinned for epoch %d and for epoch %d; a "+
				"receipt naming it could be checked by either", ErrConflict,
				key.KeyID, existing.Epoch, key.Epoch)
		}
		ring.byKeyID[key.KeyID] = key
	}

	return ring, nil
}

// Deferred: the consumer-side verification half needs a consumer: these
// five verify a receipt or a key id some process read BACK, and the process
// that does that is the ATC's receipt registration. Three names this reason
// once covered -- ValidateLease, RenewLease, ReleaseLease -- are now spent
// by hangar/output.LeaseReadProfile and are off the list
//
// KeyIDs is what the ring holds, sorted, so that a drain predicate can say
// which epochs are still verifiable.
func (ring *ReceiptKeyRing) KeyIDs() []string {
	ids := make([]string, 0, len(ring.byKeyID))
	for id := range ring.byKeyID {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	return ids
}

// ReceiptSignatureVerifier checks the signature and the binding.
//
// It is not the whole of Req 26: the exact-generation stat and the durable
// revalidation live where the store and the transaction are. This is the half
// that can be a pure function over bytes and a pinned key, and keeping it
// separable is what lets the tamper table below be a table.
type ReceiptSignatureVerifier struct {
	ring  *ReceiptKeyRing
	clock Clock

	// consumed is the in-process half of the challenge's one-use rule.
	//
	// The durable authority is the schema, and it is worth naming the guards
	// that actually fire, because the obvious candidate does not.
	// hangar_challenge_one_use raises on a second UPDATE of a *consumed* row,
	// but RegisterReceipt's consume is `UPDATE … WHERE nonce = $1 AND
	// consumed_at IS NULL AND not_after > now()` — a consumed, expired or
	// absent nonce is simply outside the update set, so that trigger never sees
	// it. What refuses those durably is
	// hangar_output_receipts.challenge_nonce's `NOT NULL UNIQUE REFERENCES
	// hangar_receipt_stat_challenges(nonce)` (a reused nonce is a unique
	// violation, since the insert's ON CONFLICT names only reservation_id, and
	// a missing one is a foreign-key violation) together with the deferred
	// hangar_receipt_admission trigger's `consumed_at IS NULL` arm (JB004),
	// which refuses a registration whose challenge was never consumed — an
	// expired nonce among them. Every case fails closed; those two are the
	// guards a restart survives.
	//
	// This one exists because a verifier that accepted the same nonce twice in
	// one process would report "verified" twice for one challenge, and whoever
	// read the second answer would learn nothing about whether the transaction
	// behind it committed. Entries are dropped once past their not-after, so the
	// map is bounded by the five-minute window rather than by uptime.
	mu       sync.Mutex
	consumed map[string]time.Time
}

// NewReceiptSignatureVerifier builds a verifier over a pinned key ring.
func NewReceiptSignatureVerifier(ring *ReceiptKeyRing, clock Clock) (*ReceiptSignatureVerifier, error) {
	if ring == nil {
		return nil, fmt.Errorf("%w: a verifier needs a pinned key ring", ErrIncomplete)
	}
	if clock == nil {
		return nil, fmt.Errorf("%w: a verifier needs a clock; a challenge has a not-after and a "+
			"key has a validity window", ErrIncomplete)
	}

	return &ReceiptSignatureVerifier{ring: ring, clock: clock, consumed: map[string]time.Time{}}, nil
}

// Verify checks one receipt against one challenge.
//
// The order is deliberate: shape, then key, then signature, then binding. A
// verifier that checked the binding first would report "this receipt is for
// another capture" for a forged receipt, which tells an attacker which of their
// two mistakes to fix.
func (verifier *ReceiptSignatureVerifier) Verify(receipt Receipt, challenge StatChallenge) error {
	if err := receipt.Validate(); err != nil {
		return err
	}
	if err := challenge.Validate(); err != nil {
		return err
	}

	key, pinned := verifier.ring.byKeyID[receipt.KeyID]
	if !pinned {
		return fmt.Errorf("%w: no pinned public key has id %q. A receipt names the key that can "+
			"check it, and this deployment has not attested that one", ErrUnauthorized, receipt.KeyID)
	}
	if key.Epoch != receipt.Claims.ActivationEpoch {
		return fmt.Errorf("%w: key %q is pinned for epoch %d and the receipt claims epoch %d",
			ErrUnauthorized, receipt.KeyID, key.Epoch, receipt.Claims.ActivationEpoch)
	}

	now := verifier.clock.Now().UTC()
	if now.Before(key.ValidFrom.UTC()) || now.After(key.ValidUntil.UTC()) {
		return fmt.Errorf("%w: key %q is valid from %s to %s and it is now %s", ErrUnauthorized,
			receipt.KeyID, key.ValidFrom.UTC(), key.ValidUntil.UTC(), now)
	}

	signature, err := base64.StdEncoding.DecodeString(receipt.Signature)
	if err != nil {
		return fmt.Errorf("%w: the receipt's signature is not base64: %v", ErrCorrupt, err)
	}
	if len(signature) != ed25519.SignatureSize {
		return fmt.Errorf("%w: an Ed25519 signature is %d bytes, this one is %d",
			ErrCorrupt, ed25519.SignatureSize, len(signature))
	}

	canonical, err := CanonicalReceiptBytes(receipt.Claims, receipt.KeyID)
	if err != nil {
		return err
	}
	if !ed25519.Verify(key.PublicKey, canonical, signature) {
		return fmt.Errorf("%w: the receipt's signature does not check out under the "+
			"activation-pinned public key for %q", ErrUnauthorized, receipt.KeyID)
	}

	if err := verifier.bind(receipt, challenge, now); err != nil {
		return err
	}

	// Consumed last, and only on success: a nonce burned by a receipt that did
	// not verify would let one forgery deny the capture its real answer.
	return verifier.consume(challenge, now)
}

// consume records one nonce as used and refuses the second use.
func (verifier *ReceiptSignatureVerifier) consume(challenge StatChallenge, now time.Time) error {
	verifier.mu.Lock()
	defer verifier.mu.Unlock()

	for nonce, notAfter := range verifier.consumed {
		if now.After(notAfter) {
			delete(verifier.consumed, nonce)
		}
	}

	if at, used := verifier.consumed[challenge.Nonce]; used {
		return fmt.Errorf("%w: stat challenge %s was already answered at %s. It is one-use, "+
			"which is what stops one fresh observation from settling two captures",
			ErrConflict, challenge.Nonce, at.UTC())
	}
	verifier.consumed[challenge.Nonce] = challenge.NotAfter.UTC()

	return nil
}

// bind is the replay half: every fact the challenge names must be the fact the
// receipt signed.
//
// Each comparison is its own message. "The receipt does not match the
// challenge" would be true of all of them and useful for none, and the fields
// differ in what they mean operationally: a wrong fence is a superseded owner,
// a wrong reservation is a replay, a wrong generation is a stale observation.
func (verifier *ReceiptSignatureVerifier) bind(receipt Receipt, challenge StatChallenge, now time.Time) error {
	claims := receipt.Claims

	if now.After(challenge.NotAfter.UTC()) {
		return fmt.Errorf("%w: the stat challenge expired at %s and it is now %s. A receipt "+
			"proves its facts were true when it was signed; the challenge is what makes them "+
			"true now", ErrTimeout, challenge.NotAfter.UTC(), now)
	}
	if claims.ChallengeNonce != challenge.Nonce {
		return fmt.Errorf("%w: the receipt answers stat challenge %s and this verifier issued %s. "+
			"A receipt bound to its facts alone answers every later challenge naming the same "+
			"facts, which is the replay a nonce exists to stop", ErrConflict,
			claims.ChallengeNonce, challenge.Nonce)
	}
	if !claims.ChallengeIssuedAt.Equal(challenge.IssuedAt.Time) {
		return fmt.Errorf("%w: the receipt says its challenge was issued at %s and this one was "+
			"issued at %s", ErrConflict,
			claims.ChallengeIssuedAt.UTC(), challenge.IssuedAt.UTC())
	}
	if claims.SignedAt.Before(challenge.IssuedAt.Time) {
		// The receipt's instant is the daemon's clock and the challenge's is the
		// database's, so this is a cross-clock comparison and it fails closed: a
		// node running behind the database has its receipt refused and re-attests
		// against a new challenge, which is the direction that cannot be used to
		// present a stale observation as a fresh one.
		return fmt.Errorf("%w: the receipt was signed at %s and its challenge was issued at %s. "+
			"A stat that predates the challenge is the old observation the challenge was issued "+
			"to replace", ErrConflict, claims.SignedAt.UTC(), challenge.IssuedAt.UTC())
	}
	if claims.HandoffID != challenge.HandoffID {
		return fmt.Errorf("%w: the receipt is for handoff %s and the challenge names %s",
			ErrConflict, claims.HandoffID, challenge.HandoffID)
	}
	if claims.ReservationID != challenge.ReservationID {
		return fmt.Errorf("%w: the receipt is for reservation %s and the challenge names %s; a "+
			"receipt for deduplicated bytes is newly signed for its own capture and is not "+
			"another capture's evidence", ErrConflict, claims.ReservationID, challenge.ReservationID)
	}
	if claims.ActivationEpoch != challenge.ActivationEpoch {
		return fmt.Errorf("%w: the receipt claims epoch %d and the challenge names %d",
			ErrConflict, claims.ActivationEpoch, challenge.ActivationEpoch)
	}
	if claims.CaptureFence != challenge.CaptureFence {
		return fmt.Errorf("%w: the receipt was signed at capture fence %d and the challenge "+
			"names %d; a stale owner may not publish", ErrConflict,
			claims.CaptureFence, challenge.CaptureFence)
	}
	if claims.Ref != challenge.Ref {
		return fmt.Errorf("%w: the receipt names %s/%s/%d and the challenge names %s/%s/%d",
			ErrConflict,
			claims.Ref.Scope, claims.Ref.Digest, claims.Ref.Generation,
			challenge.Ref.Scope, challenge.Ref.Digest, challenge.Ref.Generation)
	}

	return nil
}

// Deferred: the consumer-side verification half needs a consumer: these
// five verify a receipt or a key id some process read BACK, and the process
// that does that is the ATC's receipt registration. Three names this reason
// once covered -- ValidateLease, RenewLease, ReleaseLease -- are now spent
// by hangar/output.LeaseReadProfile and are off the list
//
// ReceiptEnvelopeIsUnaltered is the tamper check over the wire form.
//
// It exists because the receipt travels as JSON and a verifier that decoded,
// re-canonicalized and checked would accept a body whose *encoding* said
// something the claims did not -- a duplicate key, say, or a field the decoder
// ignored. Decoding strictly and re-encoding is what turns "the claims verify"
// into "this body is those claims".
// It is the same three steps ReadWarrantVerifier.VerifyBinding performs, and for
// the same reason: this used to be json.Unmarshal plus Validate, which is
// neither half of what the paragraph above describes. Measured before the fix,
// it accepted a body carrying an unknown top-level field outright, and accepted
// a duplicated `key_id` -- one receipt read two different ways by two
// conforming JSON parsers, where a first-wins reader sees one key id and Go's
// last-wins decoder resolves to the other.
func ReceiptEnvelopeIsUnaltered(body []byte) (Receipt, error) {
	var receipt Receipt
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return Receipt{}, fmt.Errorf("%w: the receipt body does not decode: %v", ErrCorrupt, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Receipt{}, fmt.Errorf("%w: the receipt body carries a trailing token; a body that "+
			"is two JSON values is a body two readers disagree about", ErrCorrupt)
	}
	if err := receipt.Validate(); err != nil {
		return Receipt{}, err
	}

	// The re-encode. A strict decoder refuses an unknown field and refuses a
	// trailing token, and it still accepts a DUPLICATE key -- last wins, and
	// the other reader takes the first. Comparing the body against what these
	// claims encode to is what turns "the claims verify" into "this body is
	// those claims".
	rendered, err := json.Marshal(receipt)
	if err != nil || !bytes.Equal(rendered, body) {
		return Receipt{}, fmt.Errorf("%w: the receipt body is not the encoding of the claims it "+
			"decodes to", ErrCorrupt)
	}

	return receipt, nil
}

// Deferred: the consumer-side verification half needs a consumer: these
// five verify a receipt or a key id some process read BACK, and the process
// that does that is the ATC's receipt registration. Three names this reason
// once covered -- ValidateLease, RenewLease, ReleaseLease -- are now spent
// by hangar/output.LeaseReadProfile and are off the list
//
// ConstantTimeKeyIDEqual compares two key ids without leaking which byte
// differed. It is small, but a key id is compared before a signature is
// checked, and a comparison that returns early is a comparison an attacker can
// time.
func ConstantTimeKeyIDEqual(left, right string) bool {
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}
