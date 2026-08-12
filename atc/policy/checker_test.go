package policy_test

import (
	"github.com/concourse/concourse/atc/policy"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Policy checker", func() {
	var (
		checker policy.Checker
		filter  policy.Filter
		err     error
	)

	BeforeEach(func() {
		filter = policy.Filter{
			HttpMethods:   []string{"POST", "PUT"},
			Actions:       []string{"do_1", "do_2"},
			ActionsToSkip: []string{"skip_1", "skip_2"},
		}
	})

	JustBeforeEach(func() {
		checker, err = policy.Initialize(testLogger, "some-cluster", "some-version", filter)
	})

	Context("Initialize", func() {
		It("should return a checker backed by the configured agent", func() {
			Expect(err).ToNot(HaveOccurred())
			Expect(checker).To(BeAssignableToTypeOf(&policy.AgentChecker{}))
		})

		Context("Checker", func() {
			Context("ShouldCheckHttpMethod", func() {
				It("should return correct result", func() {
					Expect(checker.ShouldCheckHttpMethod("GET")).To(BeFalse())
					Expect(checker.ShouldCheckHttpMethod("DELETE")).To(BeFalse())
					Expect(checker.ShouldCheckHttpMethod("PUT")).To(BeTrue())
					Expect(checker.ShouldCheckHttpMethod("POST")).To(BeTrue())
				})
			})

			Context("ShouldCheckAction", func() {
				It("should return correct result", func() {
					Expect(checker.ShouldCheckAction("did_1")).To(BeFalse())
					Expect(checker.ShouldCheckAction("did_2")).To(BeFalse())
					Expect(checker.ShouldCheckAction("do_1")).To(BeTrue())
					Expect(checker.ShouldCheckAction("do_2")).To(BeTrue())
				})
			})

			Context("ShouldSkipAction", func() {
				It("should return correct result", func() {
					Expect(checker.ShouldSkipAction("did_1")).To(BeFalse())
					Expect(checker.ShouldSkipAction("did_2")).To(BeFalse())
					Expect(checker.ShouldSkipAction("skip_1")).To(BeTrue())
					Expect(checker.ShouldSkipAction("skip_2")).To(BeTrue())
				})
			})

			Context("Check", func() {
				var (
					input    policy.PolicyCheckInput
					output   policy.PolicyCheckResult
					checkErr error
				)

				BeforeEach(func() {
					input = policy.PolicyCheckInput{}
					opaServer.Reset(`{"result": {"allowed": false, "block": true, "reasons": ["a policy says you can't do that"]}}`)
				})

				JustBeforeEach(func() {
					output, checkErr = checker.Check(input)
				})

				It("agent should be called", func() {
					Expect(opaServer.Requests()).To(HaveLen(1))
				})

				It("cluster name should be injected into input", func() {
					Expect(opaServer.Requests()[0]).To(MatchJSON(`{
						"input": {
							"service": "concourse",
							"cluster_name": "some-cluster",
							"cluster_version": "some-version",
							"action": ""
						}
					}`))
				})

				It("return the same result the agent returns", func() {
					Expect(checkErr).ToNot(HaveOccurred())
					Expect(output.Allowed()).To(BeFalse())
					Expect(output.ShouldBlock()).To(BeTrue())
					Expect(output.Messages()).To(ConsistOf("a policy says you can't do that"))
				})
			})
		})
	})
})
