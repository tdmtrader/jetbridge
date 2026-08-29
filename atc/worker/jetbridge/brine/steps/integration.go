package steps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
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
	"k8s.io/client-go/kubernetes/fake"
)

// IntegrationDefinitions migrates the jetbridge suite's INTEGRATION files —
// the ones that drive a real worker against a real PostgreSQL database and a
// fake Kubernetes cluster, end to end:
//
//	behavioral_worker_test.go     15 cases  (RC / CO / LR families)
//	podname_integration_test.go    9 cases  (PN-07 and the pod-name seam)
//	artifact_integration_test.go   8 cases  (artifact passing between steps)
//	resource_test.go               6 cases  (get / put / check step protocol)
//	secret_env_test.go             2 cases  (SecretEnv -> SecretKeyRef)
//	node_ip_resolver_test.go       4 cases  (node name -> internal IP)
//	executor_test.go               3 cases  (see executorDisposition)
//
// Every case is either a scenario in ../features/step-integration.feature or
// carries a disposition comment in this file. Dispositions are grouped at the
// bottom under "Dispositions".
//
// Two conventions inherited from the files this joins:
//
//   - coverage_matrix.md Addendum 2 — a recording double can only tell you
//     what it recorded, so replace it with a WORKING one and assert the round
//     trip. resource_test.go's six spy sites and artifact_integration_test.go's
//     five are answered by localResourceAdapter and localShellAdapter below,
//     which really run the command. Nothing here asserts a pod name, a
//     namespace, a container name or a command slice that was handed to a
//     collaborator.
//
//   - worker.feature's rule — never assert that a volume is of a particular Go
//     type. `Expect(vol).To(BeAssignableToTypeOf(&DaemonSetVolume{}))` is not
//     something a consumer of runtime.Volume can observe. The effect is.

// ---------------------------------------------------------------------------
// Domain states
// ---------------------------------------------------------------------------

// IntegrationCluster is a jetbridge worker on a fake Kubernetes cluster backed
// by a real PostgreSQL database, plus a team to own the rows. Every Given in
// step-integration.feature refines this state.
//
// Its PodStartupTimeout is deliberately seconds, not the five-minute default.
// A scenario that waits on a Kubernetes deadline HANGS rather than failing,
// and a hang is worse than an absent test.
type IntegrationCluster struct {
	Namespace string
	Ctx       context.Context
	DB        JetbridgeDB
	DBWorker  db.Worker
	Team      db.Team
	Clientset *fake.Clientset
	Config    jetbridge.Config
	Worker    *jetbridge.Worker

	// ResourceRoot is the directory the local resource scripts live in, when
	// the worker runs resource scripts. Empty otherwise.
	ResourceRoot string

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
	Created    StepCreated
	Pod        *corev1.Pod
	PodCount   int
	Stdout     string
	ExitStatus int
	ProcessID  string
	Err        error
	Message    string

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
	Clientset *fake.Clientset
	Resolver  *jetbridge.NodeIPResolver
}

type NodeIPOutcome struct {
	IPs     []string
	Err     error
	Message string
	IsIPArg bool
}

// ---------------------------------------------------------------------------
// Working doubles
// ---------------------------------------------------------------------------

// localResourceAdapter is a REAL PodExecutor that runs the resource script the
// runtime asked for, from a directory laid out like a resource image.
//
// This is coverage_matrix.md Addendum 2 applied to resource_test.go's six spy
// sites, which assert:
//
//	call.command == []string{"/opt/resource/in", "/tmp/build/get"}
//	call.podName == "get-resource-handle"
//	call.namespace == "test-namespace"
//	call.containerName == "main"
//	io.ReadAll(call.stdin) == stdinJSON
//
// None of that proves the script ran, that its answer reached the caller, or
// that a non-zero exit came back. Installing a real script at
// <root>/opt/resource/in that echoes its argument and its stdin proves the
// path, the argument and the stdin plumbing TOGETHER, through the only thing a
// get step consumer ever sees: the bytes on stdout.
//
// The named behavioral difference: the script runs in a local directory
// instead of in a pod. It records nothing.
type localResourceAdapter struct {
	root string
}

func (l localResourceAdapter) ExecInPod(
	ctx context.Context,
	_, _, _ string,
	command []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	_ bool,
	_ jetbridge.ExecAttrs,
) error {
	if len(command) == 0 {
		return fmt.Errorf("empty command")
	}
	// Resolve the pod-absolute script path into this adapter's root. The
	// runtime builds the path; we honour it rather than asserting on it, so a
	// runtime that execs the wrong script finds nothing there.
	//
	// Anything the resource image does not provide runs as named, which is
	// what makes a scenario able to hold a resource step and a task step at
	// once: the image is an overlay on the local filesystem, not a jail.
	program := filepath.Join(l.root, filepath.Clean("/"+command[0]))
	if _, err := os.Stat(program); err != nil {
		program = command[0]
	}
	cmd := exec.CommandContext(ctx, program, command[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr

	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		// Surface a non-zero exit the way the SPDY executor does, so the
		// runtime's exit-code extraction is on the real path.
		return &jetbridge.ExecExitError{ExitCode: exitErr.ExitCode()}
	}
	return err
}

// installResourceScripts writes a tiny but real resource implementation: three
// scripts that read the request from stdin, echo it back with the directory
// they were given, and exit with the code the scenario asked for.
func installResourceScripts(root string, exitCode int) error {
	dir := filepath.Join(root, "opt", "resource")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create resource dir: %w", err)
	}
	for _, name := range []string{"in", "out", "check"} {
		body := "#!/bin/sh\n" +
			"request=$(cat)\n" +
			"printf 'script=" + name + " dir=%s request=%s' \"$1\" \"$request\"\n" +
			"exit " + strconv.Itoa(exitCode) + "\n"
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Step definitions
// ---------------------------------------------------------------------------

// IntegrationDefinitions is the single entry point this file exports.
func IntegrationDefinitions() []brine.StepDefinition {
	defs := integrationClusterDefinitions()
	defs = append(defs, integrationStepDefinitions()...)
	defs = append(defs, integrationRunDefinitions()...)
	defs = append(defs, integrationPodCheckDefinitions()...)
	defs = append(defs, integrationVolumeDefinitions()...)
	defs = append(defs, integrationNodeIPDefinitions()...)
	return defs
}

func integrationClusterDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMapUsing[brine.Empty, IntegrationCluster](
			"a jetbridge cluster in namespace {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, res brine.Resources) (IntegrationCluster, error) {
				ns, ok := p.GetString(0)
				if !ok {
					return IntegrationCluster{}, fmt.Errorf("expected a namespace parameter")
				}
				return newIntegrationCluster(res, ns)
			},
		),

		// The worker gets an exec transport. Which one depends on what the
		// scenario is about: a shell for task commands, a resource image for
		// the get/put/check protocol.
		Refine[IntegrationCluster]("the worker execs commands in pods",
			func(in IntegrationCluster, _ Args) IntegrationCluster {
				in.Worker.SetExecutor(localShellAdapter{})
				return in
			}),

		brine.DefineMap[IntegrationCluster, IntegrationCluster](
			"the worker runs resource scripts that exit {int}",
			func(in IntegrationCluster, p brine.Params, _ *brine.Recorder) (IntegrationCluster, error) {
				code, ok := p.GetInt(0)
				if !ok {
					return IntegrationCluster{}, fmt.Errorf("expected an exit code parameter")
				}
				root, err := os.MkdirTemp("", "brine-resource")
				if err != nil {
					return IntegrationCluster{}, fmt.Errorf("create resource root: %w", err)
				}
				if err := installResourceScripts(root, code); err != nil {
					return IntegrationCluster{}, err
				}
				in.ResourceRoot = root
				in.Worker.SetExecutor(localResourceAdapter{root: root})
				return in, nil
			},
		),

		// A locator entry is the state a previous step's output left behind.
		// LookupVolume is supposed to be indifferent to it — see the
		// "whatever the locator remembers" scenario.
		Refine[IntegrationCluster]("the worker remembers the artifact {string} on node {string}",
			func(in IntegrationCluster, a Args) IntegrationCluster {
				locator := jetbridge.NewArtifactLocator()
				locator.Record(jetbridge.ArtifactKey(a.String(0)), a.String(1), "container/output")
				in.Worker.SetArtifactLocator(locator)
				return in
			}),

		brine.DefineMap[IntegrationCluster, IntegrationCluster](
			"an artifact volume {string} persisted for this team",
			func(in IntegrationCluster, p brine.Params, _ *brine.Recorder) (IntegrationCluster, error) {
				name, ok := p.GetString(0)
				if !ok {
					return IntegrationCluster{}, fmt.Errorf("expected a volume name parameter")
				}
				return persistArtifactVolume(in, name, in.Team.ID())
			},
		),

		brine.DefineMap[IntegrationCluster, IntegrationCluster](
			"an artifact volume {string} persisted for a second team",
			func(in IntegrationCluster, p brine.Params, _ *brine.Recorder) (IntegrationCluster, error) {
				name, ok := p.GetString(0)
				if !ok {
					return IntegrationCluster{}, fmt.Errorf("expected a volume name parameter")
				}
				other, err := in.DB.TeamFactory.CreateTeam(atc.Team{Name: "artifact-team-2"})
				if err != nil {
					return IntegrationCluster{}, fmt.Errorf("create second team: %w", err)
				}
				return persistArtifactVolume(in, name, other.ID())
			},
		),

		// A volume row written straight through the repository, the way a
		// previous step's output already sits in the database when the next
		// step looks it up.
		brine.DefineMap[IntegrationCluster, IntegrationCluster](
			"a volume {string} recorded against this worker",
			func(in IntegrationCluster, p brine.Params, _ *brine.Recorder) (IntegrationCluster, error) {
				handle, ok := p.GetString(0)
				if !ok {
					return IntegrationCluster{}, fmt.Errorf("expected a handle parameter")
				}
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

		brine.DefineMap[IntegrationCluster, StepDraft](
			"a {string} step in pipeline {string} job {string} build {string} named {string} with handle {string}",
			func(in IntegrationCluster, p brine.Params, _ *brine.Recorder) (StepDraft, error) {
				kind, _ := p.GetString(0)
				pipeline, _ := p.GetString(1)
				job, _ := p.GetString(2)
				build, _ := p.GetString(3)
				stepName, _ := p.GetString(4)
				handle, ok := p.GetString(5)
				if !ok {
					return StepDraft{}, fmt.Errorf("expected six parameters")
				}
				containerType := db.ContainerType(kind)
				return StepDraft{
					Cluster: in,
					Handle:  handle,
					Metadata: db.ContainerMetadata{
						Type:         containerType,
						PipelineName: pipeline,
						JobName:      job,
						BuildName:    build,
						StepName:     stepName,
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
		brine.DefineMap[IntegrationCluster, StepDraft](
			"a task step with handle {string} and no pipeline or job",
			func(in IntegrationCluster, p brine.Params, _ *brine.Recorder) (StepDraft, error) {
				handle, ok := p.GetString(0)
				if !ok {
					return StepDraft{}, fmt.Errorf("expected a handle parameter")
				}
				return StepDraft{
					Cluster:  in,
					Handle:   handle,
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

		// A resource step is described by its type, not by an image URL: the
		// worker resolves "git" to concourse/git-resource itself.
		brine.DefineMap[IntegrationCluster, StepDraft](
			"a {string} step {string} for resource type {string}",
			func(in IntegrationCluster, p brine.Params, _ *brine.Recorder) (StepDraft, error) {
				kind, _ := p.GetString(0)
				handle, _ := p.GetString(1)
				resourceType, ok := p.GetString(2)
				if !ok {
					return StepDraft{}, fmt.Errorf("expected three parameters")
				}
				containerType := db.ContainerType(kind)
				return StepDraft{
					Cluster:  in,
					Handle:   handle,
					Metadata: db.ContainerMetadata{Type: containerType},
					Spec: runtime.ContainerSpec{
						TeamID:         in.Team.ID(),
						TeamName:       in.Team.Name(),
						ImageSpec:      runtime.ImageSpec{ResourceType: resourceType},
						Type:           containerType,
						CertsBindMount: true,
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

		Refine[StepDraft]("the step takes an input at {string}",
			func(in StepDraft, a Args) StepDraft {
				in.Spec.Inputs = append(in.Spec.Inputs, runtime.Input{DestinationPath: a.String(0)})
				return in
			}),

		brine.DefineMap[StepDraft, StepDraft](
			"the step takes the artifact {string} as an input at {string}",
			func(in StepDraft, p brine.Params, _ *brine.Recorder) (StepDraft, error) {
				name, _ := p.GetString(0)
				path, ok := p.GetString(1)
				if !ok {
					return StepDraft{}, fmt.Errorf("expected an artifact name and a path")
				}
				named, found := in.Cluster.Artifacts[name]
				if !found {
					return StepDraft{}, fmt.Errorf("no artifact volume named %q was created", name)
				}
				// Look it up the way the next step does, rather than reusing
				// the object the producing step happened to hold.
				vol, ok2, err := in.Cluster.Worker.LookupVolume(in.Cluster.Ctx, named.Handle)
				if err != nil {
					return StepDraft{}, fmt.Errorf("look up artifact %q: %w", name, err)
				}
				if !ok2 {
					return StepDraft{}, fmt.Errorf("artifact volume %q is not in the database", name)
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
					&noopDelegate{},
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

		// Three parameters, and three independent claims about the row: that
		// it is created, that it is of that type, and that it is on that
		// worker. No combinator compares more than one value, and folding two
		// of the three into a getter error would demote them to presumptions.
		brine.DefineCheck[StepCreated](
			"the container row for {string} is a created {string} container on worker {string}",
			func(in StepCreated, p brine.Params, _ *brine.Recorder) error {
				handle, _ := p.GetString(0)
				wantType, _ := p.GetString(1)
				wantWorker, ok := p.GetString(2)
				if !ok {
					return fmt.Errorf("expected a handle, a type and a worker name")
				}
				return checkContainerRow(in.Cluster, handle, wantType, wantWorker)
			},
		),
	}
}

func integrationRunDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// Direct mode: no executor, so the command is baked into the pod spec
		// and Run returns as soon as the pod exists. Nothing is waited on, so
		// nothing can hang.
		brine.DefineMap[StepCreated, StepRan](
			"the step's container runs",
			func(in StepCreated, _ brine.Params, _ *brine.Recorder) (StepRan, error) {
				return runStep(in, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{}, false)
			},
		),

		// Exec mode with a shell that really runs the command, and a wait, so
		// what the scenario asserts is what a build log would show.
		// A task step with no stdin runs under the in-pod supervisor, whose
		// state directory is keyed on the process id plus a hash of the
		// COMMAND. Left alone that key is stable across runs, so a second
		// `brine run` would replay the first run's log instead of executing
		// anything — green, and blind to a regression. Threading the
		// scenario-scoped workspace through the command makes the key unique
		// per run without changing what the command prints.
		brine.DefineMapUsing[StepCreated, StepRan](
			"the step's container runs the command {string}",
			[]string{"task-workspace"},
			func(in StepCreated, p brine.Params, _ *brine.Recorder, res brine.Resources) (StepRan, error) {
				command, ok := p.GetString(0)
				if !ok {
					return StepRan{}, fmt.Errorf("expected a command parameter")
				}
				workspace, ok := res.Get("task-workspace").(TaskWorkspace)
				if !ok {
					return StepRan{}, fmt.Errorf("task-workspace resource is %T", res.Get("task-workspace"))
				}
				log := new(bytes.Buffer)
				return runStep(in, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", command + " # " + workspace.Dir},
				}, runtime.ProcessIO{Stdout: log, Stderr: log}, true, log)
			},
		),

		// The resource protocol: a request on stdin, an answer on stdout.
		brine.DefineMap[StepCreated, StepRan](
			"the resource is asked for {string} into {string}",
			func(in StepCreated, p brine.Params, _ *brine.Recorder) (StepRan, error) {
				request, _ := p.GetString(0)
				dir, ok := p.GetString(1)
				if !ok {
					return StepRan{}, fmt.Errorf("expected a request and a directory")
				}
				script := "/opt/resource/in"
				if in.Metadata.Type == db.ContainerTypePut {
					script = "/opt/resource/out"
				}
				out := new(bytes.Buffer)
				return runStep(in, runtime.ProcessSpec{
					ID:   "resource",
					Path: script,
					Args: []string{dir},
				}, runtime.ProcessIO{
					Stdin:  bytes.NewBufferString(request),
					Stdout: out,
					Stderr: new(bytes.Buffer),
				}, true, out)
			},
		),

		brine.DefineMap[StepCreated, StepRan](
			"the resource is checked with {string}",
			func(in StepCreated, p brine.Params, _ *brine.Recorder) (StepRan, error) {
				request, ok := p.GetString(0)
				if !ok {
					return StepRan{}, fmt.Errorf("expected a request parameter")
				}
				out := new(bytes.Buffer)
				return runStep(in, runtime.ProcessSpec{
					Path: "/opt/resource/check",
				}, runtime.ProcessIO{
					Stdin:  bytes.NewBufferString(request),
					Stdout: out,
					Stderr: new(bytes.Buffer),
				}, true, out)
			},
		),

		// A pipeline is more than one step. The cluster travels inside the
		// outcome, so the next step is described from where the last one
		// finished rather than from a fresh Given.
		brine.DefineMap[StepRan, StepDraft](
			"next, a {string} step {string} for resource type {string}",
			func(in StepRan, p brine.Params, _ *brine.Recorder) (StepDraft, error) {
				kind, _ := p.GetString(0)
				handle, _ := p.GetString(1)
				resourceType, ok := p.GetString(2)
				if !ok {
					return StepDraft{}, fmt.Errorf("expected three parameters")
				}
				containerType := db.ContainerType(kind)
				cluster := in.Created.Cluster
				return StepDraft{
					Cluster:  cluster,
					Handle:   handle,
					Metadata: db.ContainerMetadata{Type: containerType},
					Spec: runtime.ContainerSpec{
						TeamID:         cluster.Team.ID(),
						TeamName:       cluster.Team.Name(),
						ImageSpec:      runtime.ImageSpec{ResourceType: resourceType},
						Type:           containerType,
						CertsBindMount: true,
					},
				}, nil
			},
		),

		// Attaching. Both halves of the pod-name seam live here: a step that
		// already exited is resumed from its recorded status, and a step whose
		// pod is gone has to say WHICH pod is gone.
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

		// The exit status a completed step left behind, plus the pod it left
		// behind. Both are prerequisites for a successful re-attach.
		brine.DefineMap[StepCreated, StepCreated](
			"the step finished with exit status {string} and left its pod behind",
			func(in StepCreated, p brine.Params, _ *brine.Recorder) (StepCreated, error) {
				status, ok := p.GetString(0)
				if !ok {
					return StepCreated{}, fmt.Errorf("expected an exit status parameter")
				}
				podName := jetbridge.GeneratePodName(in.Metadata, in.Handle)
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: in.Cluster.Namespace},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "main", Image: "busybox"}},
					},
				}
				if _, err := in.Cluster.Clientset.CoreV1().Pods(in.Cluster.Namespace).
					Create(in.Cluster.Ctx, pod, metav1.CreateOptions{}); err != nil {
					return StepCreated{}, fmt.Errorf("create pod %q: %w", podName, err)
				}
				if err := in.Container.SetProperty("concourse:exit-status", status); err != nil {
					return StepCreated{}, fmt.Errorf("record exit status: %w", err)
				}
				return in, nil
			},
		),

		CheckInt[AttachOutcome]("the step resumes reporting exit status {int}",
			"the resumed exit status",
			func(in AttachOutcome) (int, error) {
				if in.Err != nil {
					return 0, fmt.Errorf("expected the step to resume, attaching failed: %v", in.Err)
				}
				return in.ExitStatus, nil
			}),

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
		brine.DefineCheck[StepRan](
			"the pod in the cluster is named to match {string}",
			func(in StepRan, p brine.Params, _ *brine.Recorder) error {
				pattern, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a pattern parameter")
				}
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
						key, labelKeys(in.Pod.Labels))
				}
				return got, nil
			}),

		CheckNotMember[StepRan]("the pod carries no {string} label",
			"the pod's labels",
			func(in StepRan) ([]string, error) {
				if in.Pod == nil {
					return nil, fmt.Errorf("no pod was created")
				}
				return labelKeys(in.Pod.Labels), nil
			}),

		// PN-07's hard half: Kubernetes rejects a pod whose label value is
		// longer than 63 characters, so a long pipeline name has to be cut.
		CheckThat[StepRan]("every pod label value fits in a Kubernetes label",
			func(in StepRan) error {
				if in.Pod == nil {
					return fmt.Errorf("no pod was created")
				}
				for key, value := range in.Pod.Labels {
					if len(value) > 63 {
						return fmt.Errorf("label %q is %d characters, over the 63-character limit: %q",
							key, len(value), value)
					}
				}
				return nil
			}),

		CheckString[StepRan]("the step's pod runs the image {string}",
			"the image the step's pod runs",
			func(in StepRan) (string, error) {
				main, err := integrationMainContainer(in.Pod)
				return main.Image, err
			}),

		// PE-01: the pod is a pause pod. Baking the resource script into the
		// pod spec would run it once, at pod start, with no stdin and nowhere
		// to send stdout — the resource protocol needs an exec.
		CheckThat[StepRan]("the pod's own command is not the resource script",
			func(in StepRan) error {
				main, err := integrationMainContainer(in.Pod)
				if err != nil {
					return err
				}
				if len(main.Command) == 0 {
					return fmt.Errorf("expected the pod to carry a pause command, it has none")
				}
				if strings.HasPrefix(main.Command[0], "/opt/resource/") {
					return fmt.Errorf("expected a pause command, the pod runs %v", main.Command)
				}
				return nil
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
		brine.DefineCheck[StepRan](
			"the pod reads {string} from the secret {string} key {string}",
			func(in StepRan, p brine.Params, _ *brine.Recorder) error {
				name, _ := p.GetString(0)
				secret, _ := p.GetString(1)
				key, ok := p.GetString(2)
				if !ok {
					return fmt.Errorf("expected three parameters")
				}
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
		brine.DefineCheck[StepRan](
			"the mount at {string} read from no pod before the step ran",
			func(in StepRan, p brine.Params, _ *brine.Recorder) error {
				path, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a mount path parameter")
				}
				bound, found := in.BoundBefore[path]
				if !found {
					return fmt.Errorf("no mount at %q (have %v)", path, keysOf(in.BoundBefore))
				}
				if bound != "" {
					return fmt.Errorf("expected the mount at %q to read from no pod before the step ran, it read from %q",
						path, bound)
				}
				return nil
			},
		),

		brine.DefineCheck[StepRan](
			"the mount at {string} reads from the pod the step created",
			func(in StepRan, p brine.Params, _ *brine.Recorder) error {
				path, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a mount path parameter")
				}
				if in.Pod == nil {
					return fmt.Errorf("no pod was created")
				}
				bound, found := in.BoundAfter[path]
				if !found {
					return fmt.Errorf("no mount at %q (have %v)", path, keysOf(in.BoundAfter))
				}
				if bound != in.Pod.Name {
					return fmt.Errorf("expected the mount at %q to read from the pod %q, it reads from %q",
						path, in.Pod.Name, bound)
				}
				return nil
			},
		),

		CheckString[StepRan]("the resource answers {string}",
			"the resource's answer",
			func(in StepRan) (string, error) {
				if in.Err != nil {
					return "", fmt.Errorf("the resource step failed: %v", in.Err)
				}
				return in.Stdout, nil
			}),

		CheckContains[StepRan]("the step's output is {string}",
			"the step's output",
			func(in StepRan) (string, error) {
				if in.Err != nil {
					return "", fmt.Errorf("the step failed: %v", in.Err)
				}
				return in.Stdout, nil
			}),

		// Same three claims as the StepCreated form above — created, of that
		// type, on that worker — so the same reason it keeps its own body.
		brine.DefineCheck[StepRan](
			"the step's container row is a created {string} container on worker {string}",
			func(in StepRan, p brine.Params, _ *brine.Recorder) error {
				wantType, _ := p.GetString(0)
				wantWorker, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected a type and a worker name")
				}
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

		CheckString[StepRan]("the running process is identified as {string}",
			"the running process's id",
			func(in StepRan) (string, error) {
				return in.ProcessID, nil
			}),
	}
}

func integrationVolumeDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[IntegrationCluster, IntegrationVolume](
			"the volume {string} is looked up twice",
			func(in IntegrationCluster, p brine.Params, _ *brine.Recorder) (IntegrationVolume, error) {
				handle, ok := p.GetString(0)
				if !ok {
					return IntegrationVolume{}, fmt.Errorf("expected a handle parameter")
				}
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
		brine.DefineMap[IntegrationCluster, IntegrationVolume](
			"a restarted ATC looks the volume {string} up",
			func(in IntegrationCluster, p brine.Params, _ *brine.Recorder) (IntegrationVolume, error) {
				handle, ok := p.GetString(0)
				if !ok {
					return IntegrationVolume{}, fmt.Errorf("expected a handle parameter")
				}
				restarted := jetbridge.NewWorker(in.DBWorker, in.Clientset, in.Config)
				restarted.SetVolumeRepo(db.NewVolumeRepository(in.DB.Conn))
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
		brine.DefineMap[IntegrationCluster, IntegrationVolume](
			"the reaper destroys the artifact volume {string}",
			func(in IntegrationCluster, p brine.Params, _ *brine.Recorder) (IntegrationVolume, error) {
				name, ok := p.GetString(0)
				if !ok {
					return IntegrationVolume{}, fmt.Errorf("expected an artifact name parameter")
				}
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
		brine.DefineMap[IntegrationCluster, IntegrationVolume](
			"the volume {string} is initialised as the resource cache for type {string} version {string}",
			func(in IntegrationCluster, p brine.Params, _ *brine.Recorder) (IntegrationVolume, error) {
				handle, _ := p.GetString(0)
				resourceType, _ := p.GetString(1)
				version, ok := p.GetString(2)
				if !ok {
					return IntegrationVolume{}, fmt.Errorf("expected a handle, a type and a version")
				}

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
					atc.Version{"version": version},
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
		brine.DefineCheck[IntegrationVolume](
			"it carries the database row for handle {string} on worker {string}",
			func(in IntegrationVolume, p brine.Params, _ *brine.Recorder) error {
				wantHandle, _ := p.GetString(0)
				wantWorker, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected a handle and a worker name")
				}
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
		brine.DefineMap[IntegrationCluster, IntegrationVolume](
			"the artifact {string} is asked for by its own team and by the other team",
			func(in IntegrationCluster, p brine.Params, _ *brine.Recorder) (IntegrationVolume, error) {
				name, ok := p.GetString(0)
				if !ok {
					return IntegrationVolume{}, fmt.Errorf("expected an artifact name parameter")
				}
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

		brine.DefineMap[brine.Empty, NodeCluster](
			"a cluster whose node {string} has internal address {string} and external address {string}",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (NodeCluster, error) {
				name, _ := p.GetString(0)
				internal, _ := p.GetString(1)
				external, ok := p.GetString(2)
				if !ok {
					return NodeCluster{}, fmt.Errorf("expected a name and two addresses")
				}
				var addresses []corev1.NodeAddress
				if internal != "" {
					addresses = append(addresses, corev1.NodeAddress{
						Type: corev1.NodeInternalIP, Address: internal})
				}
				if external != "" {
					addresses = append(addresses, corev1.NodeAddress{
						Type: corev1.NodeExternalIP, Address: external})
				}
				clientset := fake.NewSimpleClientset(&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{Name: name},
					Status:     corev1.NodeStatus{Addresses: addresses},
				})
				return NodeCluster{
					Ctx:       context.Background(),
					Clientset: clientset,
					Resolver:  jetbridge.NewNodeIPResolver(clientset),
				}, nil
			},
		),

		brine.DefineMap[brine.Empty, NodeCluster](
			"a cluster with no nodes",
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder) (NodeCluster, error) {
				clientset := fake.NewSimpleClientset()
				return NodeCluster{
					Ctx:       context.Background(),
					Clientset: clientset,
					Resolver:  jetbridge.NewNodeIPResolver(clientset),
				}, nil
			},
		),

		// Resolving twice is the cache case. The consumer-visible claim is
		// that the second answer is the same as the first — not that the
		// Nodes API went unasked, which only a recording double could say.
		brine.DefineMap[NodeCluster, NodeIPOutcome](
			"a caller resolves {string} twice",
			func(in NodeCluster, p brine.Params, _ *brine.Recorder) (NodeIPOutcome, error) {
				name, ok := p.GetString(0)
				if !ok {
					return NodeIPOutcome{}, fmt.Errorf("expected a node name parameter")
				}
				out := NodeIPOutcome{}
				for i := 0; i < 2; i++ {
					ip, err := in.Resolver.Resolve(in.Ctx, name)
					if err != nil {
						out.Err, out.Message = err, err.Error()
						out.IsIPArg = errors.Is(err, jetbridge.ErrNodeNameIsIP)
						return out, nil
					}
					out.IPs = append(out.IPs, ip)
				}
				return out, nil
			},
		),

		// EVERY element must equal the parameter, and there must be at least
		// one. That is neither membership — which one matching element would
		// satisfy — nor a count, and the failure has to say which answer of
		// the several differed.
		brine.DefineCheck[NodeIPOutcome](
			"every answer is {string}",
			func(in NodeIPOutcome, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected an address parameter")
				}
				if in.Err != nil {
					return fmt.Errorf("resolving failed: %v", in.Err)
				}
				if len(in.IPs) == 0 {
					return fmt.Errorf("no address came back")
				}
				for i, got := range in.IPs {
					if got != want {
						return fmt.Errorf("expected answer %d to be %q, got %q", i+1, want, got)
					}
				}
				return nil
			},
		),

		CheckThat[NodeIPOutcome]("resolving fails",
			func(in NodeIPOutcome) error {
				if in.Err == nil {
					return fmt.Errorf("expected resolving to fail, it returned %v", in.IPs)
				}
				return nil
			}),

		// The sentinel is the whole point: an IP-shaped argument is rejected
		// as a misuse, not reported as a node that happens not to exist. On a
		// cluster with no nodes at all the two outcomes are indistinguishable
		// by anything BUT the sentinel.
		CheckThat[NodeIPOutcome]("it is refused as an IP address rather than reported as a missing node",
			func(in NodeIPOutcome) error {
				if in.Err == nil {
					return fmt.Errorf("expected the argument to be refused, resolving returned %v", in.IPs)
				}
				if !in.IsIPArg {
					return fmt.Errorf("expected an ErrNodeNameIsIP refusal, got %q", in.Message)
				}
				return nil
			}),

		// The refusal has to come BEFORE the Nodes API, and "before" is a
		// claim about a call, which nothing here records. What can be stated
		// as an outcome is the consequence: on the one cluster where asking
		// and not asking differ in what comes back — a cluster that has a
		// node registered under that very IP-shaped name — the answer the API
		// would have given must not appear. A resolver that fell through
		// returns that node's internal address here, and returns it with no
		// error at all.
		//
		// The residue, stated rather than faked: a mutation that made the Get
		// and then threw the answer away in favour of the sentinel is
		// indistinguishable from production by any value that comes back out.
		// Only a call record separates those two, and this file does not keep
		// one.
		CheckThat[NodeIPOutcome]("it is refused as an IP address even though a node is registered under that name",
			func(in NodeIPOutcome) error {
				if in.Err == nil {
					return fmt.Errorf("expected the argument to be refused, resolving answered %v — the Nodes API was consulted and its answer used", in.IPs)
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

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newIntegrationCluster(res brine.Resources, namespace string) (IntegrationCluster, error) {
	handle := res.Get("jetbridge-db")
	database, ok := handle.(JetbridgeDB)
	if !ok {
		return IntegrationCluster{}, fmt.Errorf("jetbridge-db resource is %T", handle)
	}

	dbWorker, err := database.PersistNamedWorker("k8s-worker-1")
	if err != nil {
		return IntegrationCluster{}, err
	}
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "main"})
	if err != nil {
		return IntegrationCluster{}, fmt.Errorf("create team: %w", err)
	}

	clientset := fake.NewSimpleClientset()
	cfg := jetbridge.NewConfig(namespace, "")
	// Seconds, not the five-minute default: a scenario that waits on a
	// Kubernetes deadline must fail, not hang.
	cfg.PodStartupTimeout = 5 * time.Second
	cfg.PodSchedulingTimeout = 5 * time.Second

	worker := jetbridge.NewWorker(dbWorker, clientset, cfg)
	worker.SetVolumeRepo(database.VolumeRepository)

	return IntegrationCluster{
		Namespace: namespace,
		Ctx:       context.Background(),
		DB:        database,
		DBWorker:  dbWorker,
		Team:      team,
		Clientset: clientset,
		Config:    cfg,
		Worker:    worker,
		Artifacts: map[string]NamedArtifact{},
	}, nil
}

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

// runStep drives one container to a pod, and — when the scenario waits on it —
// to a process result. The optional log buffer is read after the wait so the
// scenario can assert what a consumer saw.
func runStep(in StepCreated, spec runtime.ProcessSpec, pio runtime.ProcessIO, wait bool, log ...*bytes.Buffer) (StepRan, error) {
	out := StepRan{
		Created:     in,
		BoundBefore: map[string]string{},
		BoundAfter:  map[string]string{},
	}
	for _, m := range in.Mounts {
		out.BoundBefore[m.MountPath] = podNameOf(m.Volume)
	}

	process, err := in.Container.Run(in.Cluster.Ctx, spec, pio)
	if err != nil {
		return StepRan{}, fmt.Errorf("run container %q: %w", in.Handle, err)
	}
	out.ProcessID = process.ID()

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
	out.Pod = pod

	pods, listErr := in.Cluster.Clientset.CoreV1().Pods(in.Cluster.Namespace).
		List(in.Cluster.Ctx, metav1.ListOptions{})
	if listErr != nil {
		return StepRan{}, fmt.Errorf("list pods: %w", listErr)
	}
	out.PodCount = len(pods.Items)

	if !wait {
		return out, nil
	}

	// The pause pod has to be Running before the exec, exactly as the kubelet
	// would have made it. Doing this BEFORE Wait is what keeps the scenario
	// from sitting on the startup deadline.
	if err := markIntegrationPodRunning(in.Cluster, pod.Name); err != nil {
		return StepRan{}, err
	}

	result, waitErr := process.Wait(in.Cluster.Ctx)
	out.ExitStatus = result.ExitStatus
	if waitErr != nil {
		out.Err, out.Message = waitErr, waitErr.Error()
	}
	if len(log) > 0 && log[0] != nil {
		out.Stdout = log[0].String()
	}

	refreshed, getErr := in.Cluster.Clientset.CoreV1().Pods(in.Cluster.Namespace).
		Get(in.Cluster.Ctx, pod.Name, metav1.GetOptions{})
	if getErr == nil {
		out.Pod = refreshed
	}
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

func markIntegrationPodRunning(cluster IntegrationCluster, podName string) error {
	pods := cluster.Clientset.CoreV1().Pods(cluster.Namespace)
	pod, err := pods.Get(cluster.Ctx, podName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get pod %q: %w", podName, err)
	}
	pod.Status.Phase = corev1.PodRunning
	pod.Status.Conditions = []corev1.PodCondition{
		{Type: corev1.PodReady, Status: corev1.ConditionTrue},
	}
	if _, err := pods.UpdateStatus(cluster.Ctx, pod, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update pod status: %w", err)
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

func labelKeys(labels map[string]string) []string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	return keys
}

func keysOf(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
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
