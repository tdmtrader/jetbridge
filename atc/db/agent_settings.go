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
		mode      string
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
	return mode, updatedAt, updatedBy.String, true, nil
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
