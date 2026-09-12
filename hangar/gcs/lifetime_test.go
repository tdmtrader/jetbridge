package gcs

// The IAM role expansion, which is the one thing in lifetime.go that makes a
// judgement rather than a translation.
//
// It had no test of any kind, and the judgement it made about a role it did not
// recognise -- report the role name as if it were a permission -- was what let
// a publisher holding a custom role with storage.objects.delete attest safe:
// the matrix tests held[permission] against a forbidden list, and a
// pseudo-permission matches nothing. So the table is pinned member for member,
// and so is the unknown-role answer.
//
// The table is a snapshot of something Google owns. These assertions cannot
// notice Google changing a predefined role; what they can do is make a change
// HERE deliberate, and record what was believed on 2026-09-10, with
// storage.objects.update added on 2026-09-12 from the same reference.

import (
	"sort"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/storage"
)

func TestThePredefinedRoleExpansionIsPinnedMemberForMember(t *testing.T) {
	for _, expansion := range []struct {
		role string
		want []string
	}{
		{"roles/storage.objectCreator", []string{"storage.objects.create"}},
		{"roles/storage.objectViewer", []string{"storage.objects.get", "storage.objects.list"}},
		// storage.objects.update is the one that was missing, and its absence
		// was the whole of Req 22's "the publisher cannot update the marker":
		// the marker IS object metadata, and a matrix that never names the
		// permission that rewrites metadata cannot forbid it to anyone.
		{"roles/storage.objectUser", []string{
			"storage.objects.create", "storage.objects.delete",
			"storage.objects.get", "storage.objects.list",
			"storage.objects.update",
		}},
		{"roles/storage.objectAdmin", []string{
			"storage.objects.create", "storage.objects.delete",
			"storage.objects.get", "storage.objects.list",
			"storage.objects.update",
		}},
		// The one that was wrong. GCS's own definition carries object LIST as
		// well as the bucket read, and the matrix forbids bucket-wide list to
		// the publisher and the reclaimer -- so a deployment that granted the
		// legacy reader to either of them gained exactly the permission the
		// matrix exists to refuse, and the matrix said safe.
		{"roles/storage.legacyBucketReader", []string{
			"storage.buckets.get", "storage.objects.list",
		}},
		{"roles/storage.bucketViewer", []string{
			"storage.buckets.get", "storage.buckets.list",
		}},
		{"roles/storage.legacyBucketWriter", []string{
			"storage.buckets.get", "storage.objects.create",
			"storage.objects.delete", "storage.objects.list",
			"storage.objects.update",
		}},
		{"roles/storage.legacyObjectReader", []string{"storage.objects.get"}},
		{"roles/storage.admin", []string{
			"storage.buckets.get", "storage.buckets.getIamPolicy",
			"storage.buckets.setIamPolicy", "storage.buckets.update",
			"storage.objects.create", "storage.objects.delete",
			"storage.objects.get", "storage.objects.list",
			"storage.objects.update",
		}},
	} {
		got, recognised := permissionsOf(expansion.role)
		if !recognised {
			t.Errorf("%s came back unrecognised; it is in the table", expansion.role)

			continue
		}
		sort.Strings(got)
		if len(got) != len(expansion.want) {
			t.Errorf("%s expands to %v, expected %v", expansion.role, got, expansion.want)

			continue
		}
		for i := range got {
			if got[i] != expansion.want[i] {
				t.Errorf("%s expands to %v, expected %v", expansion.role, got, expansion.want)

				break
			}
		}
	}
}

func TestARoleThisPlaneCannotExpandIsUnknownAndNotHarmless(t *testing.T) {
	for _, role := range []string{
		"projects/example/roles/customOutputWriter",
		"roles/storage.somethingAddedAfterThisTableWasWritten",
		"organizations/1/roles/blanket",
		"",
	} {
		permissions, recognised := permissionsOf(role)
		if recognised {
			t.Errorf("%q was recognised; the table does not contain it", role)
		}
		if len(permissions) != 0 {
			t.Errorf("%q expanded to %v.\n\nThis is the shape of the defect: a role name "+
				"returned as if it were a permission matches nothing in the forbidden list, so "+
				"a publisher bound roles/storage.objectCreator PLUS a custom role containing "+
				"storage.objects.delete satisfies its required set, trips no excess finding, "+
				"and attests safe. An unexpandable role must come back as unknown so the leaf "+
				"can call it a violation.", role, permissions)
		}
	}
}

func TestTheMemberPrefixIsStrippedSoOneIdentityIsOneIdentity(t *testing.T) {
	for _, member := range []struct{ raw, want string }{
		{"serviceAccount:publisher@project.iam.gserviceaccount.com",
			"publisher@project.iam.gserviceaccount.com"},
		{"user:someone@example.com", "someone@example.com"},
		{"publisher@project.iam.gserviceaccount.com",
			"publisher@project.iam.gserviceaccount.com"},
	} {
		if got, _ := normalizeMember(member.raw); got != member.want {
			t.Errorf("normalizeMember(%q) = %q, expected %q.\n\nThe configured identity and the "+
				"one the IAM policy names are the same service account written two ways; a "+
				"mismatch here reports every principal as a stranger on its own bucket.",
				member.raw, got, member.want)
		}
	}
}

func TestTheBucketFingerprintIsTheSameSpellingTheNamespaceUses(t *testing.T) {
	for _, bucket := range []struct{ raw, want string }{
		{"output-bucket", "gs://output-bucket"},
		{"gs://output-bucket", "gs://output-bucket"},
	} {
		if got := fingerprintOf(bucket.raw); got != bucket.want {
			t.Errorf("fingerprintOf(%q) = %q, expected %q; an attestation and a namespace that "+
				"named the same bucket differently would make every reading a wrong-bucket "+
				"finding", bucket.raw, got, bucket.want)
		}
	}
}

// Two different Delete rules must not be described identically.
//
// The renderer read four of the twelve condition fields GCS models, so a rule
// scoped by prefix, suffix, storage class, custom time or noncurrent time came
// out as "unconditional". The safe/at_risk decision is made from the ACTION
// alone and was never affected; the recorded finding an operator has to act on
// was.
func TestEveryLifecycleConditionFieldReachesTheRenderedText(t *testing.T) {
	for _, row := range []struct {
		name      string
		condition storage.LifecycleCondition
		want      string
	}{
		{"prefix", storage.LifecycleCondition{MatchesPrefix: []string{"deployments/blue/"}},
			"matchesPrefix=deployments/blue/"},
		{"suffix", storage.LifecycleCondition{MatchesSuffix: []string{".tar.zst"}},
			"matchesSuffix=.tar.zst"},
		{"storage class", storage.LifecycleCondition{MatchesStorageClasses: []string{"NEARLINE"}},
			"matchesStorageClasses=NEARLINE"},
		{"days since custom time", storage.LifecycleCondition{DaysSinceCustomTime: 3},
			"daysSinceCustomTime>3"},
		{"days since noncurrent", storage.LifecycleCondition{DaysSinceNoncurrentTime: 5},
			"daysSinceNoncurrentTime>5"},
		{"noncurrent time before",
			storage.LifecycleCondition{NoncurrentTimeBefore: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
			"noncurrentTimeBefore=2026-01-02"},
		{"custom time before",
			storage.LifecycleCondition{CustomTimeBefore: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)},
			"customTimeBefore=2026-01-03"},
		{"all objects", storage.LifecycleCondition{AllObjects: true}, "allObjects"},
		{"age", storage.LifecycleCondition{AgeInDays: 30}, "age>30"},
	} {
		rendered := describeCondition(row.condition)
		if !strings.Contains(rendered, row.want) {
			t.Errorf("a rule conditioned on %s rendered as %q, which does not contain %q.\n\n"+
				"Two different Delete rules that render identically are two findings an "+
				"operator cannot tell apart.", row.name, rendered, row.want)
		}
		if rendered == "unconditional" {
			t.Errorf("a rule conditioned on %s rendered as unconditional", row.name)
		}
	}
	if describeCondition(storage.LifecycleCondition{}) != "unconditional" {
		t.Error("a genuinely unconditional rule no longer renders as unconditional")
	}
}
