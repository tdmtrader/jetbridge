package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
)

const snapshotDigestLockDomain = "agent-snapshot-digest-lock/v1\x00"

func NewAgentSnapshotDigestLocker(conn DbConn) snapshot.DigestLockManager {
	return &agentSnapshotDigestLocker{conn: conn}
}

type agentSnapshotDigestLocker struct {
	conn DbConn
}

func (manager *agentSnapshotDigestLocker) AcquireMany(
	ctx context.Context,
	digests []snapshot.Digest,
) (snapshot.DigestLease, error) {
	if len(digests) == 0 {
		return nil, fmt.Errorf("db: at least one snapshot digest lock is required")
	}
	unique := make(map[snapshot.Digest]struct{}, len(digests))
	for _, digest := range digests {
		if err := digest.Validate(); err != nil {
			return nil, err
		}
		unique[digest] = struct{}{}
	}
	ordered := make([]snapshot.Digest, 0, len(unique))
	for digest := range unique {
		ordered = append(ordered, digest)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })

	connection, err := manager.conn.Conn(ctx)
	if err != nil {
		return nil, err
	}
	lease := &agentSnapshotDigestLease{
		conn:    connection,
		covered: make(map[snapshot.Digest]struct{}, len(ordered)),
	}
	for _, digest := range ordered {
		key := snapshotDigestLockKey(digest)
		if _, err := connection.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, key); err != nil {
			return lease, fmt.Errorf("db: acquire snapshot digest lock %s: %w", digest, err)
		}
		lease.keys = append(lease.keys, key)
		lease.covered[digest] = struct{}{}
	}
	return lease, nil
}

func snapshotDigestLockKey(digest snapshot.Digest) int64 {
	sum := sha256.Sum256([]byte(snapshotDigestLockDomain + digest.String()))
	return int64(binary.BigEndian.Uint64(sum[:8]))
}

type agentSnapshotDigestLease struct {
	mutex    sync.Mutex
	conn     *sql.Conn
	keys     []int64
	covered  map[snapshot.Digest]struct{}
	closed   bool
	closeErr error
}

func (lease *agentSnapshotDigestLease) Covers(digest snapshot.Digest) bool {
	lease.mutex.Lock()
	defer lease.mutex.Unlock()
	if lease.closed {
		return false
	}
	_, found := lease.covered[digest]
	return found
}

func (lease *agentSnapshotDigestLease) Close() error {
	lease.mutex.Lock()
	defer lease.mutex.Unlock()
	if lease.closed {
		return lease.closeErr
	}
	lease.closed = true

	var closeErr error
	unlockContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for index := len(lease.keys) - 1; index >= 0; index-- {
		var unlocked bool
		err := lease.conn.QueryRowContext(
			unlockContext,
			`SELECT pg_advisory_unlock($1)`,
			lease.keys[index],
		).Scan(&unlocked)
		if err != nil {
			// database/sql invalidates the dedicated connection when a blocked
			// acquisition is cancelled. PostgreSQL releases all of that session's
			// advisory locks when the connection is discarded, so this is the
			// safety-release path for a partial acquisition rather than an unlock
			// failure.
			if errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) {
				break
			}
			closeErr = errors.Join(closeErr, fmt.Errorf("db: release snapshot digest lock: %w", err))
			continue
		}
		if !unlocked {
			closeErr = errors.Join(closeErr, fmt.Errorf("db: snapshot digest advisory lock was not held by its lease connection"))
		}
	}
	if lease.conn != nil {
		if err := lease.conn.Close(); err != nil &&
			!errors.Is(err, sql.ErrConnDone) && !errors.Is(err, driver.ErrBadConn) {
			closeErr = errors.Join(closeErr, err)
		}
	}
	lease.closeErr = closeErr
	return closeErr
}

var _ snapshot.DigestLockManager = (*agentSnapshotDigestLocker)(nil)
var _ snapshot.DigestLease = (*agentSnapshotDigestLease)(nil)
