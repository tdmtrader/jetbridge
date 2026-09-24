package steps

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/vars"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// IntegrationDefinitions migrates the jetbridge suite's INTEGRATION files —
// the ones that drive a real worker against a real PostgreSQL database and a
// real Kubernetes API. API/row cases use a real SPDY executor without waiting
// on a process. Artifact-chain cases use real tasks, daemon storage and Git:
//
//	behavioral_worker_test.go     15 cases  (RC / CO / LR families)
//	podname_integration_test.go    9 cases  (PN-07 and the pod-name seam)
//	artifact_integration_test.go   8 cases  (artifact passing between steps)
//	resource_test.go               now in live/git-resource.feature
//	secret_env_test.go             2 cases  (SecretEnv -> SecretKeyRef)
//	node_ip_resolver_test.go       4 cases  (node name -> internal IP)
//	executor_test.go               3 cases  (see executorDisposition)
//
// Every case is either a scenario in ../features/step-integration.feature or
// carries a disposition comment in this file. Dispositions are grouped at the
// bottom under "Dispositions".
//
// The two artifact-chain cases execute in the live tier. API-only pod
// construction does not claim kubelet execution. Volume assertions read
// observable behavior rather than requiring a particular Go implementation.

// ---------------------------------------------------------------------------
// Domain states
// ---------------------------------------------------------------------------

// IntegrationCluster refines the shared real-API WorkerReady, backed
// by a real PostgreSQL database, plus a team to own the rows. Every Given in
// step-integration.feature refines this state.
//
// Its PodStartupTimeout is deliberately seconds, not the five-minute default.
// A scenario that waits on a Kubernetes deadline HANGS rather than failing,
// and a hang is worse than an absent test.
type IntegrationCluster struct {
	WorkerReady
	Team db.Team

	// Artifacts holds artifact volumes a scenario created and named, so a
	// later step can feed one to a container as an input.
	Artifacts map[string]NamedArtifact
}

// NamedArtifact is one artifact volume a scenario created under a name.
type NamedArtifact struct {
	Name     string
	Handle   string
	Key      string
	TeamID   int
	Volume   runtime.Volume
	Artifact db.WorkerArtifact
}

// StepDraft is a step under description: a handle, the metadata the ATC
// recorded it with, and the spec it will be created from. Refinements take
// StepDraft in and out, so they compose in any order before the container is
// created.
type StepDraft struct {
	Cluster  IntegrationCluster
	Handle   string
	Metadata db.ContainerMetadata
	Spec     runtime.ContainerSpec
}

// StepCreated is the state after FindOrCreateContainer: the container the ATC
// holds, and the volume mounts it was handed for the step's working set.
type StepCreated struct {
	Cluster   IntegrationCluster
	Handle    string
	Metadata  db.ContainerMetadata
	Spec      runtime.ContainerSpec
	Container runtime.Container
	Mounts    []runtime.VolumeMount
}

// StepRan is the state after the step's container ran — and, where the
// scenario waited on it, after the process reported. It carries the pod that
// was created so checks can read the spec Kubernetes was actually asked for.
type StepRan struct {
	Created     StepCreated
	Pod         *corev1.Pod
	PodCount    int
	Stdout      string
	ExitStatus  int
	Err         error
	Message     string
	publication *integrationPublication

	// BoundBefore and BoundAfter record which pod each mount's volume reads
	// from, before and after Run. A volume that is never bound reads from
	// nowhere, which is the failure the "volume binding uses podName" case
	// exists to catch.
	BoundBefore map[string]string
	BoundAfter  map[string]string
}

// AttachOutcome is what `fly intercept` / a restarted web sees when it attaches
// to a step whose container it already holds.
type AttachOutcome struct {
	Created     StepCreated
	ExpectedPod string
	ExitStatus  int
	Err         error
	Message     string
}

// IntegrationVolume is the outcome of a volume operation: a lookup, an
// artifact creation, or a resource-cache initialisation. An error is a value
// here so a scenario can assert failure without dying.
type IntegrationVolume struct {
	Cluster IntegrationCluster
	Handle  string
	Volume  runtime.Volume
	Found   bool
	Err     error
	Message string

	// Keys accumulates the artifact key observed on each lookup, so the
	// "does the key survive a restart" claim is about several observations
	// rather than one.
	Keys []string

	// PodsAfter is how many pods the cluster held once the operation was
	// done. A lookup that scheduled anything is a bug.
	PodsAfter int

	Artifact    db.WorkerArtifact
	CacheResult *db.UsedWorkerResourceCache
	CacheID     int
}

// NodeCluster and NodeIPOutcome are the node-IP states. There is no database,
// no worker and no pod in this family — the resolver's only collaborator is
// the Kubernetes Nodes API.
type NodeCluster struct {
	Ctx       context.Context
	Clientset kubernetes.Interface
	Resolver  *jetbridge.NodeIPResolver
	Node      *corev1.Node
	trace     *execObservation
}

type NodeIPOutcome struct {
	IPs      []string
	Expected string
	NodePath string
	Err      error
	Message  string
	IsIPArg  bool
	trace    *execObservation
}

// ---------------------------------------------------------------------------
// Step definitions
// ---------------------------------------------------------------------------

// IntegrationDefinitions is the single entry point this file exports.
func IntegrationDefinitions() []brine.StepDefinition {
	defs := liveArtifactIntegrationDefinitions()
	defs = append(defs, integrationClusterDefinitions()...)
	defs = append(defs, integrationStepDefinitions()...)
	defs = append(defs, integrationRunDefinitions()...)
	defs = append(defs, integrationPodCheckDefinitions()...)
	defs = append(defs, integrationVolumeDefinitions()...)
	defs = append(defs, integrationNodeIPDefinitions()...)
	defs = append(defs, liveNodeResolverDefinitions()...)
	return defs
}

func integrationClusterDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		Transform[WorkerReady, IntegrationCluster](
			"the worker tracks integration containers and artifacts",
			func(in WorkerReady, _ Args) (IntegrationCluster, error) {
				team, found, err := in.DB.TeamFactory.FindTeam("main")
				if err != nil {
					return IntegrationCluster{}, fmt.Errorf("find integration team: %w", err)
				}
				if !found || team.ID() != in.TeamID {
					return IntegrationCluster{}, fmt.Errorf("integration worker has no matching team")
				}
				if in.ProducerExecutor == nil {
					return IntegrationCluster{}, fmt.Errorf("integration worker has no real SPDY executor")
				}
				// Use the production exec-mode path even for API-only pod
				// construction. These cases do not invoke Process.Wait.
				in.Executor = in.ProducerExecutor
				in.Config.PodStartupTimeout = 5 * time.Second
				in.Config.PodSchedulingTimeout = 5 * time.Second
				return IntegrationCluster{WorkerReady: in.rebuild(), Team: team, Artifacts: map[string]NamedArtifact{}}, nil
			},
		),

		// A locator entry is the state a previous step's output left behind.
		// LookupVolume is supposed to be indifferent to it — see the
		// "whatever the locator remembers" scenario.
		Refine[IntegrationCluster]("the worker remembers the artifact {string} on node {string}",
			func(in IntegrationCluster, a Args) IntegrationCluster {
				locator := jetbridge.NewArtifactLocator()
				locator.Record(jetbridge.ArtifactKey(a.String(0)), a.String(1), "container/output")
				in.Locator = locator
				in.WorkerReady = in.WorkerReady.rebuild()
				return in
			}),

		Transform[IntegrationCluster, IntegrationCluster](
			"an artifact volume {string} persisted for this team",
			func(in IntegrationCluster, a Args) (IntegrationCluster, error) {
				return persistArtifactVolume(in, a.String(0), in.Team.ID())
			},
		),

		Transform[IntegrationCluster, IntegrationCluster](
			"an artifact volume {string} persisted for a second team",
			func(in IntegrationCluster, a Args) (IntegrationCluster, error) {
				other, err := in.DB.TeamFactory.CreateTeam(atc.Team{Name: "artifact-team-2"})
				if err != nil {
					return IntegrationCluster{}, fmt.Errorf("create second team: %w", err)
				}
				return persistArtifactVolume(in, a.String(0), other.ID())
			},
		),

		// A volume row written straight through the repository, the way a
		// previous step's output already sits in the database when the next
		// step looks it up.
		Transform[IntegrationCluster, IntegrationCluster](
			"a volume {string} recorded against this worker",
			func(in IntegrationCluster, a Args) (IntegrationCluster, error) {
				handle := a.String(0)

				creating, err := in.DB.VolumeRepository.CreateVolumeWithHandle(
					handle, in.Team.ID(), in.DBWorker.Name(), db.VolumeTypeArtifact)
				if err != nil {
					return IntegrationCluster{}, fmt.Errorf("create volume %q: %w", handle, err)
				}
				if _, err := creating.Created(); err != nil {
					return IntegrationCluster{}, fmt.Errorf("transition volume %q: %w", handle, err)
				}
				return in, nil
			},
		),
	}
}

func integrationStepDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		Transform[IntegrationCluster, StepDraft](
			"a {string} step in pipeline {string} job {string} build {string} named {string} with handle {string}",
			func(in IntegrationCluster, a Args) (StepDraft, error) {
				containerType := db.ContainerType(a.String(0))
				return StepDraft{
					Cluster: in,
					Handle:  a.String(5),
					Metadata: db.ContainerMetadata{
						Type:         containerType,
						PipelineName: a.String(1),
						JobName:      a.String(2),
						BuildName:    a.String(3),
						StepName:     a.String(4),
					},
					Spec: runtime.ContainerSpec{
						TeamID:    in.Team.ID(),
						TeamName:  in.Team.Name(),
						Dir:       "/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
						Type:      containerType,
					},
				}, nil
			},
		),

		// The sparse case: `fly execute` has no pipeline and no job, so the
		// pod has nothing to be named after but the handle.
		Transform[IntegrationCluster, StepDraft](
			"a task step with handle {string} and no pipeline or job",
			func(in IntegrationCluster, a Args) (StepDraft, error) {
				return StepDraft{
					Cluster:  in,
					Handle:   a.String(0),
					Metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
					Spec: runtime.ContainerSpec{
						TeamID:    in.Team.ID(),
						TeamName:  in.Team.Name(),
						Dir:       "/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
						Type:      db.ContainerTypeTask,
					},
				}, nil
			},
		),

		// Draft refinements. In and Out are the same type, so any number may
		// appear in any order before the container is created.
		Refine[StepDraft]("the step works in {string}",
			func(in StepDraft, a Args) StepDraft {
				in.Spec.Dir = a.String(0)
				return in
			}),

		// Some input, freshly created for the occasion, as opposed to the
		// sibling sentence below that names an artifact the scenario made
		// earlier. This one says only "the step has an input here"; where it
		// came from is not what the scenario is about. It still carries a real
		// artifact volume, because that is the only kind of input a pipeline
		// can produce — both producers of runtime.Input skip a name the
		// artifact repository has nothing for.
		brine.DefineMap[StepDraft, StepDraft](
			"the step takes an input at {string}",
			func(in StepDraft, p brine.Params, _ *brine.Recorder) (StepDraft, error) {
				path, ok := p.GetString(0)
				if !ok {
					return StepDraft{}, fmt.Errorf("expected a path")
				}
				vol, _, err := in.Cluster.Worker.CreateVolumeForArtifact(
					in.Cluster.Ctx, in.Cluster.Team.ID())
				if err != nil {
					return StepDraft{}, fmt.Errorf("create artifact for input %q: %w", path, err)
				}
				in.Spec.Inputs = append(in.Spec.Inputs, runtime.Input{
					Artifact:        vol,
					DestinationPath: path,
				})
				return in, nil
			},
		),

		Refine[StepDraft]("the step produces an output {string} at {string}",
			func(in StepDraft, a Args) StepDraft {
				if in.Spec.Outputs == nil {
					in.Spec.Outputs = runtime.OutputPaths{}
				}
				in.Spec.Outputs[a.String(0)] = a.String(1)
				return in
			}),

		Refine[StepDraft]("the step caches {string}",
			func(in StepDraft, a Args) StepDraft {
				in.Spec.Caches = append(in.Spec.Caches, a.String(0))
				return in
			}),

		Refine[StepDraft]("the step sets the environment {string}",
			func(in StepDraft, a Args) StepDraft {
				in.Spec.Env = append(in.Spec.Env, a.String(0))
				return in
			}),

		Refine[StepDraft]("the step reads {string} from the secret {string} key {string} in namespace {string}",
			func(in StepDraft, a Args) StepDraft {
				if in.Spec.SecretEnv == nil {
					in.Spec.SecretEnv = map[string]vars.SecretRef{}
				}
				in.Spec.SecretEnv[a.String(0)] = vars.SecretRef{
					Namespace: a.String(3), Name: a.String(1), Key: a.String(2),
				}
				return in
			}),

		// StepDraft -> StepCreated.
		brine.DefineMap[StepDraft, StepCreated](
			"the step's container is created",
			func(in StepDraft, _ brine.Params, _ *brine.Recorder) (StepCreated, error) {
				container, mounts, err := in.Cluster.Worker.FindOrCreateContainer(
					in.Cluster.Ctx,
					db.NewFixedHandleContainerOwner(in.Handle),
					in.Metadata,
					in.Spec,
					nil,
				)
				if err != nil {
					return StepCreated{}, fmt.Errorf("find or create container %q: %w", in.Handle, err)
				}
				return StepCreated{
					Cluster:   in.Cluster,
					Handle:    in.Handle,
					Metadata:  in.Metadata,
					Spec:      in.Spec,
					Container: container,
					Mounts:    mounts,
				}, nil
			},
		),

		// Checks over what the ATC was handed before anything was scheduled.

		// A wrong count is only diagnosable from the paths themselves, which
		// is what says WHICH mount went missing — so this is a count over the
		// collection rather than over its length.
		CheckCount[StepCreated]("the step is handed {int} volume mounts",
			"volume mounts",
			func(in StepCreated) ([]string, error) {
				return mountPaths(in.Mounts), nil
			}),

		CheckMember[StepCreated]("the step is handed a mount at {string}",
			"the mounts the step was handed",
			func(in StepCreated) ([]string, error) {
				return mountPaths(in.Mounts), nil
			}),
	}
}

func integrationRunDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// This action observes the created API pod without waiting for a
		// command. It checks pod construction, not kubelet execution.
		brine.DefineMap[StepCreated, StepRan](
			"the step's pause pod is created",
			func(in StepCreated, _ brine.Params, _ *brine.Recorder) (StepRan, error) {
				return runStep(in, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{})
			},
		),

		// A missing pod must be diagnosed by its metadata-based name. Successful
		// completion recovery runs against real task pods in the live tier.
		brine.DefineMap[StepCreated, AttachOutcome](
			"the web restarts and attaches to the step",
			func(in StepCreated, _ brine.Params, _ *brine.Recorder) (AttachOutcome, error) {
				expected := jetbridge.GeneratePodName(in.Metadata, in.Handle)
				process, err := in.Container.Attach(in.Cluster.Ctx, "some-process", runtime.ProcessIO{})
				out := AttachOutcome{Created: in, ExpectedPod: expected}
				if err != nil {
					out.Err = err
					out.Message = err.Error()
					return out, nil
				}
				result, waitErr := process.Wait(in.Cluster.Ctx)
				out.ExitStatus = result.ExitStatus
				if waitErr != nil {
					out.Err = waitErr
					out.Message = waitErr.Error()
				}
				return out, nil
			},
		),

		CheckThat[AttachOutcome]("attaching fails naming the pod the step would have created",
			func(in AttachOutcome) error {
				if in.Err == nil {
					return fmt.Errorf("expected attaching to fail, it succeeded with exit status %d", in.ExitStatus)
				}
				if !strings.Contains(in.Message, in.ExpectedPod) {
					return fmt.Errorf("expected the failure to name the pod %q, got %q", in.ExpectedPod, in.Message)
				}
				return nil
			}),

		CheckThat[AttachOutcome]("the failure does not name the handle",
			func(in AttachOutcome) error {
				if in.Err == nil {
					return fmt.Errorf("expected attaching to have failed, it succeeded")
				}
				if strings.Contains(in.Message, in.Created.Handle) {
					return fmt.Errorf("expected the failure not to name the handle %q, got %q",
						in.Created.Handle, in.Message)
				}
				return nil
			}),
	}
}

func integrationPodCheckDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// The parameter is a regular expression, so the comparison is neither
		// equality nor containment and no combinator expresses it.
		Assert[StepRan](
			"the pod in the cluster is named to match {string}",
			func(in StepRan, args Args) error {
				pattern := args.String(0)

				if in.Pod == nil {
					return fmt.Errorf("no pod was created")
				}
				re, err := regexp.Compile(pattern)
				if err != nil {
					return fmt.Errorf("bad pattern %q: %w", pattern, err)
				}
				if !re.MatchString(in.Pod.Name) {
					return fmt.Errorf("expected the pod name to match %q, got %q", pattern, in.Pod.Name)
				}
				return nil
			},
		),

		CheckString[StepRan]("the pod in the cluster is named exactly {string}",
			"the pod's name",
			func(in StepRan) (string, error) {
				if in.Pod == nil {
					return "", fmt.Errorf("no pod was created")
				}
				return in.Pod.Name, nil
			}),

		CheckThat[StepRan]("the pod is not named after the handle",
			func(in StepRan) error {
				if in.Pod == nil {
					return fmt.Errorf("no pod was created")
				}
				if in.Pod.Name == in.Created.Handle {
					return fmt.Errorf("expected the pod not to be named after the handle, got %q", in.Pod.Name)
				}
				return nil
			}),

		CheckStringFor[StepRan]("the pod is labelled {string} as {string}",
			"the pod label",
			func(in StepRan, key string) (string, error) {
				if in.Pod == nil {
					return "", fmt.Errorf("no pod was created")
				}
				got, found := in.Pod.Labels[key]
				if !found {
					return "", fmt.Errorf("expected the pod to carry the label %q, it carries %v",
						key, sortedKeys(in.Pod.Labels))
				}
				return got, nil
			}),

		CheckNotMember[StepRan]("the pod carries no {string} label",
			"the pod's labels",
			func(in StepRan) ([]string, error) {
				if in.Pod == nil {
					return nil, fmt.Errorf("no pod was created")
				}
				return sortedKeys(in.Pod.Labels), nil
			}),

		CheckMember[StepRan]("the step's pod mounts {string}",
			"the pod's mounts",
			func(in StepRan) ([]string, error) {
				main, err := integrationMainContainer(in.Pod)
				if err != nil {
					return nil, err
				}
				var paths []string
				for _, vm := range main.VolumeMounts {
					paths = append(paths, vm.MountPath)
				}
				return paths, nil
			}),

		// Three parameters, and three independent claims about the variable:
		// that it is read from a secret at all, that it is that secret and
		// that key, and that it carries no literal alongside. A getter can
		// derive one value, not adjudicate three.
		Assert[StepRan](
			"the pod reads {string} from the secret {string} key {string}",
			func(in StepRan, args Args) error {
				name := args.String(0)
				secret := args.String(1)
				key := args.String(2)

				env, err := integrationEnvVar(in.Pod, name)
				if err != nil {
					return err
				}
				if env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil {
					return fmt.Errorf("expected %q to come from a secret, it carries the literal %q",
						name, env.Value)
				}
				ref := env.ValueFrom.SecretKeyRef
				if ref.Name != secret || ref.Key != key {
					return fmt.Errorf("expected %q to read secret %q key %q, got %q key %q",
						name, secret, key, ref.Name, ref.Key)
				}
				if env.Value != "" {
					return fmt.Errorf("expected %q to carry no literal value, it carries %q", name, env.Value)
				}
				return nil
			},
		),

		CheckStringFor[StepRan]("the pod sets {string} to the literal {string}",
			"the pod's literal environment value",
			func(in StepRan, name string) (string, error) {
				env, err := integrationEnvVar(in.Pod, name)
				if err != nil {
					return "", err
				}
				if env.ValueFrom != nil {
					return "", fmt.Errorf("expected %q to be a literal, it is read from elsewhere", name)
				}
				return env.Value, nil
			}),

		// The volume-binding claim, stated as the two observations that make
		// it meaningful: nothing before the pod existed, the step's own pod
		// afterwards.
		//
		// Both keep their own bodies. Their parameter is the mount path — the
		// KEY a value is looked up by — while the expectation is not in the
		// sentence at all: it is fixed here, and derived from the state in the
		// check below. CheckString would compare the parameter itself, and
		// CheckStringFor wants the expectation as a second parameter.
		Assert[StepRan](
			"the mount at {string} read from no pod before the step ran",
			func(in StepRan, args Args) error {
				path := args.String(0)

				bound, found := in.BoundBefore[path]
				if !found {
					return fmt.Errorf("no mount at %q (have %v)", path, sortedKeys(in.BoundBefore))
				}
				if bound != "" {
					return fmt.Errorf("expected the mount at %q to read from no pod before the step ran, it read from %q",
						path, bound)
				}
				return nil
			},
		),

		Assert[StepRan](
			"the mount at {string} reads from the pod the step created",
			func(in StepRan, args Args) error {
				path := args.String(0)

				if in.Pod == nil {
					return fmt.Errorf("no pod was created")
				}
				bound, found := in.BoundAfter[path]
				if !found {
					return fmt.Errorf("no mount at %q (have %v)", path, sortedKeys(in.BoundAfter))
				}
				if bound != in.Pod.Name {
					return fmt.Errorf("expected the mount at %q to read from the pod %q, it reads from %q",
						path, in.Pod.Name, bound)
				}
				return nil
			},
		),

		CheckContains[StepRan]("the step's output is {string}",
			"the step's output",
			func(in StepRan) (string, error) {
				if in.Err != nil {
					return "", fmt.Errorf("the step failed: %v", in.Err)
				}
				return in.Stdout, nil
			}),

		// Created state, container type and worker identity are independent
		// claims. Share the row comparison with the real Git resource cases.
		Assert[StepRan](
			"the step's container row is a created {string} container on worker {string}",
			func(in StepRan, args Args) error {
				wantType := args.String(0)
				wantWorker := args.String(1)

				return checkContainerRow(in.Created.Cluster, in.Created.Handle, wantType, wantWorker)
			},
		),

		// An unexpected exit is only diagnosable alongside the error and the
		// output the step produced, so both ride along on the failure. The
		// error is NOT a precondition here — a step that reports a status and
		// an error at once is exactly what "a script that fails hands its exit
		// code back" is about — so it stays detail rather than a getter error.
		CheckInt[StepRan]("the step reports exit status {int}",
			"the step's exit status",
			func(in StepRan) (int, error) { return in.ExitStatus, nil },
			func(in StepRan) string {
				return fmt.Sprintf("err: %v, output: %q", in.Err, in.Stdout)
			}),
	}
}

func integrationVolumeDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		Transform[IntegrationCluster, IntegrationVolume](
			"the volume {string} is looked up twice",
			func(in IntegrationCluster, a Args) (IntegrationVolume, error) {
				handle := a.String(0)

				out := IntegrationVolume{Cluster: in, Handle: handle}
				for i := 0; i < 2; i++ {
					vol, found, err := in.Worker.LookupVolume(in.Ctx, handle)
					if err != nil {
						out.Err, out.Message = err, err.Error()
						return out, nil
					}
					out.Found = found
					if !found {
						return out, nil
					}
					out.Volume = vol
					if keyed, ok := vol.(interface{ Key() string }); ok {
						out.Keys = append(out.Keys, keyed.Key())
					}
				}
				pods, err := in.Clientset.CoreV1().Pods(in.Namespace).List(in.Ctx, metav1.ListOptions{})
				if err != nil {
					return IntegrationVolume{}, fmt.Errorf("list pods: %w", err)
				}
				out.PodsAfter = len(pods.Items)
				return out, nil
			},
		),

		// The ATC process is replaced. Nothing is carried over in memory: a
		// new worker, a new volume repository, the same database.
		Transform[IntegrationCluster, IntegrationVolume](
			"a restarted ATC looks the volume {string} up",
			func(in IntegrationCluster, a Args) (IntegrationVolume, error) {
				handle := a.String(0)

				restarted := jetbridge.NewWorker(in.DBWorker, in.Clientset, in.Config, jetbridge.WorkerDeps{
					VolumeRepo: db.NewVolumeRepository(in.DB.Conn),
				})
				vol, found, err := restarted.LookupVolume(in.Ctx, handle)
				out := IntegrationVolume{Cluster: in, Handle: handle, Found: found, Volume: vol}
				if err != nil {
					out.Err, out.Message = err, err.Error()
				}
				return out, nil
			},
		),

		// The reaper's half of the artifact lifecycle: the row goes, and the
		// handle stops resolving.
		Transform[IntegrationCluster, IntegrationVolume](
			"the reaper destroys the artifact volume {string}",
			func(in IntegrationCluster, a Args) (IntegrationVolume, error) {
				name := a.String(0)

				named, found := in.Artifacts[name]
				if !found {
					return IntegrationVolume{}, fmt.Errorf("no artifact volume named %q", name)
				}
				holder, ok2 := named.Volume.(interface{ DBVolume() db.CreatedVolume })
				if !ok2 {
					return IntegrationVolume{}, fmt.Errorf("artifact volume %q carries no database row", name)
				}
				destroying, err := holder.DBVolume().Destroying()
				if err != nil {
					return IntegrationVolume{}, fmt.Errorf("mark %q destroying: %w", named.Handle, err)
				}
				destroyed, err := destroying.Destroy()
				if err != nil {
					return IntegrationVolume{}, fmt.Errorf("destroy %q: %w", named.Handle, err)
				}
				if !destroyed {
					return IntegrationVolume{}, fmt.Errorf("the volume row for %q was not destroyed", named.Handle)
				}

				vol, lookedUp, lookupErr := in.Worker.LookupVolume(in.Ctx, named.Handle)
				out := IntegrationVolume{Cluster: in, Handle: named.Handle, Found: lookedUp, Volume: vol}
				if lookupErr != nil {
					out.Err, out.Message = lookupErr, lookupErr.Error()
				}
				return out, nil
			},
		),

		// RC-03's database half: a get step that hits the cache still has to
		// record the association, or the next build cannot find it.
		Transform[IntegrationCluster, IntegrationVolume](
			"the volume {string} is initialised as the resource cache for type {string} version {string}",
			func(in IntegrationCluster, a Args) (IntegrationVolume, error) {
				handle := a.String(0)
				resourceType := a.String(1)

				// The worker has to offer the type before a cache for it can
				// exist. This is the same row the registrar writes.
				if _, err := in.DB.WorkerFactory.SaveWorker(atc.Worker{
					Name: in.DBWorker.Name(), Platform: "linux", Version: "1.2.3",
					State: string(db.WorkerStateRunning),
					ResourceTypes: []atc.WorkerResourceType{{
						Type: resourceType, Image: "some-image", Version: "some-version",
					}},
				}, 0); err != nil {
					return IntegrationVolume{}, fmt.Errorf("save worker with resource types: %w", err)
				}

				build, err := in.Team.CreateOneOffBuild()
				if err != nil {
					return IntegrationVolume{}, fmt.Errorf("create one-off build: %w", err)
				}
				cacheFactory := db.NewResourceCacheFactory(in.DB.Conn, in.DB.LockFactory)
				cache, err := cacheFactory.FindOrCreateResourceCache(
					db.ForBuild(build.ID()),
					resourceType,
					atc.Version{"version": a.String(2)},
					atc.Source{"uri": "example.invalid"},
					nil,
					nil,
				)
				if err != nil {
					return IntegrationVolume{}, fmt.Errorf("find or create resource cache: %w", err)
				}

				vol, found, err := in.Worker.LookupVolume(in.Ctx, handle)
				if err != nil {
					return IntegrationVolume{}, fmt.Errorf("look up %q: %w", handle, err)
				}
				if !found {
					return IntegrationVolume{}, fmt.Errorf("volume %q is not in the database", handle)
				}

				result, err := vol.InitializeResourceCache(in.Ctx, cache)
				out := IntegrationVolume{
					Cluster: in, Handle: handle, Found: true, Volume: vol,
					CacheResult: result, CacheID: cache.ID(),
				}
				if err != nil {
					out.Err, out.Message = err, err.Error()
				}
				return out, nil
			},
		),

		// Checks over IntegrationVolume.
		CheckThat[IntegrationVolume]("the lookup finds it",
			func(in IntegrationVolume) error {
				if in.Err != nil {
					return fmt.Errorf("the lookup failed: %v", in.Err)
				}
				if !in.Found {
					return fmt.Errorf("expected the volume %q to be found, it was not", in.Handle)
				}
				if in.Volume == nil {
					return fmt.Errorf("the volume %q was reported found but nothing came back", in.Handle)
				}
				return nil
			}),

		CheckThat[IntegrationVolume]("the lookup finds nothing",
			func(in IntegrationVolume) error {
				if in.Err != nil {
					return fmt.Errorf("expected a clean miss, the lookup failed: %v", in.Err)
				}
				if in.Found {
					return fmt.Errorf("expected the volume %q not to be found, it was", in.Handle)
				}
				return nil
			}),

		CheckString[IntegrationVolume]("the volume that came back is handle {string}",
			"the volume's handle",
			func(in IntegrationVolume) (string, error) {
				if in.Volume == nil {
					return "", fmt.Errorf("no volume came back")
				}
				return in.Volume.Handle(), nil
			}),

		// Source() is what a downstream step uses to decide where to stream
		// from. It has to be the worker that persisted the volume.
		CheckString[IntegrationVolume]("it names {string} as the worker holding it",
			"the worker the volume names as holding it",
			func(in IntegrationVolume) (string, error) {
				if in.Volume == nil {
					return "", fmt.Errorf("no volume came back")
				}
				return in.Volume.Source(), nil
			}),

		// Two parameters and two independent comparisons against the same row.
		// Splitting them across a getter would make one of them a precondition
		// of the other, which is not what the sentence says.
		Assert[IntegrationVolume](
			"it carries the database row for handle {string} on worker {string}",
			func(in IntegrationVolume, args Args) error {
				wantHandle := args.String(0)
				wantWorker := args.String(1)

				holder, ok2 := in.Volume.(interface{ DBVolume() db.CreatedVolume })
				if !ok2 || holder.DBVolume() == nil {
					return fmt.Errorf("the volume carries no database row")
				}
				row := holder.DBVolume()
				if row.Handle() != wantHandle {
					return fmt.Errorf("expected the row for %q, got %q", wantHandle, row.Handle())
				}
				if row.WorkerName() != wantWorker {
					return fmt.Errorf("expected the row on worker %q, got %q", wantWorker, row.WorkerName())
				}
				return nil
			},
		),

		CheckThat[IntegrationVolume]("looking it up scheduled nothing",
			func(in IntegrationVolume) error {
				if in.PodsAfter != 0 {
					return fmt.Errorf("expected the lookup to schedule nothing, the cluster holds %d pods",
						in.PodsAfter)
				}
				return nil
			}),

		CheckThat[IntegrationVolume]("both lookups named the same artifact key",
			func(in IntegrationVolume) error {
				if len(in.Keys) < 2 {
					return fmt.Errorf("expected two artifact keys, observed %d (%v)", len(in.Keys), in.Keys)
				}
				for _, k := range in.Keys[1:] {
					if k != in.Keys[0] {
						return fmt.Errorf("expected a stable artifact key, observed %v", in.Keys)
					}
				}
				return nil
			}),

		CheckThat[IntegrationVolume]("the artifact key is the volume's handle",
			func(in IntegrationVolume) error {
				if len(in.Keys) == 0 {
					return fmt.Errorf("no artifact key was observed")
				}
				if in.Keys[0] != in.Handle {
					return fmt.Errorf("expected the artifact key to be the handle %q, got %q",
						in.Handle, in.Keys[0])
				}
				return nil
			}),

		CheckThat[IntegrationVolume]("the volume row points at the worker resource cache the caller was handed",
			func(in IntegrationVolume) error {
				if in.Err != nil {
					return fmt.Errorf("initialising the resource cache failed: %v", in.Err)
				}
				if in.CacheResult == nil || in.CacheResult.ID == 0 {
					return fmt.Errorf("no worker resource cache came back")
				}
				var workerResourceCacheID, resourceCacheID int
				err := in.Cluster.DB.Conn.QueryRow(`
					SELECT v.worker_resource_cache_id, wrc.resource_cache_id
					FROM volumes v
					JOIN worker_resource_caches wrc ON wrc.id = v.worker_resource_cache_id
					WHERE v.handle = $1
				`, in.Handle).Scan(&workerResourceCacheID, &resourceCacheID)
				if err != nil {
					return fmt.Errorf("read the volume's resource cache row: %w", err)
				}
				if workerResourceCacheID != in.CacheResult.ID {
					return fmt.Errorf("expected the volume row to point at worker resource cache %d, it points at %d",
						in.CacheResult.ID, workerResourceCacheID)
				}
				if resourceCacheID != in.CacheID {
					return fmt.Errorf("expected that cache to be resource cache %d, it is %d",
						in.CacheID, resourceCacheID)
				}
				return nil
			}),

		// Team isolation. An artifact reachable from another team's id is a
		// cross-team data leak, not a convenience.
		Transform[IntegrationCluster, IntegrationVolume](
			"the artifact {string} is asked for by its own team and by the other team",
			func(in IntegrationCluster, a Args) (IntegrationVolume, error) {
				name := a.String(0)

				named, found := in.Artifacts[name]
				if !found {
					return IntegrationVolume{}, fmt.Errorf("no artifact volume named %q", name)
				}
				out := IntegrationVolume{Cluster: in, Handle: named.Handle, Artifact: named.Artifact}

				ownVolume, ownFound, err := named.Artifact.Volume(named.TeamID)
				if err != nil {
					return IntegrationVolume{}, fmt.Errorf("read the artifact's own volume: %w", err)
				}
				if !ownFound {
					return IntegrationVolume{}, fmt.Errorf("the artifact's own team cannot see its volume")
				}
				out.Found = true
				out.Keys = []string{ownVolume.Handle()}

				for _, otherID := range otherTeamIDs(in, named.TeamID) {
					_, otherFound, err := named.Artifact.Volume(otherID)
					if err != nil {
						return IntegrationVolume{}, fmt.Errorf("read the artifact as team %d: %w", otherID, err)
					}
					if otherFound {
						out.Err = fmt.Errorf("team %d can see team %d's artifact", otherID, named.TeamID)
						out.Message = out.Err.Error()
					}
				}
				return out, nil
			},
		),

		CheckThat[IntegrationVolume]("only its own team can reach it",
			func(in IntegrationVolume) error {
				if in.Err != nil {
					return in.Err
				}
				if !in.Found {
					return fmt.Errorf("the artifact's own team could not reach it")
				}
				if len(in.Keys) == 0 || in.Keys[0] != in.Handle {
					return fmt.Errorf("expected the artifact's own team to reach the volume %q, got %v",
						in.Handle, in.Keys)
				}
				return nil
			}),
	}
}

func integrationNodeIPDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, NodeCluster](
			"a cluster containing node {string} with no reported addresses",
			[]string{"real-cluster"},
			func(_ brine.Empty, p brine.Params, rec *brine.Recorder, res brine.Resources) (NodeCluster, error) {
				name, ok := p.GetString(0)
				if !ok {
					return NodeCluster{}, fmt.Errorf("expected node name")
				}
				in, err := emptyNodeCluster(res)
				if err != nil {
					return in, err
				}
				ctx, cancel := context.WithTimeout(in.Ctx, 10*time.Second)
				defer cancel()
				in.Node, err = createRealNode(ctx, in.Clientset, rec, name)
				if err != nil {
					return in, err
				}
				if len(in.Node.Status.Addresses) != 0 {
					return in, fmt.Errorf("new node unexpectedly reports addresses")
				}
				return in, nil
			},
		),

		TransformUsing[brine.Empty, NodeCluster]("a cluster with no nodes",
			[]string{"real-cluster"},
			func(_ brine.Empty, _ Args, res brine.Resources) (NodeCluster, error) {
				return emptyNodeCluster(res)
			}),

		Transform[NodeCluster, NodeIPOutcome]("a caller resolves {string} twice",
			func(in NodeCluster, a Args) (NodeIPOutcome, error) {
				out := NodeIPOutcome{}
				for i := 0; i < 2; i++ {
					if !out.resolve(in, a.String(0)) {
						break
					}
				}
				return out, nil
			}),

		CheckThat[NodeIPOutcome]("resolving fails",
			func(in NodeIPOutcome) error {
				if in.Err == nil {
					return fmt.Errorf("expected resolving to fail, it returned %v", in.IPs)
				}
				return nil
			}),

		// Fixture setup uses the original client; this trace observes only
		// the resolver's real requests, including an unnecessary lookup that
		// still returns the correct typed sentinel afterwards.
		CheckThat[NodeIPOutcome]("the node-name argument is refused as an IP address",
			func(in NodeIPOutcome) error {
				if in.trace == nil {
					return fmt.Errorf("node refusal has no API observation")
				}
				in.trace.mu.Lock()
				defer in.trace.mu.Unlock()
				if len(in.trace.requests) != 0 {
					return fmt.Errorf("IP-shaped argument must make zero API requests; observed %d", len(in.trace.requests))
				}
				if in.Err == nil {
					return fmt.Errorf("expected the argument to be refused, resolving returned %v", in.IPs)
				}
				if !in.IsIPArg {
					return fmt.Errorf("expected an ErrNodeNameIsIP refusal, got %q", in.Message)
				}
				if len(in.IPs) > 0 {
					return fmt.Errorf("expected no address to come back, got %v before the refusal %q", in.IPs, in.Message)
				}
				return nil
			}),
	}
}

func emptyNodeCluster(res brine.Resources) (NodeCluster, error) {
	cluster, err := getRealCluster(res)
	if err != nil {
		return NodeCluster{}, err
	}
	trace := new(execObservation)
	resolverClient, err := kubernetes.NewForConfig(trace.config(cluster.RESTConfig))
	if err != nil {
		return NodeCluster{}, err
	}
	in := NodeCluster{
		Ctx: context.Background(), Clientset: cluster.Clientset,
		Resolver: jetbridge.NewNodeIPResolver(resolverClient), trace: trace,
	}
	ctx, cancel := context.WithTimeout(in.Ctx, 10*time.Second)
	defer cancel()
	nodes, err := in.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return in, fmt.Errorf("read initial nodes: %w", err)
	}
	if len(nodes.Items) != 0 {
		return in, fmt.Errorf("expected an empty scenario node inventory, found %d nodes", len(nodes.Items))
	}
	return in, nil
}

func (out *NodeIPOutcome) resolve(in NodeCluster, name string) bool {
	out.trace = in.trace
	ip, err := in.Resolver.Resolve(in.Ctx, name)
	if err != nil {
		out.Err, out.Message = err, err.Error()
		out.IsIPArg = errors.Is(err, jetbridge.ErrNodeNameIsIP)
		return false
	}
	out.IPs = append(out.IPs, ip)
	return true
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func persistArtifactVolume(in IntegrationCluster, name string, teamID int) (IntegrationCluster, error) {
	vol, artifact, err := in.Worker.CreateVolumeForArtifact(in.Ctx, teamID)
	if err != nil {
		return IntegrationCluster{}, fmt.Errorf("create artifact volume %q: %w", name, err)
	}
	key := vol.Handle()
	if keyed, ok := vol.(interface{ Key() string }); ok {
		key = keyed.Key()
	}
	in.Artifacts[name] = NamedArtifact{
		Name: name, Handle: vol.Handle(), Key: key,
		TeamID: teamID, Volume: vol, Artifact: artifact,
	}
	return in, nil
}

func otherTeamIDs(in IntegrationCluster, exclude int) []int {
	seen := map[int]bool{exclude: true}
	var ids []int
	if !seen[in.Team.ID()] {
		ids = append(ids, in.Team.ID())
		seen[in.Team.ID()] = true
	}
	for _, named := range in.Artifacts {
		if !seen[named.TeamID] {
			ids = append(ids, named.TeamID)
			seen[named.TeamID] = true
		}
	}
	return ids
}

// runStep observes API-only pod construction. Actual command execution and
// artifact integration live in live_artifact_integration.go.
func runStep(in StepCreated, spec runtime.ProcessSpec, pio runtime.ProcessIO) (StepRan, error) {
	out := StepRan{
		Created:     in,
		BoundBefore: map[string]string{},
		BoundAfter:  map[string]string{},
	}
	for _, m := range in.Mounts {
		out.BoundBefore[m.MountPath] = podNameOf(m.Volume)
	}

	_, err := in.Container.Run(in.Cluster.Ctx, spec, pio)
	if err != nil {
		return StepRan{}, fmt.Errorf("run container %q: %w", in.Handle, err)
	}

	for _, m := range in.Mounts {
		out.BoundAfter[m.MountPath] = podNameOf(m.Volume)
	}

	// The pod the step created is named after the step, not after the handle,
	// so ask for it by the name the runtime would have used. Listing and
	// taking the only pod would forbid a scenario from holding two steps.
	podName := jetbridge.GeneratePodName(in.Metadata, in.Handle)
	pod, err := in.Cluster.Clientset.CoreV1().Pods(in.Cluster.Namespace).
		Get(in.Cluster.Ctx, podName, metav1.GetOptions{})
	if err != nil {
		return StepRan{}, fmt.Errorf("the step created no pod named %q: %w", podName, err)
	}
	if pod.UID == "" || pod.ResourceVersion == "" {
		return StepRan{}, fmt.Errorf("integration pod has no API identity")
	}
	out.Pod = pod

	pods, listErr := in.Cluster.Clientset.CoreV1().Pods(in.Cluster.Namespace).
		List(in.Cluster.Ctx, metav1.ListOptions{})
	if listErr != nil {
		return StepRan{}, fmt.Errorf("list pods: %w", listErr)
	}
	out.PodCount = len(pods.Items)

	return out, nil
}

func checkContainerRow(cluster IntegrationCluster, handle, wantType, wantWorker string) error {
	var gotState, gotWorker, gotType string
	err := cluster.DB.Conn.QueryRow(
		`SELECT state::text, worker_name, meta_type FROM containers WHERE handle = $1`,
		handle,
	).Scan(&gotState, &gotWorker, &gotType)
	if err != nil {
		return fmt.Errorf("read the container row for %q: %w", handle, err)
	}
	if gotState != "created" {
		return fmt.Errorf("expected container %q to be created, it is %q", handle, gotState)
	}
	if gotWorker != wantWorker {
		return fmt.Errorf("expected container %q on worker %q, got %q", handle, wantWorker, gotWorker)
	}
	if gotType != wantType {
		return fmt.Errorf("expected container %q to be a %q container, got %q", handle, wantType, gotType)
	}
	return nil
}

func podNameOf(vol runtime.Volume) string {
	if named, ok := vol.(interface{ PodName() string }); ok {
		return named.PodName()
	}
	return ""
}

func mountPaths(mounts []runtime.VolumeMount) []string {
	paths := make([]string, len(mounts))
	for i, m := range mounts {
		paths[i] = m.MountPath
	}
	return paths
}

func integrationMainContainer(pod *corev1.Pod) (corev1.Container, error) {
	if pod == nil {
		return corev1.Container{}, fmt.Errorf("no pod was created")
	}
	return mainContainer(pod)
}

func integrationEnvVar(pod *corev1.Pod, name string) (corev1.EnvVar, error) {
	main, err := integrationMainContainer(pod)
	if err != nil {
		return corev1.EnvVar{}, err
	}
	for i := range main.Env {
		if main.Env[i].Name == name {
			return main.Env[i], nil
		}
	}
	var names []string
	for _, e := range main.Env {
		names = append(names, e.Name)
	}
	return corev1.EnvVar{}, fmt.Errorf("expected the pod to carry %q, it carries %v", name, names)
}

// ---------------------------------------------------------------------------
// Dispositions
// ---------------------------------------------------------------------------
//
// behavioral_worker_test.go — 8 of 15 cases are not scenarios here.
//
//	"RC-05: returns not found when no persisted volume has the handle"
//	    Already migrated. worker.feature, "A handle the database does not
//	    hold is not found — nothing like it".
//
//	"RC-05: returns not found when volumeRepo is nil"
//	    Already migrated. worker.feature, "A worker with no volume repository
//	    reports every volume missing", which also records the fact that a
//	    misconfiguration is indistinguishable from an absent volume.
//
//	"CO-09: persists an artifact volume and returns its database artifact"
//	    Already migrated. worker.feature, "An artifact volume is persisted
//	    with the artifact it carries", which asserts the same four columns.
//	    Its extra clause — `dsVol.Key() == ArtifactKey(vol.Handle())` — is a
//	    Go-type assertion whose observable consequence is that a read under
//	    that key reaches the daemon; worker.feature's "A step's output
//	    outlives the pod that produced it — an arbitrary handle as key" is
//	    exactly that read, and "An artifact key is the same on every lookup"
//	    below covers the stability half.
//
//	"LR-04: returns the same persisted container without inserting another row"
//	    Already migrated. worker.feature, "Asking again for a container
//	    returns the one already recorded", including the row count.
//
//	"LookupVolume propagates DB errors"
//	    Already migrated. worker.feature, "Looking a volume up reports a lost
//	    database".
//
//	"SkipResourceCache returns false"
//	    Already migrated. worker.feature, "The worker presents the identity
//	    the database gave it" — "the worker takes part in resource caching".
//
//	"CreateVolumeForArtifact without volumeRepo"
//	    Already migrated. worker.feature, "Creating an artifact volume
//	    without a volume repository is refused".
//
//	"LookupVolume passes handle to FindVolume / finds only the exact
//	 persisted handle"
//	    Already migrated. worker.feature, "A volume in the database is found
//	    by its handle" plus the outline row "a prefix of a real one".
//
// artifact_integration_test.go — 2 of 8 cases are not scenarios here.
//
//	"artifact volumes are created as VolumeTypeArtifact for Reaper
//	 identification"
//	    Already migrated. worker.feature, "An artifact volume is persisted
//	    with the artifact it carries" asserts type "artifact" on the
//	    persisted row, from the same CreateVolumeForArtifact call.
//
//	"CreateVolumeForArtifact always returns DaemonSetVolume"
//	    Vacuous, and a duplicate. Its BeforeEach rebuilds the worker with
//	    `jetbridge.NewConfig("ci-namespace", "")` — byte-for-byte the config
//	    the outer BeforeEach already used — so "noArtifactWorker" is the same
//	    worker under another name and the case asserts exactly what its
//	    siblings assert. This is the same defect worker.feature already
//	    recorded for the ginkgo Context called "when the artifact store is
//	    configured", which configured nothing. Migrating it would import the
//	    vacuum.
//
// executor_test.go — all 3 cases are dispositioned, immediately below.
//
// executor_test.go is not migrated at all, for two separate reasons.
//
//	TestNewSPDYExecutorCreation and TestNewSPDYExecutorWithDifferentConfigs
//	assert `executor.clientset == clientset` and `executor.restConfig.Host ==
//	host` — UNEXPORTED FIELDS of jetbridge.SPDYExecutor. The task brief
//	stated this file "reach[es] no unexported identifiers"; that is not
//	correct, and it is why these cannot move to an external package at all.
//	Beyond reachability they are constructor-field-inspection tests: they
//	assert that a struct literal stored what was passed to it, which is
//	coverage_matrix.md Addendum's "mechanism, not behavior" class in its
//	purest form. Disposition: keep as Go unit tests, do not call them
//	behavioral requirements.
//
//	TestExecExitErrorMessage asserts ExecExitError's message string. It is
//	migratable in principle, but the behavior a consumer depends on is that a
//	non-zero exit REACHES THEM AS AN EXIT STATUS, not that the intermediate
//	error reads a particular way. "A resource script that fails hands its
//	exit code back" in step-integration.feature drives exactly that path
//	through a real non-zero exit. Disposition: covered by effect; the string
//	assertion stays a Go unit test.
