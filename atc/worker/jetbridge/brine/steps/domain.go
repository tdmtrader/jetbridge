// Package steps hosts the brine step registry for the jetbridge runtime's
// behavioral contract.
//
// Contract-3/4 authoring: every step is a transition between NAMED DOMAIN
// STATES. The chain walk keeps a SINGLE live state and replaces it wholesale
// on each map step, so a state must carry forward everything its successors
// need. `brine check` verifies each scenario's path without running anything.
package steps

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
)

// ClusterReady is the state after a jetbridge worker is registered against a
// fake Kubernetes cluster. Reached from brine.Empty.
type ClusterReady struct {
	Namespace string
	Worker    *jetbridge.Worker
	Clientset *fake.Clientset
	Ctx       context.Context
	// TeamID is a real team row, needed by anything that persists a volume;
	// the volumes table has a foreign key onto teams.
	TeamID int
}

// StepRunning is the state after a task container has been created and its
// process started. It carries the clientset forward because the live state is
// replaced wholesale — ClusterReady is gone once this step runs.
type StepRunning struct {
	Namespace string
	Clientset *fake.Clientset
	Ctx       context.Context
	Handle    string
	Process   runtime.Process
	Stderr    *bytes.Buffer
}

// StepOutcome is the terminal state: what the process reported when it was
// waited on. Check steps read this and cannot transition out of it.
type StepOutcome struct {
	Err     error
	Message string
	Stderr  string
	// ExitStatus is what the step actually reports. Process.Wait returns a
	// non-zero exit as a RESULT, not an error, so Err alone cannot tell a
	// failed task from a successful one.
	ExitStatus int
}

// noopDelegate satisfies runtime.BuildStepDelegate for scenarios that need
// neither volume streaming nor build timing. Mirrors the ginkgo suite's
// helper of the same name.
type noopDelegate struct{}

// ContainerDraft is a container spec under description. Map steps that refine
// the draft take ContainerDraft in and out — the live state's type is
// unchanged, so any number of them may appear in any order before the
// container runs.
type ContainerDraft struct {
	Namespace    string
	Worker       *jetbridge.Worker
	Clientset    *fake.Clientset
	Ctx          context.Context
	Handle       string
	ImageURL     string
	Dir          string
	ContainerEnv []string
	ProcessEnv   []string

	// Inputs are destination paths, each of which gets a real artifact volume
	// when the container runs. There is no artifact-less form: production's
	// two producers of runtime.Input — atc/exec/put_inputs.go and
	// atc/exec/task_step.go — both skip a name the artifact repository has no
	// artifact for, so an input with a nil Artifact is a state no pipeline can
	// reach, and the runtime now rejects one outright.
	Inputs           []string
	Outputs          []string
	Caches           []string
	Scratch          []string
	LimitCPU         *uint64
	LimitMemory      *uint64
	RequestCPU       *uint64
	RequestMemory    *uint64
	LimitEphemeral   *uint64
	RequestEphemeral *uint64
	JobID            int
	StepName         string

	// A materialized run job has no JobID of its own — its pipeline is
	// created and destroyed per run — so it names itself by the template and
	// the job name inside it, which are what stay the same from one run to
	// the next.
	RunJobName            string
	RunTemplatePipelineID int
	RunTeamID             int
	Privileged            bool
	Sidecars              []atc.SidecarConfig

	// ContainerType is empty for the task containers every scenario written
	// before check containers existed assumes; draftContainerType defaults it.
	ContainerType db.ContainerType
	// RanBefore makes the run step create the container row once first, so
	// the run under test finds it already created and is REUSED.
	RanBefore bool
	// TeamID carried from ClusterReady, so artifact volumes satisfy the
	// volumes table's foreign key onto teams.
	TeamID int
}

// taskCacheIdentity is what the described step's ContainerSpec must carry for
// its caches to be kept on the node.
//
// Since 0d336e062b the choice is the ContainerSpec's alone: hostPath cache
// storage is selected only when TaskCacheIdentity is set, and an explicit
// hostPath choice is downgraded to an emptyDir without one. The container
// METADATA's JobID, which is what these drafts used to lean on, no longer
// reaches the decision at all. A draft that never said which job it belongs to
// is a one-off build, which has no stable thing to key a cache under, so it
// keeps getting nil and its cache keeps dying with the pod.
//
// A materialized run job takes the other arm. Its pipeline exists only for the
// life of the run, so its JobID is new every time and would key a cache nobody
// ever reads again; what persists is the template it came from and the job's
// name within it.
func (d ContainerDraft) taskCacheIdentity() *atc.TaskCacheIdentity {
	if d.RunJobName != "" {
		return &atc.TaskCacheIdentity{
			TeamID:             d.RunTeamID,
			TemplatePipelineID: d.RunTemplatePipelineID,
			RunJobName:         d.RunJobName,
		}
	}
	if d.JobID == 0 {
		return nil
	}
	return &atc.TaskCacheIdentity{JobID: d.JobID}
}

// draftInputs turns the draft's input paths into the runtime.Inputs the ATC
// would hand the worker: one real artifact volume per path, created against
// the draft's team so the volumes table's foreign key onto teams is satisfied.
//
// Every path gets an artifact because that is the only kind of input
// production emits. Both producers of runtime.Input skip an input the artifact
// repository has nothing for, and the runtime refuses one that carries neither
// an Artifact nor a HangarTree, so a draft cannot describe an input without an
// artifact even to see what would happen.
func draftInputs(d ContainerDraft) ([]runtime.Input, error) {
	var inputs []runtime.Input
	for _, path := range d.Inputs {
		vol, _, err := d.Worker.CreateVolumeForArtifact(d.Ctx, d.TeamID)
		if err != nil {
			return nil, fmt.Errorf("create artifact for input %q: %w", path, err)
		}
		inputs = append(inputs, runtime.Input{Artifact: vol, DestinationPath: path})
	}
	return inputs, nil
}

// PodCreated is the state after a described container has run and its pod has
// been read back from the cluster. Check steps assert over the pod spec.
type PodCreated struct {
	Namespace string
	Ctx       context.Context
	Handle    string
	Pod       *corev1.Pod
}

// ExecClusterReady is ClusterReady plus a span recorder and an exec-mode
// executor. It is a separate state rather than a field on ClusterReady because
// the chain walk matches on nominal type: a scenario that never records spans
// should not be able to reach a span assertion.
type ExecClusterReady struct {
	Namespace string
	Worker    *jetbridge.Worker
	Clientset *fake.Clientset
	Ctx       context.Context
	Capture   SpanCapture
}

// ExecStepRunning is the exec-mode counterpart of StepRunning.
type ExecStepRunning struct {
	Namespace string
	Clientset *fake.Clientset
	Ctx       context.Context
	Handle    string
	Process   runtime.Process
	Capture   SpanCapture
}

// SpansRecorded is the terminal state for observability scenarios.
type SpansRecorded struct {
	Capture    SpanCapture
	ExitStatus int
	WaitErr    error
	Message    string
}

// VolumeSet and VolumeRead are the volume states. Note what they do NOT
// carry: no recorded exec calls, no command slice, no pod name. There is
// nothing to assert on but the artifact.
type VolumeSet struct {
	Volumes map[string]*jetbridge.Volume
	Ctx     context.Context
}

func (v VolumeSet) volume(name string) (*jetbridge.Volume, error) {
	vol, ok := v.Volumes[name]
	if !ok {
		known := make([]string, 0, len(v.Volumes))
		for n := range v.Volumes {
			known = append(known, n)
		}
		return nil, fmt.Errorf("no volume named %q (have %v)", name, known)
	}
	return vol, nil
}

// VolumeRead is the outcome of reading a volume — or of trying to. An error
// is a value here, so a scenario can assert on failure without dying.
type VolumeRead struct {
	Files   map[string]string
	Err     error
	Message string
}

func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

// TaskWorkspace is a scenario-scoped scratch directory, so two scenarios
// running the same command never share supervisor state.
type TaskWorkspace struct {
	Dir string
}

// TaskCluster is a worker whose executor really runs commands.
type TaskCluster struct {
	Namespace string
	Worker    *jetbridge.Worker
	Clientset *fake.Clientset
	Ctx       context.Context
	Workspace TaskWorkspace
}

// TaskOutcome is what the consumer saw: the build log, the exit status, and
// enough context to re-execute the same task the way a restarted web would.
type TaskOutcome struct {
	Cluster    TaskCluster
	Handle     string
	Script     string
	Log        string
	ExitStatus int
	Err        error
}

// PodNameRequest and GeneratedPodName are the pod-naming states. The seam is
// a pure function, so there is no cluster, database or double in this family.
type PodNameRequest struct {
	Metadata db.ContainerMetadata
	Handle   string
}

type GeneratedPodName struct {
	Name   string
	Handle string
}

// ResolvedConfig, ClientsetAttempt and ResourceTypeImages are the
// configuration states — all reached from exported constructors, no cluster.
type ResolvedConfig struct {
	Config jetbridge.Config
}

type ClientsetAttempt struct {
	Built   bool
	Err     error
	Message string
}

// ResourceTypeImages carries a snapshot of the built-in defaults taken before
// the merge, so a scenario can assert the shared map was not mutated.
type ResourceTypeImages struct {
	Images         map[string]string
	DefaultsBefore map[string]string
}

// RegistrarReady and RegistrationOutcome are the worker-registration states.
type RegistrarReady struct {
	Namespace string
	Clientset *fake.Clientset
	DB        JetbridgeDB
	Config    jetbridge.Config
	Registrar *jetbridge.Registrar
	Ctx       context.Context
}

type RegistrationOutcome struct {
	Ready   RegistrarReady
	Worker  db.Worker
	Err     error
	Message string
}

// WatchedPod and WatchObservation are the pod-watch states. Feed and
// SecondFeed are client-go's own controllable watch fakes — real
// implementations of watch.Interface, used to make a connection drop
// deterministic rather than to record calls.
type WatchedPod struct {
	Name       string
	Clientset  *fake.Clientset
	Pod        *corev1.Pod
	Ctx        context.Context
	Watcher    *jetbridge.PodWatcher
	Feed       *watch.RaceFreeFakeWatcher
	SecondFeed *watch.RaceFreeFakeWatcher
	Version    int
	// Replay is shared by pointer because the watch reactor writes into it
	// after this struct has already been copied down the chain.
	Replay *WatchReplay
}

type WatchObservation struct {
	Watched WatchedPod
	Pod     *corev1.Pod
	Err     error
	Message string
}

// ReaperReady and ReaperOutcome are the garbage-collection states.
type ReaperReady struct {
	DB          JetbridgeDB
	Worker      db.Worker
	Clientset   *fake.Clientset
	Config      jetbridge.Config
	Reaper      *jetbridge.Reaper
	Ctx         context.Context
	BuildLookup bool
}

type ReaperOutcome struct {
	Ready ReaperReady
	Err   error
}

// VolumeIdentity is the state for a volume's identity and its database row.
type VolumeIdentity struct {
	Volume       *jetbridge.Volume
	DaemonVolume *jetbridge.DaemonSetVolume
	DBHandle     string
	WorkerName   string
}
