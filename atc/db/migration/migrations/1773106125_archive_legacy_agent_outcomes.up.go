package migrations

import "fmt"

// Up_1773106125 retires the legacy agent_outcomes compatibility table: it
// renames it to agent_legacy_outcomes_archive as inert audit data and creates
// the agent_legacy_outcomes_unresolved report alongside it. Every production
// reader of agent_outcomes was removed before this migration, so this is a
// one-way archival; durable dispositions live in agent_workflow_outcomes.
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

	if _, err := m.Exec(`ALTER TABLE agent_outcomes RENAME TO agent_legacy_outcomes_archive`); err != nil {
		return fmt.Errorf("archive legacy outcomes: rename agent_outcomes: %w", err)
	}
	return nil
}
