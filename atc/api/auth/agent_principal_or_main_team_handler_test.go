package auth_test

import (
	"net/http"
	"net/http/httptest"

	"github.com/concourse/concourse/atc/api/auth"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentPrincipalOrMainTeamHandler", func() {
	var principalCalled, teamCalled bool
	var handler http.Handler

	BeforeEach(func() {
		principalCalled, teamCalled = false, false
		handler = auth.AgentPrincipalOrMainTeamHandler(
			http.HandlerFunc(func(http.ResponseWriter, *http.Request) { principalCalled = true }),
			http.HandlerFunc(func(http.ResponseWriter, *http.Request) { teamCalled = true }),
		)
	})

	It("routes cap1 bearer tokens to the principal tier", func() {
		req := httptest.NewRequest("GET", "/api/v1/agent/tickets/1", nil)
		req.Header.Set("Authorization", "Bearer cap1.7.s3cret")
		handler.ServeHTTP(httptest.NewRecorder(), req)
		Expect(principalCalled).To(BeTrue())
		Expect(teamCalled).To(BeFalse())
	})

	It("routes user JWTs to the main-team tier", func() {
		req := httptest.NewRequest("GET", "/api/v1/agent/tickets/1", nil)
		req.Header.Set("Authorization", "Bearer eyJhbGciOi.something.jwt")
		handler.ServeHTTP(httptest.NewRecorder(), req)
		Expect(principalCalled).To(BeFalse())
		Expect(teamCalled).To(BeTrue())
	})

	It("routes tokenless requests to the main-team tier", func() {
		req := httptest.NewRequest("GET", "/api/v1/agent/tickets/1", nil)
		handler.ServeHTTP(httptest.NewRecorder(), req)
		Expect(principalCalled).To(BeFalse())
		Expect(teamCalled).To(BeTrue())
	})
})
