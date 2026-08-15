package engine_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"code.cloudfoundry.org/clock/fakeclock"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/engine"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/policy"
	"github.com/concourse/concourse/atc/resource"
	"github.com/concourse/concourse/vars"
)

var _ = Describe("GetDelegate", func() {
	var (
		logger    *lagertest.TestLogger
		fakeClock *fakeclock.FakeClock

		state exec.RunState

		now        = time.Date(1991, 6, 3, 5, 30, 0, 0, time.UTC)
		delegate   exec.GetDelegate
		info       resource.VersionResult
		exitStatus exec.ExitStatus
	)

	BeforeEach(func() {
		logger = lagertest.NewTestLogger("test")

		fakeClock = fakeclock.NewFakeClock(now)
		credVars := vars.StaticVariables{
			"source-param": "super-secret-source",
			"git-key":      "{\n123\n456\n789\n}\n",
		}
		state = exec.NewRunState(noopStepper, credVars)

		info = resource.VersionResult{
			Version:  atc.Version{"foo": "bar"},
			Metadata: atc.Metadata{{Name: "baz", Value: "shmaz"}},
		}
	})

	Describe("persisted PostgreSQL state", func() {
		var (
			fixture   *engineDBFixture
			team      db.Team
			pipeline  db.Pipeline
			realBuild db.Build
			version   db.ResourceConfigVersion
		)

		BeforeEach(func() {
			fixture = useEngineDB()
			var job db.Job
			team, pipeline, job, realBuild = createEngineJobBuild(
				fixture,
				"get-delegate-team",
				atc.PipelineRef{Name: "some-pipeline"},
				atc.Config{
					Jobs: atc.JobConfigs{{Name: "some-job"}},
					Resources: atc.ResourceConfigs{{
						Name:   "some-resource",
						Type:   dbtest.BaseResourceType,
						Source: atc.Source{"some": "source"},
					}},
				},
				"some-user",
			)
			Expect(job.Name()).To(Equal("some-job"))
			scenario := &dbtest.Scenario{Team: team, Pipeline: pipeline}
			scenario.Run(fixture.Builder.WithResourceVersions("some-resource", info.Version))
			version = scenario.ResourceVersion("some-resource", info.Version)
			delegate = engine.NewGetDelegate(realBuild, "some-plan-id", state, fakeClock, policy.NoopChecker{})
		})

		It("saves the finish event", func() {
			delegate.Finished(logger, exitStatus, info)
			found, err := realBuild.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(consumeEngineBuildEvent(realBuild, 0)).To(Equal(event.FinishGet{
				Origin:          event.Origin{ID: event.OriginID("some-plan-id")},
				Time:            now.Unix(),
				ExitStatus:      int(exitStatus),
				FetchedVersion:  info.Version,
				FetchedMetadata: info.Metadata,
			}))
		})

		It("updates resource version metadata", func() {
			delegate.UpdateResourceVersion(logger, "some-resource", info)

			found, err := version.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(version.Metadata()).To(Equal(db.NewResourceConfigMetadataFields(info.Metadata)))
		})

		It("leaves metadata unchanged when the real one-off build has no pipeline", func() {
			oneOff, err := team.CreateOneOffBuild()
			Expect(err).NotTo(HaveOccurred())
			delegate = engine.NewGetDelegate(oneOff, "some-plan-id", state, fakeClock, policy.NoopChecker{})
			delegate.UpdateResourceVersion(logger, "some-resource", info)
			found, err := version.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(version.Metadata()).To(BeEmpty())
		})

		It("leaves metadata unchanged when the named resource is absent", func() {
			delegate.UpdateResourceVersion(logger, "absent-resource", info)
			found, err := version.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(version.Metadata()).To(BeEmpty())
		})
	})
})
