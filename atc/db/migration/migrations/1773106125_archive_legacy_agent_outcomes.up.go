package migrations

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/concourse/concourse/atc/db/migration/legacyworkflow"
)

// Up_1773106125 migrates every legacy agent_outcomes row that can be resolved
// exactly into the durable agent_workflow_outcomes table, sets aside the rest
// as inert audit data in agent_legacy_outcomes_unresolved, and renames the
// compatibility table to agent_legacy_outcomes_archive. Task 3 already removed
// every production reader of agent_outcomes, so this is a one-way archival.
//
// The migration never infers an owning run or output by type, insertion order,
// or timestamp, and it never overwrites a native workflow outcome: inserts use
// ON CONFLICT DO NOTHING so a pre-existing generic outcome always wins.
func (m *migrations) Up_1773106125() error {
	if _, err := m.Exec(`
		CREATE TABLE agent_legacy_outcomes_unresolved (
			ticket_id   INTEGER PRIMARY KEY,
			reason      TEXT NOT NULL,
			recorded_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		return fmt.Errorf("archive legacy outcomes: create unresolved report: %w", err)
	}

	rows, err := m.Query(`SELECT ticket_id, merge_state, disposition FROM agent_outcomes ORDER BY ticket_id`)
	if err != nil {
		return fmt.Errorf("archive legacy outcomes: read agent_outcomes: %w", err)
	}
	legacy := []legacyOutcomeRow{}
	for rows.Next() {
		var row legacyOutcomeRow
		if err := rows.Scan(&row.ticketID, &row.mergeState, &row.disposition); err != nil {
			_ = rows.Close()
			return fmt.Errorf("archive legacy outcomes: scan agent_outcomes row: %w", err)
		}
		legacy = append(legacy, row)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("archive legacy outcomes: iterate agent_outcomes: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("archive legacy outcomes: close agent_outcomes rows: %w", err)
	}

	for _, row := range legacy {
		if err := m.migrateLegacyOutcome(row); err != nil {
			return err
		}
	}

	if _, err := m.Exec(`ALTER TABLE agent_outcomes RENAME TO agent_legacy_outcomes_archive`); err != nil {
		return fmt.Errorf("archive legacy outcomes: rename agent_outcomes: %w", err)
	}
	return nil
}

type legacyOutcomeRow struct {
	ticketID    int
	mergeState  string
	disposition string
}

// migrateLegacyOutcome resolves one legacy row exactly or records it as
// unresolved. Only infrastructure failures propagate as errors; every
// resolution refusal is recorded and returns nil.
func (m *migrations) migrateLegacyOutcome(row legacyOutcomeRow) error {
	runID, teamID, definitionID, reason, err := m.resolveLegacyOutcomeRun(row.ticketID)
	if err != nil {
		return err
	}
	if reason != "" {
		return m.recordLegacyOutcomeUnresolved(row.ticketID, reason)
	}

	outputID, reason, err := m.resolveLegacyOutcomeOutput(runID, definitionID)
	if err != nil {
		return err
	}
	if reason != "" {
		return m.recordLegacyOutcomeUnresolved(row.ticketID, reason)
	}

	generic, humanModified, interventions, ok := mapLegacyDisposition(
		effectiveLegacyDisposition(row.mergeState, row.disposition))
	if !ok {
		return m.recordLegacyOutcomeUnresolved(row.ticketID, "unmappable-disposition")
	}

	if _, err := m.Exec(`
		INSERT INTO agent_workflow_outcomes
			(team_id, workflow_run_id, output_snapshot_id, disposition, publication_state,
			 human_modified, intervention_count, labels, actor)
		VALUES ($1, $2, $3, $4, 'not_requested', $5, $6,
		        '["legacy-ticket-migrated"]'::jsonb, 'legacy-outcome-migration')
		ON CONFLICT (workflow_run_id, output_snapshot_id) DO NOTHING
	`, teamID, runID, outputID, generic, humanModified, interventions); err != nil {
		return fmt.Errorf("archive legacy outcomes: insert workflow outcome for ticket %d: %w", row.ticketID, err)
	}
	return nil
}

// resolveLegacyOutcomeRun finds the exact durable run for a legacy ticket. It
// prefers the exact agent_tickets.workflow_run_id adapter link, then falls back
// to a single historical ticket-origin run. Zero or multiple candidates are a
// refusal, never a guess.
func (m *migrations) resolveLegacyOutcomeRun(ticketID int) (runID int64, teamID, definitionID int, reason string, err error) {
	var exact sql.NullInt64
	scanErr := m.QueryRow(`SELECT workflow_run_id FROM agent_tickets WHERE id = $1`, ticketID).Scan(&exact)
	switch {
	case scanErr == sql.ErrNoRows:
		// No adapter row; fall through to the origin fallback.
	case scanErr != nil:
		return 0, 0, 0, "", fmt.Errorf("archive legacy outcomes: read ticket %d link: %w", ticketID, scanErr)
	case exact.Valid:
		return m.legacyOutcomeRunIdentity(exact.Int64, ticketID)
	}

	candidateRows, queryErr := m.Query(`
		SELECT id FROM agent_workflow_runs
		WHERE origin_kind = 'ticket' AND origin_reference = $1
		ORDER BY id
	`, strconv.Itoa(ticketID))
	if queryErr != nil {
		return 0, 0, 0, "", fmt.Errorf("archive legacy outcomes: read ticket-origin runs for %d: %w", ticketID, queryErr)
	}
	var candidates []int64
	for candidateRows.Next() {
		var id int64
		if err := candidateRows.Scan(&id); err != nil {
			_ = candidateRows.Close()
			return 0, 0, 0, "", fmt.Errorf("archive legacy outcomes: scan ticket-origin run for %d: %w", ticketID, err)
		}
		candidates = append(candidates, id)
	}
	if err := candidateRows.Err(); err != nil {
		_ = candidateRows.Close()
		return 0, 0, 0, "", fmt.Errorf("archive legacy outcomes: iterate ticket-origin runs for %d: %w", ticketID, err)
	}
	if err := candidateRows.Close(); err != nil {
		return 0, 0, 0, "", fmt.Errorf("archive legacy outcomes: close ticket-origin runs for %d: %w", ticketID, err)
	}

	switch len(candidates) {
	case 0:
		return 0, 0, 0, "missing-run", nil
	case 1:
		return m.legacyOutcomeRunIdentity(candidates[0], ticketID)
	default:
		return 0, 0, 0, "ambiguous-run", nil
	}
}

func (m *migrations) legacyOutcomeRunIdentity(runID int64, ticketID int) (int64, int, int, string, error) {
	var teamID, definitionID int
	if err := m.QueryRow(`
		SELECT team_id, workflow_definition_id FROM agent_workflow_runs WHERE id = $1
	`, runID).Scan(&teamID, &definitionID); err != nil {
		return 0, 0, 0, "", fmt.Errorf("archive legacy outcomes: read run %d for ticket %d: %w", runID, ticketID, err)
	}
	return runID, teamID, definitionID, "", nil
}

// resolveLegacyOutcomeOutput selects the exact output snapshot that owns the
// disposition. When the pinned schema-v3 source names a disposition_output the
// bound output snapshot at that port is used; otherwise the run must have
// exactly one promoted public output.
func (m *migrations) resolveLegacyOutcomeOutput(runID int64, definitionID int) (int64, string, error) {
	dispositionOutput, decodable, err := m.legacyOutcomeDispositionOutput(definitionID)
	if err != nil {
		return 0, "", err
	}
	if !decodable {
		return 0, "undecodable-source", nil
	}

	if dispositionOutput != "" {
		var snapshotID int64
		scanErr := m.QueryRow(`
			SELECT snapshot_id FROM agent_workflow_run_snapshots
			WHERE workflow_run_id = $1 AND direction = 'output' AND port_name = $2
		`, runID, dispositionOutput).Scan(&snapshotID)
		if scanErr == sql.ErrNoRows {
			return 0, "missing-disposition-output", nil
		}
		if scanErr != nil {
			return 0, "", fmt.Errorf("archive legacy outcomes: read disposition output for run %d: %w", runID, scanErr)
		}
		return snapshotID, "", nil
	}

	outputRows, queryErr := m.Query(`
		SELECT snapshot_id FROM agent_workflow_run_snapshots
		WHERE workflow_run_id = $1 AND direction = 'output'
		ORDER BY snapshot_id
	`, runID)
	if queryErr != nil {
		return 0, "", fmt.Errorf("archive legacy outcomes: read outputs for run %d: %w", runID, queryErr)
	}
	var outputs []int64
	for outputRows.Next() {
		var id int64
		if err := outputRows.Scan(&id); err != nil {
			_ = outputRows.Close()
			return 0, "", fmt.Errorf("archive legacy outcomes: scan output for run %d: %w", runID, err)
		}
		outputs = append(outputs, id)
	}
	if err := outputRows.Err(); err != nil {
		_ = outputRows.Close()
		return 0, "", fmt.Errorf("archive legacy outcomes: iterate outputs for run %d: %w", runID, err)
	}
	if err := outputRows.Close(); err != nil {
		return 0, "", fmt.Errorf("archive legacy outcomes: close outputs for run %d: %w", runID, err)
	}
	if len(outputs) != 1 {
		return 0, "ambiguous-output", nil
	}
	return outputs[0], "", nil
}

// legacyOutcomeDispositionOutput decodes the pinned source manifest for a
// definition with the migration-local decoder and returns its disposition
// output. decodable is false when the stored source cannot be compiled, which
// callers treat as a resolution refusal rather than a migration failure.
func (m *migrations) legacyOutcomeDispositionOutput(definitionID int) (result string, decodable bool, err error) {
	var definition string
	var manifest sql.NullString
	if scanErr := m.QueryRow(`
		SELECT definition, source_manifest FROM agent_workflow_definitions WHERE id = $1
	`, definitionID).Scan(&definition, &manifest); scanErr != nil {
		return "", false, fmt.Errorf("archive legacy outcomes: read definition %d: %w", definitionID, scanErr)
	}
	source := map[string]string{"workflow.yml": definition}
	if manifest.Valid {
		if json.Unmarshal([]byte(manifest.String), &source) != nil {
			return "", false, nil
		}
	}
	metadata, _, decodeErr := legacyworkflow.DecodeManifest(source)
	if decodeErr != nil {
		return "", false, nil
	}
	return metadata.DispositionOutput, true, nil
}

func (m *migrations) recordLegacyOutcomeUnresolved(ticketID int, reason string) error {
	if _, err := m.Exec(`
		INSERT INTO agent_legacy_outcomes_unresolved (ticket_id, reason)
		VALUES ($1, $2)
		ON CONFLICT (ticket_id) DO NOTHING
	`, ticketID, reason); err != nil {
		return fmt.Errorf("archive legacy outcomes: record unresolved ticket %d: %w", ticketID, err)
	}
	return nil
}

// effectiveLegacyDisposition collapses the two legacy columns (merge_state and
// disposition) into the single terminal disposition they encode. merge_state's
// merge signals win; otherwise the disposition column decides.
func effectiveLegacyDisposition(mergeState, disposition string) string {
	switch mergeState {
	case "merged":
		return "merged"
	case "merged_with_fixes":
		return "merged_with_fixes"
	}
	switch disposition {
	case "sent_back":
		return "sent_back"
	case "abandoned":
		return "abandoned"
	case "concluded":
		return "concluded"
	}
	if mergeState == "concluded" {
		return "concluded"
	}
	return ""
}

// mapLegacyDisposition maps an exact legacy disposition to its generic
// equivalent. The final bool is false for any value with no exact mapping.
func mapLegacyDisposition(value string) (string, bool, int, bool) {
	switch value {
	case "merged":
		return "merged", false, 0, true
	case "merged_with_fixes":
		return "merged", true, 1, true
	case "sent_back":
		return "rejected", false, 0, true
	case "abandoned":
		return "abandoned", false, 0, true
	case "concluded":
		return "accepted", false, 0, true
	default:
		return "", false, 0, false
	}
}
