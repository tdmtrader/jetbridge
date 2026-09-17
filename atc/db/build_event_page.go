package db

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/concourse/concourse/atc"
)

var (
	ErrBuildEventCursor         = errors.New("INVALID_CURSOR: event cursor is stale, malformed or belongs to another build")
	ErrBuildEventStreamChanged  = errors.New("STREAM_CHANGED: earlier output changed; restart and replace accumulated output")
	ErrBuildEventsReaped        = errors.New("OUTPUT_RETAINED_AWAY: build output has been removed by retention")
	ErrBuildEventsUnavailable   = errors.New("OUTPUT_UNAVAILABLE: build or event storage is unavailable")
	ErrBuildEventTooLarge       = errors.New("EVENT_TOO_LARGE: indivisible event metadata exceeds 65536 bytes")
	ErrBuildEventStoredTooLarge = errors.New("STORED_EVENT_TOO_LARGE: stored event exceeds 8388608 JSON bytes; a smaller max_bytes cannot resolve this")
	ErrBuildEventPageTooSmall   = errors.New("PAGE_TOO_SMALL: max_bytes must fit the next complete UTF-8 character")
)

const eventPageMaxEvents = 128
const eventPageMaxEncoded = 256 * 1024
const eventMetadataMaxBytes = 64 * 1024
const eventStoredMaxBytes = 8 * 1024 * 1024

type eventPageCursor struct {
	Version int   `json:"v"`
	Build   int   `json:"b"`
	Event   int   `json:"e"`
	Offset  int   `json:"o"`
	Prefix  int64 `json:"n"`
}

func (c eventPageCursor) token() string {
	data, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(data)
}

func (b *build) EventPage(ctx context.Context, request atc.BuildEventPageRequest) (atc.BuildEventPage, error) {
	return finiteEventPage(ctx, b.conn, b.eventsTable(), b.id, request, func(ctx context.Context, tx Tx) (bool, bool, error) {
		var completed bool
		var reap sql.NullTime
		err := tx.QueryRowContext(ctx, "SELECT completed,reap_time FROM builds WHERE id=$1", b.id).Scan(&completed, &reap)
		if err == sql.ErrNoRows {
			return false, false, ErrBuildEventsUnavailable
		}
		return completed, reap.Valid, err
	})
}

func (b *inMemoryCheckBuildForApi) EventPage(ctx context.Context, request atc.BuildEventPageRequest) (atc.BuildEventPage, error) {
	return finiteEventPage(ctx, b.conn, "check_build_events", b.id, request, func(ctx context.Context, tx Tx) (bool, bool, error) {
		var current, last sql.NullInt64
		var status sql.NullString
		var start, end sql.NullTime
		err := tx.QueryRowContext(ctx, `SELECT r.in_memory_build_id,r.in_memory_build_status,rcs.last_check_build_id,rcs.last_check_start_time,rcs.last_check_end_time FROM resources r LEFT JOIN resource_config_scopes rcs ON rcs.id=r.resource_config_scope_id WHERE r.id=$1`, b.resourceId).Scan(&current, &status, &last, &start, &end)
		if err == sql.ErrNoRows {
			return false, false, ErrBuildEventsUnavailable
		}
		if err != nil {
			return false, false, err
		}
		if current.Valid && current.Int64 == int64(b.id) && status.String == string(BuildStatusStarted) {
			return false, false, nil
		}
		if last.Valid && last.Int64 == int64(b.id) {
			return start.Valid && end.Valid && start.Time.Before(end.Time), false, nil
		}
		if current.Valid && current.Int64 == int64(b.id) && status.Valid && status.String != string(BuildStatusStarted) {
			return true, false, nil
		}
		return false, false, ErrBuildEventsUnavailable
	})
}

// Prefix counts catch lower event IDs that committed after a previous snapshot.
// Numeric gaps alone are valid (rolled-back writes consume IDs). Every statement
// below shares one snapshot so a late commit cannot be baked into an unseen prefix.
func finiteEventPage(ctx context.Context, conn DbConn, table string, buildID int, request atc.BuildEventPageRequest, state func(context.Context, Tx) (bool, bool, error)) (atc.BuildEventPage, error) {
	page := atc.BuildEventPage{Events: []atc.BuildEventRecord{}}
	if request.MaxBytes == 0 {
		request.MaxBytes = 32768
	}
	if request.MaxBytes < 1 || request.MaxBytes > 65536 {
		return page, errors.New("max_bytes must be in 1..65536")
	}
	cursor := eventPageCursor{Version: 1, Build: buildID, Event: -1}
	if request.Cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(request.Cursor)
		if len(request.Cursor) > 2048 || err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Version != 1 || cursor.Build != buildID || cursor.Event < -1 || cursor.Offset < 0 || cursor.Prefix < 0 || (cursor.Event == -1 && (cursor.Offset != 0 || cursor.Prefix != 0)) {
			return page, ErrBuildEventCursor
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer Rollback(tx)
	completed, reaped, err := state(ctx, tx)
	if err != nil {
		return page, err
	}
	if reaped {
		return page, ErrBuildEventsReaped
	}
	predicate := "build_id=$1"
	if request.Cursor != "" && cursor.Event >= 0 {
		var count int64
		var anchor bool
		err = tx.QueryRowContext(ctx, "SELECT count(*),coalesce(bool_or(event_id=$2),false) FROM "+table+" WHERE "+predicate+" AND event_id <= $2", buildID, cursor.Event).Scan(&count, &anchor)
		if err != nil {
			return page, err
		}
		if !anchor {
			return page, ErrBuildEventCursor
		}
		if count != cursor.Prefix {
			return page, ErrBuildEventStreamChanged
		}
	}
	// Fetch only IDs and sizes in the bounded lookahead. pgx drains unread rows
	// on Close: selecting 129 large payloads would download them even if the
	// first one fills the page. Load the payloads that fit in one query, at most 8 MiB.
	where := "event_id > $2"
	if cursor.Offset > 0 {
		where = "event_id >= $2"
	}
	rows, err := tx.QueryContext(ctx, "SELECT event_id,type,version,octet_length(payload) FROM "+table+" WHERE "+predicate+" AND "+where+" ORDER BY event_id ASC LIMIT 129", buildID, cursor.Event)
	if err != nil {
		return page, err
	}
	type eventRow struct {
		id, size      int
		kind, version string
	}
	listed := make([]eventRow, 0, eventPageMaxEvents+1)
	for rows.Next() {
		var row eventRow
		if err = rows.Scan(&row.id, &row.kind, &row.version, &row.size); err != nil {
			rows.Close()
			return page, err
		}
		listed = append(listed, row)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return page, err
	}
	if err = rows.Close(); err != nil {
		return page, err
	}
	// Select the rows this page can hold before touching payloads: at most 128
	// events and 8 MiB of stored JSON, each row individually within its limit.
	wanted, storedBytes := []int{}, 0
	more := false
	for _, row := range listed {
		if row.size > eventStoredMaxBytes {
			return page, ErrBuildEventStoredTooLarge
		}
		if row.kind != "log" && row.size > eventMetadataMaxBytes {
			return page, ErrBuildEventTooLarge
		}
		if len(wanted) == eventPageMaxEvents || storedBytes+row.size > eventStoredMaxBytes {
			more = true
			break
		}
		storedBytes += row.size
		wanted = append(wanted, row.id)
	}
	payloads := map[int][]byte{}
	if len(wanted) > 0 {
		rows, err = tx.QueryContext(ctx, "SELECT event_id,payload FROM "+table+" WHERE "+predicate+" AND event_id = ANY($2)", buildID, wanted)
		if err != nil {
			return page, err
		}
		for rows.Next() {
			var id int
			var raw []byte
			if err = rows.Scan(&id, &raw); err != nil {
				rows.Close()
				return page, err
			}
			payloads[id] = raw
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return page, err
		}
		if err = rows.Close(); err != nil {
			return page, err
		}
	}
	remaining, encodedBytes := request.MaxBytes, 0
	anchorRead := cursor.Offset == 0
pageRows:
	for _, row := range listed[:len(wanted)] {
		raw := payloads[row.id]
		var data map[string]json.RawMessage
		if json.Unmarshal(raw, &data) != nil || data == nil {
			return page, errors.New("INVALID_EVENT: event data is not an object")
		}
		var logText string
		if row.kind == "log" {
			if json.Unmarshal(data["payload"], &logText) != nil {
				return page, errors.New("INVALID_EVENT: malformed log payload")
			}
			delete(data, "payload")
		}
		metadata, _ := json.Marshal(data)
		if len(metadata) > eventMetadataMaxBytes {
			return page, ErrBuildEventTooLarge
		}
		if cursor.Offset > 0 && !anchorRead {
			if row.id != cursor.Event || row.kind != "log" || cursor.Offset >= len(logText) || !utf8.RuneStart(logText[cursor.Offset]) {
				return page, ErrBuildEventCursor
			}
			anchorRead = true
		}
		next := cursor
		start := 0
		if row.id == cursor.Event {
			start = cursor.Offset
		} else {
			next.Prefix++
		}
		next.Event = row.id
		next.Offset = 0
		chunkLimit := remaining
		var record atc.BuildEventRecord
		var encoded []byte
		var text string
		for {
			if row.kind == "log" {
				if remaining == 0 && len(logText) > start {
					more = true
					break pageRows
				}
				end := min(start+chunkLimit, len(logText))
				for end > start && !utf8.ValidString(logText[start:end]) {
					end--
				}
				text = logText[start:end]
				if len(text) == 0 && len(logText) > start {
					if len(page.Events) > 0 {
						more = true
						break pageRows
					}
					if chunkLimit < remaining {
						return page, ErrBuildEventTooLarge
					}
					return page, ErrBuildEventPageTooSmall
				}
				data["payload"], _ = json.Marshal(text)
			}
			payload, err := json.Marshal(data)
			if err != nil {
				return page, err
			}
			record = atc.BuildEventRecord{ID: strconv.Itoa(row.id), Event: row.kind, Version: row.version, Data: payload}
			encoded, _ = json.Marshal(record)
			if encodedBytes+len(encoded) <= eventPageMaxEncoded-4096 {
				break
			}
			if row.kind == "log" && chunkLimit > 0 {
				chunkLimit /= 2
				continue
			}
			if len(page.Events) == 0 {
				return page, ErrBuildEventTooLarge
			}
			more = true
			break pageRows
		}
		if row.kind == "log" && start+len(text) < len(logText) {
			next.Offset = start + len(text)
		}
		page.Events = append(page.Events, record)
		encodedBytes += len(encoded) + 1
		cursor = next
		if row.kind == "log" {
			remaining -= len(text)
		}
		if cursor.Offset > 0 {
			more = true
			break
		}
	}
	if !anchorRead {
		return page, ErrBuildEventCursor
	}
	page.CaughtUp = !more
	page.Truncated = more
	page.Finished = completed && page.CaughtUp
	token := cursor.token()
	page.NextCursor = &token
	encoded, err := json.Marshal(page)
	if err != nil {
		return page, err
	}
	if len(encoded) > eventPageMaxEncoded {
		return page, fmt.Errorf("event page exceeds %d encoded bytes", eventPageMaxEncoded)
	}
	if err = tx.Commit(); err != nil {
		return page, err
	}
	return page, nil
}
