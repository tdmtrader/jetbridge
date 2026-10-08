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

// The managed-output read warrant: output read warrant v1.
//
// It is what a consuming Pod carries to the daemon, and it is deliberately NOT
// the foundation's strict-input materialization warrant. Three separations, and
// each of them exists because collapsing it would give one authority two
// meanings:
//
//   - A DIFFERENT KEY. Strict input uses the foundation's materialization HMAC
//     key. This uses the output plane's own exact-32-byte key, and neither
//     signs for the other. A deployment that shared them would let a strict
//     input warrant name a managed output.
//   - A DIFFERENT DOMAIN. MaterializeDomain is inside the signed bytes, first,
//     so bytes canonicalized for a capability or for a strict input can never be a
//     prefix of these. A signer that could be persuaded to produce one while
//     believing it produced the other is a signer with one authority.
//   - A DIFFERENT SHAPE. A strict input warrant binds a ref and a destination. A
//     read warrant also binds the READER'S CLAIM, because a managed read is
//     only ever authorized by a committed claim, and a token that did not name
//     one could outlive the protection it was issued under.
//
// A read is: the web, in the consumer's transaction, acquires a claim with a
// term (the materialization timeout plus ReadClaimMargin) on the registered
// generation; then it mints a warrant bound to that claim's id, the ref, the
// destination handle and volume, the node, and the claim's own acquired-at and
// expires-at. The node daemon verifies the warrant against its key and the
// token's window, keeps it single-use per claim id on itself, and never calls
// the web. When the read ends the web releases the claim; an abandoned read's
// claim expires on its own.
//
// THE WARRANT'S WINDOW IS THE CLAIM'S WINDOW, and nothing in the token comes
// from the instant it was minted: no nonce, no mint time. Two mints of one
// committed claim are byte-identical, so a mint whose commit answer was lost
// is retried by acquiring the same claim again (idempotent) and minting again.
// A mint cannot extend the authority the database committed.

const (
	// readWarrantVersion is the version inside every warrant. It moves when the
	// bound field set moves, never for an encoding change.
	readWarrantVersion = "1"

	// ReadWarrantKeyBytes is the exact raw key length. It is exact rather than a
	// minimum: a "long enough" key check accepts a 33-byte key that somebody
	// pasted a newline into.
	ReadWarrantKeyBytes = sha256.Size

	// MaxCanonicalReadWarrantBytes bounds the canonical form: the encoding is
	// length-prefixed and a verifier reads those lengths.
	MaxCanonicalReadWarrantBytes = 4096

	// MaxReadWarrantBytes bounds the token on the wire.
	MaxReadWarrantBytes = 4096
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

// ReadWarrantClaims is everything a read warrant binds.
//
// Every field here is checked by the verifier. There is no field a caller may
// supply that is not covered by the signature, which is the property that
// makes "a valid HMAC bound to a released claim authorizes nothing" a statement
// about the CLAIM rather than about the token.
type ReadWarrantClaims struct {
	Domain      string          `json:"domain"`
	Version     string          `json:"version"`
	ClaimID     ClaimID         `json:"claim_id"`
	Ref         hangar.TreeRef  `json:"ref"`
	Destination ReadDestination `json:"destination"`
	// NodeUID is the one node whose daemon may honour the warrant. The node
	// keeps a warrant single-use on itself; binding the node is what makes it
	// one read in the whole cluster rather than one per node.
	NodeUID   executioncontrol.NodeUID `json:"node_uid"`
	IssuedAt  Timestamp                `json:"issued_at"`
	ExpiresAt Timestamp                `json:"expires_at"`
}

func (claims ReadWarrantClaims) Validate() error {
	if claims.Domain != MaterializeDomain {
		return fmt.Errorf("%w: read warrant domain is %q, not %q", ErrUnauthorized,
			claims.Domain, MaterializeDomain)
	}
	if claims.Version != readWarrantVersion {
		return fmt.Errorf("%w: read warrant version is %q, not %q", ErrUnauthorized,
			claims.Version, readWarrantVersion)
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
	if claims.NodeUID == "" {
		return fmt.Errorf("%w: read warrant names no node", ErrIncomplete)
	}
	if err := claims.IssuedAt.Validate(); err != nil {
		return err
	}
	if err := claims.ExpiresAt.Validate(); err != nil {
		return err
	}
	if !claims.ExpiresAt.After(claims.IssuedAt.Time) {
		return fmt.Errorf("%w: read warrant expires at or before it was issued", ErrIncomplete)
	}

	return nil
}

// CanonicalReadWarrantBytes is the exact byte string a read warrant's MAC covers.
//
// Length-prefixed, fixed order, domain first. A signature over an encoder's
// output would be a signature over that encoder's field ordering.
func CanonicalReadWarrantBytes(claims ReadWarrantClaims) ([]byte, error) {
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
	field(readWarrantVersion)
	field(string(claims.ClaimID))
	field(string(claims.Ref.Scope))
	field(string(claims.Ref.Digest))
	number(claims.Ref.Generation)
	field(claims.Destination.Handle)
	field(claims.Destination.Volume)
	field(string(claims.NodeUID))
	field(claims.IssuedAt.UTC().Format(time.RFC3339Nano))
	field(claims.ExpiresAt.UTC().Format(time.RFC3339Nano))

	if len(canonical) > MaxCanonicalReadWarrantBytes {
		return nil, fmt.Errorf("%w: the canonical read warrant is %d bytes, the bound is %d",
			ErrLimitExceeded, len(canonical), MaxCanonicalReadWarrantBytes)
	}

	return canonical, nil
}

// ReadWarrantSigner mints a warrant for one committed claim.
//
// It holds no clock, on purpose. Everything dated in a warrant comes from the
// claim the database committed, so there is no instant a signer could choose
// and no way for a mint to widen the window the transaction agreed to.
type ReadWarrantSigner struct {
	key [ReadWarrantKeyBytes]byte
}

// ReadWarrantVerifier checks one.
type ReadWarrantVerifier struct {
	key   [ReadWarrantKeyBytes]byte
	clock Clock
}

func NewReadWarrantSigner(material []byte) (*ReadWarrantSigner, error) {
	if len(material) != ReadWarrantKeyBytes {
		return nil, fmt.Errorf("%w: an output read warrant key is exactly %d raw bytes, this one "+
			"is %d; it is never the control capability key and never the strict-input materialization key",
			ErrIncomplete, ReadWarrantKeyBytes, len(material))
	}
	signer := &ReadWarrantSigner{}
	copy(signer.key[:], material)

	return signer, nil
}

func NewReadWarrantVerifier(material []byte, clock Clock) (*ReadWarrantVerifier, error) {
	if len(material) != ReadWarrantKeyBytes {
		return nil, fmt.Errorf("%w: an output read warrant key is exactly %d raw bytes, this one "+
			"is %d", ErrIncomplete, ReadWarrantKeyBytes, len(material))
	}
	if clock == nil {
		return nil, fmt.Errorf("%w: a read warrant verifier needs a clock; a warrant has an expiry",
			ErrIncomplete)
	}
	verifier := &ReadWarrantVerifier{clock: clock}
	copy(verifier.key[:], material)

	return verifier, nil
}

// WarrantClaimsFor is everything a read warrant over one committed claim
// binds: every dated value comes from the claim row itself.
func WarrantClaimsFor(claim ClaimRecord, destination ReadDestination, node executioncontrol.NodeUID) ReadWarrantClaims {
	claims := ReadWarrantClaims{
		Domain:      MaterializeDomain,
		Version:     readWarrantVersion,
		ClaimID:     claim.ClaimID,
		Ref:         claim.Ref,
		Destination: destination,
		NodeUID:     node,
		IssuedAt:    claim.AcquiredAt,
	}
	if claim.ExpiresAt != nil {
		claims.ExpiresAt = *claim.ExpiresAt
	}

	return claims
}

// Sign mints the warrant for an already-committed reader's claim.
//
// The claim is the parameter rather than a pile of fields because every dated
// value in the token must come from the committed row: a signature over a
// caller's idea of the claim would be a signature over a claim that may never
// have existed. A consumer's claim -- one with no expiry -- is refused: a read
// warrant has a window, and the window is the claim's.
func (signer *ReadWarrantSigner) Sign(claim ClaimRecord, destination ReadDestination, node executioncontrol.NodeUID) (string, error) {
	if err := claim.Validate(); err != nil {
		return "", err
	}
	if claim.ExpiresAt == nil {
		return "", fmt.Errorf("%w: claim %s has no expiry; a read warrant is minted over a "+
			"reader's expiring claim, never a consumer's hold", ErrIncomplete, claim.ClaimID)
	}
	if !claim.Active() {
		return "", fmt.Errorf("%w: claim %s was released; a read warrant is minted over a live claim",
			ErrConflict, claim.ClaimID)
	}
	claims := WarrantClaimsFor(claim, destination, node)

	canonical, err := CanonicalReadWarrantBytes(claims)
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
	if len(token) > MaxReadWarrantBytes {
		return "", fmt.Errorf("%w: the read warrant is %d bytes, the bound is %d",
			ErrLimitExceeded, len(token), MaxReadWarrantBytes)
	}

	return token, nil
}

// Verify checks a warrant against the ref and destination the caller is asking
// for, and returns what it binds.
//
// It answers ErrUnauthorized and nothing more specific. A verifier that said
// which field failed would tell a caller holding a forged token exactly which
// byte to change next; the operator's diagnosis comes from the claim row.
//
// It is the BINDING plus the token's own window, and it is what the DAEMON
// calls: nothing is opened under a token whose window has passed, and that
// check runs on the node before any question is asked.
func (verifier *ReadWarrantVerifier) Verify(token string, ref hangar.TreeRef, destination ReadDestination) (ReadWarrantClaims, error) {
	claims, err := verifier.VerifyBinding(token, ref, destination)
	if err != nil {
		return ReadWarrantClaims{}, err
	}

	now := verifier.clock.Now().UTC()
	if now.Before(claims.IssuedAt.UTC()) || !now.Before(claims.ExpiresAt.UTC()) {
		return ReadWarrantClaims{}, fmt.Errorf("%w: the read warrant does not authorize this read",
			ErrUnauthorized)
	}

	return claims, nil
}

// VerifyBinding checks everything the MAC covers EXCEPT the token's window:
// what a warrant binds -- the claim, the ref, the destination, the node -- is
// settled by the MAC and is true forever. Whether the claim is still
// a live hold is the database's question, on its own clock.
func (verifier *ReadWarrantVerifier) VerifyBinding(token string, ref hangar.TreeRef, destination ReadDestination) (ReadWarrantClaims, error) {
	unauthorized := func() (ReadWarrantClaims, error) {
		return ReadWarrantClaims{}, fmt.Errorf("%w: the read warrant does not authorize this read",
			ErrUnauthorized)
	}

	if len(token) == 0 || len(token) > MaxReadWarrantBytes ||
		ref.Validate() != nil || destination.Validate() != nil {
		return unauthorized()
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != token ||
		len(raw) <= sha256.Size || len(raw)-sha256.Size > MaxReadWarrantBytes {
		return unauthorized()
	}
	payload, provided := raw[:len(raw)-sha256.Size], raw[len(raw)-sha256.Size:]

	var claims ReadWarrantClaims
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
	canonical, err := CanonicalReadWarrantBytes(claims)
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
