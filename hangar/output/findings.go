package output

import (
	"encoding/json"
	"fmt"

	"github.com/concourse/concourse/hangar/executioncontrol"
)

// ExtensionHandshake is what a node's output plane reports about its capture
// extension.
//
// It embeds the base handshake rather than restating it, so the base facet
// can be served before an output bucket exists at all.
//
// None of this is authority; it is a description. The warrant the daemon
// verifies is the authority, and a node label is only a scheduling hint.
type ExtensionHandshake struct {
	Base                    executioncontrol.Handshake `json:"base"`
	CaptureExtensionVersion string                     `json:"capture_extension_version"`
	SourceLedgerVersion     string                     `json:"source_ledger_version"`
	BucketFingerprint       string                     `json:"bucket_fingerprint"`
	DerivedNamespace        string                     `json:"derived_namespace"`
}

func (handshake ExtensionHandshake) Validate() error {
	if err := handshake.Base.Validate(); err != nil {
		return err
	}
	if handshake.CaptureExtensionVersion != ProtocolVersion {
		return fmt.Errorf("%w: capture extension version %q, this daemon speaks %q",
			ErrUnsupportedProtocol, handshake.CaptureExtensionVersion, ProtocolVersion)
	}
	if handshake.SourceLedgerVersion != SourceLedgerVersion {
		return fmt.Errorf("%w: source ledger version %q, this daemon speaks %q",
			ErrUnsupportedProtocol, handshake.SourceLedgerVersion, SourceLedgerVersion)
	}
	if handshake.BucketFingerprint == "" {
		return fmt.Errorf("%w: handshake reports no output bucket", ErrIncomplete)
	}
	if handshake.DerivedNamespace == "" {
		return fmt.Errorf("%w: handshake reports no derived namespace", ErrIncomplete)
	}

	return nil
}

// IntegrityViolation is the class of a runtime storage-integrity failure.
type IntegrityViolation string

const (
	ViolationOutOfBandAbsence       IntegrityViolation = "out_of_band_absence"
	ViolationRuntimePrincipalDenied IntegrityViolation = "runtime_principal_denied"
)

func IntegrityViolations() []IntegrityViolation {
	return []IntegrityViolation{ViolationOutOfBandAbsence, ViolationRuntimePrincipalDenied}
}

func ParseIntegrityViolation(value string) (IntegrityViolation, error) {
	for _, member := range IntegrityViolations() {
		if string(member) == value {
			return member, nil
		}
	}

	return "", fmt.Errorf("%w: integrity violation %q; the vocabulary is %v",
		ErrUnknownMember, value, IntegrityViolations())
}

func (violation *IntegrityViolation) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return err
	}
	parsed, err := ParseIntegrityViolation(text)
	if err != nil {
		return err
	}
	*violation = parsed

	return nil
}

func (violation IntegrityViolation) Validate() error {
	_, err := ParseIntegrityViolation(string(violation))

	return err
}

// IntegrityFindingRecord records a runtime failure and the exact object or
// principal involved.
type IntegrityFindingRecord struct {
	Violation IntegrityViolation
	Subject   string
	Detail    string
}

// maxFindingDetailBytes bounds the stored explanation of one runtime failure.
const maxFindingDetailBytes = 1024

func (finding IntegrityFindingRecord) Validate() error {
	if err := finding.Violation.Validate(); err != nil {
		return err
	}
	if finding.Subject == "" {
		return fmt.Errorf("%w: an integrity finding names no subject; an operator asked to fix a "+
			"binding needs the binding", ErrIncomplete)
	}
	if len(finding.Detail) > maxFindingDetailBytes {
		return fmt.Errorf("%w: finding detail is %d bytes, the bound is %d",
			ErrLimitExceeded, len(finding.Detail), maxFindingDetailBytes)
	}

	return nil
}

// IntegrityFinding is one open finding as an operator reads it and resolves
// it by id.
type IntegrityFinding struct {
	ID         int64
	Violation  IntegrityViolation
	Subject    string
	Detail     string
	ObservedAt Timestamp

	// BlocksAdmission is true for the runtime classes the admission trigger
	// refuses on; the others are historical observations kept for the record.
	BlocksAdmission bool
}
