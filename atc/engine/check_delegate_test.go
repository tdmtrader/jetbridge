package engine_test

import (
	"context"
	"database/sql"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"code.cloudfoundry.org/clock/fakeclock"
	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/engine"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/policy"
	"github.com/concourse/concourse/vars"
)

func independentEngineLockFactory() lock.LockFactory {
	GinkgoHelper()

	var lockConns [lock.FactoryCount]*sql.DB
	for i := range lock.FactoryCount {
		conn := enginePostgresRunner.OpenSingleton()
		lockConns[i] = conn
		DeferCleanup(func() { Expect(conn.Close()).To(Succeed()) })
	}

	return lock.NewLockFactory(
		lockConns,
		func(lager.Logger, lock.LockID) {},
		func(lager.Logger, lock.LockID) {},
	)
}

func setEngineScopeLastCheck(
	fixture *engineDBFixture,
	scope db.ResourceConfigScope,
	start time.Time,
	end time.Time,
	succeeded bool,
) {
	GinkgoHelper()

	_, err := fixture.Conn.Exec(`
		UPDATE resource_config_scopes
		SET last_check_start_time = $1,
		    last_check_end_time = $2,
		    last_check_succeeded = $3
		WHERE id = $4
	`, start, end, succeeded, scope.ID())
	Expect(err).NotTo(HaveOccurred())
}

var _ = Describe("CheckDelegate", func() {
	var (
		now       time.Time
		fakeClock *fakeclock.FakeClock
		state     exec.RunState

		fixture       *engineDBFixture
		team          db.Team
		pipeline      db.Pipeline
		jobBuild      db.Build
		checkBuild    db.Build
		resource      db.Resource
		resourceType  db.ResourceType
		prototype     db.Prototype
		config        db.ResourceConfig
		globalScope   db.ResourceConfigScope
		resourceScope db.ResourceConfigScope
	)

	newDelegate := func(build db.Build, check atc.CheckPlan) exec.CheckDelegate {
		return engine.NewCheckDelegate(
			build,
			atc.Plan{ID: "some-plan-id", Check: &check},
			state,
			fakeClock,
			newCheckRateLimiter(),
			policy.NoopChecker{},
		)
	}

	BeforeEach(func() {
		now = time.Date(1991, 6, 3, 5, 30, 0, 0, time.UTC)
		fakeClock = fakeclock.NewFakeClock(now)
		state = exec.NewRunState(noopStepper, vars.StaticVariables{
			"source-param": "super-secret-source",
			"git-key":      "{\n123\n456\n789\n}\n",
		})

		fixture = useEngineDB()
		var job db.Job
		team, pipeline, job, jobBuild = createEngineJobBuild(
			fixture,
			"check-delegate-team",
			atc.PipelineRef{Name: "some-pipeline"},
			atc.Config{
				Jobs: atc.JobConfigs{{Name: "some-job"}},
				Resources: atc.ResourceConfigs{{
					Name: "some-resource", Type: dbtest.BaseResourceType,
					Source: atc.Source{"some": "source"},
				}},
				ResourceTypes: atc.ResourceTypes{{
					Name: "some-resource-type", Type: dbtest.BaseResourceType,
					Source: atc.Source{"some": "type-source"},
				}},
				Prototypes: atc.Prototypes{{
					Name: "some-prototype", Type: dbtest.BaseResourceType,
					Source: atc.Source{"some": "prototype-source"},
				}},
			},
			"some-user",
		)
		Expect(job.Name()).To(Equal("some-job"))

		var found bool
		var err error
		resource, found, err = pipeline.Resource("some-resource")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		resourceType, found, err = pipeline.ResourceType("some-resource-type")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		prototype, found, err = pipeline.Prototype("some-prototype")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())

		checkPlan := atc.Plan{ID: "resource-check", Check: &atc.CheckPlan{
			Name: resource.Name(), Type: resource.Type(), Source: resource.Source(), Resource: resource.Name(),
		}}
		checkBuild, found, err = resource.CreateBuild(context.Background(), false, checkPlan)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())

		config, err = fixture.ResourceConfigFactory.FindOrCreateResourceConfig(
			resource.Type(), resource.Source(), nil,
		)
		Expect(err).NotTo(HaveOccurred())
		globalScope, err = config.FindOrCreateScope(nil)
		Expect(err).NotTo(HaveOccurred())
		resourceID := resource.ID()
		resourceScope, err = config.FindOrCreateScope(&resourceID)
		Expect(err).NotTo(HaveOccurred())
	})

	Describe("persisted PostgreSQL state", func() {
		It("finds the real global and named-resource scopes", func() {
			global, err := newDelegate(jobBuild, atc.CheckPlan{ResourceType: "some-resource-type"}).FindOrCreateScope(config)
			Expect(err).NotTo(HaveOccurred())
			Expect(global.ID()).To(Equal(globalScope.ID()))
			Expect(global.ResourceID()).To(BeNil())

			named, err := newDelegate(checkBuild, atc.CheckPlan{Resource: "some-resource"}).FindOrCreateScope(config)
			Expect(err).NotTo(HaveOccurred())
			Expect(named.ID()).To(Equal(resourceScope.ID()))
			Expect(named.ResourceID()).ToNot(BeNil())
			Expect(*named.ResourceID()).To(Equal(resource.ID()))
		})

		It("rejects a deleted named resource before creating a scope", func() {
			_, err := newDelegate(jobBuild, atc.CheckPlan{Resource: "missing-resource"}).FindOrCreateScope(config)
			Expect(err).To(MatchError(ContainSubstring("resource 'missing-resource' deleted")))
		})

		It("persists resource, resource-type, and prototype scope attachment", func() {
			Expect(newDelegate(jobBuild, atc.CheckPlan{Resource: "some-resource"}).PointToCheckedConfig(resourceScope)).To(Succeed())
			found, err := resource.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(resource.ResourceConfigScopeID()).To(Equal(resourceScope.ID()))

			Expect(newDelegate(jobBuild, atc.CheckPlan{ResourceType: "some-resource-type"}).PointToCheckedConfig(globalScope)).To(Succeed())
			found, err = resourceType.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(resourceType.ResourceConfigScopeID()).To(Equal(globalScope.ID()))

			Expect(newDelegate(jobBuild, atc.CheckPlan{Prototype: "some-prototype"}).PointToCheckedConfig(globalScope)).To(Succeed())
			found, err = prototype.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(prototype.ResourceConfigScopeID()).To(Equal(globalScope.ID()))
		})

		It("persists last-check start and end evidence", func() {
			delegate := newDelegate(checkBuild, atc.CheckPlan{Resource: "some-resource"})
			found, buildID, err := delegate.UpdateScopeLastCheckStartTime(resourceScope, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(buildID).To(Equal(checkBuild.ID()))

			started, err := resourceScope.LastCheck()
			Expect(err).NotTo(HaveOccurred())
			Expect(started.StartTime).To(BeTemporally("~", time.Now(), 2*time.Second))

			found, err = delegate.UpdateScopeLastCheckEndTime(resourceScope, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			finished, err := resourceScope.LastCheck()
			Expect(err).NotTo(HaveOccurred())
			Expect(finished.EndTime).To(BeTemporally(">=", started.StartTime))
			Expect(finished.Succeeded).To(BeTrue())
		})

		It("observes a scope deleted before its start and end updates", func() {
			_, err := fixture.Conn.Exec("DELETE FROM resource_config_scopes WHERE id = $1", resourceScope.ID())
			Expect(err).NotTo(HaveOccurred())

			delegate := newDelegate(checkBuild, atc.CheckPlan{Resource: "some-resource"})
			found, buildID, err := delegate.UpdateScopeLastCheckStartTime(resourceScope, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
			Expect(buildID).To(Equal(checkBuild.ID()))

			found, err = delegate.UpdateScopeLastCheckEndTime(resourceScope, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
		})

		It("updates nested-check evidence without associating a build", func() {
			delegate := newDelegate(checkBuild, atc.CheckPlan{ResourceType: "some-resource-type"})
			found, buildID, err := delegate.UpdateScopeLastCheckStartTime(globalScope, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(buildID).To(BeZero())

			var persistedBuildID *int
			var persistedPlan []byte
			Expect(fixture.Conn.QueryRow(`
				SELECT last_check_build_id, last_check_build_plan
				FROM resource_config_scopes
				WHERE id = $1
			`, globalScope.ID()).Scan(&persistedBuildID, &persistedPlan)).To(Succeed())
			Expect(persistedBuildID).To(BeNil())
			Expect(persistedPlan).To(BeNil())
		})

		It("treats an empty checked-config target as a no-op", func() {
			oneOff, err := team.CreateOneOffBuild()
			Expect(err).NotTo(HaveOccurred())
			Expect(newDelegate(oneOff, atc.CheckPlan{}).PointToCheckedConfig(globalScope)).To(Succeed())
		})

		It("uses a real one-off build for the genuine missing-pipeline state", func() {
			oneOff, err := team.CreateOneOffBuild()
			Expect(err).NotTo(HaveOccurred())
			err = newDelegate(oneOff, atc.CheckPlan{Resource: "some-resource"}).PointToCheckedConfig(resourceScope)
			Expect(err).To(MatchError(ContainSubstring("pipeline not found")))
		})
	})

	Describe("check timing and locking through real scopes", func() {
		It("runs a due periodic resource check and returns its production lock", func() {
			setEngineScopeLastCheck(fixture, resourceScope, now.Add(-2*time.Hour), now.Add(-2*time.Hour), true)

			acquired, run, err := newDelegate(checkBuild, atc.CheckPlan{
				Resource: "some-resource", Interval: atc.CheckEvery{Interval: time.Hour},
			}).WaitToRun(context.Background(), resourceScope)
			Expect(err).NotTo(HaveOccurred())
			Expect(run).To(BeTrue())
			Expect(acquired).NotTo(BeNil())
			Expect(acquired.Release()).To(Succeed())
		})

		It("waits for the production resource-config lock and proceeds after release", func() {
			setEngineScopeLastCheck(fixture, resourceScope, now.Add(-2*time.Hour), now.Add(-2*time.Hour), true)
			contender := independentEngineLockFactory()
			held, acquired, err := contender.Acquire(
				lagertest.NewTestLogger("held-check-lock"),
				lock.NewResourceConfigCheckingLockID(config.ID()),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(acquired).To(BeTrue())

			type outcome struct {
				lock lock.Lock
				run  bool
				err  error
			}
			result := make(chan outcome, 1)
			go func() {
				gotLock, run, err := newDelegate(checkBuild, atc.CheckPlan{
					Resource: "some-resource", Interval: atc.CheckEvery{Interval: time.Hour},
				}).WaitToRun(context.Background(), resourceScope)
				result <- outcome{lock: gotLock, run: run, err: err}
			}()

			Consistently(result, 100*time.Millisecond).ShouldNot(Receive())
			Expect(held.Release()).To(Succeed())
			fakeClock.WaitForWatcherAndIncrement(time.Second)

			var completed outcome
			Eventually(result).Should(Receive(&completed))
			Expect(completed.err).NotTo(HaveOccurred())
			Expect(completed.run).To(BeTrue())
			Expect(completed.lock).NotTo(BeNil())
			Expect(completed.lock.Release()).To(Succeed())
		})

		It("skips a nonmanual check_every never resource check", func() {
			before, err := resourceScope.LastCheck()
			Expect(err).NotTo(HaveOccurred())

			acquired, run, err := newDelegate(checkBuild, atc.CheckPlan{
				Resource: "some-resource", Interval: atc.CheckEvery{Never: true},
			}).WaitToRun(context.Background(), resourceScope)
			Expect(err).NotTo(HaveOccurred())
			Expect(run).To(BeFalse())
			Expect(acquired).To(BeNil())
			Expect(resourceScope.LastCheck()).To(Equal(before))
		})

		It("skips a periodic resource check whose interval has not elapsed", func() {
			setEngineScopeLastCheck(fixture, resourceScope, now, now, true)

			acquired, run, err := newDelegate(checkBuild, atc.CheckPlan{
				Resource: "some-resource", Interval: atc.CheckEvery{Interval: time.Hour},
			}).WaitToRun(context.Background(), resourceScope)
			Expect(err).NotTo(HaveOccurred())
			Expect(run).To(BeFalse())
			Expect(acquired).To(BeNil())
		})

		It("forces a manual resource check when a from-version is configured", func() {
			setEngineScopeLastCheck(fixture, resourceScope, time.Now().Add(time.Hour), time.Now().Add(time.Hour), true)

			acquired, run, err := newDelegate(checkBuild, atc.CheckPlan{
				Resource: "some-resource", SkipInterval: true,
				FromVersion: atc.Version{"some": "version"},
			}).WaitToRun(context.Background(), resourceScope)
			Expect(err).NotTo(HaveOccurred())
			Expect(run).To(BeTrue())
			Expect(acquired).NotTo(BeNil())
			Expect(acquired.Release()).To(Succeed())
		})

		It("reuses a newer successful manual resource check", func() {
			setEngineScopeLastCheck(fixture, resourceScope, time.Now().Add(time.Hour), time.Now().Add(time.Hour), true)

			acquired, run, err := newDelegate(checkBuild, atc.CheckPlan{
				Resource: "some-resource", SkipInterval: true,
			}).WaitToRun(context.Background(), resourceScope)
			Expect(err).NotTo(HaveOccurred())
			Expect(run).To(BeFalse())
			Expect(acquired).To(BeNil())
		})

		DescribeTable("evaluates non-resource checks from persisted last-check state",
			func(check atc.CheckPlan, endOffset time.Duration, succeeded bool, expectedRun bool) {
				end := now.Add(endOffset)
				setEngineScopeLastCheck(fixture, globalScope, end, end, succeeded)
				acquired, run, err := newDelegate(checkBuild, check).WaitToRun(context.Background(), globalScope)
				Expect(err).NotTo(HaveOccurred())
				Expect(run).To(Equal(expectedRun))
				if expectedRun {
					Expect(acquired).To(Equal(lock.NoopLock{}))
				} else {
					Expect(acquired).To(BeNil())
				}
			},
			Entry("runs after a prior failure",
				atc.CheckPlan{ResourceType: "some-resource-type"}, time.Duration(0), false, true,
			),
			Entry("runs after the configured interval elapsed",
				atc.CheckPlan{ResourceType: "some-resource-type", Interval: atc.CheckEvery{Interval: time.Hour}},
				-2*time.Hour, true, true,
			),
			Entry("skips while the configured interval is unelapsed",
				atc.CheckPlan{ResourceType: "some-resource-type", Interval: atc.CheckEvery{Interval: time.Hour}},
				time.Duration(0), true, false,
			),
		)
	})
})
