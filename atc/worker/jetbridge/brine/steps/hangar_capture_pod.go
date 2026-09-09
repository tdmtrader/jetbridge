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
	"fmt"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"

	corev1 "k8s.io/api/core/v1"

	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar/executioncontrol"
	hangaroutput "github.com/concourse/concourse/hangar/output"
)

const capturePodPhase = "Phase 4 Green"

// scenarioReservingNode is the Kubernetes node whose daemon issued the
// scenario's reservation. A reservation is a directory on ONE node's disk, so
// the capture pod carries a required affinity on this node by name; the node
// UID beside it (hangarNodeUID) is what the daemon compares a hold against.
const scenarioReservingNode = "hangar-node-a"

// HangarCapturePodDefinitions is the capture-pod family.
func HangarCapturePodDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// THE ONLY WAY INTO CaptureDraft, and it is a REFINEMENT over a draft
		// that has not run. There is deliberately no sentence that selects
		// capture after the container runs, so "a late request captures an
		// already-running task" is a state no scenario can build (Req 1).
		brine.DefineMap[ContainerDraft, CaptureDraft](
			"its output {string} is captured when the step succeeds",
			func(in ContainerDraft, p brine.Params, _ *brine.Recorder) (CaptureDraft, error) {
				const pattern = "its output {string} is captured when the step succeeds"
				outputName, err := paramAt(pattern, p, 0)
				if err != nil {
					return CaptureDraft{}, err
				}

				// The identities are MINTED HERE, not named by the feature
				// file, for the same reason the daemon-side sentence mints
				// them: a scenario that could choose a handoff could make two
				// scenarios collide on one node's ledger.
				return CaptureDraft{
					Draft:  in,
					Output: hangaroutput.OutputName(outputName),
					Admission: hangaroutput.CaptureAdmission{
						ProtocolVersion: hangaroutput.ProtocolVersion,
						Execution: executioncontrol.Identity{
							ExecutionID: executioncontrol.ExecutionID(freshUUID()),
							Fence:       1,
						},
						ActivationEpoch: executioncontrol.ActivationEpoch(hangarEpoch),
						HandoffID:       hangaroutput.HandoffID(freshUUID()),
						SourceLeaseID:   hangaroutput.SourceLeaseID(freshUUID()),
						Output:          hangaroutput.OutputName(outputName),
						CaptureDeadline: hangaroutput.NewTimestamp(time.Now().UTC().Add(time.Hour)),
					},
				}, nil
			},
		),

		// The second way in, for chains that need the daemon as well as the pod.
		// It is a different sentence rather than a second definition of the one
		// above, because brine's registry is keyed on the sentence alone: one
		// pattern has exactly one input type, and a duplicate pattern is caught
		// by TestNoStepLineMatchesTwoDefinitions.
		//
		// WHAT IT BUILDS IS AN ADMISSION, not a Pod. Everything a Phase 3
		// scenario needs from this sentence is the identities the control plane
		// predeclares before anything may run -- the handoff, the source lease,
		// the exact execution, the declared output and the activation epoch --
		// and none of that is a Pod fact. The Pod-shaped assertions still enter
		// through `the capture pod is built`, which stays Phase 4's.
		//
		// The identities are MINTED HERE, not named by the feature file. A
		// scenario that could choose a handoff id could make two scenarios
		// collide on one node's ledger, and a scenario that could choose an
		// activation epoch would be choosing which key signs its receipts.
		brine.DefineMap[HangarDaemon, CaptureDraft](
			"a capture-selected task {string} built from image {string} declares the output {string}",
			func(in HangarDaemon, p brine.Params, _ *brine.Recorder) (CaptureDraft, error) {
				const pattern = "a capture-selected task {string} built from image {string} declares the output {string}"
				name, err := paramAt(pattern, p, 0)
				if err != nil {
					return CaptureDraft{}, err
				}
				image, err := paramAt(pattern, p, 1)
				if err != nil {
					return CaptureDraft{}, err
				}
				outputName, err := paramAt(pattern, p, 2)
				if err != nil {
					return CaptureDraft{}, err
				}

				draft := CaptureDraft{
					Daemon: in,
					Output: hangaroutput.OutputName(outputName),
					Admission: hangaroutput.CaptureAdmission{
						ProtocolVersion: hangaroutput.ProtocolVersion,
						Execution: executioncontrol.Identity{
							ExecutionID: executioncontrol.ExecutionID(freshUUID()),
							Fence:       1,
						},
						ActivationEpoch: executioncontrol.ActivationEpoch(hangarEpoch),
						HandoffID:       hangaroutput.HandoffID(freshUUID()),
						SourceLeaseID:   hangaroutput.SourceLeaseID(freshUUID()),
						Output:          hangaroutput.OutputName(outputName),
						CaptureDeadline: hangaroutput.NewTimestamp(time.Now().UTC().Add(time.Hour)),
					},
				}
				draft.Draft.StepName, draft.Draft.ImageURL = name, image

				// The execution is admitted before anything may hold or start.
				// That is the ordering the base protocol exists to impose, and
				// doing it here rather than inside `the daemon holds the
				// source` keeps the two facts separable: a scenario can admit
				// and then never hold.
				// The envelope names no Pod, and could not: the reservation the
				// producer will mount is issued against this admission, so the
				// admission precedes the Pod. The draft mints a Pod UID here
				// only because the next step -- the hold -- is the one made
				// from inside the Pod, and that is where the daemon binds it.
				answer := in.base("admit", "/execution/v1/admit", draft.Admission.Execution,
					executioncontrol.Envelope{
						ProtocolVersion: executioncontrol.ProtocolVersion,
						Identity:        draft.Admission.Execution,
						ActivationEpoch: draft.Admission.ActivationEpoch,
						NodeUID:         hangarNodeUID,
						Capability:      "opaque-admission-capability",
					})
				if _, err := decodeControl[executioncontrol.ClassifyResult](answer); err != nil {
					return CaptureDraft{}, fmt.Errorf("admitting the capture-selected execution: %w", err)
				}
				draft.PodUID = executioncontrol.PodUID(freshUUID())

				return draft, nil
			},
		),

		// It records an ATTEMPT. Production never reaches a state where two
		// outputs are selected; it reaches a state where a second was asked
		// for, and the refusal is what `the capture pod is built` returns.
		Refine[CaptureDraft]("a second output {string} is also selected for capture",
			func(in CaptureDraft, a Args) CaptureDraft {
				in.SecondOutput = hangaroutput.OutputName(a.String(0))

				return in
			}),

		stubMap[CaptureDraft, CaptureDraft](
			"the worker's cohort is ready for {string}",
			"Phase 8 Green",
			"the per-facet readiness labels and the authenticated extension handshake"),

		stubMap[CaptureDraft, CaptureDraft](
			"the daemon cohort has not handshaked",
			"Phase 8 Green",
			"the extension handshake a ready label alone is never a substitute for"),

		brine.DefineMap[CaptureDraft, CapturePodCreated](
			"the capture pod is built",
			func(in CaptureDraft, _ brine.Params, _ *brine.Recorder) (CapturePodCreated, error) {
				return buildCapturePod(in)
			},
		),

		// Checks, terminal over the Pod that came back.

		CheckThat[CapturePodCreated]("the capture control init runs before every writer",
			captureControlIsFirstInitContainer),

		CheckThat[CapturePodCreated](
			"the capture control init is the only container carrying the source-control grant",
			captureGrantIsInExactlyTheControlInit),

		// The absence half. Its positive control is the line above it, in the
		// same scenario: "no credential" passes on a worker that ignores its
		// configuration entirely.
		CheckThat[CapturePodCreated]("the task's containers carry no Hangar credential",
			taskContainersCarryNoHangarCredential),

		CheckThat[CapturePodCreated](
			"the capture pod carries the base control handshake and the Downward API pod and node fields",
			captureCarriesHandshakeAndDownwardAPI),

		// A COUNT, not membership. The migration's own GAP rows warn that
		// brine's mount steps are membership by default, and "every mount
		// resolves to a declared Volume" is only a real claim when the number
		// of mounts is pinned too.
		CheckInt[CapturePodCreated](
			"the capture pod declares {int} mounts, and every one resolves to a declared Volume",
			"the number of mounts that resolve to a declared Volume",
			captureResolvedMountCount),

		// The reservation, read off the Pod. It is the claim the whole
		// completion pass is about: the producer's declared output volume is
		// the location the daemon issued, not this step's own directory.
		//
		// The assertion is that the pod builder REPEATED what it was given.
		// The reservation in this chain is the fixture's stand-in (there is no
		// daemon in a pod-shape scenario), so what would redden here is a
		// builder that composed a path of its own -- which is exactly what it
		// used to do.
		CheckThat[CapturePodCreated](
			"the captured output is mounted at the incarnation the daemon reserved",
			capturedOutputMountsTheReservation),

		CheckInt[CapturePodCreated]("the capture pod carries {int} init containers",
			"the number of init containers",
			func(in CapturePodCreated) (int, error) {
				if in.Pod == nil {
					return 0, fmt.Errorf("no capture pod was built: %v", in.Err)
				}

				return len(in.Pod.Spec.InitContainers), nil
			}),

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

		CheckContains[CapturePodCreated]("the pod build is refused saying {string}",
			"the refusal",
			func(in CapturePodCreated) (string, error) {
				if in.Err == nil {
					return "", fmt.Errorf("the capture pod was built; nothing was refused")
				}

				return in.Err.Error(), nil
			}),

		stubCheck[CapturePodCreated](
			"the pod's fetch init container reads from the bucket {string}",
			"Phase 9",
			"the strict-input bucket the output plane must never redirect"),

		// The ORDINARY pod's half of the control scenario, over PodCreated
		// rather than CapturePodCreated: the pod a task that selected nothing
		// builds carries no output-plane container at all. Without this line
		// the control scenario cannot redden on a buildPod that emits the
		// capture control init when there is no capture.
		CheckThat[PodCreated]("the pod carries no output-plane container",
			func(in PodCreated) error {
				if in.Pod == nil {
					return fmt.Errorf("no pod was created")
				}
				for _, container := range in.Pod.Spec.InitContainers {
					if container.Name == captureControlInitName {
						return fmt.Errorf("the pod for %q carries the capture control init, and "+
							"this step selected no output for capture; an ordinary pipeline's pod "+
							"is byte-identical to the one built before the output plane existed",
							in.Handle)
					}
				}
				for _, carrier := range containersCarrying(in.Pod, captureGrantEnvName) {
					return fmt.Errorf("container %q of an ordinary pod carries the source-control "+
						"grant", carrier)
				}

				return nil
			}),
	}
}

// buildCapturePod runs the described container through the worker the way the
// ATC does, with the control envelope on the spec, and reads back what the
// cluster was actually asked for.
//
// It is `runDraft` with one field added, deliberately: if the capture path
// composed its own ContainerSpec the control scenario would be comparing two
// pods this fixture built two different ways, which proves nothing about
// production. The ONLY difference between the pod this function asks for and
// the pod `the container runs` asks for is ContainerSpec.ExecutionControl.
func buildCapturePod(in CaptureDraft) (CapturePodCreated, error) {
	draft := in.Draft

	control := &runtime.ExecutionControl{
		Version:         runtime.ExecutionControlVersion,
		Phase:           runtime.ControlPhaseAdmitted,
		Identity:        in.Admission.Execution,
		ActivationEpoch: in.Admission.ActivationEpoch,
		Endpoint:        "http://127.0.0.1:7781",
		Capability:      "brine-base-capability",
	}
	reserved := scenarioReservation(in)
	selection := runtime.DurableOutputCapture{
		Version:             runtime.DurableOutputCaptureVersion,
		Identity:            in.Admission.Execution,
		ActivationEpoch:     in.Admission.ActivationEpoch,
		HandoffID:           in.Admission.HandoffID,
		SourceLeaseID:       in.Admission.SourceLeaseID,
		Output:              string(in.Output),
		SourceControlGrant:  captureGrantForScenario,
		CaptureDeadline:     in.Admission.CaptureDeadline.Time,
		ReservedIncarnation: reserved.Incarnation,
		ReservedDirectory:   reserved.Directory,
		ReservingNode:       scenarioReservingNode,
	}
	if err := control.SelectCapture(selection); err != nil {
		return CapturePodCreated{Draft: in, Err: err}, nil
	}
	if in.SecondOutput != "" {
		second := selection
		second.Output = string(in.SecondOutput)
		if err := control.SelectCapture(second); err != nil {
			return CapturePodCreated{Draft: in, Err: err}, nil
		}
	}

	inputs, err := draftInputs(draft)
	if err != nil {
		return CapturePodCreated{}, err
	}
	outputs := runtime.OutputPaths{}
	for i, path := range draft.Outputs {
		outputs[fmt.Sprintf("output-%d", i)] = path
	}
	// The captured output is named, so it has to BE one of the declared
	// outputs rather than a name beside them: `its output "result" is captured`
	// selects the first declared output and calls it that.
	if len(draft.Outputs) > 0 {
		delete(outputs, "output-0")
		outputs[string(in.Output)] = draft.Outputs[0]
	}

	spec := runtime.ContainerSpec{
		TeamID:           1,
		Dir:              draft.Dir,
		ImageSpec:        runtime.ImageSpec{ImageURL: draft.ImageURL, Privileged: draft.Privileged},
		Env:              draft.ContainerEnv,
		Inputs:           inputs,
		Caches:           draft.Caches,
		ScratchPaths:     draft.Scratch,
		Sidecars:         draft.Sidecars,
		ExecutionControl: control,
	}
	if len(outputs) > 0 {
		spec.Outputs = outputs
	}

	created, err := runDraft(draft, draftContainerType(draft.ContainerType), spec, draft.RanBefore)
	if err != nil {
		// A refusal is a VALUE here: "the pod build is refused saying …" is an
		// outcome scenarios assert on, and a fixture that died on it could not.
		return CapturePodCreated{Draft: in, Err: err}, nil
	}

	in.Reserved = reserved

	return CapturePodCreated{Draft: in, Pod: created.Pod}, nil
}

// scenarioReservation is the daemon's answer, or a stand-in with the same shape
// when this chain has no daemon.
//
// The pod-shape scenarios enter through `a jetbridge worker with an artifact
// store` and never start an output daemon, so there is nothing to ask. What
// they assert is not WHICH location was reserved -- that is the daemon's own
// suite -- but that the pod builder REPEATS whatever reservation it was handed,
// rather than composing `steps/<handle>/<output>` the way it used to. A
// stand-in makes that assertion possible; a scenario that named a directory
// would not, and there is deliberately no phrase that lets one.
//
// The generation is fixed rather than random so the mount assertion can name
// the directory it expects.
func scenarioReservation(in CaptureDraft) hangaroutput.ReservedIncarnation {
	if in.Reserved.Directory != "" {
		return in.Reserved
	}
	incarnation := hangaroutput.SourceIncarnation{
		ExecutionID:      in.Admission.Execution.ExecutionID,
		NodeUID:          hangarNodeUID,
		HandleGeneration: 4,
		Output:           in.Output,
	}

	return hangaroutput.ReservedIncarnation{
		ProtocolVersion: hangaroutput.ProtocolVersion,
		Execution:       in.Admission.Execution,
		ActivationEpoch: in.Admission.ActivationEpoch,
		HandoffID:       in.Admission.HandoffID,
		SourceLeaseID:   in.Admission.SourceLeaseID,
		NodeUID:         hangarNodeUID,
		Incarnation:     incarnation,
		Directory:       incarnation.Directory(),
		LedgerSequence:  4,
		ObservedAt:      hangaroutput.NewTimestamp(time.Now().UTC()),
	}
}

// captureGrantForScenario is the attenuated source-control grant the control
// init carries. It is a fixture value with no authority; what the scenarios
// assert is WHERE it appears, not what it opens.
const captureGrantForScenario = "brine-source-control-grant"

// The names the pod-shape assertions read. They are the production spellings,
// restated here so a rename that moved the grant to a different variable is a
// compile-time or an assertion failure rather than a scan that finds nothing.
const (
	captureControlInitName = "hangar-capture-control"
	captureGrantEnvName    = "HANGAR_SOURCE_CONTROL_GRANT"
)

func captureControlIsFirstInitContainer(in CapturePodCreated) error {
	if in.Pod == nil {
		return fmt.Errorf("no capture pod was built: %v", in.Err)
	}
	inits := in.Pod.Spec.InitContainers
	if len(inits) == 0 {
		return fmt.Errorf("the capture pod carries no init containers at all")
	}
	if inits[0].Name != captureControlInitName {
		names := make([]string, 0, len(inits))
		for _, c := range inits {
			names = append(names, c.Name)
		}

		return fmt.Errorf("the first init container is %q, not the capture control init; "+
			"the init containers are %v and every one of them after index 0 writes into the "+
			"tree the hold protects", inits[0].Name, names)
	}

	// "Before every writer" is only a claim when there IS a writer after it.
	// A pod whose sole init container is the control init would pass an index
	// check while asserting nothing about ordering.
	writers := len(inits) - 1 + len(in.Pod.Spec.Containers)
	if writers == 0 {
		return fmt.Errorf("nothing follows the capture control init, so its position says nothing")
	}
	for _, c := range inits[1:] {
		if c.Name == captureControlInitName {
			return fmt.Errorf("the capture control init appears twice")
		}
	}

	return nil
}

func captureGrantIsInExactlyTheControlInit(in CapturePodCreated) error {
	if in.Pod == nil {
		return fmt.Errorf("no capture pod was built: %v", in.Err)
	}
	carrying := containersCarrying(in.Pod, captureGrantEnvName)
	if len(carrying) != 1 || carrying[0] != captureControlInitName {
		return fmt.Errorf("the source-control grant is carried by %v; it belongs to the capture "+
			"control init and to nothing else in this pod", carrying)
	}

	return nil
}

func taskContainersCarryNoHangarCredential(in CapturePodCreated) error {
	if in.Pod == nil {
		return fmt.Errorf("no capture pod was built: %v", in.Err)
	}
	// Everything that is not the control init: the task, its sidecars, and
	// every other init container.
	for _, name := range []string{captureGrantEnvName, "HANGAR_CAPTURE_CAPABILITY"} {
		for _, carrier := range containersCarrying(in.Pod, name) {
			if carrier == captureControlInitName {
				continue
			}

			return fmt.Errorf("container %q carries %s; Req 24 gives the task and its sidecars "+
				"no output-plane credential at all", carrier, name)
		}
	}

	return nil
}

// containersCarrying names every container in the pod with an env var of that
// name, init containers included.
func containersCarrying(pod *corev1.Pod, envName string) []string {
	var carrying []string
	for _, group := range [][]corev1.Container{pod.Spec.InitContainers, pod.Spec.Containers} {
		for _, container := range group {
			for _, env := range container.Env {
				if env.Name == envName {
					carrying = append(carrying, container.Name)

					break
				}
			}
		}
	}

	return carrying
}

func captureCarriesHandshakeAndDownwardAPI(in CapturePodCreated) error {
	if in.Pod == nil {
		return fmt.Errorf("no capture pod was built: %v", in.Err)
	}
	var control *corev1.Container
	for i := range in.Pod.Spec.InitContainers {
		if in.Pod.Spec.InitContainers[i].Name == captureControlInitName {
			control = &in.Pod.Spec.InitContainers[i]

			break
		}
	}
	if control == nil {
		return fmt.Errorf("the capture pod has no %s container", captureControlInitName)
	}

	values := map[string]corev1.EnvVar{}
	for _, env := range control.Env {
		values[env.Name] = env
	}

	// The base handshake: the protocol this cohort speaks, the exact identity
	// and its fence, the epoch it was admitted under, and where to ask.
	for name, want := range map[string]string{
		"HANGAR_PROTOCOL_VERSION": hangaroutput.ProtocolVersion,
		"HANGAR_EXECUTION_ID":     string(in.Draft.Admission.Execution.ExecutionID),
		"HANGAR_EXECUTION_FENCE":  fmt.Sprintf("%d", in.Draft.Admission.Execution.Fence),
		"HANGAR_ACTIVATION_EPOCH": fmt.Sprintf("%d", in.Draft.Admission.ActivationEpoch),
		"HANGAR_HANDOFF_ID":       string(in.Draft.Admission.HandoffID),
		"HANGAR_SOURCE_LEASE_ID":  string(in.Draft.Admission.SourceLeaseID),
	} {
		got, present := values[name]
		if !present {
			return fmt.Errorf("the control init carries no %s", name)
		}
		if got.Value != want {
			return fmt.Errorf("%s is %q and the execution it extends says %q", name, got.Value, want)
		}
	}

	// The Downward API fields, by EXACT field path. `metadata.uid` is the Pod
	// incarnation a writer ticket binds to; `spec.nodeName` and
	// `status.hostIP` are how the container reaches the daemon that owns this
	// node's ledger. A literal value here would be the control plane filling
	// in a guess: neither exists when the Pod is composed.
	for name, path := range map[string]string{
		"HANGAR_POD_UID":   "metadata.uid",
		"HANGAR_NODE_NAME": "spec.nodeName",
		"HANGAR_HOST_IP":   "status.hostIP",
	} {
		got, present := values[name]
		if !present {
			return fmt.Errorf("the control init carries no %s", name)
		}
		if got.ValueFrom == nil || got.ValueFrom.FieldRef == nil {
			return fmt.Errorf("%s is a literal %q rather than a Downward API field reference; "+
				"neither the Pod UID nor the node exists when this Pod is composed", name, got.Value)
		}
		if got.ValueFrom.FieldRef.FieldPath != path {
			return fmt.Errorf("%s reads %q and the field that carries it is %q",
				name, got.ValueFrom.FieldRef.FieldPath, path)
		}
	}

	return nil
}

// capturedOutputMountsTheReservation reads the selected output's volume off the
// Pod and requires its hostPath to end in the reserved directory.
//
// The control is in the same function and checked first: the step's WORKING
// directory still resolves under the step's own handle, so this cannot pass on
// a builder that has started pointing every volume at the incarnation.
func capturedOutputMountsTheReservation(in CapturePodCreated) error {
	if in.Pod == nil {
		return fmt.Errorf("no capture pod was built: %v", in.Err)
	}
	reserved := in.Draft.Reserved
	if reserved.Directory == "" {
		return fmt.Errorf("this chain reserved no incarnation, so there is nothing to repeat")
	}

	hostPath := func(name string) string {
		for _, volume := range in.Pod.Spec.Volumes {
			if volume.Name != name {
				continue
			}
			if volume.HostPath == nil {
				return ""
			}

			return volume.HostPath.Path
		}

		return ""
	}
	mounted := map[string]string{}
	for _, container := range in.Pod.Spec.Containers {
		for _, mount := range container.VolumeMounts {
			mounted[mount.MountPath] = hostPath(mount.Name)
		}
	}

	// The control: an ordinary step volume is unchanged.
	dir := in.Draft.Draft.Dir
	if dir != "" {
		if path := mounted[dir]; !strings.HasSuffix(path, "/steps/"+in.Draft.Draft.Handle+"/dir") {
			return fmt.Errorf("the step's working directory resolves to %q, which is not this "+
				"step's own directory; a builder that pointed everything at the incarnation "+
				"would pass the assertion below while breaking every ordinary volume", path)
		}
	}

	outputPath := ""
	for _, path := range in.Draft.Draft.Outputs {
		outputPath = path

		break
	}
	if outputPath == "" {
		return fmt.Errorf("this scenario declares no output")
	}
	path, found := mounted[outputPath]
	if !found {
		return fmt.Errorf("the task container mounts nothing at the captured output %q", outputPath)
	}
	if !strings.HasSuffix(path, "/steps/"+reserved.Directory) {
		return fmt.Errorf("the captured output is mounted from %q and the daemon reserved "+
			"%q. A producer writing anywhere else is a hold over bytes nobody wrote",
			path, reserved.Directory)
	}

	return nil
}

// captureResolvedMountCount counts the mounts that resolve, and returns the
// count so the sentence can pin it.
//
// It is a COUNT and not a membership test. brine's mount steps are membership
// by default, and "every mount resolves to a declared Volume" passes trivially
// on a pod with no mounts -- so the number is the assertion and the resolution
// is the precondition for counting one at all.
func captureResolvedMountCount(in CapturePodCreated) (int, error) {
	if in.Pod == nil {
		return 0, fmt.Errorf("no capture pod was built: %v", in.Err)
	}
	declared := map[string]struct{}{}
	for _, volume := range in.Pod.Spec.Volumes {
		declared[volume.Name] = struct{}{}
	}

	resolved := 0
	for _, group := range [][]corev1.Container{in.Pod.Spec.InitContainers, in.Pod.Spec.Containers} {
		for _, container := range group {
			for _, mount := range container.VolumeMounts {
				if _, ok := declared[mount.Name]; !ok {
					return 0, fmt.Errorf("container %q mounts %q, which no Pod volume declares",
						container.Name, mount.Name)
				}
				resolved++
			}
		}
	}

	return resolved, nil
}
