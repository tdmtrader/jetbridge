package auth_test

import (
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/accessor/accessorfakes"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/auth/authfakes"
	"github.com/concourse/concourse/atc/auditor/auditorfakes"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CheckAgentAuthorizationHandler", func() {
	var (
		fakeRejector *authfakes.FakeRejector
		fakeAccessor *accessorfakes.FakeAccessFactory
		fakeaccess   *accessorfakes.FakeAccess
		server       *httptest.Server
		client       *http.Client
	)

	simpleHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "agent route")
	})

	BeforeEach(func() {
		fakeRejector = new(authfakes.FakeRejector)
		fakeAccessor = new(accessorfakes.FakeAccessFactory)
		fakeaccess = new(accessorfakes.FakeAccess)

		fakeRejector.UnauthorizedStub = func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "nope", http.StatusUnauthorized)
		}
		fakeRejector.ForbiddenStub = func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "still nope", http.StatusForbidden)
		}

		server = httptest.NewServer(accessor.NewHandler(
			logger,
			"some-action",
			auth.CheckAgentAuthorizationHandler(simpleHandler, fakeRejector),
			fakeAccessor,
			new(auditorfakes.FakeAuditor),
			map[string]string{},
		))
		client = &http.Client{Transport: &http.Transport{}}
	})

	AfterEach(func() {
		server.Close()
	})

	JustBeforeEach(func() {
		fakeAccessor.CreateReturns(fakeaccess, nil)
	})

	get := func() *http.Response {
		// team-less path — no :team_name param anywhere
		resp, err := client.Get(server.URL + "/api/v1/agent/feedback")
		Expect(err).NotTo(HaveOccurred())
		return resp
	}

	It("401s unauthenticated requests", func() {
		fakeaccess.IsAuthenticatedReturns(false)
		Expect(get().StatusCode).To(Equal(http.StatusUnauthorized))
	})

	It("authorizes against the main team, not the empty team", func() {
		fakeaccess.IsAuthenticatedReturns(true)
		fakeaccess.IsAuthorizedReturns(true)

		Expect(get().StatusCode).To(Equal(http.StatusOK))
		Expect(fakeaccess.IsAuthorizedCallCount()).To(Equal(1))
		Expect(fakeaccess.IsAuthorizedArgsForCall(0)).To(Equal("main"))
	})

	It("403s main-team-unauthorized users", func() {
		fakeaccess.IsAuthenticatedReturns(true)
		fakeaccess.IsAuthorizedReturns(false)
		Expect(get().StatusCode).To(Equal(http.StatusForbidden))
	})
})
