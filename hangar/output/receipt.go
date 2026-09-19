package output

import (
	"fmt"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
)

// ReceiptAlgorithm is the only signature algorithm receipts use.
const ReceiptAlgorithm = "ed25519"

// ReceiptClaims are what a receipt asserts.
//
// The list is long on purpose. A receipt is the one artefact that travels from
// the node that sealed the bytes to the transaction that binds them, so every
// fact a later verifier must not have to trust the caller for is signed here:
// the producer checkpoint, the capture identity, the source incarnation, the
// reservation and selected output, both fences, the exact TreeRef including its
// generation, the strict tree attributes, the marker version and the activation
// epoch.
//
// A receipt for deduplicated bytes is newly signed for its own capture and
// cannot be replayed for another capture, source, output or fence -- which is
// exactly what binding all of those together buys.
//
// ChallengeNonce and ChallengeIssuedAt are what make the facts true *now*
// rather than once. Without them a receipt is bound to a set of facts, so it
// answers every later challenge naming the same facts and cannot prove its stat
// post-dates the challenge at all -- and the database's one-use row cannot
// catch that, because it consumes a nonce the receipt never named. They are
// signed, so the daemon that performed the stat is what attests to which
// challenge it was answering.
//
// Attributes is the wire projection of the foundation's hangar.TreeAttributes,
// declared below. The projection exists for exactly one reason: the foundation
// encodes its CreatedAt with trailing zeros trimmed, so one instant has several
// spellings, and every other claim a verifier must match here has one.
type ReceiptClaims struct {
	ProtocolVersion      string                           `json:"protocol_version"`
	ReceiptVersion       string                           `json:"receipt_version"`
	Execution            executioncontrol.Identity        `json:"execution"`
	ActivationEpoch      executioncontrol.ActivationEpoch `json:"activation_epoch"`
	HandoffID            HandoffID                        `json:"handoff_id"`
	ProducerCheckpointID OpaqueID                         `json:"producer_checkpoint_id"`
	ReservationID        ReservationID                    `json:"reservation_id"`
	ChallengeNonce       string                           `json:"challenge_nonce"`
	ChallengeIssuedAt    Timestamp                        `json:"challenge_issued_at"`
	Incarnation          SourceIncarnation                `json:"incarnation"`
	Output               OutputName                       `json:"output"`
	CaptureFence         CaptureFence                     `json:"capture_fence"`
	WriterFence          WriterFence                      `json:"writer_fence"`
	Ref                  hangar.TreeRef                   `json:"ref"`
	Attributes           TreeAttributes                   `json:"attributes"`
	MarkerVersion        string                           `json:"marker_version"`
	SignedAt             Timestamp                        `json:"signed_at"`
}

func (claims ReceiptClaims) Validate() error {
	if err := validateProtocol(claims.ProtocolVersion); err != nil {
		return err
	}
	if claims.ReceiptVersion != ReceiptDomain {
		return fmt.Errorf("%w: receipt version %q, this cohort signs %q",
			ErrUnsupportedProtocol, claims.ReceiptVersion, ReceiptDomain)
	}
	if err := claims.Execution.Validate(); err != nil {
		return err
	}
	if claims.ActivationEpoch == 0 {
		return fmt.Errorf("%w: activation epoch is zero; a stale label or handshake cannot "+
			"authorize emission", ErrIncomplete)
	}
	if err := claims.HandoffID.Validate(); err != nil {
		return err
	}
	if err := claims.ProducerCheckpointID.Validate(); err != nil {
		return err
	}
	if err := claims.ReservationID.Validate(); err != nil {
		return err
	}
	if claims.ChallengeNonce == "" {
		return fmt.Errorf("%w: the receipt names no stat challenge; a signature over old facts "+
			"proves only that the facts were once true", ErrIncomplete)
	}
	if len(claims.ChallengeNonce) < MinChallengeNonceBytes ||
		len(claims.ChallengeNonce) > MaxChallengeNonceBytes {
		return fmt.Errorf("%w: the challenge nonce is %d bytes and the schema issues between %d "+
			"and %d", ErrLimitExceeded, len(claims.ChallengeNonce),
			MinChallengeNonceBytes, MaxChallengeNonceBytes)
	}
	if err := claims.ChallengeIssuedAt.Validate(); err != nil {
		return err
	}
	if err := claims.Incarnation.Validate(); err != nil {
		return err
	}
	if claims.Incarnation.ExecutionID != claims.Execution.ExecutionID {
		return fmt.Errorf("%w: the receipt binds an incarnation from a different execution",
			ErrInvalidIdentity)
	}
	if err := claims.Output.Validate(); err != nil {
		return err
	}
	if claims.Output != claims.Incarnation.Output {
		return fmt.Errorf("%w: the receipt names output %q but binds an incarnation of %q",
			ErrInvalidIdentity, claims.Output, claims.Incarnation.Output)
	}
	if claims.CaptureFence == 0 {
		return fmt.Errorf("%w: capture fence is zero", ErrIncomplete)
	}
	if claims.WriterFence == 0 {
		return fmt.Errorf("%w: writer fence is zero; a receipt without the fence its seal ran "+
			"under cannot be told from one signed before the seal", ErrIncomplete)
	}
	if err := claims.Ref.Validate(); err != nil {
		return err
	}
	// The attributes are validated on their own terms first. Req 25 says the
	// signed claims bind "strict tree attributes" and Req 26 says every signed
	// claim is matched against durable state -- so claims that validate while
	// carrying a zero creation instant or a negative size are a hole, and
	// comparing only the ref was how the hole stayed open.
	if err := claims.Attributes.Validate(); err != nil {
		return err
	}
	if claims.Attributes.Ref != claims.Ref {
		return fmt.Errorf("%w: the signed attributes describe a different tree ref",
			ErrInvalidIdentity)
	}
	if claims.MarkerVersion != MarkerVersion {
		return fmt.Errorf("%w: marker version %q, this cohort accepts %q",
			ErrConflict, claims.MarkerVersion, MarkerVersion)
	}

	return claims.SignedAt.Validate()
}

// Receipt is the versioned, daemon-signed statement returned by a successful
// capture.
//
// The signing key lives only in the output daemon. Web, controllers, tasks and
// API callers hold the public key and the key id, and rotation creates a new
// activation epoch rather than replacing a key in place -- so a receipt always
// names the epoch whose key can check it.
type Receipt struct {
	Claims    ReceiptClaims `json:"claims"`
	KeyID     string        `json:"key_id"`
	Algorithm string        `json:"algorithm"`
	Signature string        `json:"signature"`
}

func (receipt Receipt) Validate() error {
	if err := receipt.Claims.Validate(); err != nil {
		return err
	}
	if receipt.KeyID == "" {
		return fmt.Errorf("%w: receipt names no key id; without it a verifier cannot tell which "+
			"epoch's public key should check it", ErrIncomplete)
	}
	if receipt.Algorithm != ReceiptAlgorithm {
		return fmt.Errorf("%w: receipt algorithm %q, this cohort verifies %q",
			ErrUnsupportedProtocol, receipt.Algorithm, ReceiptAlgorithm)
	}
	if receipt.Signature == "" {
		return fmt.Errorf("%w: receipt is unsigned", ErrIncomplete)
	}

	return nil
}

// Ref is the tree ref this receipt is about. It exists so callers stop
// reaching two levels into the claims for the one field they always want.
func (receipt Receipt) Ref() hangar.TreeRef { return receipt.Claims.Ref }

// TreeAttributes is the wire projection of hangar.TreeAttributes.
//
// It declares no new fact. Every field is the foundation's, in the foundation's
// order, and AttributesFromFoundation / Foundation convert between them without
// loss -- so nothing has to decide which of the two is authoritative. In-process
// callers keep using hangar.TreeAttributes; this exists for the one place the
// value is signed.
//
// The single difference is CreatedAt. The foundation's is a time.Time, and Go's
// RFC 3339 encoding trims trailing zeros, so one instant has several wire
// spellings -- `…23Z`, `…23.4Z`, `…23.418927631Z`. Every other claim in a
// receipt has exactly one, and a verifier in another language must not have to
// accept two widths on the one field that would then differ. Timestamp fixes
// the width at nine digits and requires UTC, the way the rest of this protocol
// already does.
//
// Duplicating the *type* to make the width uniform would be a second attribute
// model. Projecting it at the wire boundary is not: the projection is where an
// encoding decision belongs.
type TreeAttributes struct {
	Ref          hangar.TreeRef `json:"ref"`
	StoredBytes  int64          `json:"stored_bytes"`
	LogicalBytes int64          `json:"logical_bytes"`
	CreatedAt    Timestamp      `json:"created_at"`
}

// AttributesFromFoundation projects the foundation's value onto the wire.
func AttributesFromFoundation(attributes hangar.TreeAttributes) TreeAttributes {
	return TreeAttributes{
		Ref:          attributes.Ref,
		StoredBytes:  attributes.StoredBytes,
		LogicalBytes: attributes.LogicalBytes,
		CreatedAt:    NewTimestamp(attributes.CreatedAt),
	}
}

// Foundation projects back. It normalizes the location to UTC, which is the
// point of the projection, and never moves the instant.
func (attributes TreeAttributes) Foundation() hangar.TreeAttributes {
	return hangar.TreeAttributes{
		Ref:          attributes.Ref,
		StoredBytes:  attributes.StoredBytes,
		LogicalBytes: attributes.LogicalBytes,
		CreatedAt:    attributes.CreatedAt.Time,
	}
}

func (attributes TreeAttributes) Validate() error {
	if err := attributes.Ref.Validate(); err != nil {
		return err
	}
	if attributes.StoredBytes < 0 || attributes.LogicalBytes < 0 {
		return fmt.Errorf("%w: attributes report a negative size", ErrCorrupt)
	}

	return attributes.CreatedAt.Validate()
}
