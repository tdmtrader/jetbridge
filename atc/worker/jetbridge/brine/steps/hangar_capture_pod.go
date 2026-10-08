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

	"github.com/brine-dev/brine-go/pkg/brine"

	corev1 "k8s.io/api/core/v1"

	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar/executioncontrol"
	hangaroutput "github.com/concourse/concourse/hangar/output"
)

const capturePodPhase = "Phase 4 Green"

// scenarioCaptureNode is the Kubernetes node the scenario's capture row names.
// A step directory is a directory on ONE node's disk, so the capture pod
// carries a required affinity on this node by name; the node UID beside it
// (hangarNodeUID) is what the daemon on that node writes its markers under.
const scenarioCaptureNode = "hangar-node-a"

// HangarCapturePodDefinitions is the capture-pod family.
func HangarCapturePodDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// THE ONLY WAY INTO CaptureDraft, and it is a REFINEMENT over a draft
		// that has not run. There is deliberately no sentence that selects
		// capture after the container runs, so "a late request captures an
		// already-running task" is a state no scenario can build.
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
				// them: a scenario that could choose an execution could make
				// two scenarios collide on one node's markers.
				return CaptureDraft{
					Draft:     in,
					Output:    hangaroutput.OutputName(outputName),
					Admission: newCaptureAdmission(hangaroutput.OutputName(outputName)),
				}, nil
			},
		),

		// The second way in, for chains that need the daemon as well as the pod.
		// It is a different sentence rather than a second definition of the one
		// above, because brine's registry is keyed on the sentence alone: one
		// pattern has exactly one input type, and a duplicate pattern is caught
		// by TestNoStepLineMatchesTwoDefinitions.
		//
		// WHAT IT BUILDS IS AN ADMISSION, not a Pod. Everything these
		// scenarios need from this sentence is what the capture row carries
		// before anything may run -- the exact execution and the declared
		// output -- and none of that is a Pod fact. The
		// Pod-shaped assertions still enter through `the capture pod is built`.
		//
		// The identity is MINTED HERE, not named by the feature file. A
		// scenario that could choose an execution could make two scenarios
		// collide on one node's markers.
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
					Daemon:    in,
					Output:    hangaroutput.OutputName(outputName),
					Admission: newCaptureAdmission(hangaroutput.OutputName(outputName)),
				}
				draft.Draft.StepName, draft.Draft.ImageURL = name, image

				// The execution is admitted before anything may hold or start.
				// That is the ordering the base protocol exists to impose, and
				// doing it here rather than inside `the daemon holds the
				// source` keeps the two facts separable: a scenario can admit
				// and then never hold.
				// The envelope names no Pod, and could not: the admission
				// precedes the Pod. The draft mints a Pod UID here only because
				// the next step -- the hold -- is the one made from inside the
				// Pod, and that is where the daemon binds it.
				answer := in.base("admit", "/execution/v1/admit", draft.Admission.Execution,
					executioncontrol.Envelope{
						ProtocolVersion: executioncontrol.ProtocolVersion,
						Identity:        draft.Admission.Execution,
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

		// The scheduling refinements. They REBUILD the worker, because what
		// they describe is a fact about the deployment rather than about this
		// step: which ready labels its nodes carry is configuration the worker
		// was started with, and a phrase that only recorded an intention on the
		// draft would be a sentence with nothing behind it.
		brine.DefineMapUsing[CaptureDraft, CaptureDraft](
			"the worker's nodes are ready for {string}",
			[]string{"jetbridge-db", "real-cluster"},
			func(in CaptureDraft, p brine.Params, rec *brine.Recorder, res brine.Resources) (CaptureDraft, error) {
				facet, ok := p.GetString(0)
				if !ok {
					return CaptureDraft{}, fmt.Errorf("expected a facet parameter")
				}

				return withReadyLabels(in, res, rec, facet)
			},
		),

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

		// The step directory, read off the Pod. The producer's declared
		// output volume is the capture's step directory -- the one derivation
		// of CaptureKey.Directory() the node's daemon, its marker and its
		// sweeper all key on -- not this step's own directory.
		CheckThat[CapturePodCreated](
			"the captured output is mounted at the capture's step directory",
			capturedOutputMountsTheStepDirectory),

		CheckInt[CapturePodCreated]("the capture pod carries {int} init containers",
			"the number of init containers",
			func(in CapturePodCreated) (int, error) {
				if in.Pod == nil {
					return 0, fmt.Errorf("no capture pod was built: %v", in.Err)
				}

				return len(in.Pod.Spec.InitContainers), nil
			}),

		CheckMember[CapturePodCreated](
			"the capture pod is admitted only by a node carrying {string}",
			"the node-selector keys the capture pod requires",
			capturePodRequiredLabelKeys),

		// A COUNT of the READY labels, not membership. "the pod requires this
		// label" passes on a pod that requires it and eleven others; the claim
		// is that a capture pod needs exactly the two, and neither alone
		// admits it.
		CheckInt[CapturePodCreated]("the capture pod requires {int} ready labels",
			"the number of required ready labels",
			capturePodReadyLabelCount),

		// The node pin, declared here in Phase 5 and executed in Phase 8, per
		// the Phase 4 round-2 ruling 3. It is a stub beside its two affinity
		// siblings rather than a live step above them, because the scenarios
		// that exercise ready labels are Phase 8's and splitting the family is
		// what the ruling declined. The production half exists and is red
		// under M8 in Go
		// (TestACaptureSelectedPodIsPinnedToTheReservingNodeAndAnOrdinaryOneIsNot).
		// The node pin. A step directory is a directory on ONE node's disk,
		// named by the capture row before this Pod existed, so a label-wide
		// placement lets the scheduler land the producer on a node the row
		// does not name -- where the daemon writes no marker the control plane
		// will ever ask about. Requiring the node turns that
		// outage into a pending Pod.
		CheckString[CapturePodCreated](
			"the capture pod is admitted only by the node {string}",
			"the node the capture pod is pinned to",
			capturePodRequiredNode),

		// The ABSENCE, whose control is the line above it in every scenario
		// that uses it: "no capture pod" passes on a worker that builds no pods
		// at all, so the refusal's own text is asserted first.
		CheckThat[CapturePodCreated]("no capture pod is built",
			func(in CapturePodCreated) error {
				if in.Pod == nil {
					return nil
				}

				return fmt.Errorf("a capture pod was built: %s", in.Pod.Name)
			}),

		// The control that makes the absence mean something, in the same
		// scenario (convention 5): the very same worker still builds an
		// ordinary pod for a step that captures nothing.
		CheckThat[CapturePodCreated](
			"the same worker still builds an ordinary pod for a step that captures nothing",
			sameWorkerStillBuildsAnOrdinaryPod),

		CheckContains[CapturePodCreated]("the pod build is refused saying {string}",
			"the refusal",
			func(in CapturePodCreated) (string, error) {
				if in.Err == nil {
					return "", fmt.Errorf("the capture pod was built; nothing was refused")
				}

				return in.Err.Error(), nil
			}),

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
		Version:    runtime.ExecutionControlVersion,
		Phase:      runtime.ControlPhaseAdmitted,
		Identity:   in.Admission.Execution,
		Endpoint:   "http://127.0.0.1:7781",
		Capability: "brine-base-capability",
	}
	selection := runtime.DurableOutputCapture{
		Version:            runtime.DurableOutputCaptureVersion,
		Identity:           in.Admission.Execution,
		Output:             string(in.Output),
		SourceControlGrant: captureGrantForScenario,
		CaptureDeadline:    in.Admission.CaptureDeadline,
		Node:               scenarioCaptureNode,
		NodeUID:            hangarNodeUID,
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

	return CapturePodCreated{Draft: in, Pod: created.Pod}, nil
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

			return fmt.Errorf("container %q carries %s; the task and its sidecars get "+
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

	// The base handshake: the protocol the node speaks, the exact identity
	// and its fence, and where to ask.
	for name, want := range map[string]string{
		"HANGAR_PROTOCOL_VERSION": hangaroutput.ProtocolVersion,
		"HANGAR_EXECUTION_ID":     string(in.Draft.Admission.Execution.ExecutionID),
		"HANGAR_EXECUTION_FENCE":  fmt.Sprintf("%d", in.Draft.Admission.Execution.Fence),
		"HANGAR_OUTPUT_NAME":      string(in.Draft.Admission.Output),
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
	// the held marker binds to; `spec.nodeName` and
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

// capturedOutputMountsTheStepDirectory reads the selected output's volume off
// the Pod and requires its hostPath to end in the capture's step directory.
//
// The control is in the same function and checked first: the step's WORKING
// directory still resolves under the step's own handle, so this cannot pass on
// a builder that has started pointing every volume at the step directory.
func capturedOutputMountsTheStepDirectory(in CapturePodCreated) error {
	if in.Pod == nil {
		return fmt.Errorf("no capture pod was built: %v", in.Err)
	}
	directory := in.Draft.Admission.Key().Directory()

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
				"step's own directory; a builder that pointed everything at the step directory "+
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
	if !strings.HasSuffix(path, "/steps/"+directory) {
		return fmt.Errorf("the captured output is mounted from %q and the capture's step "+
			"directory is %q. A producer writing anywhere else is a hold over bytes nobody wrote",
			path, directory)
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

// ---------------------------------------------------------------------------
// The scheduling block
// ---------------------------------------------------------------------------

// withReadyLabels rebuilds the worker for one ready label.
//
// A REBUILD rather than a mutation, because which labels its nodes carry is
// configuration the worker was started with. The draft is a description at this
// point -- no container has been created -- so replacing the cluster under it is
// safe, and it is what makes the phrase a construction rather than a note.
func withReadyLabels(in CaptureDraft, res brine.Resources, rec *brine.Recorder,
	facet string) (CaptureDraft, error) {
	outputFacet := false
	switch facet {
	case executioncontrol.ReadyLabel:
		// The BASE label alone. A base-only worker is a real deployment -- it
		// is the one the sibling exact_execution_control track schedules onto.
	case hangaroutput.ReadyLabel, "hangar-output-v1":
		outputFacet = true
	default:
		return CaptureDraft{}, fmt.Errorf("no such facet %q; there are two, "+
			"%s and %s", facet, executioncontrol.ReadyLabel, hangaroutput.ReadyLabel)
	}

	// A DISTINCT worker name per rebuild, because a worker is a row keyed on its
	// name: two rebuilds sharing one would be the second redefining the first's
	// registration rather than standing beside it.
	ready, err := newWorkerReady(res, rec,
		"k8s-worker-ready-"+strings.TrimPrefix(facet, "concourse.dev/"), "",
		func(cfg *jetbridge.Config) {
			cfg.ArtifactDaemonHostPath = "/var/concourse/artifacts"
			cfg.OutputPlaneEnabled = outputFacet
		})
	if err != nil {
		return CaptureDraft{}, err
	}

	in.Draft.Namespace = ready.Namespace
	in.Draft.Worker = ready.Worker
	in.Draft.Clientset = ready.Clientset
	in.Draft.MountExecutor = ready.ProducerExecutor
	in.Draft.workerWith = ready.workerWith
	in.Draft.Ctx = ready.Ctx
	// The team the rebuilt worker's volumes hang off. The draft was carrying
	// the team of the worker this one replaces, and an input volume created
	// against a team row is the one thing in this chain that reads it.
	in.Draft.TeamID = ready.TeamID
	in.ReadyFacets = append(in.ReadyFacets, facet)

	return in, nil
}

// requiredNodeSelector is the single required term BuildAffinity emits.
//
// Single by construction: the whole affinity is one NodeSelectorTerm whose
// expressions are ANDed, because "this node has the cache AND both facets AND is
// the reserving node" is one conjunction. A second term would be an OR, and an
// OR would admit a node that had only some of them.
func requiredNodeSelector(in CapturePodCreated) ([]corev1.NodeSelectorRequirement, error) {
	if in.Pod == nil {
		return nil, fmt.Errorf("no capture pod was built: %v", in.Err)
	}
	affinity := in.Pod.Spec.Affinity
	if affinity == nil || affinity.NodeAffinity == nil ||
		affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		return nil, fmt.Errorf("the capture pod carries no required node affinity at all")
	}
	terms := affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	if len(terms) != 1 {
		return nil, fmt.Errorf("the capture pod carries %d node selector terms; separate terms "+
			"are ORed, so more than one would admit a node that satisfies only some of them",
			len(terms))
	}

	return terms[0].MatchExpressions, nil
}

// capturePodRequiredLabelKeys is every key the pod requires, so a membership
// check names one and the count beside it pins the rest.
func capturePodRequiredLabelKeys(in CapturePodCreated) ([]string, error) {
	expressions, err := requiredNodeSelector(in)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(expressions))
	for _, expression := range expressions {
		keys = append(keys, expression.Key)
	}

	return keys, nil
}

// capturePodReadyLabelCount counts the READY-label requirements and nothing
// else: the artifact cache label and the hostname pin are required too, and
// counting them would make "2" an accident of how many other terms exist.
func capturePodReadyLabelCount(in CapturePodCreated) (int, error) {
	expressions, err := requiredNodeSelector(in)
	if err != nil {
		return 0, err
	}

	ready := 0
	for _, expression := range expressions {
		switch expression.Key {
		case executioncontrol.ReadyLabel, hangaroutput.ReadyLabel:
			if expression.Operator != corev1.NodeSelectorOpIn ||
				len(expression.Values) != 1 || expression.Values[0] != "ready" {
				return 0, fmt.Errorf("%s is required with %s %v rather than In [ready]",
					expression.Key, expression.Operator, expression.Values)
			}
			ready++
		}
	}

	return ready, nil
}

// capturePodRequiredNode is the ONE node the pod may land on.
func capturePodRequiredNode(in CapturePodCreated) (string, error) {
	expressions, err := requiredNodeSelector(in)
	if err != nil {
		return "", err
	}
	for _, expression := range expressions {
		if expression.Key != corev1.LabelHostname {
			continue
		}
		if len(expression.Values) != 1 {
			return "", fmt.Errorf("the capture pod is pinned to %d nodes; a step directory is a "+
				"directory on ONE node's disk", len(expression.Values))
		}

		return expression.Values[0], nil
	}

	return "", fmt.Errorf("the capture pod is not pinned to any node. The two ready labels pick " +
		"a set of nodes; the capture row is narrower than that, so a label-wide placement lets the " +
		"scheduler land the producer on a node the row does not name")
}

// sameWorkerStillBuildsAnOrdinaryPod is the absence's control.
//
// "no capture pod is built" passes on a worker that builds no pods at all, and
// convention 5 says the positive half belongs in the same scenario. So this
// builds an ORDINARY pod -- same worker, same image, same declared output, no
// capture selected -- and requires it to come back.
func sameWorkerStillBuildsAnOrdinaryPod(in CapturePodCreated) error {
	draft := in.Draft.Draft
	if draft.Worker == nil {
		return fmt.Errorf("the chain carries no worker to ask")
	}

	ordinary := draft
	ordinary.Handle = draft.Handle + "-ordinary"

	outputs := runtime.OutputPaths{}
	for index, path := range draft.Outputs {
		outputs[fmt.Sprintf("output-%d", index)] = path
	}
	inputs, err := draftInputs(ordinary)
	if err != nil {
		return err
	}

	spec := runtime.ContainerSpec{
		TeamID:    1,
		Dir:       ordinary.Dir,
		ImageSpec: runtime.ImageSpec{ImageURL: ordinary.ImageURL},
		Env:       ordinary.ContainerEnv,
		Inputs:    inputs,
		Caches:    ordinary.Caches,
	}
	if len(outputs) > 0 {
		spec.Outputs = outputs
	}

	created, err := runDraft(ordinary, draftContainerType(ordinary.ContainerType), spec, false)
	if err != nil {
		return fmt.Errorf("the same worker refused an ORDINARY step as well, so the refusal "+
			"above says nothing about capture: %w", err)
	}
	if created.Pod == nil {
		return fmt.Errorf("the same worker built no ordinary pod either")
	}

	return nil
}
