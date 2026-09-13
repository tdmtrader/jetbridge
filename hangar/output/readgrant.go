package output

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
)

// The managed-output read grant: output read grant v1.
//
// It is what a consuming Pod carries to the daemon, and it is deliberately NOT
// the foundation's strict-input materialization grant. Three separations, and
// each of them exists because collapsing it would give one authority two
// meanings:
//
//   - A DIFFERENT KEY. Strict input uses the foundation's materialization HMAC
//     key. This uses the output plane's own exact-32-byte key, and neither
//     signs for the other. A deployment that shared them would let a strict
//     input grant name a managed output.
//   - A DIFFERENT DOMAIN. MaterializeDomain is inside the signed bytes, first,
//     so bytes canonicalized for a receipt or for a strict input can never be a
//     prefix of these. A signer that could be persuaded to produce one while
//     believing it produced the other is a signer with one authority.
//   - A DIFFERENT SHAPE. A strict input grant binds a ref and a destination. A
//     read grant also binds the READ LEASE, because a managed read is only ever
//     authorized by a committed lease, and a token that did not name one could
//     outlive the protection it was issued under. It binds the lease's IDENTITY
//     and not a fence: the read lease's fence has no writer anywhere in this
//     plane -- it is inserted as 1 and never moved, and Phase 7's takeover works
//     on the CAPTURE fence, which is a different column on a different table --
//     so a fence in the token would have been a field with exactly one possible
//     value, checked against itself. The column stays (see the migration's note
//     at hangar_read_leases.lease_fence); the claim does not.
//
// THE GRANT IS NOT AUTHORITY BY ITSELF. A valid HMAC over a lease that is
// missing, released, expired, superseded or reclaim-conflicted authorizes
// nothing: the daemon verifies the signature and then independently asks the
// control plane whether that exact lease is still active. This type is the
// first half of that pair and never the whole of it.
//
// THE GRANT'S WINDOW IS THE LEASE'S WINDOW, and that is a choice with a reason.
// Requirement 37 wants an ambiguous mint to be retryable with a BYTE-IDENTICAL
// grant rather than with a second lease, so nothing in the token may come from
// the instant it was minted: issue and expiry are the lease's own granted-at
// and expires-at, and the nonce is the one stored with the lease row. Two mints
// of one committed lease produce the same bytes, and a mint cannot extend the
// authority the database committed.

const (
	// ReadGrantVersion is the version inside every grant. It moves when the
	// bound field set moves, never for an encoding change.
	ReadGrantVersion = "1"

	// ReadGrantKeyBytes is the exact raw key length. It is exact rather than a
	// minimum: a "long enough" key check accepts a 33-byte key that somebody
	// pasted a newline into.
	ReadGrantKeyBytes = sha256.Size

	// ReadGrantNonceBytes is the length of the durable per-lease nonce.
	ReadGrantNonceBytes = 16

	// MaxCanonicalReadGrantBytes bounds the canonical form, for the same reason
	// MaxCanonicalReceiptBytes does: the encoding is length-prefixed and a
	// verifier reads those lengths.
	MaxCanonicalReadGrantBytes = 4096

	// MaxReadGrantBytes bounds the token on the wire.
	MaxReadGrantBytes = 4096
)

// ReadDestination is where a materialization may land, in the only vocabulary
// the node has: a step handle and one of its volumes.
//
// It is never a path. The daemon derives the path from these beneath its own
// managed root, exactly as the strict-input materializer does, so no caller --
// task, consumer or API -- can point a read anywhere of its own choosing.
type ReadDestination struct {
	Handle string `json:"handle"`
	Volume string `json:"volume"`
}

func (destination ReadDestination) Validate() error {
	if !validDestinationSegment(destination.Handle) {
		return fmt.Errorf("%w: destination handle %q is not a canonical path segment; a "+
			"destination is a handle and a volume, never a path", ErrInvalidIdentity,
			destination.Handle)
	}
	if !validDestinationSegment(destination.Volume) {
		return fmt.Errorf("%w: destination volume %q is not a canonical path segment",
			ErrInvalidIdentity, destination.Volume)
	}

	return nil
}

// validDestinationSegment is the foundation's rule, restated here rather than
// imported, because hangar's copy is unexported and this package may not widen
// it. Both must stay the same rule; grant_test.go's cross-check is what says so.
func validDestinationSegment(segment string) bool {
	if len(segment) < 1 || len(segment) > 128 {
		return false
	}
	alphanumeric := func(character byte) bool {
		return character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9'
	}
	if !alphanumeric(segment[0]) || !alphanumeric(segment[len(segment)-1]) {
		return false
	}
	for index := 0; index < len(segment); index++ {
		character := segment[index]
		if !alphanumeric(character) && character != '.' && character != '_' && character != '-' {
			return false
		}
	}

	return true
}

// ReadGrantClaims is everything a read grant binds.
//
// Every field here is checked by the verifier and re-checked by the control
// plane against the committed lease. There is no field a caller may supply that
// is not covered by the signature, which is the property that makes "a valid
// HMAC bound to a released lease authorizes nothing" a statement about the
// LEASE rather than about the token.
type ReadGrantClaims struct {
	Domain          string                           `json:"domain"`
	Version         string                           `json:"version"`
	ReadLeaseID     ReadLeaseID                      `json:"read_lease_id"`
	ClaimID         ClaimID                          `json:"claim_id"`
	Ref             hangar.TreeRef                   `json:"ref"`
	Destination     ReadDestination                  `json:"destination"`
	ActivationEpoch executioncontrol.ActivationEpoch `json:"activation_epoch"`
	IssuedAt        Timestamp                        `json:"issued_at"`
	ExpiresAt       Timestamp                        `json:"expires_at"`
	Nonce           string                           `json:"nonce"`
}

func (claims ReadGrantClaims) Validate() error {
	if claims.Domain != MaterializeDomain {
		return fmt.Errorf("%w: read grant domain is %q, not %q", ErrUnauthorized,
			claims.Domain, MaterializeDomain)
	}
	if claims.Version != ReadGrantVersion {
		return fmt.Errorf("%w: read grant version is %q, not %q", ErrUnauthorized,
			claims.Version, ReadGrantVersion)
	}
	if err := claims.ReadLeaseID.Validate(); err != nil {
		return err
	}
	if err := claims.ClaimID.Validate(); err != nil {
		return err
	}
	if err := claims.Ref.Validate(); err != nil {
		return err
	}
	if err := claims.Destination.Validate(); err != nil {
		return err
	}
	if claims.ActivationEpoch == 0 {
		return fmt.Errorf("%w: read grant names no activation epoch", ErrIncomplete)
	}
	if err := claims.IssuedAt.Validate(); err != nil {
		return err
	}
	if err := claims.ExpiresAt.Validate(); err != nil {
		return err
	}
	if !claims.ExpiresAt.After(claims.IssuedAt.Time) {
		return fmt.Errorf("%w: read grant expires at or before it was issued", ErrIncomplete)
	}
	return validateReadGrantNonce(claims.Nonce)
}

// validateReadGrantNonce is the nonce rule, stated once and used by both the
// claims and the lease request.
func validateReadGrantNonce(nonce string) error {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(nonce)
	if err != nil || len(raw) != ReadGrantNonceBytes ||
		base64.RawURLEncoding.EncodeToString(raw) != nonce {
		return fmt.Errorf("%w: a read grant nonce is %d raw bytes in strict raw-url base64",
			ErrIncomplete, ReadGrantNonceBytes)
	}

	return nil
}

// NewReadGrantNonce mints the durable per-lease nonce.
//
// It is generated once, by the caller that creates the lease, and stored with
// it -- not generated at mint time. A nonce chosen when the token is minted
// would make two mints of one lease differ, and requirement 37's replay would
// have to create a second lease to be answerable.
func NewReadGrantNonce(random io.Reader) (string, error) {
	raw := make([]byte, ReadGrantNonceBytes)
	if _, err := io.ReadFull(random, raw); err != nil {
		return "", fmt.Errorf("%w: generating a read grant nonce: %v", ErrInfrastructure, err)
	}

	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// CanonicalReadGrantBytes is the exact byte string a read grant's MAC covers.
//
// Length-prefixed, fixed order, domain first: the same rule as
// CanonicalReceiptBytes, and for the same reason. A signature over an encoder's
// output would be a signature over that encoder's field ordering.
func CanonicalReadGrantBytes(claims ReadGrantClaims) ([]byte, error) {
	if err := claims.Validate(); err != nil {
		return nil, err
	}

	var canonical []byte
	field := func(value string) {
		canonical = append(canonical, fmt.Sprintf("%d:", len(value))...)
		canonical = append(canonical, value...)
		canonical = append(canonical, '|')
	}
	number := func(value int64) { field(fmt.Sprintf("%d", value)) }

	field(MaterializeDomain)
	field(ReadGrantVersion)
	field(string(claims.ReadLeaseID))
	field(string(claims.ClaimID))
	field(string(claims.Ref.Scope))
	field(string(claims.Ref.Digest))
	number(claims.Ref.Generation)
	field(claims.Destination.Handle)
	field(claims.Destination.Volume)
	number(int64(claims.ActivationEpoch))
	field(claims.IssuedAt.UTC().Format(time.RFC3339Nano))
	field(claims.ExpiresAt.UTC().Format(time.RFC3339Nano))
	field(claims.Nonce)

	if len(canonical) > MaxCanonicalReadGrantBytes {
		return nil, fmt.Errorf("%w: the canonical read grant is %d bytes, the bound is %d",
			ErrLimitExceeded, len(canonical), MaxCanonicalReadGrantBytes)
	}

	return canonical, nil
}

// ReadGrantSigner mints a grant for one committed lease.
//
// It holds no clock, on purpose. Everything dated in a grant comes from the
// lease the database committed, so there is no instant a signer could choose
// and no way for a mint to widen the window the transaction agreed to.
type ReadGrantSigner struct {
	key [ReadGrantKeyBytes]byte
}

// ReadGrantVerifier checks one.
type ReadGrantVerifier struct {
	key   [ReadGrantKeyBytes]byte
	clock Clock
}

func NewReadGrantSigner(material []byte) (*ReadGrantSigner, error) {
	if len(material) != ReadGrantKeyBytes {
		return nil, fmt.Errorf("%w: an output read grant key is exactly %d raw bytes, this one "+
			"is %d; it is never the receipt key and never the strict-input materialization key",
			ErrIncomplete, ReadGrantKeyBytes, len(material))
	}
	signer := &ReadGrantSigner{}
	copy(signer.key[:], material)

	return signer, nil
}

// Deferred: the consumer-side verification half needs a consumer: these
// five verify a receipt or a key id some process read BACK, and the process
// that does that is the ATC's receipt registration. Three names this reason
// once covered -- ValidateLease, RenewLease, ReleaseLease -- are now spent
// by hangar/output.LeaseReadProfile and are off the list
func NewReadGrantVerifier(material []byte, clock Clock) (*ReadGrantVerifier, error) {
	if len(material) != ReadGrantKeyBytes {
		return nil, fmt.Errorf("%w: an output read grant key is exactly %d raw bytes, this one "+
			"is %d", ErrIncomplete, ReadGrantKeyBytes, len(material))
	}
	if clock == nil {
		return nil, fmt.Errorf("%w: a read grant verifier needs a clock; a grant has an expiry",
			ErrIncomplete)
	}
	verifier := &ReadGrantVerifier{clock: clock}
	copy(verifier.key[:], material)

	return verifier, nil
}

// Sign mints the grant for an already-committed lease.
//
// The lease is the parameter rather than a pile of fields because every dated
// and fenced value in the token must come from the committed row: a signature
// over a caller's idea of the lease would be a signature over a lease that may
// never have existed.
func (signer *ReadGrantSigner) Sign(lease ReadLease, destination ReadDestination, nonce string) (string, error) {
	if err := lease.Validate(); err != nil {
		return "", err
	}
	claims := ReadGrantClaims{
		Domain:          MaterializeDomain,
		Version:         ReadGrantVersion,
		ReadLeaseID:     lease.ReadLeaseID,
		ClaimID:         lease.ClaimID,
		Ref:             lease.Ref,
		Destination:     destination,
		ActivationEpoch: lease.ActivationEpoch,
		IssuedAt:        lease.GrantedAt,
		ExpiresAt:       lease.ExpiresAt,
		Nonce:           nonce,
	}

	canonical, err := CanonicalReadGrantBytes(claims)
	if err != nil {
		return "", err
	}

	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}

	mac := hmac.New(sha256.New, signer.key[:])
	_, _ = mac.Write(canonical)
	token := base64.RawURLEncoding.EncodeToString(append(payload, mac.Sum(nil)...))
	if len(token) > MaxReadGrantBytes {
		return "", fmt.Errorf("%w: the read grant is %d bytes, the bound is %d",
			ErrLimitExceeded, len(token), MaxReadGrantBytes)
	}

	return token, nil
}

// Verify checks a grant against the ref and destination the caller is asking
// for, and returns what it binds.
//
// It answers ErrUnauthorized and nothing more specific. A verifier that said
// which field failed would tell a caller holding a forged token exactly which
// byte to change next; the operator's diagnosis comes from the lease-control
// answer, which is authenticated.
//
// It is the BINDING plus the token's own window, and it is what the DAEMON
// calls: nothing is opened under a token whose window has passed, and that
// check runs on the node before any question is asked. The control plane calls
// VerifyBinding instead, for the reason written there.
func (verifier *ReadGrantVerifier) Verify(token string, ref hangar.TreeRef, destination ReadDestination) (ReadGrantClaims, error) {
	claims, err := verifier.VerifyBinding(token, ref, destination)
	if err != nil {
		return ReadGrantClaims{}, err
	}

	now := verifier.clock.Now().UTC()
	if now.Before(claims.IssuedAt.UTC()) || !now.Before(claims.ExpiresAt.UTC()) {
		return ReadGrantClaims{}, fmt.Errorf("%w: the read grant does not authorize this read",
			ErrUnauthorized)
	}

	return claims, nil
}

// VerifyBinding checks everything the MAC covers EXCEPT the token's window.
//
// The split is not a weakening, it is a statement about which clock owns which
// question. What a grant binds -- the lease, the claim, the ref, the
// destination, the epoch, the nonce -- is settled by the MAC and is true
// forever. Whether that lease is still a live protection is settled by the ROW,
// on the database clock, and only the row knows about a renewal: a renewal
// moves the row's expiry and cannot move a token already in a reader's hands.
//
// So the control plane authenticates the binding here and then asks the
// repository, which refuses a released, expired, or no-longer-readable lease on
// the clock that owns those facts. A control plane that had refused on the
// token's window instead would have answered `unauthorized` to a legitimately
// renewed reader trying to RELEASE, and the protection would have been held
// until recovery closed it.
//
// The daemon still calls Verify. A stale token opening an object is exactly
// what the window is for; what it is not for is deciding, on the control
// plane's side, a question the database has a better answer to.
func (verifier *ReadGrantVerifier) VerifyBinding(token string, ref hangar.TreeRef, destination ReadDestination) (ReadGrantClaims, error) {
	unauthorized := func() (ReadGrantClaims, error) {
		return ReadGrantClaims{}, fmt.Errorf("%w: the read grant does not authorize this read",
			ErrUnauthorized)
	}

	if len(token) == 0 || len(token) > MaxReadGrantBytes ||
		ref.Validate() != nil || destination.Validate() != nil {
		return unauthorized()
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != token ||
		len(raw) <= sha256.Size || len(raw)-sha256.Size > MaxReadGrantBytes {
		return unauthorized()
	}
	payload, provided := raw[:len(raw)-sha256.Size], raw[len(raw)-sha256.Size:]

	var claims ReadGrantClaims
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&claims); err != nil {
		return unauthorized()
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return unauthorized()
	}
	// The wire form is a RENDERING of the claims, and the canonical form is
	// what is signed. Both are checked: re-marshalling catches a payload whose
	// bytes differ from what these claims encode to, and re-canonicalizing
	// catches everything the MAC is actually over.
	rendered, err := json.Marshal(claims)
	if err != nil || !bytes.Equal(rendered, payload) {
		return unauthorized()
	}
	canonical, err := CanonicalReadGrantBytes(claims)
	if err != nil {
		return unauthorized()
	}
	mac := hmac.New(sha256.New, verifier.key[:])
	_, _ = mac.Write(canonical)
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return unauthorized()
	}

	if !sameRef(claims.Ref, ref) ||
		!constantTimeEqual(claims.Destination.Handle, destination.Handle) ||
		!constantTimeEqual(claims.Destination.Volume, destination.Volume) {
		return unauthorized()
	}

	return claims, nil
}

func sameRef(left, right hangar.TreeRef) bool {
	return constantTimeEqual(string(left.Scope), string(right.Scope)) &&
		constantTimeEqual(string(left.Digest), string(right.Digest)) &&
		left.Generation == right.Generation
}

func constantTimeEqual(left, right string) bool {
	return hmac.Equal([]byte(left), []byte(right))
}

// DecodeReadGrantClaims reads a grant's claims WITHOUT checking anything.
//
// It exists for exactly one caller: a verifier that needs to know which ref and
// destination a token names before it can check the token against them. That is
// not a weakening -- the destination in a managed read is the grant's, never the
// caller's (requirement 7: no API accepts a caller-chosen path), so there is no
// second opinion to compare it with, and the MAC over the canonical form is what
// decides. Nothing else may use it: the name says unverified, and the result is
// data until Verify has run.
func DecodeReadGrantClaims(token string, claims *ReadGrantClaims) error {
	if claims == nil {
		return fmt.Errorf("%w: nowhere to decode a read grant into", ErrIncomplete)
	}
	if len(token) == 0 || len(token) > MaxReadGrantBytes {
		return fmt.Errorf("%w: the read grant is %d bytes", ErrUnauthorized, len(token))
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) <= sha256.Size {
		return fmt.Errorf("%w: the read grant is not a payload and a MAC", ErrUnauthorized)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw[:len(raw)-sha256.Size]))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(claims); err != nil {
		return fmt.Errorf("%w: the read grant's claims do not decode", ErrUnauthorized)
	}

	return nil
}
