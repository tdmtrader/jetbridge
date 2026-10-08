package hangar

// The one warrant.
//
// Every authorization the node daemon verifies is a warrant: a short-lived,
// signed, attenuated statement from the web about exactly one target. There is
// one key (the Hangar key, a raw 32-byte secret the web signs with and every
// daemon verifies against), one canonical encoding whose first field is the
// purpose, one HMAC domain, and one signer and one verifier. A warrant minted
// for one purpose is refused at every route of another, with ErrUnauthorized
// and nothing more specific: a verifier that said which field failed would
// tell a caller holding a forged token which byte to change next.
//
// Four purposes:
//
//   - materialize-input: one exact tree into one step's volume. Single-use by
//     nonce in the sense that no two mints are alike; the daemon's strict-input
//     materialization is idempotent and does not keep a spent set.
//   - read-result: one staged read of one published generation, bound to the
//     reader's committed claim. Its window is the CLAIM's window and it carries
//     no nonce: two mints of one committed claim are byte-identical, so a mint
//     whose commit answer was lost is retried by acquiring the same claim again.
//     The node keeps it single-use by claim id.
//   - execution-control and output-capture: one control operation on one exact
//     execution under one fence. Each carries a nonce; the node refuses a
//     replayed one inside its window (cmd/artifact-daemon/outputplane).
//
// The wire form is base64url(payload JSON ‖ HMAC-SHA256). The MAC is over the
// canonical, length-prefixed bytes rather than the JSON, so a signature is
// never a signature over one encoder's field ordering; the verifier checks both
// that the JSON re-marshals to the bytes received and that the canonical form
// of what it decoded carries the MAC.

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"
)

const (
	// WarrantDomain separates a warrant's MAC from every other HMAC in the
	// system.
	WarrantDomain = "hangar-warrant-v1"
	// warrantVersion moves when the bound field set moves, never for an
	// encoding change.
	warrantVersion = 1
	// WarrantKeyBytes is the exact raw key length. Exact rather than a
	// minimum: a "long enough" check accepts a key somebody pasted a newline
	// into.
	WarrantKeyBytes = sha256.Size
	warrantNonceLen = 16
	// MaxWarrantBytes bounds the token on the wire and its payload.
	MaxWarrantBytes = 4096
	// MaxWarrantTTL bounds how long any warrant may live. A warrant is
	// presented once, within one operation; an hour-long one is a credential.
	MaxWarrantTTL = 15 * time.Minute
)

// Purpose is what a warrant authorizes. A route admits only its own purpose.
type Purpose string

const (
	PurposeMaterializeInput Purpose = "materialize-input"
	PurposeReadResult       Purpose = "read-result"
	PurposeControlBase      Purpose = "execution-control"
	PurposeControlCapture   Purpose = "output-capture"
)

func (purpose Purpose) Validate() error {
	switch purpose {
	case PurposeMaterializeInput, PurposeReadResult, PurposeControlBase, PurposeControlCapture:
		return nil
	}
	return fmt.Errorf("hangar: %q is not a warrant purpose", string(purpose))
}

// singleUse reports whether a warrant of this purpose carries a nonce and a
// signer-chosen window. The read warrant does not: its window is its claim's.
func (purpose Purpose) singleUse() bool { return purpose != PurposeReadResult }

// Warrant is everything a warrant binds. Every field is covered by the MAC.
//
// The bound fields are the purpose's: a tree ref and a destination handle and
// volume for materialize-input and read-result; the reader's claim id and the
// node for read-result; an operation, an execution id and a fence for the two
// control purposes. A field another purpose owns must be zero. The window and
// the nonce are the signer's for every purpose but read-result, whose window
// the caller supplies from the committed claim.
type Warrant struct {
	Purpose Purpose `json:"purpose"`
	Version int     `json:"version"`

	Ref    TreeRef `json:"ref"`
	Handle string  `json:"handle"`
	Volume string  `json:"volume"`

	ClaimID string `json:"claim_id"`
	NodeUID string `json:"node_uid"`

	Operation   string `json:"operation"`
	ExecutionID string `json:"execution_id"`
	Fence       uint64 `json:"fence"`

	IssuedAt  int64  `json:"issued_at_nanos"`
	ExpiresAt int64  `json:"expires_at_nanos"`
	Nonce     string `json:"nonce"`
}

// Validate checks the warrant is a whole, well-formed statement for its
// purpose: the fields its purpose binds are present and canonical, the
// fields other purposes own are zero, the window is positive and within
// MaxWarrantTTL, and the nonce is present exactly when the purpose is
// single-use.
func (warrant Warrant) Validate() error {
	if err := warrant.Purpose.Validate(); err != nil {
		return err
	}
	if warrant.Version != warrantVersion {
		return fmt.Errorf("hangar: warrant version %d is not %d", warrant.Version, warrantVersion)
	}
	tree := warrant.Purpose == PurposeMaterializeInput || warrant.Purpose == PurposeReadResult
	read := warrant.Purpose == PurposeReadResult
	control := warrant.Purpose == PurposeControlBase || warrant.Purpose == PurposeControlCapture
	if tree {
		if err := warrant.Ref.Validate(); err != nil {
			return fmt.Errorf("hangar: warrant: %w", err)
		}
		if !validWarrantSegment(warrant.Handle) || !validWarrantSegment(warrant.Volume) {
			return errors.New("hangar: warrant handle and volume must be canonical path segments")
		}
	} else if warrant.Ref != (TreeRef{}) || warrant.Handle != "" || warrant.Volume != "" {
		return fmt.Errorf("hangar: a %s warrant binds no tree or destination", warrant.Purpose)
	}
	if read {
		if !validWarrantField(warrant.ClaimID) || !validWarrantField(warrant.NodeUID) {
			return errors.New("hangar: a read warrant names a claim and a node")
		}
	} else if warrant.ClaimID != "" || warrant.NodeUID != "" {
		return fmt.Errorf("hangar: a %s warrant binds no claim or node", warrant.Purpose)
	}
	if control {
		if !validWarrantField(warrant.Operation) || !validWarrantField(warrant.ExecutionID) {
			return errors.New("hangar: a control warrant names an operation and an execution")
		}
	} else if warrant.Operation != "" || warrant.ExecutionID != "" || warrant.Fence != 0 {
		return fmt.Errorf("hangar: a %s warrant binds no operation, execution or fence", warrant.Purpose)
	}
	if warrant.IssuedAt <= 0 || warrant.ExpiresAt <= warrant.IssuedAt || warrant.ExpiresAt-warrant.IssuedAt > MaxWarrantTTL.Nanoseconds() {
		return errors.New("hangar: warrant window must be positive and no longer than MaxWarrantTTL")
	}
	if warrant.Purpose.singleUse() {
		nonce, err := base64.RawURLEncoding.Strict().DecodeString(warrant.Nonce)
		if err != nil || len(nonce) != warrantNonceLen || base64.RawURLEncoding.EncodeToString(nonce) != warrant.Nonce {
			return errors.New("hangar: a single-use warrant carries a 16-byte nonce")
		}
	} else if warrant.Nonce != "" {
		return errors.New("hangar: a read warrant carries no nonce; its window is its claim's")
	}
	return nil
}

// CanonicalWarrantBytes is the exact byte string a warrant's MAC covers:
// length-prefixed, fixed order, the purpose first, every field present for
// every purpose (a field the purpose does not bind is empty), then the
// window and the nonce.
func CanonicalWarrantBytes(warrant Warrant) ([]byte, error) {
	if err := warrant.Validate(); err != nil {
		return nil, err
	}
	var canonical []byte
	field := func(value string) {
		canonical = strconv.AppendInt(canonical, int64(len(value)), 10)
		canonical = append(canonical, ':')
		canonical = append(canonical, value...)
		canonical = append(canonical, '|')
	}
	number := func(value int64) { field(strconv.FormatInt(value, 10)) }

	field(string(warrant.Purpose))
	number(int64(warrant.Version))
	field(string(warrant.Ref.Scope))
	field(string(warrant.Ref.Digest))
	number(warrant.Ref.Generation)
	field(warrant.Handle)
	field(warrant.Volume)
	field(warrant.ClaimID)
	field(warrant.NodeUID)
	field(warrant.Operation)
	field(warrant.ExecutionID)
	field(strconv.FormatUint(warrant.Fence, 10))
	number(warrant.IssuedAt)
	number(warrant.ExpiresAt)
	field(warrant.Nonce)

	if len(canonical) > MaxWarrantBytes {
		return nil, fmt.Errorf("%w: the canonical warrant is %d bytes, the bound is %d", ErrLimitExceeded, len(canonical), MaxWarrantBytes)
	}
	return canonical, nil
}

func warrantMAC(key []byte, canonical []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(WarrantDomain))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(canonical)
	return mac.Sum(nil)
}

// Signer is the web's half. It holds the one Hangar key.
type Signer struct {
	key    [WarrantKeyBytes]byte
	ttl    time.Duration
	clock  func() time.Time
	random io.Reader
}

// Verifier is the daemon's half. It is stateless: a purpose that is single-use
// on the node keeps its spent set beside the daemon's own durable records.
type Verifier struct {
	key    [WarrantKeyBytes]byte
	maxTTL time.Duration
	clock  func() time.Time
}

func NewSigner(key []byte, ttl time.Duration, clock func() time.Time) (*Signer, error) {
	if len(key) != WarrantKeyBytes {
		return nil, fmt.Errorf("hangar: the Hangar key must contain exactly %d raw bytes", WarrantKeyBytes)
	}
	if ttl <= 0 || ttl > MaxWarrantTTL {
		return nil, fmt.Errorf("hangar: warrant TTL must be positive and no greater than %s", MaxWarrantTTL)
	}
	if clock == nil {
		clock = time.Now
	}
	signer := &Signer{ttl: ttl, clock: clock, random: rand.Reader}
	copy(signer.key[:], key)
	return signer, nil
}

func NewVerifier(key []byte, maxTTL time.Duration, clock func() time.Time) (*Verifier, error) {
	if len(key) != WarrantKeyBytes {
		return nil, fmt.Errorf("hangar: the Hangar key must contain exactly %d raw bytes", WarrantKeyBytes)
	}
	if maxTTL <= 0 || maxTTL > MaxWarrantTTL {
		return nil, fmt.Errorf("hangar: warrant TTL must be positive and no greater than %s", MaxWarrantTTL)
	}
	if clock == nil {
		clock = time.Now
	}
	verifier := &Verifier{maxTTL: maxTTL, clock: clock}
	copy(verifier.key[:], key)
	return verifier, nil
}

// Sign mints one warrant from its bound fields.
//
// For a single-use purpose the caller supplies the bound fields only: the
// window is now plus the signer's TTL and the nonce is fresh, and a caller
// that supplied either is refused. For the read warrant the caller supplies
// the window too, from the committed claim, and no nonce: a signer that could
// choose an instant could widen the window the transaction agreed to.
func (signer *Signer) Sign(warrant Warrant) (string, error) {
	if err := warrant.Purpose.Validate(); err != nil {
		return "", err
	}
	warrant.Version = warrantVersion
	if warrant.Purpose.singleUse() {
		if warrant.IssuedAt != 0 || warrant.ExpiresAt != 0 || warrant.Nonce != "" {
			return "", fmt.Errorf("hangar: sign %s warrant: the window and nonce are the signer's", warrant.Purpose)
		}
		now := signer.clock().UTC()
		issuedAt, ok := exactUnixNano(now)
		if now.IsZero() || !ok || issuedAt <= 0 {
			return "", fmt.Errorf("hangar: sign %s warrant: clock is outside the supported range", warrant.Purpose)
		}
		expiresAt, ok := exactUnixNano(now.Add(signer.ttl))
		if !ok || expiresAt <= issuedAt || expiresAt-issuedAt != signer.ttl.Nanoseconds() {
			return "", fmt.Errorf("hangar: sign %s warrant: expiry is outside the supported range", warrant.Purpose)
		}
		nonce := make([]byte, warrantNonceLen)
		if _, err := io.ReadFull(signer.random, nonce); err != nil {
			return "", fmt.Errorf("hangar: generate %s warrant nonce: %w", warrant.Purpose, err)
		}
		warrant.IssuedAt, warrant.ExpiresAt = issuedAt, expiresAt
		warrant.Nonce = base64.RawURLEncoding.EncodeToString(nonce)
	}
	canonical, err := CanonicalWarrantBytes(warrant)
	if err != nil {
		return "", fmt.Errorf("hangar: sign %s warrant: %w", warrant.Purpose, err)
	}
	payload, err := json.Marshal(warrant)
	if err != nil {
		return "", fmt.Errorf("hangar: marshal %s warrant: %w", warrant.Purpose, err)
	}
	token := base64.RawURLEncoding.EncodeToString(append(payload, warrantMAC(signer.key[:], canonical)...))
	if len(payload) > MaxWarrantBytes || len(token) > MaxWarrantBytes {
		return "", fmt.Errorf("%w: the %s warrant exceeds %d bytes", ErrLimitExceeded, warrant.Purpose, MaxWarrantBytes)
	}
	return token, nil
}

// Verify admits a token for exactly what the caller expects, and returns what
// it binds.
//
// The expected warrant is the ROUTE's: its purpose and its bound fields. A
// verifier that decoded the purpose out of the token and then checked the token
// against it would authorize every purpose. The window and the nonce are the
// token's and must be zero in expected; so may the read warrant's claim id be,
// since the node does not know it in advance (it is what the node keeps the
// warrant single-use by), and it is compared when given. Every other bound
// field is compared, in constant time.
//
// It answers ErrUnauthorized and nothing more specific.
func (verifier *Verifier) Verify(token string, expected Warrant) (Warrant, error) {
	unauthorized := func() (Warrant, error) { return Warrant{}, ErrUnauthorized }
	if expected.Purpose.Validate() != nil || expected.IssuedAt != 0 || expected.ExpiresAt != 0 || expected.Nonce != "" {
		return unauthorized()
	}
	if len(token) == 0 || len(token) > MaxWarrantBytes {
		return unauthorized()
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != token || len(raw) <= sha256.Size || len(raw)-sha256.Size > MaxWarrantBytes {
		return unauthorized()
	}
	payload, provided := raw[:len(raw)-sha256.Size], raw[len(raw)-sha256.Size:]

	var warrant Warrant
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&warrant); err != nil {
		return unauthorized()
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return unauthorized()
	}
	// The wire form is a rendering of the warrant and the canonical form is
	// what is signed. Both are checked: re-marshalling catches a payload whose
	// bytes differ from what the decoded warrant encodes to, and
	// re-canonicalizing catches everything the MAC is over.
	rendered, err := json.Marshal(warrant)
	if err != nil || !bytes.Equal(rendered, payload) {
		return unauthorized()
	}
	canonical, err := CanonicalWarrantBytes(warrant)
	if err != nil || !hmac.Equal(provided, warrantMAC(verifier.key[:], canonical)) {
		return unauthorized()
	}

	if warrant.Purpose != expected.Purpose || warrant.ExpiresAt-warrant.IssuedAt > verifier.maxTTL.Nanoseconds() {
		return unauthorized()
	}
	now, ok := exactUnixNano(verifier.clock().UTC())
	if !ok || now < warrant.IssuedAt || now >= warrant.ExpiresAt {
		return unauthorized()
	}
	if !sameTreeRef(warrant.Ref, expected.Ref) ||
		!constantTimeStringEqual(warrant.Handle, expected.Handle) ||
		!constantTimeStringEqual(warrant.Volume, expected.Volume) ||
		!constantTimeStringEqual(warrant.NodeUID, expected.NodeUID) ||
		!constantTimeStringEqual(warrant.Operation, expected.Operation) ||
		!constantTimeStringEqual(warrant.ExecutionID, expected.ExecutionID) ||
		warrant.Fence != expected.Fence ||
		(expected.ClaimID != "" && !constantTimeStringEqual(warrant.ClaimID, expected.ClaimID)) {
		return unauthorized()
	}
	return warrant, nil
}

func sameTreeRef(left, right TreeRef) bool {
	return constantTimeStringEqual(string(left.Scope), string(right.Scope)) &&
		constantTimeStringEqual(string(left.Digest), string(right.Digest)) && left.Generation == right.Generation
}

func constantTimeStringEqual(left, right string) bool {
	return hmac.Equal([]byte(left), []byte(right))
}

// validWarrantSegment is the rule for a destination handle or volume: a
// canonical path segment, which the daemon derives a path from beneath its own
// root. hangar/output restates it for ReadDestination.
func validWarrantSegment(segment string) bool {
	if len(segment) < 1 || len(segment) > 128 || !isASCIIAlphanumeric(segment[0]) || !isASCIIAlphanumeric(segment[len(segment)-1]) {
		return false
	}
	for index := 0; index < len(segment); index++ {
		character := segment[index]
		if !isASCIIAlphanumeric(character) && character != '.' && character != '_' && character != '-' {
			return false
		}
	}
	return true
}

// validWarrantField is the rule for an opaque bound identifier: present,
// bounded, and printable ASCII with no space.
func validWarrantField(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] <= ' ' || value[index] > '~' {
			return false
		}
	}
	return true
}

func isASCIIAlphanumeric(character byte) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9'
}

func exactUnixNano(value time.Time) (int64, bool) {
	value = value.UTC()
	nanos := value.UnixNano()
	return nanos, time.Unix(0, nanos).UTC().Equal(value)
}
