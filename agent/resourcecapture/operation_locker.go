package resourcecapture

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	dblock "github.com/concourse/concourse/atc/db/lock"
)

const operationLockRetryInterval = 25 * time.Millisecond

type DBOperationLocker struct {
	logger  lager.Logger
	factory dblock.LockFactory
}

func NewDBOperationLocker(logger lager.Logger, factory dblock.LockFactory) (*DBOperationLocker, error) {
	if isNil(logger) || isNil(factory) {
		return nil, fmt.Errorf("resource capture: logger and lock factory are required")
	}
	return &DBOperationLocker{logger: logger, factory: factory}, nil
}

func (locker *DBOperationLocker) WithLock(ctx context.Context, key string, action func() error) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(key) == "" || action == nil {
		return fmt.Errorf("resource capture: lock key and action are required")
	}
	lockID := dblock.NewResourceGetLockID(key)
	for {
		acquiredLock, acquired, err := locker.factory.Acquire(locker.logger, lockID)
		if err != nil {
			return fmt.Errorf("resource capture: acquire operation lock: %w", err)
		}
		if acquired {
			if isNil(acquiredLock) {
				return fmt.Errorf("resource capture: lock factory returned a nil acquired lock")
			}
			actionErr := action()
			releaseErr := acquiredLock.Release()
			return errors.Join(actionErr, releaseErr)
		}
		timer := time.NewTimer(operationLockRetryInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}
