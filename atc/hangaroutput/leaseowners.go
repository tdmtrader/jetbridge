package hangaroutput

import "github.com/concourse/concourse/hangar/output"

// Only operation kinds claimed by deployed controllers have liveness metrics.
// Transitions performed inline have no independent owner to monitor.

// OwnedOperationKinds are the kinds a production controller.Runner claims a
// lease for, and therefore the only kinds whose lease term means anything.
func OwnedOperationKinds() []output.OperationKind {
	return []output.OperationKind{
		output.OperationInventory,
		output.OperationReclaimAdmission,
		output.OperationReclaimDelete,
	}
}

// UnownedOperationKinds are the five with no wired owner, each with why.
//
// They are not dead: every one of them is a real transition the schema, the
// repository and the passes implement, and the work they name is performed --
// by the web node's capture recovery path, by the release that follows a
// no-capture intent, by adoption inside the inventory sweep, by finalization
// after an acknowledged delete, and by read-lease expiry on the database clock.
// What none of them has is a workload that takes a LEASE of that kind, so there
// is no owner to be alive or dead.
//
// Whether each should get one is R3-F4's open question and is recorded as such
// rather than answered here. What is settled is that until one does, the plane
// must not claim its lease is unheld: that is a statement about a worker that
// does not exist.
func UnownedOperationKinds() map[output.OperationKind]string {
	return map[output.OperationKind]string{
		output.OperationCaptureRecovery: "advanced by the web node's recovery pass, which needs " +
			"PostgreSQL, Kubernetes and the output daemon and holds no output-bucket role. It " +
			"is not a leased controller",
		output.OperationNoCaptureRelease: "settled on the same web-node pass. Nothing is at " +
			"stake in the bucket while it waits: the source hold is the thing held, and it is " +
			"held on one node",
		output.OperationAdoption: "performed inside the inventory sweep, under the inventory " +
			"lease. It is a separate KIND so that a sweep which could not classify an object is " +
			"still not prevented from adopting the ones it could -- not a separate worker",
		output.OperationReclaimFinalization: "follows an acknowledged delete, so the object is " +
			"already gone; the latency costs bookkeeping rather than an observable state, and " +
			"the backlog it shows up in is " +
			"concourse_hangar_output_plane_inventory{kind=\"unfinalized_reclaim_jobs\"}, which " +
			"has an alert of its own",
		output.OperationReadLeaseCleanup: "a lease becomes abandoned by EXPIRING, on the " +
			"database clock. Expiry is not a write and closing an expired lease is not work " +
			"somebody has to be holding a lease to do",
	}
}
