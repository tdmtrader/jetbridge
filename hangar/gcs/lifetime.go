package gcs

// The authoritative whole-bucket lifetime-policy and IAM read.
//
// Most of this file is a TRANSLATION: it turns what the provider's SDK returns
// into the neutral shapes hangar/output declares, and the judgements about a
// reading -- is this safe, is this binding excessive, is this a shared bucket --
// are made in hangar/output/policy against those shapes. The split is
// deliberate: a derivation that lived here could only be tested against a live
// API, and a translation that lived there would need the SDK in the leaf.
//
// permissionsOf is the exception, and pretending otherwise was a finding. It
// expands an IAM role name into permissions, which means it decides what a
// grant MEANS, and the decision it used to make about a role it did not
// recognise -- report the role name as if it were a permission -- was the one
// that let a publisher holding a custom delete role attest safe. It is
// therefore tested here, in this package, including the shape of that mistake.
//
// What it reads is the WHOLE policy. Req 51 wants a whole-bucket lifecycle read
// proving the snapshot, and a reader that fetched only the rules it expected
// could not prove the absence of the one it did not.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/storage"

	"github.com/concourse/concourse/hangar/output"
)

// BucketPolicySource reads one bucket's lifetime policy and IAM bindings.
//
// It holds a bucket handle and no object handle at all, which is the attestor's
// whole value: a workload that could also read, create or delete an object
// would be a workload whose compromise costs the data rather than the
// assessment.
type BucketPolicySource struct {
	bucket *storage.BucketHandle
	name   string
	now    func() time.Time
}

// NewBucketPolicySource narrows a storage client to one bucket's metadata.
func NewBucketPolicySource(client *storage.Client, bucket string, now func() time.Time) (*BucketPolicySource, error) {
	if client == nil {
		return nil, fmt.Errorf("%w: the policy source needs a storage client",
			output.ErrIncomplete)
	}
	if bucket == "" {
		return nil, fmt.Errorf("%w: the policy source names no bucket", output.ErrIncomplete)
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}

	return &BucketPolicySource{bucket: client.Bucket(bucket), name: bucket, now: now}, nil
}

// ReadLifetimePolicy fetches the bucket's attributes and reports every
// lifecycle rule it has.
//
// The rules come back as the provider's own action strings rather than parsed
// into a vocabulary of ours. A rule this code does not recognise is still
// visible and still counted, and the classification of "can this remove an
// object" is made by the leaf against the string -- over-reporting, on purpose.
func (source *BucketPolicySource) ReadLifetimePolicy(ctx context.Context) (output.BucketLifetimePolicy, error) {
	attrs, err := source.bucket.Attrs(ctx)
	if err != nil {
		return output.BucketLifetimePolicy{}, fmt.Errorf("%w: reading the lifetime policy of %s: "+
			"%v", output.ErrAtRisk, source.name, err)
	}

	policy := output.BucketLifetimePolicy{
		BucketFingerprint: fingerprintOf(source.name),
		Metageneration:    attrs.MetaGeneration,
		ObservedAt:        output.NewTimestamp(source.now().UTC()),
	}
	for _, rule := range attrs.Lifecycle.Rules {
		policy.Rules = append(policy.Rules, output.LifecycleRule{
			Action:    rule.Action.Type,
			Condition: describeCondition(rule.Condition),
		})
	}

	return policy, nil
}

// ReadPrincipalBindings fetches the bucket's IAM policy and reports the raw
// grant list per role.
//
// The mapping from a cloud identity to one of this plane's four roles is the
// DEPLOYMENT's, supplied by the caller, because a bucket's IAM policy names
// service accounts and knows nothing about publishers or reclaimers. An
// identity in the policy that the caller did not name comes back under its own
// name, which is how a shared bucket is detected rather than assumed away.
func (source *BucketPolicySource) ReadPrincipalBindings(ctx context.Context, identities map[output.PrincipalRole]string) (output.PrincipalBindings, error) {
	policy, err := source.bucket.IAM().Policy(ctx)
	if err != nil {
		return output.PrincipalBindings{}, fmt.Errorf("%w: reading the IAM policy of %s: %v",
			output.ErrAtRisk, source.name, err)
	}

	byIdentity := map[string]output.PrincipalRole{}
	for role, identity := range identities {
		byIdentity[normalizeMember(identity)] = role
	}

	bindings := output.PrincipalBindings{
		BucketFingerprint: fingerprintOf(source.name),
		Permissions:       map[output.PrincipalRole][]string{},
		UnrecognisedRoles: map[output.PrincipalRole][]string{},
	}
	for _, role := range policy.Roles() {
		for _, member := range policy.Members(role) {
			identity := normalizeMember(member)
			principal, known := byIdentity[identity]
			if !known {
				// Somebody else on this bucket. It is reported under its own
				// identity so the leaf can call it what it is; inventing a
				// role for it would hide exactly the case Req 54 refuses.
				principal = output.PrincipalRole(identity)
			}
			permissions, recognised := permissionsOf(string(role))
			if !recognised {
				// Reported as UNEXPANDED rather than folded into the
				// permission list. A role name in the permission list matches
				// nothing the matrix forbids, so folding it in is the same as
				// saying the role grants nothing -- and the one thing this
				// code knows about a custom role is that it does not know
				// what it grants.
				bindings.UnrecognisedRoles[principal] =
					append(bindings.UnrecognisedRoles[principal], string(role))

				continue
			}
			bindings.Permissions[principal] = append(bindings.Permissions[principal],
				permissions...)
		}
	}

	return bindings, nil
}

// permissionsOf expands an IAM role name into the object and bucket permissions
// this plane compares against, and says whether it recognised the role at all.
//
// This table is a SNAPSHOT OF SOMETHING GOOGLE OWNS. The membership of a
// predefined role is Google's to change, and this file cannot notice when it
// does; the table was read from Cloud Storage's predefined-role reference and
// last checked on 2026-09-10. That date is here rather than in a commit message
// because the next person to wonder whether it is still true needs to know when
// it was last true.
//
// The boolean is the fix for R1-F5 and it is the reason this function is not a
// translation. It makes a JUDGEMENT: an unrecognised role is not its own name
// and not an empty grant -- both of those are "harmless" in a matrix that tests
// held[permission] against a forbidden list, and "harmless" is precisely what a
// custom role carrying storage.objects.delete is not. What this plane knows
// about a custom role is that it does not know what it grants, so it says so
// and the leaf makes it a finding.
func permissionsOf(role string) ([]string, bool) {
	switch role {
	case "roles/storage.objectCreator":
		return []string{"storage.objects.create"}, true
	case "roles/storage.objectViewer":
		return []string{"storage.objects.get", "storage.objects.list"}, true
	case "roles/storage.objectUser", "roles/storage.objectAdmin":
		return []string{
			"storage.objects.create", "storage.objects.get",
			"storage.objects.list", "storage.objects.delete",
		}, true
	case "roles/storage.legacyBucketReader":
		// storage.objects.list as well as the bucket read, which is the half
		// this table had missing: a principal granted the legacy reader gains
		// bucket-wide object listing, and the matrix forbids exactly that to
		// the publisher and the reclaimer.
		return []string{"storage.buckets.get", "storage.objects.list"}, true
	case "roles/storage.bucketViewer":
		// Bucket metadata and bucket LISTING -- of buckets, not of objects.
		// It is a different role from the legacy reader above and was
		// previously folded in with it.
		return []string{"storage.buckets.get", "storage.buckets.list"}, true
	case "roles/storage.legacyBucketWriter":
		return []string{
			"storage.buckets.get", "storage.objects.create",
			"storage.objects.delete", "storage.objects.list",
		}, true
	case "roles/storage.legacyObjectReader":
		return []string{"storage.objects.get"}, true
	case "roles/storage.admin":
		return []string{
			"storage.objects.create", "storage.objects.get",
			"storage.objects.list", "storage.objects.delete",
			"storage.buckets.get", "storage.buckets.getIamPolicy",
			"storage.buckets.update", "storage.buckets.setIamPolicy",
		}, true
	}

	return nil, false
}

// normalizeMember strips the IAM member type prefix, so
// "serviceAccount:a@b" and "a@b" are one identity.
func normalizeMember(member string) string {
	if index := strings.Index(member, ":"); index >= 0 {
		return member[index+1:]
	}

	return member
}

// fingerprintOf is the bucket identity this plane records. It is the same
// spelling the namespace derivation uses, so an attestation and a namespace
// name the same bucket the same way.
func fingerprintOf(bucket string) string {
	if strings.HasPrefix(bucket, "gs://") {
		return bucket
	}

	return "gs://" + bucket
}

// describeCondition renders a lifecycle condition for an operator to read. It
// is diagnostic text and nothing decides anything from it, which is why a
// condition this code cannot render is still a rule that counts.
func describeCondition(condition storage.LifecycleCondition) string {
	var parts []string
	if condition.AgeInDays > 0 {
		parts = append(parts, fmt.Sprintf("age>%d", condition.AgeInDays))
	}
	if condition.NumNewerVersions > 0 {
		parts = append(parts, fmt.Sprintf("newerVersions>%d", condition.NumNewerVersions))
	}
	if !condition.CreatedBefore.IsZero() {
		parts = append(parts, "createdBefore="+condition.CreatedBefore.Format(time.RFC3339))
	}
	if condition.Liveness != storage.LiveAndArchived {
		parts = append(parts, fmt.Sprintf("liveness=%d", condition.Liveness))
	}
	if len(parts) == 0 {
		return "unconditional"
	}

	return strings.Join(parts, ",")
}
