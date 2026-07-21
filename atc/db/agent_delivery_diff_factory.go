package db

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/concourse/concourse/agent/deliverydiff"
	"github.com/concourse/concourse/agent/gitcheck"
)

// AgentDeliveryDiffFactory persists the immutable review diff associated with
// each successfully delivered harvest attempt.
type AgentDeliveryDiffFactory interface {
	deliverydiff.Store
}

func NewAgentDeliveryDiffFactory(conn DbConn) AgentDeliveryDiffFactory {
	return &agentDeliveryDiffFactory{conn: conn}
}

type agentDeliveryDiffFactory struct {
	conn DbConn
}

func (f *agentDeliveryDiffFactory) Upsert(diff deliverydiff.DeliveryDiff) error {
	files, err := json.Marshal(diff.Files)
	if err != nil {
		return err
	}
	var pipelineRunID any
	if diff.PipelineRunID > 0 {
		pipelineRunID = diff.PipelineRunID
	}
	_, err = f.conn.Exec(
		`INSERT INTO agent_delivery_diffs (
		   build_id, plan_id, ticket_id, pipeline_run_id, repo, target_branch,
		   delivered_branch, base_sha, pushed_sha, diff, total_files,
		   captured_files, byte_len, truncated
		 ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		 ON CONFLICT (build_id, plan_id) DO UPDATE SET
		   ticket_id = EXCLUDED.ticket_id,
		   pipeline_run_id = EXCLUDED.pipeline_run_id,
		   repo = EXCLUDED.repo,
		   target_branch = EXCLUDED.target_branch,
		   delivered_branch = EXCLUDED.delivered_branch,
		   base_sha = EXCLUDED.base_sha,
		   pushed_sha = EXCLUDED.pushed_sha,
		   diff = EXCLUDED.diff,
		   total_files = EXCLUDED.total_files,
		   captured_files = EXCLUDED.captured_files,
		   byte_len = EXCLUDED.byte_len,
		   truncated = EXCLUDED.truncated`,
		diff.BuildID, diff.PlanID, diff.TicketID, pipelineRunID, diff.Repo,
		diff.TargetBranch, diff.DeliveredBranch, diff.BaseSHA, diff.PushedSHA,
		files, diff.TotalFiles, diff.CapturedFiles, diff.ByteLen, diff.Truncated,
	)
	return err
}

func (f *agentDeliveryDiffFactory) GetLatest(ticketID int) (deliverydiff.DeliveryDiff, bool, error) {
	var diff deliverydiff.DeliveryDiff
	var pipelineRunID sql.NullInt64
	var files []byte
	err := f.conn.QueryRow(
		`SELECT build_id, plan_id, ticket_id, pipeline_run_id, repo, target_branch,
		        delivered_branch, base_sha, pushed_sha, diff, total_files,
		        captured_files, byte_len, truncated
		 FROM agent_delivery_diffs
		 WHERE ticket_id = $1
		 ORDER BY build_id DESC, plan_id DESC
		 LIMIT 1`,
		ticketID,
	).Scan(
		&diff.BuildID, &diff.PlanID, &diff.TicketID, &pipelineRunID, &diff.Repo,
		&diff.TargetBranch, &diff.DeliveredBranch, &diff.BaseSHA, &diff.PushedSHA,
		&files, &diff.TotalFiles, &diff.CapturedFiles, &diff.ByteLen, &diff.Truncated,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return deliverydiff.DeliveryDiff{}, false, nil
	}
	if err != nil {
		return deliverydiff.DeliveryDiff{}, false, err
	}
	if pipelineRunID.Valid {
		diff.PipelineRunID = int(pipelineRunID.Int64)
	}
	if err := json.Unmarshal(files, &diff.Files); err != nil {
		return deliverydiff.DeliveryDiff{}, false, err
	}
	if diff.Files == nil {
		diff.Files = []gitcheck.DiffFile{}
	}
	return diff, true, nil
}
