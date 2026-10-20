package db_test

import (
	"errors"
	"sync/atomic"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/event"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var errEventSourceTestDatabase = errors.New("database unavailable")

// eventSourceFailingConn is a pass-through decorator over the real
// connection: every statement still executes against Postgres, but once the
// allowed number of Begins (or Commits) is used up, the next one fails the way
// a dropped connection would.
type eventSourceFailingConn struct {
	db.DbConn
	begins        atomic.Int32
	allowedBegins int32 // < 0: never fail Begin
	commits       atomic.Int32
	allowedCommit int32 // < 0: never fail Commit
}

func (c *eventSourceFailingConn) Begin() (db.Tx, error) {
	if c.allowedBegins >= 0 && c.begins.Add(1) > c.allowedBegins {
		return nil, errEventSourceTestDatabase
	}
	tx, err := c.DbConn.Begin()
	if err != nil {
		return nil, err
	}
	return eventSourceFailingTx{Tx: tx, conn: c}, nil
}

type eventSourceFailingTx struct {
	db.Tx
	conn *eventSourceFailingConn
}

func (tx eventSourceFailingTx) Commit() error {
	if tx.conn.allowedCommit >= 0 && tx.conn.commits.Add(1) > tx.conn.allowedCommit {
		_ = tx.Tx.Rollback()
		return errEventSourceTestDatabase
	}
	return tx.Tx.Commit()
}

type nextResult struct {
	envelope event.Envelope
	err      error
}

func nextAsync(source db.EventSource) <-chan nextResult {
	result := make(chan nextResult, 1)
	go func() {
		envelope, err := source.Next()
		result <- nextResult{envelope, err}
	}()
	return result
}

var _ = Describe("Build event source", func() {
	var build db.Build

	BeforeEach(func() {
		var err error
		build, err = defaultTeam.CreateOneOffBuild()
		Expect(err).NotTo(HaveOccurred())
	})

	eventsThrough := func(conn *eventSourceFailingConn) db.EventSource {
		conn.DbConn = dbConn
		failing, found, err := db.NewBuildFactory(conn, lockFactory, 5*time.Minute, 5*time.Minute).Build(build.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())

		events, err := failing.Events(0)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = events.Close() })
		return events
	}

	It("ends the stream with the error when a batch cannot begin its transaction", func() {
		// The subscription's own check may begin; the first batch may not.
		events := eventsThrough(&eventSourceFailingConn{allowedBegins: 1, allowedCommit: -1})

		var result nextResult
		Eventually(nextAsync(events), "5s").Should(Receive(&result),
			"Next must not block forever once the stream can no longer read")
		Expect(result.err).To(MatchError(errEventSourceTestDatabase))
	})

	It("ends the stream with the error when a batch cannot commit", func() {
		Expect(build.SaveEvent(event.Log{Payload: "before the failure"})).To(Succeed())

		// The subscription's own check may commit; the first batch may not.
		events := eventsThrough(&eventSourceFailingConn{allowedBegins: -1, allowedCommit: 1})

		var result nextResult
		Eventually(nextAsync(events), "5s").Should(Receive(&result))
		Expect(result.err).NotTo(HaveOccurred())
		Expect(result.envelope).To(Equal(envelope(event.Log{Payload: "before the failure"}, "0")))

		Eventually(nextAsync(events), "5s").Should(Receive(&result))
		Expect(result.err).To(MatchError(errEventSourceTestDatabase),
			"a stream that stopped on a database error must not report a clean empty event")
	})
})
