package steps

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ContainerGapDefinitions closes the places container_test.go and
// behavioral_permutations_test.go were stronger than brine. They are all about
// the pod a container builds, and most were invisible for one reason: every
// pod scenario written before this file runs on a worker with NO storage
// backend — with none configured, stepVolume returns emptyDir for every
// container type, CacheVolume is never reached and buildCleanupInitContainer
// returns nil, so the mutations change nothing a scenario can see.
//
// The second wave adds five more, each measured: a mutation was applied to
// production, the Go suite in behavioral_permutations_test.go went red, and
// every brine scenario stayed green.
//
//   - the subdir an input shares with an output (buildVolumeMounts dropping
//     `subdir = outName`),
//   - a relative scratch path resolved against "/" instead of the working
//     directory,
//   - task caches defaulting to emptyDir when a storage backend is
//     configured and nobody named a cache store,
//   - the ORDER of the init containers, which is the sharpest of them: swap
//     the two appends in buildPod and the pod deletes the inputs it has just
//     fetched, with every existing scenario still green because they only ask
//     whether both containers are present,
//   - and a reused CHECK container being handed a cleanup container. That
//     last one is keyed on the type the CONTAINER SPEC declares rather than
//     the metadata's, which is why it needs its own run sentence: the general
//     "the container runs" leaves ContainerSpec.Type empty, so a scenario
//     written on it cannot tell a check from a task here at all.

// draftContainerType defaults an unset draft to a task, which is what every
// scenario written before check containers existed assumes.
func draftContainerType(t db.ContainerType) db.ContainerType {
	if t == "" {
		return db.ContainerTypeTask
	}
	return t
}

func ContainerGapDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// The worker every scenario in this file needs: one with somewhere on
		// the node to keep step data, which is what makes the pod's storage
		// decisions observable at all.
		//
		// It deliberately says NOTHING about caches. CacheStore is the
		// operator's explicit override and leaving it unset is not an
		// omission in the fixture — it is the configuration under test in
		// "With no explicit choice, caches follow the artifact store onto the
		// node". The sibling Given that names a cache store cannot express
		// that scenario, because naming one is the thing it has to not do.
		brine.DefineMapUsing[brine.Empty, ClusterReady](
			"a jetbridge worker with an artifact store",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (ClusterReady, error) {
				return newConfiguredWorker(res, func(cfg *jetbridge.Config) {
					cfg.ArtifactDaemonHostPath = "/var/concourse/artifacts"
				})
			},
		),

		// A check container's working directory must be ephemeral even when
		// the worker keeps step data on the node. The same container handle
		// is reused for every check of a resource, so node-local storage
		// would carry one check's state into the next.
		brine.DefineMap[ClusterReady, ContainerDraft](
			"a check container {string} built from image {string}",
			func(in ClusterReady, p brine.Params, _ *brine.Recorder) (ContainerDraft, error) {
				handle, ok := p.GetString(0)
				if !ok {
					return ContainerDraft{}, fmt.Errorf("expected a container handle parameter")
				}
				image, ok := p.GetString(1)
				if !ok {
					return ContainerDraft{}, fmt.Errorf("expected an image parameter")
				}
				return ContainerDraft{
					Namespace:     in.Namespace,
					Worker:        in.Worker,
					Clientset:     in.Clientset,
					Ctx:           in.Ctx,
					Handle:        handle,
					ImageURL:      image,
					Dir:           "/workdir",
					TeamID:        in.TeamID,
					ContainerType: db.ContainerTypeCheck,
				}, nil
			},
		),

		// A container whose row already exists is REUSED, and a reused
		// container's pod has to clear the workspace the previous run left on
		// the node before anything else starts.
		// An input that carries a real artifact, rather than just a mount
		// path. The backend only emits a fetch init container for inputs it
		// can actually locate.
		Refine[ContainerDraft]("it takes an input at {string} produced by an earlier step",
			func(in ContainerDraft, a Args) ContainerDraft {
				in.ArtifactInputs = append(in.ArtifactInputs, a.String(0))
				return in
			}),

		Refine[ContainerDraft]("the container has run before on this worker",
			func(in ContainerDraft, _ Args) ContainerDraft {
				in.RanBefore = true
				return in
			}),

		CheckThat[PodCreated]("the pod clears the workspace left by the previous run",
			func(in PodCreated) error {
				if !hasInitContainer(in, "cleanup-stale") {
					return fmt.Errorf(
						"the pod for %q has no cleanup init container, so the step starts on top of "+
							"whatever the previous run left in its node-local workspace — the "+
							"\"destination path already exists\" failure", in.Handle)
				}
				return nil
			}),

		// A step reads its inputs from the artifact daemon on its own node, so
		// the pod must not be scheduled anywhere that daemon is not running.
		// Without the requirement the scheduler is free to place it on a node
		// with no artifact cache, where the step cannot read its inputs.
		CheckThat[PodCreated]("the pod is only scheduled where the artifact cache is ready",
			func(in PodCreated) error {
				if in.Pod == nil {
					return fmt.Errorf("no pod was created")
				}
				aff := in.Pod.Spec.Affinity
				if aff == nil || aff.NodeAffinity == nil ||
					aff.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
					return fmt.Errorf(
						"the pod for %q carries no node requirement, so the scheduler may place it "+
							"on a node with no artifact daemon — the step then cannot read its "+
							"inputs at all", in.Handle)
				}
				for _, term := range aff.NodeAffinity.
					RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
					for _, expr := range term.MatchExpressions {
						if expr.Key == "concourse.dev/artifact-cache" {
							return nil
						}
					}
				}
				return fmt.Errorf(
					"the pod for %q requires a node, but not one running the artifact cache", in.Handle)
			}),

		// Inputs are fetched into the workspace by init containers before the
		// step's own command starts. Without them the step runs against an
		// empty directory and fails on a file it was handed.
		CheckThat[PodCreated]("the pod fetches its inputs before the step starts",
			func(in PodCreated) error {
				if in.Pod == nil {
					return fmt.Errorf("no pod was created")
				}
				for _, c := range in.Pod.Spec.InitContainers {
					if c.Name != "cleanup-stale" {
						return nil
					}
				}
				return fmt.Errorf(
					"the pod for %q has no init container to fetch its inputs, so the step starts "+
						"with an empty workspace and fails on a file it was given", in.Handle)
			}),

		CheckThat[PodCreated]("the pod does not clear the workspace",
			func(in PodCreated) error {
				if hasInitContainer(in, "cleanup-stale") {
					return fmt.Errorf(
						"the pod for %q clears its workspace, but nothing ran here before — a fresh "+
							"container has nothing to clean and the extra init container only costs "+
							"an image pull", in.Handle)
				}
				return nil
			}),

		// ---------------------------------------------------------------
		// The second wave.
		// ---------------------------------------------------------------

		// An input and an output on the same path share ONE volume, and where
		// that volume lives on the node has to be the output's name.
		//
		// This needs its own transition because the generality of "the
		// container runs" is exactly what hides the behaviour: that step
		// names outputs positionally ("output-0"), so a scenario written on
		// it cannot say which name the directory should carry, and the
		// sentence would be asserting against a name brine invented rather
		// than one the pipeline chose.
		brine.DefineMap[ContainerDraft, PodCreated](
			"the container runs with an input and the output {string} both at {string}",
			func(in ContainerDraft, p brine.Params, _ *brine.Recorder) (PodCreated, error) {
				name, _ := p.GetString(0)
				path, ok := p.GetString(1)
				if !ok {
					return PodCreated{}, fmt.Errorf("expected an output name and a path")
				}
				kind := draftContainerType(in.ContainerType)
				return runDraft(in, kind, runtime.ContainerSpec{
					TeamID:    in.TeamID,
					Dir:       in.Dir,
					ImageSpec: runtime.ImageSpec{ImageURL: in.ImageURL},
					Inputs:    []runtime.Input{{DestinationPath: path}},
					Outputs:   runtime.OutputPaths{name: path},
					Type:      kind,
				}, false)
			},
		),

		// Keeps its own body: the two parameters are a lookup key and a NAME
		// the expectation is derived from, not a value to compare, and the
		// three ways it fails are three different diagnoses — nothing is
		// mounted there, the volume is ephemeral, or it is filed under the
		// wrong name. The last is the one this exists for.
		brine.DefineCheck[PodCreated](
			"the volume mounted at {string} is the node directory recorded for the output {string}",
			func(in PodCreated, p brine.Params, _ *brine.Recorder) error {
				mountPath, _ := p.GetString(0)
				outputName, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected a mount path and an output name")
				}
				v, err := volumeAt(in.Pod, mountPath)
				if err != nil {
					return err
				}
				if v.HostPath == nil {
					return fmt.Errorf(
						"expected what the step writes at %q to live on the node, where the daemon "+
							"can serve it to the next step; it is ephemeral and dies with the pod",
						mountPath)
				}
				want := filepath.Join("steps", in.Handle, outputName)
				if !strings.HasSuffix(filepath.Clean(v.HostPath.Path), "/"+want) {
					return fmt.Errorf(
						"the step writes %q into %q, but its output is recorded under the key %q and "+
							"served from a directory ending %q — the next step resolves that key, "+
							"finds a directory nothing ever wrote to, and starts with an empty input "+
							"that looks like a producing step which emitted nothing",
						mountPath, v.HostPath.Path, in.Handle+"/"+outputName, want)
				}
				return nil
			},
		),

		// THE ORDER IS THE BEHAVIOUR. Kubernetes runs init containers in the
		// order the spec lists them, so this is not a detail of how the slice
		// was built: it is what the cluster is being asked to do.
		CheckThat[PodCreated]("the pod clears the workspace before it fetches its inputs",
			func(in PodCreated) error {
				if in.Pod == nil {
					return fmt.Errorf("no pod was created")
				}
				cleanup, fetch := -1, -1
				var order []string
				for i, c := range in.Pod.Spec.InitContainers {
					order = append(order, c.Name)
					switch {
					case c.Name == "cleanup-stale" && cleanup < 0:
						cleanup = i
					case c.Name != "cleanup-stale" && fetch < 0:
						fetch = i
					}
				}
				if cleanup < 0 {
					return fmt.Errorf(
						"the pod for %q never clears its workspace, so the step starts on top of what "+
							"its last attempt left there (init containers: %v)", in.Handle, order)
				}
				if fetch < 0 {
					return fmt.Errorf(
						"the pod for %q fetches nothing, so there is no order to check — the step "+
							"starts against an empty workspace (init containers: %v)", in.Handle, order)
				}
				if cleanup > fetch {
					return fmt.Errorf(
						"the pod for %q runs %v in that order, so it deletes the inputs it has just "+
							"fetched: the cleanup removes this step's whole directory under the store, "+
							"which is where the fetch wrote them. The step then runs against an empty "+
							"workspace and fails on a file it was handed, with nothing in the build "+
							"log to say why", in.Handle, order)
				}
				return nil
			}),

		// A check's handle is reused for every check of the same resource, so
		// "has run before" is a check's ordinary state rather than a crash.
		//
		// Its own sentence rather than "the container runs": the rule under
		// test is keyed on the type the CONTAINER SPEC declares — what
		// check_step.go sets — and the general run step leaves that field
		// empty, which makes a check indistinguishable from a task here.
		brine.DefineMap[ContainerDraft, PodCreated](
			"the same check runs again",
			func(in ContainerDraft, _ brine.Params, _ *brine.Recorder) (PodCreated, error) {
				if kind := draftContainerType(in.ContainerType); kind != db.ContainerTypeCheck {
					return PodCreated{}, fmt.Errorf(
						"this sentence describes a check running again; %q is a %s", in.Handle, kind)
				}
				// A check carries no inputs, outputs, caches or scratch. The
				// guard is here so that a draft which grew some would fail
				// loudly rather than have them silently dropped by a spec
				// that does not carry them.
				if n := len(in.Inputs) + len(in.Outputs) + len(in.Caches) +
					len(in.Scratch) + len(in.ArtifactInputs); n > 0 {
					return PodCreated{}, fmt.Errorf(
						"a check container has no inputs, outputs, caches or scratch, but %q was "+
							"described with %d of them and this sentence would drop them", in.Handle, n)
				}
				return runDraft(in, db.ContainerTypeCheck, runtime.ContainerSpec{
					TeamID:    in.TeamID,
					Dir:       in.Dir,
					ImageSpec: runtime.ImageSpec{ImageURL: in.ImageURL},
					Type:      db.ContainerTypeCheck,
				}, true)
			},
		),

		// Same predicate as "the pod does not clear the workspace", different
		// reason, and the reason is the whole diagnosis — so it says its own.
		CheckThat[PodCreated]("the check's pod does not try to clear a workspace it never kept",
			func(in PodCreated) error {
				if hasInitContainer(in, "cleanup-stale") {
					return fmt.Errorf(
						"the pod for the check %q clears stale workspace data, but a check's "+
							"workspace is an emptyDir that arrives fresh with the pod, so there is "+
							"nothing stale to remove. The cleanup container mounts the artifact "+
							"store's hostPath, which a check's pod does not carry at all, and the "+
							"API server rejects a container mounting a volume the pod never "+
							"defines — so every check of every resource stops", in.Handle)
				}
				return nil
			}),

		// ---------------------------------------------------------------
		// The third wave: the on-disk layout of step volumes.
		//
		// Three more measured gaps, and two of the three are the same
		// disagreement. A step volume lands somewhere under the store's
		// hostPath, and RecordOutputs registers a daemon key that names the
		// SAME place — "<handle>/<name>", served from
		// <store>/steps/<handle>/<name>. When the two halves choose different
		// names nothing fails at the seam: the producing step writes its files
		// and exits 0, the consumer resolves a key the daemon happily creates
		// an empty directory for, and the build breaks inside a task several
		// steps later, on a file the pipeline plainly handed it.
		//
		// The third is the input loop giving up on the whole batch when one
		// input has no artifact to fetch, which fails the same silent way.
		// ---------------------------------------------------------------

		// A get container. Its output is not in ContainerSpec.Outputs at all:
		// what a get produces IS its working directory, which is why
		// RecordOutputs counts spec.Dir as an output for everything that is
		// not a task or a check.
		//
		// Its own Given rather than a refinement on the task one, for the
		// reason the check container has its own: the type has to be on the
		// draft before the container runs, so the run sentence can put it on
		// the CONTAINER SPEC as well as on the metadata.
		brine.DefineMap[ClusterReady, ContainerDraft](
			"a get container {string} built from image {string}",
			func(in ClusterReady, p brine.Params, _ *brine.Recorder) (ContainerDraft, error) {
				handle, ok := p.GetString(0)
				if !ok {
					return ContainerDraft{}, fmt.Errorf("expected a container handle parameter")
				}
				image, ok := p.GetString(1)
				if !ok {
					return ContainerDraft{}, fmt.Errorf("expected an image parameter")
				}
				return ContainerDraft{
					Namespace:     in.Namespace,
					Worker:        in.Worker,
					Clientset:     in.Clientset,
					Ctx:           in.Ctx,
					Handle:        handle,
					ImageURL:      image,
					Dir:           "/workdir",
					TeamID:        in.TeamID,
					ContainerType: db.ContainerTypeGet,
				}, nil
			},
		),

		// Its own sentence rather than "the container runs", for two reasons.
		// The general step leaves ContainerSpec.Type empty — so a get is
		// indistinguishable from a task to anything keyed on the spec, which
		// is what decides whether the working directory counts as an output.
		// And a get carries no inputs, outputs, caches or scratch, so a draft
		// that grew some should say so rather than have them dropped.
		brine.DefineMap[ContainerDraft, PodCreated](
			"the get container runs",
			func(in ContainerDraft, _ brine.Params, _ *brine.Recorder) (PodCreated, error) {
				if kind := draftContainerType(in.ContainerType); kind != db.ContainerTypeGet {
					return PodCreated{}, fmt.Errorf(
						"this sentence describes a get container running; %q is a %s", in.Handle, kind)
				}
				if n := len(in.Inputs) + len(in.Outputs) + len(in.Caches) +
					len(in.Scratch) + len(in.ArtifactInputs); n > 0 {
					return PodCreated{}, fmt.Errorf(
						"a get container's output is its working directory, and its spec carries no "+
							"inputs, outputs, caches or scratch; %q was described with %d of them and "+
							"this sentence would drop them", in.Handle, n)
				}
				return runDraft(in, db.ContainerTypeGet, runtime.ContainerSpec{
					TeamID:    in.TeamID,
					Dir:       in.Dir,
					ImageSpec: runtime.ImageSpec{ImageURL: in.ImageURL},
					Type:      db.ContainerTypeGet,
				}, false)
			},
		),

		// An output the pipeline NAMED, on a path of its own. "it produces an
		// output at {string}" plus "the container runs" cannot express this:
		// that pair names outputs positionally, "output-0", which is one
		// character away from the volume name the mutation files them under —
		// so a scenario written on it would be asserting against a name brine
		// invented, and against a near-miss at that.
		brine.DefineMap[ContainerDraft, PodCreated](
			"the container runs producing the output {string} at {string}",
			func(in ContainerDraft, p brine.Params, _ *brine.Recorder) (PodCreated, error) {
				name, _ := p.GetString(0)
				path, ok := p.GetString(1)
				if !ok {
					return PodCreated{}, fmt.Errorf("expected an output name and a path")
				}
				kind := draftContainerType(in.ContainerType)
				return runDraft(in, kind, runtime.ContainerSpec{
					TeamID:    in.TeamID,
					Dir:       in.Dir,
					ImageSpec: runtime.ImageSpec{ImageURL: in.ImageURL},
					Outputs:   runtime.OutputPaths{name: path},
					Type:      kind,
				}, false)
			},
		),

		// Where a step volume actually lands, against the key the daemon will
		// be asked for.
		//
		// It keeps its own body because the two parameters are a lookup key
		// and a key the expectation is DERIVED from, and because its four
		// failures are four different diagnoses: nothing is mounted there at
		// all, the pod carries no store to serve anything from, the directory
		// is ephemeral, or it is on the node under the wrong name.
		//
		// It sits beside "is the node directory recorded for the output
		// {string}" rather than replacing it. That one asks a narrower
		// question — when an input and an output share a volume, whose name
		// wins — and answers it without reference to where the store is
		// rooted. This one pins the WHOLE path, and takes the daemon key
		// rather than an output name so it can also speak about a directory
		// that is nobody's named output: a get step's working directory,
		// which RecordOutputs files under "dir".
		//
		// The root is read off the pod's own artifact-store volume rather
		// than written into the sentence. That keeps the expectation derived
		// from what the cluster was actually asked for, and it means a pod
		// with no store at all — a check's — fails here instead of quietly
		// agreeing.
		brine.DefineCheck[PodCreated](
			"the volume mounted at {string} is the node directory the daemon serves as {string}",
			func(in PodCreated, p brine.Params, _ *brine.Recorder) error {
				mountPath, _ := p.GetString(0)
				key, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected a mount path and a daemon key")
				}
				keyHandle, subdir, split := strings.Cut(key, "/")
				if !split || keyHandle == "" || subdir == "" {
					return fmt.Errorf(
						"a daemon key is \"<handle>/<name>\"; %q is not one", key)
				}
				if keyHandle != in.Handle {
					return fmt.Errorf(
						"this sentence names the daemon key %q, but the step under description is "+
							"%q — the key of another step would be checked against this step's pod",
						key, in.Handle)
				}
				root, err := storeRootOf(in.Pod, in.Handle)
				if err != nil {
					return err
				}
				want := filepath.Join(root, "steps", key)

				v, err := volumeAt(in.Pod, mountPath)
				if err != nil {
					return fmt.Errorf(
						"%w, so the step writes %q into the container's own image filesystem. No "+
							"daemon can read that and it is gone when the pod ends, yet the runtime "+
							"still registers the key %q and points it at %q — a directory the daemon "+
							"creates empty on demand. The step exits 0 and every later step that "+
							"resolves that key is handed nothing",
						err, mountPath, key, want)
				}
				if v.HostPath == nil {
					return fmt.Errorf(
						"expected what the step writes at %q to live on the node at %q, where the "+
							"daemon serves the key %q from; it is ephemeral and dies with the pod, so "+
							"the key resolves to an empty directory for every step downstream",
						mountPath, want, key)
				}
				if got := filepath.Clean(v.HostPath.Path); got != want {
					return fmt.Errorf(
						"the step writes %q into %q on the node, but its data is registered under the "+
							"key %q, which the daemon serves from %q. The two halves name different "+
							"directories, so nothing fails here: this step writes its files and "+
							"succeeds, and the next step resolves the key, is handed a directory "+
							"nothing ever wrote to, and reads an empty input from a producer that "+
							"plainly ran",
						mountPath, got, key, want)
				}
				return nil
			},
		),

		// Which of the step's inputs the pod actually fetches.
		//
		// All three read the same list — the destination paths the
		// fetch-inputs init container mounts, which is exactly the set of
		// inputs it was built to write into — so the getter reports the
		// absence of that container rather than returning an empty list.
		// A sentence about which inputs are fetched presumes some are; left
		// as "none", the negative check below would pass on a pod that
		// fetches nothing at all, which is the failure it exists to catch.
		CheckCount[PodCreated]("the pod fetches exactly {int} of the step's inputs",
			"inputs fetched before the step starts", fetchedInputPaths),

		CheckMember[PodCreated]("the pod fetches the input at {string}",
			"the inputs fetched before the step starts", fetchedInputPaths),

		CheckNotMember[PodCreated]("the pod does not fetch the input at {string}",
			"the inputs fetched before the step starts", fetchedInputPaths),
	}
}

// The names production gives the two pod fixtures these checks read back.
// Both are unexported in the jetbridge package; this file already reads
// "cleanup-stale" the same way, and the container-spec family reads "main".
const (
	podArtifactStoreVolumeName  = "artifact-daemon-hostpath"
	podFetchInputsContainerName = "fetch-inputs"
)

// storeRootOf returns the node directory the pod's artifact store is mounted
// from — the root every step's data is served out of.
//
// Reading it off the pod means the expected layout is derived from what the
// cluster was asked for rather than from a constant that would have to be kept
// in step with the fixture's configuration; and a pod that carries no store
// cannot silently satisfy a sentence about where the daemon serves something.
func storeRootOf(pod *corev1.Pod, handle string) (string, error) {
	if pod == nil {
		return "", fmt.Errorf("no pod was created")
	}
	for _, v := range pod.Spec.Volumes {
		if v.Name != podArtifactStoreVolumeName {
			continue
		}
		if v.HostPath == nil {
			return "", fmt.Errorf(
				"the pod for %q carries an artifact store volume that is not node storage, so "+
					"nothing it writes outlives the pod for a later step to read", handle)
		}
		return filepath.Clean(v.HostPath.Path), nil
	}
	return "", fmt.Errorf(
		"the pod for %q carries no artifact store volume, so no directory in it is served to "+
			"any later step — this sentence is about where the daemon serves a directory from, "+
			"and this worker keeps nothing on the node", handle)
}

// fetchedInputPaths is where in the step's workspace the fetch init container
// writes: one mount per input it was given an artifact for, plus the
// read-only store mount, which is the container's window onto the node and
// not one of the step's inputs.
func fetchedInputPaths(in PodCreated) ([]string, error) {
	if in.Pod == nil {
		return nil, fmt.Errorf("no pod was created")
	}
	var names []string
	for _, c := range in.Pod.Spec.InitContainers {
		names = append(names, c.Name)
		if c.Name != podFetchInputsContainerName {
			continue
		}
		var paths []string
		for _, m := range c.VolumeMounts {
			if m.Name == podArtifactStoreVolumeName {
				continue
			}
			paths = append(paths, m.MountPath)
		}
		return paths, nil
	}
	return nil, fmt.Errorf(
		"the pod for %q has no %q init container, so nothing is fetched into its workspace at "+
			"all: every input the step was promised arrives as an empty directory and the "+
			"command fails on a file the pipeline plainly handed it, with nothing in the build "+
			"log to say why (init containers: %v)",
		in.Handle, podFetchInputsContainerName, names)
}

// runDraft builds the pod through the worker the way the ATC does, then reads
// back what the cluster was actually asked for.
//
// ranBefore makes the container row exist first, so the run under test finds
// it already created and is REUSED — which is what a retried step, and every
// check after a resource's first, actually is.
func runDraft(in ContainerDraft, kind db.ContainerType, spec runtime.ContainerSpec, ranBefore bool) (PodCreated, error) {
	owner := db.NewFixedHandleContainerOwner(in.Handle)
	metadata := db.ContainerMetadata{Type: kind, JobID: in.JobID, StepName: in.StepName}

	if ranBefore {
		if _, _, err := in.Worker.FindOrCreateContainer(
			in.Ctx, owner, metadata, spec, &noopDelegate{},
		); err != nil {
			return PodCreated{}, fmt.Errorf("first run of %q: %w", in.Handle, err)
		}
	}

	container, _, err := in.Worker.FindOrCreateContainer(in.Ctx, owner, metadata, spec, &noopDelegate{})
	if err != nil {
		return PodCreated{}, fmt.Errorf("find or create container %q: %w", in.Handle, err)
	}
	if _, err := container.Run(in.Ctx,
		runtime.ProcessSpec{Path: "/bin/sh"}, runtime.ProcessIO{},
	); err != nil {
		return PodCreated{}, fmt.Errorf("run container %q: %w", in.Handle, err)
	}

	pods, err := in.Clientset.CoreV1().Pods(in.Namespace).List(in.Ctx, metav1.ListOptions{})
	if err != nil {
		return PodCreated{}, fmt.Errorf("list pods: %w", err)
	}
	if len(pods.Items) != 1 {
		return PodCreated{}, fmt.Errorf("expected exactly 1 pod, found %d", len(pods.Items))
	}
	pod := pods.Items[0]

	return PodCreated{
		Namespace: in.Namespace,
		Ctx:       in.Ctx,
		Handle:    in.Handle,
		Pod:       &pod,
	}, nil
}

func hasInitContainer(in PodCreated, name string) bool {
	if in.Pod == nil {
		return false
	}
	for _, c := range in.Pod.Spec.InitContainers {
		if c.Name == name {
			return true
		}
	}
	return false
}
