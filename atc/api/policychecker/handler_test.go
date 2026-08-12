package policychecker_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"

	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/policychecker"
	"github.com/concourse/concourse/atc/policy"
	"github.com/concourse/concourse/atc/policy/policyfakes"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type stubPolicyChecker struct {
	result policy.PolicyCheckResult
	err    error
}

func (c stubPolicyChecker) Check(string, accessor.Access, *http.Request) (policy.PolicyCheckResult, error) {
	return c.result, c.err
}

var _ = Describe("Handler", func() {
	var (
		innerHandlerCalled    bool
		dummyHandler          http.HandlerFunc
		policyCheckerHandler  http.Handler
		req                   *http.Request
		policyChecker         policychecker.PolicyChecker
		fakePolicyCheckResult *policyfakes.FakePolicyCheckResult
		responseWriter        *httptest.ResponseRecorder

		logger = lagertest.NewTestLogger("test")
	)

	BeforeEach(func() {
		policyChecker = policychecker.NewApiPolicyChecker(policy.NoopChecker{})
		fakePolicyCheckResult = new(policyfakes.FakePolicyCheckResult)

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
			fakePolicyCheckResult.AllowedReturns(false)
			fakePolicyCheckResult.MessagesReturns([]string{"a policy says you can't do that", "another policy also says you can't do that"})
			policyChecker = stubPolicyChecker{result: fakePolicyCheckResult}
		})

		Context("when should block", func() {
			BeforeEach(func() {
				fakePolicyCheckResult.ShouldBlockReturns(true)
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
				fakePolicyCheckResult.ShouldBlockReturns(false)
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
			policyChecker = stubPolicyChecker{err: errors.New("some-error")}
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
