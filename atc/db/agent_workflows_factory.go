package db

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/concourse/concourse/agent/workflow"
)

//counterfeiter:generate . AgentWorkflowsFactory
type AgentWorkflowsFactory interface {
	workflow.Store
}

func NewAgentWorkflowsFactory(conn DbConn) AgentWorkflowsFactory {
	return &agentWorkflowsFactory{conn: conn}
}

type agentWorkflowsFactory struct {
	conn DbConn
}

const workflowMetaColumns = `id, name, version, content_hash, live, description, created_by,
	EXTRACT(EPOCH FROM created_at)::bigint`

func (f *agentWorkflowsFactory) Import(name string, rawYAML []byte, createdBy string) (*workflow.Definition, error) {
	cfg, err := workflow.Parse(rawYAML)
	if err != nil {
		return nil, workflow.InvalidDefinitionError{Err: err}
	}
	if cfg.Name != name {
		return nil, workflow.InvalidDefinitionError{Err: fmt.Errorf("definition name %q does not match import name %q", cfg.Name, name)}
	}
	hash := workflow.Hash(rawYAML)

	tx, err := f.conn.Begin()
	if err != nil {
		return nil, err
	}
	defer Rollback(tx)

	// Serialize imports per name so version assignment is race-free
	// under concurrent web nodes.
	_, err = tx.Exec(`SELECT pg_advisory_xact_lock(hashtext('agent_workflow_definitions:' || $1))`, name)
	if err != nil {
		return nil, err
	}

	var def workflow.Definition
	err = tx.QueryRow(`
		SELECT `+workflowMetaColumns+`, definition
		FROM agent_workflow_definitions
		WHERE name = $1 AND content_hash = $2`,
		name, hash,
	).Scan(&def.ID, &def.Name, &def.Version, &def.ContentHash, &def.Live,
		&def.Description, &def.CreatedBy, &def.CreatedAt, &def.RawYAML)
	if err == nil {
		// Idempotent on hash: byte-identical YAML returns the existing
		// version untouched (contracts §1.6).
		def.Config = *cfg
		return &def, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	err = tx.QueryRow(`
		INSERT INTO agent_workflow_definitions
			(name, version, content_hash, definition, description, created_by)
		SELECT $1, COALESCE(MAX(version), 0) + 1, $2, $3, $4, $5
		FROM agent_workflow_definitions WHERE name = $1
		RETURNING id, version, EXTRACT(EPOCH FROM created_at)::bigint`,
		name, hash, string(rawYAML), cfg.Description, createdBy,
	).Scan(&def.ID, &def.Version, &def.CreatedAt)
	if err != nil {
		return nil, err
	}

	def.Name = name
	def.ContentHash = hash
	def.Description = cfg.Description
	def.CreatedBy = createdBy
	def.RawYAML = string(rawYAML)
	def.Config = *cfg
	return &def, tx.Commit()
}

func (f *agentWorkflowsFactory) Get(name string, version int) (*workflow.Definition, bool, error) {
	return f.getOne(`name = $1 AND version = $2`, name, version)
}

func (f *agentWorkflowsFactory) Live(name string) (*workflow.Definition, bool, error) {
	return f.getOne(`name = $1 AND live`, name)
}

func (f *agentWorkflowsFactory) getOne(where string, args ...any) (*workflow.Definition, bool, error) {
	var def workflow.Definition
	err := f.conn.QueryRow(`
		SELECT `+workflowMetaColumns+`, definition
		FROM agent_workflow_definitions
		WHERE `+where, args...,
	).Scan(&def.ID, &def.Name, &def.Version, &def.ContentHash, &def.Live,
		&def.Description, &def.CreatedBy, &def.CreatedAt, &def.RawYAML)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	cfg, err := workflow.Parse([]byte(def.RawYAML))
	if err != nil {
		// Rows are validated at import; a parse failure here means the
		// stored bytes were corrupted out-of-band.
		return nil, false, fmt.Errorf("stored definition %s/v%d no longer parses: %w", def.Name, def.Version, err)
	}
	def.Config = *cfg
	return &def, true, nil
}

func (f *agentWorkflowsFactory) List() ([]workflow.Definition, error) {
	return nil, errors.New("not implemented") // Task 7
}

func (f *agentWorkflowsFactory) Versions(name string) ([]workflow.Definition, error) {
	return nil, errors.New("not implemented") // Task 7
}

func (f *agentWorkflowsFactory) Promote(name string, version int, promotedBy string) error {
	return errors.New("not implemented") // Task 7
}
