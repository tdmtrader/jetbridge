package atccmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/experiment"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/workflowrun"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
)

type experimentWorkflowRunStore interface {
	Get(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error)
	Snapshots(context.Context, snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error)
}

type experimentRunObserver struct {
	runs experimentWorkflowRunStore
}

func (cmd *RunCommand) agentExperimentBudgetConfig() db.AgentExperimentBudgetConfig {
	return db.AgentExperimentBudgetConfig{
		GlobalDailyCapUSD: cmd.AgentDailyBudgetUSD,
		Location:          time.Local,
		Now:               time.Now,
	}
}

func (cmd *RunCommand) newAgentExperimentsFactory(
	dbConn db.DbConn,
	targetRenderer workflowrun.WorkflowTargetRenderer,
) db.AgentExperimentsFactory {
	return db.NewAgentExperimentsFactory(
		dbConn,
		targetRenderer,
		db.WithAgentExperimentBudgetConfig(cmd.agentExperimentBudgetConfig()),
	)
}

func (observer experimentRunObserver) Inspect(
	ctx context.Context,
	teamID int,
	id snapshot.WorkflowRunID,
) (experiment.RunObservation, bool, error) {
	if observer.runs == nil {
		return experiment.RunObservation{}, false, fmt.Errorf("experiment run observer: workflow-run store is required")
	}
	run, found, err := observer.runs.Get(ctx, teamID, id)
	if err != nil || !found {
		return experiment.RunObservation{}, found, err
	}
	switch run.Status {
	case db.AgentWorkflowRunStatusAdmitting:
		return experiment.RunObservation{Status: experiment.ObservedRunPending}, true, nil
	case db.AgentWorkflowRunStatusRunning, db.AgentWorkflowRunStatusCanceling:
		return experiment.RunObservation{Status: experiment.ObservedRunRunning}, true, nil
	case db.AgentWorkflowRunStatusFailed,
		db.AgentWorkflowRunStatusErrored,
		db.AgentWorkflowRunStatusAborted:
		failure := experiment.ObservedPlatformFailure
		if run.Status == db.AgentWorkflowRunStatusFailed &&
			run.ErrorMessage == workflowrun.OutputContractMismatchReason {
			failure = experiment.ObservedContractFailure
		}
		return experiment.RunObservation{
			Status: experiment.ObservedRunFailed, Failure: failure,
		}, true, nil
	case db.AgentWorkflowRunStatusSucceeded:
	default:
		return experiment.RunObservation{
			Status: experiment.ObservedRunFailed, Failure: experiment.ObservedPlatformFailure,
		}, true, nil
	}

	bindings, err := observer.runs.Snapshots(ctx, id)
	if err != nil {
		return experiment.RunObservation{}, false, err
	}
	outputs := make(map[string]snapshot.SnapshotRef)
	for _, binding := range bindings {
		if binding.Direction != db.AgentWorkflowRunSnapshotOutput {
			continue
		}
		if strings.TrimSpace(binding.PortName) == "" || binding.Snapshot.Validate() != nil {
			return experiment.RunObservation{
				Status: experiment.ObservedRunFailed, Failure: experiment.ObservedContractFailure,
			}, true, nil
		}
		if _, exists := outputs[binding.PortName]; exists {
			return experiment.RunObservation{
				Status: experiment.ObservedRunFailed, Failure: experiment.ObservedContractFailure,
			}, true, nil
		}
		outputs[binding.PortName] = binding.Snapshot
	}
	return experiment.RunObservation{Status: experiment.ObservedRunSucceeded, Outputs: outputs}, true, nil
}

type experimentSnapshotMetadata interface {
	GetAuthorized(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error)
}

type experimentSnapshotContent interface {
	Open(context.Context, snapshot.Snapshot) (io.ReadCloser, error)
}

type experimentMeasurementsReader struct {
	metadata   experimentSnapshotMetadata
	content    experimentSnapshotContent
	validators snapshot.ValidatorRegistry
	limits     snapshot.ArchiveLimits
	tempDir    string
}

func (reader experimentMeasurementsReader) ReadMeasurements(
	ctx context.Context,
	teamID int,
	id snapshot.SnapshotID,
) (contracts.MeasurementsDocument, bool, error) {
	if reader.metadata == nil || reader.content == nil || reader.validators == nil {
		return contracts.MeasurementsDocument{}, false, fmt.Errorf("experiment measurements reader: snapshot dependencies are required")
	}
	manifest, found, err := reader.metadata.GetAuthorized(ctx, teamID, id)
	if err != nil || !found {
		return contracts.MeasurementsDocument{}, found, err
	}
	if manifest.Validate() != nil ||
		manifest.Type != snapshot.TypeRef("measurements/v1") ||
		manifest.ContentState != snapshot.ContentStateAvailable {
		return contracts.MeasurementsDocument{}, false, nil
	}
	stream, err := reader.content.Open(ctx, manifest)
	if err != nil {
		if errors.Is(err, snapshot.ErrContentUnavailable) || errors.Is(err, snapshot.ErrExpired) {
			return contracts.MeasurementsDocument{}, false, nil
		}
		return contracts.MeasurementsDocument{}, false, err
	}
	if stream == nil {
		return contracts.MeasurementsDocument{}, false, fmt.Errorf("experiment measurements reader: content store returned no stream")
	}
	defer stream.Close()

	tree, err := (snapshot.Canonicalizer{
		MaxContentBytes: reader.limits.MaxContentBytes,
		MaxEntries:      reader.limits.MaxEntries,
		TempDir:         reader.tempDir,
	}).Capture(ctx, stream)
	if err != nil {
		if ctx.Err() != nil {
			return contracts.MeasurementsDocument{}, false, ctx.Err()
		}
		return contracts.MeasurementsDocument{}, false, nil
	}
	defer tree.Close()
	if tree.Digest != manifest.Digest || tree.ByteSize != manifest.ByteSize || tree.FileCount != manifest.FileCount {
		return contracts.MeasurementsDocument{}, false, nil
	}

	root, err := os.OpenRoot(tree.Root)
	if err != nil {
		return contracts.MeasurementsDocument{}, false, err
	}
	defer root.Close()
	if validator, err := reader.validators.Lookup(manifest.Type); err != nil || validator == nil {
		return contracts.MeasurementsDocument{}, false, nil
	}
	document, err := decodeExperimentMeasurements(ctx, root)
	if err != nil {
		return contracts.MeasurementsDocument{}, false, nil
	}
	return document, true, nil
}

func decodeExperimentMeasurements(ctx context.Context, root *os.Root) (contracts.MeasurementsDocument, error) {
	record, err := contracts.ReadSealedMeasurementsRecord(ctx, root)
	if err != nil {
		return contracts.MeasurementsDocument{}, err
	}
	return record.Body, nil
}

func (cmd *RunCommand) agentExperimentComponents(
	dbConn db.DbConn,
	lockFactory lock.LockFactory,
	teamFactory db.TeamFactory,
	buildFactory db.BuildFactory,
	pipelineRunFactory db.PipelineRunFactory,
) ([]RunnableComponent, error) {
	if !cmd.AgentExperiments.Enabled {
		return nil, nil
	}
	cmd.agentSnapshotMu.Lock()
	metadata := cmd.agentSnapshotMetadataStore
	content := cmd.agentSnapshotContentStore
	validators := cmd.agentSnapshotValidatorRegistry
	limits := cmd.agentSnapshotArchiveLimits
	cmd.agentSnapshotMu.Unlock()
	if metadata == nil || isNilDependency(content) || validators == nil || limits == (snapshot.ArchiveLimits{}) {
		return nil, fmt.Errorf("construct experiment runtime: snapshot storage is unavailable")
	}

	mainTeam, found, err := teamFactory.FindTeam(atc.DefaultTeamName)
	if err != nil {
		return nil, fmt.Errorf("construct experiment runtime: resolve main team: %w", err)
	}
	if !found || mainTeam == nil || mainTeam.ID() <= 0 || mainTeam.Name() != atc.DefaultTeamName {
		return nil, fmt.Errorf("construct experiment runtime: main team is unavailable")
	}

	workflowStore := db.NewAgentWorkflowsFactory(dbConn)
	runStore := db.NewAgentWorkflowRunsFactory(dbConn)
	templateSaver, err := workflowrun.NewTemplateSaver(
		teamFactory,
		db.NewWorkflowRunTemplateFactory(dbConn, lockFactory),
	)
	if err != nil {
		return nil, fmt.Errorf("construct experiment template saver: %w", err)
	}
	// Each experiment cell already holds the atomic shared-budget reservation
	// that covers its candidate and evaluator runs. Reserving each child again
	// in the generic workflow binder would double-count the same liability.
	workflowBudget := workflowrun.AllowAllBudgetAdmitter{}
	workflowSecrets, err := workflowrun.NewVaultedRunSecretPreparer(
		db.NewAgentUserLookup(dbConn),
		db.NewAgentUserCredentialsFactory(dbConn),
		cmd.agentRunSecrets(),
	)
	if err != nil {
		return nil, fmt.Errorf("construct experiment secret preparation: %w", err)
	}
	targetRenderer := workflowrun.WorkflowTargetRenderer{RuntimeImage: cmd.AgentStepImage}
	binder, err := workflowrun.NewBinder(
		workflowrun.WorkflowDefinitionStoreResolver{Store: workflowStore},
		targetRenderer,
		metadata,
		runStore,
		workflowBudget,
		templateSaver,
		pipelineRunFactory,
		workflowSecrets,
	)
	if err != nil {
		return nil, fmt.Errorf("construct experiment workflow binder: %w", err)
	}
	experimentBinder, err := workflowrun.NewExperimentBinderAdapter(binder)
	if err != nil {
		return nil, err
	}
	experimentStore := cmd.newAgentExperimentsFactory(dbConn, targetRenderer)
	runner, err := experiment.NewRunner(experimentStore, experimentBinder, experiment.RunnerConfig{
		MaxConcurrency: cmd.AgentExperiments.MaxConcurrency,
	})
	if err != nil {
		return nil, fmt.Errorf("construct experiment runner: %w", err)
	}
	evaluator, err := experiment.NewEvaluator(
		experimentStore,
		experimentRunObserver{runs: runStore},
		experimentMeasurementsReader{
			metadata: metadata, content: content, validators: validators,
			limits: limits, tempDir: cmd.AgentSnapshots.TempDir,
		},
		experimentBinder,
		cmd.AgentExperiments.MaxConcurrency,
	)
	if err != nil {
		return nil, fmt.Errorf("construct experiment evaluator: %w", err)
	}
	workflowCanceler, err := workflowrun.NewCancelerWithWaits(
		runStore,
		buildFactory,
		db.NewAgentWorkflowWaitsFactory(dbConn),
	)
	if err != nil {
		return nil, fmt.Errorf("construct experiment workflow canceler: %w", err)
	}
	experimentCanceler, err := workflowrun.NewExperimentRunCancelerAdapter(workflowCanceler)
	if err != nil {
		return nil, err
	}
	cancellation, err := experiment.NewCancellationReconciler(
		experimentStore,
		experimentCanceler,
		cmd.AgentExperiments.MaxConcurrency,
	)
	if err != nil {
		return nil, fmt.Errorf("construct experiment cancellation reconciler: %w", err)
	}

	return []RunnableComponent{
		{
			Component: atc.Component{Name: atc.ComponentAgentExperimentRunner},
			Runnable:  runner,
			Interval:  cmd.AgentExperiments.Interval,
		},
		{
			Component: atc.Component{Name: atc.ComponentAgentExperimentEvaluator},
			Runnable:  evaluator,
			Interval:  cmd.AgentExperiments.Interval,
		},
		{
			Component: atc.Component{Name: atc.ComponentAgentExperimentCancellation},
			Runnable:  cancellation,
			Interval:  cmd.AgentExperiments.Interval,
		},
	}, nil
}

var _ experiment.RunObserver = experimentRunObserver{}
var _ experiment.MeasurementsReader = experimentMeasurementsReader{}
