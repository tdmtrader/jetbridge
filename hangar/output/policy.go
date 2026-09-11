package output

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

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

// Deferred: the operator status and diagnosis surface is Phase 8's; no running
// process reads it yet
//
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

// LifecycleRule is one bucket lifetime rule as the provider reports it.
//
// Action is kept as the provider's own string rather than a parsed enum: the
// rule this plane cares about is "is there a Delete action here", and a
// vocabulary of our own would answer that question about our vocabulary rather
// than about the bucket. Anything this code does not recognise is therefore
// still visible, and still counted.
type LifecycleRule struct {
	Action    string
	Condition string
}

// RemovesObjects reports whether this rule can remove an object.
//
// It matches case-insensitively and it matches on a PREFIX, because the safe
// direction is to over-report: a rule this plane does not recognise that
// happens to remove objects is the failure mode the whole attestation exists to
// catch, and a rule it over-reports costs an operator one look.
//
// The name is not `IsDelete`, and that is the delete-isolation guard doing its
// job rather than being worked around: a predicate about a RULE is not an
// operation, but a method whose name says "delete" outside the reclaimer is the
// thing the guard cannot tell apart from one, and the honest fix is a name that
// says what this is.
func (rule LifecycleRule) RemovesObjects() bool {
	action := strings.ToLower(strings.TrimSpace(rule.Action))

	return strings.HasPrefix(action, "delete")
}

// BucketLifetimePolicy is one authoritative whole-bucket policy read.
//
// It is the WHOLE policy and not a summary: Req 51 wants a whole-bucket
// lifecycle read proving the snapshot, and a reader that fetched only the rules
// it expected could not prove the absence of the one it did not.
type BucketLifetimePolicy struct {
	BucketFingerprint string
	Metageneration    int64
	Rules             []LifecycleRule
	ObservedAt        Timestamp
}

// PolicyHash is a stable digest of the observed policy.
//
// It exists so a later reader can tell an UNCHANGED policy from an UNREAD one.
// Metageneration alone cannot: a bucket whose metadata was touched and reverted
// has a new metageneration and the same policy, and a reader comparing only the
// number would report a change that did not happen -- or, worse, would be
// tempted to treat a matching number as proof the rules were re-read.
func (policy BucketLifetimePolicy) PolicyHash() string {
	hash := sha256.New()
	fmt.Fprintf(hash, "hangar-output-lifetime-policy-v1\n%s\n%d\n",
		policy.BucketFingerprint, policy.Metageneration)
	for _, rule := range policy.Rules {
		fmt.Fprintf(hash, "%s\x00%s\n", rule.Action, rule.Condition)
	}

	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

// RemovalRules counts the rules that can remove an object.
func (policy BucketLifetimePolicy) RemovalRules() int {
	count := 0
	for _, rule := range policy.Rules {
		if rule.RemovesObjects() {
			count++
		}
	}

	return count
}

// PolicyViolation is the closed set of things an attestation can conclude is
// wrong, each of which moves the epoch to at_risk.
//
// They are separate members because the operator action differs: a lifecycle
// Delete rule is a bucket misconfiguration, an excess role is an IAM
// misconfiguration, and a stale reading is a monitor that stopped. A single
// `unsafe` would tell an operator that something is wrong and nothing else.
type PolicyViolation string

const (
	// ViolationLifecycleDeleteRule is the one Req 51 is named for: the
	// dedicated output bucket has no lifecycle Delete rule, and a bucket that
	// has one can remove a claimed generation without asking anybody.
	ViolationLifecycleDeleteRule PolicyViolation = "lifecycle_delete_rule"

	// ViolationEvidenceStale is an attestation older than the detection bound.
	// "We have not checked" and "the check failed" are the same amount of
	// evidence, and this is both of them.
	ViolationEvidenceStale PolicyViolation = "evidence_stale"

	// ViolationEvidenceUnreadable is a read that did not answer.
	ViolationEvidenceUnreadable PolicyViolation = "evidence_unreadable"

	// ViolationExcessRole is a principal holding a permission its role
	// forbids -- the publisher with delete, the reclaimer with list.
	ViolationExcessRole PolicyViolation = "excess_role"

	// ViolationInsufficientRole is a principal missing a permission its role
	// needs. It is a violation rather than a warning because a role that
	// cannot do its work is a plane that will silently stop reclaiming.
	ViolationInsufficientRole PolicyViolation = "insufficient_role"

	// ViolationWrongPrincipal is a role bound to an identity other than the
	// configured one, including the case where two roles share one identity.
	// A Kubernetes service account is Pod-wide: shared identities are shared
	// permissions.
	ViolationWrongPrincipal PolicyViolation = "wrong_principal"

	// ViolationSharedBucket is an identity outside this plane's four roles
	// holding object permissions on the output bucket. Prefix-only isolation
	// in a mixed bucket is an activation failure (Req 54), and this is what
	// detects one after activation.
	ViolationSharedBucket PolicyViolation = "shared_bucket"

	// ViolationMixedCohort is more than one activation epoch's principals bound
	// at once. Rotation creates a new epoch rather than replacing a key in
	// place, and two cohorts on one bucket is two planes disagreeing about
	// whose object is whose.
	ViolationMixedCohort PolicyViolation = "mixed_cohort"
)

func PolicyViolations() []PolicyViolation {
	return []PolicyViolation{
		ViolationLifecycleDeleteRule,
		ViolationEvidenceStale,
		ViolationEvidenceUnreadable,
		ViolationExcessRole,
		ViolationInsufficientRole,
		ViolationWrongPrincipal,
		ViolationSharedBucket,
		ViolationMixedCohort,
	}
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

// PolicyFinding is one violation with the detail an operator needs to act.
//
// Subject is the principal or rule it is about, and it is bounded free text
// read off an authoritative response rather than composed here: an operator
// asked to fix an IAM binding needs the binding's own name.
type PolicyFinding struct {
	Violation PolicyViolation
	Subject   string
	Detail    string
}

// MaxFindingDetailBytes bounds one finding's free text, so a bucket with a
// thousand bindings cannot turn an attestation into an unbounded write.
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
