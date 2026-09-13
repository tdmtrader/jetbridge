package gcs

// The traversal that turns a real IAM policy into the permission matrix's
// input.
//
// It had no test anywhere in the tree, in either tier, and it was wrong in the
// direction that reads "safe". fake-gcs-server implements no bucket IAM, so
// every test that reached ReadPrincipalBindings could assert only that the read
// failed closed; every other IAM test drives DeriveBindingFindings with a
// hand-built PrincipalBindings, which is downstream of this function. These
// cases are over the binding list itself, which needs no server at all.

import (
	"sort"
	"strings"
	"testing"

	iampb "cloud.google.com/go/iam/apiv1/iampb"
	expr "google.golang.org/genproto/googleapis/type/expr"

	"github.com/concourse/concourse/hangar/output"
)

const (
	publisherIdentity = "publisher@project.iam.gserviceaccount.com"
	strangerIdentity  = "stranger@project.iam.gserviceaccount.com"
)

func planeIdentities() map[output.PrincipalRole]string {
	return map[output.PrincipalRole]string{
		output.PrincipalPublisher: "serviceAccount:" + publisherIdentity,
	}
}

// The defect, as measured: iam.Policy.Roles() returns one entry per BINDING and
// Members(role) answers from the FIRST binding with that name, so a policy with
// two bindings for one role was read as "the first binding's members, twice"
// and the second binding's members never. A stranger holding object-create
// behind a duplicate role name was invisible to the shared-bucket arm.
func TestEveryBindingIsVisitedEvenWhenTwoNameTheSameRole(t *testing.T) {
	bindings := bindingsOf("gs://output", []*iampb.Binding{
		{Role: "roles/storage.objectCreator", Members: []string{"serviceAccount:" + publisherIdentity}},
		{Role: "roles/storage.objectCreator", Members: []string{"serviceAccount:" + strangerIdentity}},
	}, planeIdentities())

	if got := bindings.Permissions[output.PrincipalRole(strangerIdentity)]; len(got) == 0 {
		t.Errorf("a stranger holding storage.objects.create behind a DUPLICATE role binding is "+
			"invisible to the attestation.\n\nPermissions: %v\n\nThis is the shape of the "+
			"defect: the whole plane's fail-closed behaviour is downstream of this traversal, "+
			"and a grant it cannot see is a grant nobody will ever be told about.",
			bindings.Permissions)
	}
	if got := bindings.Permissions[output.PrincipalPublisher]; len(got) != 1 {
		t.Errorf("the publisher's single objects.create binding expanded to %v; a traversal that "+
			"reads one binding twice double-counts every member of it", got)
	}
}

// A condition this plane cannot evaluate is UNKNOWN, and unknown is unsafe.
//
// resource.name.startsWith(...) is the standard way to scope a grant to a
// prefix, and prefix-only isolation in a mixed bucket is what Req 54 declares
// an activation failure. Expanding a conditional binding as if it were
// unconditional would report a narrow grant as a whole-bucket one; dropping it
// would report a real grant as absent. Both answers are wrong; "unexpandable"
// is the one that fails closed.
func TestAConditionalBindingIsUnexpandableRatherThanUnconditional(t *testing.T) {
	bindings := bindingsOf("gs://output", []*iampb.Binding{{
		Role:    "roles/storage.objectAdmin",
		Members: []string{"serviceAccount:" + publisherIdentity},
		Condition: &expr.Expr{
			Title:      "prefix-only",
			Expression: `resource.name.startsWith("projects/_/buckets/b/objects/p/")`,
		},
	}}, planeIdentities())

	if len(bindings.Permissions[output.PrincipalPublisher]) != 0 {
		t.Errorf("a CONDITIONAL binding was expanded into permissions %v as though it were "+
			"unconditional; this plane evaluates no CEL and cannot know what it grants",
			bindings.Permissions[output.PrincipalPublisher])
	}
	unexpanded := bindings.UnrecognisedRoles[output.PrincipalPublisher]
	if len(unexpanded) != 1 {
		t.Fatalf("a conditional binding produced %v unexpanded roles, expected one", unexpanded)
	}
	if !strings.Contains(unexpanded[0], "roles/storage.objectAdmin") ||
		!strings.Contains(unexpanded[0], "prefix-only") {
		t.Errorf("the unexpanded role %q names neither the role nor the condition an operator "+
			"has to go and look at", unexpanded[0])
	}
}

// A stranger behind a duplicate role name, holding an UNEXPANDABLE role.
func TestAStrangerHoldingACustomRoleIsReportedUnderItsOwnIdentity(t *testing.T) {
	bindings := bindingsOf("gs://output", []*iampb.Binding{
		{Role: "roles/storage.objectCreator", Members: []string{"serviceAccount:" + publisherIdentity}},
		{Role: "projects/p/roles/customWriter", Members: []string{"serviceAccount:" + strangerIdentity}},
	}, planeIdentities())

	roles := bindings.UnrecognisedRoles[output.PrincipalRole(strangerIdentity)]
	if len(roles) != 1 || roles[0] != "projects/p/roles/customWriter" {
		t.Errorf("a stranger's custom role came back as %v; an unexpandable role must reach the "+
			"leaf as unknown so it can be called a violation", roles)
	}
}

func TestTheEmptyPolicyIsEmptyAndNotAnError(t *testing.T) {
	bindings := bindingsOf("gs://output", nil, planeIdentities())

	if len(bindings.Permissions) != 0 || len(bindings.UnrecognisedRoles) != 0 {
		t.Errorf("an empty policy produced %v / %v", bindings.Permissions, bindings.UnrecognisedRoles)
	}
	if bindings.BucketFingerprint != "gs://output" {
		t.Errorf("the bindings name bucket %q", bindings.BucketFingerprint)
	}
}

// A deleted principal is not the live one, and the finding says so by name.
//
// GCP leaves `deleted:serviceAccount:a@b?uid=123` in a policy routinely. The
// old normalization turned it into `serviceAccount:a@b?uid=123`, which matches
// no configured identity, so it was reported as a STRANGER on the bucket: the
// right direction with a diagnosis an operator cannot act on.
func TestADeletedPrincipalIsNamedRatherThanReportedAsAStranger(t *testing.T) {
	bindings := bindingsOf("gs://output", []*iampb.Binding{{
		Role:    "roles/storage.objectCreator",
		Members: []string{"deleted:serviceAccount:" + publisherIdentity + "?uid=123456"},
	}}, planeIdentities())

	if len(bindings.Permissions[output.PrincipalPublisher]) != 0 {
		t.Error("a binding for a DELETED service account was expanded as a live grant")
	}
	roles := bindings.UnrecognisedRoles[output.PrincipalPublisher]
	if len(roles) != 1 || !strings.Contains(roles[0], "DELETED") {
		t.Errorf("a deleted principal's binding came back as %v, under the publisher's own "+
			"role rather than under a spelling no operator recognises", roles)
	}
}

func TestAMemberShapeThisPlaneCannotReadIsItsOwnFindingAndNotAnIdentity(t *testing.T) {
	for _, member := range []string{
		"principal://iam.googleapis.com/locations/global/workforcePools/p/subject/s",
		"allUsers",
	} {
		bindings := bindingsOf("gs://output", []*iampb.Binding{{
			Role: "roles/storage.objectCreator", Members: []string{member},
		}}, planeIdentities())

		if len(bindings.Permissions) != 0 {
			t.Errorf("%q was expanded into permissions %v as though it were an ordinary identity",
				member, bindings.Permissions)
		}
		var named bool
		for _, roles := range bindings.UnrecognisedRoles {
			for _, role := range roles {
				if strings.Contains(role, "cannot read as an identity") {
					named = true
				}
			}
		}
		if !named {
			t.Errorf("%q produced %v; an unreadable member shape is its own finding",
				member, bindings.UnrecognisedRoles)
		}
	}
}

func TestTheMemberKindOfEveryShapeIsPinned(t *testing.T) {
	for _, member := range []struct {
		raw  string
		want string
		kind memberKind
	}{
		{"serviceAccount:a@b", "a@b", memberIdentity},
		{"user:someone@example.com", "someone@example.com", memberIdentity},
		{"group:team@example.com", "team@example.com", memberIdentity},
		{"domain:example.com", "example.com", memberIdentity},
		{"a@b", "a@b", memberIdentity},
		{"deleted:serviceAccount:a@b?uid=123", "a@b", memberDeleted},
		{"principal://iam.googleapis.com/x", "principal://iam.googleapis.com/x", memberOpaque},
		{"allUsers", "allUsers", memberOpaque},
	} {
		got, kind := normalizeMember(member.raw)
		if got != member.want || kind != member.kind {
			t.Errorf("normalizeMember(%q) = (%q, %d), expected (%q, %d)",
				member.raw, got, kind, member.want, member.kind)
		}
	}
}

// Req 22. storage.objects.update is the GCS permission that rewrites an
// object's metadata, and the marker is metadata.
func TestTheRolesThatGrantObjectUpdateSaySo(t *testing.T) {
	for _, role := range []string{
		"roles/storage.objectUser", "roles/storage.objectAdmin",
		"roles/storage.legacyBucketWriter", "roles/storage.admin",
	} {
		permissions, recognised := permissionsOf(role)
		if !recognised {
			t.Fatalf("%s is not in the table", role)
		}
		sort.Strings(permissions)
		if sort.SearchStrings(permissions, "storage.objects.update") == len(permissions) {
			t.Errorf("%s expands to %v and does not name storage.objects.update.\n\nReq 22's "+
				"'the publisher cannot update the marker' is enforced by the matrix only if the "+
				"matrix can SEE the permission; a role expansion that never mentions it makes "+
				"even roles/storage.objectAdmin invisible on that axis.", role, permissions)
		}
	}
	for _, role := range []string{
		"roles/storage.objectCreator", "roles/storage.objectViewer",
		"roles/storage.legacyObjectReader",
	} {
		permissions, _ := permissionsOf(role)
		for _, permission := range permissions {
			if permission == "storage.objects.update" {
				t.Errorf("%s expands to storage.objects.update and GCS does not grant it", role)
			}
		}
	}
}
