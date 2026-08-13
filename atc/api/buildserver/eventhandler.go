package buildserver

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"sync"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/event"
	"github.com/vito/go-sse/sse"
)

const (
	ProtocolVersionHeader  = "X-ATC-Stream-Version"
	CurrentProtocolVersion = "2.0"
)

func NewEventHandler(logger lager.Logger, build db.BuildForAPI) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var eventID uint = 0

		// Parse Last-Event-ID header
		if lastEventID := r.Header.Get("Last-Event-ID"); lastEventID != "" {
			parsedID, err := strconv.ParseUint(lastEventID, 10, 64)
			if err != nil {
				logger.Info("failed-to-parse-last-event-id", lager.Data{"last-event-id": lastEventID})
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			eventID = uint(parsedID) + 1
		}

		// Set response headers
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("X-Accel-Buffering", "no")
		w.Header().Set(ProtocolVersionHeader, CurrentProtocolVersion)

		flusher, ok := w.(http.Flusher)
		if !ok {
			logger.Error("streaming-not-supported", nil)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		writer := eventWriter{
			responseWriter:  w,
			responseFlusher: flusher,
		}

		events, err := build.Events(eventID)
		if err != nil {
			logger.Error("failed-to-get-build-events", err, lager.Data{
				"build-id": build.ID(),
				"start":    eventID,
			})
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// Next() blocks until the build emits, which for an idle live build is
		// unbounded, so it cannot be waited on from the same goroutine that has
		// to notice the client leaving. Read it here and hand the results over
		// instead. The defers unwind in the order this goroutine needs: stop
		// wanting results, close the source so Next() returns, then reap.
		var reading sync.WaitGroup
		defer reading.Wait()
		defer db.Close(events)

		handlerDone := make(chan struct{})
		defer close(handlerDone)

		next := make(chan nextEvent, 1)

		reading.Add(1)
		go func() {
			defer reading.Done()

			for {
				ev, err := events.Next()

				select {
				case next <- nextEvent{envelope: ev, err: err}:
				case <-handlerDone:
					return
				}

				if err != nil {
					return
				}
			}
		}()

		for {
			contextLogger := logger.WithData(lager.Data{"id": eventID})

			var result nextEvent
			select {
			case result = <-next:
			case <-r.Context().Done():
				return
			}

			if result.err != nil {
				if result.err == db.ErrEndOfBuildEventStream {
					if err := writer.WriteEnd(eventID); err != nil {
						contextLogger.Info("failed-to-write-end", lager.Data{"error": err.Error()})
					}
					<-r.Context().Done()
				} else {
					contextLogger.Error("failed-to-get-next-build-event", result.err)
				}
				return
			}

			if err := writer.WriteEvent(eventID, result.envelope); err != nil {
				contextLogger.Info("failed-to-write-event", lager.Data{"error": err.Error()})
				return
			}

			eventID++
		}
	})
}

type nextEvent struct {
	envelope event.Envelope
	err      error
}

type eventWriter struct {
	responseWriter  io.Writer
	responseFlusher http.Flusher
}

func (writer eventWriter) WriteEvent(id uint, envelope any) error {
	payload, err := json.Marshal(envelope)
	if err != nil {
		return err
	}

	event := sse.Event{
		ID:   strconv.FormatUint(uint64(id), 10),
		Name: "event",
		Data: payload,
	}

	if err := event.Write(writer.responseWriter); err != nil {
		return err
	}

	writer.responseFlusher.Flush()
	return nil
}

func (writer eventWriter) WriteEnd(id uint) error {
	event := sse.Event{
		ID:   strconv.FormatUint(uint64(id), 10),
		Name: "end",
	}

	if err := event.Write(writer.responseWriter); err != nil {
		return err
	}

	writer.responseFlusher.Flush()
	return nil
}
