package policy_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/policy"
)

// What an attestation concludes from an authoritative response.
//
// The substrate here is the RESPONSE, not an enforcement fake, and the plan
// says why: an IAM binding and a lifecycle rule are what no fake enforces, so a
// suite built on one would be asserting the fake. What is testable here is the
// derivation -- given this reading, what does this plane conclude, and what does
// it refuse to conclude -- and enforcement is a real-GCS observation recorded
// with its date and project (Phase 9, ACs 16/17).

const bucket = "gs://output-bucket"

func expectation() policy.Expectation {
	return policy.Expectation{
		BucketFingerprint: bucket,
		ActivationEpoch:   7,
		Principals: map[output.PrincipalRole]string{
			output.PrincipalPublisher:      "publisher@project.iam.gserviceaccount.com",
			output.PrincipalInventory:      "inventory@project.iam.gserviceaccount.com",
			output.PrincipalReclaimer:      "reclaimer@project.iam.gserviceaccount.com",
			output.PrincipalPolicyAttestor: "attestor@project.iam.gserviceaccount.com",
		},
	}
}

func safePolicy() output.BucketLifetimePolicy {
	return output.BucketLifetimePolicy{
		BucketFingerprint: bucket,
		Metageneration:    3,
		Rules: []output.LifecycleRule{
			{Action: "SetStorageClass", Condition: "age>30"},
		},
		ObservedAt: output.NewTimestamp(time.Now().UTC()),
	}
}

func conformingBindings() output.PrincipalBindings {
	return output.PrincipalBindings{
		BucketFingerprint: bucket,
		Permissions: map[output.PrincipalRole][]string{
			output.PrincipalPublisher: {
				policy.PermissionObjectCreate, policy.PermissionObjectGet,
			},
			output.PrincipalInventory: {
				policy.PermissionObjectList, policy.PermissionObjectGet,
			},
			output.PrincipalReclaimer: {
				policy.PermissionObjectGet, policy.PermissionObjectDelete,
			},
			output.PrincipalPolicyAttestor: {
				policy.PermissionBucketGet, policy.PermissionBucketGetIAMPolicy,
			},
		},
	}
}

func violationsOf(findings []output.PolicyFinding) map[output.PolicyViolation]int {
	counted := map[output.PolicyViolation]int{}
	for _, finding := range findings {
		counted[finding.Violation]++
	}

	return counted
}

func TestASafePolicyIsSafeAndAnyRemovalRuleIsNot(t *testing.T) {
	// The control, first: a bucket with no rule that can remove an object is
	// safe, so the at-risk rows below cannot pass for a derivation that refuses
	// everything.
	snapshot, findings, err := policy.DeriveSnapshot(expectation(), safePolicy())
	if err != nil {
		t.Fatalf("deriving a safe snapshot: %v", err)
	}
	if snapshot.State != output.PolicySafe {
		t.Fatalf("a bucket with no removal rule derived %q", snapshot.State)
	}
	if len(findings) != 0 {
		t.Errorf("a safe bucket produced findings: %v", findings)
	}
	if snapshot.LifecycleDeleteRules != 0 {
		t.Errorf("a safe snapshot counts %d removal rules", snapshot.LifecycleDeleteRules)
	}
	if snapshot.Metageneration != 3 {
		t.Errorf("the snapshot records metageneration %d, and the reading said 3",
			snapshot.Metageneration)
	}
	if snapshot.PolicyHash == "" {
		t.Error("the snapshot carries no policy hash; without it a later reader cannot tell an " +
			"unchanged policy from an unread one")
	}

	// And every spelling of a removal rule. The match is deliberately loose:
	// over-reporting a rule costs an operator one look, and under-reporting one
	// is the failure the whole attestation exists to catch.
	for _, action := range []string{"Delete", "delete", "DELETE", " Delete ", "DeleteObject"} {
		unsafe := safePolicy()
		unsafe.Rules = append(unsafe.Rules, output.LifecycleRule{
			Action: action, Condition: "age>1",
		})

		snapshot, findings, err := policy.DeriveSnapshot(expectation(), unsafe)
		if err != nil {
			t.Fatalf("deriving %q: %v", action, err)
		}
		if snapshot.State != output.PolicyAtRisk {
			t.Errorf("a bucket with a %q lifecycle rule derived %q", action, snapshot.State)
		}
		if counted := violationsOf(findings); counted[output.ViolationLifecycleDeleteRule] != 1 {
			t.Errorf("a %q rule produced %d lifecycle findings", action,
				counted[output.ViolationLifecycleDeleteRule])
		}
		if snapshot.LifecycleDeleteRules != 1 {
			t.Errorf("a %q rule counted %d", action, snapshot.LifecycleDeleteRules)
		}
	}
}

func TestThePolicyHashDistinguishesAnUnchangedPolicyFromAnUnreadOne(t *testing.T) {
	first := safePolicy()
	same := safePolicy()
	same.ObservedAt = output.NewTimestamp(time.Now().Add(time.Hour).UTC())

	if first.PolicyHash() != same.PolicyHash() {
		t.Error("two readings of one unchanged policy hash differently, so every refresh would " +
			"look like a change")
	}

	changed := safePolicy()
	changed.Rules = append(changed.Rules, output.LifecycleRule{Action: "Delete", Condition: "age>1"})
	if changed.PolicyHash() == first.PolicyHash() {
		t.Error("adding a lifecycle rule did not change the policy hash")
	}

	// Metageneration is part of it too, and a hash that ignored it would let a
	// reader treat "the bucket metadata moved" as "the rules are the same".
	touched := safePolicy()
	touched.Metageneration = 4
	if touched.PolicyHash() == first.PolicyHash() {
		t.Error("a changed metageneration did not change the policy hash")
	}
}

func TestAPolicyReadForAnotherBucketIsNeverSafe(t *testing.T) {
	somebodyElses := safePolicy()
	somebodyElses.BucketFingerprint = "gs://not-ours"

	snapshot, findings, err := policy.DeriveSnapshot(expectation(), somebodyElses)
	if err != nil {
		t.Fatalf("deriving: %v", err)
	}
	if snapshot.State == output.PolicySafe {
		t.Error("a clean policy read for the WRONG bucket was derived safe; the attestation " +
			"would be proving something about a bucket this deployment does not use")
	}
	if counted := violationsOf(findings); counted[output.ViolationWrongPrincipal] != 1 {
		t.Errorf("a foreign bucket produced %v", counted)
	}
}

func TestAnUnreadablePolicyIsUnknownAndUnknownBlocksExactlyAsAtRiskDoes(t *testing.T) {
	unreadable := safePolicy()
	unreadable.Metageneration = 0

	snapshot, findings, err := policy.DeriveSnapshot(expectation(), unreadable)
	if err != nil {
		t.Fatalf("deriving: %v", err)
	}
	if snapshot.State != output.PolicyUnknown {
		t.Errorf("a snapshot that does not validate derived %q", snapshot.State)
	}
	if counted := violationsOf(findings); counted[output.ViolationEvidenceUnreadable] != 1 {
		t.Errorf("an unreadable policy produced %v", counted)
	}
	if output.PolicyUnknown.AdmitsNewWork() {
		t.Error("unknown admits new work. `we have not checked` and `the check failed` are the " +
			"same amount of evidence, and neither is a safe policy")
	}
	if !output.PolicySafe.AdmitsNewWork() {
		t.Error("safe does not admit new work, so nothing would ever publish")
	}
	if output.PolicyAtRisk.AdmitsNewWork() {
		t.Error("at_risk admits new work")
	}
}

func TestStalenessIsRefusedAtTheDetectionBoundAndNotBefore(t *testing.T) {
	// A functioning monitor detects a change within the bound; it does not
	// prevent a deletion inside it. That is the documented window, and these
	// two rows are its edges.
	if err := output.ValidatePolicyEvidenceAge(output.MaxPolicyEvidenceAge - time.Second); err != nil {
		t.Errorf("evidence one second inside the bound was refused: %v", err)
	}
	if err := output.ValidatePolicyEvidenceAge(output.MaxPolicyEvidenceAge + time.Second); err == nil {
		t.Error("evidence one second past the bound was accepted; a stale check is not a safe one")
	} else if !errors.Is(err, output.ErrAtRisk) {
		t.Errorf("stale evidence is typed %v, expected %v", err, output.ErrAtRisk)
	}
	if err := output.ValidatePolicyEvidenceAge(-time.Hour); err == nil {
		t.Error("evidence dated an hour in the future was accepted")
	}
	if policy.StaleAfter != output.MaxPolicyEvidenceAge {
		t.Errorf("the attestor thinks it must run every %s and the gate refuses after %s; the "+
			"attestor would be late by construction", policy.StaleAfter,
			output.MaxPolicyEvidenceAge)
	}
}

func TestTheExactPermissionMatrixIsWhatIsChecked(t *testing.T) {
	// The control: the conforming set produces nothing.
	if findings := policy.DeriveBindingFindings(expectation(), conformingBindings()); len(findings) != 0 {
		t.Fatalf("the conforming binding set produced findings: %v", findings)
	}

	// Excess, per role, one permission at a time. Each of these is a real
	// consequence rather than a tidiness rule: a publisher with delete can
	// remove a claimed generation, a reclaimer with list can enumerate the
	// bucket, an inventory with create can write one.
	excess := []struct {
		role       output.PrincipalRole
		permission string
	}{
		{output.PrincipalPublisher, policy.PermissionObjectDelete},
		{output.PrincipalPublisher, policy.PermissionObjectList},
		{output.PrincipalInventory, policy.PermissionObjectCreate},
		{output.PrincipalInventory, policy.PermissionObjectDelete},
		{output.PrincipalReclaimer, policy.PermissionObjectCreate},
		{output.PrincipalReclaimer, policy.PermissionObjectList},
		{output.PrincipalPolicyAttestor, policy.PermissionObjectGet},
		{output.PrincipalPolicyAttestor, policy.PermissionObjectDelete},
	}
	for _, row := range excess {
		bindings := conformingBindings()
		bindings.Permissions[row.role] = append(bindings.Permissions[row.role], row.permission)

		findings := policy.DeriveBindingFindings(expectation(), bindings)
		if counted := violationsOf(findings); counted[output.ViolationExcessRole] != 1 {
			t.Errorf("the %s principal holding %s produced %v", row.role, row.permission, counted)
		}
	}

	// Every role is forbidden the two administration permissions. A runtime
	// identity that could rewrite the lifecycle policy could delete everything
	// this plane protects by editing one rule.
	for _, role := range output.PrincipalRoles() {
		for _, permission := range []string{
			policy.PermissionBucketUpdate, policy.PermissionBucketSetIAMPolicy,
		} {
			bindings := conformingBindings()
			bindings.Permissions[role] = append(bindings.Permissions[role], permission)

			findings := policy.DeriveBindingFindings(expectation(), bindings)
			if counted := violationsOf(findings); counted[output.ViolationExcessRole] == 0 {
				t.Errorf("the %s principal holding %s produced no excess-role finding; policy "+
					"administration is an operator trust root", role, permission)
			}
		}
	}

	// And insufficient, which is a violation rather than a warning: a role that
	// cannot do its work is a plane that stops silently.
	for _, role := range output.PrincipalRoles() {
		bindings := conformingBindings()
		bindings.Permissions[role] = nil

		findings := policy.DeriveBindingFindings(expectation(), bindings)
		if counted := violationsOf(findings); counted[output.ViolationInsufficientRole] == 0 {
			t.Errorf("the %s principal holding nothing produced %v", role, counted)
		}
	}
}

func TestTheAttestationClaimsNothingAboutMetadataOnlyOrPrefixScopedPermission(t *testing.T) {
	// Req 41 is explicit: there is no metadata-only object permission and list
	// authority cannot be narrowed to a prefix. A matrix that expected either
	// would be asserting a permission that does not exist -- and would make a
	// correctly configured bucket look wrong forever.
	//
	// The check is on the permission strings this package compares: none of
	// them may be an invention.
	real := map[string]bool{
		"storage.objects.create": true, "storage.objects.get": true,
		"storage.objects.list": true, "storage.objects.delete": true,
		"storage.buckets.get": true, "storage.buckets.getIamPolicy": true,
		"storage.buckets.update": true, "storage.buckets.setIamPolicy": true,
	}
	for _, permission := range []string{
		policy.PermissionObjectCreate, policy.PermissionObjectGet, policy.PermissionObjectList,
		policy.PermissionObjectDelete, policy.PermissionBucketGet,
		policy.PermissionBucketGetIAMPolicy, policy.PermissionBucketUpdate,
		policy.PermissionBucketSetIAMPolicy,
	} {
		if !real[permission] {
			t.Errorf("%q is not a GCS permission; a matrix that checks an invented permission "+
				"reports a violation nobody can fix", permission)
		}
		if strings.Contains(permission, "metadata") || strings.Contains(permission, "prefix") {
			t.Errorf("%q claims a metadata-only or prefix-scoped permission, and Req 41 says "+
				"GCS has neither", permission)
		}
	}

	// Inventory holds object get, and that get reads BODIES as well as
	// metadata. The code does not download them; IAM is not described as
	// enforcing that.
	for _, role := range []output.PrincipalRole{
		output.PrincipalInventory, output.PrincipalReclaimer,
	} {
		bindings := conformingBindings()
		findings := policy.DeriveBindingFindings(expectation(), bindings)
		if len(findings) != 0 {
			t.Fatalf("the conforming set is not conforming: %v", findings)
		}
		held := false
		for _, permission := range bindings.Permissions[role] {
			if permission == policy.PermissionObjectGet {
				held = true
			}
		}
		if !held {
			t.Errorf("the %s role does not hold object get, so this deployment cannot stat an "+
				"exact generation at all", role)
		}
	}
}

func TestAnIdentityOutsideTheFourRolesIsASharedBucket(t *testing.T) {
	bindings := conformingBindings()
	bindings.Permissions["cache-daemon"] = []string{
		policy.PermissionObjectCreate, policy.PermissionObjectGet,
	}

	findings := policy.DeriveBindingFindings(expectation(), bindings)
	counted := violationsOf(findings)
	if counted[output.ViolationSharedBucket] != 1 {
		t.Errorf("an identity outside the four roles produced %v. The dedicated output bucket "+
			"contains only output-plane objects, and prefix-only isolation in a mixed bucket is "+
			"an activation failure", counted)
	}
	if !policy.AtRisk(findings) {
		t.Error("a shared bucket did not move the epoch out of safe")
	}
}

func TestTwoRolesOnOneIdentityIsRefusedBeforeAnythingIsRead(t *testing.T) {
	shared := expectation()
	shared.Principals[output.PrincipalReclaimer] = shared.Principals[output.PrincipalPublisher]

	if err := shared.Validate(); err == nil {
		t.Fatal("two roles configured with one identity was accepted. A Kubernetes service " +
			"account is Pod-wide: a shared identity is shared permission, and the publisher " +
			"would hold delete")
	} else if !errors.Is(err, output.ErrConflict) {
		t.Errorf("a shared identity is typed %v", err)
	}

	// A missing identity is the same class of activation failure.
	missing := expectation()
	delete(missing.Principals, output.PrincipalInventory)
	if err := missing.Validate(); err == nil {
		t.Error("a role with no configured identity was accepted")
	}
}

func TestAMixedCohortIsDetected(t *testing.T) {
	// The control: this epoch's own identities produce nothing.
	if findings := policy.DeriveCohortFindings(expectation(),
		expectation().Principals); len(findings) != 0 {
		t.Fatalf("this epoch's own identities produced findings: %v", findings)
	}

	observed := map[output.PrincipalRole]string{}
	for role, identity := range expectation().Principals {
		observed[role] = identity
	}
	observed[output.PrincipalReclaimer] = "reclaimer-epoch-6@project.iam.gserviceaccount.com"

	findings := policy.DeriveCohortFindings(expectation(), observed)
	if counted := violationsOf(findings); counted[output.ViolationMixedCohort] != 1 {
		t.Errorf("a previous epoch's reclaimer bound beside this one's produced %v. Rotation "+
			"creates a new epoch rather than replacing a key in place, and two cohorts on one "+
			"bucket is two planes disagreeing about whose object is whose", counted)
	}
}

// The API outage, and the recovery after it.
//
// A read that fails is not "unknown and therefore fine": the attestor reports
// at_risk with the reason, because a monitor that goes quiet and a bucket that
// is safe look identical from the outside.
func TestAnOutageIsAtRiskAndAFreshReadingRecovers(t *testing.T) {
	namespace, err := output.DeriveNamespace(output.NamespaceConfig{
		Store:            output.StoreGCS,
		Bucket:           "output-bucket",
		DeploymentPrefix: "deployments/blue",
		TenantID:         "tenant-policy",
		ActivationEpoch:  7,
	})
	if err != nil {
		t.Fatalf("deriving the namespace: %v", err)
	}

	source := &scriptedSource{}
	attestor, err := policy.New(namespace, source, output.ClockFunc(time.Now))
	if err != nil {
		t.Fatalf("building the attestor: %v", err)
	}

	source.policyErr = errors.New("the metadata API did not answer")
	if _, err := attestor.ReadLifetimePolicy(context.Background()); err == nil {
		t.Fatal("an outage produced a policy reading")
	} else if !errors.Is(err, output.ErrAtRisk) {
		t.Errorf("an outage is typed %v, expected %v", err, output.ErrAtRisk)
	}

	source.policyErr = nil
	source.policy = output.PolicySnapshot{
		ProtocolVersion:   output.ProtocolVersion,
		ActivationEpoch:   7,
		BucketFingerprint: bucket,
		Metageneration:    3,
		PolicyHash:        "sha256:whatever",
		State:             output.PolicySafe,
		ObservedAt:        output.NewTimestamp(time.Now().UTC()),
	}
	if _, err := attestor.ReadLifetimePolicy(context.Background()); err != nil {
		t.Errorf("the reading after the outage failed: %v", err)
	}

	// An IAM read that comes back EMPTY is an unread bucket, not an unbound
	// one, and saying so is the difference between "nobody can touch this" and
	// "we did not look".
	source.bindings = output.PrincipalBindings{BucketFingerprint: bucket}
	if _, err := attestor.ReadPrincipalBindings(context.Background()); err == nil {
		t.Error("a bucket that reported no bindings at all was accepted as correctly bound")
	}
}

type scriptedSource struct {
	policy      output.PolicySnapshot
	policyErr   error
	bindings    output.PrincipalBindings
	bindingsErr error
}

func (source *scriptedSource) ReadLifetimePolicy(context.Context) (output.PolicySnapshot, error) {
	return source.policy, source.policyErr
}

func (source *scriptedSource) ReadPrincipalBindings(context.Context) (output.PrincipalBindings, error) {
	return source.bindings, source.bindingsErr
}

// A role cannot silently broaden.
//
// Every guard above asks what the attestation concludes about a BUCKET. This
// one asks what the matrix itself says, and it is the half that catches a change
// nobody meant: the four roles are four cloud identities whose whole value is
// that each holds strictly less than the union, and an edit that added one
// permission to one role's required set would make that role's principal
// correctly configured while holding more than it did yesterday -- with every
// bucket-facing assertion still green.
//
// It is derived from the matrix rather than from a list, so a fifth role
// inherits it.
func TestNoRoleCanBroadenWithoutThisFailing(t *testing.T) {
	// The exact matrix, frozen. A changed row has to be changed HERE too, which
	// is the whole mechanism: the permission a role holds is a security
	// boundary, and moving one should cost a deliberate edit in a file that
	// says so.
	frozen := map[output.PrincipalRole][]string{
		output.PrincipalPublisher:      {"storage.objects.create", "storage.objects.get"},
		output.PrincipalInventory:      {"storage.objects.list", "storage.objects.get"},
		output.PrincipalReclaimer:      {"storage.objects.get", "storage.objects.delete"},
		output.PrincipalPolicyAttestor: {"storage.buckets.get", "storage.buckets.getIamPolicy"},
	}

	for role, expected := range frozen {
		// The required set is measured through the derivation rather than read
		// from an exported list, so this cannot pass against a matrix that is
		// declared correctly and applied wrongly. A principal holding exactly
		// the frozen set produces no finding; a principal missing any one of
		// them produces an insufficient-role finding.
		bindings := conformingBindings()
		bindings.Permissions[role] = expected
		if findings := policy.DeriveBindingFindings(expectation(), bindings); len(findings) != 0 {
			t.Errorf("the frozen %s set %v was judged non-conforming: %v", role, expected, findings)
		}

		for index := range expected {
			short := append([]string{}, expected[:index]...)
			short = append(short, expected[index+1:]...)
			bindings := conformingBindings()
			bindings.Permissions[role] = short
			counted := violationsOf(policy.DeriveBindingFindings(expectation(), bindings))
			if counted[output.ViolationInsufficientRole] == 0 {
				t.Errorf("the %s role without %s produced no insufficient-role finding, so the "+
					"frozen matrix has drifted from what is actually required",
					role, expected[index])
			}
		}
	}

	// And nothing outside a role's own set is tolerated. Every permission this
	// package knows about, offered to every role that does not need it, has to
	// be an excess-role finding -- which is the broadening this guard is named
	// for.
	every := []string{
		policy.PermissionObjectCreate, policy.PermissionObjectGet, policy.PermissionObjectList,
		policy.PermissionObjectDelete, policy.PermissionBucketGet,
		policy.PermissionBucketGetIAMPolicy, policy.PermissionBucketUpdate,
		policy.PermissionBucketSetIAMPolicy,
	}
	checked := 0
	for role, own := range frozen {
		held := map[string]bool{}
		for _, permission := range own {
			held[permission] = true
		}
		for _, permission := range every {
			if held[permission] {
				continue
			}
			// The two bucket reads are the attestor's and are harmless to the
			// object roles, so they are not in anybody's forbidden set. Every
			// OBJECT permission, and both mutations, are.
			if permission == policy.PermissionBucketGet ||
				permission == policy.PermissionBucketGetIAMPolicy {
				continue
			}
			bindings := conformingBindings()
			bindings.Permissions[role] = append(append([]string{}, own...), permission)
			counted := violationsOf(policy.DeriveBindingFindings(expectation(), bindings))
			if counted[output.ViolationExcessRole] == 0 {
				t.Errorf("the %s role holding %s produced no excess-role finding; a role that "+
					"can quietly gain a permission is not an isolation boundary",
					role, permission)
			}
			checked++
		}
	}
	if checked < 16 {
		t.Errorf("this guard checked %d role/permission pairs, which is too few to be the whole "+
			"matrix; it would pass for a table that lost most of its rows", checked)
	}
}
