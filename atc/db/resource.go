package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"time"

	sq "github.com/Masterminds/squirrel"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/util"
)

var (
	ErrPinnedThroughConfig = errors.New("resource is pinned through config")
)

type ResourceHasNoScopeErr struct {
	Name string
}

func (e ResourceHasNoScopeErr) Error() string {
	return fmt.Sprintf("resource/resource-type %s has no scope", e.Name)
}

const CheckBuildName = "check"

type Resource interface {
	PipelineRef

	ID() int
	Name() string
	Public() bool
	TeamID() int
	TeamName() string
	Type() string
	Source() atc.Source
	CheckEvery() *atc.CheckEvery
	CheckTimeout() string
	LastCheckStartTime() time.Time
	LastCheckEndTime() time.Time
	Tags() atc.Tags
	WebhookToken() string
	Config() atc.ResourceConfig
	ConfigPinnedVersion() atc.Version
	APIPinnedVersion() atc.Version
	PinComment() string
	SetPinComment(string) error
	ResourceConfigID() int
	ResourceConfigScopeID() int
	Icon() string

	HasWebhook() bool

	CurrentPinnedVersion() atc.Version

	BuildSummary() *atc.BuildSummary

	ClearVersions() (int64, error)
	Versions(page Page, versionFilter atc.Version) ([]atc.ResourceVersion, Pagination, bool, error)
	FindVersion(filter atc.Version) (ResourceConfigVersion, bool, error) // Only used in tests!!
	UpdateMetadata(atc.Version, ResourceConfigMetadataFields) (bool, error)

	EnableVersion(rcvID int) error
	DisableVersion(rcvID int) error

	PinVersion(rcvID int) (bool, error)
	UnpinVersion() error

	SetResourceConfigScope(ResourceConfigScope) error

	CheckPlan(planFactory atc.PlanFactory, imagePlanner atc.ImagePlanner, from atc.Version, interval atc.CheckEvery, sourceDefaults atc.Source, skipInterval bool, skipIntervalRecursively bool) atc.Plan
	CreateBuild(context.Context, bool, atc.Plan) (Build, bool, error)
	CreateInMemoryBuild(context.Context, atc.Plan, util.SequenceGenerator) (Build, error)

	NotifyScan() error

	ClearResourceCache(atc.Version) (int64, error)

	SharedResourcesAndTypes() (atc.ResourcesAndTypes, error)

	DeprecatedScopes() ([]DeprecatedScope, error)
	CopyVersionsFromScope(fromScopeID int) (int, error)

	Reload() (bool, error)
}

// DeprecatedScope is a version history this resource used before: a scope
// deprecated when the resource left it (a unique history's config change, a
// type-image carry), or a live shared scope of the resource's team that the
// resource was the last to leave. DeprecatedAt is zero for a live one.
type DeprecatedScope struct {
	ID           int
	DeprecatedAt time.Time
	ConfigID     int
	Versions     int
	LastCheckEnd time.Time
}

var (
	resourcesQuery = psql.Select(
		"r.id",
		"r.name",
		"r.type",
		"r.config",
		"rs.last_check_start_time",
		"rs.last_check_end_time",
		"rs.last_check_build_id",
		"rs.last_check_succeeded",
		"rs.last_check_build_plan",
		"r.pipeline_id",
		"r.nonce",
		"r.resource_config_id",
		"r.resource_config_scope_id",
		"p.name",
		"p.instance_vars",
		"p.pipeline_run_id",
		"t.id",
		"t.name",
		"rp.version",
		"rp.comment_text",
		"rp.config",
		"r.in_memory_build_id",
		"r.in_memory_build_start_time",
		"r.in_memory_build_plan",
		"r.in_memory_build_status",
	).
		From("resources r").
		Join("pipelines p ON p.id = r.pipeline_id").
		Join("teams t ON t.id = p.team_id").
		LeftJoin("resource_config_scopes rs ON r.resource_config_scope_id = rs.id").
		LeftJoin("resource_pins rp ON rp.resource_id = r.id").
		Where(sq.Eq{"r.active": true})
)

type resource struct {
	pipelineRef

	id                    int
	name                  string
	teamID                int
	teamName              string
	type_                 string
	lastCheckStartTime    time.Time
	lastCheckEndTime      time.Time
	config                atc.ResourceConfig
	configPinnedVersion   atc.Version
	apiPinnedVersion      atc.Version
	pinComment            string
	resourceConfigID      int
	resourceConfigScopeID int
	buildSummary          *atc.BuildSummary
}

func newEmptyResource(conn DbConn, lockFactory lock.LockFactory) *resource {
	return &resource{pipelineRef: pipelineRef{conn: conn, lockFactory: lockFactory}}
}

type Resources []Resource

func (resources Resources) Lookup(name string) (Resource, bool) {
	for _, resource := range resources {
		if resource.Name() == name {
			return resource, true
		}
	}

	return nil, false
}

func (resources Resources) Configs() atc.ResourceConfigs {
	var configs atc.ResourceConfigs
	for _, r := range resources {
		configs = append(configs, r.Config())
	}
	return configs
}

func (r *resource) ID() int                          { return r.id }
func (r *resource) Name() string                     { return r.name }
func (r *resource) Public() bool                     { return r.config.Public }
func (r *resource) TeamID() int                      { return r.teamID }
func (r *resource) TeamName() string                 { return r.teamName }
func (r *resource) Type() string                     { return r.type_ }
func (r *resource) Source() atc.Source               { return r.config.Source }
func (r *resource) CheckEvery() *atc.CheckEvery      { return r.config.CheckEvery }
func (r *resource) CheckTimeout() string             { return r.config.CheckTimeout }
func (r *resource) LastCheckStartTime() time.Time    { return r.lastCheckStartTime }
func (r *resource) LastCheckEndTime() time.Time      { return r.lastCheckEndTime }
func (r *resource) Tags() atc.Tags                   { return r.config.Tags }
func (r *resource) WebhookToken() string             { return r.config.WebhookToken }
func (r *resource) Config() atc.ResourceConfig       { return r.config }
func (r *resource) ConfigPinnedVersion() atc.Version { return r.configPinnedVersion }
func (r *resource) APIPinnedVersion() atc.Version    { return r.apiPinnedVersion }
func (r *resource) PinComment() string               { return r.pinComment }
func (r *resource) ResourceConfigID() int            { return r.resourceConfigID }
func (r *resource) ResourceConfigScopeID() int       { return r.resourceConfigScopeID }
func (r *resource) Icon() string                     { return r.config.Icon }

func (r *resource) HasWebhook() bool { return r.WebhookToken() != "" }

func (r *resource) Reload() (bool, error) {
	row := resourcesQuery.Where(sq.Eq{"r.id": r.id}).
		RunWith(r.conn).
		QueryRow()

	err := scanResource(r, row)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

func (r *resource) SetResourceConfigScope(scope ResourceConfigScope) error {
	tx, err := r.conn.Begin()
	if err != nil {
		return err
	}

	defer Rollback(tx)

	changed, err := setResourceConfigScopeForResource(tx, scope, sq.Eq{"id": r.id})
	if err != nil {
		return err
	}

	err = tx.Commit()
	if err != nil {
		return err
	}

	if changed {
		r.conn.Bus().Notify(atc.ComponentLidarScanner)
	}

	return nil
}

func setResourceConfigScopeForResource(tx Tx, scope ResourceConfigScope, pred any, args ...any) (bool, error) {
	// Only a resource that is actually moving is locked: a check points its
	// resource at the scope it already has far more often than at a new one.
	// It is locked FOR NO KEY UPDATE, the strength of the UPDATE below, so a
	// foreign key on the resource (FOR KEY SHARE, as the scheduler's next
	// build inputs take it while holding a job this move then schedules)
	// does not wait for the move.
	var resourceID int
	var oldConfigID, oldScopeID sql.NullInt64
	err := psql.Select("id", "resource_config_id", "resource_config_scope_id").
		From("resources").
		Where(pred, args...).
		Where(sq.Or{
			sq.Eq{"resource_config_id": nil},
			sq.Eq{"resource_config_scope_id": nil},
			sq.NotEq{"resource_config_id": scope.ResourceConfig().ID()},
			sq.NotEq{"resource_config_scope_id": scope.ID()},
		}).
		Suffix("FOR NO KEY UPDATE").
		RunWith(tx).
		QueryRow().
		Scan(&resourceID, &oldConfigID, &oldScopeID)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}

	// Both scopes are locked, in id order, before the resource row points at
	// the new one: that update takes the new scope FOR KEY SHARE through the
	// foreign key, and two resources moving into it together would otherwise
	// each hold that and wait to lock it for the carry. Resources leaving the
	// old scope take turns, so the last one out sees that it is the last.
	moving := oldScopeID.Valid && int(oldScopeID.Int64) != scope.ID()
	if moving {
		err = lockScopes(tx, int(oldScopeID.Int64), scope.ID())
		if err != nil {
			return false, err
		}
	}

	_, err = psql.Update("resources").
		Set("resource_config_id", scope.ResourceConfig().ID()).
		Set("resource_config_scope_id", scope.ID()).
		Where(sq.Eq{"id": resourceID}).
		RunWith(tx).
		Exec()
	if err != nil {
		return false, err
	}

	if moving {
		// The resource leaves its old history: stamp when, and that it was
		// the one, before the carry may deprecate it. The old scope is the
		// resource's team's (the store refuses any other), and so is the new.
		var teamID int
		err = tx.QueryRow(`
			SELECT p.team_id FROM resources r JOIN pipelines p ON p.id = r.pipeline_id WHERE r.id = $1
		`, resourceID).Scan(&teamID)
		if err != nil {
			return false, err
		}

		err = stampLeftHistories(tx, teamID, []leftHistory{{
			resourceID: resourceID,
			configID:   int(oldConfigID.Int64),
			scopeID:    int(oldScopeID.Int64),
		}})
		if err != nil {
			return false, err
		}

		err = carryVersionHistoryForward(tx, resourceID, int(oldScopeID.Int64), scope)
		if err != nil {
			return false, err
		}
	}

	err = requestScheduleForJobsUsingResource(tx, resourceID)
	if err != nil {
		return false, err
	}

	return true, nil
}

// leftHistory is a resource leaving the config and scope it pointed at.
type leftHistory struct {
	resourceID int
	configID   int
	scopeID    int
}

// stampLeftHistories records resources leaving their version histories, all
// of one team. Each old config's last_referenced is set to now, the clock the
// config collector keeps a version history by, and each old live shared
// scope of the team names the resource that left it last
// (deprecated_from_resource_id), without being deprecated: the resource may
// list it and copy its history back (CopyVersionsFromScope), and a resource
// of the team that returns to the config finds it as it is.
//
// Every row is taken FOR NO KEY UPDATE SKIP LOCKED, so this never waits. A
// pipeline save calls it while holding its pipeline's job rows, and a mover
// holds scopes while it schedules those jobs. A config row held by someone
// else is held by whoever is using the config now (a check bumping its
// clock, a put finding it). A scope row held by someone else (a move, a copy,
// a last-check update; a version writer holds it only FOR KEY SHARE, which
// does not conflict) is left unstamped: the history is kept but not listed
// for this resource. A mover already holds both its scopes, so its own stamp
// is never skipped.
func stampLeftHistories(tx Tx, teamID int, leaves []leftHistory) error {
	if len(leaves) == 0 {
		return nil
	}

	configIDs := []int{}
	for _, leave := range leaves {
		if leave.configID != 0 {
			configIDs = append(configIDs, leave.configID)
		}
	}
	if len(configIDs) > 0 {
		query, args, err := psql.Select("id").
			From("resource_configs").
			Where(sq.Eq{"id": configIDs}).
			OrderBy("id").
			Suffix("FOR NO KEY UPDATE SKIP LOCKED").
			ToSql()
		if err != nil {
			return err
		}
		_, err = tx.Exec(`
			WITH stamped AS (`+query+`)
			UPDATE resource_configs SET last_referenced = now()
			FROM stamped WHERE resource_configs.id = stamped.id
		`, args...)
		if err != nil {
			return err
		}
	}

	// One resource per scope: several leaving one scope in a pipeline save
	// name the last of them.
	leftBy := map[int]int{}
	for _, leave := range leaves {
		if leave.scopeID != 0 {
			leftBy[leave.scopeID] = leave.resourceID
		}
	}
	for _, scopeID := range slices.Sorted(maps.Keys(leftBy)) {
		_, err := tx.Exec(`
			WITH stamped AS (
				SELECT id FROM resource_config_scopes
				WHERE id = $1
				AND team_id = $3
				AND resource_id IS NULL
				AND deprecated_at IS NULL
				FOR NO KEY UPDATE SKIP LOCKED
			)
			UPDATE resource_config_scopes SET deprecated_from_resource_id = $2
			FROM stamped WHERE resource_config_scopes.id = stamped.id
		`, scopeID, leftBy[scopeID], teamID)
		if err != nil {
			return err
		}
	}

	return nil
}

// carryVersionHistoryForward keeps a resource's version history when the image
// version of its custom resource type changes. The resource's config is keyed
// on the type's resource cache, so a new image version means a new config and
// a new, empty global scope, though the resource itself is unchanged. The old
// scope's versions are merged into the new scope (see copyVersionsIntoScopeTx),
// and the old scope is deprecated once no active resource or resource type
// still uses it, so it outlives its config's grace period by the
// deprecated-scope grace period. The caller holds the resource and both
// scopes.
//
// The two type images must be versions of the same image: the same type
// config (base type, or parent type image, and source) and the same params.
// Saving the pipeline detaches a resource whose own source or type name
// changed, but not one whose type's source changed, and a type on another
// image is not this resource's history: its checks may have found anything.
// A resolved type is keyed on its image's repository and digest alone
// (PinnedImage), so this is "the same repository": whoever can push to it,
// or select an older digest of it, chooses the destination. Both scopes are
// the resource's team's (a version history belongs to one team, ADR-0012),
// so whatever is carried is written only into that team's history, and the
// old scope's users that keep it live are only that team's.
func carryVersionHistoryForward(tx Tx, resourceID int, oldScopeID int, newScope ResourceConfigScope) error {
	if newScope.ResourceID() != nil {
		return nil
	}

	var oldConfigID int
	err := tx.QueryRow(`
		SELECT resource_config_id FROM resource_config_scopes
		WHERE id = $1 AND resource_id IS NULL
	`, oldScopeID).Scan(&oldConfigID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}

	sameImage, err := onlyTypeImageVersionDiffers(tx, oldConfigID, newScope.ResourceConfig().ID())
	if err != nil {
		return err
	}

	if !sameImage {
		return nil
	}

	_, err = copyVersionsIntoScopeTx(tx, newScope.ID(), oldScopeID)
	if err != nil {
		return err
	}

	_, err = tx.Exec(`
		UPDATE resource_config_scopes
		SET deprecated_at = now(), deprecated_from_resource_id = $2
		WHERE id = $1
		AND deprecated_at IS NULL
		AND NOT EXISTS (
			SELECT 1 FROM resources r
			WHERE r.resource_config_scope_id = $1 AND r.active
		)
		AND NOT EXISTS (
			SELECT 1 FROM resource_types rt
			JOIN pipelines p ON p.id = rt.pipeline_id
			JOIN resource_config_scopes s ON s.id = $1
			WHERE rt.resource_config_id = $3 AND rt.active
			AND p.team_id = s.team_id
		)
	`, oldScopeID, resourceID, oldConfigID)
	return err
}

// onlyTypeImageVersionDiffers says whether two resource configs are the same
// resource on two versions of one custom type image: different configs with
// the same source, each on a resource cache of the same type-image config
// (base type, or parent type image, and type source) with the same params.
// Only then is one's version history the other's.
func onlyTypeImageVersionDiffers(tx Tx, configID, otherConfigID int) (bool, error) {
	var differs bool
	err := tx.QueryRow(`
		SELECT EXISTS (
			SELECT 1
			FROM resource_configs config
			JOIN resource_configs other_config ON other_config.id = $2
			JOIN resource_caches image ON image.id = config.resource_cache_id
			JOIN resource_caches other_image ON other_image.id = other_config.resource_cache_id
			WHERE config.id = $1
			AND config.id <> other_config.id
			AND config.source_hash = other_config.source_hash
			AND image.resource_config_id = other_image.resource_config_id
			AND image.params_hash = other_image.params_hash
		)
	`, configID, otherConfigID).Scan(&differs)
	return differs, err
}

func (r *resource) CheckPlan(planFactory atc.PlanFactory, imagePlanner atc.ImagePlanner, from atc.Version, interval atc.CheckEvery, sourceDefaults atc.Source, skipInterval bool, skipIntervalRecursively bool) atc.Plan {
	plan := planFactory.NewPlan(atc.CheckPlan{
		Name:    r.name,
		Type:    r.type_,
		Source:  sourceDefaults.Merge(r.config.Source),
		Tags:    r.config.Tags,
		Timeout: r.config.CheckTimeout,

		FromVersion: from,
		Interval:    interval,

		SkipInterval: skipInterval,

		Resource: r.name,
	})

	plan.Check.TypeImage = imagePlanner.ImageForType(plan.ID, r.type_, r.config.Tags, skipInterval && skipIntervalRecursively)
	return plan
}

// CreateBuild tries to create a check build for this resource. A new build
// will only be created if there isn't an existing running check build for this
// resource, or if the build is manuallyTriggered (in which case a new build
// will always be created).
func (r *resource) CreateBuild(ctx context.Context, manuallyTriggered bool, plan atc.Plan) (Build, bool, error) {
	tx, err := r.conn.Begin()
	if err != nil {
		return nil, false, err
	}

	defer Rollback(tx)

	if !manuallyTriggered {
		var numRunningBuilds int
		err = psql.Select("COUNT(1)").
			From("builds").
			Where(sq.Eq{"resource_id": r.id, "completed": false}).
			RunWith(tx).
			QueryRow().
			Scan(&numRunningBuilds)
		if err != nil {
			return nil, false, err
		}

		if numRunningBuilds > 0 {
			// a build is already running; leave it be
			return nil, false, nil
		}
	}

	build := newEmptyBuild(r.conn, r.lockFactory)
	err = createStartedBuild(tx, build, startedBuildArgs{
		Name:              CheckBuildName,
		PipelineID:        r.pipelineID,
		TeamID:            r.teamID,
		Plan:              plan,
		ManuallyTriggered: manuallyTriggered,
		SpanContext:       NewSpanContext(ctx),
		ExtraValues: map[string]any{
			"resource_id": r.id,
		},
	})
	if err != nil {
		return nil, false, err
	}

	err = tx.Commit()
	if err != nil {
		return nil, false, err
	}

	err = r.conn.Bus().Notify(atc.ComponentBuildTracker)
	if err != nil {
		return nil, false, err
	}

	_, err = build.Reload()
	if err != nil {
		return nil, false, err
	}

	return build, true, nil
}

func (r *resource) CreateInMemoryBuild(ctx context.Context, plan atc.Plan, seqGen util.SequenceGenerator) (Build, error) {
	durable, err := durableRunChecks(ctx, r.conn, r)
	if err != nil {
		return nil, err
	}
	if durable {
		build, _, err := r.CreateBuild(ctx, true, plan)
		return build, err
	}

	return newRunningInMemoryCheckBuild(r.conn, r.lockFactory, r, plan, NewSpanContext(ctx), seqGen)
}

func (r *resource) UpdateMetadata(version atc.Version, metadata ResourceConfigMetadataFields) (bool, error) {
	versionJSON, err := json.Marshal(version)
	if err != nil {
		return false, err
	}

	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return false, err
	}

	_, err = psql.Update("resource_config_versions").
		Set("metadata", string(metadataJSON)).
		Where(sq.Eq{
			"resource_config_scope_id": r.ResourceConfigScopeID(),
		}).
		Where(sq.Expr(
			"version_sha256 = encode(digest(?, 'sha256'), 'hex')", versionJSON,
		)).
		RunWith(r.conn).
		Exec()

	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// FindVersion looks up a specific version of this resource's config scope.
// Used by scheduler algorithm resolvers (group_resolver, pinned_resolver).
func (r *resource) FindVersion(v atc.Version) (ResourceConfigVersion, bool, error) {
	if r.resourceConfigScopeID == 0 {
		return nil, false, nil
	}

	ver := &resourceConfigVersion{
		conn: r.conn,
	}

	versionByte, err := json.Marshal(v)
	if err != nil {
		return nil, false, err
	}

	row := resourceConfigVersionQuery.
		Where(sq.Eq{
			"v.resource_config_scope_id": r.resourceConfigScopeID,
		}).
		Where(sq.Expr("v.version_sha256 = encode(digest(?, 'sha256'), 'hex')", versionByte)).
		RunWith(r.conn).
		QueryRow()

	err = scanResourceConfigVersion(ver, row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, false, nil
		}
		return nil, false, err
	}

	return ver, true, nil
}

func (r *resource) SetPinComment(comment string) error {
	_, err := psql.Update("resource_pins").
		Set("comment_text", comment).
		Where(sq.Eq{"resource_id": r.ID()}).
		RunWith(r.conn).
		Exec()

	return err
}

func (r *resource) CurrentPinnedVersion() atc.Version {
	if r.configPinnedVersion != nil {
		return r.configPinnedVersion
	}
	if r.apiPinnedVersion != nil {
		return r.apiPinnedVersion
	}
	return nil
}

func (r *resource) BuildSummary() *atc.BuildSummary {
	return r.buildSummary
}

// DeprecatedScopes returns the version histories this resource used before,
// most recently deprecated first, then the live ones by their last check:
// the scopes of the resource's own team that name this resource as the one
// that left them (deprecated_from_resource_id). A deprecated scope is one the
// resource's config change or a type-image carry retired, listed as in 0.4.6.
// A live shared scope is one the resource left and nothing has deprecated,
// kept for the deprecated-scope grace period after it was left
// (CleanUnreferencedConfigs); it is listed while it holds versions and is
// not the resource's current scope (the resource came back to it). A
// resource that left a shared scope before another resource did is not named
// on it: only the last to leave is.
func (r *resource) DeprecatedScopes() ([]DeprecatedScope, error) {
	rows, err := r.conn.Query(`
		SELECT s.id, s.deprecated_at, s.resource_config_id, s.last_check_end_time,
			(SELECT count(*) FROM resource_config_versions v WHERE v.resource_config_scope_id = s.id)
		FROM resource_config_scopes s
		JOIN resources r ON r.id = s.deprecated_from_resource_id
		JOIN pipelines p ON p.id = r.pipeline_id
		WHERE r.id = $1
		AND s.team_id = p.team_id
		AND (
			s.deprecated_at IS NOT NULL
			OR (
				s.resource_id IS NULL
				AND s.id IS DISTINCT FROM r.resource_config_scope_id
				AND EXISTS (SELECT 1 FROM resource_config_versions v WHERE v.resource_config_scope_id = s.id)
			)
		)
		ORDER BY s.deprecated_at DESC NULLS LAST, s.last_check_end_time DESC, s.id DESC
	`, r.id)
	if err != nil {
		return nil, err
	}
	defer Close(rows)

	var scopes []DeprecatedScope
	for rows.Next() {
		var s DeprecatedScope
		var deprecatedAt sql.NullTime
		err := rows.Scan(&s.ID, &deprecatedAt, &s.ConfigID, &s.LastCheckEnd, &s.Versions)
		if err != nil {
			return nil, err
		}
		if deprecatedAt.Valid {
			s.DeprecatedAt = deprecatedAt.Time
		}
		scopes = append(scopes, s)
	}

	return scopes, rows.Err()
}

// ErrResourceVersionNotFound is returned for a version row id that names no
// version of the resource's own history.
var ErrResourceVersionNotFound = errors.New("resource version not found")

// ErrVersionHistoryNotFound is returned for a source scope that is gone, or
// that is not a history of the resource's own team.
var ErrVersionHistoryNotFound = errors.New("source version history not found")

// ErrCopyIntoSharedScope refuses a copy into a version history that other
// resources share, from a history that is not theirs.
var ErrCopyIntoSharedScope = errors.New("the resource's current version history is shared with other resources or resource types of the team, and the source scope's history is not one of the same resource config or of another version of the same type image (same type source and params); copying it would write into their history")

// CopyVersionsFromScope copies the given source scope's versions into this
// resource's current scope, merged into its order as copyVersionsIntoScopeTx
// describes. Returns count of versions copied.
//
// A current scope that belongs to this resource alone (a base type with a
// unique version history) takes any history, and so does a shared one no
// other active resource points at and no active resource type of the team is
// on the config of (the predicate the history carry uses): there is nobody
// else reading it to protect. A teammate that attaches to it later reads the
// copied versions, as it reads everything that history's checks found
// before it arrived. A current scope other active resources or types share
// takes only a history of the same resource config or of another version of
// the same type image: anything else could write versions their own checks
// never found into their history (ErrCopyIntoSharedScope).
//
// Both scopes are the resource's team's (ADR-0012): the current one by the
// store's trigger, the source checked here, so a copy never reads or writes
// another team's history. A source that is gone (collected since it was
// listed) or of another team is ErrVersionHistoryNotFound.
//
// The resource row is locked first, as a move locks it, so the resource
// cannot move off the scope mid-copy, and then both scopes, before anything
// about either is read. The config collector deletes the source only through
// its scope row, which waits for this lock, so a source read here survives
// the copy.
func (r *resource) CopyVersionsFromScope(fromScopeID int) (int, error) {
	tx, err := r.conn.Begin()
	if err != nil {
		return 0, err
	}

	defer Rollback(tx)

	// The resource row is locked alone, and its scope read in a statement of
	// its own: locking it through a join with the scope would, when a move
	// commits while this waits, recheck the moved row against the old scope
	// row, find no match, and return no row at all.
	var currentScopeID sql.NullInt64
	err = tx.QueryRow(`
		SELECT resource_config_scope_id FROM resources WHERE id = $1 FOR NO KEY UPDATE
	`, r.id).Scan(&currentScopeID)
	if err != nil {
		return 0, err
	}

	if !currentScopeID.Valid {
		return 0, errors.New("resource does not have a current scope")
	}

	var scopeID, configID int
	var scopeResourceID sql.NullInt64
	err = tx.QueryRow(`
		SELECT id, resource_id, resource_config_id FROM resource_config_scopes WHERE id = $1
	`, currentScopeID.Int64).Scan(&scopeID, &scopeResourceID, &configID)
	if err != nil {
		return 0, err
	}

	err = lockScopes(tx, scopeID, fromScopeID)
	if err != nil {
		return 0, err
	}

	var fromConfigID int
	err = tx.QueryRow(`
		SELECT s.resource_config_id FROM resource_config_scopes s
		JOIN resources r ON r.id = $2
		JOIN pipelines p ON p.id = r.pipeline_id
		WHERE s.id = $1 AND s.team_id = p.team_id
	`, fromScopeID, r.id).Scan(&fromConfigID)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, ErrVersionHistoryNotFound
		}
		return 0, err
	}

	if !scopeResourceID.Valid || int(scopeResourceID.Int64) != r.id {
		if fromConfigID != configID {
			sameImage, err := onlyTypeImageVersionDiffers(tx, fromConfigID, configID)
			if err != nil {
				return 0, err
			}

			if !sameImage {
				soleUser, err := soleActiveUserOfScope(tx, r.id, scopeID, configID)
				if err != nil {
					return 0, err
				}

				if !soleUser {
					return 0, ErrCopyIntoSharedScope
				}
			}
		}
	}

	copied, err := copyVersionsIntoScopeTx(tx, scopeID, fromScopeID)
	if err != nil {
		return 0, err
	}

	err = tx.Commit()
	if err != nil {
		return 0, err
	}

	return copied, nil
}

// soleActiveUserOfScope says whether a resource is the only active user of a
// shared scope: no other active resource points at it, and no active
// resource type of the scope's team is on its config. It is the predicate the
// history carry deprecates a scope by. The caller holds the scope FOR UPDATE,
// so no resource can attach to it before the caller commits.
func soleActiveUserOfScope(tx Tx, resourceID, scopeID, configID int) (bool, error) {
	var sole bool
	err := tx.QueryRow(`
		SELECT NOT EXISTS (
			SELECT 1 FROM resources r
			WHERE r.resource_config_scope_id = $1 AND r.active AND r.id <> $2
		)
		AND NOT EXISTS (
			SELECT 1 FROM resource_types rt
			JOIN pipelines p ON p.id = rt.pipeline_id
			JOIN resource_config_scopes s ON s.id = $1
			WHERE rt.resource_config_id = $3 AND rt.active
			AND p.team_id = s.team_id
		)
	`, scopeID, resourceID, configID).Scan(&sole)
	return sole, err
}

func (r *resource) ClearVersions() (int64, error) {
	results, err := psql.Delete("resource_config_versions").
		Where(sq.Eq{
			"resource_config_scope_id": r.resourceConfigScopeID,
		}).
		RunWith(r.conn).
		Exec()
	if err != nil {
		return 0, err
	}
	rowsDeleted, err := results.RowsAffected()
	if err != nil {
		return 0, err
	}

	return rowsDeleted, err
}

func (r *resource) Versions(page Page, versionFilter atc.Version) ([]atc.ResourceVersion, Pagination, bool, error) {
	tx, err := r.conn.Begin()
	if err != nil {
		return nil, Pagination{}, false, err
	}

	defer Rollback(tx)

	query := `
		SELECT v.id, v.version, v.metadata, v.check_order,
			NOT EXISTS (
				SELECT 1
				FROM resource_disabled_versions d
				WHERE d.version_digest IN (v.version_md5, v.version_sha256)
				AND r.resource_config_scope_id = v.resource_config_scope_id
				AND r.id = d.resource_id
			)
		FROM resource_config_versions v, resources r
		WHERE r.id = $1 AND r.resource_config_scope_id = v.resource_config_scope_id
	`

	filterJSON := "{}"
	if len(versionFilter) != 0 {
		filterBytes, err := json.Marshal(versionFilter)
		if err != nil {
			return nil, Pagination{}, false, err
		}

		filterJSON = string(filterBytes)
	}

	var rows *sql.Rows
	if page.From != nil {
		rows, err = tx.Query(fmt.Sprintf(`
			SELECT sub.*
				FROM (
						%s
					AND version @> $4
					AND v.check_order >= (SELECT check_order FROM resource_config_versions WHERE id = $2 AND resource_config_scope_id = r.resource_config_scope_id)
				ORDER BY v.check_order ASC
				LIMIT $3
			) sub
			ORDER BY sub.check_order DESC
		`, query), r.id, *page.From, page.Limit, filterJSON)
		if err != nil {
			return nil, Pagination{}, false, err
		}
	} else if page.To != nil {
		rows, err = tx.Query(fmt.Sprintf(`
			%s
				AND version @> $4
				AND v.check_order <= (SELECT check_order FROM resource_config_versions WHERE id = $2 AND resource_config_scope_id = r.resource_config_scope_id)
			ORDER BY v.check_order DESC
			LIMIT $3
		`, query), r.id, *page.To, page.Limit, filterJSON)
		if err != nil {
			return nil, Pagination{}, false, err
		}
	} else {
		rows, err = tx.Query(fmt.Sprintf(`
			%s
			AND version @> $3
			ORDER BY v.check_order DESC
			LIMIT $2
		`, query), r.id, page.Limit, filterJSON)
		if err != nil {
			return nil, Pagination{}, false, err
		}
	}

	defer Close(rows)

	type rcvCheckOrder struct {
		ResourceConfigVersionID int
		CheckOrder              int
	}

	rvs := make([]atc.ResourceVersion, 0)
	checkOrderRVs := make([]rcvCheckOrder, 0)
	for rows.Next() {
		var (
			metadataBytes sql.NullString
			versionBytes  string
			checkOrder    int
		)

		rv := atc.ResourceVersion{}
		err := rows.Scan(&rv.ID, &versionBytes, &metadataBytes, &checkOrder, &rv.Enabled)
		if err != nil {
			return nil, Pagination{}, false, err
		}

		err = json.Unmarshal([]byte(versionBytes), &rv.Version)
		if err != nil {
			return nil, Pagination{}, false, err
		}

		if metadataBytes.Valid {
			err = json.Unmarshal([]byte(metadataBytes.String), &rv.Metadata)
			if err != nil {
				return nil, Pagination{}, false, err
			}
		}

		checkOrderRV := rcvCheckOrder{
			ResourceConfigVersionID: rv.ID,
			CheckOrder:              checkOrder,
		}

		rvs = append(rvs, rv)
		checkOrderRVs = append(checkOrderRVs, checkOrderRV)
	}

	if len(rvs) == 0 {
		return nil, Pagination{}, true, nil
	}

	newestRCVCheckOrder := checkOrderRVs[0]
	oldestRCVCheckOrder := checkOrderRVs[len(checkOrderRVs)-1]

	var pagination Pagination

	var olderRCVId int
	err = tx.QueryRow(`
		SELECT v.id
		FROM resource_config_versions v, resources r
		WHERE v.check_order < $2 AND r.id = $1 AND v.resource_config_scope_id = r.resource_config_scope_id
		ORDER BY v.check_order DESC
		LIMIT 1
	`, r.id, oldestRCVCheckOrder.CheckOrder).Scan(&olderRCVId)
	if err != nil && err != sql.ErrNoRows {
		return nil, Pagination{}, false, err
	}
	if err == nil {
		pagination.Older = &Page{
			To:    &olderRCVId,
			Limit: page.Limit,
		}
	}

	var newerRCVId int
	err = tx.QueryRow(`
		SELECT v.id
		FROM resource_config_versions v, resources r
		WHERE v.check_order > $2 AND r.id = $1 AND v.resource_config_scope_id = r.resource_config_scope_id
		ORDER BY v.check_order ASC
		LIMIT 1
	`, r.id, newestRCVCheckOrder.CheckOrder).Scan(&newerRCVId)
	if err != nil && err != sql.ErrNoRows {
		return nil, Pagination{}, false, err
	}
	if err == nil {
		pagination.Newer = &Page{
			From:  &newerRCVId,
			Limit: page.Limit,
		}
	}

	err = tx.Commit()
	if err != nil {
		return nil, Pagination{}, false, nil
	}

	return rvs, pagination, true, nil
}

func (r *resource) EnableVersion(rcvID int) error {
	return r.toggleVersion(rcvID, true)
}

func (r *resource) DisableVersion(rcvID int) error {
	return r.toggleVersion(rcvID, false)
}

func (r *resource) PinVersion(rcvID int) (bool, error) {
	tx, err := r.conn.Begin()
	if err != nil {
		return false, err
	}
	defer Rollback(tx)
	var pinnedThroughConfig bool
	err = tx.QueryRow(`
		SELECT EXISTS (
			SELECT 1
			FROM resource_pins
			WHERE resource_id = $1
			AND config
		)`, r.id).Scan(&pinnedThroughConfig)
	if err != nil {
		return false, err
	}

	if pinnedThroughConfig {
		return false, ErrPinnedThroughConfig
	}

	// A version row id reaches only a version of the resource's own history.
	results, err := tx.Exec(`
	    INSERT INTO resource_pins(resource_id, version, comment_text, config)
			SELECT $1, rcv.version, '', false
			FROM resource_config_versions rcv
			WHERE rcv.id = $2
			AND rcv.resource_config_scope_id = (SELECT resource_config_scope_id FROM resources WHERE id = $1)
			ON CONFLICT (resource_id) DO UPDATE SET version=EXCLUDED.version`, r.id, rcvID)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}

	rowsAffected, err := results.RowsAffected()
	if err != nil {
		return false, err
	}

	if rowsAffected != 1 {
		return false, nil
	}

	err = requestScheduleForJobsUsingResource(tx, r.id)
	if err != nil {
		return false, err
	}

	err = tx.Commit()
	if err != nil {
		return false, err
	}

	r.conn.Bus().Notify(atc.ComponentLidarScanner)

	return true, nil
}

func (r *resource) UnpinVersion() error {
	tx, err := r.conn.Begin()
	if err != nil {
		return err
	}

	defer Rollback(tx)

	results, err := psql.Delete("resource_pins").
		Where(sq.Eq{"resource_pins.resource_id": r.id}).
		RunWith(tx).
		Exec()
	if err != nil {
		return err
	}

	rowsAffected, err := results.RowsAffected()
	if err != nil {
		return err
	}

	if rowsAffected != 1 {
		return NonOneRowAffectedError{rowsAffected}
	}

	err = requestScheduleForJobsUsingResource(tx, r.id)
	if err != nil {
		return err
	}

	err = tx.Commit()
	if err != nil {
		return err
	}

	r.conn.Bus().Notify(atc.ComponentLidarScanner)

	return nil
}

func (r *resource) toggleVersion(rcvID int, enable bool) error {
	tx, err := r.conn.Begin()
	if err != nil {
		return err
	}

	defer Rollback(tx)

	// A version row id reaches only a version of the resource's own history.
	var digest string
	err = tx.QueryRow(`
		SELECT COALESCE(rcv.version_md5, rcv.version_sha256)
		FROM resource_config_versions rcv
		WHERE rcv.id = $2
		AND rcv.resource_config_scope_id = (SELECT resource_config_scope_id FROM resources WHERE id = $1)
	`, r.id, rcvID).Scan(&digest)
	if err != nil {
		if err == sql.ErrNoRows {
			return ErrResourceVersionNotFound
		}
		return err
	}

	var results sql.Result
	if enable {
		results, err = tx.Exec(`
			DELETE FROM resource_disabled_versions
			WHERE resource_id = $1
			AND version_digest = $2
			`, r.id, digest)
	} else {
		results, err = tx.Exec(`
			INSERT INTO resource_disabled_versions (resource_id, version_digest)
			VALUES ($1, $2)
			`, r.id, digest)
	}
	if err != nil {
		return err
	}

	rowsAffected, err := results.RowsAffected()
	if err != nil {
		return err
	}

	if rowsAffected != 1 {
		return NonOneRowAffectedError{rowsAffected}
	}

	err = requestScheduleForJobsUsingResource(tx, r.id)
	if err != nil {
		return err
	}

	err = tx.Commit()
	if err != nil {
		return err
	}

	r.conn.Bus().Notify(atc.ComponentLidarScanner)

	return nil
}

func (r *resource) NotifyScan() error {
	return r.conn.Bus().Notify(fmt.Sprintf("resource_scan_%d", r.id))
}

func (r *resource) ClearResourceCache(version atc.Version) (int64, error) {
	tx, err := r.conn.Begin()
	if err != nil {
		return 0, err
	}

	defer Rollback(tx)

	selectStatement := psql.Select("id").
		From("resource_caches").
		Where(sq.Eq{
			"resource_config_id": r.resourceConfigID,
		})

	if version != nil {
		versionJson, err := json.Marshal(version)
		if err != nil {
			return 0, err
		}

		selectStatement = selectStatement.Where(
			sq.Expr("version_digest = encode(digest(?, 'sha256'), 'hex')", versionJson),
		)
	}

	sqlStatement, args, err := selectStatement.ToSql()
	if err != nil {
		return 0, err
	}

	results, err := tx.Exec(`DELETE FROM worker_resource_caches WHERE resource_cache_id IN (`+sqlStatement+`)`, args...)

	if err != nil {
		return 0, err
	}

	rowsDeleted, err := results.RowsAffected()
	if err != nil {
		return 0, err
	}

	return rowsDeleted, tx.Commit()
}

func (r *resource) SharedResourcesAndTypes() (atc.ResourcesAndTypes, error) {
	return sharedResourcesAndTypes(r.conn, r.resourceConfigScopeID, r.name)
}

func sharedResourcesAndTypes(conn DbConn, resourceConfigScopeID int, name string) (atc.ResourcesAndTypes, error) {
	if resourceConfigScopeID == 0 {
		return atc.ResourcesAndTypes{}, ResourceHasNoScopeErr{name}
	}

	rows, err := psql.Select("r.name", "p.name", "t.name").
		From("resources r").
		Join("pipelines p ON p.id = r.pipeline_id").
		Join("teams t ON t.id = p.team_id").
		Where(sq.Eq{
			"r.resource_config_scope_id": resourceConfigScopeID,
		}).
		RunWith(conn).
		Query()
	if err != nil {
		return atc.ResourcesAndTypes{}, err
	}

	var shared atc.ResourcesAndTypes
	for rows.Next() {
		var resourceName, pipelineName, teamName string
		err = rows.Scan(&resourceName, &pipelineName, &teamName)
		if err != nil {
			return atc.ResourcesAndTypes{}, err
		}

		shared.Resources = append(shared.Resources, atc.ResourceIdentifier{
			Name:         resourceName,
			PipelineName: pipelineName,
			TeamName:     teamName,
		})
	}

	rows, err = psql.Select("rt.name", "p.name", "t.name").
		From("resource_types rt").
		Join("pipelines p ON p.id = rt.pipeline_id").
		Join("teams t ON t.id = p.team_id").
		Join("resource_config_scopes rs ON rs.resource_config_id = rt.resource_config_id AND rs.team_id = p.team_id AND rs.resource_id IS NULL AND rs.deprecated_at IS NULL").
		Where(sq.Eq{
			"rs.id": resourceConfigScopeID,
		}).
		RunWith(conn).
		Query()
	if err != nil {
		return atc.ResourcesAndTypes{}, err
	}

	for rows.Next() {
		var resourceTypeName, pipelineName, teamName string
		err = rows.Scan(&resourceTypeName, &pipelineName, &teamName)
		if err != nil {
			return atc.ResourcesAndTypes{}, err
		}

		shared.ResourceTypes = append(shared.ResourceTypes, atc.ResourceIdentifier{
			Name:         resourceTypeName,
			PipelineName: pipelineName,
			TeamName:     teamName,
		})
	}

	return shared, nil
}

func scanResource(r *resource, row scannable) error {
	var (
		configBlob                                        sql.NullString
		nonce, rcID, rcScopeID, pinnedVersion, pinComment sql.NullString
		pinnedThroughConfig                               sql.NullBool
		pipelineInstanceVars                              sql.NullString
		pipelineRunID                                     sql.NullInt64
		buildData                                         buildData
	)

	err := row.Scan(&r.id, &r.name, &r.type_, &configBlob, &buildData.lastCheckStartTime,
		&buildData.lastCheckEndTime, &buildData.lastCheckBuildId, &buildData.lastCheckSucceeded, &buildData.lastCheckBuildPlan,
		&r.pipelineID, &nonce, &rcID, &rcScopeID,
		&r.pipelineName, &pipelineInstanceVars, &pipelineRunID, &r.teamID, &r.teamName,
		&pinnedVersion, &pinComment, &pinnedThroughConfig,
		&buildData.inMemoryBuildId, &buildData.inMemoryBuildStartTime, &buildData.inMemoryBuildPlan, &buildData.inMemoryBuildStatus)
	if err != nil {
		return err
	}

	es := r.conn.EncryptionStrategy()

	var noncense *string
	if nonce.Valid {
		noncense = &nonce.String
	}

	if configBlob.Valid {
		decryptedConfig, err := es.Decrypt(configBlob.String, noncense)
		if err != nil {
			return err
		}

		err = json.Unmarshal(decryptedConfig, &r.config)
		if err != nil {
			return err
		}
	} else {
		r.config = atc.ResourceConfig{}
	}

	if pinnedVersion.Valid {
		var version atc.Version
		err = json.Unmarshal([]byte(pinnedVersion.String), &version)
		if err != nil {
			return err
		}

		if pinnedThroughConfig.Valid && pinnedThroughConfig.Bool {
			r.configPinnedVersion = version
			r.apiPinnedVersion = nil
		} else {
			r.configPinnedVersion = nil
			r.apiPinnedVersion = version
		}
	} else {
		r.apiPinnedVersion = nil
		r.configPinnedVersion = nil
	}

	if pinComment.Valid {
		r.pinComment = pinComment.String
	} else {
		r.pinComment = ""
	}

	if rcID.Valid {
		r.resourceConfigID, err = strconv.Atoi(rcID.String)
		if err != nil {
			return err
		}
	}

	if rcScopeID.Valid {
		r.resourceConfigScopeID, err = strconv.Atoi(rcScopeID.String)
		if err != nil {
			return err
		}
	}

	if pipelineInstanceVars.Valid {
		err = json.Unmarshal([]byte(pipelineInstanceVars.String), &r.pipelineInstanceVars)
		if err != nil {
			return err
		}
	}
	r.pipelineRunID = int(pipelineRunID.Int64)

	if buildData.lastCheckBuildId.Valid || (buildData.inMemoryBuildId.Valid && !buildData.resolvedNativelySince()) {
		r.buildSummary = &atc.BuildSummary{
			Name: CheckBuildName,

			TeamName: r.teamName,

			PipelineID:           r.pipelineID,
			PipelineName:         r.pipelineName,
			PipelineInstanceVars: r.pipelineInstanceVars,
		}

		err := buildData.populate(r.buildSummary, &r.lastCheckStartTime, &r.lastCheckEndTime)
		if err != nil {
			return err
		}
	} else {
		// A natively resolved resource has no check build newer than its
		// last resolve, but its scope still records when it last resolved;
		// the check interval is measured from that.
		r.lastCheckStartTime = buildData.lastCheckStartTime.Time
		r.lastCheckEndTime = buildData.lastCheckEndTime.Time
	}

	return nil
}

// resolvedNativelySince reports whether the scope's last check ended after the
// resource's in-memory check build started and names no check build, as a
// native resolve does. The resource's in-memory build id is never cleared, so
// otherwise one check build that errored before it reached the scope (a
// fallback check, a manual check) would hide every later native resolve's end
// time, and the check interval would never apply again.
func (d buildData) resolvedNativelySince() bool {
	return !d.lastCheckBuildId.Valid &&
		d.lastCheckEndTime.Valid &&
		d.lastCheckEndTime.Time.After(d.inMemoryBuildStartTime.Time)
}

type buildData struct {
	inMemoryBuildId        sql.NullInt64
	inMemoryBuildStartTime sql.NullTime
	inMemoryBuildPlan      sql.NullString
	inMemoryBuildStatus    sql.NullString

	lastCheckBuildId   sql.NullInt64
	lastCheckStartTime sql.NullTime
	lastCheckEndTime   sql.NullTime
	lastCheckSucceeded sql.NullBool
	lastCheckBuildPlan sql.NullString
}

func (d buildData) populate(buildSummary *atc.BuildSummary, lastCheckStart *time.Time, lastCheckEnd *time.Time) error {
	if d.inMemoryBuildId.Valid && d.lastCheckBuildId.Valid {
		// If both ids are valid, and they are same, then we should use info
		// from resource_config_scope; otherwise we use build start time to
		// determine which one is newer.
		if d.inMemoryBuildId.Int64 == d.lastCheckBuildId.Int64 {
			return d.useLastCheckBuild(buildSummary, lastCheckStart, lastCheckEnd)
		}
		if d.inMemoryBuildStartTime.Time.Before(d.lastCheckStartTime.Time) {
			return d.useLastCheckBuild(buildSummary, lastCheckStart, lastCheckEnd)
		}
		return d.useInMemoryBuild(buildSummary, lastCheckStart)
	}
	if d.lastCheckBuildId.Valid {
		return d.useLastCheckBuild(buildSummary, lastCheckStart, lastCheckEnd)
	}
	if d.inMemoryBuildId.Valid {
		return d.useInMemoryBuild(buildSummary, lastCheckStart)
	}
	return errors.New("no build info available")
}

func (d buildData) useInMemoryBuild(buildSummary *atc.BuildSummary, lastCheckStart *time.Time) error {
	buildSummary.ID = int(d.inMemoryBuildId.Int64)
	buildSummary.StartTime = d.inMemoryBuildStartTime.Time.Unix()
	buildSummary.Status = atc.BuildStatus(d.inMemoryBuildStatus.String)
	*lastCheckStart = d.inMemoryBuildStartTime.Time
	if d.inMemoryBuildPlan.Valid {
		err := json.Unmarshal([]byte(d.inMemoryBuildPlan.String), &buildSummary.PublicPlan)
		if err != nil {
			return err
		}
	}
	return nil
}

func (d buildData) useLastCheckBuild(buildSummary *atc.BuildSummary, lastCheckStart *time.Time, lastCheckEnd *time.Time) error {
	buildSummary.ID = int(d.lastCheckBuildId.Int64)
	buildSummary.StartTime = d.lastCheckStartTime.Time.Unix()
	*lastCheckStart = d.lastCheckStartTime.Time
	if d.lastCheckEndTime.Valid && d.lastCheckStartTime.Time.Before(d.lastCheckEndTime.Time) {
		buildSummary.EndTime = d.lastCheckEndTime.Time.Unix()
		*lastCheckEnd = d.lastCheckEndTime.Time
		if d.lastCheckSucceeded.Valid && d.lastCheckSucceeded.Bool {
			buildSummary.Status = atc.StatusSucceeded
		} else {
			buildSummary.Status = atc.StatusFailed
		}
	} else {
		buildSummary.Status = atc.StatusStarted
	}

	if d.lastCheckBuildPlan.Valid {
		err := json.Unmarshal([]byte(d.lastCheckBuildPlan.String), &buildSummary.PublicPlan)
		if err != nil {
			return err
		}
	}
	return nil
}

// The SELECT query orders the jobs for updating to prevent deadlocking.
// Updating multiple rows using a SELECT subquery does not preserve the same
// order for the updates, which can lead to deadlocking.
func requestScheduleForJobsUsingResource(tx Tx, resourceID int) error {
	rows, err := psql.Select("DISTINCT job_id").
		From("job_inputs").
		Where(sq.Eq{
			"resource_id": resourceID,
		}).
		OrderBy("job_id DESC").
		RunWith(tx).
		Query()
	if err != nil {
		return err
	}

	var jobs []int
	for rows.Next() {
		var jid int
		err = rows.Scan(&jid)
		if err != nil {
			return err
		}

		jobs = append(jobs, jid)
	}

	if err := rows.Err(); err != nil {
		Close(rows)
		return err
	}
	Close(rows)
	return requestScheduleJobs(tx, jobs, false)
}
