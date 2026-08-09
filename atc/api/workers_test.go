package api_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func apiWorker() atc.Worker {
	return atc.Worker{Name: "worker-name", ActiveContainers: 2, ActiveVolumes: 10, ActiveTasks: 42, ResourceTypes: []atc.WorkerResourceType{{Type: "some-resource", Image: "some-resource-image"}}, Platform: "haiku", Tags: []string{"not", "a", "limerick"}, Version: "1.2.3"}
}

func expectPersistedAPIWorker(actual db.Worker, expected atc.Worker, requestedAt, respondedAt time.Time) {
	Expect(actual.Name()).To(Equal(expected.Name))
	Expect(actual.ActiveContainers()).To(Equal(expected.ActiveContainers))
	Expect(actual.ActiveVolumes()).To(Equal(expected.ActiveVolumes))
	Expect(actual.ResourceTypes()).To(Equal(expected.ResourceTypes))
	Expect(actual.Platform()).To(Equal(expected.Platform))
	Expect(actual.Tags()).To(Equal([]string(expected.Tags)))
	Expect(actual.Version()).NotTo(BeNil())
	Expect(*actual.Version()).To(Equal(expected.Version))
	Expect(actual.ExpiresAt()).To(BeTemporally(">=", requestedAt.Add(30*time.Second)))
	Expect(actual.ExpiresAt()).To(BeTemporally("<=", respondedAt.Add(30*time.Second)))
}

type observedWorkerRegistration struct {
	worker atc.Worker
	ttl    time.Duration
}

type observingWorkerSaveFactory struct {
	db.WorkerFactory

	lock  sync.Mutex
	calls []observedWorkerRegistration
}

func (factory *observingWorkerSaveFactory) SaveWorker(worker atc.Worker, ttl time.Duration) (db.Worker, error) {
	factory.lock.Lock()
	factory.calls = append(factory.calls, observedWorkerRegistration{worker: worker, ttl: ttl})
	factory.lock.Unlock()
	return factory.WorkerFactory.SaveWorker(worker, ttl)
}

func (factory *observingWorkerSaveFactory) callSnapshot() []observedWorkerRegistration {
	factory.lock.Lock()
	defer factory.lock.Unlock()
	return append([]observedWorkerRegistration(nil), factory.calls...)
}

type workerTeamLookupResultFactory struct {
	db.TeamFactory

	teamName string
	team     db.Team
}

func (factory workerTeamLookupResultFactory) FindTeam(name string) (db.Team, bool, error) {
	team, found, err := factory.TeamFactory.FindTeam(name)
	if err != nil || !found || name != factory.teamName {
		return team, found, err
	}
	return factory.team, true, nil
}

type observingWorkerLookupFactory struct {
	db.WorkerFactory

	lock  sync.Mutex
	calls int
}

func (factory *observingWorkerLookupFactory) GetWorker(name string) (db.Worker, bool, error) {
	factory.lock.Lock()
	factory.calls++
	factory.lock.Unlock()
	return factory.WorkerFactory.GetWorker(name)
}

func (factory *observingWorkerLookupFactory) callCount() int {
	factory.lock.Lock()
	defer factory.lock.Unlock()
	return factory.calls
}

type workerLookupResultFactory struct {
	db.WorkerFactory

	workerName string
	worker     db.Worker
}

func (factory workerLookupResultFactory) GetWorker(name string) (db.Worker, bool, error) {
	if name == factory.workerName {
		return factory.worker, true, nil
	}
	return factory.WorkerFactory.GetWorker(name)
}

var _ = Describe("Workers API", func() {
	Describe("GET /api/v1/workers", func() {
		var (
			realdb             *realDB
			response           *http.Response
			global, own, other db.Worker
		)
		BeforeEach(func() {
			realdb = useRealDB()
			server = realdb.Serve()
			someTeam, err := realdb.Deps.teamFactory.CreateTeam(atc.Team{Name: "some-team"})
			Expect(err).NotTo(HaveOccurred())
			otherTeam, err := realdb.Deps.teamFactory.CreateTeam(atc.Team{Name: "other-team"})
			Expect(err).NotTo(HaveOccurred())
			global, err = realdb.Deps.workerFactory.SaveWorker(atc.Worker{Name: "global-worker", Platform: "linux", Version: "1.2.3", State: string(db.WorkerStateRunning), Tags: []string{"global"}}, 0)
			Expect(err).NotTo(HaveOccurred())
			own, err = someTeam.SaveWorker(atc.Worker{Name: "some-team-worker", Platform: "linux", Version: "1.2.3", State: string(db.WorkerStateRunning), Tags: []string{"own"}}, 0)
			Expect(err).NotTo(HaveOccurred())
			other, err = otherTeam.SaveWorker(atc.Worker{Name: "other-team-worker", Platform: "linux", Version: "1.2.3", State: string(db.WorkerStateRunning), Tags: []string{"other"}}, 0)
			Expect(err).NotTo(HaveOccurred())
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.TeamNamesReturns([]string{"some-team"})
		})
		JustBeforeEach(func() {
			var err error
			response, err = client.Get(server.URL + "/api/v1/workers")
			Expect(err).NotTo(HaveOccurred())
		})
		It("shows global and authorized-team workers", func() {
			var workers []atc.Worker
			Expect(json.NewDecoder(response.Body).Decode(&workers)).To(Succeed())
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(response.Header.Get("Content-Type")).To(Equal("application/json"))
			Expect(workers).To(HaveLen(2))
			Expect([]string{workers[0].Name, workers[1].Name}).To(ConsistOf(global.Name(), own.Name()))
		})
		Context("when the user is an admin", func() {
			BeforeEach(func() { fakeAccess.IsAdminReturns(true) })
			It("shows all workers", func() {
				var workers []atc.Worker
				Expect(json.NewDecoder(response.Body).Decode(&workers)).To(Succeed())
				Expect(workers).To(HaveLen(3))
				Expect([]string{workers[0].Name, workers[1].Name, workers[2].Name}).To(ConsistOf(global.Name(), own.Name(), other.Name()))
			})
		})
		Context("when listing workers fails", func() {
			BeforeEach(func() {
				doomed := postgresRunner.OpenConn()
				Expect(doomed.Close()).To(Succeed())
				deps := realdb.Deps
				deps.workerFactory = db.NewWorkerFactory(doomed, db.NewStaticWorkerCache(logger, doomed, 0))
				server = newAPIServer(deps)
				DeferCleanup(server.Close)
			})
			It("returns 500", func() { Expect(response.StatusCode).To(Equal(http.StatusInternalServerError)) })
		})
		Context("when not authenticated", func() {
			BeforeEach(func() { fakeAccess.IsAuthenticatedReturns(false) })
			It("returns 401", func() { Expect(response.StatusCode).To(Equal(http.StatusUnauthorized)) })
		})
	})

	Describe("POST /api/v1/workers", func() {
		var (
			realdb      *realDB
			requestedAt time.Time
			respondedAt time.Time
			worker      atc.Worker
			ttl         string
			response    *http.Response
		)
		BeforeEach(func() {
			realdb = useRealDB()
			server = realdb.Serve()
			Expect(realdb.Conn.QueryRow("SELECT NOW()").Scan(&requestedAt)).To(Succeed())
			worker = apiWorker()
			ttl = "30s"
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			fakeAccess.IsSystemReturns(true)
		})
		JustBeforeEach(func() {
			payload, err := json.Marshal(worker)
			Expect(err).NotTo(HaveOccurred())
			req, err := http.NewRequest("POST", server.URL+"/api/v1/workers?ttl="+ttl, io.NopCloser(bytes.NewBuffer(payload)))
			Expect(err).NotTo(HaveOccurred())
			response, err = client.Do(req)
			Expect(err).NotTo(HaveOccurred())
			Expect(realdb.Conn.QueryRow("SELECT NOW()").Scan(&respondedAt)).To(Succeed())
		})
		Context("for a global worker", func() {
			It("persists worker registration through PostgreSQL", func() {
				Expect(response.StatusCode).To(Equal(http.StatusOK))
				registered, found, err := realdb.Deps.workerFactory.GetWorker("worker-name")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				expectPersistedAPIWorker(registered, worker, requestedAt, respondedAt)
				Expect(registered.TeamName()).To(BeEmpty())
			})

			// Retained forwarding seam: ActiveTasks is registration telemetry and
			// factory input; workers.active_tasks is runtime placement state.
			Context("when observing registration forwarding", func() {
				var observer *observingWorkerSaveFactory

				BeforeEach(func() {
					observer = &observingWorkerSaveFactory{WorkerFactory: realdb.Deps.workerFactory}
					deps := realdb.Deps
					deps.workerFactory = observer
					server = newAPIServer(deps)
					DeferCleanup(server.Close)
				})

				It("forwards active task telemetry and TTL to SaveWorker", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
					calls := observer.callSnapshot()
					Expect(calls).To(Equal([]observedWorkerRegistration{{
						worker: worker,
						ttl:    30 * time.Second,
					}}))
					Expect(calls[0].worker.ActiveTasks).To(Equal(42))
					_, found, err := realdb.Deps.workerFactory.GetWorker(worker.Name)
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())
				})
			})
		})
		Context("for a team worker", func() {
			var someTeam db.Team
			BeforeEach(func() {
				var err error
				someTeam, err = realdb.Deps.teamFactory.CreateTeam(atc.Team{Name: "some-team"})
				Expect(err).NotTo(HaveOccurred())
				worker.Team = "some-team"
			})
			It("persists the worker with its team", func() {
				Expect(response.StatusCode).To(Equal(http.StatusOK))
				registered, found, err := realdb.Deps.workerFactory.GetWorker("worker-name")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				expectPersistedAPIWorker(registered, worker, requestedAt, respondedAt)
				Expect(registered.TeamName()).To(Equal("some-team"))
				Expect(registered.TeamID()).To(Equal(someTeam.ID()))
			})
			Context("when saving after lookup fails", func() {
				var foundTeam *dbfakes.FakeTeam
				BeforeEach(func() {
					// Retained fault seam: Team.SaveWorker must fail after FindTeam succeeds.
					// A real Team returned by CreateTeam shares the factory connection, so
					// closing it makes FindTeam fail before Team.SaveWorker is reached.
					foundTeam = new(dbfakes.FakeTeam)
					foundTeam.SaveWorkerReturns(nil, errors.New("oh no!"))
					deps := realdb.Deps
					deps.workerTeamFactory = workerTeamLookupResultFactory{
						TeamFactory: deps.workerTeamFactory,
						teamName:    "some-team",
						team:        foundTeam,
					}
					server = newAPIServer(deps)
					DeferCleanup(server.Close)
				})
				It("returns 500", func() { Expect(response.StatusCode).To(Equal(http.StatusInternalServerError)) })
			})
		})
		Context("when the team does not exist", func() {
			BeforeEach(func() { worker.Team = "some-team" })
			It("returns 400", func() { Expect(response.StatusCode).To(Equal(http.StatusBadRequest)) })
		})
		Context("when request is not from tsa", func() {
			BeforeEach(func() { fakeAccess.IsSystemReturns(false) })
			It("return 403", func() { Expect(response.StatusCode).To(Equal(http.StatusForbidden)) })
		})
		Context("when the worker has no name", func() {
			BeforeEach(func() { worker.Name = "" })
			It("returns 400 without saving it", func() {
				Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
				_, found, err := realdb.Deps.workerFactory.GetWorker("worker-name")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeFalse())
			})
		})
		Context("when saving a global worker fails", func() {
			BeforeEach(func() {
				doomed := postgresRunner.OpenConn()
				Expect(doomed.Close()).To(Succeed())
				deps := realdb.Deps
				deps.workerFactory = db.NewWorkerFactory(doomed, db.NewStaticWorkerCache(logger, doomed, 0))
				server = newAPIServer(deps)
				DeferCleanup(server.Close)
			})
			It("returns 500", func() { Expect(response.StatusCode).To(Equal(http.StatusInternalServerError)) })
		})
		Context("when the TTL is invalid", func() {
			BeforeEach(func() { ttl = "invalid-duration" })
			It("returns 400 without persisting the worker", func() {
				Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
				Expect(io.ReadAll(response.Body)).To(Equal([]byte("malformed ttl")))
				registered, found, err := realdb.Deps.workerFactory.GetWorker(worker.Name)
				Expect(err).NotTo(HaveOccurred())
				Expect(registered).To(BeNil())
				Expect(found).To(BeFalse())
			})
		})
		Context("when worker version is invalid", func() {
			BeforeEach(func() { worker.Version = "invalid" })
			It("returns 400 without persisting the worker", func() {
				Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
				Expect(io.ReadAll(response.Body)).To(Equal([]byte("invalid worker version, only numeric characters are allowed")))
				registered, found, err := realdb.Deps.workerFactory.GetWorker(worker.Name)
				Expect(err).NotTo(HaveOccurred())
				Expect(registered).To(BeNil())
				Expect(found).To(BeFalse())
			})
		})
		Context("when not authenticated", func() {
			BeforeEach(func() { fakeAccess.IsAuthenticatedReturns(false) })
			It("returns 401 without saving the worker", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
				_, found, err := realdb.Deps.workerFactory.GetWorker("worker-name")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeFalse())
			})
		})
	})

	Describe("DELETE /api/v1/workers/:worker_name", func() {
		var (
			realdb   *realDB
			response *http.Response
		)
		BeforeEach(func() {
			realdb = useRealDB()
			server = realdb.Serve()
			fakeAccess.IsAuthenticatedReturns(true)
		})
		JustBeforeEach(func() {
			req, err := http.NewRequest("DELETE", server.URL+"/api/v1/workers/some-worker", nil)
			Expect(err).NotTo(HaveOccurred())
			response, err = client.Do(req)
			Expect(err).NotTo(HaveOccurred())
		})
		assertDeleted := func() {
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			worker, found, err := realdb.Deps.workerFactory.GetWorker("some-worker")
			Expect(err).NotTo(HaveOccurred())
			Expect(worker).To(BeNil())
			Expect(found).To(BeFalse())
		}
		Context("when the user is system", func() {
			BeforeEach(func() {
				_, err := realdb.Deps.workerFactory.SaveWorker(atc.Worker{Name: "some-worker", Version: "1.2.3"}, 0)
				Expect(err).NotTo(HaveOccurred())
				fakeAccess.IsSystemReturns(true)
			})
			It("deletes a global worker", assertDeleted)
		})
		Context("when the user is an admin", func() {
			BeforeEach(func() {
				_, err := realdb.Deps.workerFactory.SaveWorker(atc.Worker{Name: "some-worker", Version: "1.2.3"}, 0)
				Expect(err).NotTo(HaveOccurred())
				fakeAccess.IsAdminReturns(true)
			})
			It("deletes a global worker", assertDeleted)
		})
		Context("when the user is authorized for its team", func() {
			BeforeEach(func() {
				team, err := realdb.Deps.teamFactory.CreateTeam(atc.Team{Name: "some-team"})
				Expect(err).NotTo(HaveOccurred())
				_, err = team.SaveWorker(atc.Worker{Name: "some-worker", Version: "1.2.3"}, 0)
				Expect(err).NotTo(HaveOccurred())
				fakeAccess.IsAuthorizedReturns(true)
			})
			It("deletes a team worker", assertDeleted)
		})
		Context("when the worker has already been deleted", func() {
			BeforeEach(func() { fakeAccess.IsSystemReturns(true) })
			It("returns 500", func() { Expect(response.StatusCode).To(Equal(http.StatusInternalServerError)) })
		})
		Context("when not authenticated", func() {
			var observer *observingWorkerLookupFactory

			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
				observer = &observingWorkerLookupFactory{WorkerFactory: realdb.Deps.workerFactory}
				deps := realdb.Deps
				deps.workerFactory = observer
				server = newAPIServer(deps)
				DeferCleanup(server.Close)
			})
			It("returns 401 without looking up the worker", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
				Expect(observer.callCount()).To(BeZero())
			})
		})
		Context("when deletion fails after lookup", func() {
			var fakeWorker *dbfakes.FakeWorker
			BeforeEach(func() {
				// Retained fault seam: Worker.Delete must fail after GetWorker succeeds;
				// a closed WorkerFactory fails the lookup before this method.
				fakeWorker = new(dbfakes.FakeWorker)
				fakeWorker.DeleteReturns(errors.New("some-error"))
				deps := realdb.Deps
				deps.workerFactory = workerLookupResultFactory{
					WorkerFactory: deps.workerFactory,
					workerName:    "some-worker",
					worker:        fakeWorker,
				}
				server = newAPIServer(deps)
				DeferCleanup(server.Close)
				fakeAccess.IsSystemReturns(true)
			})
			It("returns 500", func() { Expect(response.StatusCode).To(Equal(http.StatusInternalServerError)) })
		})
	})
})
