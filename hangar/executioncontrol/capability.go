package executioncontrol

// Facet-scoped control capabilities.
//
// A capability is what a caller presents to act on an execution. Three
// properties make it worth having, and all three are enforced here rather than
// at each route:
//
//   - it names its FACET, and a route admits only its own facet. A token valid
//     for base execution control cannot hold, seal or publish, however valid it
//     is;
//   - it binds the immutable facts of the operation -- the execution, the
//     fence, the activation epoch, the operation name -- so a token minted for
//     one execution cannot act on another; and
//   - it carries a nonce and an expiry, so a captured token is refused after
//     its window and a replayed one is refused inside it.
//
// The word "facet" is deliberately opaque here. This package knows there is
// more than one and that they do not cross; it does not know that the second
// one is a durable output capture, and the vocabulary guard would fail the
// suite if it learned.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CapabilityDomain separates a control capability from every other HMAC in the
// system, including the materialization warrant that uses the same primitive.
const CapabilityDomain = "hangar-execution-capability-v1"

// CapabilityKeyBytes is the exact key length. A short key is a configuration
// mistake that must be refused rather than stretched.
const CapabilityKeyBytes = sha256.Size

// MaxCapabilityTTL bounds how long a minted capability may live. A capability
// is presented once, within one operation; an hour-long one is a credential.
const MaxCapabilityTTL = 15 * time.Minute

// Facet names a disjoint authorization surface. BaseFacet is this protocol's
// own; an extension declares its own constant and this package never learns
// what it means.
type Facet string

const BaseFacet Facet = "execution-control"

func (facet Facet) Validate() error {
	if facet == "" {
		return fmt.Errorf("%w: capability names no facet", ErrIncomplete)
	}
	if strings.ContainsAny(string(facet), "\x00|") {
		return fmt.Errorf("%w: facet %q contains a separator", ErrIncomplete, facet)
	}

	return nil
}

var (
	// ErrUnauthorized is a capability that does not authorize what it was
	// presented for -- wrong facet, wrong operation, wrong execution, wrong
	// epoch, expired, replayed, or forged.
	ErrUnauthorized = errors.New("hangar/executioncontrol: capability does not authorize this operation")
)

// CapabilityClaims are the facts a capability binds. Every one of them is
// covered by the MAC, and a verifier is handed the facts it expects rather than
// reading them out of the token: a token that told you what it was for would be
// a token authorizing itself.
type CapabilityClaims struct {
	Facet           Facet
	Operation       string
	Identity        Identity
	ActivationEpoch ActivationEpoch
}

func (claims CapabilityClaims) Validate() error {
	if err := claims.Facet.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(claims.Operation) == "" {
		return fmt.Errorf("%w: capability names no operation", ErrIncomplete)
	}
	if err := claims.Identity.Validate(); err != nil {
		return err
	}
	if claims.ActivationEpoch == 0 {
		return fmt.Errorf("%w: capability names no activation epoch", ErrIncomplete)
	}

	return nil
}

func canonicalCapabilityBytes(claims CapabilityClaims, nonce string, expiresAtNanos int64) []byte {
	var out []byte

	field := func(value string) {
		out = binary.BigEndian.AppendUint64(out, uint64(len(value)))
		out = append(out, value...)
	}

	field(CapabilityDomain)
	field(string(claims.Facet))
	field(claims.Operation)
	field(string(claims.Identity.ExecutionID))
	field(strconv.FormatUint(uint64(claims.Identity.Fence), 10))
	field(strconv.FormatUint(uint64(claims.ActivationEpoch), 10))
	field(nonce)
	field(strconv.FormatInt(expiresAtNanos, 10))

	return out
}

// CapabilityMinter is the control plane's half.
type CapabilityMinter struct {
	secret [CapabilityKeyBytes]byte
	ttl    time.Duration
	clock  func() time.Time
}

func NewCapabilityMinter(secret []byte, ttl time.Duration, clock func() time.Time) (*CapabilityMinter, error) {
	if len(secret) != CapabilityKeyBytes {
		return nil, fmt.Errorf("%w: the capability key must contain exactly %d raw bytes, not %d",
			ErrIncomplete, CapabilityKeyBytes, len(secret))
	}
	if ttl <= 0 || ttl > MaxCapabilityTTL {
		return nil, fmt.Errorf("%w: the capability TTL must be positive and no greater than %s",
			ErrIncomplete, MaxCapabilityTTL)
	}
	if clock == nil {
		clock = time.Now
	}
	minter := &CapabilityMinter{ttl: ttl, clock: clock}
	copy(minter.secret[:], secret)

	return minter, nil
}

// Mint issues one capability for one operation on one exact execution.
//
// The nonce is the caller's, not this function's, because the caller is the one
// that has to be able to say which token it minted when a verifier reports a
// replay.
func (minter *CapabilityMinter) Mint(claims CapabilityClaims, nonce string) (ControlCapability, error) {
	if err := claims.Validate(); err != nil {
		return "", err
	}
	if strings.TrimSpace(nonce) == "" || strings.ContainsAny(nonce, "|") {
		return "", fmt.Errorf("%w: a capability nonce must be non-empty and carry no separator",
			ErrIncomplete)
	}

	expiresAt := minter.clock().UTC().Add(minter.ttl).UnixNano()
	mac := hmac.New(sha256.New, minter.secret[:])
	mac.Write(canonicalCapabilityBytes(claims, nonce, expiresAt))

	return ControlCapability(strings.Join([]string{
		nonce,
		strconv.FormatInt(expiresAt, 10),
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil)),
	}, "|")), nil
}

// SpentCapabilities is where a verifier keeps the nonces it has already
// admitted, so a restart does not forget them.
//
// It is an interface because this package has no storage of its own and must
// not grow one: the daemon that verifies capabilities already owns a durable,
// checksummed, atomically-replaced control directory, and a second persistence
// mechanism beside it would be a second thing to get wrong. The map is the
// whole set -- it is bounded by the TTL, which is at most fifteen minutes of
// one node's control traffic -- so a save replaces it rather than appending.
type SpentCapabilities interface {
	LoadSpentCapabilities() (map[string]time.Time, error)
	SaveSpentCapabilities(map[string]time.Time) error
}

// CapabilityVerifier is the daemon's half. It refuses replay, which is why it
// holds state.
type CapabilityVerifier struct {
	secret [CapabilityKeyBytes]byte
	maxTTL time.Duration
	clock  func() time.Time

	// spent is the replay refusal. A capability authorizes one operation; a
	// second presentation of the same nonce is refused whether or not the first
	// one succeeded, because a verifier that let the second through could not
	// tell a retry from a captured token. Entries are dropped once past their
	// expiry, so the map is bounded by the TTL and not by uptime.
	//
	// durable is where the same set is kept across a restart. Without it the
	// refusal was a property of one process's uptime: a crash, a rollout or an
	// OOM kill inside a token's TTL made a captured capability good for one
	// more operation.
	mu      sync.Mutex
	spent   map[string]time.Time
	durable SpentCapabilities
}

// RememberSpentIn gives the verifier somewhere durable to keep spent nonces,
// and reads back what is already there.
//
// Called once at startup, before the listener. A load that fails is not
// survivable by ignoring it: this verifier would then admit every capability
// spent before the restart, which is the defect the store exists to close.
func (verifier *CapabilityVerifier) RememberSpentIn(store SpentCapabilities) error {
	if store == nil {
		return fmt.Errorf("%w: a spent-capability store is required", ErrIncomplete)
	}

	spent, err := store.LoadSpentCapabilities()
	if err != nil {
		return err
	}

	verifier.mu.Lock()
	defer verifier.mu.Unlock()

	verifier.durable = store
	now := verifier.clock().UTC()
	for nonce, expiresAt := range spent {
		// Pruned on the way in. A nonce past its expiry cannot authorize
		// anything, so keeping it would only make the record grow.
		if now.Before(expiresAt) {
			verifier.spent[nonce] = expiresAt
		}
	}

	return nil
}

func NewCapabilityVerifier(secret []byte, maxTTL time.Duration, clock func() time.Time) (*CapabilityVerifier, error) {
	if len(secret) != CapabilityKeyBytes {
		return nil, fmt.Errorf("%w: the capability key must contain exactly %d raw bytes, not %d",
			ErrIncomplete, CapabilityKeyBytes, len(secret))
	}
	if maxTTL <= 0 || maxTTL > MaxCapabilityTTL {
		return nil, fmt.Errorf("%w: the capability TTL must be positive and no greater than %s",
			ErrIncomplete, MaxCapabilityTTL)
	}
	if clock == nil {
		clock = time.Now
	}
	verifier := &CapabilityVerifier{maxTTL: maxTTL, clock: clock, spent: map[string]time.Time{}}
	copy(verifier.secret[:], secret)

	return verifier, nil
}

// Verify admits a capability for exactly the operation it is offered for.
//
// It takes the expected claims as an argument. That is the whole point: the
// route says what it is, and the token either authorizes that or it does not.
// A verifier that decoded the facet out of the token and then checked the token
// against it would authorize every facet.
func (verifier *CapabilityVerifier) Verify(capability ControlCapability, expected CapabilityClaims) error {
	if err := expected.Validate(); err != nil {
		return err
	}

	parts := strings.Split(string(capability), "|")
	if len(parts) != 3 {
		return fmt.Errorf("%w: malformed capability", ErrUnauthorized)
	}
	nonce, expiryText, offered := parts[0], parts[1], parts[2]

	expiresAtNanos, err := strconv.ParseInt(expiryText, 10, 64)
	if err != nil {
		return fmt.Errorf("%w: malformed capability expiry", ErrUnauthorized)
	}

	mac := hmac.New(sha256.New, verifier.secret[:])
	mac.Write(canonicalCapabilityBytes(expected, nonce, expiresAtNanos))
	if !hmac.Equal([]byte(base64.RawURLEncoding.EncodeToString(mac.Sum(nil))), []byte(offered)) {
		// One message for a forged token and for a valid token presented at the
		// wrong facet, operation, execution or epoch: the difference is exactly
		// what an attacker would like to learn, and the route's own log line
		// carries what an operator needs.
		return fmt.Errorf("%w: the capability presented for %s/%s on execution %s is not one this "+
			"cohort issued for that operation", ErrUnauthorized,
			expected.Facet, expected.Operation, expected.Identity.ExecutionID)
	}

	now := verifier.clock().UTC()
	expiresAt := time.Unix(0, expiresAtNanos).UTC()
	if !now.Before(expiresAt) {
		return fmt.Errorf("%w: the capability expired at %s", ErrUnauthorized, expiresAt)
	}
	if expiresAt.Sub(now) > verifier.maxTTL {
		return fmt.Errorf("%w: the capability lives until %s, more than %s from now",
			ErrUnauthorized, expiresAt, verifier.maxTTL)
	}

	verifier.mu.Lock()
	defer verifier.mu.Unlock()
	for spent, at := range verifier.spent {
		if !now.Before(at) {
			delete(verifier.spent, spent)
		}
	}
	if _, replayed := verifier.spent[nonce]; replayed {
		return fmt.Errorf("%w: capability nonce %q has already been presented; a capability "+
			"authorizes one operation", ErrUnauthorized, nonce)
	}
	verifier.spent[nonce] = expiresAt
	if verifier.durable != nil {
		// Recorded as spent BEFORE the operation runs, and a failure to record
		// it refuses the operation. A verifier that admitted a capability it
		// could not remember would be one restart away from admitting it
		// twice, and this is the direction that must fail closed.
		if err := verifier.durable.SaveSpentCapabilities(verifier.spent); err != nil {
			delete(verifier.spent, nonce)

			return fmt.Errorf("%w: this capability could not be recorded as spent, and one that "+
				"is not recorded is one a restart would admit again: %v", ErrUnauthorized, err)
		}
	}

	return nil
}
