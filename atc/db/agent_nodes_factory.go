package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/agent/workflow"
)

type AgentNodesFactory interface{ workflow.NodeStore }

func NewAgentNodesFactory(conn DbConn) AgentNodesFactory { return &agentNodesFactory{conn: conn} }

func NewAgentNodesFactoryWithBrokerCatalog(conn DbConn, catalog *broker.Catalog) AgentNodesFactory {
	if catalog == nil {
		panic("db: broker-aware node factory requires a catalog")
	}
	return &agentNodesFactory{conn: conn, brokerCatalog: catalog}
}

type agentNodesFactory struct {
	conn          DbConn
	brokerCatalog *broker.Catalog
}

type agentNodeRowQuerier interface {
	QueryRow(string, ...any) squirrel.RowScanner
}

type transactionAgentNodeResolver struct {
	querier agentNodeRowQuerier
}

func (f *agentNodesFactory) nodeResolverForTransaction(tx Tx) workflow.NodeResolver {
	return transactionAgentNodeResolver{querier: tx}
}

func (resolver transactionAgentNodeResolver) Released(name string, version int) (workflow.NodeDefinition, bool, error) {
	return releasedAgentNode(resolver.querier, name, version)
}

const nodeMetaColumns = `id, name, version, content_hash, description, created_by,
	EXTRACT(EPOCH FROM created_at)::bigint, COALESCE(EXTRACT(EPOCH FROM released_at)::bigint, 0),
	released_by, COALESCE(release_predecessor_version, 0), COALESCE(release_compatibility, ''),
	COALESCE(EXTRACT(EPOCH FROM deprecated_at)::bigint, 0), deprecated_by`

const nodeStoredColumns = nodeMetaColumns + `, schema_version, signature_version, source_manifest, compiled_definition`

const nodeRuntimeSchemaVersion = 3

type storedNodeDefinition struct {
	definition       workflow.NodeDefinition
	schemaVersion    int
	signatureVersion int
	sourceManifest   sql.NullString
	compiled         sql.NullString
}

func (f *agentNodesFactory) ImportManifest(name string, source workflow.Manifest, createdBy string) (*workflow.NodeDefinition, error) {
	if err := source.Validate(); err != nil {
		return nil, workflow.InvalidDefinitionError{Err: err}
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
	var stored storedNodeDefinition
	err = tx.QueryRow(`SELECT `+nodeStoredColumns+` FROM agent_workflow_definitions WHERE definition_kind = 'node' AND name=$1 AND content_hash=$2`, name, hash).Scan(nodeStoredScan(&stored)...)
	if err == nil {
		if err = populateStoredNode(&stored); err != nil {
			return nil, err
		}
		return &stored.definition, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var compiled *workflow.CompiledNodeDefinition
	if f.brokerCatalog == nil {
		compiled, err = workflow.CompileNodeDefinition(source)
	} else {
		compiled, err = workflow.CompileNodeDefinitionWithBrokerCatalog(source, f.brokerCatalog)
	}
	if err != nil {
		return nil, workflow.InvalidDefinitionError{Err: err}
	}
	if compiled.Name != name {
		return nil, workflow.InvalidDefinitionError{Err: fmt.Errorf("node definition name %q does not match import name %q", compiled.Name, name)}
	}
	compiledJSON, err := json.Marshal(compiled)
	if err != nil {
		return nil, fmt.Errorf("encode compiled node definition: %w", err)
	}
	var d workflow.NodeDefinition
	err = tx.QueryRow(`INSERT INTO agent_workflow_definitions
		(definition_kind,name,version,content_hash,definition,source_manifest,compiled_definition,description,created_by,schema_version,signature_version)
		SELECT 'node',$1,COALESCE(MAX(version),0)+1,$2,$3,$4::jsonb,$5::jsonb,$6,$7,3,$8
		FROM agent_workflow_definitions WHERE definition_kind='node' AND name=$1
		RETURNING id, version, EXTRACT(EPOCH FROM created_at)::bigint`, name, hash, source[workflow.NodeFileName], string(source.Canonical()), string(compiledJSON), compiled.Description, createdBy, compiled.Function.SignatureVersion).Scan(&d.ID, &d.Version, &d.CreatedAt)
	if err != nil {
		return nil, err
	}
	d.Name, d.ContentHash, d.Description, d.CreatedBy, d.Compiled, d.SourceManifest = name, hash, compiled.Description, createdBy, *compiled, source
	return &d, tx.Commit()
}

func (f *agentNodesFactory) Get(name string, version int) (*workflow.NodeDefinition, bool, error) {
	return getAgentNode(f.conn, name, version)
}

func getAgentNode(querier agentNodeRowQuerier, name string, version int) (*workflow.NodeDefinition, bool, error) {
	var stored storedNodeDefinition
	err := querier.QueryRow(`SELECT `+nodeStoredColumns+` FROM agent_workflow_definitions WHERE definition_kind='node' AND name=$1 AND version=$2`, name, version).Scan(nodeStoredScan(&stored)...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if err := populateStoredNode(&stored); err != nil {
		return nil, false, err
	}
	return &stored.definition, true, nil
}
func (f *agentNodesFactory) Latest(name string) (*workflow.NodeDefinition, bool, error) {
	var stored storedNodeDefinition
	err := f.conn.QueryRow(`SELECT `+nodeStoredColumns+` FROM agent_workflow_definitions WHERE definition_kind='node' AND name=$1 ORDER BY version DESC LIMIT 1`, name).Scan(nodeStoredScan(&stored)...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if err = populateStoredNode(&stored); err != nil {
		return nil, false, err
	}
	return &stored.definition, true, nil
}
func (f *agentNodesFactory) List() ([]workflow.NodeDefinition, error) {
	rows, err := f.conn.Query(`SELECT DISTINCT ON (name) ` + nodeStoredColumns + ` FROM agent_workflow_definitions WHERE definition_kind='node' ORDER BY name,version DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanStoredNodes(rows)
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
	rows, err := f.conn.QueryContext(ctx, `SELECT `+nodeStoredColumns+` FROM agent_workflow_definitions WHERE definition_kind='node' AND name=$1 AND ($2=0 OR version<$2) ORDER BY version DESC LIMIT $3`, name, r.Cursor, r.Limit+1)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	defs, err := scanStoredNodes(rows)
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
	return releasedAgentNode(f.conn, name, version)
}

func releasedAgentNode(querier agentNodeRowQuerier, name string, version int) (workflow.NodeDefinition, bool, error) {
	d, ok, err := getAgentNode(querier, name, version)
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
	var storedTarget storedNodeDefinition
	err = tx.QueryRow(`SELECT `+nodeStoredColumns+` FROM agent_workflow_definitions WHERE definition_kind='node' AND name=$1 AND version=$2 FOR UPDATE`, name, version).Scan(nodeStoredScan(&storedTarget)...)
	if errors.Is(err, sql.ErrNoRows) {
		return workflow.NodeRelease{}, workflow.ErrVersionNotFound
	}
	if err != nil {
		return workflow.NodeRelease{}, err
	}
	if storedTarget.definition.Release.ReleasedAt != 0 {
		return storedTarget.definition.Release, tx.Commit()
	}
	if err = populateStoredNode(&storedTarget); err != nil {
		return workflow.NodeRelease{}, err
	}
	target := &storedTarget.definition
	var storedPrior storedNodeDefinition
	err = tx.QueryRow(`SELECT `+nodeStoredColumns+` FROM agent_workflow_definitions WHERE definition_kind='node' AND name=$1 AND released_at IS NOT NULL ORDER BY version DESC LIMIT 1 FOR UPDATE`, name).Scan(nodeStoredScan(&storedPrior)...)
	if err == nil {
		if err = populateStoredNode(&storedPrior); err != nil {
			return workflow.NodeRelease{}, err
		}
		prior := &storedPrior.definition
		if c == workflow.ReleaseCompatible && !workflow.NodeDefinitionsStructurallyCompatible(prior.Compiled, target.Compiled) {
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
func nodeStoredScan(stored *storedNodeDefinition) []any {
	return append(
		nodeMetaScan(&stored.definition),
		&stored.schemaVersion,
		&stored.signatureVersion,
		&stored.sourceManifest,
		&stored.compiled,
	)
}
func scanStoredNodes(rows *sql.Rows) ([]workflow.NodeDefinition, error) {
	out := []workflow.NodeDefinition{}
	for rows.Next() {
		var stored storedNodeDefinition
		if err := rows.Scan(nodeStoredScan(&stored)...); err != nil {
			return nil, err
		}
		if err := populateStoredNode(&stored); err != nil {
			return nil, err
		}
		out = append(out, stored.definition)
	}
	return out, rows.Err()
}
func populateStoredNode(stored *storedNodeDefinition) error {
	d := &stored.definition
	if !stored.sourceManifest.Valid {
		return fmt.Errorf("stored node %s/v%d has no source manifest", d.Name, d.Version)
	}
	var source workflow.Manifest
	if err := json.Unmarshal([]byte(stored.sourceManifest.String), &source); err != nil {
		return err
	}
	var (
		compiled *workflow.CompiledNodeDefinition
		err      error
	)
	if stored.compiled.Valid {
		compiled, err = workflow.ParseCompiledNodeDefinition([]byte(stored.compiled.String))
		if err != nil {
			return fmt.Errorf("stored node %s/v%d has invalid compiled definition: %w", d.Name, d.Version, err)
		}
	} else {
		compiled, err = workflow.CompileNodeDefinition(source)
		if err != nil {
			return fmt.Errorf("stored legacy node %s/v%d no longer compiles without broker authority: %w", d.Name, d.Version, err)
		}
	}
	if stored.schemaVersion != nodeRuntimeSchemaVersion ||
		stored.signatureVersion != compiled.Function.SignatureVersion ||
		compiled.Name != d.Name ||
		source.Hash() != d.ContentHash {
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
