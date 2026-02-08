package db

import (
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc"
	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrWorkerNotPresent = errors.New("worker not present in db")
)

type ContainerOwnerDisappearedError struct {
	owner ContainerOwner
}

func (e ContainerOwnerDisappearedError) Error() string {
	return fmt.Sprintf("container owner %T disappeared", e.owner)
}

type WorkerState string

const (
	WorkerStateRunning = WorkerState("running")
	WorkerStateStalled = WorkerState("stalled")
)

func AllWorkerStates() []WorkerState {
	return []WorkerState{
		WorkerStateRunning,
		WorkerStateStalled,
	}
}

//counterfeiter:generate . Worker
type Worker interface {
	Name() string
	Version() *string
	State() WorkerState
	ActiveContainers() int
	ActiveVolumes() int
	ResourceTypes() []atc.WorkerResourceType
	Platform() string
	Tags() []string
	TeamID() int
	TeamName() string
	StartTime() time.Time
	ExpiresAt() time.Time
	Ephemeral() bool

	Reload() (bool, error)

	Delete() error

	ActiveTasks() (int, error)

	FindContainer(owner ContainerOwner) (CreatingContainer, CreatedContainer, error)
	CreateContainer(owner ContainerOwner, meta ContainerMetadata) (CreatingContainer, error)
}

type worker struct {
	conn DbConn

	name             string
	version          *string
	state            WorkerState
	activeContainers int
	activeVolumes    int
	activeTasks      int
	resourceTypes    []atc.WorkerResourceType
	platform         string
	tags             []string
	teamID           int
	teamName         string
	startTime        time.Time
	expiresAt        time.Time
	ephemeral        bool
}

func (worker *worker) Name() string         { return worker.name }
func (worker *worker) Version() *string     { return worker.version }
func (worker *worker) State() WorkerState   { return worker.state }
func (worker *worker) ActiveContainers() int { return worker.activeContainers }
func (worker *worker) ActiveVolumes() int                      { return worker.activeVolumes }
func (worker *worker) ResourceTypes() []atc.WorkerResourceType { return worker.resourceTypes }
func (worker *worker) Platform() string                        { return worker.platform }
func (worker *worker) Tags() []string                          { return worker.tags }
func (worker *worker) TeamID() int                             { return worker.teamID }
func (worker *worker) TeamName() string                        { return worker.teamName }
func (worker *worker) Ephemeral() bool                         { return worker.ephemeral }

func (worker *worker) StartTime() time.Time { return worker.startTime }
func (worker *worker) ExpiresAt() time.Time { return worker.expiresAt }

func (worker *worker) Reload() (bool, error) {
	row := workersQuery.Where(sq.Eq{"w.name": worker.name}).
		RunWith(worker.conn).
		QueryRow()

	err := scanWorker(worker, row)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

func (worker *worker) Delete() error {
	_, err := sq.Delete("workers").
		Where(sq.Eq{
			"name": worker.name,
		}).
		PlaceholderFormat(sq.Dollar).
		RunWith(worker.conn).
		Exec()

	return err
}

func (worker *worker) FindContainer(owner ContainerOwner) (CreatingContainer, CreatedContainer, error) {
	ownerQuery, found, err := owner.Find(worker.conn)
	if err != nil {
		return nil, nil, err
	}

	if !found {
		return nil, nil, nil
	}

	return worker.findContainer(sq.And{
		sq.Eq{"worker_name": worker.name},
		ownerQuery,
	})
}

func (worker *worker) CreateContainer(owner ContainerOwner, meta ContainerMetadata) (CreatingContainer, error) {
	handle, err := uuid.NewRandom()
	if err != nil {
		return nil, err
	}
	var containerID int
	cols := []any{&containerID}

	metadata := &ContainerMetadata{}
	cols = append(cols, metadata.ScanTargets()...)

	tx, err := worker.conn.Begin()
	if err != nil {
		return nil, err
	}

	defer Rollback(tx)

	insMap := meta.SQLMap()
	insMap["worker_name"] = worker.name
	insMap["handle"] = handle.String()

	ownerCols, err := owner.Create(tx, worker.name)
	if err != nil {
		return nil, fmt.Errorf("create owner: %w", err)
	}

	maps.Copy(insMap, ownerCols)

	err = psql.Insert("containers").
		SetMap(insMap).
		Suffix("RETURNING id, " + strings.Join(containerMetadataColumns, ", ")).
		RunWith(tx).
		QueryRow().
		Scan(cols...)
	if err != nil {
		if pgErr, ok := err.(*pgconn.PgError); ok && pgErr.Code == pgerrcode.ForeignKeyViolation {
			return nil, ContainerOwnerDisappearedError{owner}
		}

		return nil, fmt.Errorf("insert container: %w", err)
	}

	err = tx.Commit()
	if err != nil {
		return nil, err
	}

	return newCreatingContainer(
		containerID,
		// Allow overwriting the random handle via the ContainerOwner
		insMap["handle"].(string),
		worker.name,
		*metadata,
		worker.conn,
	), nil
}

func (worker *worker) findContainer(whereClause sq.Sqlizer) (CreatingContainer, CreatedContainer, error) {
	creating, created, destroying, _, err := scanContainer(
		selectContainers().
			Where(whereClause).
			RunWith(worker.conn).
			QueryRow(),
		worker.conn,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	if destroying != nil {
		return nil, nil, nil
	}

	return creating, created, nil
}

func (worker *worker) ActiveTasks() (int, error) {
	err := psql.Select("active_tasks").From("workers").Where(sq.Eq{"name": worker.name}).
		RunWith(worker.conn).
		QueryRow().
		Scan(&worker.activeTasks)
	if err != nil {
		return 0, err
	}
	return worker.activeTasks, nil
}

