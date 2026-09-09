package output

import (
	"encoding/json"
	"fmt"

	"github.com/concourse/concourse/hangar/executioncontrol"
)

// PolicyState is what the last lifetime-policy attestation concluded.
//
// The honest framing is in the doc comments rather than the names: Hangar can
// serialize its own publishers, claimants, readers and reclaimers, and it
// cannot stop a cloud administrator or a changed bucket lifecycle rule from
// deleting an object. Its promise is conditional on a recently verified policy
// and isolated principals, and when that condition cannot be proved it says so
// visibly instead of describing a periodic poll as continuous prevention.
type PolicyState string

const (
	// PolicyUnknown is the state before any attestation, and after one that
	// could not be read. It is not a benign default: it blocks admission
	// exactly as at_risk does.
	PolicyUnknown PolicyState = "unknown"

	// PolicySafe is a whole-bucket lifecycle read, no older than
	// MaxPolicyEvidenceAge, proving no Delete rule. It is a bounded-staleness
	// trust check and nothing stronger.
	PolicySafe PolicyState = "safe"

	// PolicyAtRisk is durable and fail-closed. From detection onward, new
	// captures, claim acquires, managed-output grants, orphan adoption and
	// reclaim admission all stop. Releases and diagnosis remain possible,
	// existing claims and read leases remain recorded, and already-admitted
	// conditional delete work may finish. Recovery needs a fresh safe
	// attestation *and* violation reconciliation: re-attesting alone never
	// erases an unresolved violation.
	PolicyAtRisk PolicyState = "at_risk"
)

func PolicyStates() []PolicyState {
	return []PolicyState{
		PolicyUnknown,
		PolicySafe,
		PolicyAtRisk,
	}
}

func ParsePolicyState(value string) (PolicyState, error) {
	for _, member := range PolicyStates() {
		if string(member) == value {
			return member, nil
		}
	}

	return "", fmt.Errorf("%w: policy state %q; the vocabulary is %v",
		ErrUnknownMember, value, PolicyStates())
}

func (state *PolicyState) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return err
	}
	parsed, err := ParsePolicyState(text)
	if err != nil {
		return err
	}
	*state = parsed

	return nil
}

func (state PolicyState) Validate() error {
	_, err := ParsePolicyState(string(state))

	return err
}

// AdmitsNewWork reports whether this state permits new captures, claim
// acquires, grants, adoption and reclaim admission. Only PolicySafe does;
// PolicyUnknown is treated exactly as PolicyAtRisk, because "we have not
// checked" and "the check failed" are the same amount of evidence.
func (state PolicyState) AdmitsNewWork() bool { return state == PolicySafe }

// PolicySnapshot is one attestation.
//
// It records what was observed and when, so a later reader can decide for
// itself whether the evidence is still fresh enough rather than trusting a
// boolean somebody computed earlier. LifecycleDeleteRules is a count rather
// than a flag for the same reason: it says what was seen, and the rule that
// zero is required is applied where it is enforced.
type PolicySnapshot struct {
	ProtocolVersion      string                           `json:"protocol_version"`
	ActivationEpoch      executioncontrol.ActivationEpoch `json:"activation_epoch"`
	BucketFingerprint    string                           `json:"bucket_fingerprint"`
	Metageneration       int64                            `json:"metageneration"`
	PolicyHash           string                           `json:"policy_hash"`
	LifecycleDeleteRules int                              `json:"lifecycle_delete_rules"`
	State                PolicyState                      `json:"state"`
	ObservedAt           Timestamp                        `json:"observed_at"`
}

func (snapshot PolicySnapshot) Validate() error {
	if err := validateProtocol(snapshot.ProtocolVersion); err != nil {
		return err
	}
	if snapshot.ActivationEpoch == 0 {
		return fmt.Errorf("%w: activation epoch is zero", ErrIncomplete)
	}
	if snapshot.BucketFingerprint == "" {
		return fmt.Errorf("%w: snapshot names no bucket", ErrIncomplete)
	}
	if snapshot.Metageneration <= 0 {
		return fmt.Errorf("%w: snapshot metageneration is not positive", ErrIncomplete)
	}
	if snapshot.PolicyHash == "" {
		return fmt.Errorf("%w: snapshot carries no policy hash; without it a later reader cannot "+
			"tell an unchanged policy from an unread one", ErrIncomplete)
	}
	if snapshot.LifecycleDeleteRules < 0 {
		return fmt.Errorf("%w: negative lifecycle rule count", ErrCorrupt)
	}
	if err := snapshot.State.Validate(); err != nil {
		return err
	}
	if snapshot.State == PolicySafe && snapshot.LifecycleDeleteRules != 0 {
		return fmt.Errorf("%w: snapshot is safe with %d lifecycle Delete rules; the dedicated "+
			"output bucket has none", ErrIncomplete, snapshot.LifecycleDeleteRules)
	}

	return snapshot.ObservedAt.Validate()
}

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
		return fmt.Errorf("%w: the receipt and materialization keys are the same; a read grant "+
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
