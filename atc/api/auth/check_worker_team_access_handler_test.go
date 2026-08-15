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
		handler  http.Handler

		// set by a Context to give the request a token; "" leaves it anonymous
		authorization string
	)

	BeforeEach(func() {
		authorization = ""
	})

	JustBeforeEach(func() {
		innerHandler := auth.NewCheckWorkerTeamAccessHandlerFactory(workerFactory).
			HandlerFor(http.HandlerFunc(renderWorkerScope), auth.UnauthorizedRejector{})

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
		}, nil)
		Expect(err).NotTo(HaveOccurred())

		if authorization != "" {
			request.Header.Set("Authorization", authorization)
		}

		response, err = new(http.Client).Do(request)
		Expect(err).NotTo(HaveOccurred())
	})

	var _ = AfterEach(func() {
		Expect(response.Body.Close()).To(Succeed())
		server.Close()
	})

	Context("when not authenticated", func() {
		It("returns 401", func() {
			Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
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

				It("returns the requested worker scope", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
					Expect(response.Header.Get("X-Concourse-Scoped-Worker")).To(Equal("some-worker"))
				})
			})

			Context("when the token carries the system claim", func() {
				BeforeEach(func() {
					authorization = systemAccessToken()
				})

				It("returns the requested worker scope", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
					Expect(response.Header.Get("X-Concourse-Scoped-Worker")).To(Equal("some-worker"))
				})
			})

			Context("when team in auth matches worker team", func() {
				BeforeEach(func() {
					grantRole(team, accessor.MemberRole)
				})

				It("returns the requested worker scope", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
					Expect(response.Header.Get("X-Concourse-Scoped-Worker")).To(Equal("some-worker"))
				})
			})

			Context("when team in auth does not match worker team", func() {
				BeforeEach(func() {
					grantRole(createTeam("some-other-team"), accessor.MemberRole)
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

				It("returns the requested worker scope", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
					Expect(response.Header.Get("X-Concourse-Scoped-Worker")).To(Equal("some-worker"))
				})
			})

			Context("when user is not admin/system", func() {
				BeforeEach(func() {
					grantRole(createTeam("some-team"), accessor.MemberRole)
				})

				It("returns 403 Forbidden", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				})
			})
		})

		Context("when worker does not exist", func() {
			// No worker is registered, so the lookup misses.
			It("returns 404 Not found", func() {
				Expect(response.StatusCode).To(Equal(http.StatusNotFound))
			})
		})
	})
})

func renderWorkerScope(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Concourse-Scoped-Worker", r.FormValue(":worker_name"))
}
