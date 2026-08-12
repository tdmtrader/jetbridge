package policychecker_test

import (
	"io"
	"net/http"
	"net/http/httptest"

	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/atc/api/policychecker"
	"github.com/concourse/concourse/atc/policy"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Handler", func() {
	var (
		innerHandlerCalled   bool
		dummyHandler         http.HandlerFunc
		policyCheckerHandler http.Handler
		req                  *http.Request
		policyChecker        policychecker.PolicyChecker
		responseWriter       *httptest.ResponseRecorder

		logger = lagertest.NewTestLogger("test")
	)

	BeforeEach(func() {
		policyChecker = policychecker.NewApiPolicyChecker(policy.NoopChecker{})

		innerHandlerCalled = false
		dummyHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			innerHandlerCalled = true
		})

		responseWriter = httptest.NewRecorder()

		var err error
		req, err = http.NewRequest("GET", "localhost:8080", nil)
		Expect(err).NotTo(HaveOccurred())
	})

	JustBeforeEach(func() {
		policyCheckerHandler = policychecker.NewHandler(logger, dummyHandler, "some-action", policyChecker)
		policyCheckerHandler.ServeHTTP(responseWriter, req)
	})

	Context("policy check passes", func() {
		It("calls the inner handler", func() {
			Expect(innerHandlerCalled).To(BeTrue())
		})
	})

	Context("policy check doesn't pass", func() {
		BeforeEach(func() {
			policyChecker = policychecker.NewApiPolicyChecker(initializedChecker(policy.Filter{
				Actions: []string{"some-action"},
			}))
		})

		Context("when should block", func() {
			BeforeEach(func() {
				opaServer.Reset(`{"result": {"allowed": false, "block": true, "reasons": ["a policy says you can't do that", "another policy also says you can't do that"]}}`)
			})

			It("return http forbidden", func() {
				Expect(responseWriter.Code).To(Equal(http.StatusForbidden))

				msg, err := io.ReadAll(responseWriter.Body)
				Expect(err).ToNot(HaveOccurred())
				Expect(string(msg)).To(ContainSubstring("a policy says you can't do that"))
				Expect(string(msg)).To(ContainSubstring("another policy also says you can't do that"))
			})

			It("not call the inner handler", func() {
				Expect(innerHandlerCalled).To(BeFalse())
			})
		})

		Context("when should not block", func() {
			BeforeEach(func() {
				opaServer.Reset(`{"result": {"allowed": false, "block": false, "reasons": ["a policy says you can't do that", "another policy also says you can't do that"]}}`)
			})

			It("calls the inner handler", func() {
				Expect(innerHandlerCalled).To(BeTrue())
			})

			It("response should have a header about policy check warning", func() {
				value := responseWriter.Header().Get("X-Concourse-Policy-Check-Warning")
				Expect(value).To(ContainSubstring("a policy says you can't do that"))
				Expect(value).To(ContainSubstring("another policy also says you can't do that"))
			})
		})
	})

	Context("policy check errors", func() {
		BeforeEach(func() {
			opaServer.RespondWithStatus(http.StatusInternalServerError)
			policyChecker = policychecker.NewApiPolicyChecker(initializedChecker(policy.Filter{
				Actions: []string{"some-action"},
			}))
		})

		It("return http bad request", func() {
			Expect(responseWriter.Code).To(Equal(http.StatusBadRequest))

			msg, err := io.ReadAll(responseWriter.Body)
			Expect(err).ToNot(HaveOccurred())
			Expect(string(msg)).To(Equal("policy-checker: unreachable or misconfigured"))
		})

		It("not call the inner handler", func() {
			Expect(innerHandlerCalled).To(BeFalse())
		})
	})
})
