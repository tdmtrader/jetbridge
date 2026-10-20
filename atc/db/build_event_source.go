package db

import (
	"encoding/json"
	"errors"
	"strconv"
	"sync"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/event"
)

var ErrEndOfBuildEventStream = errors.New("end of build event stream")
var ErrBuildEventStreamClosed = errors.New("build event stream closed")

type EventSource interface {
	Next() (event.Envelope, error)
	Close() error
}

type buildCompleteWatcherFunc func(Tx, int) (bool, error)

func newBuildEventSource(
	buildID int,
	table string,
	conn DbConn,
	from uint,
	watcher buildCompleteWatcherFunc,
) (*buildEventSource, error) {
	wg := new(sync.WaitGroup)

	source := &buildEventSource{
		buildID: buildID,
		table:   table,

		conn: conn,

		events: make(chan event.Envelope, 2000),
		stop:   make(chan struct{}),
		wg:     wg,

		watcherFunc: watcher,
	}

	tx, err := conn.Begin()
	if err != nil {
		return nil, err
	}
	defer Rollback(tx)

	completed, err := watcher(tx, buildID)
	if err != nil {
		return nil, err
	}
	err = tx.Commit()
	if err != nil {
		return nil, err
	}

	if !completed {
		signal, err := conn.Bus().ListenSignal(buildEventsChannel(buildID))
		if err != nil {
			return nil, err
		}

		source.signal = signal
		source.signalChannel = buildEventsChannel(buildID)
	}

	wg.Add(1)
	go source.collectEvents(from, completed)

	return source, nil
}

type buildEventSource struct {
	buildID int
	table   string

	conn          DbConn
	signal        *NotifySignal
	signalChannel string

	events chan event.Envelope
	stop   chan struct{}
	err    error
	wg     *sync.WaitGroup

	watcherFunc buildCompleteWatcherFunc
}

func (source *buildEventSource) Next() (event.Envelope, error) {
	e, ok := <-source.events
	if !ok {
		return event.Envelope{}, source.err
	}

	return e, nil
}

func (source *buildEventSource) Close() error {
	select {
	case <-source.stop:
		// If closed already, then do nothing.
		return nil
	default:
		close(source.stop)
	}

	source.wg.Wait()

	if source.signal != nil {
		return source.conn.Bus().UnlistenSignal(source.signalChannel, source.signal)
	}
	return nil
}

// collectEvents feeds the stream until it ends, then records why it ended
// before closing the channel, so a reader blocked in Next always wakes and
// never mistakes a stream that stopped on an error for a clean empty event.
func (source *buildEventSource) collectEvents(from uint, completed bool) {
	defer source.wg.Done()

	source.err = source.collect(from, completed)
	close(source.events)
}

func (source *buildEventSource) collect(from uint, completed bool) error {
	batchSize := cap(source.events)
	// cursor points to the last emitted event, so subtract 1
	// (the first event is fetched using cursor == -1)
	cursor := int(from) - 1

	for {
		select {
		case <-source.stop:
			return ErrBuildEventStreamClosed
		default:
		}

		rowsReturned, err := source.collectBatch(&cursor, &completed, batchSize)
		if err != nil {
			return err
		}

		if rowsReturned == batchSize {
			// still more events
			continue
		}

		if completed {
			return ErrEndOfBuildEventStream
		}

		select {
		case <-source.signal.C():
		case <-source.stop:
			return ErrBuildEventStreamClosed
		}
	}
}

// collectBatch reads and emits up to batchSize events after the cursor in one
// transaction, advancing the cursor past each emitted event.
func (source *buildEventSource) collectBatch(cursor *int, completed *bool, batchSize int) (int, error) {
	tx, err := source.conn.Begin()
	if err != nil {
		return 0, err
	}

	defer Rollback(tx)

	if !*completed {
		*completed, err = source.watcherFunc(tx, source.buildID)
		if err != nil {
			return 0, err
		}
	}

	rows, err := psql.Select("event_id", "type", "version", "payload").
		From(source.table).
		Where(sq.Eq{"build_id": source.buildID}).
		Where(sq.Gt{"event_id": *cursor}).
		OrderBy("event_id ASC").
		Limit(uint64(batchSize)).
		RunWith(tx).
		Query()
	if err != nil {
		return 0, err
	}

	defer rows.Close()

	rowsReturned := 0

	for rows.Next() {
		rowsReturned++

		var t, v, p string
		err := rows.Scan(cursor, &t, &v, &p)
		if err != nil {
			return rowsReturned, err
		}

		data := json.RawMessage(p)

		ev := event.Envelope{
			Data:    &data,
			Event:   atc.EventType(t),
			Version: atc.EventVersion(v),
			EventID: strconv.Itoa(*cursor),
		}

		select {
		case source.events <- ev:
		case <-source.stop:
			return rowsReturned, ErrBuildEventStreamClosed
		}
	}

	if err := rows.Err(); err != nil {
		return rowsReturned, err
	}

	_ = rows.Close()

	return rowsReturned, tx.Commit()
}
