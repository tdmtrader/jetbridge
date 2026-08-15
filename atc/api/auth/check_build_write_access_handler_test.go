package auth_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CheckBuildWriteAccessHandler", func() {
	var (
		response    *http.Response
		server      *httptest.Server
		handler     http.Handler
		team        db.Team
		build       db.Build
		requestedID int

		// set by a Context to give the request a token; "" leaves it anonymous
		authorization string
	)

	BeforeEach(func() {
		authorization = ""

		// A real build of a real job, owned by "some-team" -- which is the team
		// the request claims, so the authorization decision is made against rows
		// rather than a configured team-name response.
		team = createTeam("some-team")
		build = createJobBuild(team, "some-pipeline", "some-job")
		requestedID = build.ID()
	})

	// JustBeforeEach so a Context can point the request at a different id before
	// the handler is built.
	JustBeforeEach(func() {
		innerHandler := auth.NewCheckBuildWriteAccessHandlerFactory(buildFactory).
			HandlerFor(http.HandlerFunc(renderBuildScope), auth.UnauthorizedRejector{})

		// AbortBuild is a write route, so the real accessor demands the operator
		// role rather than the blank one every role fails.
		handler = accessor.NewHandler(
			logger,
			atc.AbortBuild,
			innerHandler,
			realAccessFactory(),
			realAuditor(),
			map[string]string{},
		)

		server = httptest.NewServer(handler)

		request, err := http.NewRequest("POST",
			fmt.Sprintf("%s?:team_name=some-team&:build_id=%d", server.URL, requestedID), nil)
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

	Context("when authenticated and accessing same team's build", func() {
		BeforeEach(func() {
			authorization = validAccessToken()
			grantRole(team, accessor.OperatorRole)
		})

		Context("when build exists", func() {
			It("returns 200 ok", func() {
				Expect(response.StatusCode).To(Equal(http.StatusOK))
			})

			It("scopes the response to the requested build", func() {
				Expect(response.Header.Get("X-Concourse-Scoped-Build")).To(Equal(fmt.Sprint(build.ID())))
			})
		})

		Context("when build is not found", func() {
			BeforeEach(func() {
				requestedID = build.ID() + 1000
			})

			It("returns 404", func() {
				Expect(response.StatusCode).To(Equal(http.StatusNotFound))
			})
		})
	})

	Context("when authenticated but accessing different team's build", func() {
		BeforeEach(func() {
			authorization = validAccessToken()
			grantRole(createTeam("some-other-team"), accessor.OperatorRole)
		})

		It("returns 403", func() {
			Expect(response.StatusCode).To(Equal(http.StatusForbidden))
		})
	})

	Context("when authenticated with too weak a role for the team", func() {
		BeforeEach(func() {
			authorization = validAccessToken()
			grantRole(team, accessor.ViewerRole)
		})

		It("returns 403", func() {
			Expect(response.StatusCode).To(Equal(http.StatusForbidden))
		})
	})

	Context("when not authenticated", func() {
		BeforeEach(func() {
			grantRole(team, accessor.OperatorRole)
		})

		It("returns 401", func() {
			Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
		})
	})
})
