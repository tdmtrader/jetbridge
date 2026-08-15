package buildserver_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	. "github.com/concourse/concourse/atc/testhelpers"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	. "github.com/concourse/concourse/atc/api/buildserver"
	"github.com/concourse/concourse/atc/db"
	"github.com/vito/go-sse/sse"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type numberedEvent struct {
	Number int `json:"event"`
}

func (numberedEvent) EventType() atc.EventType  { return "numbered" }
func (numberedEvent) Version() atc.EventVersion { return "42.0" }

func numberedEventData(number int, eventID int) string {
	return fmt.Sprintf(`{"data":{"event":%d},"event":"numbered","version":"42.0","event_id":"%d"}`, number, eventID)
}

// eventStreamLifecycleBuild keeps the production event source intact and adds
// a one-shot signal for its externally important close lifecycle. Closing a
// live stream must release its goroutine and PostgreSQL LISTEN connection.
type eventStreamLifecycleBuild struct {
	db.BuildForAPI

	mutex        sync.Mutex
	openSource   db.EventSource
	streamClosed chan struct{}
}

func newEventStreamLifecycleBuild(build db.BuildForAPI) *eventStreamLifecycleBuild {
	return &eventStreamLifecycleBuild{
		BuildForAPI:  build,
		streamClosed: make(chan struct{}),
	}
}

func (b *eventStreamLifecycleBuild) Events(from uint) (db.EventSource, error) {
	source, err := b.BuildForAPI.Events(from)
	if err != nil {
		return nil, err
	}

	b.mutex.Lock()
	b.openSource = source
	b.mutex.Unlock()

	return &eventStreamLifecycleSource{
		EventSource: source,
		closed:      b.streamClosed,
	}, nil
}

// releaseOpenStream prevents a failed lifecycle assertion from wedging suite
// teardown on the source's PostgreSQL listener.
func (b *eventStreamLifecycleBuild) releaseOpenStream() {
	b.mutex.Lock()
	openSource := b.openSource
	b.mutex.Unlock()

	if openSource != nil {
		_ = openSource.Close()
	}
}

type eventStreamLifecycleSource struct {
	db.EventSource

	closed chan struct{}
	once   sync.Once
}

func (s *eventStreamLifecycleSource) Close() error {
	err := s.EventSource.Close()
	s.once.Do(func() { close(s.closed) })
	return err
}

var _ = Describe("Handler", func() {
	var (
		handlerBuild db.BuildForAPI

		handlerReturned chan struct{}

		server *httptest.Server
	)

	BeforeEach(func() {
		handlerBuild = nil
		handlerReturned = make(chan struct{}, 1)

		// Each context picks its own build, so resolve it when the request
		// arrives rather than when the server starts.
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				select {
				case handlerReturned <- struct{}{}:
				default:
				}
			}()

			NewEventHandler(lagertest.NewTestLogger("test"), handlerBuild).ServeHTTP(w, r)
		}))
		DeferCleanup(server.Close)
	})

	Describe("GET", func() {
		var (
			request  *http.Request
			response *http.Response
		)

		BeforeEach(func() {
			var err error

			request, err = http.NewRequest("GET", server.URL, nil)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when subscribing to the build succeeds", func() {
			BeforeEach(func() {
				team := createTeam("some-team")

				build := createBuild(team)
				Expect(build.SaveEvent(numberedEvent{1})).To(Succeed())
				Expect(build.SaveEvent(numberedEvent{2})).To(Succeed())
				Expect(build.SaveEvent(numberedEvent{3})).To(Succeed())
				Expect(build.Finish(db.BuildStatusSucceeded)).To(Succeed())

				// A sibling build shares the team's build_events partition, so
				// its events are what a build_id-blind query would leak.
				sibling := createBuild(team)
				Expect(sibling.SaveEvent(numberedEvent{99})).To(Succeed())
				Expect(sibling.Finish(db.BuildStatusSucceeded)).To(Succeed())

				handlerBuild = buildForAPI(build)
			})

			JustBeforeEach(func() {
				var err error

				client := &http.Client{
					Transport: &http.Transport{},
				}
				response, err = client.Do(request)
				Expect(err).NotTo(HaveOccurred())
			})

			It("returns 200", func() {
				_ = response.Body.Close()
				Expect(response.StatusCode).To(Equal(http.StatusOK))
			})

			It("returns Content-Type as text/event-stream", func() {
				_ = response.Body.Close()
				expectedHeaderEntries := map[string]string{
					"Content-Type":      "text/event-stream; charset=utf-8",
					"Cache-Control":     "no-cache, no-store, must-revalidate",
					"X-Accel-Buffering": "no",
				}
				Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))

				expectedHeaderEntries = map[string]string{
					"Connection": "keep-alive",
				}
				Expect(response).ShouldNot(IncludeHeaderEntries(expectedHeaderEntries))

			})

			It("returns the protocol version as X-ATC-Stream-Version", func() {
				_ = response.Body.Close()
				expectedHeaderEntries := map[string]string{
					"X-Atc-Stream-Version": "2.0",
				}
				Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
			})

			It("emits this build's events from the start, followed by an end event", func() {
				defer db.Close(response.Body)
				reader := sse.NewReadCloser(response.Body)

				Expect(reader.Next()).To(Equal(sse.Event{
					ID:   "0",
					Name: "event",
					Data: []byte(numberedEventData(1, 0)),
				}))

				Expect(reader.Next()).To(Equal(sse.Event{
					ID:   "1",
					Name: "event",
					Data: []byte(numberedEventData(2, 1)),
				}))

				Expect(reader.Next()).To(Equal(sse.Event{
					ID:   "2",
					Name: "event",
					Data: []byte(numberedEventData(3, 2)),
				}))

				status, err := reader.Next()
				Expect(err).NotTo(HaveOccurred())
				Expect(status.ID).To(Equal("3"))
				Expect(status.Name).To(Equal("event"))
				Expect(string(status.Data)).To(ContainSubstring(`"event":"status"`))

				Expect(reader.Next()).To(Equal(sse.Event{
					ID:   "4",
					Name: "end",
					Data: []byte{},
				}))
			})

			Context("when the Last-Event-ID header is given", func() {
				BeforeEach(func() {
					request.Header.Set("Last-Event-ID", "1")
				})

				It("starts subscribing from after the id", func() {
					defer db.Close(response.Body)
					reader := sse.NewReadCloser(response.Body)

					Expect(reader.Next()).To(Equal(sse.Event{
						ID:   "2",
						Name: "event",
						Data: []byte(numberedEventData(3, 2)),
					}))
				})
			})
		})

		Context("when the event stream never ends", func() {
			var watchedBuild *eventStreamLifecycleBuild

			BeforeEach(func() {
				build := createBuild(createTeam("some-team"))
				Expect(build.SaveEvent(numberedEvent{1})).To(Succeed())

				// An unfinished build's production source remains open while it
				// waits for another event; only the client disconnect ends it.
				watchedBuild = newEventStreamLifecycleBuild(buildForAPI(build))
				DeferCleanup(watchedBuild.releaseOpenStream)
				handlerBuild = watchedBuild
			})

			JustBeforeEach(func() {
				var err error

				client := &http.Client{
					Transport: &http.Transport{},
				}
				response, err = client.Do(request)
				Expect(err).NotTo(HaveOccurred())
			})

			Context("when request accepts gzip", func() {
				BeforeEach(func() {
					request.Header.Set("Accept-Encoding", "gzip")
				})

				It("closes the event stream when connection is closed", func() {
					err := response.Body.Close()
					Expect(err).NotTo(HaveOccurred())
					Eventually(watchedBuild.streamClosed, 30*time.Second).Should(BeClosed())
				})
			})
		})

		Context("when the build is live but idle", func() {
			var watchedBuild *eventStreamLifecycleBuild

			BeforeEach(func() {
				build := createBuild(createTeam("some-team"))
				Expect(build.SaveEvent(numberedEvent{1})).To(Succeed())

				// The build never finishes and never emits again, so once the
				// handler has drained what is already there it is parked in
				// Next() with nothing to wake it but the client going away.
				watchedBuild = newEventStreamLifecycleBuild(buildForAPI(build))
				DeferCleanup(watchedBuild.releaseOpenStream)
				handlerBuild = watchedBuild
			})

			It("returns and closes the event source when the client disconnects", func() {
				client := &http.Client{
					Transport: &http.Transport{},
				}
				response, err := client.Do(request)
				Expect(err).NotTo(HaveOccurred())

				reader := sse.NewReadCloser(response.Body)
				Expect(reader.Next()).To(Equal(sse.Event{
					ID:   "0",
					Name: "event",
					Data: []byte(numberedEventData(1, 0)),
				}))

				Expect(response.Body.Close()).To(Succeed())

				Eventually(handlerReturned, 30*time.Second).Should(Receive())
				Eventually(watchedBuild.streamClosed, 30*time.Second).Should(BeClosed())
			})
		})
	})
})
