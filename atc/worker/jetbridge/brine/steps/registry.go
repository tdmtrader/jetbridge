package steps

import (
	"fmt"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
)

// Definitions is the step registry: the executable half of the behavioral
// contract in ../features/.
func Definitions() []brine.StepDefinition {
	defs := failureDefinitions()
	defs = append(defs, ContainerSpecDefinitions()...)
	defs = append(defs, ObservabilityDefinitions()...)
	defs = append(defs, VolumeStreamingDefinitions()...)
	defs = append(defs, PlaceholderVolumeDefinitions()...)
	defs = append(defs, VolumeRouteDefinitions()...)
	defs = append(defs, ArtifactHandoffDefinitions()...)
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
	defs = append(defs, MirrorClientDefinitions()...)
	defs = append(defs, ContainerLifecycleDefinitions()...)
	defs = append(defs, ObservabilityExtraDefinitions()...)
	defs = append(defs, AttachDefinitions()...)
	defs = append(defs, ObservabilityMetricDefinitions()...)
	defs = append(defs, IntegrationDefinitions()...)
	defs = append(defs, ProcessDefinitions()...)
	defs = append(defs, InitContainerDefinitions()...)
	defs = append(defs, ContainerExtraDefinitions()...)
	defs = append(defs, VolumeIdentityDefinitions()...)
	defs = append(defs, RemoteArtifactDefinitions()...)
	defs = append(defs, SidecarLogDefinitions()...)
	defs = append(defs, LiveExecDefinitions()...)
	defs = append(defs, PauseRecoveryDefinitions()...)
	defs = append(defs, S3PutDefinitions()...)
	defs = append(defs, LivePeerReadDefinitions()...)
	defs = append(defs, ClosingDefinitions()...)
	defs = append(defs, CacheStorageDefinitions()...)
	defs = append(defs, SeveredExecDefinitions()...)
	defs = append(defs, PodNameSegmentDefinitions()...)
	defs = append(defs, CancelledExecDefinitions()...)
	defs = append(defs, HijackCancellationDefinitions()...)
	defs = append(defs, ResourceProtocolDefinitions()...)
	defs = append(defs, GitResourceDefinitions()...)
	defs = append(defs, ConfigCompletenessDefinitions()...)
	defs = append(defs, RegistrarIdentityDefinitions()...)
	defs = append(defs, ReaperLookupFailureDefinitions()...)
	defs = append(defs, ReaperGapDefinitions()...)
	defs = append(defs, WorkerArtifactKeyDefinitions()...)
	defs = append(defs, ContainerGapDefinitions()...)
	defs = append(defs, ProcessGapDefinitions()...)
	defs = append(defs, TTYDefinitions()...)
	defs = append(defs, HijackOptionDefinitions()...)

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
	defs = append(defs, ReviewDefinitions()...)
	defs = append(defs, ReviewWorkerDefinitions()...)
	defs = append(defs, ReviewSubscriptionDefinitions()...)
	defs = append(defs, ReviewHandoffDefinitions()...)
	defs = append(defs, HangarFixtureDefinitions()...)
	defs = append(defs, HangarCapturePodDefinitions()...)
	defs = append(defs, HangarHandoffDefinitions()...)
	defs = append(defs, HangarPausePodDefinitions()...)
	defs = append(defs, HangarPublicationDefinitions()...)
	defs = append(defs, HangarDispositionDefinitions()...)
	defs = append(defs, HangarBindingDefinitions()...)
	defs = append(defs, HangarReadLifecycleDefinitions()...)
	defs = append(defs, HangarRecoveryDefinitions()...)
	defs = append(defs, HangarAdmissionDefinitions()...)
	defs = append(defs, RunResultDeclarationDefinitions()...)
	defs = append(defs, RunInputDeclarationDefinitions()...)
	defs = append(defs, RunInvocationReplayDefinitions()...)
	defs = append(defs, RunInputBindingDefinitions()...)
	defs = append(defs, RunTaskInputDefinitions()...)
	defs = append(defs, RunInputDeliveryDefinitions()...)
	defs = append(defs, RunTaskEngineDefinitions()...)
	defs = append(defs, RunSealedInputDefinitions()...)
	defs = append(defs, RunInputUploadDefinitions()...)
	defs = append(defs, RunInputRegistrationDefinitions()...)
	defs = append(defs, RunInputIntakeDefinitions()...)
	defs = append(defs, RunInputAPIDefinitions()...)
	defs = append(defs, RunInvocationAPIDefinitions()...)
	defs = append(defs, BoundSessionDefinitions()...)
	defs = append(defs, RunCredentialDefinitions()...)
	defs = append(defs, ReviewSubmitDefinitions()...)
	defs = append(defs, ReviewKubeletDefinitions()...)
	defs = append(defs, ReviewKubeletSubmitDefinitions()...)
	defs = append(defs, RunDefinitionDefinitions()...)
	defs = append(defs, RunOutputStartDefinitions()...)
	defs = append(defs, RunOutputFinishDefinitions()...)
	defs = append(defs, RunOutputReleaseDefinitions()...)
	defs = append(defs, RunOutputReservationDefinitions()...)
	defs = append(defs, RunOutputRuntimeDefinitions()...)
	defs = append(defs, RunOutputCandidateDefinitions()...)
	defs = append(defs, RunResultDefinitions()...)
	defs = append(defs, RunResultAPIDefinitions()...)
	defs = append(defs, RunResultDownloadDefinitions()...)
	defs = append(defs, RunReviewClientDefinitions()...)
	defs = append(defs, RunReviewResultDefinitions()...)
	defs = append(defs, HangarManagedReadDefinitions()...)
	defs = append(defs, HangarNodeIdentityDefinitions()...)
	defs = append(defs, RunCancellationDefinitions()...)
	defs = append(defs, RunCancellationBoundaryDefinitions()...)
	defs = append(defs, RunCancellationAPIDefinitions()...)
	defs = append(defs, ExecutionStopDefinitions()...)
	defs = append(defs, CancellationLeaseDefinitions()...)
	defs = append(defs, CancellationQueueDefinitions()...)
	defs = append(defs, CancellationSourceProofDefinitions()...)
	defs = append(defs, CancellationPassDefinitions()...)
	defs = append(defs, CancellationClassificationDefinitions()...)
	defs = append(defs, CancellationSourcesDefinitions()...)
	defs = append(defs, RunCancellationFinalityDefinitions()...)
	defs = append(defs, ExecutionOutcomeRecoveryDefinitions()...)
	defs = append(defs, RunExecutionAdmissionDefinitions()...)
	defs = append(defs, RunExecutionRuntimeDefinitions()...)
	defs = append(defs, AuthenticationDefinitions()...)
	defs = append(defs, MCPAuthenticationDefinitions()...)
	defs = append(defs, MCPOperationDefinitions()...)
	defs = append(defs, MCPBoundaryDefinitions()...)
	defs = append(defs, MCPReferenceDefinitions()...)
	return defs
}

func failureDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

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
		Assert[StepOutcome](
			"the failure does not mention {string}",
			func(in StepOutcome, args Args) error {
				unwanted := args.String(0)

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
