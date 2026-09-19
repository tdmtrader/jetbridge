// Package output_testsupport holds the fixtures the output plane's tests share:
// a derived namespace, a resolved reservation, a cursor, a lease, and a tier-1
// store behind a recorder.
//
// It exists because four test packages -- the three object roles' own tests and
// the conformance suite -- each declared the same helpers, and one of them had
// already drifted: a digest builder that panicked on a three-character fill.
// Separate external test packages cannot share unexported helpers, so the
// shared shape lives here, once.
//
// Nothing production links it, and the root reachability rule skips it for the
// same reason it skips the conformance suite. It constructs no role: a helper
// that built a publisher would link the role into every test that imported
// it, and each role package builds its own.
package output_testsupport

import (
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/gcstest"
	"github.com/concourse/concourse/hangar/output"
)

// FixedInstant is the one clock reading every fixture is stamped with.
var FixedInstant = time.Date(2026, 3, 4, 5, 6, 7, 890123456, time.UTC)

// Namespace derives an output namespace for one bucket, tenant and epoch, under
// the deployment prefix every fixture shares.
func Namespace(t *testing.T, bucket, tenant string, epoch executioncontrol.ActivationEpoch) output.OutputNamespace {
	t.Helper()

	namespace, err := output.DeriveNamespace(output.NamespaceConfig{
		Store:            output.StoreGCS,
		Bucket:           bucket,
		DeploymentPrefix: "deployments/blue",
		TenantID:         tenant,
		ActivationEpoch:  epoch,
	})
	if err != nil {
		t.Fatalf("deriving the namespace: %v", err)
	}

	return namespace
}

// Digest is a syntactically valid sha256 digest built from a repeating fill, so
// two fills give two distinct trees and the same fill gives the same one.
func Digest(fill string) hangar.Digest {
	return hangar.Digest("sha256:" + strings.Repeat(fill, 64)[:64])
}

// ExecutionIdentity is the one exact execution every fixture reservation names.
func ExecutionIdentity() executioncontrol.Identity {
	return executioncontrol.Identity{
		ExecutionID: "33333333-3333-4333-8333-333333333333",
		Fence:       1,
	}
}

// Reservation resolves a reservation into namespace, with the marker the
// namespace derives for it, and fails the test if the result does not validate.
func Reservation(t *testing.T, namespace output.OutputNamespace, id output.ReservationID, digest hangar.Digest) output.ResolvedReservation {
	t.Helper()

	reservation := output.ResolvedReservation{
		ReservationID:   id,
		Execution:       ExecutionIdentity(),
		ActivationEpoch: namespace.ActivationEpoch(),
		HandoffID:       "11111111-1111-4111-8111-111111111111",
		CaptureFence:    1,
		Scope:           namespace.Scope(),
		Digest:          digest,
		Marker:          namespace.MarkerFor(id, digest, output.NewTimestamp(FixedInstant)),
	}
	if err := reservation.Validate(); err != nil {
		t.Fatalf("the fixture reservation does not validate: %v", err)
	}

	return reservation
}

// Cursor is a valid cursor at the start of a cycle under one epoch.
func Cursor(epoch executioncontrol.ActivationEpoch) output.InventoryCursor {
	return output.InventoryCursor{
		ProtocolVersion: output.ProtocolVersion,
		ActivationEpoch: epoch,
		CursorFence:     1,
		UpdatedAt:       output.NewTimestamp(FixedInstant),
	}
}

// Lease is a valid, unexpired read lease over one exact ref.
func Lease(t *testing.T, ref hangar.TreeRef, epoch executioncontrol.ActivationEpoch) output.ReadLease {
	t.Helper()

	lease := output.ReadLease{
		ProtocolVersion: output.ProtocolVersion,
		ReadLeaseID:     "22222222-2222-4222-8222-222222222222",
		ClaimID:         "66666666-6666-4666-8666-666666666666",
		Ref:             ref,
		ActivationEpoch: epoch,
		LeaseFence:      1,
		GrantedAt:       output.NewTimestamp(FixedInstant),
		ExpiresAt:       output.NewTimestamp(FixedInstant.Add(20 * time.Minute)),
	}
	if err := lease.Validate(); err != nil {
		t.Fatalf("the fixture lease does not validate: %v", err)
	}

	return lease
}

// RecordedMemory is the tier-1 store with its bucket created, behind a
// recorder, so a test can say which RPCs a role issued.
func RecordedMemory(bucket string) (*gcstest.Memory, *gcstest.Recorder) {
	memory := gcstest.NewMemory()
	memory.CreateBucket(bucket)

	return memory, gcstest.Record(memory)
}

// ExpectNoRPC fails the test if the recorder saw any call. A refusal decided
// from the arguments must be decided before any RPC, or the refused caller
// learns something about the bucket.
func ExpectNoRPC(t *testing.T, recorder *gcstest.Recorder) {
	t.Helper()

	if calls := recorder.Calls(); len(calls) != 0 {
		t.Errorf("the store was reached: %v. A refusal decided from the arguments must be "+
			"decided before any RPC, or the refused caller learns something about the bucket", calls)
	}
}
