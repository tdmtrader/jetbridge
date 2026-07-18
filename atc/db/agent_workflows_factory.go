package db

import (
	"database/sql"
	"encoding/json"
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
	return f.ImportManifest(name, workflow.Manifest{"workflow.yml": string(rawYAML)}, createdBy)
}

// ImportManifest compiles and stores a source tree (design 2026-07-17
// §3): the compiled Config is rebuilt on read from the stored canonical
// manifest, so there is exactly one persisted source of truth per row.
// Idempotent on the canonical-manifest hash.
func (f *agentWorkflowsFactory) ImportManifest(name string, src workflow.Manifest, createdBy string) (*workflow.Definition, error) {
	cfg, err := workflow.Compile(src)
	if err != nil {
		return nil, workflow.InvalidDefinitionError{Err: err}
	}
	if cfg.Name != name {
		return nil, workflow.InvalidDefinitionError{Err: fmt.Errorf("definition name %q does not match import name %q", cfg.Name, name)}
	}
	hash := src.Hash()

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
		SELECT `+workflowMetaColumns+`
		FROM agent_workflow_definitions
		WHERE name = $1 AND content_hash = $2`,
		name, hash,
	).Scan(&def.ID, &def.Name, &def.Version, &def.ContentHash, &def.Live,
		&def.Description, &def.CreatedBy, &def.CreatedAt)
	if err == nil {
		// Idempotent on hash: byte-identical source returns the existing
		// version untouched (contracts §1.6).
		def.Config = *cfg
		def.RawYAML = src["workflow.yml"]
		def.SourceManifest = src
		return &def, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	err = tx.QueryRow(`
		INSERT INTO agent_workflow_definitions
			(name, version, content_hash, definition, source_manifest, description, created_by)
		SELECT $1, COALESCE(MAX(version), 0) + 1, $2, $3, $4::jsonb, $5, $6
		FROM agent_workflow_definitions WHERE name = $1
		RETURNING id, version, EXTRACT(EPOCH FROM created_at)::bigint`,
		name, hash, src["workflow.yml"], string(src.Canonical()), cfg.Description, createdBy,
	).Scan(&def.ID, &def.Version, &def.CreatedAt)
	if err != nil {
		return nil, err
	}

	def.Name = name
	def.ContentHash = hash
	def.Description = cfg.Description
	def.CreatedBy = createdBy
	def.RawYAML = src["workflow.yml"]
	def.SourceManifest = src
	def.Config = *cfg
	return &def, tx.Commit()
}

func (f *agentWorkflowsFactory) Get(name string, version int) (*workflow.Definition, bool, error) {
	return f.getOne(`name = $1 AND version = $2`, name, version)
}

func (f *agentWorkflowsFactory) Live(name string) (*workflow.Definition, bool, error) {
	return f.getOne(`name = $1 AND live`, name)
}

func (f *agentWorkflowsFactory) Latest(name string) (*workflow.Definition, bool, error) {
	return f.getOne(`name = $1 ORDER BY version DESC LIMIT 1`, name)
}

// LiveVersions resolves every workflow's live version in ONE metadata query —
// list consumers previously called Live(name) per non-live-latest name, each
// dragging and parsing the full definition YAML just to read a version number.
func (f *agentWorkflowsFactory) LiveVersions() (map[string]int, error) {
	rows, err := f.conn.Query(`SELECT name, version FROM agent_workflow_definitions WHERE live`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var name string
		var version int
		if err := rows.Scan(&name, &version); err != nil {
			return nil, err
		}
		out[name] = version
	}
	return out, rows.Err()
}

func (f *agentWorkflowsFactory) getOne(where string, args ...any) (*workflow.Definition, bool, error) {
	var def workflow.Definition
	var manifestJSON sql.NullString
	err := f.conn.QueryRow(`
		SELECT `+workflowMetaColumns+`, definition, source_manifest
		FROM agent_workflow_definitions
		WHERE `+where, args...,
	).Scan(&def.ID, &def.Name, &def.Version, &def.ContentHash, &def.Live,
		&def.Description, &def.CreatedBy, &def.CreatedAt, &def.RawYAML, &manifestJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if manifestJSON.Valid {
		var src workflow.Manifest
		if err := json.Unmarshal([]byte(manifestJSON.String), &src); err != nil {
			return nil, false, fmt.Errorf("stored manifest %s/v%d no longer parses: %w", def.Name, def.Version, err)
		}
		cfg, err := workflow.Compile(src)
		if err != nil {
			// Rows are compiled at import; a failure here means the stored
			// manifest was corrupted out-of-band.
			return nil, false, fmt.Errorf("stored manifest %s/v%d no longer compiles: %w", def.Name, def.Version, err)
		}
		def.Config = *cfg
		def.SourceManifest = src
		return &def, true, nil
	}
	// Legacy pre-manifest row: the definition column is the whole source.
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
	rows, err := f.conn.Query(`
		SELECT DISTINCT ON (name) ` + workflowMetaColumns + `
		FROM agent_workflow_definitions
		ORDER BY name, version DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanWorkflowMetaRows(rows)
}

func (f *agentWorkflowsFactory) Versions(name string) ([]workflow.Definition, error) {
	rows, err := f.conn.Query(`
		SELECT `+workflowMetaColumns+`
		FROM agent_workflow_definitions
		WHERE name = $1
		ORDER BY version ASC`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanWorkflowMetaRows(rows)
}

func (f *agentWorkflowsFactory) Promote(name string, version int, promotedBy string) error {
	tx, err := f.conn.Begin()
	if err != nil {
		return err
	}
	defer Rollback(tx)

	// Serialize promotions per name (same key as Import) so concurrent
	// clear-then-set sequences don't race into a unique violation on
	// agent_workflow_definitions_live.
	_, err = tx.Exec(`SELECT pg_advisory_xact_lock(hashtext('agent_workflow_definitions:' || $1))`, name)
	if err != nil {
		return err
	}

	// Clear-then-set inside one tx: the partial unique index
	// agent_workflow_definitions_live enforces at most one live row per
	// name at every intermediate statement.
	_, err = tx.Exec(`UPDATE agent_workflow_definitions SET live = false WHERE name = $1 AND live`, name)
	if err != nil {
		return err
	}

	res, err := tx.Exec(`
		UPDATE agent_workflow_definitions
		SET live = true, promoted_at = now(), promoted_by = $3
		WHERE name = $1 AND version = $2`,
		name, version, promotedBy)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return workflow.ErrVersionNotFound
	}
	return tx.Commit()
}

func scanWorkflowMetaRows(rows *sql.Rows) ([]workflow.Definition, error) {
	out := []workflow.Definition{}
	for rows.Next() {
		var def workflow.Definition
		if err := rows.Scan(&def.ID, &def.Name, &def.Version, &def.ContentHash, &def.Live,
			&def.Description, &def.CreatedBy, &def.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, def)
	}
	return out, rows.Err()
}
