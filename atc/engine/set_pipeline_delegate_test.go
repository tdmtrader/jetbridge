package engine_test

import (
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"code.cloudfoundry.org/clock/fakeclock"
	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/engine"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/policy"
	"github.com/concourse/concourse/vars"
)

var _ = Describe("SetPipelineStepDelegate", func() {
	var (
		logger        *lagertest.TestLogger
		fakeClock     *fakeclock.FakeClock
		policyChecker policy.Checker

		state exec.RunState

		now      = time.Date(1991, 6, 3, 5, 30, 0, 0, time.UTC)
		delegate exec.SetPipelineStepDelegate
	)

	BeforeEach(func() {
		logger = lagertest.NewTestLogger("test")

		fakeClock = fakeclock.NewFakeClock(now)
		credVars := vars.StaticVariables{
			"source-param": "super-secret-source",
			"git-key":      "{\n123\n456\n789\n}\n",
		}
		state = exec.NewRunState(noopStepper, credVars)

		policyChecker = policy.NoopChecker{}
	})

	Describe("persisted PostgreSQL state", func() {
		var realBuild db.Build

		BeforeEach(func() {
			fixture := useEngineDB()
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
			delegate = engine.NewSetPipelineStepDelegate(realBuild, "some-plan-id", state, fakeClock, policyChecker)
		})

		It("saves changed event", func() {
			delegate.SetPipelineChanged(logger, true)

			found, err := realBuild.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())

			Expect(realBuild.Finish(db.BuildStatusSucceeded)).To(Succeed())
			Expect(consumeEngineBuildEvent(realBuild, 0)).To(Equal(event.SetPipelineChanged{
				Origin:  event.Origin{ID: event.OriginID("some-plan-id")},
				Changed: true,
			}))
		})
	})

	Describe("CheckRunSetPipelinePolicy", func() {
		var (
			checkErr       error
			fixture        *engineDBFixture
			pipelineConfig atc.Config
			realBuild      db.Build
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
		})

		JustBeforeEach(func() {
			delegate = engine.NewSetPipelineStepDelegate(realBuild, "some-plan-id", state, fakeClock, policyChecker)

			pipelineConfig = atc.Config{
				Groups: atc.GroupConfigs{
					{
						Name: "g1",
					},
					{
						Name: "g2",
					},
				},
			}

			checkErr = delegate.CheckRunSetPipelinePolicy(&pipelineConfig)
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
				policyChecker = newPolicyChecker(policy.ActionRunSetPipeline)
			})

			It("should check policy", func() {
				Expect(opaServer.Requests()).To(HaveLen(1))

				request := opaServer.Requests()[0]
				Expect(request.PolicyCheckInput).To(Equal(policy.PolicyCheckInput{
					Service:        "concourse",
					ClusterName:    "some-cluster",
					ClusterVersion: "some-version",
					Action:         policy.ActionRunSetPipeline,
					Team:           "some-team",
					Pipeline:       "some-pipeline",
				}))

				var checked atc.Config
				Expect(json.Unmarshal(request.Data, &checked)).To(Succeed())
				Expect(checked).To(Equal(pipelineConfig))
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
