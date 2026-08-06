package auth_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/accessor/accessorfakes"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/auditor/auditorfakes"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CheckBuildWriteAccessHandler", func() {
	var (
		response     *http.Response
		server       *httptest.Server
		delegate     *buildDelegateHandler
		factory      db.BuildFactory
		handler      http.Handler
		fakeAccessor *accessorfakes.FakeAccessFactory
		fakeaccess   *accessorfakes.FakeAccess
		build        db.Build
		requestedID  int
	)

	BeforeEach(func() {
		factory = buildFactory
		fakeAccessor = new(accessorfakes.FakeAccessFactory)
		fakeaccess = new(accessorfakes.FakeAccess)

		delegate = &buildDelegateHandler{}

		// A real build of a real job, owned by "some-team" -- which is the team
		// the request claims, so the authorization decision is made against rows
		// rather than against TeamNameReturns.
		build = createJobBuild(createTeam("some-team"), "some-pipeline", "some-job")
		requestedID = build.ID()
	})

	// JustBeforeEach so a Context can point the request at a different id, or
	// swap the factory for a doomed one, before the handler is built.
	JustBeforeEach(func() {
		innerHandler := auth.NewCheckBuildWriteAccessHandlerFactory(factory).
			HandlerFor(delegate, auth.UnauthorizedRejector{})

		handler = accessor.NewHandler(
			logger,
			"some-action",
			innerHandler,
			fakeAccessor,
			new(auditorfakes.FakeAuditor),
			map[string]string{},
		)

		fakeAccessor.CreateReturns(fakeaccess, nil)
		server = httptest.NewServer(handler)

		request, err := http.NewRequest("POST",
			fmt.Sprintf("%s?:team_name=some-team&:build_id=%d", server.URL, requestedID), nil)
		Expect(err).NotTo(HaveOccurred())

		response, err = new(http.Client).Do(request)
		Expect(err).NotTo(HaveOccurred())
	})

	var _ = AfterEach(func() {
		server.Close()
	})

	Context("when authenticated and accessing same team's build", func() {
		BeforeEach(func() {
			fakeaccess.IsAuthenticatedReturns(true)
			fakeaccess.IsAuthorizedReturns(true)
		})

		Context("when build exists", func() {
			It("returns 200 ok", func() {
				Expect(response.StatusCode).To(Equal(http.StatusOK))
			})

			It("calls delegate with the build context", func() {
				Expect(delegate.IsCalled).To(BeTrue())
				Expect(delegate.ContextBuild.ID()).To(Equal(build.ID()))
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

		Context("when getting build fails", func() {
			BeforeEach(func() {
				factory = doomedBuildFactory()
			})

			It("returns 500", func() {
				Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
			})
		})
	})

	Context("when authenticated but accessing different team's build", func() {
		BeforeEach(func() {
			fakeaccess.IsAuthenticatedReturns(true)
			fakeaccess.IsAuthorizedReturns(false)
		})

		It("returns 403", func() {
			Expect(response.StatusCode).To(Equal(http.StatusForbidden))
		})
	})

	Context("when not authenticated", func() {
		BeforeEach(func() {
			fakeaccess.IsAuthenticatedReturns(false)
		})

		It("returns 401", func() {
			Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
		})
	})
})
