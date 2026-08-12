package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"code.cloudfoundry.org/clock/fakeclock"
	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/lock/lockfakes"
	"github.com/concourse/concourse/atc/engine"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/policy/policyfakes"
	"github.com/concourse/concourse/vars"
)

// A healthy resource config cannot fail only FindOrCreateScope while retaining
// its persisted identity and origin fields.
type scopeErrorResourceConfig struct {
	db.ResourceConfig
	err error
}

func (config scopeErrorResourceConfig) FindOrCreateScope(*int) (db.ResourceConfigScope, error) {
	return nil, config.err
}

// checkTimingScope is the narrow timing/fault seam for WaitToRun and update
// error ordering. Persisted scope creation, attachment, and timestamps are
// covered separately below against real PostgreSQL rows.
type checkTimingScope struct {
	db.ResourceConfigScope

	lastCheck       func() (db.LastCheck, error)
	acquire         func(lager.Logger) (lock.Lock, bool, error)
	updateStart     func(int, *json.RawMessage) (bool, error)
	updateEnd       func(bool) (bool, error)
	lastCheckCalls  int
	acquireCalls    int
	updateStartArgs []struct {
		buildID int
		plan    *json.RawMessage
	}
	updateEndArgs []bool
}

func (scope *checkTimingScope) LastCheck() (db.LastCheck, error) {
	scope.lastCheckCalls++
	return scope.lastCheck()
}

func (scope *checkTimingScope) AcquireResourceCheckingLock(logger lager.Logger) (lock.Lock, bool, error) {
	scope.acquireCalls++
	return scope.acquire(logger)
}

func (scope *checkTimingScope) UpdateLastCheckStartTime(buildID int, plan *json.RawMessage) (bool, error) {
	scope.updateStartArgs = append(scope.updateStartArgs, struct {
		buildID int
		plan    *json.RawMessage
	}{buildID: buildID, plan: plan})
	return scope.updateStart(buildID, plan)
}

func (scope *checkTimingScope) UpdateLastCheckEndTime(succeeded bool) (bool, error) {
	scope.updateEndArgs = append(scope.updateEndArgs, succeeded)
	return scope.updateEnd(succeeded)
}

// A rate limiter admits checks without recording that it did, so the count of
// admissions is only observable from the outside.
type countingRateLimiter struct {
	engine.RateLimiter

	waits int
}

func (limiter *countingRateLimiter) Wait(ctx context.Context) error {
	limiter.waits++
	return limiter.RateLimiter.Wait(ctx)
}

var _ = Describe("CheckDelegate", func() {
	var (
		now               time.Time
		fakeClock         *fakeclock.FakeClock
		rateLimiter       *countingRateLimiter
		fakePolicyChecker *policyfakes.FakeChecker
		state             exec.RunState
	)

	BeforeEach(func() {
		now = time.Date(1991, 6, 3, 5, 30, 0, 0, time.UTC)
		fakeClock = fakeclock.NewFakeClock(now)
		rateLimiter = &countingRateLimiter{RateLimiter: newCheckRateLimiter()}
		fakePolicyChecker = new(policyfakes.FakeChecker)
		state = exec.NewRunState(noopStepper, vars.StaticVariables{
			"source-param": "super-secret-source",
			"git-key":      "{\n123\n456\n789\n}\n",
		})
	})

	Describe("persisted PostgreSQL state", func() {
		var (
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
				rateLimiter,
				fakePolicyChecker,
			)
		}

		BeforeEach(func() {
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

		It("bounds a selective scope lookup failure around the healthy config", func() {
			_, err := newDelegate(jobBuild, atc.CheckPlan{}).FindOrCreateScope(
				scopeErrorResourceConfig{ResourceConfig: config, err: errors.New("nope")},
			)
			Expect(err).To(MatchError(ContainSubstring("find or create scope: nope")))
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
			var attachedResourceID int
			Expect(fixture.Conn.QueryRow(`
				SELECT scope.resource_id
				FROM resources resource
				JOIN resource_config_scopes scope
				  ON scope.id = resource.resource_config_scope_id
				WHERE resource.id = $1
			`, resource.ID()).Scan(&attachedResourceID)).To(Succeed())
			Expect(attachedResourceID).To(Equal(resource.ID()))

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

			found, err = pipeline.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			persistedResource, found, err := pipeline.Resource("some-resource")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(persistedResource.ResourceConfigScopeID()).To(Equal(resourceScope.ID()))
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

	Describe("retained timing and fault matrix", func() {
		var fakeBuild *dbfakes.FakeBuild

		newDelegate := func(check atc.CheckPlan) exec.CheckDelegate {
			return engine.NewCheckDelegate(
				fakeBuild,
				atc.Plan{ID: "some-plan-id", Check: &check},
				state,
				fakeClock,
				rateLimiter,
				fakePolicyChecker,
			)
		}

		BeforeEach(func() {
			// Retained: build timestamps, selective pipeline errors, and callback
			// ordering cannot be represented as ordinary persisted success state.
			fakeBuild = new(dbfakes.FakeBuild)
		})

		It("rate limits before locking a due resource check", func() {
			last := db.LastCheck{StartTime: now.Add(-2 * time.Hour), EndTime: now.Add(-2 * time.Hour), Succeeded: true}
			fakeLock := new(lockfakes.FakeLock)
			scope := &checkTimingScope{
				lastCheck: func() (db.LastCheck, error) { return last, nil },
				acquire: func(lager.Logger) (lock.Lock, bool, error) {
					Expect(rateLimiter.waits).To(Equal(1))
					return fakeLock, true, nil
				},
			}
			gotLock, run, err := newDelegate(atc.CheckPlan{
				Resource: "some-resource", Interval: atc.CheckEvery{Interval: time.Hour},
			}).WaitToRun(context.Background(), scope)
			Expect(err).NotTo(HaveOccurred())
			Expect(run).To(BeTrue())
			Expect(gotLock).To(Equal(fakeLock))
			Expect(rateLimiter.waits).To(Equal(1))
			Expect(scope.acquireCalls).To(Equal(1))
			Expect(scope.lastCheckCalls).To(Equal(2))
		})

		It("forces a manual resource check when a from-version is configured", func() {
			fakeBuild.CreateTimeReturns(now.Add(-time.Minute))
			fakeLock := new(lockfakes.FakeLock)
			scope := &checkTimingScope{
				lastCheck: func() (db.LastCheck, error) {
					return db.LastCheck{StartTime: now, Succeeded: true}, nil
				},
				acquire: func(lager.Logger) (lock.Lock, bool, error) { return fakeLock, true, nil },
			}

			gotLock, run, err := newDelegate(atc.CheckPlan{
				Resource:     "some-resource",
				SkipInterval: true,
				FromVersion:  atc.Version{"some": "version"},
			}).WaitToRun(context.Background(), scope)
			Expect(err).NotTo(HaveOccurred())
			Expect(run).To(BeTrue())
			Expect(gotLock).To(Equal(fakeLock))
			Expect(rateLimiter.waits).To(BeZero())
			Expect(scope.acquireCalls).To(Equal(1))
			Expect(scope.lastCheckCalls).To(Equal(2))
		})

		It("lets SkipInterval override Interval.Never for a resource check", func() {
			fakeLock := new(lockfakes.FakeLock)
			scope := &checkTimingScope{
				lastCheck: func() (db.LastCheck, error) { return db.LastCheck{}, nil },
				acquire:   func(lager.Logger) (lock.Lock, bool, error) { return fakeLock, true, nil },
			}

			gotLock, run, err := newDelegate(atc.CheckPlan{
				Resource:     "some-resource",
				SkipInterval: true,
				Interval:     atc.CheckEvery{Never: true},
			}).WaitToRun(context.Background(), scope)
			Expect(err).NotTo(HaveOccurred())
			Expect(run).To(BeTrue())
			Expect(gotLock).To(Equal(fakeLock))
			Expect(rateLimiter.waits).To(BeZero())
			Expect(scope.acquireCalls).To(Equal(1))
			Expect(scope.lastCheckCalls).To(Equal(2))
		})

		It("runs a manual resource check created after the last successful check started", func() {
			fakeBuild.CreateTimeReturns(now.Add(time.Minute))
			fakeLock := new(lockfakes.FakeLock)
			scope := &checkTimingScope{
				lastCheck: func() (db.LastCheck, error) {
					return db.LastCheck{StartTime: now, Succeeded: true}, nil
				},
				acquire: func(lager.Logger) (lock.Lock, bool, error) { return fakeLock, true, nil },
			}

			gotLock, run, err := newDelegate(atc.CheckPlan{
				Resource: "some-resource", SkipInterval: true,
			}).WaitToRun(context.Background(), scope)
			Expect(err).NotTo(HaveOccurred())
			Expect(run).To(BeTrue())
			Expect(gotLock).To(Equal(fakeLock))
			Expect(scope.acquireCalls).To(Equal(1))
		})

		It("runs a manual resource check when the last check failed", func() {
			fakeBuild.CreateTimeReturns(now.Add(-time.Minute))
			fakeLock := new(lockfakes.FakeLock)
			scope := &checkTimingScope{
				lastCheck: func() (db.LastCheck, error) {
					return db.LastCheck{StartTime: now, Succeeded: false}, nil
				},
				acquire: func(lager.Logger) (lock.Lock, bool, error) { return fakeLock, true, nil },
			}

			gotLock, run, err := newDelegate(atc.CheckPlan{
				Resource: "some-resource", SkipInterval: true,
			}).WaitToRun(context.Background(), scope)
			Expect(err).NotTo(HaveOccurred())
			Expect(run).To(BeTrue())
			Expect(gotLock).To(Equal(fakeLock))
			Expect(scope.acquireCalls).To(Equal(1))
		})

		It("retries after losing the resource lock and re-evaluates LastCheck", func() {
			interval := time.Minute
			scope := &checkTimingScope{}
			scope.lastCheck = func() (db.LastCheck, error) {
				if scope.lastCheckCalls == 1 {
					return db.LastCheck{EndTime: now.Add(-interval - time.Second), Succeeded: true}, nil
				}
				return db.LastCheck{EndTime: now, Succeeded: true}, nil
			}
			scope.acquire = func(lager.Logger) (lock.Lock, bool, error) {
				return nil, false, nil
			}
			go fakeClock.WaitForWatcherAndIncrement(time.Second)

			gotLock, run, err := newDelegate(atc.CheckPlan{
				Resource: "some-resource", Interval: atc.CheckEvery{Interval: interval},
			}).WaitToRun(context.Background(), scope)
			Expect(err).NotTo(HaveOccurred())
			Expect(run).To(BeFalse())
			Expect(gotLock).To(BeNil())
			Expect(scope.acquireCalls).To(Equal(1))
			Expect(scope.lastCheckCalls).To(Equal(2))
		})

		It("skips a nonmanual Interval.Never resource check without reading LastCheck", func() {
			scope := &checkTimingScope{
				lastCheck: func() (db.LastCheck, error) {
					Fail("Interval.Never must return before reading LastCheck")
					return db.LastCheck{}, nil
				},
				acquire: func(lager.Logger) (lock.Lock, bool, error) {
					Fail("Interval.Never must return before acquiring a lock")
					return nil, false, nil
				},
			}

			gotLock, run, err := newDelegate(atc.CheckPlan{
				Resource: "some-resource", Interval: atc.CheckEvery{Never: true},
			}).WaitToRun(context.Background(), scope)
			Expect(err).NotTo(HaveOccurred())
			Expect(run).To(BeFalse())
			Expect(gotLock).To(BeNil())
			Expect(scope.lastCheckCalls).To(BeZero())
			Expect(scope.acquireCalls).To(BeZero())
			Expect(rateLimiter.waits).To(BeZero())
		})

		It("skips an interval-blocked resource check before acquiring the lock", func() {
			scope := &checkTimingScope{
				lastCheck: func() (db.LastCheck, error) {
					return db.LastCheck{EndTime: now, Succeeded: true}, nil
				},
				acquire: func(lager.Logger) (lock.Lock, bool, error) {
					Fail("interval-blocked checks must not acquire a lock")
					return nil, false, nil
				},
			}
			_, run, err := newDelegate(atc.CheckPlan{
				Resource: "some-resource", Interval: atc.CheckEvery{Interval: time.Hour},
			}).WaitToRun(context.Background(), scope)
			Expect(err).NotTo(HaveOccurred())
			Expect(run).To(BeFalse())
			Expect(scope.acquireCalls).To(BeZero())
		})

		It("reuses a successful manual check created after this build", func() {
			fakeBuild.CreateTimeReturns(now.Add(-time.Minute))
			scope := &checkTimingScope{
				lastCheck: func() (db.LastCheck, error) {
					return db.LastCheck{StartTime: now, Succeeded: true}, nil
				},
				acquire: func(lager.Logger) (lock.Lock, bool, error) {
					Fail("reused manual checks must not acquire a lock")
					return nil, false, nil
				},
			}
			_, run, err := newDelegate(atc.CheckPlan{
				Resource: "some-resource", SkipInterval: true,
			}).WaitToRun(context.Background(), scope)
			Expect(err).NotTo(HaveOccurred())
			Expect(run).To(BeFalse())
			Expect(rateLimiter.waits).To(BeZero())
		})

		It("reuses a successful nested check completed after the build started", func() {
			fakeBuild.StartTimeReturns(now.Add(-time.Minute))
			scope := &checkTimingScope{lastCheck: func() (db.LastCheck, error) {
				return db.LastCheck{EndTime: now, Succeeded: true}, nil
			}}
			gotLock, run, err := newDelegate(atc.CheckPlan{
				ResourceType: "some-resource-type", Interval: atc.CheckEvery{Interval: time.Hour},
			}).WaitToRun(context.Background(), scope)
			Expect(err).NotTo(HaveOccurred())
			Expect(run).To(BeFalse())
			Expect(gotLock).To(BeNil())
			Expect(rateLimiter.waits).To(BeZero())
		})

		It("blocks a successful non-resource check solely while its interval is unelapsed", func() {
			fakeBuild.StartTimeReturns(now.Add(time.Minute))
			scope := &checkTimingScope{lastCheck: func() (db.LastCheck, error) {
				return db.LastCheck{EndTime: now, Succeeded: true}, nil
			}}

			gotLock, run, err := newDelegate(atc.CheckPlan{
				ResourceType: "some-resource-type",
				Interval:     atc.CheckEvery{Interval: time.Hour},
				SkipInterval: false,
			}).WaitToRun(context.Background(), scope)
			Expect(err).NotTo(HaveOccurred())
			Expect(run).To(BeFalse())
			Expect(gotLock).To(BeNil())
			Expect(scope.lastCheckCalls).To(Equal(1))
			Expect(rateLimiter.waits).To(BeZero())
		})

		DescribeTable("runs a non-resource check with a no-op lock",
			func(check atc.CheckPlan, lastEndOffset time.Duration, succeeded bool, buildStartOffset time.Duration) {
				fakeBuild.StartTimeReturns(now.Add(buildStartOffset))
				scope := &checkTimingScope{
					lastCheck: func() (db.LastCheck, error) {
						return db.LastCheck{EndTime: now.Add(lastEndOffset), Succeeded: succeeded}, nil
					},
					acquire: func(lager.Logger) (lock.Lock, bool, error) {
						Fail("non-resource checks must not acquire a resource lock")
						return nil, false, nil
					},
				}

				gotLock, run, err := newDelegate(check).WaitToRun(context.Background(), scope)
				Expect(err).NotTo(HaveOccurred())
				Expect(run).To(BeTrue())
				Expect(gotLock).To(Equal(lock.NoopLock{}))
				Expect(scope.lastCheckCalls).To(Equal(1))
				Expect(scope.acquireCalls).To(BeZero())
				Expect(rateLimiter.waits).To(BeZero())
			},
			Entry("after a prior failure",
				atc.CheckPlan{ResourceType: "some-resource-type"},
				time.Duration(0), false, -time.Minute,
			),
			Entry("when the last check ended before this build started",
				atc.CheckPlan{ResourceType: "some-resource-type"},
				-time.Minute, true, time.Duration(0),
			),
			Entry("after the configured interval elapsed",
				atc.CheckPlan{ResourceType: "some-resource-type", Interval: atc.CheckEvery{Interval: time.Hour}},
				-2*time.Hour, true, time.Duration(0),
			),
			Entry("when a manual check overrides an unelapsed interval",
				atc.CheckPlan{ResourceType: "some-resource-type", SkipInterval: true, Interval: atc.CheckEvery{Interval: time.Hour}},
				time.Duration(0), true, time.Minute,
			),
			Entry("when a failed check overrides an unelapsed interval",
				atc.CheckPlan{ResourceType: "some-resource-type", Interval: atc.CheckEvery{Interval: time.Hour}},
				time.Duration(0), false, time.Minute,
			),
		)

		It("propagates the selected timing lookup error", func() {
			scope := &checkTimingScope{lastCheck: func() (db.LastCheck, error) {
				return db.LastCheck{}, errors.New("some-error")
			}}
			_, _, err := newDelegate(atc.CheckPlan{ResourceType: "some-resource-type"}).WaitToRun(context.Background(), scope)
			Expect(err).To(MatchError("some-error"))
		})

		It("propagates resource LastCheck errors for periodic and manual checks", func() {
			for _, check := range []atc.CheckPlan{
				{Resource: "some-resource"},
				{Resource: "some-resource", SkipInterval: true},
			} {
				waitsBefore := rateLimiter.waits
				scope := &checkTimingScope{
					lastCheck: func() (db.LastCheck, error) {
						return db.LastCheck{}, errors.New("some-error")
					},
					acquire: func(lager.Logger) (lock.Lock, bool, error) {
						Fail("a LastCheck error must return before acquiring the lock")
						return nil, false, nil
					},
				}

				gotLock, run, err := newDelegate(check).WaitToRun(context.Background(), scope)
				Expect(err).To(MatchError("some-error"))
				Expect(run).To(BeFalse())
				Expect(gotLock).To(BeNil())
				Expect(scope.lastCheckCalls).To(Equal(1))
				Expect(scope.acquireCalls).To(BeZero())
				if check.SkipInterval {
					Expect(rateLimiter.waits).To(Equal(waitsBefore))
				} else {
					Expect(rateLimiter.waits).To(Equal(waitsBefore + 1))
				}
			}
		})

		It("does not update the scope when OnCheckBuildStart fails", func() {
			fakeBuild.OnCheckBuildStartReturns(errors.New("some-error"))
			scope := &checkTimingScope{updateStart: func(int, *json.RawMessage) (bool, error) {
				Fail("scope must not update after OnCheckBuildStart fails")
				return false, nil
			}}
			found, buildID, err := newDelegate(atc.CheckPlan{Resource: "some-resource"}).UpdateScopeLastCheckStartTime(scope, false)
			Expect(err).To(MatchError("some-error"))
			Expect(found).To(BeFalse())
			Expect(buildID).To(BeZero())
			Expect(scope.updateStartArgs).To(BeEmpty())
		})

		It("preserves nonnested build evidence when the scope start update fails", func() {
			publicPlan := json.RawMessage(`{"id":"some-plan"}`)
			fakeBuild.IDReturns(9999)
			fakeBuild.PublicPlanReturns(&publicPlan)
			scope := &checkTimingScope{updateStart: func(buildID int, plan *json.RawMessage) (bool, error) {
				Expect(buildID).To(Equal(9999))
				Expect(plan).To(Equal(&publicPlan))
				return false, errors.New("some-error")
			}}

			found, buildID, err := newDelegate(atc.CheckPlan{Resource: "some-resource"}).UpdateScopeLastCheckStartTime(scope, false)
			Expect(err).To(MatchError("some-error"))
			Expect(found).To(BeFalse())
			Expect(buildID).To(Equal(9999))
			Expect(scope.updateStartArgs).To(HaveLen(1))
			Expect(fakeBuild.OnCheckBuildStartCallCount()).To(Equal(1))
		})

		It("updates nested-check start evidence once without touching the build", func() {
			scope := &checkTimingScope{updateStart: func(buildID int, plan *json.RawMessage) (bool, error) {
				Expect(buildID).To(BeZero())
				Expect(plan).To(BeNil())
				return true, nil
			}}

			found, buildID, err := newDelegate(atc.CheckPlan{ResourceType: "some-resource-type"}).UpdateScopeLastCheckStartTime(scope, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(buildID).To(BeZero())
			Expect(scope.updateStartArgs).To(HaveLen(1))
			Expect(fakeBuild.OnCheckBuildStartCallCount()).To(BeZero())
		})

		It("passes nested-check nil build evidence and bounds a scope update failure", func() {
			scope := &checkTimingScope{updateStart: func(buildID int, plan *json.RawMessage) (bool, error) {
				Expect(buildID).To(BeZero())
				Expect(plan).To(BeNil())
				return false, errors.New("some-error")
			}}
			found, buildID, err := newDelegate(atc.CheckPlan{ResourceType: "some-resource-type"}).UpdateScopeLastCheckStartTime(scope, true)
			Expect(err).To(MatchError("some-error"))
			Expect(found).To(BeFalse())
			Expect(buildID).To(BeZero())
			Expect(fakeBuild.OnCheckBuildStartCallCount()).To(BeZero())
		})

		It("passes end status and bounds a scope completion failure", func() {
			scope := &checkTimingScope{updateEnd: func(succeeded bool) (bool, error) {
				Expect(succeeded).To(BeTrue())
				return false, errors.New("some-error")
			}}
			found, err := newDelegate(atc.CheckPlan{}).UpdateScopeLastCheckEndTime(scope, true)
			Expect(err).To(MatchError("some-error"))
			Expect(found).To(BeFalse())
			Expect(scope.updateEndArgs).To(Equal([]bool{true}))
		})

		It("bounds pipeline lookup errors", func() {
			fakeBuild.PipelineReturns(nil, false, errors.New("nope"))
			_, err := newDelegate(atc.CheckPlan{Resource: "some-resource"}).FindOrCreateScope(
				scopeErrorResourceConfig{err: errors.New("scope must not be reached")},
			)
			Expect(err).To(MatchError(ContainSubstring("get build pipeline: nope")))
		})
	})
})
