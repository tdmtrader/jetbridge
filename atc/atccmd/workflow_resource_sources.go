package atccmd

import (
	"context"
	"errors"
	"fmt"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/agent/resourcecapture"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflowrun"
	"github.com/concourse/concourse/atc/component"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
)

type workflowResourceSourceRuntime struct {
	resourceCapturer *resourcecapture.Capturer
	captures         *workflowrun.SourceCaptureCoordinator
	admitter         workflowrun.ResourceSourceAdmitter
}

// newWorkflowResourceSourceRuntime builds one capturer shared by the resource
// capture API and source-admission capture. The admission adapter receives
// only persisted selections; neither source contents nor caller-selected
// versions cross this composition boundary.
func (cmd *RunCommand) newWorkflowResourceSourceRuntime(
	logger lager.Logger,
	dbConn db.DbConn,
	lockFactory lock.LockFactory,
	teamFactory db.TeamFactory,
	trustedTeam db.Team,
	checkFactory db.CheckFactory,
	pipelineRuns db.PipelineRunFactory,
	definitions workflowrun.SourceCaptureDefinitionStore,
	snapshots db.AgentSnapshotsFactory,
	templates workflowrun.ImmutableTemplateSaver,
) (workflowResourceSourceRuntime, error) {
	if !cmd.AgentSnapshots.Enabled {
		return workflowResourceSourceRuntime{}, nil
	}
	if trustedTeam == nil || trustedTeam.ID() <= 0 || trustedTeam.Name() == "" {
		return workflowResourceSourceRuntime{}, errors.New("construct workflow resource sources: trusted team is unavailable")
	}
	resolver, err := resourcecapture.NewATCResolver(teamFactory)
	if err != nil {
		return workflowResourceSourceRuntime{}, fmt.Errorf("construct resource-capture resolver: %w", err)
	}
	captureTemplates, err := resourcecapture.NewTemplateStore(templates)
	if err != nil {
		return workflowResourceSourceRuntime{}, fmt.Errorf("construct resource-capture template store: %w", err)
	}
	captureRuns, ok := pipelineRuns.(resourcecapture.PipelineRunStore)
	if !ok {
		return workflowResourceSourceRuntime{}, errors.New("construct resource-capture execution store: pipeline run factory lacks server-template execution")
	}
	captureLocker, err := resourcecapture.NewDBOperationLocker(
		logger.Session("resource-capture"), lockFactory,
	)
	if err != nil {
		return workflowResourceSourceRuntime{}, fmt.Errorf("construct resource-capture operation locker: %w", err)
	}
	captureExecutions, err := resourcecapture.NewExecutionStore(captureRuns, captureLocker)
	if err != nil {
		return workflowResourceSourceRuntime{}, fmt.Errorf("construct resource-capture execution store: %w", err)
	}
	captureFinder, ok := snapshots.(resourcecapture.CaptureOutputFinder)
	if !ok {
		return workflowResourceSourceRuntime{}, errors.New("construct resource-capture output store: snapshot store lacks capture lookup")
	}
	captureOutputs, err := resourcecapture.NewOutputStore(
		captureFinder, snapshots, db.NewAgentSnapshotDigestLocker(dbConn),
	)
	if err != nil {
		return workflowResourceSourceRuntime{}, fmt.Errorf("construct resource-capture output store: %w", err)
	}
	capturer, err := resourcecapture.NewCapturer(
		resolver, captureTemplates, captureExecutions, captureOutputs, cmd.AgentStepImage,
	)
	if err != nil {
		return workflowResourceSourceRuntime{}, fmt.Errorf("construct resource capturer: %w", err)
	}
	workflowCapturer, err := resourcecapture.NewWorkflowSourceCapturer(capturer)
	if err != nil {
		return workflowResourceSourceRuntime{}, fmt.Errorf("construct workflow source capturer: %w", err)
	}
	admissions := db.NewWorkflowResourceSourceAdmissionStore(dbConn)
	sourceBuilds := db.NewWorkflowResourceSourceBuildStore(dbConn, lockFactory, checkFactory)
	manualSources, err := workflowrun.NewSourceManualAdmission(
		trustedTeam.ID(), admissions, sourceBuilds,
	)
	if err != nil {
		return workflowResourceSourceRuntime{}, fmt.Errorf("construct manual workflow resource source admission: %w", err)
	}
	captures, err := workflowrun.NewSourceCaptureCoordinator(
		trustedTeam.ID(), trustedTeam.Name(), admissions, definitions, workflowCapturer,
	)
	if err != nil {
		return workflowResourceSourceRuntime{}, fmt.Errorf("construct workflow resource source capture: %w", err)
	}
	admitter, err := workflowrun.NewResourceSourceAdmitter(
		trustedTeam.ID(),
		db.NewWorkflowResourceSourcePipelinesFactory(dbConn),
		manualSources,
		captures,
		admissions,
		snapshots,
	)
	if err != nil {
		return workflowResourceSourceRuntime{}, fmt.Errorf("construct workflow resource source admitter: %w", err)
	}
	return workflowResourceSourceRuntime{
		resourceCapturer: capturer,
		captures:         captures,
		admitter:         admitter,
	}, nil
}

func newAutomaticSourceBuildReconciler(
	trustedTeamID int,
	trustedTeamName string,
	pipelines workflowrun.SourceBuildPipelineStore,
	builds workflowrun.SourceBuildStore,
	admissions db.WorkflowResourceSourceAdmissionStore,
	definitions workflowrun.SourceBuildDefinitionStore,
	captures workflowrun.SourceBuildCaptureCoordinator,
	sources workflowrun.ResourceSourceAdmitter,
	binder workflowrun.ReadySourceAdmissionBinder,
) (*workflowrun.SourceBuildReconciler, error) {
	return workflowrun.NewSourceBuildReconciler(
		trustedTeamID,
		pipelines,
		builds,
		admissions,
		definitions,
		workflowrun.WithAutomaticSourceLaunch(
			trustedTeamName, captures, sources, binder,
		),
	)
}

func newWorkflowResourceSourceComposition(
	connection db.DbConn,
	trustedMainTeamID int,
	promotionValidator workflow.PromotionValidator,
) (db.AgentWorkflowsFactory, component.Runnable, error) {
	return newWorkflowResourceSourceCompositionWithBrokerCatalog(
		connection,
		trustedMainTeamID,
		promotionValidator,
		db.NewAgentNodesFactory(connection),
		nil,
	)
}

func newWorkflowResourceSourceCompositionWithBrokerCatalog(
	connection db.DbConn,
	trustedMainTeamID int,
	promotionValidator workflow.PromotionValidator,
	nodeStore db.AgentNodesFactory,
	catalog *broker.Catalog,
) (db.AgentWorkflowsFactory, component.Runnable, error) {
	if trustedMainTeamID <= 0 {
		return nil, nil, errors.New("workflow resource source composition requires a trusted main team")
	}
	if promotionValidator == nil {
		return nil, nil, errors.New("workflow resource source composition requires a promotion validator")
	}
	registry := db.NewWorkflowResourceSourcePipelinesFactory(connection)
	lifecycle, err := workflowrun.NewSourcePipelineLifecycle(trustedMainTeamID, registry)
	if err != nil {
		return nil, nil, err
	}
	runnable, err := newWorkflowResourceSourceRunnable(lifecycle)
	if err != nil {
		return nil, nil, err
	}
	if nodeStore == nil {
		return nil, nil, errors.New("workflow resource source composition requires a node store")
	}
	promotion := db.AgentWorkflowResourceSourcePromotion{
		TeamID:   trustedMainTeamID,
		Registry: registry,
		Renderer: db.DefaultWorkflowResourceSourcePipelineRenderer{},
	}
	if catalog != nil {
		return db.NewAgentWorkflowsFactoryWithResourceSourcesAndNodeResolverAndBrokerCatalog(
			connection, nodeStore, catalog, promotionValidator, promotion,
		), runnable, nil
	}
	return db.NewAgentWorkflowsFactoryWithResourceSourcesAndNodeResolver(
		connection, nodeStore, promotionValidator, promotion,
	), runnable, nil
}

type workflowResourceSourceReconciler interface {
	Reconcile(context.Context) error
}

func newWorkflowResourceSourceRunnable(
	lifecycle workflowResourceSourceReconciler,
) (component.Runnable, error) {
	if lifecycle == nil {
		return nil, errors.New(
			"workflow resource source runnable requires lifecycle reconciliation",
		)
	}
	return component.RunFunc(func(ctx context.Context) error {
		return lifecycle.Reconcile(ctx)
	}), nil
}
