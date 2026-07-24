package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db/lock"
)

// ErrNotATemplate is returned when CreateRun is called on a pipeline that is
// not a base template (template: true, no instance vars).
var ErrNotATemplate = errors.New("pipeline is not a template")

// ErrTemplateNotFound is returned when the template pipeline id is unknown.
var ErrTemplateNotFound = errors.New("template pipeline not found")

type WorkflowRunTemplateRef struct {
	PipelineID    int
	TeamID        int
	Name          string
	ConfigVersion int
	FullHash      string
}

type BeforeWorkflowRunCommit func(context.Context, int) error

type WorkflowRunExecution struct {
	PipelineRun           PipelineRun
	InstanceConfig        atc.Config
	InstanceCanonicalJSON []byte
	InstanceConfigHash    string
	EntryBuildIDs         []int
}

var pipelineRunsQuery = psql.Select(
	"r.id", "r.template_pipeline_id", "r.instance_pipeline_id", "r.number",
	"r.params", "r.status", "r.created_by", "r.created_at", "r.completed_at",
	"r.archived",
).From("pipeline_runs r")

//counterfeiter:generate . PipelineRunFactory
type PipelineRunFactory interface {
	// CreateRun validates params against the template's params schema,
	// allocates the next run number, materializes the instanced pipeline
	// (instance_vars: {"run": N}), triggers entry jobs, and returns the run.
	CreateRun(templatePipelineID int, params map[string]any, createdBy string) (PipelineRun, error)
	CreateRunForWorkflowRun(
		context.Context,
		snapshot.WorkflowRunID,
		WorkflowRunTemplateRef,
		map[string]any,
		string,
		BeforeWorkflowRunCommit,
	) (WorkflowRunExecution, bool, error)
	GetRun(templatePipelineID, number int) (PipelineRun, bool, error)
	// GetRunByID fetches a run by its global pipeline_runs.id (additive,
	// 2026-07-09 checkpoint seam delta §6 — consumed by dispatch's
	// run-completion reconciler, which holds run ids from
	// tickets.pipeline_run_id).
	GetRunByID(id int) (PipelineRun, bool, error)
	ListRuns(templatePipelineID int, limit int) ([]PipelineRun, error)
	RunningRuns() ([]PipelineRun, error)
	CompletedRunsWithNewActivity() ([]PipelineRun, error)
	RunsToArchive() ([]PipelineRun, error)

	// RunBelongsToPipeline reports whether pipeline_runs row `runID` was
	// materialized as pipeline instance `pipelineID`. The agent-step exec
	// gates §8.2 model-credential delivery on this: AGENT_PIPELINE_RUN_ID
	// arrives via attacker-writable plan env (F30), so a run id may only name
	// the `agent-run-<id>` secret when its instance pipeline is the very
	// pipeline this build runs in. The verified model credential is delivered
	// only to that build's main agent container; sidecar secret env is empty.
	RunBelongsToPipeline(runID, pipelineID int) (bool, error)

	// TicketBelongsToRun reports whether agent_tickets row `ticketID` is
	// currently dispatched as pipeline run `runID`
	// (agent_tickets.pipeline_run_id, contracts §1.7 — the latest attempt).
	// The agent-step exec gates budget admission and cost/metrics
	// attribution on this: AGENT_TICKET_ID arrives via attacker-writable
	// plan env (F30) just like the run id, so a claimed ticket may only be
	// admitted against — and charged in agent_cost_ledger — when server-side
	// linkage proves the verified run was dispatched for it. agent_tickets
	// landed with ticket-core (migration 1773106062); on a DB that has not
	// migrated (or was downgraded) no ticket claim is verifiable and this
	// reports false (fail closed — there are no legitimate tickets to claim).
	TicketBelongsToRun(ticketID, runID int) (bool, error)
}

// NewPipelineRunFactory constructs the factory. The CheckFactory is
// injected (F27, 2026-07-09) because CreateRun itself enqueues the frozen
// check set — the runs API handler is a pass-through, so in-process
// consumers (dispatch, experiments) get identical semantics. The logger is
// injected (Registrar/Reaper idiom) because CreateRun has no ctx/logger
// parameter (§2.3 frozen signature) and the check enqueue is best-effort:
// its failures must be logged, never returned.
func NewPipelineRunFactory(
	logger lager.Logger,
	conn DbConn,
	lockFactory lock.LockFactory,
	checkFactory CheckFactory,
) PipelineRunFactory {
	return &pipelineRunFactory{
		logger:       logger,
		conn:         conn,
		lockFactory:  lockFactory,
		checkFactory: checkFactory,
	}
}

type pipelineRunFactory struct {
	logger       lager.Logger
	conn         DbConn
	lockFactory  lock.LockFactory
	checkFactory CheckFactory
}

func (f *pipelineRunFactory) CreateRun(templatePipelineID int, params map[string]any, createdBy string) (PipelineRun, error) {
	return f.createRun(context.Background(), nil, templatePipelineID, params, createdBy)
}

// CreateRunForServerTemplate is the narrow execution seam for immutable,
// registry-owned server adapters that are not durable workflow definitions.
// It deliberately is not part of PipelineRunFactory: public callers retain
// CreateRun's guard, while trusted composition must opt into this capability.
func (f *pipelineRunFactory) CreateRunForServerTemplate(
	ctx context.Context,
	templateRef WorkflowRunTemplateRef,
	params map[string]any,
	createdBy string,
) (PipelineRun, error) {
	if ctx == nil {
		return nil, fmt.Errorf("db: server template context is required")
	}
	if templateRef.PipelineID <= 0 || templateRef.TeamID <= 0 || templateRef.Name == "" ||
		templateRef.ConfigVersion <= 0 || templateRef.FullHash == "" {
		return nil, fmt.Errorf("db: invalid server template reference")
	}
	return f.createRun(ctx, &templateRef, templateRef.PipelineID, params, createdBy)
}

func (f *pipelineRunFactory) createRun(
	ctx context.Context,
	serverTemplate *WorkflowRunTemplateRef,
	templatePipelineID int,
	params map[string]any,
	createdBy string,
) (PipelineRun, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	template, found, err := f.pipelineByID(templatePipelineID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrTemplateNotFound
	}
	if !template.Template() || template.InstanceVars() != nil {
		return nil, ErrNotATemplate
	}

	validated, err := atc.ValidateRunParams(template.ParamsSchema(), params)
	if err != nil {
		return nil, err
	}

	// Relies on Task 7's Config() carrying Template/Params/RunRetention
	// (F19, 2026-07-09): the returned config is re-saved as the instance, so
	// a Config() that dropped Template would save instances with
	// template=false and break lidar exclusion + version pinning. If the
	// instance.Template() assertion in the factory test fails, fix
	// db.pipeline.Config() — do NOT force Template=true here.
	config, err := template.Config()
	if err != nil {
		return nil, err
	}

	tx, err := f.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer Rollback(tx)
	if serverTemplate == nil {
		if err := rejectWorkflowRunOwnedPipeline(ctx, tx, templatePipelineID); err != nil {
			return nil, err
		}
	} else {
		if err := validateLockedServerTemplate(ctx, tx, *serverTemplate, config); err != nil {
			return nil, err
		}
	}

	// A pipeline instance {name, {"run": N}} may already exist (e.g. a user
	// ran fly set-pipeline with those instance vars before this run number
	// was ever allocated). savePipeline below is called with from=0, which
	// only ever creates: a pre-existing instance fails the tx, and because
	// the allocator increment rolls back with it, every retry would hit the
	// same instance — wedging the template permanently. Skip past existing
	// instances instead; the increments accumulate within the tx, so the
	// committed last_run_number lands on the first free number.
	var number int
	for {
		err = tx.QueryRow(
			`UPDATE pipelines SET last_run_number = last_run_number + 1 WHERE id = $1 RETURNING last_run_number`,
			templatePipelineID,
		).Scan(&number)
		if err != nil {
			return nil, err
		}

		instanceVars, err := json.Marshal(atc.InstanceVars{"run": number})
		if err != nil {
			return nil, err
		}

		var exists bool
		err = tx.QueryRow(
			`SELECT EXISTS (SELECT 1 FROM pipelines WHERE team_id = $1 AND name = $2 AND instance_vars = $3::jsonb)`,
			template.TeamID(), template.Name(), instanceVars,
		).Scan(&exists)
		if err != nil {
			return nil, err
		}
		if !exists {
			break
		}
	}

	// F30 (2026-07-09): allocate pipeline_runs.id BEFORE materialization so
	// ((run_id)) — the globally-unique id that §8.1 AGENT_PIPELINE_RUN_ID is
	// defined as — can be interpolated into the instance config. nextval
	// keeps the SERIAL sequence consistent with the explicit-id insert below.
	var runID int
	err = tx.QueryRow(
		`SELECT nextval(pg_get_serial_sequence('pipeline_runs', 'id'))`,
	).Scan(&runID)
	if err != nil {
		return nil, err
	}

	instanceConfig, err := atc.MaterializeRunConfig(config, number, runID, validated)
	if err != nil {
		return nil, err
	}

	nullID := sql.NullInt64{Valid: false}
	instanceID, _, err := savePipeline(
		tx,
		atc.PipelineRef{Name: template.Name(), InstanceVars: atc.InstanceVars{"run": number}},
		instanceConfig,
		0,     // fresh instance; the allocator loop above skipped any pre-existing {"run": N}
		false, // run instances start unpaused
		template.TeamID(),
		nullID, nullID,
	)
	if err != nil {
		return nil, err
	}

	paramsPayload, err := json.Marshal(validated)
	if err != nil {
		return nil, err
	}

	run := newPipelineRun(f.conn, f.lockFactory)
	run.id = runID
	run.templatePipelineID = templatePipelineID
	run.instancePipelineID = sql.NullInt64{Int64: int64(instanceID), Valid: true}
	run.number = number
	run.params = validated
	run.status = PipelineRunRunning
	run.createdBy = createdBy

	// explicit id: pre-allocated above so ((run_id)) is already baked into
	// the saved instance config (F30)
	err = psql.Insert("pipeline_runs").
		Columns("id", "template_pipeline_id", "instance_pipeline_id", "number", "params", "created_by").
		Values(runID, templatePipelineID, instanceID, number, paramsPayload, createdBy).
		Suffix("RETURNING created_at").
		RunWith(tx).
		QueryRow().
		Scan(&run.createdAt)
	if err != nil {
		return nil, err
	}
	if serverTemplate != nil {
		if err := f.createServerTemplateEntryBuild(tx, instanceID, template.TeamID(), template.TeamName(), instanceConfig, createdBy); err != nil {
			return nil, err
		}
	}

	err = tx.Commit()
	if err != nil {
		return nil, err
	}

	// Trigger entry jobs (no passed: upstream) as manually-triggered builds.
	instance, found, err := f.pipelineByID(instanceID)
	if err != nil {
		return nil, err
	}
	if found {
		if serverTemplate == nil {
			for _, jobName := range instanceConfig.EntryJobs() {
				job, jobFound, err := instance.Job(jobName)
				if err != nil {
					return nil, err
				}
				if !jobFound {
					continue
				}
				_, err = job.CreateBuild(createdBy)
				if err != nil {
					return nil, fmt.Errorf("triggering entry job %s: %w", jobName, err)
				}
			}
		}

		// F27 (2026-07-09): the frozen check set is enqueued HERE, by the
		// factory, per shared-contracts §7.1 item 2 — not in the API handler.
		f.enqueueInitialChecks(instance)
	}

	return run, nil
}

func (f *pipelineRunFactory) CreateRunForWorkflowRun(
	ctx context.Context,
	workflowRunID snapshot.WorkflowRunID,
	templateRef WorkflowRunTemplateRef,
	params map[string]any,
	createdBy string,
	beforeCommit BeforeWorkflowRunCommit,
) (WorkflowRunExecution, bool, error) {
	if err := workflowRunID.Validate(); err != nil {
		return WorkflowRunExecution{}, false, err
	}
	if templateRef.PipelineID <= 0 || templateRef.TeamID <= 0 || templateRef.Name == "" ||
		templateRef.ConfigVersion <= 0 || templateRef.FullHash == "" {
		return WorkflowRunExecution{}, false, fmt.Errorf("db: invalid workflow-run template reference")
	}
	if beforeCommit == nil {
		return WorkflowRunExecution{}, false, fmt.Errorf("db: workflow-run before-commit callback is required")
	}

	tx, err := f.conn.BeginTx(ctx, nil)
	if err != nil {
		return WorkflowRunExecution{}, false, err
	}
	defer Rollback(tx)

	durable, err := scanAgentWorkflowRun(tx.QueryRowContext(ctx, `
		SELECT `+agentWorkflowRunColumns+`
		FROM agent_workflow_runs
		WHERE id = $1
		FOR UPDATE
	`, int64(workflowRunID)), tx.EncryptionStrategy())
	if errors.Is(err, sql.ErrNoRows) {
		return WorkflowRunExecution{}, false, fmt.Errorf("db: workflow run not found")
	}
	if err != nil {
		return WorkflowRunExecution{}, false, err
	}

	complete := workflowRunExecutionComplete(durable)
	empty := workflowRunExecutionEmpty(durable)
	switch durable.Status {
	case AgentWorkflowRunStatusRunning:
		if !complete {
			return WorkflowRunExecution{}, false, fmt.Errorf("db: corrupt partial workflow-run execution")
		}
	case AgentWorkflowRunStatusAdmitting:
		if !empty && !complete {
			return WorkflowRunExecution{}, false, fmt.Errorf("db: corrupt partial workflow-run admission")
		}
	default:
		return WorkflowRunExecution{}, false, fmt.Errorf("db: workflow run is not admitting or running")
	}

	if err := validateLockedWorkflowTemplate(ctx, tx, durable.TeamID, templateRef); err != nil {
		return WorkflowRunExecution{}, false, err
	}
	parameterizedConfig, _, err := canonicalWorkflowRunConfig(durable.ParameterizedConfig)
	if err != nil {
		return WorkflowRunExecution{}, false, err
	}
	targetHash, err := workflow.TargetConfigHash(parameterizedConfig)
	if err != nil {
		return WorkflowRunExecution{}, false, err
	}
	if targetHash != durable.ParameterizedConfigHash || targetHash != templateRef.FullHash {
		return WorkflowRunExecution{}, false, fmt.Errorf("db: workflow-run durable template hash mismatch")
	}
	validatedParams, err := atc.ValidateRunParams(parameterizedConfig.Params, cloneRunParams(params))
	if err != nil {
		return WorkflowRunExecution{}, false, err
	}
	for name, value := range validatedParams {
		if _, ok := value.(string); !ok {
			return WorkflowRunExecution{}, false, fmt.Errorf("db: workflow-run param %q must be a string", name)
		}
	}
	if err := validateWorkflowRunEntryJob(parameterizedConfig); err != nil {
		return WorkflowRunExecution{}, false, err
	}

	if complete {
		execution, err := loadCommittedWorkflowRunExecution(ctx, tx, f.conn, f.lockFactory, durable, templateRef, parameterizedConfig, validatedParams)
		if err != nil {
			return WorkflowRunExecution{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return WorkflowRunExecution{}, false, err
		}
		return execution, false, nil
	}

	var number int
	for {
		if err := tx.QueryRowContext(ctx, `
			UPDATE pipelines
			SET last_run_number = last_run_number + 1
			WHERE id = $1
			RETURNING last_run_number
		`, templateRef.PipelineID).Scan(&number); err != nil {
			return WorkflowRunExecution{}, false, err
		}
		instanceVars, err := json.Marshal(atc.InstanceVars{"run": number})
		if err != nil {
			return WorkflowRunExecution{}, false, err
		}
		var exists bool
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pipelines
				WHERE team_id = $1 AND name = $2 AND instance_vars = $3::jsonb
			)
		`, templateRef.TeamID, templateRef.Name, instanceVars).Scan(&exists); err != nil {
			return WorkflowRunExecution{}, false, err
		}
		if !exists {
			break
		}
	}

	var pipelineRunID int
	if err := tx.QueryRowContext(ctx, `
		SELECT nextval(pg_get_serial_sequence('pipeline_runs', 'id'))
	`).Scan(&pipelineRunID); err != nil {
		return WorkflowRunExecution{}, false, err
	}
	instanceConfig, err := atc.MaterializeRunConfig(parameterizedConfig, number, pipelineRunID, validatedParams)
	if err != nil {
		return WorkflowRunExecution{}, false, err
	}
	instanceCanonical, err := instanceConfig.CanonicalJSON()
	if err != nil {
		return WorkflowRunExecution{}, false, err
	}
	instanceSum := sha256.Sum256(append([]byte("workflow-instance-config/v1\x00"), instanceCanonical...))
	instanceHash := fmt.Sprintf("%x", instanceSum[:])

	nullID := sql.NullInt64{Valid: false}
	instanceID, _, err := savePipeline(
		tx,
		atc.PipelineRef{Name: templateRef.Name, InstanceVars: atc.InstanceVars{"run": number}},
		instanceConfig,
		0,
		false,
		templateRef.TeamID,
		nullID,
		nullID,
	)
	if err != nil {
		return WorkflowRunExecution{}, false, err
	}

	paramsPayload, err := json.Marshal(validatedParams)
	if err != nil {
		return WorkflowRunExecution{}, false, err
	}
	pipelineRun := newPipelineRun(f.conn, f.lockFactory)
	pipelineRun.id = pipelineRunID
	pipelineRun.templatePipelineID = templateRef.PipelineID
	pipelineRun.instancePipelineID = sql.NullInt64{Int64: int64(instanceID), Valid: true}
	pipelineRun.number = number
	pipelineRun.params = validatedParams
	pipelineRun.status = PipelineRunRunning
	pipelineRun.createdBy = createdBy
	if err := psql.Insert("pipeline_runs").
		Columns("id", "template_pipeline_id", "instance_pipeline_id", "number", "params", "created_by").
		Values(pipelineRunID, templateRef.PipelineID, instanceID, number, paramsPayload, createdBy).
		Suffix("RETURNING created_at").
		RunWith(tx).
		QueryRow().
		Scan(&pipelineRun.createdAt); err != nil {
		return WorkflowRunExecution{}, false, err
	}

	if err := linkAgentWorkflowRunExecution(ctx, tx, workflowRunID, AgentWorkflowRunExecutionLink{
		PipelineRunID: pipelineRunID, TemplatePipelineID: templateRef.PipelineID,
		InstancePipelineID: instanceID, ConcreteConfig: json.RawMessage(instanceCanonical),
		ConcreteConfigHash: instanceHash,
	}); err != nil {
		return WorkflowRunExecution{}, false, err
	}

	callbackCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	err = beforeCommit(callbackCtx, pipelineRunID)
	cancel()
	if err != nil {
		return WorkflowRunExecution{}, false, err
	}

	plannedBuildID, err := f.createAndSelectWorkflowEntryBuild(
		ctx, tx, workflowRunID, pipelineRunID, instanceID,
		templateRef.TeamID, durable.TeamName, instanceConfig, createdBy,
	)
	if err != nil {
		return WorkflowRunExecution{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return WorkflowRunExecution{}, false, err
	}

	instance, found, loadErr := f.pipelineByID(instanceID)
	if loadErr != nil {
		f.logger.Error("failed-to-load-workflow-run-instance-for-checks", loadErr, lager.Data{"pipeline-id": instanceID})
	} else if found {
		f.enqueueInitialChecks(instance)
	}

	return WorkflowRunExecution{
		PipelineRun: pipelineRun, InstanceConfig: instanceConfig,
		InstanceCanonicalJSON: append([]byte(nil), instanceCanonical...),
		InstanceConfigHash:    instanceHash, EntryBuildIDs: []int{plannedBuildID},
	}, true, nil
}

func workflowRunExecutionEmpty(run AgentWorkflowRun) bool {
	return run.PipelineRunID == nil && run.TemplatePipelineID == nil && run.InstancePipelineID == nil &&
		len(run.ConcreteConfig) == 0 && run.ConcreteConfigHash == nil && run.PlannedBuildID == nil
}

func workflowRunExecutionComplete(run AgentWorkflowRun) bool {
	return run.PipelineRunID != nil && run.TemplatePipelineID != nil && run.InstancePipelineID != nil &&
		len(run.ConcreteConfig) != 0 && run.ConcreteConfigHash != nil && run.PlannedBuildID != nil
}

func validateWorkflowRunEntryJob(config atc.Config) error {
	entryJobs := config.EntryJobs()
	if len(config.Jobs) != 1 || config.Jobs[0].Name != "run" ||
		len(entryJobs) != 1 || entryJobs[0] != "run" {
		return fmt.Errorf("db: workflow-run config must have exactly one entry job named run")
	}
	return nil
}

func validateLockedWorkflowTemplate(ctx context.Context, tx Tx, durableTeamID int, ref WorkflowRunTemplateRef) error {
	var (
		teamID, version int
		name            string
		template        bool
		instanceVars    sql.NullString
		archived        bool
		owned           bool
	)
	err := tx.QueryRowContext(ctx, `
		SELECT p.team_id, p.name, p.version, p.template, p.instance_vars, p.archived,
		       EXISTS (SELECT 1 FROM agent_workflow_run_templates o WHERE o.pipeline_id = p.id)
		FROM pipelines p
		WHERE p.id = $1
		FOR UPDATE OF p
	`, ref.PipelineID).Scan(&teamID, &name, &version, &template, &instanceVars, &archived, &owned)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrTemplateNotFound
	}
	if err != nil {
		return err
	}
	if teamID != durableTeamID || teamID != ref.TeamID || name != ref.Name || version != ref.ConfigVersion ||
		!template || instanceVars.Valid || archived || !owned {
		return fmt.Errorf("db: workflow-run template reference drifted or collided")
	}
	return nil
}

func validateLockedServerTemplate(ctx context.Context, tx Tx, ref WorkflowRunTemplateRef, config atc.Config) error {
	if !strings.HasPrefix(ref.Name, "agent-resource-capture-") {
		return fmt.Errorf("db: server template is not a resource-capture template")
	}
	var (
		teamID, version int
		name            string
		template        bool
		instanceVars    sql.NullString
		archived        bool
		owned           bool
		workflowLinked  bool
	)
	err := tx.QueryRowContext(ctx, `
		SELECT p.team_id, p.name, p.version, p.template, p.instance_vars, p.archived,
		       EXISTS (SELECT 1 FROM agent_workflow_run_templates o WHERE o.pipeline_id = p.id),
		       EXISTS (SELECT 1 FROM agent_workflow_runs r WHERE r.template_pipeline_id = p.id)
		FROM pipelines p
		WHERE p.id = $1
		FOR UPDATE OF p
	`, ref.PipelineID).Scan(
		&teamID, &name, &version, &template, &instanceVars, &archived, &owned, &workflowLinked,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrTemplateNotFound
	}
	if err != nil {
		return err
	}
	if teamID != ref.TeamID || name != ref.Name || version != ref.ConfigVersion || !template ||
		instanceVars.Valid || archived || !owned || workflowLinked {
		return fmt.Errorf("db: server template reference drifted or collided")
	}
	if !config.Template || len(config.Params) != 0 || len(config.Jobs) != 1 || config.Jobs[0].Name != "capture" {
		return fmt.Errorf("db: resource-capture template has an invalid shape")
	}
	entryJobs := config.EntryJobs()
	if len(entryJobs) != 1 || entryJobs[0] != "capture" {
		return fmt.Errorf("db: resource-capture template must have one entry job")
	}
	fullHash, err := workflow.TargetConfigHash(config)
	if err != nil {
		return err
	}
	if fullHash != ref.FullHash {
		return fmt.Errorf("db: server template hash mismatch")
	}
	return nil
}

func (f *pipelineRunFactory) createServerTemplateEntryBuild(
	tx Tx,
	instanceID int,
	teamID int,
	teamName string,
	config atc.Config,
	createdBy string,
) error {
	entryJobs := config.EntryJobs()
	if len(entryJobs) != 1 || entryJobs[0] != "capture" {
		return fmt.Errorf("db: resource-capture template must have one entry job")
	}
	entryJob := newEmptyJob(f.conn, f.lockFactory)
	entryJob.pipelineID = instanceID
	entryJob.teamID = teamID
	entryJob.teamName = teamName
	if err := tx.QueryRow(`
		SELECT id, name
		FROM jobs
		WHERE pipeline_id = $1 AND name = $2
	`, instanceID, "capture").Scan(&entryJob.id, &entryJob.name); err != nil {
		return fmt.Errorf("db: load resource-capture entry job: %w", err)
	}
	if _, err := entryJob.createBuild(tx, createdBy); err != nil {
		return fmt.Errorf("db: create resource-capture entry build: %w", err)
	}
	return nil
}

func canonicalWorkflowRunConfig(raw json.RawMessage) (atc.Config, []byte, error) {
	var config atc.Config
	if len(raw) == 0 || !json.Valid(raw) {
		return atc.Config{}, nil, fmt.Errorf("db: invalid durable workflow-run parameterized config")
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return atc.Config{}, nil, fmt.Errorf("db: decode durable workflow-run parameterized config: %w", err)
	}
	canonical, err := config.CanonicalJSON()
	if err != nil {
		return atc.Config{}, nil, err
	}
	return config, canonical, nil
}

func cloneRunParams(params map[string]any) map[string]any {
	cloned := make(map[string]any, len(params))
	for name, value := range params {
		cloned[name] = value
	}
	return cloned
}

func (f *pipelineRunFactory) createAndSelectWorkflowEntryBuild(
	ctx context.Context,
	tx Tx,
	workflowRunID snapshot.WorkflowRunID,
	pipelineRunID int,
	instanceID int,
	teamID int,
	teamName string,
	config atc.Config,
	createdBy string,
) (int, error) {
	if err := validateWorkflowRunEntryJob(config); err != nil {
		return 0, err
	}
	entryJob := newEmptyJob(f.conn, f.lockFactory)
	entryJob.pipelineID = instanceID
	entryJob.teamID = teamID
	entryJob.teamName = teamName
	if err := tx.QueryRowContext(ctx, `
			SELECT id, name
			FROM jobs
			WHERE pipeline_id = $1 AND name = $2
		`, instanceID, "run").Scan(&entryJob.id, &entryJob.name); err != nil {
		return 0, fmt.Errorf("db: load workflow-run entry job %q: %w", "run", err)
	}
	build, err := entryJob.createBuild(tx, createdBy)
	if err != nil {
		return 0, fmt.Errorf("db: create workflow-run entry build %q: %w", "run", err)
	}
	var selectedBuildID int
	if err := tx.QueryRowContext(ctx, `
		UPDATE agent_workflow_runs
		SET planned_build_id = $2, updated_at = now()
		WHERE id = $1
		  AND status = 'admitting'
		  AND planned_build_id IS NULL
		  AND pipeline_run_id = $3
		  AND instance_pipeline_id = $4
		  AND team_id = $5
		RETURNING planned_build_id
	`, int64(workflowRunID), build.ID(), pipelineRunID, instanceID, teamID).Scan(&selectedBuildID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("db: workflow-run selected build ownership changed")
		}
		return 0, err
	}
	if selectedBuildID != build.ID() {
		return 0, fmt.Errorf("db: workflow-run selected build mismatch")
	}
	return selectedBuildID, nil
}

func loadCommittedWorkflowRunExecution(
	ctx context.Context,
	tx Tx,
	conn DbConn,
	lockFactory lock.LockFactory,
	durable AgentWorkflowRun,
	ref WorkflowRunTemplateRef,
	parameterizedConfig atc.Config,
	validatedParams map[string]any,
) (WorkflowRunExecution, error) {
	if *durable.TemplatePipelineID != ref.PipelineID {
		return WorkflowRunExecution{}, fmt.Errorf("db: committed workflow-run template association mismatch")
	}
	pipelineRun := newPipelineRun(conn, lockFactory)
	err := scanPipelineRun(pipelineRun, tx.QueryRowContext(ctx, `
		SELECT id, template_pipeline_id, instance_pipeline_id, number,
		       params, status, created_by, created_at, completed_at, archived
		FROM pipeline_runs
		WHERE id = $1
		FOR UPDATE
	`, *durable.PipelineRunID))
	if err != nil {
		return WorkflowRunExecution{}, err
	}
	if pipelineRun.templatePipelineID != ref.PipelineID || !pipelineRun.instancePipelineID.Valid ||
		int(pipelineRun.instancePipelineID.Int64) != *durable.InstancePipelineID {
		return WorkflowRunExecution{}, fmt.Errorf("db: committed workflow-run execution association mismatch")
	}
	if !semanticJSONEqual(mustJSON(validatedParams), mustJSON(pipelineRun.params)) {
		return WorkflowRunExecution{}, fmt.Errorf("db: committed workflow-run params mismatch")
	}
	instanceConfig, instanceCanonical, err := canonicalWorkflowRunConfig(durable.ConcreteConfig)
	if err != nil {
		return WorkflowRunExecution{}, err
	}
	expectedConfig, err := atc.MaterializeRunConfig(parameterizedConfig, pipelineRun.number, pipelineRun.id, validatedParams)
	if err != nil {
		return WorkflowRunExecution{}, err
	}
	expectedCanonical, err := expectedConfig.CanonicalJSON()
	if err != nil {
		return WorkflowRunExecution{}, err
	}
	if !semanticJSONEqual(instanceCanonical, expectedCanonical) {
		return WorkflowRunExecution{}, fmt.Errorf("db: committed workflow-run instance config mismatch")
	}
	instanceSum := sha256.Sum256(append([]byte("workflow-instance-config/v1\x00"), instanceCanonical...))
	instanceHash := fmt.Sprintf("%x", instanceSum[:])
	if instanceHash != *durable.ConcreteConfigHash {
		return WorkflowRunExecution{}, fmt.Errorf("db: committed workflow-run instance hash mismatch")
	}
	var selectedBuildID int64
	var buildPipelineID, buildTeamID, jobPipelineID int
	var jobName string
	if err := tx.QueryRowContext(ctx, `
		SELECT b.id, b.pipeline_id, b.team_id, j.pipeline_id, j.name
		FROM builds b
		JOIN jobs j ON j.id = b.job_id
		WHERE b.id = $1
		FOR UPDATE OF b, j
	`, *durable.PlannedBuildID).Scan(
		&selectedBuildID, &buildPipelineID, &buildTeamID, &jobPipelineID, &jobName,
	); err != nil {
		return WorkflowRunExecution{}, err
	}
	if selectedBuildID != *durable.PlannedBuildID ||
		buildPipelineID != *durable.InstancePipelineID || jobPipelineID != *durable.InstancePipelineID ||
		buildTeamID != durable.TeamID || jobName != "run" {
		return WorkflowRunExecution{}, fmt.Errorf("db: committed workflow-run selected build association mismatch")
	}
	entryBuildID := int(selectedBuildID)
	if int64(entryBuildID) != selectedBuildID {
		return WorkflowRunExecution{}, fmt.Errorf("db: committed workflow-run selected build identifier is out of range")
	}
	return WorkflowRunExecution{
		PipelineRun: pipelineRun, InstanceConfig: instanceConfig,
		InstanceCanonicalJSON: append([]byte(nil), instanceCanonical...),
		InstanceConfigHash:    instanceHash, EntryBuildIDs: []int{entryBuildID},
	}, nil
}

func mustJSON(value any) []byte {
	payload, _ := json.Marshal(value)
	return payload
}

// enqueueInitialChecks fires one manually-triggered check per resource of
// the run's instance pipeline — the frozen-check-set pinning model
// (shared-contracts §7.1). It lives on the factory (F27, 2026-07-09) so
// in-process consumers (dispatch, experiments) get the frozen check set
// too: lidar excludes template pipelines, so a factory-created run whose
// entry job has a get step would otherwise pend forever on an empty
// version set (NULL scope → trivially-passing ResourcesChecked → zero
// versions). Best-effort: failures are logged, never fail run creation
// (fly check-resource remains available).
func (f *pipelineRunFactory) enqueueInitialChecks(instance Pipeline) {
	logger := f.logger.Session("enqueue-initial-checks", lager.Data{
		"pipeline": instance.Name(), "instance-vars": instance.InstanceVars(),
	})

	resourceTypes, err := instance.ResourceTypes()
	if err != nil {
		logger.Error("failed-to-load-instance-resource-types", err)
		return
	}
	resources, err := instance.Resources()
	if err != nil {
		logger.Error("failed-to-load-instance-resources", err)
		return
	}

	for _, resource := range resources {
		_, _, err := f.checkFactory.TryCreateCheck(
			lagerctx.NewContext(context.Background(), logger),
			resource,
			resourceTypes,
			nil,  // from latest
			true, // manually triggered: skip interval
			true, // skip interval recursively
			true, // persist to DB
		)
		if err != nil {
			logger.Error("failed-to-enqueue-initial-check", err, lager.Data{"resource": resource.Name()})
		}
	}
}

func (f *pipelineRunFactory) GetRun(templatePipelineID, number int) (PipelineRun, bool, error) {
	run := newPipelineRun(f.conn, f.lockFactory)
	err := scanPipelineRun(run, pipelineRunsQuery.
		Where(sq.Eq{"r.template_pipeline_id": templatePipelineID, "r.number": number}).
		RunWith(f.conn).
		QueryRow())
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return run, true, nil
}

func (f *pipelineRunFactory) GetRunByID(id int) (PipelineRun, bool, error) {
	run := newPipelineRun(f.conn, f.lockFactory)
	err := scanPipelineRun(run, pipelineRunsQuery.
		Where(sq.Eq{"r.id": id}).
		RunWith(f.conn).
		QueryRow())
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return run, true, nil
}

func (f *pipelineRunFactory) ListRuns(templatePipelineID int, limit int) ([]PipelineRun, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := pipelineRunsQuery.
		Where(sq.Eq{"r.template_pipeline_id": templatePipelineID}).
		OrderBy("r.number DESC").
		Limit(uint64(limit)).
		RunWith(f.conn).
		Query()
	if err != nil {
		return nil, err
	}
	return f.scanRuns(rows)
}

func (f *pipelineRunFactory) RunningRuns() ([]PipelineRun, error) {
	rows, err := pipelineRunsQuery.
		Where(sq.Eq{"r.status": string(PipelineRunRunning), "r.archived": false}).
		OrderBy("r.id ASC").
		RunWith(f.conn).
		Query()
	if err != nil {
		return nil, err
	}
	return f.scanRuns(rows)
}

// CompletedRunsWithNewActivity returns non-running, non-archived runs whose
// instance pipeline has a pending/started job build — OR a job build that
// COMPLETED after the run's completed_at (F26, 2026-07-09): the Finish
// notify (the only wakeup besides the 10s poll) fires after a build leaves
// pending/started, so a retrigger that starts AND finishes inside one
// polling gap would otherwise never be observed and the run would keep a
// stale terminal status forever (plan-creation failures Finish without ever
// starting — buildstarter.go:225). Self-terminating: reopen clears
// completed_at (run leaves this query via the status filter) and the
// re-complete stamps a newer completed_at than every existing end_time.
func (f *pipelineRunFactory) CompletedRunsWithNewActivity() ([]PipelineRun, error) {
	rows, err := pipelineRunsQuery.
		Where(sq.NotEq{"r.status": string(PipelineRunRunning)}).
		Where(sq.Eq{"r.archived": false}).
		Where(sq.Expr(`EXISTS (
			SELECT 1 FROM builds b
			WHERE b.pipeline_id = r.instance_pipeline_id
			AND b.job_id IS NOT NULL
			AND (b.status IN ('pending','started')
			     OR (b.completed AND r.completed_at IS NOT NULL
			         AND b.end_time > r.completed_at)))`)).
		OrderBy("r.id ASC").
		RunWith(f.conn).
		Query()
	if err != nil {
		return nil, err
	}
	return f.scanRuns(rows)
}

func (f *pipelineRunFactory) RunsToArchive() ([]PipelineRun, error) {
	rows, err := f.conn.Query(`
		WITH candidate AS (
			SELECT r.id,
			       r.completed_at,
			       p.run_retention,
			       ROW_NUMBER() OVER (PARTITION BY r.template_pipeline_id ORDER BY r.number DESC) AS rank
			FROM pipeline_runs r
			JOIN pipelines p ON p.id = r.template_pipeline_id
			WHERE r.archived = false
			  AND r.status <> 'running'
			  AND p.run_retention IS NOT NULL
		)
		SELECT id FROM candidate
		WHERE (run_retention ? 'keep_last' AND rank > (run_retention->>'keep_last')::int)
		   OR (run_retention ? 'ttl_days' AND completed_at IS NOT NULL
		       AND completed_at < now() - make_interval(days => (run_retention->>'ttl_days')::int))`)
	if err != nil {
		return nil, err
	}
	defer Close(rows)

	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, nil
	}

	runRows, err := pipelineRunsQuery.
		Where(sq.Eq{"r.id": ids}).
		OrderBy("r.id ASC").
		RunWith(f.conn).
		Query()
	if err != nil {
		return nil, err
	}
	return f.scanRuns(runRows)
}

func (f *pipelineRunFactory) RunBelongsToPipeline(runID, pipelineID int) (bool, error) {
	if runID <= 0 || pipelineID <= 0 {
		return false, nil
	}
	var exists bool
	err := f.conn.QueryRow(
		`SELECT EXISTS (SELECT 1 FROM pipeline_runs WHERE id = $1 AND instance_pipeline_id = $2)`,
		runID, pipelineID,
	).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

func (f *pipelineRunFactory) TicketBelongsToRun(ticketID, runID int) (bool, error) {
	if ticketID <= 0 || runID <= 0 {
		return false, nil
	}

	// Absent table ⇒ there are no tickets, so no claim can be legitimate:
	// fail closed.
	tableExists, err := f.agentTicketsTableExists()
	if err != nil {
		return false, err
	}
	if !tableExists {
		return false, nil
	}

	var exists bool
	err = f.conn.QueryRow(
		`SELECT EXISTS (SELECT 1 FROM agent_tickets WHERE id = $1 AND pipeline_run_id = $2)`,
		ticketID, runID,
	).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

// agentTicketsTableExists probes for ticket-core's table (migration
// 1773106062), which may be absent on a not-yet-migrated or downgraded DB;
// cross-aggregate refs are plain int columns with no SQL FKs (§1.1). The
// probe is a separate statement because Postgres resolves every relation
// named in a query at parse time, even in dead branches.
func (f *pipelineRunFactory) agentTicketsTableExists() (bool, error) {
	var tableExists bool
	err := f.conn.QueryRow(`SELECT to_regclass('agent_tickets') IS NOT NULL`).Scan(&tableExists)
	return tableExists, err
}

func (f *pipelineRunFactory) scanRuns(rows *sql.Rows) ([]PipelineRun, error) {
	defer Close(rows)
	var runs []PipelineRun
	for rows.Next() {
		run := newPipelineRun(f.conn, f.lockFactory)
		if err := scanPipelineRun(run, rows); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, nil
}

func (f *pipelineRunFactory) pipelineByID(id int) (Pipeline, bool, error) {
	pipeline := newPipeline(f.conn, f.lockFactory)
	err := scanPipeline(
		pipeline,
		pipelinesQuery.Where(sq.Eq{"p.id": id}).RunWith(f.conn).QueryRow(),
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return pipeline, true, nil
}
