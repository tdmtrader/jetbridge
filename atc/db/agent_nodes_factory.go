package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/concourse/concourse/agent/workflow"
)

//counterfeiter:generate . AgentNodesFactory
type AgentNodesFactory interface{ workflow.NodeStore }

func NewAgentNodesFactory(conn DbConn) AgentNodesFactory { return &agentNodesFactory{conn: conn} }

type agentNodesFactory struct{ conn DbConn }

const nodeMetaColumns = `id, name, version, content_hash, description, created_by,
	EXTRACT(EPOCH FROM created_at)::bigint, COALESCE(EXTRACT(EPOCH FROM released_at)::bigint, 0),
	released_by, COALESCE(release_predecessor_version, 0), COALESCE(release_compatibility, ''),
	COALESCE(EXTRACT(EPOCH FROM deprecated_at)::bigint, 0), deprecated_by`

func (f *agentNodesFactory) ImportManifest(name string, source workflow.Manifest, createdBy string) (*workflow.NodeDefinition, error) {
	if err := source.Validate(); err != nil {
		return nil, workflow.InvalidDefinitionError{Err: err}
	}
	compiled, err := workflow.CompileNodeDefinition(source)
	if err != nil {
		return nil, workflow.InvalidDefinitionError{Err: err}
	}
	if compiled.Name != name {
		return nil, workflow.InvalidDefinitionError{Err: fmt.Errorf("node definition name %q does not match import name %q", compiled.Name, name)}
	}
	hash := source.Hash()
	tx, err := f.conn.Begin()
	if err != nil {
		return nil, err
	}
	defer Rollback(tx)
	if _, err = tx.Exec(`SELECT pg_advisory_xact_lock(hashtext('agent_node_definitions:' || $1))`, name); err != nil {
		return nil, err
	}
	var d workflow.NodeDefinition
	err = tx.QueryRow(`SELECT `+nodeMetaColumns+` FROM agent_workflow_definitions WHERE definition_kind = 'node' AND name=$1 AND content_hash=$2`, name, hash).Scan(nodeMetaScan(&d)...)
	if err == nil {
		d.Compiled = *compiled
		d.SourceManifest = source
		return &d, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var raw string
	err = tx.QueryRow(`INSERT INTO agent_workflow_definitions
		(definition_kind,name,version,content_hash,definition,source_manifest,description,created_by,schema_version,signature_version)
		SELECT 'node',$1,COALESCE(MAX(version),0)+1,$2,$3,$4::jsonb,$5,$6,3,$7
		FROM agent_workflow_definitions WHERE definition_kind='node' AND name=$1
		RETURNING id, version, EXTRACT(EPOCH FROM created_at)::bigint`, name, hash, source[workflow.NodeFileName], string(source.Canonical()), compiled.Description, createdBy, compiled.Function.SignatureVersion).Scan(&d.ID, &d.Version, &d.CreatedAt)
	if err != nil {
		return nil, err
	}
	d.Name, d.ContentHash, d.Description, d.CreatedBy, d.Compiled, d.SourceManifest = name, hash, compiled.Description, createdBy, *compiled, source
	_ = raw
	return &d, tx.Commit()
}

func (f *agentNodesFactory) Get(name string, version int) (*workflow.NodeDefinition, bool, error) {
	var d workflow.NodeDefinition
	var manifest sql.NullString
	err := f.conn.QueryRow(`SELECT `+nodeMetaColumns+`,source_manifest FROM agent_workflow_definitions WHERE definition_kind='node' AND name=$1 AND version=$2`, name, version).Scan(append(nodeMetaScan(&d), &manifest)...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if err := populateNode(&d, manifest); err != nil {
		return nil, false, err
	}
	return &d, true, nil
}
func (f *agentNodesFactory) Latest(name string) (*workflow.NodeDefinition, bool, error) {
	var d workflow.NodeDefinition
	var m sql.NullString
	err := f.conn.QueryRow(`SELECT `+nodeMetaColumns+`,source_manifest FROM agent_workflow_definitions WHERE definition_kind='node' AND name=$1 ORDER BY version DESC LIMIT 1`, name).Scan(append(nodeMetaScan(&d), &m)...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if err = populateNode(&d, m); err != nil {
		return nil, false, err
	}
	return &d, true, nil
}
func (f *agentNodesFactory) List() ([]workflow.NodeDefinition, error) {
	rows, err := f.conn.Query(`SELECT DISTINCT ON (name) ` + nodeMetaColumns + ` FROM agent_workflow_definitions WHERE definition_kind='node' ORDER BY name,version DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNodeMeta(rows)
}
func (f *agentNodesFactory) Versions(ctx context.Context, name string, r workflow.VersionPageRequest) (workflow.NodeVersionPage, error) {
	if ctx == nil {
		return workflow.NodeVersionPage{}, fmt.Errorf("workflow: version page context is required")
	}
	if r.Limit <= 0 || r.Limit > workflow.MaxVersionPageSize || r.Cursor < 0 || r.Cursor > workflow.MaxWorkflowVersion {
		return workflow.NodeVersionPage{}, workflow.ErrInvalidVersionPage
	}
	var found bool
	if err := f.conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_workflow_definitions WHERE definition_kind='node' AND name=$1)`, name).Scan(&found); err != nil {
		return workflow.NodeVersionPage{}, err
	}
	p := workflow.NodeVersionPage{Found: found, Definitions: []workflow.NodeDefinition{}}
	if !found {
		return p, nil
	}
	rows, err := f.conn.QueryContext(ctx, `SELECT `+nodeMetaColumns+` FROM agent_workflow_definitions WHERE definition_kind='node' AND name=$1 AND ($2=0 OR version<$2) ORDER BY version DESC LIMIT $3`, name, r.Cursor, r.Limit+1)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	defs, err := scanNodeMeta(rows)
	if err != nil {
		return p, err
	}
	if len(defs) > r.Limit {
		defs = defs[:r.Limit]
		p.NextCursor = defs[len(defs)-1].Version
	}
	for i, j := 0, len(defs)-1; i < j; i, j = i+1, j-1 {
		defs[i], defs[j] = defs[j], defs[i]
	}
	p.Definitions = defs
	return p, nil
}
func (f *agentNodesFactory) Released(name string, version int) (workflow.NodeDefinition, bool, error) {
	d, ok, err := f.Get(name, version)
	if err != nil || !ok || d.Release.ReleasedAt == 0 {
		return workflow.NodeDefinition{}, false, err
	}
	return *d, true, nil
}
func (f *agentNodesFactory) Release(name string, version int, c workflow.ReleaseCompatibility, by string) (workflow.NodeRelease, error) {
	if c != workflow.ReleaseCompatible && c != workflow.ReleaseBreaking {
		return workflow.NodeRelease{}, workflow.ErrInvalidCompatibility
	}
	tx, err := f.conn.Begin()
	if err != nil {
		return workflow.NodeRelease{}, err
	}
	defer Rollback(tx)
	if _, err = tx.Exec(`SELECT pg_advisory_xact_lock(hashtext('agent_node_definitions:' || $1))`, name); err != nil {
		return workflow.NodeRelease{}, err
	}
	var target workflow.NodeDefinition
	var m sql.NullString
	err = tx.QueryRow(`SELECT `+nodeMetaColumns+`,source_manifest FROM agent_workflow_definitions WHERE definition_kind='node' AND name=$1 AND version=$2 FOR UPDATE`, name, version).Scan(append(nodeMetaScan(&target), &m)...)
	if errors.Is(err, sql.ErrNoRows) {
		return workflow.NodeRelease{}, workflow.ErrVersionNotFound
	}
	if err != nil {
		return workflow.NodeRelease{}, err
	}
	if target.Release.ReleasedAt != 0 {
		return target.Release, tx.Commit()
	}
	if err = populateNode(&target, m); err != nil {
		return workflow.NodeRelease{}, err
	}
	var prior workflow.NodeDefinition
	var pm sql.NullString
	err = tx.QueryRow(`SELECT `+nodeMetaColumns+`,source_manifest FROM agent_workflow_definitions WHERE definition_kind='node' AND name=$1 AND released_at IS NOT NULL ORDER BY version DESC LIMIT 1 FOR UPDATE`, name).Scan(append(nodeMetaScan(&prior), &pm)...)
	if err == nil {
		if err = populateNode(&prior, pm); err != nil {
			return workflow.NodeRelease{}, err
		}
		if c == workflow.ReleaseCompatible && !nodeCompatible(prior.Compiled, target.Compiled) {
			return workflow.NodeRelease{}, workflow.ErrInvalidCompatibility
		}
		target.Release.PredecessorVersion = prior.Version
	} else if !errors.Is(err, sql.ErrNoRows) {
		return workflow.NodeRelease{}, err
	}
	_, err = tx.Exec(`UPDATE agent_workflow_definitions SET released_at=now(),released_by=$3,release_predecessor_version=$4,release_compatibility=$5 WHERE definition_kind='node' AND name=$1 AND version=$2`, name, version, by, nullableNodePredecessor(target.Release.PredecessorVersion), string(c))
	if err != nil {
		return workflow.NodeRelease{}, err
	}
	target.Release.ReleasedBy, target.Release.Compatibility = by, c
	if err = tx.QueryRow(`SELECT EXTRACT(EPOCH FROM released_at)::bigint FROM agent_workflow_definitions WHERE definition_kind='node' AND name=$1 AND version=$2`, name, version).Scan(&target.Release.ReleasedAt); err != nil {
		return workflow.NodeRelease{}, err
	}
	return target.Release, tx.Commit()
}
func (f *agentNodesFactory) Deprecate(name string, version int, deprecated bool, by string) error {
	q := `UPDATE agent_workflow_definitions SET deprecated_at=CASE WHEN $3 THEN now() ELSE NULL END,deprecated_by=CASE WHEN $3 THEN $4 ELSE '' END WHERE definition_kind='node' AND name=$1 AND version=$2`
	r, e := f.conn.Exec(q, name, version, deprecated, by)
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n == 0 {
		return workflow.ErrVersionNotFound
	}
	return nil
}
func nodeMetaScan(d *workflow.NodeDefinition) []any {
	return []any{&d.ID, &d.Name, &d.Version, &d.ContentHash, &d.Description, &d.CreatedBy, &d.CreatedAt, &d.Release.ReleasedAt, &d.Release.ReleasedBy, &d.Release.PredecessorVersion, &d.Release.Compatibility, &d.DeprecatedAt, &d.DeprecatedBy}
}
func scanNodeMeta(rows *sql.Rows) ([]workflow.NodeDefinition, error) {
	out := []workflow.NodeDefinition{}
	for rows.Next() {
		var d workflow.NodeDefinition
		if err := rows.Scan(nodeMetaScan(&d)...); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func populateNode(d *workflow.NodeDefinition, m sql.NullString) error {
	if !m.Valid {
		return fmt.Errorf("stored node %s/v%d has no source manifest", d.Name, d.Version)
	}
	var source workflow.Manifest
	if err := json.Unmarshal([]byte(m.String), &source); err != nil {
		return err
	}
	compiled, err := workflow.CompileNodeDefinition(source)
	if err != nil {
		return fmt.Errorf("stored node %s/v%d no longer compiles: %w", d.Name, d.Version, err)
	}
	if compiled.Name != d.Name || compiled.Function.SignatureVersion <= 0 {
		return fmt.Errorf("stored metadata for node %q version %d does not match compiled source", d.Name, d.Version)
	}
	d.Compiled = *compiled
	d.SourceManifest = source
	return nil
}
func nullableNodePredecessor(v int) any {
	if v == 0 {
		return nil
	}
	return v
}
func nodeCompatible(previous, candidate workflow.CompiledNodeDefinition) bool {
	in := map[string]struct {
		typ      interface{}
		optional bool
	}{}
	for _, p := range candidate.Function.Inputs {
		in[p.Name] = struct {
			typ      interface{}
			optional bool
		}{p.Type, p.Optional}
	}
	for _, p := range previous.Function.Inputs {
		q, ok := in[p.Name]
		if !ok || q.typ != p.Type || q.optional != p.Optional {
			return false
		}
	}
	for _, p := range candidate.Function.Inputs {
		seen := false
		for _, old := range previous.Function.Inputs {
			if old.Name == p.Name {
				seen = true
			}
		}
		if !seen && !p.Optional {
			return false
		}
	}
	out := map[string]interface{}{}
	for _, p := range candidate.Function.Outputs {
		out[p.Name] = p.Type
	}
	for _, p := range previous.Function.Outputs {
		if q, ok := out[p.Name]; !ok || q != p.Type {
			return false
		}
	}
	params := map[string]*string{}
	for _, p := range candidate.Parameters {
		params[p.Name] = p.Default
	}
	for _, p := range previous.Parameters {
		if _, ok := params[p.Name]; !ok {
			return false
		}
	}
	for _, p := range candidate.Parameters {
		old := false
		for _, q := range previous.Parameters {
			if p.Name == q.Name {
				old = true
			}
		}
		if !old && p.Default == nil {
			return false
		}
	}
	return true
}
