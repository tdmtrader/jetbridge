package auth_test

import (
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/concourse/concourse/agent/api/principals"
	"github.com/concourse/concourse/agent/api/principals/principalstest"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/accessor/accessorfakes"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/auth/authfakes"
	"github.com/concourse/concourse/atc/auditor/auditorfakes"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CheckAgentPrincipalHandler", func() {
	var (
		fakeRejector *authfakes.FakeRejector
		fakeAccessor *accessorfakes.FakeAccessFactory
		fakeaccess   *accessorfakes.FakeAccess
		store        *principalstest.MemoryStore
		verifier     *principals.Verifier

		token      string
		seenName   string
		seenHasCtx bool
		server     *httptest.Server
		client     *http.Client
	)

	echoHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := principals.FromContext(r.Context())
		seenHasCtx = ok
		seenName = p.Name
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "delegate")
	})

	BeforeEach(func() {
		fakeRejector = new(authfakes.FakeRejector)
		fakeAccessor = new(accessorfakes.FakeAccessFactory)
		fakeaccess = new(accessorfakes.FakeAccess)
		seenName = ""
		seenHasCtx = false

		fakeRejector.UnauthorizedStub = func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "nope", http.StatusUnauthorized)
		}
		fakeRejector.ForbiddenStub = func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "still nope", http.StatusForbidden)
		}

		store = principalstest.NewMemoryStore()
		verifier = principals.NewVerifier(store)

		var err error
		_, token, err = store.Create(principals.CreateSpec{
			Name: "itest-reviewer", Scopes: []string{principals.ScopeTicketsWrite},
		})
		Expect(err).NotTo(HaveOccurred())
	})

	JustBeforeEach(func() {
		fakeAccessor.CreateReturns(fakeaccess, nil)

		factory := auth.NewCheckAgentPrincipalHandlerFactory(verifier)
		inner := factory.HandlerFor(echoHandler, fakeRejector, principals.ScopeTicketsWrite)

		server = httptest.NewServer(accessor.NewHandler(
			logger,
			"some-action",
			inner,
			fakeAccessor,
			new(auditorfakes.FakeAuditor),
			map[string]string{},
		))
		client = &http.Client{Transport: &http.Transport{}}
	})

	AfterEach(func() {
		server.Close()
	})

	get := func(authorization string) *http.Response {
		req, err := http.NewRequest("GET", server.URL, nil)
		Expect(err).NotTo(HaveOccurred())
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		resp, err := client.Do(req)
		Expect(err).NotTo(HaveOccurred())
		return resp
	}

	It("admits a valid principal token and places it in the context", func() {
		resp := get("Bearer " + token)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(seenHasCtx).To(BeTrue())
		Expect(seenName).To(Equal("itest-reviewer"))
	})

	It("rejects a tampered principal token with 401", func() {
		resp := get("Bearer " + token + "x")
		Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
	})

	It("rejects a valid token lacking the scope with 401", func() {
		_, wrongScope, err := store.Create(principals.CreateSpec{
			Name: "ticketer", Scopes: []string{principals.ScopeTicketsRead},
		})
		Expect(err).NotTo(HaveOccurred())
		resp := get("Bearer " + wrongScope)
		Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
	})

	Context("without a principal token", func() {
		It("admits an admin user token without principal context", func() {
			fakeaccess.IsAuthenticatedReturns(true)
			fakeaccess.IsAdminReturns(true)
			resp := get("")
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			Expect(seenHasCtx).To(BeFalse())
		})

		It("401s unauthenticated requests", func() {
			fakeaccess.IsAuthenticatedReturns(false)
			resp := get("")
			Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
		})

		It("401s a non-principal bearer token", func() {
			fakeaccess.IsAuthenticatedReturns(false)
			resp := get("Bearer some-static-publish-token")
			Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
			Expect(seenHasCtx).To(BeFalse())
		})

		It("403s authenticated non-admins", func() {
			fakeaccess.IsAuthenticatedReturns(true)
			fakeaccess.IsAdminReturns(false)
			resp := get("")
			Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
		})

	})
})
