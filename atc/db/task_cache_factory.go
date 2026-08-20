package db

import "github.com/concourse/concourse/atc"

type TaskCacheFactory interface {
	Find(identity atc.TaskCacheIdentity, stepName string, path string) (UsedTaskCache, bool, error)
	FindOrCreate(identity atc.TaskCacheIdentity, stepName string, path string) (UsedTaskCache, error)
}

type taskCacheFactory struct {
	conn DbConn
}

func NewTaskCacheFactory(conn DbConn) TaskCacheFactory {
	return &taskCacheFactory{
		conn: conn,
	}
}

func (f *taskCacheFactory) Find(identity atc.TaskCacheIdentity, stepName string, path string) (UsedTaskCache, bool, error) {
	if err := identity.Validate(); err != nil {
		return nil, false, err
	}
	return usedTaskCache{
		identity: identity,
		stepName: stepName,
		path:     path,
	}.find(f.conn)
}

func (f *taskCacheFactory) FindOrCreate(identity atc.TaskCacheIdentity, stepName string, path string) (UsedTaskCache, error) {
	if err := identity.Validate(); err != nil {
		return nil, err
	}
	tx, err := f.conn.Begin()
	if err != nil {
		return nil, err
	}

	defer Rollback(tx)

	utc, err := usedTaskCache{
		identity: identity,
		stepName: stepName,
		path:     path,
	}.findOrCreate(tx)

	if err != nil {
		return nil, err
	}

	err = tx.Commit()
	if err != nil {
		return nil, err
	}

	return utc, nil
}
