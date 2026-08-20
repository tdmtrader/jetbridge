package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/configvalidate"
	"github.com/concourse/concourse/atc/db/lock"
)

type RunParams struct{ Vars atc.RunParams }

type RunCreationOpts struct {
	Config       *atc.Config
	BeforeCommit func(Tx, RunCreation) error
}

type RunCreation struct {
	Run           PipelineRun
	Config        atc.Config
	CanonicalJSON []byte
	ConfigHash    string
	EntryJobs     []string
	EntryBuilds   []Build
}

type PipelineRunFactory interface {
	CreateRun(context.Context, Pipeline, RunParams, string) (RunCreation, error)
	CreateRunInTx(context.Context, Tx, Pipeline, RunParams, string, RunCreationOpts) (RunCreation, error)
	AfterRunCreated(context.Context, RunCreation) error
	GetRun(Pipeline, int) (PipelineRun, bool, error)
	GetRunByID(int) (PipelineRun, bool, error)
	Runs(Pipeline, Page) ([]PipelineRun, Pagination, error)
	InstancePipeline(PipelineRun) (Pipeline, bool, error)
}

type pipelineRunFactory struct {
	conn        DbConn
	lockFactory lock.LockFactory
}

func NewPipelineRunFactory(conn DbConn, lockFactory lock.LockFactory) PipelineRunFactory {
	return &pipelineRunFactory{conn: conn, lockFactory: lockFactory}
}

func (f *pipelineRunFactory) CreateRun(ctx context.Context, base Pipeline, params RunParams, createdBy string) (RunCreation, error) {
	tx, err := f.conn.BeginTx(ctx, nil)
	if err != nil {
		return RunCreation{}, err
	}
	defer Rollback(tx)
	creation, err := f.CreateRunInTx(ctx, tx, base, params, createdBy, RunCreationOpts{})
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

func (f *pipelineRunFactory) CreateRunInTx(_ context.Context, tx Tx, base Pipeline, params RunParams, createdBy string, opts RunCreationOpts) (RunCreation, error) {
	locked := newPipeline(f.conn, f.lockFactory)
	err := scanPipeline(locked, pipelinesQuery.Where(sq.Eq{"p.id": base.ID()}).Suffix("FOR UPDATE OF p").RunWith(tx).QueryRow())
	if err != nil {
		if err == sql.ErrNoRows {
			return RunCreation{}, ErrPipelineRunNotTemplate
		}
		return RunCreation{}, err
	}
	if locked.InstanceVars() != nil {
		return RunCreation{}, ErrPipelineRunInstanced
	}
	if !locked.Template() {
		return RunCreation{}, ErrPipelineRunNotTemplate
	}
	if locked.Archived() {
		return RunCreation{}, ErrPipelineRunArchived
	}
	if locked.Paused() {
		return RunCreation{}, ErrPipelineRunPaused
	}

	effective, err := f.effectiveConfig(tx, locked, opts.Config)
	if err != nil {
		return RunCreation{}, err
	}
	if err = configvalidate.ValidateTemplateConfig(effective); err != nil {
		return RunCreation{}, err
	}
	normalized, err := atc.ValidateRunParams(effective.Params, params.Vars)
	if err != nil {
		return RunCreation{}, err
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
	hash := sha256.Sum256(append([]byte("run-instance-config/v1\x00"), materialized.CanonicalJSON...))
	hashText := fmt.Sprintf("%x", hash)
	paramsJSON, err := json.Marshal(normalized)
	if err != nil {
		return RunCreation{}, err
	}
	run := &pipelineRun{id: runID, templatePipelineID: locked.ID(), number: number, params: atc.Params(normalized), status: atc.RunStatusRunning, createdBy: createdBy, configHash: hashText}
	// New runs are not completed; retain the nullable header value in creation
	// memory so it stays consistent with later header reads.
	var completedAt sql.NullTime
	if err = tx.QueryRow(`INSERT INTO pipeline_runs (id, template_pipeline_id, number, params, status, created_by, config_hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING created_at, completed_at`, runID, locked.ID(), number, paramsJSON, atc.RunStatusRunning, createdBy, hashText).Scan(&run.createdAt, &completedAt); err != nil {
		return RunCreation{}, err
	}
	if completedAt.Valid {
		run.completedAt = &completedAt.Time
	}

	runJobs := make(map[string]runJobMetadata, len(materialized.Config.Jobs))
	for _, job := range materialized.Config.Jobs {
		policyKey := materialized.PolicyKeyByJobName[job.Name]
		if policyKey == "" {
			policyKey = job.Name
		}
		runJobs[job.Name] = runJobMetadata{expected: materialized.ExpectedJobNames[job.Name], policyKey: policyKey}
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
	if opts.BeforeCommit != nil {
		if err = opts.BeforeCommit(tx, creation); err != nil {
			return RunCreation{}, err
		}
	}
	return creation, nil
}

func (f *pipelineRunFactory) effectiveConfig(tx Tx, base *pipeline, override *atc.Config) (atc.Config, error) {
	if override != nil {
		return *override, nil
	}
	jobsRows, err := jobsQuery.Where(sq.Eq{"j.pipeline_id": base.ID(), "j.active": true}).OrderBy("j.id ASC").RunWith(tx).Query()
	if err != nil {
		return atc.Config{}, err
	}
	jobs, err := scanJobs(f.conn, f.lockFactory, jobsRows)
	if err != nil {
		return atc.Config{}, err
	}
	resourcesRows, err := resourcesQuery.Where(sq.Eq{"r.pipeline_id": base.ID()}).OrderBy("r.name").RunWith(tx).Query()
	if err != nil {
		return atc.Config{}, err
	}
	defer Close(resourcesRows)
	resources, err := scanResources(resourcesRows, f.conn, f.lockFactory)
	if err != nil {
		return atc.Config{}, err
	}
	resourceTypes, err := f.resourceTypesInTx(tx, base.ID())
	if err != nil {
		return atc.Config{}, err
	}
	prototypes, err := f.prototypesInTx(tx, base.ID())
	if err != nil {
		return atc.Config{}, err
	}
	jobConfigs, err := jobs.Configs()
	if err != nil {
		return atc.Config{}, err
	}
	return atc.Config{Groups: base.Groups(), VarSources: base.VarSources(), Resources: Resources(resources).Configs(), ResourceTypes: resourceTypes.Configs(), Prototypes: prototypes.Configs(), Jobs: jobConfigs, Display: base.Display(), Template: base.Template(), Params: base.Params(), RunRetention: base.RunRetention()}, nil
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

func (f *pipelineRunFactory) allocateNumber(tx Tx, base *pipeline) (int, error) {
	for {
		var number int
		if err := tx.QueryRow("UPDATE pipelines SET last_run_number = last_run_number + 1 WHERE id = $1 RETURNING last_run_number", base.ID()).Scan(&number); err != nil {
			return 0, err
		}
		instanceVars, _ := json.Marshal(atc.InstanceVars{"run": float64(number)})
		var occupied bool
		err := tx.QueryRow("SELECT EXISTS (SELECT 1 FROM pipelines WHERE team_id = $1 AND name = $2 AND pipeline_run_id IS NULL AND instance_vars = $3::jsonb)", base.TeamID(), base.Name(), string(instanceVars)).Scan(&occupied)
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

func (f *pipelineRunFactory) GetRun(base Pipeline, number int) (PipelineRun, bool, error) {
	return f.getRun(pipelineRunsQuery.Where(sq.Eq{"r.template_pipeline_id": base.ID(), "r.number": number}).RunWith(f.conn).QueryRow())
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

func (f *pipelineRunFactory) Runs(base Pipeline, page Page) ([]PipelineRun, Pagination, error) {
	if page.From != nil && page.To != nil && *page.From > *page.To {
		return nil, Pagination{}, fmt.Errorf("invalid range boundaries")
	}
	tx, err := f.conn.Begin()
	if err != nil {
		return nil, Pagination{}, err
	}
	defer Rollback(tx)

	original := pipelineRunsQuery.Where(sq.Eq{"r.template_pipeline_id": base.ID()})
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
