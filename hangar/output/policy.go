package output

import (
	"encoding/json"
	"fmt"

	"github.com/concourse/concourse/hangar/executioncontrol"
)

// ExtensionHandshake is what an authenticated output daemon returns.
//
// It embeds the base handshake rather than restating it, so a base-only cohort
// is attestable while output_state is still initial -- which is exactly what
// lets the base facet be enabled before an output bucket exists at all.
//
// None of this is authority by itself; it is evidence. The activation epoch row
// is the authority, and a node label is only a scheduling hint.
type ExtensionHandshake struct {
	Base                    executioncontrol.Handshake `json:"base"`
	CaptureExtensionVersion string                     `json:"capture_extension_version"`
	SourceLedgerVersion     string                     `json:"source_ledger_version"`
	ReceiptPublicKeyID      string                     `json:"receipt_public_key_id"`
	MaterializationKeyID    string                     `json:"materialization_key_id"`
	BucketFingerprint       string                     `json:"bucket_fingerprint"`
	DerivedNamespace        string                     `json:"derived_namespace"`
}

func (handshake ExtensionHandshake) Validate() error {
	if err := handshake.Base.Validate(); err != nil {
		return err
	}
	if handshake.CaptureExtensionVersion != ProtocolVersion {
		return fmt.Errorf("%w: capture extension version %q, this cohort speaks %q",
			ErrUnsupportedProtocol, handshake.CaptureExtensionVersion, ProtocolVersion)
	}
	if handshake.SourceLedgerVersion != SourceLedgerVersion {
		return fmt.Errorf("%w: source ledger version %q, this cohort speaks %q",
			ErrUnsupportedProtocol, handshake.SourceLedgerVersion, SourceLedgerVersion)
	}
	if handshake.ReceiptPublicKeyID == "" {
		return fmt.Errorf("%w: handshake reports no receipt public key id", ErrIncomplete)
	}
	if handshake.MaterializationKeyID == "" {
		return fmt.Errorf("%w: handshake reports no materialization key id", ErrIncomplete)
	}
	if handshake.ReceiptPublicKeyID == handshake.MaterializationKeyID {
		return fmt.Errorf("%w: the receipt and materialization keys are the same; a read warrant "+
			"must not be signable by anything that can mint a publication receipt", ErrIncomplete)
	}
	if handshake.BucketFingerprint == "" {
		return fmt.Errorf("%w: handshake reports no output bucket", ErrIncomplete)
	}
	if handshake.DerivedNamespace == "" {
		return fmt.Errorf("%w: handshake reports no derived namespace", ErrIncomplete)
	}

	return nil
}

// PolicyViolation is a runtime storage-integrity failure. The name and wire values
// remain compatible with historical finding records.
type PolicyViolation string

const (
	ViolationOutOfBandAbsence       PolicyViolation = "out_of_band_absence"
	ViolationRuntimePrincipalDenied PolicyViolation = "runtime_principal_denied"
)

func PolicyViolations() []PolicyViolation {
	return []PolicyViolation{ViolationOutOfBandAbsence, ViolationRuntimePrincipalDenied}
}

func ParsePolicyViolation(value string) (PolicyViolation, error) {
	for _, member := range PolicyViolations() {
		if string(member) == value {
			return member, nil
		}
	}

	return "", fmt.Errorf("%w: policy violation %q; the vocabulary is %v",
		ErrUnknownMember, value, PolicyViolations())
}

func (violation *PolicyViolation) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return err
	}
	parsed, err := ParsePolicyViolation(text)
	if err != nil {
		return err
	}
	*violation = parsed

	return nil
}

func (violation PolicyViolation) Validate() error {
	_, err := ParsePolicyViolation(string(violation))

	return err
}

// PolicyFinding records a runtime failure and the exact object or principal
// involved. The type name remains compatible with existing repository ports.
type PolicyFinding struct {
	Violation PolicyViolation
	Subject   string
	Detail    string
}

// MaxFindingDetailBytes bounds the stored explanation of one runtime failure.
const MaxFindingDetailBytes = 1024

func (finding PolicyFinding) Validate() error {
	if err := finding.Violation.Validate(); err != nil {
		return err
	}
	if finding.Subject == "" {
		return fmt.Errorf("%w: a policy finding names no subject; an operator asked to fix a "+
			"binding needs the binding", ErrIncomplete)
	}
	if len(finding.Detail) > MaxFindingDetailBytes {
		return fmt.Errorf("%w: finding detail is %d bytes, the bound is %d",
			ErrLimitExceeded, len(finding.Detail), MaxFindingDetailBytes)
	}

	return nil
}
