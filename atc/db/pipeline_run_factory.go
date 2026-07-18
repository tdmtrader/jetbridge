package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db/lock"
)

// ErrNotATemplate is returned when CreateRun is called on a pipeline that is
// not a base template (template: true, no instance vars).
var ErrNotATemplate = errors.New("pipeline is not a template")

// ErrTemplateNotFound is returned when the template pipeline id is unknown.
var ErrTemplateNotFound = errors.New("template pipeline not found")

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
	GetRun(templatePipelineID, number int) (PipelineRun, bool, error)
	ListRuns(templatePipelineID int, limit int) ([]PipelineRun, error)
	RunningRuns() ([]PipelineRun, error)
	CompletedRunsWithNewActivity() ([]PipelineRun, error)
	RunsToArchive() ([]PipelineRun, error)

	// RunBelongsToPipeline reports whether pipeline_runs row `runID` was
	// materialized as pipeline instance `pipelineID`. The agent-step exec
	// gates §8.2 credential attachment on this: AGENT_PIPELINE_RUN_ID arrives
	// via attacker-writable plan env (F30), so a run id may only name the
	// `agent-run-<id>` secret when its instance pipeline is the very pipeline
	// this build runs in — otherwise any team could mount another run's
	// principal and Anthropic tokens into a sidecar it named "gateway".
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

	// RunsForTerminalTickets returns unarchived, no-longer-running runs
	// whose template pipeline belongs to a terminally-disposed agent ticket
	// (C3, UI audit 2026-07-17). Scoped by template rather than the latest
	// attempt: a requeued ticket leaves one run instance per attempt and
	// every one of them is a dead dashboard card once the ticket is
	// terminal. The linkage is pinned to the ticket's own
	// `agent-ticket-<id>` template on the main team — pipeline_run_id is
	// caller-writable (F30) and archival is destructive, so an unpinned
	// join would archive arbitrary victim pipelines. Still-running runs are
	// held back so the lifecycler's Finish pass completes them first; both
	// return empty on a pre-ticket-core DB.
	RunsForTerminalTickets() ([]PipelineRun, error)

	// TemplatesForTerminalTickets returns the unarchived base template
	// pipelines (template = true, no instance vars) of terminally-disposed
	// agent tickets, held back while any of their runs is still
	// aggregate-running. Without this the permanently-gray template card
	// keeps rendering after every run instance is archived.
	TemplatesForTerminalTickets() ([]Pipeline, error)
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

	tx, err := f.conn.Begin()
	if err != nil {
		return nil, err
	}
	defer Rollback(tx)

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

		// F27 (2026-07-09): the frozen check set is enqueued HERE, by the
		// factory, per shared-contracts §7.1 item 2 — not in the API handler.
		f.enqueueInitialChecks(instance)
	}

	return run, nil
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

// terminalTicketLinkage is the shared subquery: template pipeline ids
// reachable from a terminal ticket through its dispatched run
// (agent_tickets.pipeline_run_id → pipeline_runs.template_pipeline_id).
// The linkage additionally requires the template to be the ticket's OWN
// `agent-ticket-<id>` pipeline on the main team (the dispatch naming
// convention, agent/dispatch/dispatch.go). pipeline_run_id is
// caller-writable through PUT .../state (same F30 id class that
// TicketBelongsToRun exists for), and this is a DESTRUCTIVE consumer — an
// unconstrained join would let a tickets:write principal point a ticket at
// an arbitrary victim run and have the lifecycler archive that victim's
// template, re-archiving it every tick forever. With the name+team pin, a
// poisoned run id can at worst archive the ticket's own pipelines, which
// is what happens at terminal anyway.
func terminalTicketLinkage() (string, []any) {
	states := tickets.TerminalStates()
	marks := make([]string, len(states))
	args := make([]any, len(states))
	for i, s := range states {
		marks[i] = "?"
		args[i] = string(s)
	}
	args = append(args, atc.DefaultTeamName)
	return fmt.Sprintf(`(
		SELECT r0.template_pipeline_id
		FROM agent_tickets t
		JOIN pipeline_runs r0 ON r0.id = t.pipeline_run_id
		JOIN pipelines p0 ON p0.id = r0.template_pipeline_id
		JOIN teams tm0 ON tm0.id = p0.team_id
		WHERE t.state IN (%s)
		  AND p0.name = 'agent-ticket-' || t.id::text
		  AND tm0.name = ?)`, strings.Join(marks, ",")), args
}

func (f *pipelineRunFactory) RunsForTerminalTickets() ([]PipelineRun, error) {
	tableExists, err := f.agentTicketsTableExists()
	if err != nil {
		return nil, err
	}
	if !tableExists {
		return nil, nil
	}

	linkage, args := terminalTicketLinkage()
	rows, err := pipelineRunsQuery.
		Where(sq.Eq{"r.archived": false}).
		Where(sq.NotEq{"r.status": string(PipelineRunRunning)}).
		Where(sq.Expr("r.template_pipeline_id IN "+linkage, args...)).
		OrderBy("r.id ASC").
		RunWith(f.conn).
		Query()
	if err != nil {
		return nil, err
	}
	return f.scanRuns(rows)
}

func (f *pipelineRunFactory) TemplatesForTerminalTickets() ([]Pipeline, error) {
	tableExists, err := f.agentTicketsTableExists()
	if err != nil {
		return nil, err
	}
	if !tableExists {
		return nil, nil
	}

	linkage, args := terminalTicketLinkage()
	rows, err := pipelinesQuery.
		Where(sq.Eq{"p.archived": false, "p.template": true}).
		Where(sq.Expr("p.instance_vars IS NULL")).
		Where(sq.Expr("p.id IN "+linkage, args...)).
		// same hold-back as the runs pass: while any run of the template
		// is still aggregate-running, leave the template so the group
		// converges as one once the Finish pass completes the run.
		Where(sq.Expr(`NOT EXISTS (
			SELECT 1 FROM pipeline_runs r1
			WHERE r1.template_pipeline_id = p.id AND r1.status = 'running')`)).
		OrderBy("p.id ASC").
		RunWith(f.conn).
		Query()
	if err != nil {
		return nil, err
	}
	return scanPipelines(f.conn, f.lockFactory, rows)
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
