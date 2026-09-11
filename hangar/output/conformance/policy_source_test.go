package conformance

// The policy attestor's own seam, against a real API server -- and the exact
// line past which no fake can go.
//
// `hangar/gcs.BucketPolicySource` is the production reader behind Reqs 51-54.
// Before Phase 9 it had no substrate-backed test at all: `hangar/gcs/
// lifetime_test.go` covers role expansion, member-prefix stripping and bucket
// fingerprinting as pure functions, and `hangar/output/policy` is a table over
// snapshots somebody else produced. Nothing had ever driven `bucket.Attrs` or
// `bucket.IAM().Policy` against an API server, so a break in that wiring would
// have surfaced as an at-risk plane in production and nowhere earlier.
//
// WHAT THIS FILE CANNOT SAY, stated here rather than discovered later.
// fsouza/fake-gcs-server v1.52.3 implements neither bucket lifecycle rules nor
// bucket IAM -- grepped at the pinned version, zero hits for either in
// `fakestorage/`. So:
//
//   - AC 17's "change a lifecycle rule just after a successful attestation and
//     prove the documented detection window" CANNOT be performed on this
//     substrate, or on tier 1, or through the brine fixture, which is the same
//     emulator reached another way. It is real-GCS evidence, and Phase 9's
//     real-GCS box records that it could not be performed for want of a
//     project.
//   - AC 16's IAM assertions likewise. `gcstest.Recorder` records OPERATIONS,
//     never credentials; no assertion in this repository can distinguish "the
//     code issued only its role's RPCs" from "the principal could only have
//     issued them", and the suite says so where it makes the weaker claim.
//
// What IS provable here, and is worth proving, is the fail-closed half: a
// policy source that cannot read is at risk, and it is at risk by TYPE. That is
// the behaviour the whole trust argument rests on -- Req 52 says an unreadable
// policy check is a durable at-risk state and not a shrug -- and an emulator
// that answers "no such API" is a perfectly honest way to reach it.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar/gcs"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/policy"
)

// eachTier2 runs a case against the tier-2 substrate only. Tier 1 is an
// in-process adapter fake with no HTTP surface, and the seam under test here is
// a bucket handle rather than an objectstore.Client.
func eachTier2(t *testing.T, run func(*testing.T, substrate)) {
	t.Helper()

	tier := tier2(t)
	if tier.endpoint == "" {
		t.Fatalf("%s reported no endpoint; this case opens a second production seam against "+
			"the same store and needs the address", tier.name)
	}
	t.Run(tier.name, func(t *testing.T) { run(t, tier) })
}

// The control, asserted before the refusals: the reader really reads. A bucket
// with no lifecycle rules comes back as a snapshot naming that bucket, at the
// metageneration the server reports, with no delete rules -- which is the state
// Req 51 requires the dedicated output bucket to be in.
func TestTheLifetimePolicyReaderReadsARealBucketsAttributes(t *testing.T) {
	eachTier2(t, func(t *testing.T, tier substrate) {
		ctx := context.Background()
		observed := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)

		source, closer, err := gcs.NewBucketPolicySource(ctx, tier.endpoint, tier.bucket,
			func() time.Time { return observed })
		if err != nil {
			t.Fatalf("opening the policy source: %v", err)
		}
		defer func() { _ = closer() }()

		read, err := source.ReadLifetimePolicy(ctx)
		if err != nil {
			t.Fatalf("reading the lifetime policy of a bucket that exists: %v", err)
		}

		if read.BucketFingerprint == "" {
			t.Error("the policy names no bucket; an attestation that cannot say WHICH bucket " +
				"it observed is not an attestation")
		}
		if read.Metageneration <= 0 {
			t.Errorf("the policy reports metageneration %d; the attestation records it so a "+
				"later read can tell an unchanged policy from an unread one",
				read.Metageneration)
		}
		if !read.ObservedAt.Time.Equal(observed) {
			t.Errorf("the observation time is %v, not the clock the caller supplied (%v)",
				read.ObservedAt.Time, observed)
		}
		if len(read.Rules) != 0 {
			t.Errorf("a freshly created bucket reports %d lifecycle rule(s): %v",
				len(read.Rules), read.Rules)
		}

		// And the leaf's own assessment over it: a bucket with no delete rule,
		// observed now, at the expected metageneration, is SAFE.
		snapshot, findings, err := policy.DeriveSnapshot(policy.Expectation{
			ActivationEpoch:   testEpoch,
			BucketFingerprint: read.BucketFingerprint,
			Principals: map[output.PrincipalRole]string{
				output.PrincipalPublisher:      "publisher@p.iam.gserviceaccount.com",
				output.PrincipalInventory:      "inventory@p.iam.gserviceaccount.com",
				output.PrincipalReclaimer:      "reclaimer@p.iam.gserviceaccount.com",
				output.PrincipalPolicyAttestor: "attestor@p.iam.gserviceaccount.com",
			},
		}, read)
		if err != nil {
			t.Fatalf("deriving a snapshot from a real read: %v", err)
		}
		if snapshot.State != output.PolicySafe {
			t.Errorf("a bucket with no lifecycle delete rule derived state %q, not %q: %v",
				snapshot.State, output.PolicySafe, findings)
		}
		if policy.AtRisk(findings) {
			t.Errorf("a clean read is at risk: %v", findings)
		}
	})
}

// Req 52's fail-closed clause, and the one thing an emulator without IAM is
// perfectly placed to prove: "we could not check" is at risk, by type, and is
// never quietly the same as "we checked and it was fine".
//
// fake-gcs-server v1.52.3 implements no bucket IAM, so this read fails the way
// a real read fails when the attestor's principal has lost
// `storage.buckets.getIamPolicy` -- which is a deployment fault an operator
// must see, and the most dangerous thing this code could do with it is shrug.
func TestAnUnreadablePrincipalPolicyIsAtRiskAndNotAnAllClear(t *testing.T) {
	eachTier2(t, func(t *testing.T, tier substrate) {
		ctx := context.Background()

		source, closer, err := gcs.NewBucketPolicySource(ctx, tier.endpoint, tier.bucket, nil)
		if err != nil {
			t.Fatalf("opening the policy source: %v", err)
		}
		defer func() { _ = closer() }()

		bindings, err := source.ReadPrincipalBindings(ctx, map[output.PrincipalRole]string{
			output.PrincipalPublisher: "publisher@p.iam.gserviceaccount.com",
			output.PrincipalReclaimer: "reclaimer@p.iam.gserviceaccount.com",
		})
		if err == nil {
			// If a future emulator grows IAM this becomes a real conformance
			// row rather than a skip, so it says what it would then require.
			if len(bindings.Permissions) == 0 && len(bindings.UnrecognisedRoles) == 0 {
				t.Error("the substrate answered an IAM read with an EMPTY policy and no error. " +
					"An empty policy on the output bucket means no principal may touch it, " +
					"which is not a state this plane may report as healthy.")
			}

			return
		}

		if !errors.Is(err, output.ErrAtRisk) {
			t.Errorf("an unreadable IAM policy is %v; Req 52 makes a failed, unreadable or "+
				"unsafe policy check a durable at-risk state, and any other typing lets it "+
				"be retried as a blip forever while the plane keeps publishing", err)
		}
		if len(bindings.Permissions) != 0 {
			t.Errorf("a failed IAM read returned %d permission set(s); a partial answer from a "+
				"read that failed is worse than none", len(bindings.Permissions))
		}
	})
}

// A policy source pointed at a bucket that is not there is at risk too, and for
// the same reason: Req 52 lists "unexpected exact absence" beside an unreadable
// check, and a bucket the attestor cannot see is the strongest form of it.
func TestAPolicySourceOnAMissingBucketIsAtRisk(t *testing.T) {
	eachTier2(t, func(t *testing.T, tier substrate) {
		ctx := context.Background()

		source, closer, err := gcs.NewBucketPolicySource(ctx, tier.endpoint,
			uniqueBucket("never-provisioned"), nil)
		if err != nil {
			t.Fatalf("opening the policy source: %v", err)
		}
		defer func() { _ = closer() }()

		if _, err := source.ReadLifetimePolicy(ctx); !errors.Is(err, output.ErrAtRisk) {
			t.Errorf("reading the lifetime policy of a bucket that does not exist returned %v; "+
				"an operator who pointed the plane at the wrong bucket learns it here or not "+
				"at all", err)
		}
	})
}
