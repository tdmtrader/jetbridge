package db

import (
	"context"
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

func NewAgentWorkflowsFactory(conn DbConn, promotionValidators ...workflow.PromotionValidator) AgentWorkflowsFactory {
	if len(promotionValidators) > 1 {
		panic("db: NewAgentWorkflowsFactory accepts at most one promotion validator")
	}
	factory := &agentWorkflowsFactory{conn: conn}
	if len(promotionValidators) == 1 {
		factory.promotionValidator = promotionValidators[0]
	}
	return factory
}

type agentWorkflowsFactory struct {
	conn               DbConn
	promotionValidator workflow.PromotionValidator
}

const workflowMetaColumns = `id, name, version, content_hash, live, description, created_by,
	EXTRACT(EPOCH FROM created_at)::bigint, schema_version, signature_version`

func (f *agentWorkflowsFactory) Import(name string, rawYAML []byte, createdBy string) (*workflow.Definition, error) {
	return f.ImportManifest(name, workflow.Manifest{"workflow.yml": string(rawYAML)}, createdBy)
}

// ImportManifest compiles and stores a source tree (design 2026-07-17
// §3): the compiled definition is rebuilt on read from the stored canonical
// manifest, so there is exactly one persisted source of truth per row.
// Idempotent on the canonical-manifest hash.
func (f *agentWorkflowsFactory) ImportManifest(name string, src workflow.Manifest, createdBy string) (*workflow.Definition, error) {
	if err := src.Validate(); err != nil {
		return nil, workflow.InvalidDefinitionError{Err: err}
	}
	raw, ok := src["workflow.yml"]
	if !ok {
		return nil, workflow.InvalidDefinitionError{Err: fmt.Errorf("workflow: manifest has no workflow.yml")}
	}
	if err := workflow.RequireSchemaVersion3([]byte(raw)); err != nil {
		return nil, workflow.InvalidDefinitionError{Err: err}
	}
	compiled, err := workflow.CompileDefinition(src)
	if err != nil {
		return nil, workflow.InvalidDefinitionError{Err: err}
	}
	if compiled.Name != name {
		return nil, workflow.InvalidDefinitionError{Err: fmt.Errorf("definition name %q does not match import name %q", compiled.Name, name)}
	}
	metadata, err := compiled.VersionMetadata()
	if err != nil {
		return nil, workflow.InvalidDefinitionError{Err: err}
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
		&def.Description, &def.CreatedBy, &def.CreatedAt, &def.SchemaVersion, &def.SignatureVersion)
	if err == nil {
		// Idempotent on hash: byte-identical source returns the existing
		// version untouched (contracts §1.6).
		if def.SchemaVersion != metadata.SchemaVersion || def.SignatureVersion != metadata.SignatureVersion {
			return nil, fmt.Errorf("workflow: stored metadata for %q version %d does not match compiled source", name, def.Version)
		}
		populateCompiledWorkflowDefinition(&def, compiled, src)
		return &def, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	if metadata.SignatureVersion > 0 {
		var prior workflow.Definition
		var priorRaw string
		var priorManifest sql.NullString
		err = tx.QueryRow(`
			SELECT version, schema_version, signature_version, definition, source_manifest
			FROM agent_workflow_definitions
			WHERE name = $1 AND signature_version = $2
			ORDER BY version DESC
			LIMIT 1`, name, metadata.SignatureVersion,
		).Scan(&prior.Version, &prior.SchemaVersion, &prior.SignatureVersion, &priorRaw, &priorManifest)
		if err == nil {
			priorCompiled, _, compileErr := compileStoredWorkflowSource(name, prior.Version, priorRaw, priorManifest)
			if compileErr != nil {
				return nil, compileErr
			}
			priorMetadata, metadataErr := priorCompiled.VersionMetadata()
			if metadataErr != nil || priorMetadata.SchemaVersion != prior.SchemaVersion || priorMetadata.SignatureVersion != prior.SignatureVersion {
				return nil, fmt.Errorf("workflow: stored metadata for %q version %d does not match compiled source", name, prior.Version)
			}
			candidateSignature, signatureErr := compiled.PublicSignature()
			if signatureErr != nil {
				return nil, workflow.InvalidDefinitionError{Err: signatureErr}
			}
			priorSignature, signatureErr := priorCompiled.PublicSignature()
			if signatureErr != nil {
				return nil, fmt.Errorf("workflow: stored public signature for %q version %d is invalid", name, prior.Version)
			}
			if !candidateSignature.Equal(priorSignature) {
				return nil, workflow.InvalidDefinitionError{Err: fmt.Errorf("workflow %q signature_version %d is incompatible with version %d", name, metadata.SignatureVersion, prior.Version)}
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}

	err = tx.QueryRow(`
		INSERT INTO agent_workflow_definitions
			(name, version, content_hash, definition, source_manifest, description, created_by,
			 schema_version, signature_version)
		SELECT $1, COALESCE(MAX(version), 0) + 1, $2, $3, $4::jsonb, $5, $6, $7, $8
		FROM agent_workflow_definitions WHERE name = $1
		RETURNING id, version, EXTRACT(EPOCH FROM created_at)::bigint`,
		name, hash, src["workflow.yml"], string(src.Canonical()), compiled.Description, createdBy,
		metadata.SchemaVersion, metadata.SignatureVersion,
	).Scan(&def.ID, &def.Version, &def.CreatedAt)
	if err != nil {
		return nil, err
	}

	def.Name = name
	def.SchemaVersion = metadata.SchemaVersion
	def.SignatureVersion = metadata.SignatureVersion
	def.ContentHash = hash
	def.Description = compiled.Description
	def.CreatedBy = createdBy
	populateCompiledWorkflowDefinition(&def, compiled, src)
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
		&def.Description, &def.CreatedBy, &def.CreatedAt, &def.SchemaVersion, &def.SignatureVersion,
		&def.RawYAML, &manifestJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if def.SchemaVersion == 1 || def.SchemaVersion == 2 {
		return &def, true, nil
	}
	compiled, src, err := compileStoredWorkflowSource(def.Name, def.Version, def.RawYAML, manifestJSON)
	if err != nil {
		return nil, false, err
	}
	metadata, err := compiled.VersionMetadata()
	if err != nil || metadata.SchemaVersion != def.SchemaVersion || metadata.SignatureVersion != def.SignatureVersion {
		return nil, false, fmt.Errorf("workflow: stored metadata for %q version %d does not match compiled source", def.Name, def.Version)
	}
	populateCompiledWorkflowDefinition(&def, compiled, src)
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

func (f *agentWorkflowsFactory) Versions(
	ctx context.Context,
	name string,
	request workflow.VersionPageRequest,
) (workflow.VersionPage, error) {
	if ctx == nil {
		return workflow.VersionPage{}, fmt.Errorf("workflow: version page context is required")
	}
	if request.Limit <= 0 || request.Limit > workflow.MaxVersionPageSize ||
		request.Cursor < 0 || request.Cursor > workflow.MaxWorkflowVersion {
		return workflow.VersionPage{}, workflow.ErrInvalidVersionPage
	}

	var found bool
	if err := f.conn.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM agent_workflow_definitions WHERE name = $1
		)`, name).Scan(&found); err != nil {
		return workflow.VersionPage{}, err
	}
	page := workflow.VersionPage{Found: found, Definitions: []workflow.Definition{}}
	if !found {
		return page, nil
	}

	rows, err := f.conn.QueryContext(ctx, `
		SELECT `+workflowMetaColumns+`
		FROM agent_workflow_definitions
		WHERE name = $1 AND ($2 = 0 OR version < $2)
		ORDER BY version DESC
		LIMIT $3`, name, request.Cursor, request.Limit+1)
	if err != nil {
		return workflow.VersionPage{}, err
	}
	defer rows.Close()
	definitions, err := scanWorkflowMetaRows(rows)
	if err != nil {
		return workflow.VersionPage{}, err
	}
	if len(definitions) > request.Limit {
		definitions = definitions[:request.Limit]
		page.NextCursor = definitions[len(definitions)-1].Version
	}
	for left, right := 0, len(definitions)-1; left < right; left, right = left+1, right-1 {
		definitions[left], definitions[right] = definitions[right], definitions[left]
	}
	page.Definitions = definitions
	return page, nil
}

func (f *agentWorkflowsFactory) Promote(name string, version int, promotedBy string) (workflow.PromotionResult, error) {
	tx, err := f.conn.Begin()
	if err != nil {
		return workflow.PromotionResult{}, err
	}
	defer Rollback(tx)

	// Serialize promotions per name (same key as Import) so concurrent
	// clear-then-set sequences don't race into a unique violation on
	// agent_workflow_definitions_live.
	_, err = tx.Exec(`SELECT pg_advisory_xact_lock(hashtext('agent_workflow_definitions:' || $1))`, name)
	if err != nil {
		return workflow.PromotionResult{}, err
	}

	var targetDefinition workflow.Definition
	var targetManifest sql.NullString
	err = tx.QueryRow(`
		SELECT `+workflowMetaColumns+`, definition, source_manifest
		FROM agent_workflow_definitions
		WHERE name = $1 AND version = $2
		FOR UPDATE`, name, version,
	).Scan(
		&targetDefinition.ID,
		&targetDefinition.Name,
		&targetDefinition.Version,
		&targetDefinition.ContentHash,
		&targetDefinition.Live,
		&targetDefinition.Description,
		&targetDefinition.CreatedBy,
		&targetDefinition.CreatedAt,
		&targetDefinition.SchemaVersion,
		&targetDefinition.SignatureVersion,
		&targetDefinition.RawYAML,
		&targetManifest,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return workflow.PromotionResult{}, workflow.ErrVersionNotFound
	}
	if err != nil {
		return workflow.PromotionResult{}, err
	}
	if targetDefinition.SchemaVersion != 3 {
		return workflow.PromotionResult{}, workflow.InvalidPromotionError{
			Err: workflow.UnsupportedSchemaVersionError{Got: targetDefinition.SchemaVersion},
		}
	}
	compiled, source, err := compileStoredWorkflowSource(
		targetDefinition.Name,
		targetDefinition.Version,
		targetDefinition.RawYAML,
		targetManifest,
	)
	if err != nil {
		return workflow.PromotionResult{}, workflow.InvalidPromotionError{Err: err}
	}
	metadata, err := compiled.VersionMetadata()
	if err != nil ||
		metadata.SchemaVersion != targetDefinition.SchemaVersion ||
		metadata.SignatureVersion != targetDefinition.SignatureVersion {
		if err == nil {
			err = fmt.Errorf(
				"stored metadata for %q version %d does not match compiled source",
				targetDefinition.Name,
				targetDefinition.Version,
			)
		}
		return workflow.PromotionResult{}, workflow.InvalidPromotionError{Err: err}
	}
	populateCompiledWorkflowDefinition(&targetDefinition, compiled, source)
	if targetDefinition.SchemaVersion == 3 {
		if f.promotionValidator == nil {
			return workflow.PromotionResult{}, workflow.InvalidPromotionError{
				Err: workflow.ErrPromotionValidatorRequired,
			}
		}
		if err := f.promotionValidator.ValidatePromotion(targetDefinition); err != nil {
			return workflow.PromotionResult{}, workflow.InvalidPromotionError{Err: err}
		}
	}
	target := targetDefinition.VersionMetadata()

	result := workflow.PromotionResult{Target: target}
	var previous workflow.VersionMetadata
	err = tx.QueryRow(`
		SELECT version, schema_version, signature_version
		FROM agent_workflow_definitions
		WHERE name = $1 AND live
		FOR UPDATE`, name,
	).Scan(&previous.Version, &previous.SchemaVersion, &previous.SignatureVersion)
	if err == nil {
		result.PreviousLive = &previous
		result.SignatureChanged = previous.SignatureVersion != target.SignatureVersion
	} else if !errors.Is(err, sql.ErrNoRows) {
		return workflow.PromotionResult{}, err
	}

	// Clear-then-set inside one tx: the partial unique index
	// agent_workflow_definitions_live enforces at most one live row per
	// name at every intermediate statement.
	_, err = tx.Exec(`UPDATE agent_workflow_definitions SET live = false WHERE name = $1 AND live`, name)
	if err != nil {
		return workflow.PromotionResult{}, err
	}

	res, err := tx.Exec(`
		UPDATE agent_workflow_definitions
		SET live = true, promoted_at = now(), promoted_by = $3
		WHERE name = $1 AND version = $2`,
		name, version, promotedBy)
	if err != nil {
		return workflow.PromotionResult{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return workflow.PromotionResult{}, err
	}
	if n == 0 {
		return workflow.PromotionResult{}, workflow.ErrVersionNotFound
	}
	if err := tx.Commit(); err != nil {
		return workflow.PromotionResult{}, err
	}
	return result, nil
}

func scanWorkflowMetaRows(rows *sql.Rows) ([]workflow.Definition, error) {
	out := []workflow.Definition{}
	for rows.Next() {
		var def workflow.Definition
		if err := rows.Scan(&def.ID, &def.Name, &def.Version, &def.ContentHash, &def.Live,
			&def.Description, &def.CreatedBy, &def.CreatedAt, &def.SchemaVersion, &def.SignatureVersion); err != nil {
			return nil, err
		}
		out = append(out, def)
	}
	return out, rows.Err()
}

func compileStoredWorkflowSource(
	name string,
	version int,
	rawYAML string,
	manifestJSON sql.NullString,
) (*workflow.CompiledDefinition, workflow.Manifest, error) {
	compileSource := workflow.Manifest{"workflow.yml": rawYAML}
	var storedSource workflow.Manifest
	if manifestJSON.Valid {
		if err := json.Unmarshal([]byte(manifestJSON.String), &compileSource); err != nil {
			return nil, nil, fmt.Errorf("stored manifest %s/v%d no longer parses: %w", name, version, err)
		}
		storedSource = compileSource
	}
	compiled, err := workflow.CompileDefinition(compileSource)
	if err != nil {
		return nil, nil, fmt.Errorf("stored definition %s/v%d no longer compiles: %w", name, version, err)
	}
	if compiled.Name != name {
		return nil, nil, fmt.Errorf("stored definition %s/v%d compiles with a different name", name, version)
	}
	return compiled, storedSource, nil
}

func populateCompiledWorkflowDefinition(
	definition *workflow.Definition,
	compiled *workflow.CompiledDefinition,
	source workflow.Manifest,
) {
	definition.Compiled = *compiled
	if source != nil {
		definition.RawYAML = source["workflow.yml"]
		definition.SourceManifest = source
	}
}
