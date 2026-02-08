package api_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	. "github.com/concourse/concourse/atc/testhelpers"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Workers API", func() {

	Describe("GET /api/v1/workers", func() {
		var response *http.Response

		JustBeforeEach(func() {
			req, err := http.NewRequest("GET", server.URL+"/api/v1/workers", nil)
			Expect(err).NotTo(HaveOccurred())

			response, err = client.Do(req)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authenticated", func() {
			var (
				teamWorker1 *dbfakes.FakeWorker
				teamWorker2 *dbfakes.FakeWorker
			)

			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(true)
				fakeAccess.IsAuthorizedReturns(true)
				fakeAccess.TeamNamesReturns([]string{"some-team"})
				dbWorkerFactory.VisibleWorkersReturns(nil, nil)

				teamWorker1 = new(dbfakes.FakeWorker)

				teamWorker2 = new(dbfakes.FakeWorker)
			})

			It("fetches workers by team name from worker user context", func() {
				Expect(dbWorkerFactory.VisibleWorkersCallCount()).To(Equal(1))

				teamNames := dbWorkerFactory.VisibleWorkersArgsForCall(0)
				Expect(teamNames).To(ConsistOf("some-team"))
			})

			Context("when user is an admin", func() {
				BeforeEach(func() {
					fakeAccess.IsAdminReturns(true)
					dbWorkerFactory.WorkersReturns([]db.Worker{
						teamWorker1,
						teamWorker2,
					}, nil)
				})

				It("returns all the workers", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
					expectedHeaderEntries := map[string]string{
						"Content-Type": "application/json",
					}
					Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))

					Expect(dbWorkerFactory.WorkersCallCount()).To(Equal(1))

					var returnedWorkers []atc.Worker
					err := json.NewDecoder(response.Body).Decode(&returnedWorkers)
					Expect(err).NotTo(HaveOccurred())

					Expect(returnedWorkers).To(Equal([]atc.Worker{
						{},
						{},
					}))
				})
			})

			Context("when the workers can be listed", func() {
				BeforeEach(func() {
					dbWorkerFactory.VisibleWorkersReturns([]db.Worker{
						teamWorker1,
						teamWorker2,
					}, nil)
				})

				It("returns 200", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})

				It("returns Content-Type 'application/json'", func() {
					expectedHeaderEntries := map[string]string{
						"Content-Type": "application/json",
					}
					Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
				})

				It("returns the workers", func() {
					var returnedWorkers []atc.Worker
					err := json.NewDecoder(response.Body).Decode(&returnedWorkers)
					Expect(err).NotTo(HaveOccurred())

					Expect(dbWorkerFactory.VisibleWorkersCallCount()).To(Equal(1))

					Expect(returnedWorkers).To(Equal([]atc.Worker{
						{},
						{},
					}))

				})
			})

			Context("when getting the workers fails", func() {
				BeforeEach(func() {
					dbWorkerFactory.VisibleWorkersReturns(nil, errors.New("error!"))
				})

				It("returns 500", func() {
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				})
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

			It("returns 401", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	Describe("POST /api/v1/workers", func() {
		var (
			worker atc.Worker
			ttl    string

			response *http.Response
		)

		BeforeEach(func() {
			worker = atc.Worker{
				Name:             "worker-name",
				ActiveContainers: 2,
				ActiveVolumes:    10,
				ActiveTasks:      42,
				ResourceTypes: []atc.WorkerResourceType{
					{Type: "some-resource", Image: "some-resource-image"},
				},
				Platform: "haiku",
				Tags:     []string{"not", "a", "limerick"},
				Version:  "1.2.3",
			}

			ttl = "30s"
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
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(true)
			})

			It("tries to save the worker", func() {
				Expect(dbWorkerFactory.SaveWorkerCallCount()).To(Equal(1))
				savedWorker, savedTTL := dbWorkerFactory.SaveWorkerArgsForCall(0)
				Expect(savedWorker).To(Equal(atc.Worker{
					Name:             "worker-name",
					ActiveContainers: 2,
					ActiveVolumes:    10,
					ActiveTasks:      42,
					ResourceTypes: []atc.WorkerResourceType{
						{Type: "some-resource", Image: "some-resource-image"},
					},
					Platform: "haiku",
					Tags:     []string{"not", "a", "limerick"},
					Version:  "1.2.3",
				}))

				Expect(savedTTL.String()).To(Equal(ttl))
			})

			Context("when request is not from tsa", func() {
				Context("when system claim is false", func() {
					BeforeEach(func() {
						fakeAccess.IsSystemReturns(false)
					})

					It("return 403", func() {
						Expect(response.StatusCode).To(Equal(http.StatusForbidden))
					})
				})
			})

			Context("when payload contains team name", func() {
				BeforeEach(func() {
					worker.Team = "some-team"
				})

				Context("when specified team exists", func() {
					var foundTeam *dbfakes.FakeTeam

					BeforeEach(func() {
						foundTeam = new(dbfakes.FakeTeam)
						dbWorkerTeamFactory.FindTeamReturns(foundTeam, true, nil)
					})

					It("saves team name in db", func() {
						Expect(foundTeam.SaveWorkerCallCount()).To(Equal(1))
					})

					Context("when saving the worker succeeds", func() {
						BeforeEach(func() {
							foundTeam.SaveWorkerReturns(new(dbfakes.FakeWorker), nil)
						})

						It("returns 200", func() {
							Expect(response.StatusCode).To(Equal(http.StatusOK))
						})
					})

					Context("when saving the worker fails", func() {
						BeforeEach(func() {
							foundTeam.SaveWorkerReturns(nil, errors.New("oh no!"))
						})

						It("returns 500", func() {
							Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
						})
					})
				})

				Context("when specified team does not exist", func() {
					BeforeEach(func() {
						dbWorkerTeamFactory.FindTeamReturns(nil, false, nil)
					})

					It("returns 400", func() {
						Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
					})
				})
			})

			Context("when the worker has no name", func() {
				BeforeEach(func() {
					worker.Name = ""
				})

				It("returns 400", func() {
					Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
				})

				It("does not save it", func() {
					Expect(dbWorkerFactory.SaveWorkerCallCount()).To(BeZero())
				})
			})

			Context("when saving the worker succeeds", func() {
				var fakeWorker *dbfakes.FakeWorker
				BeforeEach(func() {
					fakeWorker = new(dbfakes.FakeWorker)
					dbWorkerFactory.SaveWorkerReturns(fakeWorker, nil)
				})

				It("returns 200", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})

			})

			Context("when saving the worker fails", func() {
				BeforeEach(func() {
					dbWorkerFactory.SaveWorkerReturns(nil, errors.New("oh no!"))
				})

				It("returns 500", func() {
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				})
			})

			Context("when the TTL is invalid", func() {
				BeforeEach(func() {
					ttl = "invalid-duration"
				})

				It("returns 400", func() {
					Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
				})

				It("returns the validation error in the response body", func() {
					Expect(io.ReadAll(response.Body)).To(Equal([]byte("malformed ttl")))
				})

				It("does not save it", func() {
					Expect(dbWorkerFactory.SaveWorkerCallCount()).To(BeZero())
				})
			})


			Context("when worker version is invalid", func() {
				BeforeEach(func() {
					worker.Version = "invalid"
				})

				It("returns 400", func() {
					Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
				})

				It("returns the validation error in the response body", func() {
					Expect(io.ReadAll(response.Body)).To(Equal([]byte("invalid worker version, only numeric characters are allowed")))
				})

				It("does not save it", func() {
					Expect(dbWorkerFactory.SaveWorkerCallCount()).To(BeZero())
				})
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

			It("returns 401", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})

			It("does not save the config", func() {
				Expect(dbWorkerFactory.SaveWorkerCallCount()).To(BeZero())
			})
		})
	})

	Describe("DELETE /api/v1/workers/:worker_name", func() {
		var (
			response   *http.Response
			workerName string
			fakeWorker *dbfakes.FakeWorker
		)

		JustBeforeEach(func() {
			req, err := http.NewRequest("DELETE", server.URL+"/api/v1/workers/"+workerName, nil)
			Expect(err).NotTo(HaveOccurred())

			response, err = client.Do(req)
			Expect(err).NotTo(HaveOccurred())
		})

		BeforeEach(func() {
			fakeWorker = new(dbfakes.FakeWorker)
			workerName = "some-worker"
			fakeWorker.NameReturns(workerName)

			fakeAccess.IsAuthenticatedReturns(true)
			fakeWorker.DeleteReturns(nil)
			dbWorkerFactory.GetWorkerReturns(fakeWorker, true, nil)
		})

		Context("when user is system user", func() {
			BeforeEach(func() {
				fakeAccess.IsSystemReturns(true)
			})
			It("deletes the worker from the DB", func() {
				Expect(dbWorkerFactory.GetWorkerCallCount()).To(Equal(1))
				Expect(dbWorkerFactory.GetWorkerArgsForCall(0)).To(Equal(workerName))

				Expect(fakeWorker.DeleteCallCount()).To(Equal(1))
			})
			It("returns 200", func() {
				Expect(response.StatusCode).To(Equal(http.StatusOK))
			})

			Context("when the given worker has already been deleted", func() {
				BeforeEach(func() {
					dbWorkerFactory.GetWorkerReturns(nil, false, nil)
				})
				It("returns 500", func() {
					Expect(dbWorkerFactory.GetWorkerCallCount()).To(Equal(1))
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				})
			})

			Context("when deleting the worker fails", func() {
				var returnedErr error

				BeforeEach(func() {
					returnedErr = errors.New("some-error")
					fakeWorker.DeleteReturns(returnedErr)
				})

				It("returns 500", func() {
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				})
			})
		})

		Context("when user is admin user", func() {
			BeforeEach(func() {
				fakeAccess.IsAdminReturns(true)
			})
			It("deletes the worker from the DB", func() {
				Expect(dbWorkerFactory.GetWorkerCallCount()).To(Equal(1))
				Expect(dbWorkerFactory.GetWorkerArgsForCall(0)).To(Equal(workerName))

				Expect(fakeWorker.DeleteCallCount()).To(Equal(1))
			})
			It("returns 200", func() {
				Expect(response.StatusCode).To(Equal(http.StatusOK))
			})
		})

		Context("when user is authorized for team", func() {
			BeforeEach(func() {
				fakeWorker.TeamNameReturns("some-team")
				fakeAccess.IsAuthorizedReturns(true)
			})
			It("deletes the worker from the DB", func() {
				Expect(dbWorkerFactory.GetWorkerCallCount()).To(Equal(1))
				Expect(dbWorkerFactory.GetWorkerArgsForCall(0)).To(Equal(workerName))

				Expect(fakeWorker.DeleteCallCount()).To(Equal(1))
			})
			It("returns 200", func() {
				Expect(response.StatusCode).To(Equal(http.StatusOK))
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

			It("returns 401", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})

			It("does not attempt to find the worker", func() {
				Expect(dbWorkerFactory.GetWorkerCallCount()).To(BeZero())
			})
		})
	})
})
