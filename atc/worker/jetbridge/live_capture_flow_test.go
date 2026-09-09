// hangar_live only, never live: like the generated-Pod contract beside it, this
// one needs a disposable cluster. It labels a node into the output-plane ready
// cohort (concourse.dev/hangar-execution-control-v1, concourse.dev/hangar-output-v1),
// runs a node-local output daemon, and pins a Pod to that node -- all
// cluster-scoped work a namespaced live-tier account cannot do and must not do
// against the deployed cluster. Its CI home is a job in
// deploy/k8s-e2e-pipeline.yml beside hangar-generated-pod-contract;
// build-and-vet only compiles it.
//go:build hangar_live

package jetbridge

// The one thing no unit tier can say: a real kubelet ran the Pod this runtime
// generated, and the hold the capture control init took inside it is the hold
// the producer's own start revalidated.
//
// PENDING. This file is a declared contract with no body yet, and it is added
// now rather than in Phase 9 because the Phase 4 round-1 review is the reason it
// exists: F1 and F2 were both defects that only a real node reveals, and both
// were invisible to a phase whose only coverage of the generated script was
// `sh -n`. Phase 4 closed them with a Go test that EXECUTES the script against a
// real daemon -- which is as far as a unit tier can go. What that test still
// cannot say is that a kubelet ordered the init container before the writers,
// that the Downward API really supplied metadata.uid to it, that the hostPath
// the ATC mounted is the directory the daemon reserved on that node's disk, and
// that the node affinity put the Pod on the node holding the reservation.
//
// Reddened by (to be proved when the body lands, in the Phase 9 K3s tier):
//
//  1. Emitting the capture control init anywhere but first among the init
//     containers -- `cleanup-stale` removes the tree the hold protects and
//     `artifact-fetch` stages inputs into it, so a kubelet running either
//     before the hold writes into an unheld directory. The Go and brine tiers
//     assert the ORDER in the Pod spec; only a kubelet asserts the effect.
//  2. Dropping the required node affinity on the reserving node, on a cluster
//     with two ready nodes: the Pod schedules onto the node that reserved
//     nothing, DirectoryOrCreate makes an empty unheld directory, and the hold
//     is refused. This is the outage half of F3 and it has no unit vector at
//     all -- one node cannot be scheduled away from.
//  3. Binding the hold to anything but the init container's own Downward API
//     metadata.uid -- the F3 defect -- which a real API server assigns and no
//     fixture can honestly invent.
//
// The scenario, when it lands: a capture-selected producer on a real node
// holds, writes and is sealed.
//
//	Given a K3s node labelled into the output-plane ready cohort
//	  and an output daemon running on it with a reservation for one execution
//	When the ATC builds and runs the capture-selected Pod for that execution
//	Then the capture control init acknowledged the hold before any writer
//	  started, bound to the Pod UID the API server assigned
//	  and the producer wrote into the reserved incarnation on that node's disk
//	  and the ATC's own start revalidated that hold against that Pod
//	  and the seal confirmed over the bytes the producer wrote

import "testing"

func TestLiveCaptureSelectedProducerHoldsWritesAndIsSealed(t *testing.T) {
	t.Skip("pending: Phase 9 K3s tier — see the contract above for what it must redden on")
}
