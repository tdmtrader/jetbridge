package steps

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (d *noopDelegate) BuildStartTime() time.Time { return time.Time{} }

// Definitions is the step registry: the executable half of the behavioral
// contract in ../features/.
func Definitions() []brine.StepDefinition {
	defs := failureDefinitions()
	defs = append(defs, ContainerSpecDefinitions()...)
	defs = append(defs, ObservabilityDefinitions()...)
	defs = append(defs, VolumeStreamingDefinitions()...)
	defs = append(defs, TaskCommandDefinitions()...)
	defs = append(defs, PodNameDefinitions()...)
	defs = append(defs, ConfigDefinitions()...)
	defs = append(defs, RegistrarDefinitions()...)
	defs = append(defs, PodWatchDefinitions()...)
	defs = append(defs, ReaperDefinitions()...)
	defs = append(defs, ContainerPodDefinitions()...)
	defs = append(defs, ClusterConfigDefinitions()...)
	defs = append(defs, PodFailureDefinitions()...)
	defs = append(defs, WorkerDefinitions()...)
	defs = append(defs, DaemonDefinitions()...)
	defs = append(defs, ContainerLifecycleDefinitions()...)
	defs = append(defs, ObservabilityExtraDefinitions()...)
	defs = append(defs, AttachDefinitions()...)
	defs = append(defs, ObservabilityMetricDefinitions()...)
	defs = append(defs, IntegrationDefinitions()...)
	defs = append(defs, ProcessDefinitions()...)
	defs = append(defs, InitContainerDefinitions()...)
	defs = append(defs, ContainerExtraDefinitions()...)
	defs = append(defs, VolumeIdentityDefinitions()...)
	defs = append(defs, VolumeGapDefinitions()...)
	defs = append(defs, SidecarLogDefinitions()...)
	defs = append(defs, ClosingDefinitions()...)
	defs = append(defs, CacheStorageDefinitions()...)
	defs = append(defs, SeveredExecDefinitions()...)
	defs = append(defs, PodNameSegmentDefinitions()...)
	defs = append(defs, CancelledExecDefinitions()...)
	defs = append(defs, ConfigCompletenessDefinitions()...)
	defs = append(defs, RegistrarIdentityDefinitions()...)
	defs = append(defs, PodWatchFidelityDefinitions()...)
	defs = append(defs, ReaperLookupFailureDefinitions()...)
	defs = append(defs, ReaperGapDefinitions()...)
	defs = append(defs, WorkerArtifactKeyDefinitions()...)
	defs = append(defs, ContainerGapDefinitions()...)
	defs = append(defs, ProcessGapDefinitions()...)
	defs = append(defs, TTYDefinitions()...)
	defs = append(defs, ExecTargetDefinitions()...)
	defs = append(defs, PodWatchRealDefinitions()...)
	defs = append(defs, PodWatchRealExtraDefinitions()...)
	defs = append(defs, DaemonMTLSDefinitions()...)
	defs = append(defs, RealDaemonDefinitions()...)
	defs = append(defs, ArtifactRecordingDefinitions()...)
	defs = append(defs, DaemonDurableDefinitions()...)
	defs = append(defs, DaemonContainmentDefinitions()...)
	defs = append(defs, DaemonMirroringDefinitions()...)
	defs = append(defs, DaemonCrossNodeDefinitions()...)
	defs = append(defs, GCReclamationDefinitions()...)
	defs = append(defs, GCPipelineDefinitions()...)
	defs = append(defs, GCCacheDefinitions()...)
	defs = append(defs, GCContainerDefinitions()...)
	defs = append(defs, ResourceCheckingDefinitions()...)
	defs = append(defs, StepExecutionDefinitions()...)
	defs = append(defs, BuildSchedulingDefinitions()...)
	defs = append(defs, JobAdmissionDefinitions()...)
	defs = append(defs, PipelineRetentionDefinitions()...)
	defs = append(defs, PipelineAPIDefinitions()...)
	defs = append(defs, APIConfigStrictDefinitions()...)
	defs = append(defs, AccessorDefinitions()...)
	defs = append(defs, RunStateStrictDefinitions()...)
	defs = append(defs, DBResourceRemainingStrictDefinitions()...)
	defs = append(defs, DBContainerRepositoryStrictDefinitions()...)
	defs = append(defs, DBContainerOrphansStrictDefinitions()...)
	defs = append(defs, DBCheckFactoryStrictDefinitions()...)
	defs = append(defs, FlyTeamErrorDefinitions()...)
	defs = append(defs, APIAuthDefinitions()...)
	defs = append(defs, APIResourceAuthDefinitions()...)
	defs = append(defs, ConfigValidationDefinitions()...)
	defs = append(defs, ConfigvalidateBehaviorDefinitions()...)
	defs = append(defs, TaskConfigBehaviorDefinitions()...)
	defs = append(defs, TaskConfigSourceStrictDefinitions()...)
	defs = append(defs, IDTokenGeneratorStrictDefinitions()...)
	defs = append(defs, VariableTemplateDefinitions()...)
	defs = append(defs, StaticVariableDefinitions()...)
	defs = append(defs, VariableReferenceDefinitions()...)
	defs = append(defs, VariableSourceDefinitions()...)
	defs = append(defs, BuildLogRetentionDefinitions()...)
	defs = append(defs, OPAResultDefinitions()...)
	defs = append(defs, ATCConfigBehaviorDefinitions()...)
	defs = append(defs, ConfigDiffBehaviorDefinitions()...)
	defs = append(defs, FlyEventRenderDefinitions()...)
	defs = append(defs, TeamDomainDefinitions()...)
	defs = append(defs, DBTeamStrictDefinitions()...)
	defs = append(defs, BuildDomainDefinitions()...)
	defs = append(defs, DBBuildStrictDefinitions()...)
	defs = append(defs, DBBuildRemainingStrictDefinitions()...)
	defs = append(defs, DBVolumeRepositoryStrictDefinitions()...)
	defs = append(defs, APIResourcesStrictDefinitions()...)
	defs = append(defs, APIWorkersStrictDefinitions()...)
	defs = append(defs, BuildFactoryBehaviorDefinitions()...)
	defs = append(defs, ComponentNotificationDefinitions()...)
	defs = append(defs, ContainerDomainDefinitions()...)
	defs = append(defs, NotificationBusDomainDefinitions()...)
	defs = append(defs, AuditRoutingDefinitions()...)
	defs = append(defs, OTelMetricDefinitions()...)
	defs = append(defs, MetricPrimitiveDefinitions()...)
	defs = append(defs, RuntimeMetricDefinitions()...)
	defs = append(defs, ResourceClientJourneyDefinitions()...)
	defs = append(defs, InMemoryBuildDefinitions()...)
	defs = append(defs, ResourceScopeDomainDefinitions()...)
	defs = append(defs, ResourceTypeDomainDefinitions()...)
	defs = append(defs, PipelineClientDefinitions()...)
	defs = append(defs, JobClientStrictDefinitions()...)
	defs = append(defs, APIJobsRemainingStrictDefinitions()...)
	defs = append(defs, BuildClientDefinitions()...)
	defs = append(defs, APIBuildsRemainingStrictDefinitions()...)
	defs = append(defs, APIBuildsNextStrictDefinitions()...)
	defs = append(defs, ConfigClientDefinitions()...)
	defs = append(defs, EventParserDefinitions()...)
	defs = append(defs, SidecarBehaviorDefinitions()...)
	defs = append(defs, JobConfigBehaviorDefinitions()...)
	defs = append(defs, CoreValueDefinitions()...)
	defs = append(defs, ContainerLimitsStrictDefinitions()...)
	defs = append(defs, PipelineRefDefinitions()...)
	defs = append(defs, PipelineRefStrictDefinitions()...)
	defs = append(defs, IdentifierValidationDefinitions()...)
	defs = append(defs, PipelineDomainDefinitions()...)
	defs = append(defs, JobDomainDefinitions()...)
	defs = append(defs, WorkerFactoryDomainDefinitions()...)
	defs = append(defs, TeamClientDefinitions()...)
	defs = append(defs, TeamClientStrictDefinitions()...)
	defs = append(defs, CCAPIStrictDefinitions()...)
	defs = append(defs, WallAPIStrictDefinitions()...)
	defs = append(defs, ClearTaskCacheAPIStrictDefinitions()...)
	defs = append(defs, JobBuildGuardsStrictDefinitions()...)
	defs = append(defs, UsersAPIDefinitions()...)
	defs = append(defs, CLIAPIDefinitions()...)
	defs = append(defs, SmallClientDefinitions()...)
	defs = append(defs, SchedulerAlgorithmStrictDefinitions()...)
	defs = append(defs, DBTeamRemainingStrictDefinitions()...)
	defs = append(defs, DBTeamBuildListStrictDefinitions()...)
	defs = append(defs, DBJobInputOutputStrictDefinitions()...)
	defs = append(defs, DBJobRerunStrictDefinitions()...)
	defs = append(defs, DBJobTaskCacheStrictDefinitions()...)
	defs = append(defs, DBJobBuildSelectionStrictDefinitions()...)
	defs = append(defs, DBVolumeCoreStrictDefinitions()...)
	defs = append(defs, DBPipelineQueryStrictDefinitions()...)
	defs = append(defs, VersionsAPIStrictDefinitions()...)
	defs = append(defs, ResourceVersionsClientStrictDefinitions()...)
	defs = append(defs, DBPipelineLifecycleStrictDefinitions()...)
	defs = append(defs, PipelineAPIStateStrictDefinitions()...)
	defs = append(defs, APIPipelinesNextStrictDefinitions()...)
	defs = append(defs, ContainersAPIStateStrictDefinitions()...)
	defs = append(defs, DBResourceCacheLifecycleStrictDefinitions()...)
	defs = append(defs, FlyRCTargetsStrictDefinitions()...)
	return defs
}

func failureDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// Empty -> ClusterReady. Uses the scenario-scoped database.
		brine.DefineMapUsing[brine.Empty, ClusterReady](
			"a jetbridge worker on a fake Kubernetes cluster",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (ClusterReady, error) {
				cluster, err := NewCluster(res)
				if err != nil {
					return ClusterReady{}, err
				}
				return cluster.Ready(), nil
			},
		),

		// ClusterReady -> StepRunning.
		brine.DefineMap[ClusterReady, StepRunning](
			"a task container {string} is running",
			func(in ClusterReady, p brine.Params, _ *brine.Recorder) (StepRunning, error) {
				handle, ok := p.GetString(0)
				if !ok {
					return StepRunning{}, fmt.Errorf("expected a container handle parameter")
				}

				container, _, err := in.Worker.FindOrCreateContainer(
					in.Ctx,
					db.NewFixedHandleContainerOwner(handle),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID:    1,
						ImageSpec: runtime.ImageSpec{ImageURL: "busybox"},
					},
					&noopDelegate{},
				)
				if err != nil {
					return StepRunning{}, fmt.Errorf("find or create container %q: %w", handle, err)
				}

				stderr := new(bytes.Buffer)
				process, err := container.Run(in.Ctx,
					runtime.ProcessSpec{Path: "/bin/sh"},
					runtime.ProcessIO{Stderr: stderr},
				)
				if err != nil {
					return StepRunning{}, fmt.Errorf("run container %q: %w", handle, err)
				}

				return StepRunning{
					Namespace: in.Namespace,
					Clientset: in.Clientset,
					Ctx:       in.Ctx,
					Handle:    handle,
					Process:   process,
					Stderr:    stderr,
				}, nil
			},
		),

		// StepRunning -> StepOutcome. Drives the pod into a failure shape and
		// waits, so the outcome is what a real consumer of Process.Wait sees.
		brine.DefineMap[StepRunning, StepOutcome](
			"the pod is {string} with waiting reason {string} and last terminated reason {string}",
			func(in StepRunning, p brine.Params, _ *brine.Recorder) (StepOutcome, error) {
				phase, ok := p.GetString(0)
				if !ok {
					return StepOutcome{}, fmt.Errorf("expected a pod phase parameter")
				}
				waiting, ok := p.GetString(1)
				if !ok {
					return StepOutcome{}, fmt.Errorf("expected a waiting reason parameter")
				}
				lastTerminated, ok := p.GetString(2)
				if !ok {
					return StepOutcome{}, fmt.Errorf("expected a last terminated reason parameter")
				}

				pods := in.Clientset.CoreV1().Pods(in.Namespace)
				pod, err := pods.Get(in.Ctx, in.Handle, metav1.GetOptions{})
				if err != nil {
					return StepOutcome{}, fmt.Errorf("get pod %q: %w", in.Handle, err)
				}

				status := corev1.ContainerStatus{Name: "main"}
				if waiting != "none" {
					status.State.Waiting = &corev1.ContainerStateWaiting{
						Reason:  waiting,
						Message: waitingMessageFor(waiting),
					}
				}
				if lastTerminated != "none" {
					status.RestartCount = 2
					status.LastTerminationState.Terminated = &corev1.ContainerStateTerminated{
						Reason:   lastTerminated,
						ExitCode: 137,
					}
				}

				pod.Status.Phase = corev1.PodPhase(phase)
				pod.Status.ContainerStatuses = []corev1.ContainerStatus{status}
				if _, err := pods.UpdateStatus(in.Ctx, pod, metav1.UpdateOptions{}); err != nil {
					return StepOutcome{}, fmt.Errorf("update pod status: %w", err)
				}

				_, waitErr := in.Process.Wait(in.Ctx)
				message := ""
				if waitErr != nil {
					message = waitErr.Error()
				}
				return StepOutcome{Err: waitErr, Message: message, Stderr: in.Stderr.String()}, nil
			},
		),

		// Terminal checks over StepOutcome. A step that succeeded has no
		// failure to name, which the getter reports rather than comparing an
		// empty message.
		CheckContains[StepOutcome]("the step fails naming {string}",
			"the failure",
			func(in StepOutcome) (string, error) {
				if in.Err == nil {
					return "", fmt.Errorf("expected the step to fail, but it succeeded")
				}
				return in.Message, nil
			}),

		// Keeps its own body: the assertion is that the text appears NOWHERE in
		// the message. CheckNotMember is the negative combinator, but it
		// negates membership by element EQUALITY over a collection, so it would
		// pass on a message that merely contains the unwanted reason inside a
		// longer string — which is the case this exists to catch. It also
		// asserts the step failed at all, which is a second thing.
		brine.DefineCheck[StepOutcome](
			"the failure does not mention {string}",
			func(in StepOutcome, p brine.Params, _ *brine.Recorder) error {
				unwanted, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected an unwanted-reason parameter")
				}
				if in.Err == nil {
					return fmt.Errorf("expected the step to have failed, but it succeeded")
				}
				if strings.Contains(in.Message, unwanted) {
					return fmt.Errorf("expected the failure not to mention %q, got %q", unwanted, in.Message)
				}
				return nil
			},
		),
	}
}

// waitingMessageFor mirrors the kubelet messages the ginkgo tests used, so the
// pod shapes the scenarios build are the ones the runtime actually sees.
func waitingMessageFor(reason string) string {
	switch reason {
	case "CrashLoopBackOff":
		return "back-off 10s restarting failed container"
	case "ImagePullBackOff":
		return "Back-off pulling image"
	default:
		return reason
	}
}
