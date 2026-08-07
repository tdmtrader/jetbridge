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
	"github.com/concourse/concourse/atc/policy/policyfakes"
	"github.com/concourse/concourse/atc/resource"
	"github.com/concourse/concourse/vars"
)

var _ = Describe("PutDelegate", func() {
	var (
		logger            *lagertest.TestLogger
		fakeClock         *fakeclock.FakeClock
		fakePolicyChecker *policyfakes.FakeChecker
		realBuild         db.Build
		resourceCache     db.ResourceCache

		state exec.RunState

		now = time.Date(1991, 6, 3, 5, 30, 0, 0, time.UTC)

		delegate   exec.PutDelegate
		info       resource.VersionResult
		exitStatus exec.ExitStatus
		plan       atc.PutPlan
		source     atc.Source
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
		plan = atc.PutPlan{
			Name:     "some-name",
			Type:     dbtest.BaseResourceType,
			Resource: "some-resource",
		}
		source = atc.Source{"some": "source"}

		fixture := useEngineDB()
		_, pipeline, _, build := createEngineJobBuild(
			fixture,
			"some-team",
			atc.PipelineRef{
				Name:         "some-pipeline",
				InstanceVars: atc.InstanceVars{"branch": "master"},
			},
			atc.Config{
				Jobs: atc.JobConfigs{{Name: "some-job"}},
				Resources: atc.ResourceConfigs{{
					Name:   "some-resource",
					Type:   dbtest.BaseResourceType,
					Source: source,
				}},
			},
			"some-user",
		)
		realBuild = build
		scenario := &dbtest.Scenario{Pipeline: pipeline}
		scenario.Run(fixture.Builder.WithResourceVersions("some-resource", info.Version))

		var err error
		resourceCache, err = fixture.ResourceCacheFactory.FindOrCreateResourceCache(
			db.ForBuild(realBuild.ID()),
			dbtest.BaseResourceType,
			info.Version,
			source,
			nil,
			nil,
		)
		Expect(err).NotTo(HaveOccurred())

		fakePolicyChecker = new(policyfakes.FakeChecker)

		delegate = engine.NewPutDelegate(realBuild, "some-plan-id", state, fakeClock, fakePolicyChecker)
	})

	Describe("Finished", func() {
		JustBeforeEach(func() {
			delegate.Finished(logger, exitStatus, info)
		})

		It("saves an event", func() {
			found, err := realBuild.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(consumeEngineBuildEvent(realBuild, 0)).To(Equal(event.FinishPut{
				Origin:          event.Origin{ID: event.OriginID("some-plan-id")},
				Time:            now.Unix(),
				ExitStatus:      int(exitStatus),
				CreatedVersion:  info.Version,
				CreatedMetadata: info.Metadata,
			}))
		})
	})

	Describe("SaveOutput", func() {
		JustBeforeEach(func() {
			delegate.SaveOutput(logger, plan, source, resourceCache, info)
		})

		It("saves the build output", func() {
			found, err := realBuild.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			_, outputs, err := realBuild.Resources()
			Expect(err).NotTo(HaveOccurred())
			Expect(outputs).To(Equal([]db.BuildOutput{{
				Name:    "some-name",
				Version: info.Version,
			}}))
		})
	})
})
