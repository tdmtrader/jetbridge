package engine_test

import (
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"code.cloudfoundry.org/clock/fakeclock"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/engine"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/policy"
	"github.com/concourse/concourse/vars"
)

var _ = Describe("RunPipelineStepDelegate", func() {
	var (
		fakeClock     *fakeclock.FakeClock
		policyChecker policy.Checker

		state exec.RunState

		now = time.Date(1991, 6, 3, 5, 30, 0, 0, time.UTC)
	)

	BeforeEach(func() {
		fakeClock = fakeclock.NewFakeClock(now)
		state = exec.NewRunState(noopStepper, vars.StaticVariables{
			"source-ref": "deadbeef",
		})

		policyChecker = policy.NoopChecker{}
	})

	Describe("CheckRunPipelinePolicy", func() {
		var (
			checkErr  error
			fixture   *engineDBFixture
			realBuild db.Build

			delegate exec.RunPipelineStepDelegate

			params atc.RunParams
		)

		BeforeEach(func() {
			fixture = useEngineDB()
			_, _, _, realBuild = createEngineJobBuild(
				fixture,
				"some-team",
				atc.PipelineRef{
					Name:         "some-pipeline",
					InstanceVars: atc.InstanceVars{"branch": "master"},
				},
				atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
				"some-user",
			)

			params = atc.RunParams{"ref": "deadbeef"}
		})

		JustBeforeEach(func() {
			delegate = engine.NewRunPipelineStepDelegate(realBuild, "some-plan-id", state, fakeClock, policyChecker)

			checkErr = delegate.CheckRunPipelinePolicy("some-team", "version-upgrade", params)
		})

		Context("when the action does not need to be checked", func() {
			BeforeEach(func() {
				policyChecker = newPolicyChecker()
			})

			It("should succeed", func() {
				Expect(checkErr).ToNot(HaveOccurred())
			})

			It("should not check policy", func() {
				Expect(opaServer.Requests()).To(BeEmpty())
			})
		})

		Context("when the action needs to be checked", func() {
			BeforeEach(func() {
				policyChecker = newPolicyChecker(policy.ActionRunPipeline)
			})

			// Team and Pipeline are the calling build's -- who is doing this --
			// which is what set_pipeline's check reports. The target team, the
			// template and the params are the data: what is being done.
			It("should check policy", func() {
				Expect(opaServer.Requests()).To(HaveLen(1))

				request := opaServer.Requests()[0]
				Expect(request.PolicyCheckInput).To(Equal(policy.PolicyCheckInput{
					Service:        "concourse",
					ClusterName:    "some-cluster",
					ClusterVersion: "some-version",
					Action:         policy.ActionRunPipeline,
					Team:           "some-team",
					Pipeline:       "some-pipeline",
				}))

				var checked exec.RunPipelinePolicyData
				Expect(json.Unmarshal(request.Data, &checked)).To(Succeed())
				Expect(checked).To(Equal(exec.RunPipelinePolicyData{
					Team:     "some-team",
					Pipeline: "version-upgrade",
					Params:   atc.RunParams{"ref": "deadbeef"},
				}))
			})

			Context("when policy check fails", func() {
				BeforeEach(func() {
					opaServer.Fails()
				})

				It("should fail", func() {
					Expect(checkErr).To(HaveOccurred())
					Expect(checkErr.Error()).To(Equal("policy check: OPA server returned status: 500"))
				})
			})

			Context("when policy check not pass", func() {
				Context("when should block", func() {
					BeforeEach(func() {
						opaServer.Answers(`{"result": {"allowed": false, "block": true, "reasons": ["reasonA", "reasonB"]}}`)
					})

					It("should fail", func() {
						Expect(checkErr).To(HaveOccurred())
						Expect(checkErr.Error()).To(ContainSubstring("policy check failed"))
						Expect(checkErr.Error()).To(ContainSubstring("reasonA"))
						Expect(checkErr.Error()).To(ContainSubstring("reasonB"))
					})
				})

				Context("when should not block", func() {
					BeforeEach(func() {
						opaServer.Answers(`{"result": {"allowed": false, "block": false, "reasons": ["reasonA", "reasonB"]}}`)
					})

					It("should succeed", func() {
						Expect(checkErr).ToNot(HaveOccurred())
					})

					It("should log warning", func() {
						found, err := realBuild.Reload()
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())

						e := consumeEngineBuildEvent(realBuild, 0)
						Expect(e.EventType()).To(Equal(event.EventTypeLog))
						Expect(e.(event.Log).Origin).To(Equal(event.Origin{
							ID:     "some-plan-id",
							Source: event.OriginSourceStderr,
						}))
						Expect(e.(event.Log).Payload).To(ContainSubstring("policy check failed"))
						Expect(e.(event.Log).Payload).To(ContainSubstring("reasonA"))
						Expect(e.(event.Log).Payload).To(ContainSubstring("reasonB"))

						e = consumeEngineBuildEvent(realBuild, 1)
						Expect(e.EventType()).To(Equal(event.EventTypeLog))
						Expect(e.(event.Log).Origin).To(Equal(event.Origin{
							ID:     "some-plan-id",
							Source: event.OriginSourceStderr,
						}))
						Expect(e.(event.Log).Payload).To(ContainSubstring("WARNING: unblocking from the policy check failure for soft enforcement"))
					})
				})
			})

			Context("policy check passes", func() {
				BeforeEach(func() {
					opaServer.Answers(`{"result": {"allowed": true}}`)
				})

				It("should succeed", func() {
					Expect(checkErr).ToNot(HaveOccurred())
				})

				It("should not log warning", func() {
					var count int
					Expect(fixture.Conn.QueryRow(
						"SELECT count(*) FROM build_events WHERE build_id = $1",
						realBuild.ID(),
					).Scan(&count)).To(Succeed())
					Expect(count).To(BeZero())
				})
			})
		})
	})
})
