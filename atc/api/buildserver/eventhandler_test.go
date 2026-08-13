package buildserver_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/concourse/concourse/atc/testhelpers"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	. "github.com/concourse/concourse/atc/api/buildserver"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/event"
	"github.com/vito/go-sse/sse"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type numberedEvent struct {
	Number int `json:"event"`
}

func (numberedEvent) EventType() atc.EventType  { return "fake" }
func (numberedEvent) Version() atc.EventVersion { return "42.0" }

func numberedEventData(number int, eventID int) string {
	return fmt.Sprintf(`{"data":{"event":%d},"event":"fake","version":"42.0","event_id":"%d"}`, number, eventID)
}

// closeWatchingBuild counts the Close calls the handler makes against the real
// event source it was handed. Nothing on db.EventSource reports that itself,
// and an unclosed source leaks a goroutine and a LISTEN connection.
type closeWatchingBuild struct {
	db.BuildForAPI

	closes atomic.Int64

	mutex   sync.Mutex
	sources []db.EventSource
}

func (b *closeWatchingBuild) Events(from uint) (db.EventSource, error) {
	source, err := b.BuildForAPI.Events(from)
	if err != nil {
		return nil, err
	}

	b.mutex.Lock()
	b.sources = append(b.sources, source)
	b.mutex.Unlock()

	return &closeWatchingSource{EventSource: source, closes: &b.closes}, nil
}

func (b *closeWatchingBuild) CloseCount() int {
	return int(b.closes.Load())
}

// ReleaseSources closes anything the handler left open, bypassing the counter
// so the assertion still sees what the handler did. A source left listening
// holds a session open and would wedge the suite on DROP DATABASE instead of
// failing the spec that noticed.
func (b *closeWatchingBuild) ReleaseSources() {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	for _, source := range b.sources {
		_ = source.Close()
	}
}

type closeWatchingSource struct {
	db.EventSource

	closes *atomic.Int64
}

func (s *closeWatchingSource) Close() error {
	s.closes.Add(1)
	return s.EventSource.Close()
}

// failAfterFirstEvent fails a real stream mid-flight, which a healthy build
// event source never does on its own.
type failAfterFirstEvent struct {
	db.BuildForAPI

	err error
}

func (b failAfterFirstEvent) Events(from uint) (db.EventSource, error) {
	source, err := b.BuildForAPI.Events(from)
	if err != nil {
		return nil, err
	}

	return &failingSource{EventSource: source, err: b.err}, nil
}

type failingSource struct {
	db.EventSource

	err    error
	served bool
}

func (s *failingSource) Next() (event.Envelope, error) {
	if s.served {
		return event.Envelope{}, s.err
	}

	s.served = true
	return s.EventSource.Next()
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
			var watchedBuild *closeWatchingBuild

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

				watchedBuild = &closeWatchingBuild{BuildForAPI: buildForAPI(build)}
				DeferCleanup(watchedBuild.ReleaseSources)
				handlerBuild = watchedBuild
			})

			AfterEach(func() {
				Eventually(watchedBuild.CloseCount, 30*time.Second).Should(Equal(1))
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

		Context("when the eventsource returns an error", func() {
			var watchedBuild *closeWatchingBuild
			var disaster error

			BeforeEach(func() {
				disaster = errors.New("a coffee machine")

				build := createBuild(createTeam("some-team"))
				Expect(build.SaveEvent(numberedEvent{1})).To(Succeed())
				Expect(build.Finish(db.BuildStatusSucceeded)).To(Succeed())

				watchedBuild = &closeWatchingBuild{
					BuildForAPI: failAfterFirstEvent{
						BuildForAPI: buildForAPI(build),
						err:         disaster,
					},
				}
				DeferCleanup(watchedBuild.ReleaseSources)
				handlerBuild = watchedBuild
			})

			AfterEach(func() {
				Eventually(watchedBuild.CloseCount, 30*time.Second).Should(Equal(1))
			})

			JustBeforeEach(func() {
				var err error

				client := &http.Client{
					Transport: &http.Transport{},
				}
				response, err = client.Do(request)
				Expect(err).NotTo(HaveOccurred())
			})

			It("just stops sending events", func() {
				reader := sse.NewReadCloser(response.Body)

				Expect(reader.Next()).To(Equal(sse.Event{
					ID:   "0",
					Name: "event",
					Data: []byte(numberedEventData(1, 0)),
				}))

				_, err := reader.Next()
				Expect(err).To(HaveOccurred())
				Expect(err).To(Equal(io.EOF))
			})
		})

		Context("when the event stream never ends", func() {
			var watchedBuild *closeWatchingBuild

			BeforeEach(func() {
				build := createBuild(createTeam("some-team"))
				Expect(build.SaveEvent(numberedEvent{1})).To(Succeed())

				// The build never finishes, and keeps emitting, so the handler
				// only ever stops because the client went away.
				stop := make(chan struct{})
				done := make(chan struct{})
				go func() {
					defer GinkgoRecover()
					defer close(done)

					for number := 2; ; number++ {
						select {
						case <-stop:
							return
						default:
						}

						if err := build.SaveEvent(numberedEvent{number}); err != nil {
							return
						}

						time.Sleep(time.Millisecond)
					}
				}()
				DeferCleanup(func() {
					close(stop)
					<-done
				})

				watchedBuild = &closeWatchingBuild{BuildForAPI: buildForAPI(build)}
				DeferCleanup(watchedBuild.ReleaseSources)
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
					Eventually(watchedBuild.CloseCount, 30*time.Second).Should(Equal(1))
				})
			})
		})

		Context("when the build is live but idle", func() {
			var watchedBuild *closeWatchingBuild

			BeforeEach(func() {
				build := createBuild(createTeam("some-team"))
				Expect(build.SaveEvent(numberedEvent{1})).To(Succeed())

				// The build never finishes and never emits again, so once the
				// handler has drained what is already there it is parked in
				// Next() with nothing to wake it but the client going away.
				watchedBuild = &closeWatchingBuild{BuildForAPI: buildForAPI(build)}
				DeferCleanup(watchedBuild.ReleaseSources)
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
				Eventually(watchedBuild.CloseCount, 30*time.Second).Should(Equal(1))
			})
		})

		Context("when subscribing to it fails", func() {
			BeforeEach(func() {
				// Losing the database connection is how subscribing really
				// fails: the event source opens a transaction before anything
				// else.
				severedConn := postgresRunner.OpenConn()
				build, found, err := db.NewBuildFactory(severedConn, lockFactory, 0, time.Hour).
					BuildForAPI(createBuild(createTeam("some-team")).ID())
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(severedConn.Close()).To(Succeed())

				handlerBuild = build
			})

			JustBeforeEach(func() {
				var err error

				client := &http.Client{
					Transport: &http.Transport{},
				}
				response, err = client.Do(request)
				Expect(err).NotTo(HaveOccurred())
			})

			It("returns 500", func() {
				Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
			})
		})
	})
})
