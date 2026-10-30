package engine_test

import (
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
	"github.com/concourse/concourse/vars"
)

var _ = Describe("SetPipelineStepDelegate", func() {
	var (
		logger    *lagertest.TestLogger
		fakeClock *fakeclock.FakeClock

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
			delegate = engine.NewSetPipelineStepDelegate(realBuild, "some-plan-id", state, fakeClock)
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

})
