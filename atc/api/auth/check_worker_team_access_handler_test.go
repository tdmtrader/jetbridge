package auth_test

import (
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/db"
	"github.com/tedsuo/rata"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CheckWorkerTeamAccessHandler", func() {
	var (
		response *http.Response
		server   *httptest.Server
		delegate *workerDelegateHandler
		factory  db.WorkerFactory
		handler  http.Handler

		// set by a Context to give the request a token; "" leaves it anonymous
		authorization string
	)

	BeforeEach(func() {
		factory = workerFactory
		authorization = ""
		delegate = &workerDelegateHandler{}
	})

	// JustBeforeEach so a Context can register the worker, or swap the factory
	// for a doomed one, before the handler is built.
	JustBeforeEach(func() {
		innerHandler := auth.NewCheckWorkerTeamAccessHandlerFactory(factory).
			HandlerFor(delegate, auth.UnauthorizedRejector{})

		// DeleteWorker is the route this handler guards, and it is the route
		// whose default role a real accessor resolves the request against.
		handler = accessor.NewHandler(
			logger,
			atc.DeleteWorker,
			innerHandler,
			realAccessFactory(),
			realAuditor(),
			map[string]string{},
		)

		routes := rata.Routes{}
		for _, route := range atc.Routes {
			if route.Name == atc.DeleteWorker {
				routes = append(routes, route)
			}
		}

		router, err := rata.NewRouter(routes, map[string]http.Handler{
			atc.DeleteWorker: handler,
		})
		Expect(err).NotTo(HaveOccurred())
		server = httptest.NewServer(router)

		requestGenerator := rata.NewRequestGenerator(server.URL, atc.Routes)
		request, err := requestGenerator.CreateRequest(atc.DeleteWorker, rata.Params{
			"worker_name": "some-worker",
			"team_name":   "some-team",
		}, nil)
		Expect(err).NotTo(HaveOccurred())

		if authorization != "" {
			request.Header.Set("Authorization", authorization)
		}

		response, err = new(http.Client).Do(request)
		Expect(err).NotTo(HaveOccurred())
	})

	var _ = AfterEach(func() {
		server.Close()
	})

	Context("when not authenticated", func() {
		It("returns 401", func() {
			Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
		})

		It("does not call the scoped handler", func() {
			Expect(delegate.IsCalled).To(BeFalse())
		})
	})

	Context("when authenticated", func() {
		BeforeEach(func() {
			authorization = validAccessToken()
		})

		Context("when worker exists and belongs to a team", func() {
			var team db.Team

			BeforeEach(func() {
				// A worker saved through a team is owned by it; that ownership is
				// the column the handler authorizes against.
				team = createTeam("some-team")
				_, err := team.SaveWorker(
					atc.Worker{Name: "some-worker", Platform: "linux"}, 5*time.Minute,
				)
				Expect(err).NotTo(HaveOccurred())
			})

			Context("when user is admin/system", func() {
				BeforeEach(func() {
					makeAdmin(team)
				})

				It("calls worker delegate", func() {
					Expect(delegate.IsCalled).To(BeTrue())
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})
				It("returns 200", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})
			})

			Context("when the token carries the system claim", func() {
				BeforeEach(func() {
					authorization = systemAccessToken()
				})

				It("calls worker delegate", func() {
					Expect(delegate.IsCalled).To(BeTrue())
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})
			})

			Context("when team in auth matches worker team", func() {
				BeforeEach(func() {
					grantRole(team, accessor.MemberRole)
				})

				It("calls worker delegate", func() {
					Expect(delegate.IsCalled).To(BeTrue())
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})
			})

			Context("when team in auth does not match worker team", func() {
				BeforeEach(func() {
					grantRole(createTeam("some-other-team"), accessor.MemberRole)
				})

				It("does not call worker delegate", func() {
					Expect(delegate.IsCalled).To(BeFalse())
				})

				It("returns 403 Forbidden", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				})
			})
		})

		Context("when worker is not owned by a team", func() {
			BeforeEach(func() {
				// Saved through the factory rather than a team: a global worker,
				// belonging to no one.
				_, err := workerFactory.SaveWorker(
					atc.Worker{Name: "some-worker", Platform: "linux"}, 5*time.Minute,
				)
				Expect(err).NotTo(HaveOccurred())
			})

			Context("when user is admin/system", func() {
				BeforeEach(func() {
					makeAdmin(createTeam("some-team"))
				})

				It("calls worker delegate", func() {
					Expect(delegate.IsCalled).To(BeTrue())
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})
				It("returns 200", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})
			})

			Context("when user is not admin/system", func() {
				BeforeEach(func() {
					grantRole(createTeam("some-team"), accessor.MemberRole)
				})

				It("does not call worker delegate", func() {
					Expect(delegate.IsCalled).To(BeFalse())
				})

				It("returns 403 Forbidden", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				})
			})
		})

		Context("when worker does not exist", func() {
			// No worker is registered, so the lookup misses.

			It("does not call worker delegate", func() {
				Expect(delegate.IsCalled).To(BeFalse())
			})

			It("returns 404 Not found", func() {
				Expect(response.StatusCode).To(Equal(http.StatusNotFound))
			})
		})

		Context("when getting worker fails", func() {
			BeforeEach(func() {
				factory = doomedWorkerFactory()
			})

			It("returns 500", func() {
				Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
			})

			It("does not call the scoped handler", func() {
				Expect(delegate.IsCalled).To(BeFalse())
			})
		})
	})
})

type workerDelegateHandler struct {
	IsCalled bool
}

func (handler *workerDelegateHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	handler.IsCalled = true
}
