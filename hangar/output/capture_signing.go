package output

// Signing the capture extension's statements.
//
// The base protocol signs its acknowledgements in hangar/executioncontrol; this
// is the same construction for the two statements the extension makes -- the
// source ledger's CaptureAcknowledgement and the fenced ReleaseAcknowledgement.
// They are separate domains and separate canonical byte strings, so a statement
// of one kind can never be presented as the other, and neither can be presented
// as a base acknowledgement or as a receipt.
//
// The key is the SAME node control key the base ledger signs with. One node,
// one control identity, one epoch: a second key here would mean an activation
// epoch had to pin two, and a cohort that trusted a node's base statements but
// not its capture statements is not a distinction anything in this system
// makes. What separates them is the domain string, which is what domains are
// for.

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"

	"github.com/concourse/concourse/hangar/executioncontrol"
)

const (
	// CaptureAcknowledgementDomain separates a source-ledger statement from
	// every other signature in the system.
	CaptureAcknowledgementDomain = "hangar-output-capture-acknowledgement-v1"

	// ReleaseAcknowledgementDomain separates the fenced release statement, which
	// is the one that lets destructive cleanup proceed and therefore the one
	// worth being most careful about confusing with something else.
	ReleaseAcknowledgementDomain = "hangar-output-release-acknowledgement-v1"
)

// CaptureFacet is the extension's authorization surface.
//
// The base protocol knows only that facets exist and do not cross; this is the
// second one. A base control capability presented at a hold, a seal or a publish
// is refused here, and a capture capability presented at a base route is refused
// there.
const CaptureFacet executioncontrol.Facet = "durable-output-capture"

// ErrUnsigned is a statement whose signature is absent, malformed, or does not
// verify under the pinned key.
var ErrUnsigned = errors.New("hangar/output: statement does not verify")

type fieldWriter struct{ out []byte }

func (writer *fieldWriter) field(value string) {
	writer.out = binary.BigEndian.AppendUint64(writer.out, uint64(len(value)))
	writer.out = append(writer.out, value...)
}

func (writer *fieldWriter) number(value uint64) {
	writer.field(strconv.FormatUint(value, 10))
}

func (writer *fieldWriter) incarnation(incarnation SourceIncarnation) {
	writer.field(string(incarnation.ExecutionID))
	writer.field(string(incarnation.NodeUID))
	writer.number(uint64(incarnation.HandleGeneration))
	writer.field(string(incarnation.Output))
}

// CanonicalCaptureAcknowledgementBytes is the exact byte string a source-ledger
// statement's signature covers. Length-prefixed, every field, no delimiter that
// could occur inside a value.
func CanonicalCaptureAcknowledgementBytes(ack CaptureAcknowledgement) []byte {
	writer := &fieldWriter{}

	writer.field(CaptureAcknowledgementDomain)
	writer.field(ack.ProtocolVersion)
	writer.field(string(ack.Kind))
	writer.field(string(ack.Execution.ExecutionID))
	writer.number(uint64(ack.Execution.Fence))
	writer.number(uint64(ack.ActivationEpoch))
	writer.number(uint64(ack.LedgerSequence))
	writer.field(string(ack.NodeUID))
	writer.field(string(ack.PodUID))
	writer.field(string(ack.HandoffID))
	writer.field(string(ack.SourceLeaseID))
	writer.incarnation(ack.Incarnation)
	writer.field(string(ack.WriterTicketID))
	writer.number(uint64(ack.WriterFence))
	writer.field(ack.ObservedAt.UTC().Format("2006-01-02T15:04:05.000000000Z07:00"))

	return writer.out
}

// CanonicalReleaseAcknowledgementBytes is the same for the fenced release.
func CanonicalReleaseAcknowledgementBytes(ack ReleaseAcknowledgement) []byte {
	writer := &fieldWriter{}

	writer.field(ReleaseAcknowledgementDomain)
	writer.field(ack.ProtocolVersion)
	writer.field(string(ack.Disposition))
	writer.field(string(ack.Execution.ExecutionID))
	writer.number(uint64(ack.Execution.Fence))
	writer.number(uint64(ack.ActivationEpoch))
	writer.field(string(ack.HandoffID))
	writer.field(string(ack.SourceLeaseID))
	writer.field(string(ack.ReleaseIntentID))
	writer.incarnation(ack.Incarnation)
	writer.number(uint64(ack.LedgerSequence))
	writer.field(ack.ObservedAt.UTC().Format("2006-01-02T15:04:05.000000000Z07:00"))

	return writer.out
}

// CaptureStatementSigner is the node's half for both statements.
type CaptureStatementSigner struct {
	private ed25519.PrivateKey
}

func NewCaptureStatementSigner(private ed25519.PrivateKey) (*CaptureStatementSigner, error) {
	if len(private) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%w: the node control signing key is %d bytes, not %d",
			ErrIncomplete, len(private), ed25519.PrivateKeySize)
	}

	return &CaptureStatementSigner{private: private}, nil
}

func (signer *CaptureStatementSigner) PublicKey() ed25519.PublicKey {
	return signer.private.Public().(ed25519.PublicKey)
}

// SignCapture validates and signs a source-ledger statement. A statement that
// contradicts itself must not become a signed one that contradicts itself.
func (signer *CaptureStatementSigner) SignCapture(ack CaptureAcknowledgement) (CaptureAcknowledgement, error) {
	probe := ack
	probe.Signature = "unsigned"
	if err := probe.Validate(); err != nil {
		return CaptureAcknowledgement{}, err
	}

	ack.Signature = ""
	ack.Signature = base64.StdEncoding.EncodeToString(
		ed25519.Sign(signer.private, CanonicalCaptureAcknowledgementBytes(ack)))

	return ack, nil
}

func (signer *CaptureStatementSigner) SignRelease(ack ReleaseAcknowledgement) (ReleaseAcknowledgement, error) {
	probe := ack
	probe.Signature = "unsigned"
	if err := probe.Validate(); err != nil {
		return ReleaseAcknowledgement{}, err
	}

	ack.Signature = ""
	ack.Signature = base64.StdEncoding.EncodeToString(
		ed25519.Sign(signer.private, CanonicalReleaseAcknowledgementBytes(ack)))

	return ack, nil
}

// VerifyCaptureAcknowledgement checks a source-ledger statement against the
// pinned public key. The key is a parameter, never read out of the message.
func VerifyCaptureAcknowledgement(ack CaptureAcknowledgement, public ed25519.PublicKey) error {
	if err := ack.Validate(); err != nil {
		return err
	}

	return verifyStatement(ack.Signature, func(unsigned string) []byte {
		copied := ack
		copied.Signature = unsigned

		return CanonicalCaptureAcknowledgementBytes(copied)
	}, public, fmt.Sprintf("the %s statement for %s", ack.Kind, ack.Execution.ExecutionID))
}

func VerifyReleaseAcknowledgement(ack ReleaseAcknowledgement, public ed25519.PublicKey) error {
	if err := ack.Validate(); err != nil {
		return err
	}

	return verifyStatement(ack.Signature, func(unsigned string) []byte {
		copied := ack
		copied.Signature = unsigned

		return CanonicalReleaseAcknowledgementBytes(copied)
	}, public, fmt.Sprintf("the %s release for handoff %s", ack.Disposition, ack.HandoffID))
}

func verifyStatement(signature string, canonical func(string) []byte,
	public ed25519.PublicKey, describe string) error {
	if len(public) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: the pinned public key is %d bytes, not %d",
			ErrIncomplete, len(public), ed25519.PublicKeySize)
	}

	raw, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return fmt.Errorf("%w: the signature is not base64: %v", ErrUnsigned, err)
	}
	if !ed25519.Verify(public, canonical(""), raw) {
		return fmt.Errorf("%w: %s does not verify under the pinned key", ErrUnsigned, describe)
	}

	return nil
}
