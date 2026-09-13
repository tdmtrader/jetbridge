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

	iampb "cloud.google.com/go/iam/apiv1/iampb"
	"cloud.google.com/go/storage"

	"github.com/concourse/concourse/hangar/internal/gcsclient"
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

// NewBucketPolicySource opens a client for one bucket's metadata and keeps
// nothing but the bucket handle.
//
// The narrowing is now the toolchain's rather than a comment's. The attestor
// used to build a whole *storage.Client and hold it in a local, one line above
// a comment reading "A bucket handle, and no object handle anywhere below" --
// and an object handle was one method call away on that local, which is how the
// same finding reached its third round. The client this opens is reachable only
// through the returned closer.
func NewBucketPolicySource(ctx context.Context, endpoint, bucket string, now func() time.Time) (*BucketPolicySource, func() error, error) {
	if bucket == "" {
		return nil, nil, fmt.Errorf("%w: the policy source names no bucket", output.ErrIncomplete)
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	client, err := gcsclient.New(ctx, endpoint)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: opening the policy source's client: %v",
			output.ErrInfrastructure, err)
	}

	return &BucketPolicySource{bucket: client.Bucket(bucket), name: bucket, now: now},
		client.Close, nil
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
	// V3, and the version number is the finding rather than a detail.
	// IAM().Policy asks for requestedPolicyVersion 1, which is the version that
	// does not model conditions: a bucket carrying a condition-scoped binding
	// answers with the condition elided or not at all, and either way the
	// attestor reads a narrower grant than the bucket actually carries.
	// resource.name.startsWith("projects/_/buckets/b/objects/p/") is THE
	// standard way to scope a grant to a prefix, and prefix-only isolation in a
	// mixed bucket is exactly what Req 54 calls an activation failure -- so the
	// one configuration the spec most wants to refuse was the one this read
	// could not see.
	policy, err := source.bucket.IAM().V3().Policy(ctx)
	if err != nil {
		return output.PrincipalBindings{}, fmt.Errorf("%w: reading the IAM policy of %s: %v",
			output.ErrAtRisk, source.name, err)
	}

	return bindingsOf(fingerprintOf(source.name), policy.Bindings, identities), nil
}

// bindingsOf is the traversal, separated from the fetch so it has a substrate.
//
// It is a pure function over the policy's own binding list for one reason:
// fake-gcs-server implements no bucket IAM at all, so every test that reached
// ReadPrincipalBindings could only assert that the read failed closed. The
// conversion from a cloud answer into the matrix's input -- the seam every
// later judgement is downstream of -- had no coverage anywhere in the tree, and
// it was wrong.
//
// It iterates BINDINGS rather than roles. iam.Policy.Roles() returns one entry
// per binding, duplicates included, and iam.Policy.Members(role) answers from
// the FIRST binding with that role name -- so a policy with two bindings for
// one role was traversed as "the first binding's members, twice" and the second
// binding's members never. A stranger granted object-create behind a duplicate
// role name was invisible, which is the same failure as reporting an
// unexpandable role as harmless, one layer lower down.
func bindingsOf(fingerprint string, bound []*iampb.Binding, identities map[output.PrincipalRole]string) output.PrincipalBindings {
	byIdentity := map[string]output.PrincipalRole{}
	for role, identity := range identities {
		normalized, kind := normalizeMember(identity)
		if kind == memberIdentity {
			byIdentity[normalized] = role
		}
	}

	bindings := output.PrincipalBindings{
		BucketFingerprint: fingerprint,
		Permissions:       map[output.PrincipalRole][]string{},
		UnrecognisedRoles: map[output.PrincipalRole][]string{},
	}
	for _, binding := range bound {
		role := binding.GetRole()
		for _, member := range binding.GetMembers() {
			identity, kind := normalizeMember(member)
			principal, known := byIdentity[identity]
			if !known || kind == memberOpaque {
				// Somebody else on this bucket, or a member shape this code
				// cannot read as an identity. It is reported under its own
				// spelling so the leaf can call it what it is; inventing a
				// role for it would hide exactly the case Req 54 refuses.
				//
				// A DELETED binding for a configured identity is the one
				// exception, and it is the point of memberDeleted: it is
				// reported under the role it names so the finding says
				// "the publisher principal", which is a thing an operator can
				// go and fix. It still expands to no permission.
				principal = output.PrincipalRole(identity)
			}

			// A condition this code cannot evaluate. It gets the same treatment
			// an unexpandable ROLE gets, for the same reason: this plane
			// contains no CEL evaluator, "we do not know what it grants" is not
			// "it grants nothing", and a binding whose scope is unknown cannot
			// be compared against a matrix of permissions. Expanding it as if
			// it were unconditional would be worse still -- it would report a
			// prefix-scoped grant as a whole-bucket one, which is the wrong
			// answer in the direction that reads "safe".
			if condition := binding.GetCondition(); condition != nil {
				bindings.UnrecognisedRoles[principal] = append(bindings.UnrecognisedRoles[principal],
					fmt.Sprintf("%s under the IAM condition %s", role, describeIAMCondition(
						condition.GetTitle(), condition.GetExpression())))

				continue
			}
			if kind != memberIdentity {
				bindings.UnrecognisedRoles[principal] = append(bindings.UnrecognisedRoles[principal],
					fmt.Sprintf("%s, bound to %s", role, kind.describe(member)))

				continue
			}

			permissions, recognised := permissionsOf(role)
			if !recognised {
				// Reported as UNEXPANDED rather than folded into the
				// permission list. A role name in the permission list matches
				// nothing the matrix forbids, so folding it in is the same as
				// saying the role grants nothing -- and the one thing this
				// code knows about a custom role is that it does not know
				// what it grants.
				bindings.UnrecognisedRoles[principal] =
					append(bindings.UnrecognisedRoles[principal], role)

				continue
			}
			bindings.Permissions[principal] = append(bindings.Permissions[principal],
				permissions...)
		}
	}

	return bindings
}

// describeIAMCondition renders a condition for an operator, bounded.
//
// The text is diagnostic and nothing decides anything from it -- the decision
// was made above, and it is "unknown, therefore unsafe". It is here so the
// finding names the binding an operator has to go and look at.
func describeIAMCondition(title, expression string) string {
	described := strings.TrimSpace(title)
	if described == "" {
		described = "(untitled)"
	}
	if trimmed := strings.TrimSpace(expression); trimmed != "" {
		described += ": " + trimmed
	}
	if len(described) > MaxConditionTextBytes {
		described = described[:MaxConditionTextBytes] + "..."
	}

	return described
}

// MaxConditionTextBytes bounds the rendered condition, because the finding it
// lands in is bounded and a CEL expression is caller-supplied text.
const MaxConditionTextBytes = 256

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
		// storage.objects.update is the permission that REWRITES an object's
		// metadata, and its absence from this table was the whole of Req 22's
		// "the publisher cannot update the marker": even roles/storage.objectAdmin,
		// which really does grant it, was invisible to the matrix on that axis,
		// so the immutable-marker promise rested on a Go type having no method.
		return []string{
			"storage.objects.create", "storage.objects.get",
			"storage.objects.list", "storage.objects.delete",
			"storage.objects.update",
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
			"storage.objects.update",
		}, true
	case "roles/storage.legacyObjectReader":
		return []string{"storage.objects.get"}, true
	case "roles/storage.admin":
		return []string{
			"storage.objects.create", "storage.objects.get",
			"storage.objects.list", "storage.objects.delete",
			"storage.objects.update",
			"storage.buckets.get", "storage.buckets.getIamPolicy",
			"storage.buckets.update", "storage.buckets.setIamPolicy",
		}, true
	}

	return nil, false
}

// memberKind is what shape an IAM member string turned out to be.
//
// It exists because "strip everything before the first colon" answered the same
// way for three different things. `deleted:serviceAccount:a@b?uid=123` -- which
// GCP leaves in a policy routinely -- normalized to `serviceAccount:a@b?uid=123`
// and `principal://iam.googleapis.com/...` to `//iam.googleapis.com/...`;
// neither matches any configured identity, so both were reported as STRANGERS
// on the bucket, taking the whole plane at risk with a message naming a
// principal the operator does not recognise. The direction was safe and the
// diagnosis was wrong, which is its own cost: an operator who cannot find the
// principal in the finding cannot fix the binding.
type memberKind int

const (
	// memberIdentity is one of the live member types, stripped to the identity
	// itself. Only this kind may match a configured principal.
	memberIdentity memberKind = iota

	// memberDeleted is a binding for a principal that has been deleted. The
	// underlying identity is recovered so the finding names something the
	// operator recognises, but it is never matched as a live one: a deleted
	// service account cannot do the role's work.
	memberDeleted

	// memberOpaque is a member type this code does not model -- a workforce or
	// workload-identity principal, `allUsers`, or a shape added since.
	memberOpaque
)

func (kind memberKind) describe(raw string) string {
	switch kind {
	case memberDeleted:
		return "a DELETED principal (" + raw + ")"
	case memberOpaque:
		return "an IAM member shape this plane cannot read as an identity (" + raw + ")"
	}

	return raw
}

// liveMemberPrefixes are the member types that name an identity this plane's
// configuration can also name. The list is closed on purpose: a prefix this
// code does not know is memberOpaque, not an identity with a funny name.
var liveMemberPrefixes = []string{
	"serviceAccount:", "user:", "group:", "domain:",
}

// normalizeMember strips a KNOWN IAM member type prefix, so
// "serviceAccount:a@b" and "a@b" are one identity, and says what shape the
// member was.
func normalizeMember(member string) (string, memberKind) {
	trimmed := strings.TrimSpace(member)
	kind := memberIdentity
	if rest, deleted := strings.CutPrefix(trimmed, "deleted:"); deleted {
		trimmed = rest
		kind = memberDeleted
	}

	for _, prefix := range liveMemberPrefixes {
		if rest, ok := strings.CutPrefix(trimmed, prefix); ok {
			// A deleted binding carries `?uid=...`, which is part of the
			// spelling and not part of the identity.
			if index := strings.Index(rest, "?uid="); index >= 0 {
				rest = rest[:index]
			}

			return rest, kind
		}
	}
	if strings.Contains(trimmed, ":") || strings.HasPrefix(trimmed, "//") {
		return trimmed, memberOpaque
	}
	if trimmed == "allUsers" || trimmed == "allAuthenticatedUsers" {
		return trimmed, memberOpaque
	}

	return trimmed, kind
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
//
// Every field GCS models is rendered, and it used to be four of twelve: a rule
// scoped by prefix, suffix, storage class, custom time or noncurrent time all
// rendered as "unconditional", so two different Delete rules could be described
// identically in the recorded finding an operator is supposed to act on. The
// safe/at_risk decision is made from the ACTION alone and was never affected;
// what was affected is whether the text says which rule.
func describeCondition(condition storage.LifecycleCondition) string {
	var parts []string
	if condition.AllObjects {
		parts = append(parts, "allObjects")
	}
	if condition.AgeInDays > 0 {
		parts = append(parts, fmt.Sprintf("age>%d", condition.AgeInDays))
	}
	if condition.NumNewerVersions > 0 {
		parts = append(parts, fmt.Sprintf("newerVersions>%d", condition.NumNewerVersions))
	}
	if !condition.CreatedBefore.IsZero() {
		parts = append(parts, "createdBefore="+condition.CreatedBefore.Format(time.RFC3339))
	}
	if !condition.CustomTimeBefore.IsZero() {
		parts = append(parts, "customTimeBefore="+condition.CustomTimeBefore.Format(time.RFC3339))
	}
	if !condition.NoncurrentTimeBefore.IsZero() {
		parts = append(parts,
			"noncurrentTimeBefore="+condition.NoncurrentTimeBefore.Format(time.RFC3339))
	}
	if condition.DaysSinceCustomTime > 0 {
		parts = append(parts, fmt.Sprintf("daysSinceCustomTime>%d", condition.DaysSinceCustomTime))
	}
	if condition.DaysSinceNoncurrentTime > 0 {
		parts = append(parts,
			fmt.Sprintf("daysSinceNoncurrentTime>%d", condition.DaysSinceNoncurrentTime))
	}
	if len(condition.MatchesPrefix) > 0 {
		parts = append(parts, "matchesPrefix="+strings.Join(condition.MatchesPrefix, "|"))
	}
	if len(condition.MatchesSuffix) > 0 {
		parts = append(parts, "matchesSuffix="+strings.Join(condition.MatchesSuffix, "|"))
	}
	if len(condition.MatchesStorageClasses) > 0 {
		parts = append(parts,
			"matchesStorageClasses="+strings.Join(condition.MatchesStorageClasses, "|"))
	}
	if condition.Liveness != storage.LiveAndArchived {
		parts = append(parts, fmt.Sprintf("liveness=%d", condition.Liveness))
	}
	if len(parts) == 0 {
		return "unconditional"
	}

	return strings.Join(parts, ",")
}
