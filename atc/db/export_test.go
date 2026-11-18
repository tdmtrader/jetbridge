package db

import (
	"time"

	"code.cloudfoundry.org/lager/v3"
)

// CopyVersionsIntoScope runs the merge copyVersionsIntoScopeTx does in a
// transaction of its own, to test the merge alone. Production writes into a
// scope only through the carry and copy-resource-versions, which first refuse
// a destination another team uses.
func CopyVersionsIntoScope(conn DbConn, toScopeID, fromScopeID int) (int, error) {
	tx, err := conn.Begin()
	if err != nil {
		return 0, err
	}

	defer Rollback(tx)

	copied, err := copyVersionsIntoScopeTx(tx, toScopeID, fromScopeID)
	if err != nil {
		return 0, err
	}

	return copied, tx.Commit()
}

// SetEventPageTimeout replaces the bound on one event page and returns a func
// that restores the previous bound.
func SetEventPageTimeout(d time.Duration) (restore func()) {
	previous := eventPageTimeout
	eventPageTimeout = d
	return func() { eventPageTimeout = previous }
}

// UseEmptyDecodedLogEvents gives this process an empty set of decoded log
// events and returns a func that restores the previous set.
func UseEmptyDecodedLogEvents() (restore func()) {
	previous := decodedLogEvents
	decodedLogEvents = newLogEventCache(previous.limit)
	return func() { decodedLogEvents = previous }
}

// DecodedLogEventHeld reports whether this process holds the decoded text of
// a build's log event, from any row version.
func DecodedLogEventHeld(table string, buildID, eventID int) bool {
	decodedLogEvents.mu.Lock()
	defer decodedLogEvents.mu.Unlock()
	for key := range decodedLogEvents.entries {
		if key.table == table && key.build == buildID && key.event == eventID {
			return true
		}
	}
	return false
}

// DecodedLogEventHits returns how many pages this process has served from a
// held decoded log event.
func DecodedLogEventHits() int {
	decodedLogEvents.mu.Lock()
	defer decodedLogEvents.mu.Unlock()
	return decodedLogEvents.hits
}

// NewSigningKeyFactoryEncryptingWrites is the signing key factory as it
// behaves once migration.SigningKeysEncryptedAtRest is on, so the encrypted
// write path is proven before the release that turns it on.
func NewSigningKeyFactoryEncryptingWrites(logger lager.Logger, conn DbConn) SigningKeyFactory {
	return &signingKeyFactory{logger: logger.Session("signing-key-factory"), conn: conn, encryptOnWrite: true}
}
