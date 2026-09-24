// Package steps hosts the brine step registry for the jetbridge runtime's
// behavioral contract.
//
// Every step is a transition between NAMED DOMAIN
// STATES. The chain walk keeps a SINGLE live state and replaces it wholesale
// on each map step, so a state must carry forward everything its successors
// need. `brine check` verifies each scenario's path without running anything.
package steps

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// StepRunning carries a process handle after Run creates its API pod. This
// does not imply that a kubelet started a command: API-backed status fixtures
// report input state explicitly. Other families use actual live Kubernetes execution.
type StepRunning struct {
	Namespace string
	Clientset kubernetes.Interface
	Ctx       context.Context
	Handle    string
	Process   runtime.Process
	Stderr    *bytes.Buffer
}

// StepOutcome is the terminal state: what the process reported when it was
// waited on. Check steps read this and cannot transition out of it.
type StepOutcome struct {
	schedulingRefusal string
	Err               error
	Message           string
	Stderr            string
	// ExitStatus is what the step actually reports. Process.Wait returns a
	// non-zero exit as a RESULT, not an error, so Err alone cannot tell a
	// failed task from a successful one.
	ExitStatus int
}

// ContainerDraft is a container spec under description. Map steps that refine
// the draft take ContainerDraft in and out — the live state's type is
// unchanged, so any number of them may appear in any order before the
// container runs.
type ContainerDraft struct {
	Namespace string
	Worker    *jetbridge.Worker
	Clientset kubernetes.Interface
	// MountExecutor is the transport used when creating deferred volumes.
	// Real worker drafts retain the production SPDY executor.
	MountExecutor jetbridge.PodExecutor
	// workerWith builds the draft's worker again with a different executor.
	// A worker takes its executor at construction, so a draft that hands the
	// task a transport replaces Worker rather than changing it.
	workerWith   func(jetbridge.PodExecutor) *jetbridge.Worker
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
	Inputs       []string
	Outputs      []string
	NamedOutputs map[string]string
	// CacheIdentityOmitted keeps metadata independent of the optional runtime key.
	CacheIdentityOmitted bool
	Caches               []string
	Scratch              []string
	LimitCPU             *uint64
	LimitMemory          *uint64
	RequestCPU           *uint64
	RequestMemory        *uint64
	LimitEphemeral       *uint64
	RequestEphemeral     *uint64
	JobID                int
	StepName             string

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
	// TeamID carried from WorkerReady, so artifact volumes satisfy the
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
	if d.CacheIdentityOmitted {
		return nil
	}
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
	Process   runtime.Process
}

// ExecStepRunning is the exec-mode counterpart of StepRunning.
type ExecStepRunning struct {
	live       *liveStartup
	Namespace  string
	Clientset  kubernetes.Interface
	Ctx        context.Context
	Handle     string
	Process    runtime.Process
	Capture    SpanCapture
	watchReady <-chan struct{}
	recorder   *brine.Recorder
}

// SpansRecorded is the terminal state for observability scenarios.
type SpansRecorded struct {
	live       *liveStartup
	Capture    SpanCapture
	ExitStatus int
	WaitErr    error
	Message    string
}

// VolumeSet owns streaming handles and passive observations of real exec requests.
type VolumeSet struct {
	Volumes   map[string]*jetbridge.Volume
	Ctx       context.Context
	live      *liveKubernetes
	execTrace *volumeExecObservation
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
	Files        map[string]string
	Err          error
	Message      string
	readAttempts []volumeReadAttempt
	source       *VolumeSet
	remote       *remoteArtifactRead
}

type volumeReadAttempt struct {
	encoding                   string
	openErr, readErr, closeErr error
}

func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

// TaskWorkspace is a scenario-scoped scratch directory, so two scenarios
// running the same command never share supervisor state.
type TaskWorkspace struct {
	Dir string
	// Disposal follows the allocation, not a potentially changed public Dir.
	ownedDir string
}

// TaskCluster runs supervised commands in a real Kubernetes pod.
// Its filesystem and lifetime belong to the scenario namespace.
type TaskCluster struct {
	WorkerReady
	metadata    db.ContainerMetadata
	image       string
	application *taskApplication
	execTrace   *execObservation
	directory   string
	outputs     runtime.OutputPaths
}

// TaskOutcome is what the consumer saw: the build log, the exit status, and
// enough context to re-execute the same task the way a restarted web would.
type TaskOutcome struct {
	PodStartupDuration float64
	Cluster            TaskCluster
	Handle             string
	Script             string
	Log                string
	ExitStatus         int
	Err                error
	Message            string
	Container          runtime.Container

	Props     map[string]string
	Pods      []string
	PodLabels map[string]string

	AttachErr     error
	AttachMessage string

	// Preserve the actual completed runtime object across fresh-handle recovery.
	completedContainer runtime.Container
	completedPodUID    types.UID
	mounts             []runtime.VolumeMount
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
	Clientset kubernetes.Interface
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

// ReaperReady and ReaperOutcome are the garbage-collection states.
type ReaperReady struct {
	DB          JetbridgeDB
	Worker      db.Worker
	Clientset   kubernetes.Interface
	Config      jetbridge.Config
	Reaper      *jetbridge.Reaper
	Ctx         context.Context
	BuildLookup bool
	RacePod     *corev1.Pod
}

type ReaperOutcome struct {
	Ready ReaperReady
	Err   error
}

// VolumeIdentity retains independently persisted volumes and their artifact links.
type VolumeIdentity struct {
	Volumes      []VolumeIdentityRow
	DaemonVolume *jetbridge.DaemonSetVolume
	WorkerName   string
	TeamID       int
}

type VolumeIdentityRow struct {
	Volume   *jetbridge.Volume
	DBVolume db.CreatedVolume
	DBHandle string
	Artifact db.WorkerArtifact
}

// errorMessage preserves the error snapshot each outcome records, including
// the empty message for success. Call it where the original guard ran.
func errorMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
