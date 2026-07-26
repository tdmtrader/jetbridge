package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	sq "github.com/Masterminds/squirrel"
)

// Valid dispatcher modes, mirrored by the agent_settings.dispatcher_mode CHECK
// constraint (migration 1773106091) and by agent/dispatch.Mode. Kept as bare
// strings here so atc/db has no dependency on agent/dispatch (which imports
// atc/db — the reverse edge would be a cycle).
const (
	DispatcherModeOff    = "off"
	DispatcherModePaused = "paused"
	DispatcherModeActive = "active"
)

// ErrInvalidDispatcherMode is returned by SetDispatcherMode for a mode outside
// {off,paused,active} — a guard in front of the CHECK constraint so callers get
// a clear error instead of a raw pg violation.
var ErrInvalidDispatcherMode = errors.New("dispatcher_mode must be one of off|paused|active")

// Valid action-suppression modes, mirrored by the agent_settings.actions_mode
// CHECK constraint (migration 1773106128) and by agent/publisher. Bare strings
// for the same reason as the dispatcher modes above: atc/db must not depend on
// agent/publisher.
const (
	ActionsModeActive     = "active"
	ActionsModeSuppressed = "suppressed"
)

// ErrInvalidActionsMode is returned by SetActionsMode for a mode outside
// {active,suppressed} — a guard in front of the CHECK constraint.
var ErrInvalidActionsMode = errors.New("actions_mode must be one of active|suppressed")

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

// AgentSettingsFactory reads and writes the singleton agent_settings row.
// Reads are HOT (a fresh SELECT each call) so the dispatcher loop observes a
// runtime mode change on its very next tick — mirroring the pipeline-pause
// idiom (atc/db/pipeline.go CheckPaused).
//
//counterfeiter:generate . AgentSettingsFactory
type AgentSettingsFactory interface {
	// GetDispatcherMode is the dispatcher loop's hot read. found=false means
	// no row exists yet (fall back to the boot flag).
	GetDispatcherMode() (mode string, found bool, err error)
	// GetDispatcherSetting backs the GET status API: the effective mode plus
	// its provenance (updated_at/updated_by). found=false means no row.
	GetDispatcherSetting() (mode string, updatedAt time.Time, updatedBy string, found bool, err error)
	// SetDispatcherMode UPSERTs the singleton row (id=1). Validates mode before
	// touching the DB; returns ErrInvalidDispatcherMode otherwise.
	SetDispatcherMode(mode, updatedBy string) error
	// GetActionsMode is the publisher's HOT read of the cluster-wide
	// action-suppression switch. found reports ROW EXISTENCE, not provenance:
	// found=false means no settings row exists at all, which callers must treat
	// as active.
	GetActionsMode() (mode string, found bool, err error)
	// GetActionsSetting backs the GET status API: the stored mode plus its own
	// provenance. found=false means the switch has never been set.
	GetActionsSetting() (mode string, updatedAt time.Time, updatedBy string, found bool, err error)
	// SetActionsMode UPSERTs the singleton row (id=1) without touching
	// dispatcher_mode or its provenance. Validates mode before touching the DB.
	SetActionsMode(mode, updatedBy string) error
}

func NewAgentSettingsFactory(conn DbConn) AgentSettingsFactory {
	return &agentSettingsFactory{conn: conn}
}

type agentSettingsFactory struct {
	conn DbConn
}

func validDispatcherMode(mode string) bool {
	switch mode {
	case DispatcherModeOff, DispatcherModePaused, DispatcherModeActive:
		return true
	default:
		return false
	}
}

func (f *agentSettingsFactory) GetDispatcherMode() (string, bool, error) {
	mode, _, _, found, err := f.GetDispatcherSetting()
	return mode, found, err
}

func (f *agentSettingsFactory) GetDispatcherSetting() (string, time.Time, string, bool, error) {
	var (
		mode      sql.NullString
		updatedAt time.Time
		updatedBy sql.NullString
	)
	err := psql.Select("dispatcher_mode", "updated_at", "updated_by").
		From("agent_settings").
		Where(sq.Eq{"id": 1}).
		RunWith(f.conn).
		QueryRow().
		Scan(&mode, &updatedAt, &updatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, "", false, nil
	}
	if err != nil {
		return "", time.Time{}, "", false, err
	}
	if !mode.Valid {
		// The row was created by SetActionsMode, which must not invent a
		// dispatcher mode. NULL is exactly "never set" — same effective mode
		// as no row, so the boot-flag fallback keeps applying.
		return "", time.Time{}, "", false, nil
	}
	return mode.String, updatedAt, updatedBy.String, true, nil
}

func (f *agentSettingsFactory) SetDispatcherMode(mode, updatedBy string) error {
	if !validDispatcherMode(mode) {
		return fmt.Errorf("%w: got %q", ErrInvalidDispatcherMode, mode)
	}
	_, err := psql.Insert("agent_settings").
		Columns("id", "dispatcher_mode", "updated_at", "updated_by").
		Values(1, mode, sq.Expr("now()"), updatedBy).
		Suffix("ON CONFLICT (id) DO UPDATE SET dispatcher_mode = EXCLUDED.dispatcher_mode, updated_at = now(), updated_by = EXCLUDED.updated_by").
		RunWith(f.conn).
		Exec()
	return err
}

func validActionsMode(mode string) bool {
	switch mode {
	case ActionsModeActive, ActionsModeSuppressed:
		return true
	default:
		return false
	}
}

// GetActionsMode reads the BRAKE ITSELF, never its provenance. It deliberately
// does NOT delegate to GetActionsSetting: keying the hot read on
// actions_updated_at would make a break-glass `UPDATE agent_settings SET
// actions_mode = 'suppressed'` — the operator's recourse when the API is down,
// which is exactly when the switch matters most — read back as "never engaged",
// and publishes would proceed. actions_mode is NOT NULL DEFAULT 'active'
// (migration 1773106128), so every existing row carries a total, trustworthy
// mode and no provenance is needed to interpret it. found=false therefore means
// only "no settings row exists"; callers map that to active via
// publisher.EffectiveActionsMode. GetActionsSetting keeps the provenance-based
// "never set" answer for the status API, where "who engaged it, and when" is
// the question being asked.
func (f *agentSettingsFactory) GetActionsMode() (string, bool, error) {
	var mode string
	err := psql.Select("actions_mode").
		From("agent_settings").
		Where(sq.Eq{"id": 1}).
		RunWith(f.conn).
		QueryRow().
		Scan(&mode)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return mode, true, nil
}

func (f *agentSettingsFactory) GetActionsSetting() (string, time.Time, string, bool, error) {
	var (
		mode      string
		updatedAt sql.NullTime
		updatedBy sql.NullString
	)
	err := psql.Select("actions_mode", "actions_updated_at", "actions_updated_by").
		From("agent_settings").
		Where(sq.Eq{"id": 1}).
		RunWith(f.conn).
		QueryRow().
		Scan(&mode, &updatedAt, &updatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, "", false, nil
	}
	if err != nil {
		return "", time.Time{}, "", false, err
	}
	if !updatedAt.Valid {
		// The row exists but nobody has touched the switch (SetDispatcherMode
		// created it and the column default filled in 'active'). Absence is
		// meaningful, so report it as unset rather than as an admin decision.
		return "", time.Time{}, "", false, nil
	}
	return mode, updatedAt.Time, updatedBy.String, true, nil
}

func (f *agentSettingsFactory) SetActionsMode(mode, updatedBy string) error {
	if !validActionsMode(mode) {
		return fmt.Errorf("%w: got %q", ErrInvalidActionsMode, mode)
	}
	_, err := psql.Insert("agent_settings").
		Columns("id", "actions_mode", "actions_updated_at", "actions_updated_by").
		Values(1, mode, sq.Expr("now()"), updatedBy).
		Suffix("ON CONFLICT (id) DO UPDATE SET actions_mode = EXCLUDED.actions_mode, " +
			"actions_updated_at = now(), actions_updated_by = EXCLUDED.actions_updated_by").
		RunWith(f.conn).
		Exec()
	return err
}
