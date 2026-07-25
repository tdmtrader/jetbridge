package db

import (
	"database/sql"
	"errors"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/agent/snapshot"
)

// AgentRunTranscript is one persisted agent tool-call transcript: the raw
// `claude --output-format stream-json --verbose` stdout the runner captured
// into flight/transcript.ndjson, keyed like agent_run_metrics on
// (build_id, plan_id). This IS the turn-by-turn record of what the agent
// actually did (which dir it edited, whether it committed).
//
// Its execution identity is the v3 pair (workflow_run_id, function_id) —
// the same server-owned identity agent_run_metrics carries. A ticket is a
// work-item adapter and is never an execution identity, so no ticket is
// recorded here (migration 1773106126 re-keyed the table).
type AgentRunTranscript struct {
	BuildID int
	PlanID  string
	// WorkflowRunID is NULL for a pure-CI agent step with no durable run.
	WorkflowRunID *snapshot.WorkflowRunID
	// FunctionID is the workflow function this step executed; "" for an
	// unbound CI invocation.
	FunctionID string
	StepName   string
	NDJSON     string
	ByteLen    int
	Truncated  bool
}

// AgentRunTranscriptFactory persists and reads agent transcripts. Modeled on
// AgentRunMetricsFactory: an ON CONFLICT (build_id, plan_id) upsert so a
// web-restart resume re-ingesting the same flight volume overwrites cleanly.
//
//counterfeiter:generate . AgentRunTranscriptFactory
type AgentRunTranscriptFactory interface {
	Upsert(t AgentRunTranscript) error
	// ListByWorkflowRun returns every transcript of one durable workflow run,
	// oldest-first. Both the workflow name and the run id scope the query
	// (identity + authz).
	ListByWorkflowRun(workflowName string, runID snapshot.WorkflowRunID) ([]AgentRunTranscript, error)
	// GetByBuildAndPlan returns the transcript written by one agent step,
	// addressed by its primary key.
	GetByBuildAndPlan(buildID int, planID string) (AgentRunTranscript, bool, error)
}

func NewAgentRunTranscriptFactory(conn DbConn) AgentRunTranscriptFactory {
	return &agentRunTranscriptFactory{conn: conn}
}

type agentRunTranscriptFactory struct {
	conn DbConn
}

func (f *agentRunTranscriptFactory) Upsert(t AgentRunTranscript) error {
	var workflowRunID any
	if t.WorkflowRunID != nil {
		// pass the concrete int64 so lib/pq's parameter converter never has to
		// reflect over the named type (same rule as workflowRunIDValue).
		workflowRunID = int64(*t.WorkflowRunID)
	}
	_, err := psql.Insert("agent_run_transcripts").
		Columns("build_id", "plan_id", "workflow_run_id", "function_id", "step_name", "ndjson", "byte_len", "truncated").
		Values(t.BuildID, t.PlanID, workflowRunID, t.FunctionID, t.StepName, t.NDJSON, t.ByteLen, t.Truncated).
		Suffix(`ON CONFLICT (build_id, plan_id) DO UPDATE SET
			workflow_run_id = EXCLUDED.workflow_run_id,
			function_id = EXCLUDED.function_id,
			step_name = EXCLUDED.step_name,
			ndjson = EXCLUDED.ndjson,
			byte_len = EXCLUDED.byte_len,
			truncated = EXCLUDED.truncated`).
		RunWith(f.conn).
		Exec()
	return err
}

const runTranscriptColumns = `t.build_id, t.plan_id, t.workflow_run_id, t.function_id,
	t.step_name, t.ndjson, t.byte_len, t.truncated`

// ListByWorkflowRun returns the transcripts of one durable workflow run,
// oldest-first. The run is the transcript's execution identity: an INNER JOIN
// on agent_workflow_runs asserts the row's workflow_run_id points to an
// existing run whose workflow_name matches (identity + authz — a run id under
// the wrong workflow name returns nothing). No join to `builds`: a transcript
// stays readable after its build row is reaped.
func (f *agentRunTranscriptFactory) ListByWorkflowRun(workflowName string, runID snapshot.WorkflowRunID) ([]AgentRunTranscript, error) {
	rows, err := f.conn.Query(
		`SELECT `+runTranscriptColumns+`
		 FROM agent_run_transcripts t
		 JOIN agent_workflow_runs run ON run.id = t.workflow_run_id AND run.workflow_name = $1
		 WHERE t.workflow_run_id = $2 ORDER BY t.created_at ASC, t.build_id ASC`, workflowName, int64(runID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := []AgentRunTranscript{}
	for rows.Next() {
		t, err := scanRunTranscript(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, t)
	}
	return results, rows.Err()
}

func (f *agentRunTranscriptFactory) GetByBuildAndPlan(buildID int, planID string) (AgentRunTranscript, bool, error) {
	row := psql.Select(runTranscriptColumns).
		From("agent_run_transcripts t").
		Where(sq.Eq{"t.build_id": buildID, "t.plan_id": planID}).
		RunWith(f.conn).
		QueryRow()

	t, err := scanRunTranscript(row)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentRunTranscript{}, false, nil
	}
	if err != nil {
		return AgentRunTranscript{}, false, err
	}
	return t, true, nil
}

func scanRunTranscript(src scannable) (AgentRunTranscript, error) {
	var t AgentRunTranscript
	var workflowRunID sql.NullInt64
	var stepName sql.NullString
	err := src.Scan(
		&t.BuildID, &t.PlanID, &workflowRunID, &t.FunctionID,
		&stepName, &t.NDJSON, &t.ByteLen, &t.Truncated,
	)
	if err != nil {
		return AgentRunTranscript{}, err
	}
	if workflowRunID.Valid {
		id := snapshot.WorkflowRunID(workflowRunID.Int64)
		t.WorkflowRunID = &id
	}
	t.StepName = stepName.String
	return t, nil
}
