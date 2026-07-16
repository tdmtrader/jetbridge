package schema

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// EventReader reads events line-by-line from an NDJSON stream.
// Each call to Read returns the next valid event or an error.
type EventReader struct {
	r       *bufio.Reader
	line    int
	skipped int
}

// maxEventLine caps a single NDJSON line at 5 MiB. An oversized line (e.g. a
// tool.call carrying captured output, or a foreign producer's event — contract
// §5 invites other producers to append) is SKIPPED, not fatal: agent-step
// ingestion stops its read loop on any reader error, so killing the stream on
// one giant line would silently discard every later cost.record and step.end
// event — under-ledgering spend and leaving the step as status=error even when
// a valid step.end followed (review findings, 2026-07-12 and 2026-07-16).
const maxEventLine = 5 << 20

// NewEventReader creates an EventReader that reads from r.
func NewEventReader(r io.Reader) *EventReader {
	return &EventReader{
		r: bufio.NewReaderSize(r, 64*1024),
	}
}

// Skipped reports how many oversized (> maxEventLine) lines were discarded.
// Callers should surface a non-zero count: skipped lines mean the stream was
// only partially ingested.
func (er *EventReader) Skipped() int {
	return er.skipped
}

// Read returns the next event from the NDJSON stream. It skips empty lines
// and discards (counting them via Skipped) lines longer than maxEventLine.
// Returns io.EOF when no more events are available. Parse and validation
// errors include the line number.
func (er *EventReader) Read() (*Event, error) {
	for {
		line, err := er.nextLine()
		if err != nil {
			return nil, err
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return nil, fmt.Errorf("line %d: invalid JSON: %w", er.line, err)
		}

		if err := event.Validate(); err != nil {
			return nil, fmt.Errorf("line %d: %w", er.line, err)
		}

		return &event, nil
	}
}

// nextLine returns the next newline-terminated line (delimiter included; the
// caller trims). Lines longer than maxEventLine are discarded — the reader
// resyncs at the next newline and continues — rather than poisoning the rest
// of the stream. A final unterminated line is returned as-is at io.EOF.
func (er *EventReader) nextLine() (string, error) {
	for {
		var buf []byte
		overflowed := false
		for {
			chunk, err := er.r.ReadSlice('\n')
			if !overflowed {
				if len(buf)+len(chunk) > maxEventLine {
					overflowed = true
					buf = nil
				} else {
					// ReadSlice's result aliases the internal buffer; append
					// copies it out before the next call invalidates it.
					buf = append(buf, chunk...)
				}
			}

			if errors.Is(err, bufio.ErrBufferFull) {
				continue // same line, more chunks
			}
			if errors.Is(err, io.EOF) {
				if overflowed {
					er.line++
					er.skipped++
					return "", io.EOF
				}
				if len(buf) > 0 {
					er.line++
					return string(buf), nil // unterminated final line
				}
				return "", io.EOF
			}
			if err != nil {
				return "", err
			}

			// Newline reached.
			er.line++
			if overflowed {
				er.skipped++
				break // discard this line; resync on the next one
			}
			return string(buf), nil
		}
	}
}
