package steps

// What a capture-selected task's Pod SAYS.
//
// Every sentence here is readable off the Pod the cluster hands back, which is
// the half of this track brine is the right runner for. What a real kubelet
// DOES with that Pod is a K3s flow under a build tag, and stays in Go.
//
// The bodies are stubs until Phase 4 Green teaches Container.buildPod about
// DurableOutputCapture. They fail with a typed error naming that phase; none of
// them passes.

import (
	"github.com/brine-dev/brine-go/pkg/brine"
)

const capturePodPhase = "Phase 4 Green"

// HangarCapturePodDefinitions is the capture-pod family.
func HangarCapturePodDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// THE ONLY WAY INTO CaptureDraft, and it is a REFINEMENT over a draft
		// that has not run. There is deliberately no sentence that selects
		// capture after the container runs, so "a late request captures an
		// already-running task" is a state no scenario can build (Req 1).
		stubMap[ContainerDraft, CaptureDraft](
			"its output {string} is captured when the step succeeds",
			capturePodPhase,
			"the DurableOutputCapture extension on the container spec and the "+
				"admission that predeclares its handoff and source-lease identities"),

		// The second way in, for chains that need the daemon as well as the pod.
		// It is a different sentence rather than a second definition of the one
		// above, because brine's registry is keyed on the sentence alone: one
		// pattern has exactly one input type, and a duplicate pattern is caught
		// by TestNoStepLineMatchesTwoDefinitions.
		stubMap[HangarDaemon, CaptureDraft](
			"a capture-selected task {string} built from image {string} declares the output {string}",
			capturePodPhase,
			"the capture-selected execution path through Container.buildPod"),

		stubMap[CaptureDraft, CaptureDraft](
			"a second output {string} is also selected for capture",
			capturePodPhase,
			"the admission-time refusal of more than one declared capture output"),

		stubMap[CaptureDraft, CaptureDraft](
			"the worker's cohort is ready for {string}",
			"Phase 8 Green",
			"the per-facet readiness labels and the authenticated extension handshake"),

		stubMap[CaptureDraft, CaptureDraft](
			"the daemon cohort has not handshaked",
			"Phase 8 Green",
			"the extension handshake a ready label alone is never a substitute for"),

		stubMap[CaptureDraft, CaptureDraft](
			"its pause pod reaches a terminal state",
			capturePodPhase,
			"the ledger classifier recreatePausePodIfTerminal must consult"),

		stubMap[CaptureDraft, CapturePodCreated](
			"the capture pod is built",
			capturePodPhase,
			"Container.buildPod's capture-selected branch"),

		// Checks, terminal over the Pod that came back.

		stubCheck[CapturePodCreated](
			"the capture control init runs before every writer",
			capturePodPhase,
			"the capture control init container at index 0 of the init slice"),

		stubCheck[CapturePodCreated](
			"the capture control init is the only container carrying the source-control grant",
			capturePodPhase,
			"the one-shot source-control capability, mounted into exactly one container"),

		// The absence half. Its positive control is the line above it, in the
		// same scenario: "no credential" passes on a worker that ignores its
		// configuration entirely.
		stubCheck[CapturePodCreated](
			"the task's containers carry no Hangar credential",
			capturePodPhase,
			"the credential placement buildPod must not copy into task or sidecar containers"),

		stubCheck[CapturePodCreated](
			"the capture pod carries the base control handshake and the Downward API pod and node fields",
			capturePodPhase,
			"the ExecutionControl envelope and the exact Downward API field refs"),

		// A COUNT, not membership. The migration's own GAP rows warn that
		// brine's mount steps are membership by default, and "every mount
		// resolves to a declared Volume" is only a real claim when the number
		// of mounts is pinned too.
		stubCheck[CapturePodCreated](
			"the capture pod declares {int} mounts, and every one resolves to a declared Volume",
			capturePodPhase,
			"the capture pod's volume and mount construction"),

		stubCheck[CapturePodCreated](
			"the capture pod carries {int} init containers",
			capturePodPhase,
			"the init-container list a capture-selected pod is built with"),

		stubCheck[CapturePodCreated](
			"the capture pod is admitted only by a node carrying {string}",
			"Phase 8 Green",
			"the affinity terms BuildAffinity emits for the output cohort"),

		stubCheck[CapturePodCreated](
			"the capture pod requires {int} ready labels",
			"Phase 8 Green",
			"both required ready labels, base control and output"),

		stubCheck[CapturePodCreated](
			"no capture pod is built",
			"Phase 8 Green",
			"the refusal a worker whose output facet is not enabled must return"),

		stubCheck[CapturePodCreated](
			"the pod build is refused saying {string}",
			capturePodPhase,
			"the typed refusals the capture-selected build path returns"),

		stubCheck[CapturePodCreated](
			"the pod's fetch init container reads from the bucket {string}",
			capturePodPhase,
			"the strict-input bucket the output plane must never redirect"),
	}
}
