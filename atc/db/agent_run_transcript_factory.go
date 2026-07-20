package db

import (
	"database/sql"
	"errors"

	sq "github.com/Masterminds/squirrel"
)

// AgentRunTranscript is one persisted agent tool-call transcript (ticket
// #43): the raw `claude --output-format stream-json --verbose` stdout the
// runner captured into flight/transcript.ndjson, keyed like
// agent_run_metrics on (build_id, plan_id). This IS the turn-by-turn record
// of what the agent actually did (which dir it edited, whether it committed).
type AgentRunTranscript struct {
	BuildID   int
	PlanID    string
	TicketID  *int   // NULL for pure-CI agent steps / unverified linkage
	StepName  string
	NDJSON    string
	ByteLen   int
	Truncated bool
}

// AgentRunTranscriptFactory persists and reads agent transcripts. Modeled on
// AgentRunMetricsFactory: an ON CONFLICT (build_id, plan_id) upsert so a
// web-restart resume re-ingesting the same flight volume overwrites cleanly.
//
//counterfeiter:generate . AgentRunTranscriptFactory
type AgentRunTranscriptFactory interface {
	Upsert(t AgentRunTranscript) error
	// Get returns the transcript for a ticket's run (build). When a build has
	// multiple agent-step rows, it prefers the implement/agent step over the
	// terminal harvest step; otherwise it returns the earliest row.
	Get(ticketID, buildID int) (ndjson string, found bool, err error)
}

func NewAgentRunTranscriptFactory(conn DbConn) AgentRunTranscriptFactory {
	return &agentRunTranscriptFactory{conn: conn}
}

type agentRunTranscriptFactory struct {
	conn DbConn
}

func (f *agentRunTranscriptFactory) Upsert(t AgentRunTranscript) error {
	var ticketID any
	if t.TicketID != nil {
		ticketID = *t.TicketID
	}
	_, err := psql.Insert("agent_run_transcripts").
		Columns("build_id", "plan_id", "ticket_id", "step_name", "ndjson", "byte_len", "truncated").
		Values(t.BuildID, t.PlanID, ticketID, t.StepName, t.NDJSON, t.ByteLen, t.Truncated).
		Suffix(`ON CONFLICT (build_id, plan_id) DO UPDATE SET
			ticket_id = EXCLUDED.ticket_id,
			step_name = EXCLUDED.step_name,
			ndjson = EXCLUDED.ndjson,
			byte_len = EXCLUDED.byte_len,
			truncated = EXCLUDED.truncated`).
		RunWith(f.conn).
		Exec()
	return err
}

func (f *agentRunTranscriptFactory) Get(ticketID, buildID int) (string, bool, error) {
	// build_id scopes the run; a verifier-rejected agent step carries a NULL
	// ticket_id but still belongs to this build, so allow it through. Prefer
	// the implement/agent step (the interesting transcript) over the harvest
	// step, else the earliest row.
	pred := sq.And{sq.Eq{"build_id": buildID}}
	if ticketID > 0 {
		pred = append(pred, sq.Or{sq.Eq{"ticket_id": ticketID}, sq.Eq{"ticket_id": nil}})
	}

	var ndjson string
	err := psql.Select("ndjson").
		From("agent_run_transcripts").
		Where(pred).
		OrderBy(`CASE WHEN step_name = 'implement' THEN 0 WHEN step_name = 'harvest' THEN 2 ELSE 1 END`, "created_at ASC").
		Limit(1).
		RunWith(f.conn).
		QueryRow().
		Scan(&ndjson)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return ndjson, true, nil
}
