package policy

import (
	"fmt"
	"sort"
	"strings"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// The permission matrix of Req 54, written once.
//
// Every entry is a GCS permission string rather than a word of our own, because
// what an attestation compares is what an IAM response says. A struct of
// booleans computed here would be this package's opinion of a binding; a list
// of permission names is the binding.
//
// Three limits are stated rather than modelled, and Req 41 is explicit about
// the first two. `storage.objects.get` authorizes metadata AND body reads:
// there is no metadata-only object permission, so inventory and the reclaimer
// can read the bytes even though their code does not. `storage.objects.list`
// cannot be scoped to a prefix: list authority covers the bucket. And
// `storage.objects.create` is DESTROY-CAPABLE: GCS has no separate
// content-update permission, so create is the overwrite permission, and on a
// bucket without versioning an overwrite destroys the previous generation as
// thoroughly as a delete would. "The publisher may create and get but not
// delete" therefore does not mean "the publisher cannot destroy data". What
// makes it true is the code always sending a create-if-absent precondition,
// which is an application-level property; the dedicated bucket is the boundary
// -- not an IAM condition this code could assert and does not have.
const (
	PermissionObjectCreate = "storage.objects.create"
	PermissionObjectGet    = "storage.objects.get"
	PermissionObjectList   = "storage.objects.list"
	PermissionObjectDelete = "storage.objects.delete"

	// PermissionObjectUpdate is the permission that REWRITES an object's
	// metadata. GCS has no separate content-update permission -- an overwrite
	// is a create -- so this is exactly and only the marker-rewrite authority
	// Req 22's second sentence forbids the publisher, and it was in neither
	// this matrix nor the role expansion.
	PermissionObjectUpdate = "storage.objects.update"

	PermissionBucketGet          = "storage.buckets.get"
	PermissionBucketGetIAMPolicy = "storage.buckets.getIamPolicy"
	PermissionBucketUpdate       = "storage.buckets.update"
	PermissionBucketSetIAMPolicy = "storage.buckets.setIamPolicy"
)

// requiredPermissions is what each role must hold to do its work.
func requiredPermissions(role output.PrincipalRole) []string {
	switch role {
	case output.PrincipalPublisher:
		return []string{PermissionObjectCreate, PermissionObjectGet}
	case output.PrincipalInventory:
		return []string{PermissionObjectList, PermissionObjectGet}
	case output.PrincipalReclaimer:
		return []string{PermissionObjectGet, PermissionObjectDelete}
	case output.PrincipalPolicyAttestor:
		return []string{PermissionBucketGet, PermissionBucketGetIAMPolicy}
	}

	return nil
}

// forbiddenPermissions is what each role must NOT hold.
//
// Note that every role is forbidden the two MUTATION permissions. All runtime
// principals lack lifecycle and IAM mutation authority (Req 54): policy
// administration remains an operator trust root, and a runtime identity that
// could rewrite the lifecycle policy could delete everything this plane
// protects by editing one rule.
func forbiddenPermissions(role output.PrincipalRole) []string {
	// Every role is forbidden the two administration permissions AND
	// storage.objects.update. No role in this plane ever rewrites an object's
	// metadata: the marker is written once, at creation, by the publisher, and
	// "the publisher cannot update the marker" (Req 22) is the sentence the
	// whole ownership-evidence story rests on. It was enforced by nothing but
	// the Go Handle type having no update method, which is a property of this
	// binary rather than of the principal.
	administration := []string{
		PermissionBucketUpdate, PermissionBucketSetIAMPolicy, PermissionObjectUpdate,
	}

	switch role {
	case output.PrincipalPublisher:
		return append([]string{PermissionObjectDelete, PermissionObjectList}, administration...)
	case output.PrincipalInventory:
		return append([]string{PermissionObjectCreate, PermissionObjectDelete}, administration...)
	case output.PrincipalReclaimer:
		return append([]string{PermissionObjectCreate, PermissionObjectList}, administration...)
	case output.PrincipalPolicyAttestor:
		// The attestor's whole value is that its compromise costs the
		// assessment rather than the data, so it holds no object permission at
		// all -- not even get.
		return append([]string{
			PermissionObjectCreate, PermissionObjectGet,
			PermissionObjectList, PermissionObjectDelete,
		}, administration...)
	}

	return nil
}

// Expectation is the deployment's configured identities, against which an
// observed binding set is judged.
//
// Principals maps each role to the exact cloud identity that role's Kubernetes
// service account is bound to. Activation verifies the configured bucket, the
// KSA-to-cloud identities and the exact IAM grants rather than assuming the
// chart created them (Req 54), and this is the shape of "what was configured"
// that the verification compares against.
type Expectation struct {
	BucketFingerprint string
	ActivationEpoch   executioncontrol.ActivationEpoch
	Principals        map[output.PrincipalRole]string
}

func (expectation Expectation) Validate() error {
	if expectation.BucketFingerprint == "" {
		return fmt.Errorf("%w: the expectation names no bucket", output.ErrIncomplete)
	}
	if expectation.ActivationEpoch == 0 {
		return fmt.Errorf("%w: the expectation names no activation epoch", output.ErrIncomplete)
	}
	seen := map[string]output.PrincipalRole{}
	for _, role := range output.PrincipalRoles() {
		identity, ok := expectation.Principals[role]
		if !ok || identity == "" {
			return fmt.Errorf("%w: no cloud identity is configured for the %s role; shared or "+
				"missing identities are an activation failure", output.ErrIncomplete, role)
		}
		if other, clash := seen[identity]; clash {
			return fmt.Errorf("%w: the %s and %s roles are configured with one identity %q. A "+
				"Kubernetes service account is Pod-wide, so a shared identity is shared "+
				"permission and the isolation is not real", output.ErrConflict, other, role,
				identity)
		}
		seen[identity] = role
	}

	return nil
}

// DeriveSnapshot turns one authoritative whole-bucket policy read into the
// attestation a later reader decides freshness for itself from.
//
// It is safe only when the bucket has NO rule that can remove an object and the
// reading is for the bucket that was expected. The state is computed here and
// the freshness is not: the snapshot records when it was observed, and every
// admission gate compares that against its own bound, because a boolean
// computed by a controller is a boolean that was true when that controller ran.
//
// ObservedAt here is stamped by the attestor PROCESS, from the clock wired into
// BucketPolicySource, while `now()` in hangar_check_policy_admission is
// PostgreSQL's -- so for a while the bounded-staleness promise of Req 51/52 was
// a comparison between two clocks, and an attestor running fast by more than
// MaxPolicyEvidenceAge made stale evidence look permanently fresh. That is
// closed on the control-plane side, where it had to be: the repository stores
// observed_at as least(now(), this stamp) and refuses a reading dated well
// ahead of the database, so this value is an upper bound on the truth and never
// a way to buy freshness. See hangarPolicyObservationOnTheDatabaseClock in
// atc/db. What this package supplies is still the attestor's own observation,
// and it is right that it does: a slow clock dates the reading early, evidence
// expires early, and the plane fails closed.
func DeriveSnapshot(expectation Expectation, observation output.BucketLifetimePolicy) (output.PolicySnapshot, []output.PolicyFinding, error) {
	if err := expectation.Validate(); err != nil {
		return output.PolicySnapshot{}, nil, err
	}

	snapshot := output.PolicySnapshot{
		ProtocolVersion:      output.ProtocolVersion,
		ActivationEpoch:      expectation.ActivationEpoch,
		BucketFingerprint:    observation.BucketFingerprint,
		Metageneration:       observation.Metageneration,
		PolicyHash:           observation.PolicyHash(),
		LifecycleDeleteRules: observation.RemovalRules(),
		State:                output.PolicySafe,
		ObservedAt:           observation.ObservedAt,
	}

	var findings []output.PolicyFinding
	for _, rule := range observation.Rules {
		if !rule.RemovesObjects() {
			continue
		}
		findings = append(findings, output.PolicyFinding{
			Violation: output.ViolationLifecycleDeleteRule,
			Subject:   observation.BucketFingerprint,
			Detail: truncate(fmt.Sprintf("lifecycle rule %q with condition %q can remove an "+
				"object; the dedicated output bucket has none", rule.Action, rule.Condition)),
		})
	}

	if observation.BucketFingerprint != expectation.BucketFingerprint {
		findings = append(findings, output.PolicyFinding{
			Violation: output.ViolationWrongPrincipal,
			Subject:   observation.BucketFingerprint,
			Detail: truncate(fmt.Sprintf("the policy read is for %q and this deployment is "+
				"configured for %q", observation.BucketFingerprint,
				expectation.BucketFingerprint)),
		})
	}

	if len(findings) > 0 {
		snapshot.State = output.PolicyAtRisk
	}
	if err := snapshot.Validate(); err != nil {
		// A snapshot that does not validate is a reading this plane cannot
		// stand behind, and "unknown" is the state for that. It is not benign:
		// it blocks admission exactly as at_risk does.
		snapshot.State = output.PolicyUnknown
		snapshot.LifecycleDeleteRules = 0

		return snapshot, append(findings, output.PolicyFinding{
			Violation: output.ViolationEvidenceUnreadable,
			Subject:   observation.BucketFingerprint,
			Detail:    truncate(err.Error()),
		}), nil
	}

	return snapshot, findings, nil
}

// DeriveBindingFindings judges an observed IAM binding set against the
// configured identities.
//
// It reports what the bindings SAY. It does not claim IAM enforces anything:
// no fake enforces a binding, and an assertion about enforcement is a real-GCS
// observation recorded with its date and project (Phase 9). What this catches
// is the configuration drift that a real enforcement would then act on.
func DeriveBindingFindings(expectation Expectation, bindings output.PrincipalBindings) []output.PolicyFinding {
	if err := expectation.Validate(); err != nil {
		return []output.PolicyFinding{{
			Violation: output.ViolationEvidenceUnreadable,
			Subject:   expectation.BucketFingerprint,
			Detail:    truncate(err.Error()),
		}}
	}

	var findings []output.PolicyFinding

	if bindings.BucketFingerprint != expectation.BucketFingerprint {
		findings = append(findings, output.PolicyFinding{
			Violation: output.ViolationWrongPrincipal,
			Subject:   bindings.BucketFingerprint,
			Detail: truncate(fmt.Sprintf("the IAM read is for %q and this deployment is "+
				"configured for %q", bindings.BucketFingerprint, expectation.BucketFingerprint)),
		})
	}

	// An IAM role nobody could expand, held by anyone on this bucket. It is
	// reported BEFORE the required/forbidden comparison below, because it is
	// the reason that comparison cannot be trusted for this principal: the
	// matrix tests held[permission] against a forbidden list, and a role whose
	// contents are unknown contributes no permission to test. A publisher bound
	// roles/storage.objectCreator plus a custom role containing
	// storage.objects.delete satisfied its required set and tripped no excess
	// finding, and that is what this catches.
	var unexpanded []output.PrincipalRole
	for role := range bindings.UnrecognisedRoles {
		unexpanded = append(unexpanded, role)
	}
	sort.Slice(unexpanded, func(i, j int) bool { return unexpanded[i] < unexpanded[j] })
	for _, role := range unexpanded {
		subject := expectation.Principals[role]
		if subject == "" {
			subject = string(role)
		}
		for _, name := range bindings.UnrecognisedRoles[role] {
			findings = append(findings, output.PolicyFinding{
				Violation: output.ViolationUnrecognisedRole,
				Subject:   subject,
				Detail: truncate(fmt.Sprintf("the %s principal holds %q, which this plane "+
					"cannot expand into permissions. Only the project that defined it knows "+
					"what it grants, so it is unknown and therefore unsafe: a custom role "+
					"carrying storage.objects.delete is indistinguishable from a harmless one "+
					"here", role, name)),
			})
		}
	}

	for _, role := range output.PrincipalRoles() {
		held := permissionSet(bindings.Permissions[role])

		for _, permission := range requiredPermissions(role) {
			if !held[permission] {
				findings = append(findings, output.PolicyFinding{
					Violation: output.ViolationInsufficientRole,
					Subject:   expectation.Principals[role],
					Detail: truncate(fmt.Sprintf("the %s principal does not hold %s; a role that "+
						"cannot do its work is a plane that stops silently", role, permission)),
				})
			}
		}
		for _, permission := range forbiddenPermissions(role) {
			if held[permission] {
				findings = append(findings, output.PolicyFinding{
					Violation: output.ViolationExcessRole,
					Subject:   expectation.Principals[role],
					Detail: truncate(fmt.Sprintf("the %s principal holds %s, which its role "+
						"forbids", role, permission)),
				})
			}
		}
	}

	// Anything bound that is not one of the four roles. A dedicated bucket has
	// no other object-plane identity on it, and prefix-only isolation inside a
	// shared bucket is exactly what Req 54 refuses.
	known := map[output.PrincipalRole]bool{}
	for _, role := range output.PrincipalRoles() {
		known[role] = true
	}
	var strangers []output.PrincipalRole
	for role := range bindings.Permissions {
		if !known[role] {
			strangers = append(strangers, role)
		}
	}
	for role := range bindings.UnrecognisedRoles {
		if !known[role] && !containsRole(strangers, role) {
			strangers = append(strangers, role)
		}
	}
	sort.Slice(strangers, func(i, j int) bool { return strangers[i] < strangers[j] })
	for _, stranger := range strangers {
		findings = append(findings, output.PolicyFinding{
			Violation: output.ViolationSharedBucket,
			Subject:   string(stranger),
			Detail: truncate(fmt.Sprintf("%q holds %s on the output bucket and is not one of "+
				"this plane's four roles; the dedicated bucket contains only output-plane "+
				"objects and prefix-only isolation is not a substitute",
				stranger, strings.Join(bindings.Permissions[stranger], ", "))),
		})
	}

	return findings
}

// Deferred: mixed-cohort detection needs a per-role observed identity the IAM
// read does not return; Phase 8, with the activation verification
//
// DeriveCohortFindings reports principals bound from another activation epoch.
//
// Rotation creates a new epoch rather than replacing a key in place, so two
// cohorts on one bucket is two planes disagreeing about whose object is whose --
// and the one that loses that argument deletes the other's.
func DeriveCohortFindings(expectation Expectation, observed map[output.PrincipalRole]string) []output.PolicyFinding {
	var findings []output.PolicyFinding

	for _, role := range output.PrincipalRoles() {
		identity, ok := observed[role]
		if !ok {
			continue
		}
		if identity != expectation.Principals[role] {
			findings = append(findings, output.PolicyFinding{
				Violation: output.ViolationMixedCohort,
				Subject:   identity,
				Detail: truncate(fmt.Sprintf("the %s role is bound to %q and this epoch is "+
					"configured for %q; two cohorts on one bucket is two planes disagreeing "+
					"about whose object is whose", role, identity,
					expectation.Principals[role])),
			})
		}
	}

	return findings
}

// AtRisk reports whether any finding moves the epoch out of safe.
//
// Every member does. There is no advisory violation in this vocabulary, because
// a violation this plane would carry on publishing through is one it should not
// be recording.
func AtRisk(findings []output.PolicyFinding) bool { return len(findings) > 0 }

func containsRole(roles []output.PrincipalRole, needle output.PrincipalRole) bool {
	for _, role := range roles {
		if role == needle {
			return true
		}
	}

	return false
}

func permissionSet(permissions []string) map[string]bool {
	set := make(map[string]bool, len(permissions))
	for _, permission := range permissions {
		set[strings.TrimSpace(permission)] = true
	}

	return set
}

func truncate(detail string) string {
	if len(detail) <= output.MaxFindingDetailBytes {
		return detail
	}

	return detail[:output.MaxFindingDetailBytes]
}
