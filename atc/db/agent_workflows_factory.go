package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/concourse/concourse/agent/workflow"
)

var ErrAgentWorkflowResourceSourcePromotionRequired = errors.New(
	"db: workflow resource source promotion requires trusted source-pipeline composition",
)

// AgentWorkflowResourceSourcePromotion is the trusted server composition for
// source-bearing workflow promotion. Both rendering and registry mutation are
// derived from the persisted definition, never a public promotion payload.
type AgentWorkflowResourceSourcePromotion struct {
	TeamID   int
	Registry WorkflowResourceSourcePipelineRegistry
	Renderer WorkflowResourceSourcePipelineRenderer
}

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

func NewAgentWorkflowsFactoryWithResourceSources(
	conn DbConn,
	promotionValidator workflow.PromotionValidator,
	resourceSourcePromotion AgentWorkflowResourceSourcePromotion,
) AgentWorkflowsFactory {
	if promotionValidator == nil {
		panic("db: resource-source workflow factory requires a promotion validator")
	}
	if resourceSourcePromotion.TeamID <= 0 || resourceSourcePromotion.Registry == nil || resourceSourcePromotion.Renderer == nil {
		panic("db: invalid resource-source workflow promotion composition")
	}
	return &agentWorkflowsFactory{
		conn: conn, promotionValidator: promotionValidator, resourceSourcePromotion: &resourceSourcePromotion,
	}
}

type agentWorkflowsFactory struct {
	conn                    DbConn
	promotionValidator      workflow.PromotionValidator
	resourceSourcePromotion *AgentWorkflowResourceSourcePromotion
}

// workflowMetaColumns is d.-qualified because every metadata read goes through
// workflowMetaFrom, which aliases agent_workflow_definitions as d.
const workflowMetaColumns = `d.id, d.name, d.version, d.content_hash, d.live, d.description, d.created_by,
	EXTRACT(EPOCH FROM d.created_at)::bigint, d.schema_version, d.signature_version,
	COALESCE(l.hidden, false), COALESCE(l.annotation, ''),
	COALESCE(EXTRACT(EPOCH FROM d.promoted_at)::bigint, 0), d.promoted_by`

// workflowMetaFrom LEFT JOINs the name-keyed lifecycle table so every version
// row carries its workflow's hidden/annotation (S-6). LEFT JOIN so a workflow
// with no lifecycle row yet still reads as not-hidden with an empty annotation.
const workflowMetaFrom = ` FROM agent_workflow_definitions d
	LEFT JOIN agent_workflow_lifecycle l ON l.name = d.name`

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
	raw, ok := src.DefinitionSource()
	if !ok {
		return nil, workflow.InvalidDefinitionError{Err: fmt.Errorf("workflow: manifest has no %s (or legacy %s)", workflow.WorkflowFileName, workflow.LegacyWorkflowFileName)}
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
		SELECT `+workflowMetaColumns+workflowMetaFrom+`
		WHERE d.name = $1 AND d.content_hash = $2`,
		name, hash,
	).Scan(&def.ID, &def.Name, &def.Version, &def.ContentHash, &def.Live,
		&def.Description, &def.CreatedBy, &def.CreatedAt, &def.SchemaVersion, &def.SignatureVersion,
		&def.Hidden, &def.Annotation, &def.PromotedAt, &def.PromotedBy)
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
		name, hash, raw, string(src.Canonical()), compiled.Description, createdBy,
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
	return f.getOne(`d.name = $1 AND d.version = $2`, name, version)
}

func (f *agentWorkflowsFactory) Live(name string) (*workflow.Definition, bool, error) {
	return f.getOne(`d.name = $1 AND d.live`, name)
}

func (f *agentWorkflowsFactory) Latest(name string) (*workflow.Definition, bool, error) {
	return f.getOne(`d.name = $1 ORDER BY d.version DESC LIMIT 1`, name)
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
		SELECT `+workflowMetaColumns+`, d.definition, d.source_manifest`+workflowMetaFrom+`
		WHERE `+where, args...,
	).Scan(&def.ID, &def.Name, &def.Version, &def.ContentHash, &def.Live,
		&def.Description, &def.CreatedBy, &def.CreatedAt, &def.SchemaVersion, &def.SignatureVersion,
		&def.Hidden, &def.Annotation, &def.PromotedAt, &def.PromotedBy,
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
		SELECT DISTINCT ON (d.name) ` + workflowMetaColumns + workflowMetaFrom + `
		ORDER BY d.name, d.version DESC`)
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
		SELECT `+workflowMetaColumns+workflowMetaFrom+`
		WHERE d.name = $1 AND ($2 = 0 OR d.version < $2)
		ORDER BY d.version DESC
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
	// FOR UPDATE OF d, not a bare FOR UPDATE: the lifecycle table is on the
	// nullable side of the LEFT JOIN and Postgres refuses to lock it.
	err = tx.QueryRow(`
		SELECT `+workflowMetaColumns+`, d.definition, d.source_manifest`+workflowMetaFrom+`
		WHERE d.name = $1 AND d.version = $2
		FOR UPDATE OF d`, name, version,
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
		&targetDefinition.Hidden,
		&targetDefinition.Annotation,
		&targetDefinition.PromotedAt,
		&targetDefinition.PromotedBy,
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
	var (
		renderedSourcePipeline workflow.RenderedResourceSourcePipeline
		hasResourceSources     bool
	)
	if f.resourceSourcePromotion != nil {
		renderedSourcePipeline, hasResourceSources, err = f.resourceSourcePromotion.Renderer.
			RenderResourceSourcePipelineForPromotion(targetDefinition, f.resourceSourcePromotion.TeamID)
		if err != nil {
			return workflow.PromotionResult{}, workflow.InvalidPromotionError{
				Err: fmt.Errorf("render workflow resource source pipeline: %w", err),
			}
		}
	} else if targetDefinition.Compiled.Function != nil && len(targetDefinition.Compiled.Function.ResourceSources) > 0 {
		return workflow.PromotionResult{}, workflow.InvalidPromotionError{
			Err: ErrAgentWorkflowResourceSourcePromotionRequired,
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

	// Activate the next source pipeline (or drain the old one) before the live
	// bit changes, in this same transaction. No selecting build can observe a
	// registry authority whose definition is not yet live.
	if f.resourceSourcePromotion != nil {
		if hasResourceSources {
			_, err = f.resourceSourcePromotion.Registry.SaveAndActivateForPromotion(
				context.Background(), tx, f.resourceSourcePromotion.TeamID, targetDefinition, renderedSourcePipeline,
			)
		} else {
			_, err = f.resourceSourcePromotion.Registry.DrainActiveForPromotion(
				context.Background(), tx, f.resourceSourcePromotion.TeamID, targetDefinition.Name,
			)
		}
		if err != nil {
			return workflow.PromotionResult{}, workflow.InvalidPromotionError{
				Err: fmt.Errorf("activate workflow resource source pipeline: %w", err),
			}
		}
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

func (f *agentWorkflowsFactory) Annotate(name, annotation, updatedBy string) error {
	return f.upsertLifecycle(name, "annotation", annotation, updatedBy)
}

func (f *agentWorkflowsFactory) SetHidden(name string, hidden bool, updatedBy string) error {
	return f.upsertLifecycle(name, "hidden", hidden, updatedBy)
}

// upsertLifecycle sets one lifecycle column, refusing to touch a workflow that
// has no versions (ErrVersionNotFound). The row is created lazily.
func (f *agentWorkflowsFactory) upsertLifecycle(name, column string, value any, updatedBy string) error {
	var exists bool
	err := f.conn.QueryRow(
		`SELECT EXISTS (SELECT 1 FROM agent_workflow_definitions WHERE name = $1)`, name,
	).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return workflow.ErrVersionNotFound
	}
	// column is a fixed literal ("annotation"|"hidden"), never user input.
	_, err = f.conn.Exec(`
		INSERT INTO agent_workflow_lifecycle (name, `+column+`, updated_by, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (name) DO UPDATE SET
			`+column+` = EXCLUDED.`+column+`,
			updated_by = EXCLUDED.updated_by,
			updated_at = now()`,
		name, value, updatedBy)
	return err
}

func scanWorkflowMetaRows(rows *sql.Rows) ([]workflow.Definition, error) {
	out := []workflow.Definition{}
	for rows.Next() {
		var def workflow.Definition
		if err := rows.Scan(&def.ID, &def.Name, &def.Version, &def.ContentHash, &def.Live,
			&def.Description, &def.CreatedBy, &def.CreatedAt, &def.SchemaVersion, &def.SignatureVersion,
			&def.Hidden, &def.Annotation, &def.PromotedAt, &def.PromotedBy); err != nil {
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
	// Rows written before source-manifest tracking existed carry no stored
	// tree at all, only the single raw-YAML column — always keyed by the
	// legacy name, since that predates the .yaml rename.
	compileSource := workflow.Manifest{workflow.LegacyWorkflowFileName: rawYAML}
	var storedSource workflow.Manifest
	if manifestJSON.Valid {
		// Unmarshal into a fresh map rather than reusing compileSource: json
		// merges into an existing map, so the legacy-keyed fallback above
		// would otherwise survive as a stray extra key alongside whichever
		// key the stored manifest actually used.
		var stored workflow.Manifest
		if err := json.Unmarshal([]byte(manifestJSON.String), &stored); err != nil {
			return nil, nil, fmt.Errorf("stored manifest %s/v%d no longer parses: %w", name, version, err)
		}
		compileSource = stored
		storedSource = stored
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
		definition.RawYAML, _ = source.DefinitionSource()
		definition.SourceManifest = source
	}
}
