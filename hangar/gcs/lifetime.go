package gcs

// The authoritative whole-bucket lifetime-policy and IAM read.
//
// This file is a TRANSLATION and nothing else: it turns what the provider's SDK
// returns into the neutral shapes hangar/output declares, and every judgement --
// is this safe, is this binding excessive, is this a shared bucket -- is made in
// hangar/output/policy against those shapes. The split is deliberate. A
// derivation that lived here could only be tested against a live API, and a
// translation that lived there would need the SDK in the leaf.
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
			bindings.Permissions[principal] = append(bindings.Permissions[principal],
				permissionsOf(string(role))...)
		}
	}

	return bindings, nil
}

// permissionsOf expands an IAM role name into the object and bucket permissions
// this plane compares against.
//
// It covers the predefined roles a deployment would plausibly use plus the
// custom-role case, which is passed through under its own name. A role this
// function does not recognise is NOT silently treated as harmless: it comes
// back as itself, so the matrix sees a permission it did not expect rather than
// an empty grant it would call insufficient.
func permissionsOf(role string) []string {
	switch role {
	case "roles/storage.objectCreator":
		return []string{"storage.objects.create"}
	case "roles/storage.objectViewer":
		return []string{"storage.objects.get", "storage.objects.list"}
	case "roles/storage.objectUser", "roles/storage.objectAdmin":
		return []string{
			"storage.objects.create", "storage.objects.get",
			"storage.objects.list", "storage.objects.delete",
		}
	case "roles/storage.legacyBucketReader", "roles/storage.bucketViewer":
		return []string{"storage.buckets.get"}
	case "roles/storage.admin":
		return []string{
			"storage.objects.create", "storage.objects.get",
			"storage.objects.list", "storage.objects.delete",
			"storage.buckets.get", "storage.buckets.getIamPolicy",
			"storage.buckets.update", "storage.buckets.setIamPolicy",
		}
	}

	return []string{role}
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
