package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	"strings"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/configvalidate"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/runinput"
)

type RunParams struct{ Vars atc.RunParams }

type RunCreationOpts struct {
	// ActivationEpoch is internal-only until the joint v2 checkpoint. Zero
	// preserves legacy admission; a nonzero value requires the durable marker.
	ActivationEpoch      int64
	Invocation           *RunInvocationIdentity
	Inputs               map[string]atc.RunInputSource
	SealedInputAuthority *runinput.Authority
	Config               *atc.Config
	BeforeCommit         func(Tx, RunCreation) error
}

type RunCreation struct {
	Replayed      bool
	Run           PipelineRun
	Config        atc.Config
	CanonicalJSON []byte
	ConfigHash    string
	EntryJobs     []string
	EntryBuilds   []Build
}

type PipelineRunFactory interface {
	InputUploadAudience(context.Context, Tx, Pipeline, string, int64, string) (runinput.Audience, error)
	ReserveRunInputUpload(context.Context, Tx, runinput.Audience, output.InputStage, string, time.Duration) error
	RegisterRunInputUpload(context.Context, Tx, runinput.Audience, output.InputPublication, *output.ReceiptSignatureVerifier) (RunInputUploadClaim, error)
	CaptureProgress(context.Context, int) ([]atc.RunCaptureProgress, error)
	ExecuteCancellationFinality(context.Context, RunCancellationLease, RunCancellationOperation) (RunCancellationDebt, error)
	CancellationRunExecution(context.Context, Tx, RunCancellationLease, RunCancellationOperation) (RunCancellationExecution, error)
	RecordCancelledRunExecution(context.Context, Tx, RunCancellationLease, RunCancellationOperation, RunOutputCancellationEvidence, RunExecutionVerifier) error
	RecordRunExecutionWitness(context.Context, Tx, int, atc.PlanID, executioncontrol.Acknowledgement, RunExecutionVerifier) error
	RunExecutionContainer(context.Context, Tx, string) (bool, error)
	RunExecutionOwner(context.Context, Tx, int) (int, bool, error)
	RunExecution(context.Context, Tx, int, atc.PlanID) (RunExecutionAdmission, bool, error)
	AdmitRunExecution(context.Context, Tx, RunExecutionRequest) (RunExecutionAdmission, bool, error)
	CancellationOutputTask(context.Context, Tx, RunCancellationLease, RunCancellationOperation) (RunCancellationSource, error)
	CheckCancellationOperation(context.Context, Tx, RunCancellationLease, RunCancellationOperation) error
	ExecuteCancellationOperation(context.Context, RunCancellationLease, RunCancellationOperation) (RunCancellationDebt, error)
	PendingRunCancellations(context.Context, Tx, RunCancellationLease, int) ([]int, error)
	DiscoverRunCancellation(context.Context, Tx, RunCancellationLease, int, int) (int, error)
	ClaimRunCancellationOperation(context.Context, Tx, RunCancellationLease, int) (RunCancellationOperation, bool, error)
	RecordRunCancellationProgress(context.Context, Tx, RunCancellationLease, RunCancellationOperation, RunCancellationDebt) error
	ClaimRunCancellationLease(context.Context, Tx, string, time.Duration) (RunCancellationLease, bool, error)
	RenewRunCancellationLease(context.Context, Tx, RunCancellationLease, time.Duration) (RunCancellationLease, error)
	RequestRunCancellation(context.Context, int, string, *string) (atc.RunCancelOutcome, error)
	AcceptRunCancellation(context.Context, Tx, int, string, *string) (atc.RunCancelOutcome, error)
	FinalizeOutputRun(context.Context, Tx, int) (bool, error)
	PendingOutputRuns(context.Context, Tx, int, int) ([]int, error)
	TerminalResult(context.Context, int) (RunTerminalResult, bool, error)
	AfterRunCompleted()

	OutputTask(context.Context, Tx, int, string) (RunOutputTask, bool, error)
	PendingOutputSources(context.Context, Tx, int) ([]RunOutputTask, error)
	PredeclareOutputTask(context.Context, Tx, int, atc.TaskPlan, int64, time.Duration, string, string) (output.HandoffRecord, error)
	RequestOutputSource(context.Context, Tx, int, atc.TaskPlan, int64) error
	RecordOutputSource(context.Context, Tx, int, atc.TaskPlan, output.ReservedIncarnation, string) error
	Definition(int) (atc.RunDefinition, bool, error)
	CreateRun(context.Context, Pipeline, RunParams, string) (RunCreation, error)
	CreateRunInTx(context.Context, Tx, Pipeline, RunParams, string, RunCreationOpts) (RunCreation, error)
	AfterRunCreated(context.Context, RunCreation) error
	GetRun(Pipeline, int) (PipelineRun, bool, error)
	GetRunByID(int) (PipelineRun, bool, error)
	Runs(Pipeline, Page) ([]PipelineRun, Pagination, error)
	InstancePipeline(PipelineRun) (Pipeline, bool, error)
	InstancePipelines([]PipelineRun) (map[int]Pipeline, error)
}

type pipelineRunFactory struct {
	conn        DbConn
	lockFactory lock.LockFactory
}

func NewPipelineRunFactory(conn DbConn, lockFactory lock.LockFactory) PipelineRunFactory {
	return &pipelineRunFactory{conn: conn, lockFactory: lockFactory}
}

func (f *pipelineRunFactory) CreateRun(ctx context.Context, template Pipeline, params RunParams, createdBy string) (RunCreation, error) {
	tx, err := f.conn.BeginTx(ctx, nil)
	if err != nil {
		return RunCreation{}, err
	}
	defer Rollback(tx)
	creation, err := f.CreateRunInTx(ctx, tx, template, params, createdBy, RunCreationOpts{})
	if err != nil {
		return RunCreation{}, err
	}
	if err = tx.Commit(); err != nil {
		return RunCreation{}, err
	}
	// A committed run is durable even if the best-effort wakeup is unavailable.
	// Component polling recovers missed notifications.
	_ = f.AfterRunCreated(ctx, creation)
	return creation, nil
}

func (f *pipelineRunFactory) CreateRunInTx(ctx context.Context, tx Tx, template Pipeline, params RunParams, createdBy string, opts RunCreationOpts) (RunCreation, error) {
	if opts.Invocation != nil && (opts.ActivationEpoch <= 0 || opts.Config != nil || !opts.Invocation.valid()) {
		return RunCreation{}, ErrInvalidRunInvocation
	}
	if len(opts.Inputs) > 0 && opts.Invocation == nil {
		return RunCreation{}, ErrInvalidRunInvocation
	}
	version := atc.RunContractLegacyV1
	var birthEpoch *int64
	if opts.ActivationEpoch != 0 {
		var teamID int
		if err := tx.QueryRowContext(ctx, `SELECT id FROM teams WHERE id=$1 FOR SHARE`, template.TeamID()).Scan(&teamID); err != nil {
			return RunCreation{}, err
		}
		if err := lockRunActivation(ctx, tx, opts.ActivationEpoch); err != nil {
			return RunCreation{}, err
		}
		version = atc.RunContractV2
		birthEpoch = &opts.ActivationEpoch
	}
	locked := newPipeline(f.conn, f.lockFactory)
	err := scanPipeline(locked, pipelinesQuery.Where(sq.Eq{"p.id": template.ID()}).Suffix("FOR UPDATE OF p").RunWith(tx).QueryRow())
	if err != nil {
		if err == sql.ErrNoRows {
			return RunCreation{}, ErrPipelineRunNotTemplate
		}
		return RunCreation{}, err
	}
	if opts.Invocation != nil {
		if replay, found, err := f.replayRunInvocation(ctx, tx, locked, params.Vars, opts.Inputs, *opts.Invocation); err != nil || found {
			return replay, err
		}
	}
	if err := validateRunnableTemplate(locked); err != nil {
		return RunCreation{}, err
	}

	effective, err := f.effectiveConfig(tx, locked, opts.Config)
	if err != nil {
		return RunCreation{}, err
	}
	if err = configvalidate.ValidateTemplateConfig(effective); err != nil {
		return RunCreation{}, ErrPipelineTemplateInvalid{Err: err}
	}
	declarations, err := atc.RunTaskDeclarations(effective)
	if err != nil {
		return RunCreation{}, ErrPipelineTemplateInvalid{Err: err}
	}
	if len(declarations) > 0 && version != atc.RunContractV2 {
		return RunCreation{}, atc.ErrRunResultsUnavailable
	}
	normalized, err := atc.ValidateRunParams(effective.Params, params.Vars)
	if err != nil {
		return RunCreation{}, err
	}
	var intent []byte
	var inputs []pendingRunInput
	if opts.Invocation != nil {
		intent, err = runCallerIntent(effective.Params, params.Vars, opts.Inputs)
		if err != nil {
			return RunCreation{}, err
		}
		inputs, err = resolveRunInputs(ctx, tx, runinput.Audience{TeamID: locked.TeamID(), TemplateID: locked.ID(), PrincipalDigest: opts.Invocation.PrincipalDigest, Epoch: opts.ActivationEpoch}, declarations, opts.Inputs, opts.SealedInputAuthority)
		if err != nil {
			return RunCreation{}, err
		}
	}

	number, err := f.allocateNumber(tx, locked)
	if err != nil {
		return RunCreation{}, err
	}
	var runID int
	if err = tx.QueryRow("SELECT nextval('pipeline_runs_id_seq')").Scan(&runID); err != nil {
		return RunCreation{}, err
	}
	materialized, err := atc.MaterializeRunConfig(effective, atc.RunIdentity{Number: number, ID: runID}, normalized)
	if err != nil {
		return RunCreation{}, atc.InvalidRunParamsError{Err: err}
	}
	if _, errors := configvalidate.Validate(materialized.Config); len(errors) > 0 {
		return RunCreation{}, atc.InvalidRunParamsError{Err: fmt.Errorf("materialized config is invalid: %s", strings.Join(errors, "\n"))}
	}
	hashText := materializedConfigDigest(materialized.CanonicalJSON)
	paramsJSON, err := json.Marshal(normalized)
	if err != nil {
		return RunCreation{}, err
	}
	run := &pipelineRun{contractVersion: version, activationEpoch: opts.ActivationEpoch, id: runID, templatePipelineID: locked.ID(), number: number, params: atc.Params(normalized), status: atc.RunStatusRunning, createdBy: createdBy, configHash: hashText}
	// New runs are not completed; retain the nullable header value in creation
	// memory so it stays consistent with later header reads.
	var completedAt sql.NullTime
	if err = tx.QueryRow(`INSERT INTO pipeline_runs (id, template_pipeline_id, number, params, status, created_by, config_hash, run_contract_version, activation_epoch)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING created_at, completed_at`, runID, locked.ID(), number, paramsJSON, atc.RunStatusRunning, createdBy, hashText, version, birthEpoch).Scan(&run.createdAt, &completedAt); err != nil {
		return RunCreation{}, err
	}
	if completedAt.Valid {
		run.completedAt = &completedAt.Time
	}
	if err := retainRunDefinition(tx, runID, atc.RunDefinition{Template: effective, Materialized: materialized.Config}); err != nil {
		return RunCreation{}, err
	}

	runJobs := make(map[string]runJobMetadata, len(materialized.Config.Jobs))
	for _, job := range materialized.Config.Jobs {
		runJobKey := materialized.RunJobKeyByJobName[job.Name]
		if runJobKey == "" {
			// Materialization keys every job; a missing key here is a bug in
			// it, not something to paper over with the run name -- the key is
			// what history is read by after the payload is reclaimed.
			return RunCreation{}, fmt.Errorf("materialized job %q has no run job key", job.Name)
		}
		runJobs[job.Name] = runJobMetadata{expected: materialized.ExpectedJobNames[job.Name], runJobKey: runJobKey}
	}
	childRef := atc.PipelineRef{Name: locked.Name(), InstanceVars: atc.InstanceVars{"run": float64(number)}}
	childID, _, err := savePipelineWithOptions(tx, childRef, materialized.Config, 0, false, locked.TeamID(), sql.NullInt64{}, sql.NullInt64{}, pipelineSaveOptions{
		persistTemplateMetadata: true,
		pipelineRunID:           newNullInt64(runID),
		runJobs:                 runJobs,
	})
	if err != nil {
		return RunCreation{}, err
	}
	run.instancePipelineID = childID

	entryBuilds := make([]Build, 0, len(materialized.EntryJobNames))
	for _, name := range materialized.EntryJobNames {
		var jobID int
		if err = tx.QueryRow("SELECT id FROM jobs WHERE name = $1 AND pipeline_id = $2", name, childID).Scan(&jobID); err != nil {
			return RunCreation{}, err
		}
		build := newEmptyBuild(f.conn, f.lockFactory)
		created, err := createJobBuild(tx, build, jobID, jobBuildArgs{
			NextBuildName: true,
			ObservedRunID: runID,
			Values: map[string]any{
				"status": BuildStatusPending, "manually_triggered": true, "created_by": createdBy,
			},
		})
		if err != nil {
			return RunCreation{}, err
		}
		if !created {
			return RunCreation{}, fmt.Errorf("entry build for job %q was not created", name)
		}
		latestNonRerunID, err := latestCompletedNonRerunBuild(tx, jobID)
		if err != nil {
			return RunCreation{}, err
		}
		if err = updateNextBuildForJob(tx, jobID, latestNonRerunID); err != nil {
			return RunCreation{}, err
		}
		if err = requestSchedule(tx, jobID); err != nil {
			return RunCreation{}, err
		}
		entryBuilds = append(entryBuilds, build)
	}

	creation := RunCreation{Run: run, Config: materialized.Config, CanonicalJSON: materialized.CanonicalJSON, ConfigHash: hashText, EntryJobs: materialized.EntryJobNames, EntryBuilds: entryBuilds}
	if opts.Invocation != nil {
		if err = retainRunInputs(ctx, tx, runID, inputs); err != nil {
			return RunCreation{}, err
		}
		if err = retainRunInvocation(ctx, tx, locked.TeamID(), creation, effective, intent, *opts.Invocation); err != nil {
			return RunCreation{}, err
		}
	}
	if opts.BeforeCommit != nil {
		if err = opts.BeforeCommit(tx, creation); err != nil {
			return RunCreation{}, err
		}
	}
	return creation, nil
}

func (f *pipelineRunFactory) effectiveConfig(tx Tx, template *pipeline, override *atc.Config) (atc.Config, error) {
	if override != nil {
		return *override, nil
	}
	jobsRows, err := jobsQuery.Where(sq.Eq{"j.pipeline_id": template.ID(), "j.active": true}).OrderBy("j.id ASC").RunWith(tx).Query()
	if err != nil {
		return atc.Config{}, err
	}
	jobs, err := scanJobs(f.conn, f.lockFactory, jobsRows)
	if err != nil {
		return atc.Config{}, err
	}
	resourcesRows, err := resourcesQuery.Where(sq.Eq{"r.pipeline_id": template.ID()}).OrderBy("r.name").RunWith(tx).Query()
	if err != nil {
		return atc.Config{}, err
	}
	defer Close(resourcesRows)
	resources, err := scanResources(resourcesRows, f.conn, f.lockFactory)
	if err != nil {
		return atc.Config{}, err
	}
	resourceTypes, err := f.resourceTypesInTx(tx, template.ID())
	if err != nil {
		return atc.Config{}, err
	}
	prototypes, err := f.prototypesInTx(tx, template.ID())
	if err != nil {
		return atc.Config{}, err
	}
	jobConfigs, err := jobs.Configs()
	if err != nil {
		return atc.Config{}, err
	}
	return atc.Config{Groups: template.Groups(), VarSources: template.VarSources(), Resources: Resources(resources).Configs(), ResourceTypes: resourceTypes.Configs(), Prototypes: prototypes.Configs(), Jobs: jobConfigs, Display: template.Display(), Template: template.Template(), Params: template.Params(), RunRetention: template.RunRetention(), CacheScope: template.CacheScope()}, nil
}

func (f *pipelineRunFactory) resourceTypesInTx(tx Tx, pipelineID int) (ResourceTypes, error) {
	rows, err := resourceTypesQuery.Where(sq.Eq{"r.pipeline_id": pipelineID}).OrderBy("r.name").RunWith(tx).Query()
	if err != nil {
		return nil, err
	}
	defer Close(rows)
	var values ResourceTypes
	for rows.Next() {
		value := newEmptyResourceType(f.conn, f.lockFactory)
		if err := scanResourceType(value, rows); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (f *pipelineRunFactory) prototypesInTx(tx Tx, pipelineID int) (Prototypes, error) {
	rows, err := prototypesQuery.Where(sq.Eq{"pt.pipeline_id": pipelineID}).OrderBy("pt.name").RunWith(tx).Query()
	if err != nil {
		return nil, err
	}
	defer Close(rows)
	var values Prototypes
	for rows.Next() {
		value := newEmptyPrototype(f.conn, f.lockFactory)
		if err := scanPrototype(value, rows); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (f *pipelineRunFactory) allocateNumber(tx Tx, template *pipeline) (int, error) {
	for {
		var number int
		if err := tx.QueryRow("UPDATE pipelines SET last_run_number = last_run_number + 1 WHERE id = $1 RETURNING last_run_number", template.ID()).Scan(&number); err != nil {
			return 0, err
		}
		instanceVars, _ := json.Marshal(atc.InstanceVars{"run": float64(number)})
		var occupied bool
		// The probe answers exactly one question: is the instance ref
		// (team, name, {"run": N}) already taken? The index that governs that,
		// pipelines_name_team_id_instance_vars, is on (name, team_id,
		// instance_vars) and does not mention pipeline_run_id — so neither may
		// this probe. A run payload sitting on the ref is just as much an
		// occupant as an ordinary instanced pipeline, and skipping any occupant
		// is the whole point of this retry loop. Filtering payloads out made
		// stale payloads invisible: RenamePipeline deliberately renames only the
		// template row and leaves its payloads on the old name, so a template
		// renamed away and recreated under the old name starts at
		// last_run_number 0, the probe reports {run: 1} free, and
		// savePipelineWithOptions refuses it with ErrPipelineRunPayloadMutation
		// on every attempt, forever.
		err := tx.QueryRow("SELECT EXISTS (SELECT 1 FROM pipelines WHERE team_id = $1 AND name = $2 AND instance_vars = $3::jsonb)", template.TeamID(), template.Name(), string(instanceVars)).Scan(&occupied)
		if err != nil || !occupied {
			return number, err
		}
	}
}

func (f *pipelineRunFactory) AfterRunCreated(_ context.Context, creation RunCreation) error {
	scannerErr := f.conn.Bus().Notify(atc.ComponentLidarScanner)
	schedulerErr := f.conn.Bus().Notify(atc.ComponentScheduler)
	if scannerErr != nil {
		return scannerErr
	}
	return schedulerErr
}

func (f *pipelineRunFactory) GetRun(template Pipeline, number int) (PipelineRun, bool, error) {
	return f.getRun(pipelineRunsQuery.Where(sq.Eq{"r.template_pipeline_id": template.ID(), "r.number": number}).RunWith(f.conn).QueryRow())
}

func (f *pipelineRunFactory) GetRunByID(id int) (PipelineRun, bool, error) {
	return f.getRun(pipelineRunsQuery.Where(sq.Eq{"r.id": id}).RunWith(f.conn).QueryRow())
}

func (f *pipelineRunFactory) InstancePipeline(run PipelineRun) (Pipeline, bool, error) {
	pipeline := newPipeline(f.conn, f.lockFactory)
	err := scanPipeline(pipeline, pipelinesQuery.
		Where(sq.Eq{"p.pipeline_run_id": run.ID()}).
		RunWith(f.conn).
		QueryRow())
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return pipeline, true, nil
}

// InstancePipelines resolves the payloads of a whole page of runs in one query,
// keyed by run ID. The runs listing is reachable by an unauthenticated viewer on
// an exposed template, so resolving payloads one run at a time made the page an
// amplifier bounded only by atc.PaginationAPIMaxLimit.
//
// A run whose payload has been reclaimed is absent from the map rather than
// present-and-nil: a caller's map lookup then yields the zero db.Pipeline -- a
// nil interface -- which is the same value the single-run path hands the
// presenter for a reclaimed run. pipelines_pipeline_run_id_unique guarantees at
// most one payload row per run, so no entry is ever overwritten.
func (f *pipelineRunFactory) InstancePipelines(runs []PipelineRun) (map[int]Pipeline, error) {
	payloads := make(map[int]Pipeline, len(runs))
	if len(runs) == 0 {
		return payloads, nil
	}

	ids := make([]int, 0, len(runs))
	for _, run := range runs {
		ids = append(ids, run.ID())
	}

	rows, err := pipelinesQuery.
		Where(sq.Eq{"p.pipeline_run_id": ids}).
		RunWith(f.conn).
		Query()
	if err != nil {
		return nil, err
	}
	defer Close(rows)

	for rows.Next() {
		payload := newPipeline(f.conn, f.lockFactory)
		if err := scanPipeline(payload, rows); err != nil {
			return nil, err
		}
		runID, found := payload.PipelineRunID()
		if !found {
			continue
		}
		payloads[runID] = payload
	}
	return payloads, rows.Err()
}

func (f *pipelineRunFactory) getRun(row scannable) (PipelineRun, bool, error) {
	run := &pipelineRun{}
	if err := scanPipelineRun(run, row); err != nil {
		if err == sql.ErrNoRows {
			return nil, false, nil
		}
		return nil, false, err
	}
	return run, true, nil
}

func (f *pipelineRunFactory) Runs(template Pipeline, page Page) ([]PipelineRun, Pagination, error) {
	if page.From != nil && page.To != nil && *page.From > *page.To {
		return nil, Pagination{}, fmt.Errorf("invalid range boundaries")
	}
	tx, err := f.conn.Begin()
	if err != nil {
		return nil, Pagination{}, err
	}
	defer Rollback(tx)

	original := pipelineRunsQuery.Where(sq.Eq{"r.template_pipeline_id": template.ID()})
	query, reverse := original.Limit(uint64(page.Limit)), false
	switch {
	case page.From == nil && page.To == nil:
		query = query.OrderBy("r.number DESC")
	case page.From != nil && page.To == nil:
		query = query.Where(sq.GtOrEq{"r.number": *page.From}).OrderBy("r.number ASC")
		reverse = true
	case page.From == nil && page.To != nil:
		query = query.Where(sq.LtOrEq{"r.number": *page.To}).OrderBy("r.number DESC")
	default:
		query = query.Where(sq.GtOrEq{"r.number": *page.From}).Where(sq.LtOrEq{"r.number": *page.To}).OrderBy("r.number ASC")
		reverse = true
	}
	runs, err := f.queryRuns(tx, query)
	if err != nil {
		return nil, Pagination{}, err
	}
	if reverse {
		for i, j := 0, len(runs)-1; i < j; i, j = i+1, j-1 {
			runs[i], runs[j] = runs[j], runs[i]
		}
	}
	if len(runs) == 0 {
		return runs, Pagination{}, tx.Commit()
	}

	pagination := Pagination{}
	oldest, newest := runs[len(runs)-1].Number(), runs[0].Number()
	if older, err := f.queryOneRun(tx, original.Where(sq.Lt{"r.number": oldest}).OrderBy("r.number DESC").Limit(1)); err != nil {
		return nil, Pagination{}, err
	} else if older != nil {
		pagination.Older = &Page{To: NewIntPtr(older.Number()), Limit: page.Limit}
	}
	if newer, err := f.queryOneRun(tx, original.Where(sq.Gt{"r.number": newest}).OrderBy("r.number ASC").Limit(1)); err != nil {
		return nil, Pagination{}, err
	} else if newer != nil {
		pagination.Newer = &Page{From: NewIntPtr(newer.Number()), Limit: page.Limit}
	}
	if err = tx.Commit(); err != nil {
		return nil, Pagination{}, err
	}
	return runs, pagination, nil
}

func (f *pipelineRunFactory) queryRuns(tx Tx, query sq.SelectBuilder) ([]PipelineRun, error) {
	rows, err := query.RunWith(tx).Query()
	if err != nil {
		return nil, err
	}
	defer Close(rows)
	var runs []PipelineRun
	for rows.Next() {
		run := &pipelineRun{}
		if err := scanPipelineRun(run, rows); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (f *pipelineRunFactory) queryOneRun(tx Tx, query sq.SelectBuilder) (PipelineRun, error) {
	run := &pipelineRun{}
	if err := scanPipelineRun(run, query.RunWith(tx).QueryRow()); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return run, nil
}
