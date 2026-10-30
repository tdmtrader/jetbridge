package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"code.cloudfoundry.org/clock/fakeclock"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/imageresolver"
	"github.com/concourse/concourse/atc/imageresolver/imageresolvertesting"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/vars"
)

var noopStepper exec.Stepper = func(atc.Plan) exec.Step {
	Fail("cannot create substep")
	return nil
}

// scriptedStep is a Step whose behavior the spec writes itself.
type scriptedStep struct {
	RunStub func(context.Context, exec.RunState) (bool, error)
}

func (s *scriptedStep) Run(ctx context.Context, state exec.RunState) (bool, error) {
	if s.RunStub == nil {
		return false, nil
	}
	return s.RunStub(ctx, state)
}

func taskDelegateBuildEventCount(fixture *EngineDBFixture, build db.Build) int {
	GinkgoHelper()
	var count int
	Expect(fixture.Conn.QueryRow(`
		SELECT COUNT(*)
		FROM build_events
		WHERE build_id = $1
	`, build.ID()).Scan(&count)).To(Succeed())
	return count
}

func consumeTaskDelegateBuildEvent(
	fixture *EngineDBFixture,
	build db.Build,
	expectedCount int,
	from uint,
) atc.Event {
	GinkgoHelper()
	Expect(taskDelegateBuildEventCount(fixture, build)).To(Equal(expectedCount))
	return ConsumeEngineBuildEvent(build, from)
}

func taskDelegateCacheAssociationCount(fixture *EngineDBFixture, build db.Build, cache db.ResourceCache) int {
	GinkgoHelper()
	var count int
	Expect(fixture.Conn.QueryRow(`
		SELECT COUNT(*)
		FROM build_image_resource_caches
		WHERE build_id = $1 AND resource_cache_id = $2
	`, build.ID(), cache.ID()).Scan(&count)).To(Succeed())
	return count
}

func taskDelegateCacheUseCount(fixture *EngineDBFixture, build db.Build, cache db.ResourceCache) int {
	GinkgoHelper()
	var count int
	Expect(fixture.Conn.QueryRow(`
		SELECT COUNT(*)
		FROM resource_cache_uses
		WHERE resource_cache_id = $1 AND build_id = $2
	`, cache.ID(), build.ID()).Scan(&count)).To(Succeed())
	return count
}

func findTaskDelegateScope(fixture *EngineDBFixture, resourceType string, source atc.Source) db.ResourceConfigScope {
	GinkgoHelper()
	config, err := fixture.ResourceConfigFactory.FindOrCreateResourceConfig(resourceType, source, nil)
	Expect(err).NotTo(HaveOccurred())
	scope, err := config.FindOrCreateScope(nil)
	Expect(err).NotTo(HaveOccurred())
	return scope
}

func saveTaskDelegateVersion(fixture *EngineDBFixture, resourceType string, source atc.Source, version atc.Version) db.ResourceConfigScope {
	GinkgoHelper()
	scope := findTaskDelegateScope(fixture, resourceType, source)
	Expect(scope.SaveVersions(db.SpanContext{}, []atc.Version{version})).To(Succeed())
	return scope
}

func createTaskDelegateCache(
	fixture *EngineDBFixture,
	build db.Build,
	resourceType string,
	version atc.Version,
	source atc.Source,
	params atc.Params,
) db.ResourceCache {
	GinkgoHelper()
	cache, err := fixture.ResourceCacheFactory.FindOrCreateResourceCache(
		db.ForBuild(build.ID()), resourceType, version, source, params, nil,
	)
	Expect(err).NotTo(HaveOccurred())
	return cache
}

func taskDelegateAssociatedCache(fixture *EngineDBFixture, build db.Build) db.ResourceCache {
	GinkgoHelper()
	var cacheID int
	Expect(fixture.Conn.QueryRow(`
		SELECT resource_cache_id
		FROM build_image_resource_caches
		WHERE build_id = $1
		ORDER BY resource_cache_id DESC
		LIMIT 1
	`, build.ID()).Scan(&cacheID)).To(Succeed())
	cache, found, err := fixture.ResourceCacheFactory.FindResourceCacheByID(cacheID)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	return cache
}

func taskDelegateMetadataCounts(fixture *EngineDBFixture) (int, int) {
	GinkgoHelper()
	var scopeCount, versionCount int
	Expect(fixture.Conn.QueryRow(`SELECT COUNT(*) FROM resource_config_scopes`).Scan(&scopeCount)).To(Succeed())
	Expect(fixture.Conn.QueryRow(`SELECT COUNT(*) FROM resource_config_versions`).Scan(&versionCount)).To(Succeed())
	return scopeCount, versionCount
}

var _ = Describe("TaskDelegate", func() {
	var (
		logger    *lagertest.TestLogger
		fixture   *EngineDBFixture
		realBuild db.Build
		fakeClock *fakeclock.FakeClock

		state exec.RunState

		now = time.Date(1991, 6, 3, 5, 30, 0, 0, time.UTC)

		delegate *taskDelegate
		planID   = atc.PlanID("some-plan-id")

		exitStatus exec.ExitStatus
	)

	BeforeEach(func() {
		logger = lagertest.NewTestLogger("test")

		fixture = UseEngineDB()
		_, _, _, realBuild = CreateEngineJobBuild(
			fixture,
			"task-delegate-team",
			atc.PipelineRef{Name: "some-pipeline"},
			atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
			"some-user",
		)
		fakeClock = fakeclock.NewFakeClock(now)
		credVars := vars.StaticVariables{
			"source-param": "super-secret-source",
			"git-key":      "{\n123\n456\n789\n}\n",
		}
		state = exec.NewRunState(noopStepper, credVars)

		delegate = NewTaskDelegate(realBuild, planID, state, fakeClock, fixture.WorkerFactory, fixture.LockFactory).(*taskDelegate)

		delegate.SetTaskConfig(atc.TaskConfig{
			Platform: "some-platform",
			Run: atc.TaskRunConfig{
				Path: "some-foo-path",
				Dir:  "some-bar-dir",
			},
		})
	})

	Describe("Initializing", func() {
		JustBeforeEach(func() {
			delegate.Initializing(logger)
		})

		It("saves an event", func() {
			persisted := consumeTaskDelegateBuildEvent(fixture, realBuild, 1, 0)
			Expect(persisted.EventType()).To(Equal(atc.EventType("initialize-task")))
		})

		It("calls SaveEvent with the taskConfig", func() {
			persisted := consumeTaskDelegateBuildEvent(fixture, realBuild, 1, 0)
			Expect(json.Marshal(persisted)).To(MatchJSON(`{
				"time": 675927000,
				"origin": {"id": "some-plan-id"},
				"config": {
					"platform": "some-platform",
					"image":"",
					"run": {
						"path": "some-foo-path",
						"args": null,
						"dir": "some-bar-dir"
					},
					"inputs":null
				}
			}`))
		})
	})

	Describe("Starting", func() {
		JustBeforeEach(func() {
			delegate.Starting(logger)
		})

		It("saves an event", func() {
			persisted := consumeTaskDelegateBuildEvent(fixture, realBuild, 1, 0)
			Expect(persisted.EventType()).To(Equal(atc.EventType("start-task")))
		})

		It("calls SaveEvent with the taskConfig", func() {
			persisted := consumeTaskDelegateBuildEvent(fixture, realBuild, 1, 0)
			Expect(json.Marshal(persisted)).To(MatchJSON(`{
				"time": 675927000,
				"origin": {"id": "some-plan-id"},
				"config": {
					"platform": "some-platform",
					"image":"",
					"run": {
						"path": "some-foo-path",
						"args": null,
						"dir": "some-bar-dir"
					},
					"inputs":null
				}
			}`))
		})
	})

	Describe("Finished", func() {
		JustBeforeEach(func() {
			delegate.Finished(logger, exitStatus)
		})

		It("saves an event", func() {
			persisted := consumeTaskDelegateBuildEvent(fixture, realBuild, 1, 0)
			Expect(persisted.EventType()).To(Equal(atc.EventType("finish-task")))
		})
	})

	Describe("SidecarWriter", func() {
		It("returns a non-nil writer", func() {
			w := delegate.SidecarWriter("postgres")
			Expect(w).ToNot(BeNil())
			Expect(w.(io.Closer).Close()).To(Succeed())
			Expect(taskDelegateBuildEventCount(fixture, realBuild)).To(Equal(0))
		})

		It("writes produce Log events with the sidecar plan ID as origin", func() {
			w := delegate.SidecarWriter("postgres")
			_, writeErr := w.Write([]byte("starting postgres"))
			Expect(writeErr).ToNot(HaveOccurred())

			Expect(w.(io.Closer).Close()).To(Succeed())

			ev := consumeTaskDelegateBuildEvent(fixture, realBuild, 1, 0)
			Expect(ev.EventType()).To(Equal(atc.EventType("log")))

			evJSON, _ := json.Marshal(ev)
			Expect(evJSON).To(ContainSubstring("some-plan-id/sidecar/postgres"))
		})
	})

	Describe("EmitSidecarPlans", func() {
		var sidecars []atc.SidecarConfig

		BeforeEach(func() {
			sidecars = []atc.SidecarConfig{
				{Name: "postgres", Image: "postgres:16"},
				{Name: "redis", Image: "redis:7"},
			}
		})

		JustBeforeEach(func() {
			delegate.EmitSidecarPlans(logger, sidecars)
		})

		It("saves a sidecar event per sidecar", func() {
			ev0 := consumeTaskDelegateBuildEvent(fixture, realBuild, 2, 0)
			Expect(ev0.EventType()).To(Equal(atc.EventType("sidecar")))

			ev1 := consumeTaskDelegateBuildEvent(fixture, realBuild, 2, 1)
			Expect(ev1.EventType()).To(Equal(atc.EventType("sidecar")))
		})

		It("emits events with the parent plan ID as origin", func() {
			ev0 := consumeTaskDelegateBuildEvent(fixture, realBuild, 2, 0)
			Expect(json.Marshal(ev0)).To(MatchJSON(`{
				"time": 675927000,
				"origin": {"id": "some-plan-id"},
				"plan": {
					"id": "some-plan-id/sidecar/postgres",
					"sidecar": {
						"name": "postgres",
						"image": "postgres:16"
					}
				}
			}`))

			ev1 := consumeTaskDelegateBuildEvent(fixture, realBuild, 2, 1)
			Expect(json.Marshal(ev1)).To(MatchJSON(`{
				"time": 675927000,
				"origin": {"id": "some-plan-id"},
				"plan": {
					"id": "some-plan-id/sidecar/redis",
					"sidecar": {
						"name": "redis",
						"image": "redis:7"
					}
				}
			}`))
		})

		Context("with no sidecars", func() {
			BeforeEach(func() {
				sidecars = nil
			})

			It("does not save any events", func() {
				Expect(taskDelegateBuildEventCount(fixture, realBuild)).To(Equal(0))
			})
		})
	})

	Describe("FetchImage", func() {
		// A task's image_resource is a registry-image reference the build step
		// delegate resolves natively. Nothing runs as a step and no event is
		// emitted for it.
		var (
			runPlans  []atc.Plan
			imageSpec runtime.ImageSpec
			fetchErr  error
			source    atc.Source
		)

		BeforeEach(func() {
			runPlans = nil
			stepper := func(p atc.Plan) exec.Step {
				runPlans = append(runPlans, p)
				return new(scriptedStep)
			}
			delegate = NewTaskDelegate(
				realBuild, planID, exec.NewRunState(stepper, vars.StaticVariables{"tag-var": "v7"}), fakeClock,
				fixture.WorkerFactory, fixture.LockFactory,
				WithResourceFactories(fixture.ResourceConfigFactory, fixture.ResourceCacheFactory, nil),
			).(*taskDelegate)

			source = atc.Source{"repository": "registry.example/task", "tag": "((tag-var))"}
			saveTaskDelegateVersion(fixture, "registry-image", atc.Source{"repository": "registry.example/task", "tag": "v7"}, atc.Version{"digest": "sha256:task"})
		})

		JustBeforeEach(func() {
			imageSpec, fetchErr = delegate.FetchImage(context.TODO(), atc.ImageResource{
				Type:   "registry-image",
				Source: source,
				Params: atc.Params{"format": "oci"},
			}, true, atc.Tags{"some", "tags"})
		})

		It("resolves the reference natively, running no step", func() {
			Expect(fetchErr).ToNot(HaveOccurred())
			Expect(runPlans).To(BeEmpty())
			Expect(imageSpec).To(Equal(runtime.ImageSpec{
				ImageURL:   "docker:///registry.example/task@sha256:task",
				Privileged: true,
			}))
		})

		It("records the image's resource cache for the build and emits no event", func() {
			Expect(fetchErr).ToNot(HaveOccurred())
			cache := taskDelegateAssociatedCache(fixture, realBuild)
			Expect(cache.Version()).To(Equal(atc.Version{"digest": "sha256:task"}))
			Expect(cache.BaseResourceType().Name).To(Equal("registry-image"))
			Expect(taskDelegateBuildEventCount(fixture, realBuild)).To(BeZero())
		})

		Context("when the image_resource is not a registry-image reference", func() {
			JustBeforeEach(func() {
				imageSpec, fetchErr = delegate.FetchImage(context.TODO(), atc.ImageResource{
					Type:   "docker-image",
					Source: atc.Source{"repository": "busybox"},
				}, false, nil)
			})

			It("refuses it by name without running a step", func() {
				Expect(fetchErr).To(MatchError(atc.UnsupportedImageTypeError{Type: "docker-image"}))
				Expect(runPlans).To(BeEmpty())
				Expect(taskDelegateBuildEventCount(fixture, realBuild)).To(BeZero())
			})
		})
	})

	Describe("integration: FetchImage via DelegateFactory", func() {
		// These tests exercise the full production code path:
		// DelegateFactory.TaskDelegate() → configureDelegate() → FetchImage()
		// No internal type assertions or direct struct manipulation.

		var (
			resourceConfigFactory db.ResourceConfigFactory
			resourceCacheFactory  db.ResourceCacheFactory
			imageResolver         imageresolver.Resolver

			delegateFactory DelegateFactory
		)

		// Helper: build a DelegateFactory and return a TaskDelegate.
		// The stepper records which plans are executed so we can observe
		// whether pods would be spawned.
		buildTaskDelegate := func(stepper exec.Stepper) (exec.TaskDelegate, *[]atc.Plan) {
			var executedPlans []atc.Plan
			wrappedStepper := func(p atc.Plan) exec.Step {
				executedPlans = append(executedPlans, p)
				return stepper(p)
			}

			state := exec.NewRunState(wrappedStepper, nil)
			plan := atc.Plan{ID: planID}

			delegateFactory = DelegateFactory{
				build:                 realBuild,
				plan:                  plan,
				dbWorkerFactory:       fixture.WorkerFactory,
				lockFactory:           fixture.LockFactory,
				resourceConfigFactory: resourceConfigFactory,
				resourceCacheFactory:  resourceCacheFactory,
				imageResolver:         imageResolver,
			}

			td := delegateFactory.TaskDelegate(state)
			return td, &executedPlans
		}

		BeforeEach(func() {
			resourceConfigFactory = fixture.ResourceConfigFactory
			resourceCacheFactory = fixture.ResourceCacheFactory
			imageResolver = nil
		})

		It("resolves a registry-image type without spawning extra pods when the version is cached", func() {
			source := atc.Source{"repository": "my-org/my-image"}
			saveTaskDelegateVersion(fixture, "registry-image", source, atc.Version{"digest": "sha256:metadata42"})

			noopStepper := func(p atc.Plan) exec.Step {
				step := new(scriptedStep)
				step.RunStub = func(_ context.Context, s exec.RunState) (bool, error) {
					return true, nil
				}
				return step
			}

			td, executedPlans := buildTaskDelegate(noopStepper)

			imgSpec, err := td.FetchImage(
				context.TODO(),
				atc.ImageResource{Name: "image", Type: "registry-image", Source: source},
				false, nil,
			)
			Expect(err).ToNot(HaveOccurred())

			Expect(*executedPlans).To(BeEmpty(), "no check+get pods should be spawned")
			Expect(imgSpec.ImageURL).To(Equal("docker:///my-org/my-image@sha256:metadata42"))
			Expect(imgSpec.ImageArtifact).To(BeNil(), "no volume artifact expected")
			cache := taskDelegateAssociatedCache(fixture, realBuild)
			Expect(cache.Version()).To(Equal(atc.Version{"digest": "sha256:metadata42"}))
		})

		It("saves image resource version for build tracking", func() {
			source := atc.Source{"repository": "my-org/tracked"}
			saveTaskDelegateVersion(fixture, "registry-image", source, atc.Version{"digest": "sha256:metadata42"})

			noopStepper := func(p atc.Plan) exec.Step {
				step := new(scriptedStep)
				step.RunStub = func(_ context.Context, s exec.RunState) (bool, error) { return true, nil }
				return step
			}

			td, _ := buildTaskDelegate(noopStepper)

			_, err := td.FetchImage(
				context.TODO(),
				atc.ImageResource{Name: "image", Type: "registry-image", Source: source},
				false, nil,
			)
			Expect(err).ToNot(HaveOccurred())

			savedCache := createTaskDelegateCache(
				fixture, realBuild, "registry-image", atc.Version{"digest": "sha256:metadata42"}, source, nil,
			)
			Expect(taskDelegateCacheAssociationCount(fixture, realBuild, savedCache)).To(Equal(1))
		})

		It("produces a valid docker:// URL with digest for all registry-image resolutions", func() {
			source := atc.Source{"repository": "gcr.io/my-project/worker-image"}
			saveTaskDelegateVersion(fixture, "registry-image", source, atc.Version{"digest": "sha256:metadata42"})

			noopStepper := func(p atc.Plan) exec.Step {
				step := new(scriptedStep)
				step.RunStub = func(_ context.Context, s exec.RunState) (bool, error) { return true, nil }
				return step
			}

			td, _ := buildTaskDelegate(noopStepper)

			imgSpec, err := td.FetchImage(
				context.TODO(),
				atc.ImageResource{Name: "image", Type: "registry-image", Source: source},
				false, nil,
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(imgSpec.ImageURL).To(HavePrefix("docker:///"))
			Expect(imgSpec.ImageURL).To(ContainSubstring("@sha256:"))
			Expect(imgSpec.ImageURL).To(ContainSubstring("gcr.io/my-project/worker-image"))
		})

		It("refuses a non-registry-image type by name instead of running a step", func() {
			noopStepper := func(p atc.Plan) exec.Step {
				Fail("no step may run for an image")
				return nil
			}

			td, executedPlans := buildTaskDelegate(noopStepper)

			_, err := td.FetchImage(
				context.TODO(),
				atc.ImageResource{Name: "image", Type: "s3-resource", Source: atc.Source{"bucket": "my-bucket"}},
				false, nil,
			)
			Expect(err).To(MatchError(atc.UnsupportedImageTypeError{Type: "s3-resource"}))
			Expect(*executedPlans).To(BeEmpty())
		})

		It("returns the database error instead of running a step", func() {
			source := atc.Source{"repository": "my-org/db-fail-test"}
			resourceConfigFactory = db.NewResourceConfigFactory(ClosedEngineCloneConn(), fixture.LockFactory)
			imageResolver = nil

			noopStepper := func(p atc.Plan) exec.Step {
				Fail("no step may run for an image")
				return nil
			}

			td, executedPlans := buildTaskDelegate(noopStepper)

			_, err := td.FetchImage(
				context.TODO(),
				atc.ImageResource{Name: "image", Type: "registry-image", Source: source},
				false, nil,
			)
			Expect(err).To(MatchError(ContainSubstring("find resource config:")))
			Expect(*executedPlans).To(BeEmpty())
		})

		It("does not spawn any pods when resource factories are injected and cache is warm", func() {
			source := atc.Source{"repository": "my-org/warm-cache"}
			saveTaskDelegateVersion(fixture, "registry-image", source, atc.Version{"digest": "sha256:metadata42"})

			// This is the key optimization: with warm cache, zero pods for type images.
			noopStepper := func(p atc.Plan) exec.Step {
				Fail("no steps should be created when cache is warm")
				return nil
			}

			td, _ := buildTaskDelegate(noopStepper)

			imgSpec, err := td.FetchImage(
				context.TODO(),
				atc.ImageResource{Name: "image", Type: "registry-image", Source: source},
				false, nil,
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(imgSpec.ImageURL).To(Equal("docker:///my-org/warm-cache@sha256:metadata42"))
		})

		It("resolves on-demand via resolver when no cached version exists", func() {
			source := atc.Source{"repository": "my-org/on-demand"}
			scope := findTaskDelegateScope(fixture, "registry-image", source)

			fakeResolver := new(imageresolvertesting.FakeResolver)
			fakeResolver.ResolveReturns("sha256:ondemand456", nil)
			imageResolver = fakeResolver

			noopStepper := func(p atc.Plan) exec.Step {
				Fail("no steps should be created when resolver handles resolution")
				return nil
			}

			td, executedPlans := buildTaskDelegate(noopStepper)

			imgSpec, err := td.FetchImage(
				context.TODO(),
				atc.ImageResource{Name: "image", Type: "registry-image", Source: source},
				false, nil,
			)
			Expect(err).ToNot(HaveOccurred())

			By("resolving via the resolver")
			Expect(fakeResolver.ResolveCallCount()).To(Equal(1))
			_, repo, _, _ := fakeResolver.ResolveArgsForCall(0)
			Expect(repo).To(Equal("my-org/on-demand"))
			Expect(*executedPlans).To(BeEmpty())

			By("saving the resolved version to DB")
			latest, found, latestErr := scope.LatestVersion()
			Expect(latestErr).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(atc.Version(latest.Version())).To(Equal(atc.Version{"digest": "sha256:ondemand456"}))

			By("returning the correct image URL")
			Expect(imgSpec.ImageURL).To(Equal("docker:///my-org/on-demand@sha256:ondemand456"))
			Expect(imgSpec.ImageArtifact).To(BeNil())
			Expect(taskDelegateAssociatedCache(fixture, realBuild).Version()).To(Equal(atc.Version{"digest": "sha256:ondemand456"}))
		})

		It("returns error when resolver fails (no fallback to pods)", func() {
			source := atc.Source{"repository": "my-org/fail-test"}
			scope := findTaskDelegateScope(fixture, "registry-image", source)

			fakeResolver := new(imageresolvertesting.FakeResolver)
			fakeResolver.ResolveReturns("", fmt.Errorf("registry timeout"))
			imageResolver = fakeResolver

			noopStepper := func(p atc.Plan) exec.Step {
				Fail("no steps should be created when resolver is configured")
				return nil
			}

			td, executedPlans := buildTaskDelegate(noopStepper)

			_, err := td.FetchImage(
				context.TODO(),
				atc.ImageResource{Name: "image", Type: "registry-image", Source: source},
				false, nil,
			)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("on-demand image resolve"))
			Expect(err.Error()).To(ContainSubstring("registry timeout"))
			Expect(*executedPlans).To(BeEmpty())
			latest, found, latestErr := scope.LatestVersion()
			Expect(latestErr).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
			Expect(latest).To(BeNil())
		})

		It("fails without resource factories rather than fetch the image another way", func() {
			source := atc.Source{"repository": "my-org/no-factory"}

			noopStepper := func(p atc.Plan) exec.Step {
				Fail("no step may run for an image")
				return nil
			}

			resourceConfigFactory = nil
			resourceCacheFactory = nil
			td, executedPlans := buildTaskDelegate(noopStepper)

			_, err := td.FetchImage(
				context.TODO(),
				atc.ImageResource{Name: "image", Type: "registry-image", Source: source},
				false, nil,
			)
			Expect(err).To(MatchError("image fetch needs the resource config and resource cache factories"))
			Expect(*executedPlans).To(BeEmpty())
		})

		Describe("pinned image versions", func() {
			// A pinned digest is already the exact answer any resolution would
			// compute. Requiring a DB scope lookup or a registry round-trip to
			// confirm it defeats the point of pinning — and fails closed when
			// the registry is unreachable (the registry.home TLS failure that
			// broke `fly agent snapshots capture-resource`).
			const pinnedDigest = "sha256:4866878ca7324e5c3d1fb9f250ce16e0ef6d9505166b4b57e7a59cc6b86dba74"

			var fakeResolver *imageresolvertesting.FakeResolver

			// Builds a delegate through the production factory path with an
			// image resolver attached. The stepper fails the spec if any
			// check/get plan is executed, so pod-spawning is caught too.
			buildResolvingTaskDelegate := func() exec.TaskDelegate {
				failStepper := func(p atc.Plan) exec.Step {
					Fail("no check/get plans should run on the metadata path")
					return nil
				}

				df := DelegateFactory{
					build:                 realBuild,
					plan:                  atc.Plan{ID: planID},
					dbWorkerFactory:       fixture.WorkerFactory,
					lockFactory:           fixture.LockFactory,
					resourceConfigFactory: resourceConfigFactory,
					resourceCacheFactory:  resourceCacheFactory,
					imageResolver:         fakeResolver,
				}
				return df.TaskDelegate(exec.NewRunState(failStepper, nil))
			}

			BeforeEach(func() {
				fakeResolver = new(imageresolvertesting.FakeResolver)
				fakeResolver.ResolveReturns("sha256:resolver-must-not-be-used", nil)
			})

			It("uses the pinned digest without calling the image resolver", func() {
				source := atc.Source{"repository": "registry.home/agent-runner"}
				resourceConfigFactory = db.NewResourceConfigFactory(ClosedEngineCloneConn(), fixture.LockFactory)
				td := buildResolvingTaskDelegate()

				imgSpec, err := td.FetchImage(
					context.TODO(),
					atc.ImageResource{
						Name:    "image",
						Type:    "registry-image",
						Source:  source,
						Version: atc.Version{"digest": pinnedDigest},
					},
					false, nil,
				)
				Expect(err).ToNot(HaveOccurred())

				By("never reaching out to the registry")
				Expect(fakeResolver.ResolveCallCount()).To(Equal(0))

				By("returning an image URL pinned to the requested digest")
				Expect(imgSpec.ImageURL).To(Equal("docker:///registry.home/agent-runner@" + pinnedDigest))
				Expect(imgSpec.ImageArtifact).To(BeNil(), "no volume artifact on the metadata path")
				cache := taskDelegateAssociatedCache(fixture, realBuild)
				Expect(cache.Version()).To(Equal(atc.Version{"digest": pinnedDigest}))
			})

			It("succeeds on a cold cache even when the registry is unreachable", func() {
				// Exactly the live failure: nothing cached for
				// registry.home/agent-runner, so the pre-fix code did an
				// on-demand resolve of ":latest" and died on the registry's
				// TLS certificate. A pinned digest needs no registry at all.
				fakeResolver.ResolveReturns("", fmt.Errorf(
					`Get "https://registry.home/v2/": tls: failed to verify certificate`))
				resourceConfigFactory = db.NewResourceConfigFactory(ClosedEngineCloneConn(), fixture.LockFactory)

				td := buildResolvingTaskDelegate()

				imgSpec, err := td.FetchImage(
					context.TODO(),
					atc.ImageResource{
						Name:    "image",
						Type:    "registry-image",
						Source:  atc.Source{"repository": "registry.home/agent-runner"},
						Version: atc.Version{"digest": pinnedDigest},
					},
					false, nil,
				)
				Expect(err).ToNot(HaveOccurred())

				Expect(fakeResolver.ResolveCallCount()).To(Equal(0), "a pinned digest must never hit the registry")
				Expect(imgSpec.ImageURL).To(Equal("docker:///registry.home/agent-runner@" + pinnedDigest))
				Expect(taskDelegateAssociatedCache(fixture, realBuild).Version()).To(Equal(atc.Version{"digest": pinnedDigest}))
			})

			It("neither reads nor writes the resource config scope when pinned", func() {
				scopesBefore, versionsBefore := taskDelegateMetadataCounts(fixture)
				resourceConfigFactory = db.NewResourceConfigFactory(ClosedEngineCloneConn(), fixture.LockFactory)
				td := buildResolvingTaskDelegate()

				_, err := td.FetchImage(
					context.TODO(),
					atc.ImageResource{
						Name:    "image",
						Type:    "registry-image",
						Source:  atc.Source{"repository": "registry.home/agent-runner"},
						Version: atc.Version{"digest": pinnedDigest},
					},
					false, nil,
				)
				Expect(err).ToNot(HaveOccurred())

				By("leaving scopes and versions untouched while the cache row is persisted")
				scopesAfter, versionsAfter := taskDelegateMetadataCounts(fixture)
				Expect(scopesAfter).To(Equal(scopesBefore))
				Expect(versionsAfter).To(Equal(versionsBefore))
				Expect(taskDelegateAssociatedCache(fixture, realBuild).Version()).To(Equal(atc.Version{"digest": pinnedDigest}))
			})

			It("still builds and saves a resource cache for the pinned version", func() {
				source := atc.Source{"repository": "registry.home/agent-runner"}
				params := atc.Params{"some": "param"}
				resourceConfigFactory = db.NewResourceConfigFactory(ClosedEngineCloneConn(), fixture.LockFactory)
				td := buildResolvingTaskDelegate()

				_, err := td.FetchImage(
					context.TODO(),
					atc.ImageResource{
						Name:    "image",
						Type:    "registry-image",
						Source:  source,
						Params:  params,
						Version: atc.Version{"digest": pinnedDigest},
					},
					false, nil,
				)
				Expect(err).ToNot(HaveOccurred())

				By("keying the persisted cache on the exact source, version, and params")
				associated := taskDelegateAssociatedCache(fixture, realBuild)
				Expect(taskDelegateCacheUseCount(fixture, realBuild, associated)).To(Equal(1))
				expected := createTaskDelegateCache(
					fixture, realBuild, "registry-image", atc.Version{"digest": pinnedDigest}, source, params,
				)
				wrongParams := createTaskDelegateCache(
					fixture, realBuild, "registry-image", atc.Version{"digest": pinnedDigest}, source, atc.Params{"some": "other"},
				)
				Expect(associated.ID()).To(Equal(expected.ID()))
				Expect(associated.ID()).NotTo(Equal(wrongParams.ID()))
				Expect(taskDelegateCacheAssociationCount(fixture, realBuild, associated)).To(Equal(1))
			})

			It("uses the cached DB version, not the resolver, when nothing is pinned", func() {
				source := atc.Source{"repository": "registry.home/agent-runner"}
				scope := saveTaskDelegateVersion(
					fixture, "registry-image", source, atc.Version{"digest": "sha256:stale-cached"},
				)
				td := buildResolvingTaskDelegate()

				imgSpec, err := td.FetchImage(
					context.TODO(),
					atc.ImageResource{
						Name:   "image",
						Type:   "registry-image",
						Source: source,
					},
					false, nil,
				)
				Expect(err).ToNot(HaveOccurred())

				Expect(fakeResolver.ResolveCallCount()).To(Equal(0), "a warm cache must not hit the registry")
				Expect(imgSpec.ImageURL).To(Equal("docker:///registry.home/agent-runner@sha256:stale-cached"))
				latest, found, latestErr := scope.LatestVersion()
				Expect(latestErr).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(atc.Version(latest.Version())).To(Equal(atc.Version{"digest": "sha256:stale-cached"}))
				Expect(taskDelegateAssociatedCache(fixture, realBuild).Version()).To(Equal(atc.Version{"digest": "sha256:stale-cached"}))
			})

			It("falls back to an on-demand resolve when nothing is pinned and nothing is cached", func() {
				source := atc.Source{"repository": "registry.home/agent-runner", "tag": "v1"}
				scope := findTaskDelegateScope(fixture, "registry-image", source)
				fakeResolver.ResolveReturns("sha256:resolved-on-demand", nil)

				td := buildResolvingTaskDelegate()

				imgSpec, err := td.FetchImage(
					context.TODO(),
					atc.ImageResource{
						Name:   "image",
						Type:   "registry-image",
						Source: source,
					},
					false, nil,
				)
				Expect(err).ToNot(HaveOccurred())

				Expect(fakeResolver.ResolveCallCount()).To(Equal(1))
				_, repo, tag, _ := fakeResolver.ResolveArgsForCall(0)
				Expect(repo).To(Equal("registry.home/agent-runner"))
				Expect(tag).To(Equal("v1"))
				Expect(imgSpec.ImageURL).To(Equal("docker:///registry.home/agent-runner@sha256:resolved-on-demand"))
				latest, found, latestErr := scope.LatestVersion()
				Expect(latestErr).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(atc.Version(latest.Version())).To(Equal(atc.Version{"digest": "sha256:resolved-on-demand"}))
				Expect(taskDelegateAssociatedCache(fixture, realBuild).Version()).To(Equal(atc.Version{"digest": "sha256:resolved-on-demand"}))
			})

			It("resolves on demand when the pin carries no digest", func() {
				// atc.FetchImagePlan pins a non-nil but *empty* version whenever
				// the ImageResource carries one, which is what
				// agent/resourcecapture produces for a tag-based reference.
				// An empty pin identifies nothing, so it must not short-circuit.
				source := atc.Source{"repository": "registry.home/agent-runner", "tag": "latest"}
				scope := findTaskDelegateScope(fixture, "registry-image", source)
				fakeResolver.ResolveReturns("sha256:tag-resolved", nil)

				td := buildResolvingTaskDelegate()

				imgSpec, err := td.FetchImage(
					context.TODO(),
					atc.ImageResource{
						Name:    "image",
						Type:    "registry-image",
						Source:  source,
						Version: atc.Version{},
					},
					false, nil,
				)
				Expect(err).ToNot(HaveOccurred())

				Expect(fakeResolver.ResolveCallCount()).To(Equal(1))
				Expect(imgSpec.ImageURL).To(Equal("docker:///registry.home/agent-runner@sha256:tag-resolved"))
				latest, found, latestErr := scope.LatestVersion()
				Expect(latestErr).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(atc.Version(latest.Version())).To(Equal(atc.Version{"digest": "sha256:tag-resolved"}))
				Expect(taskDelegateAssociatedCache(fixture, realBuild).Version()).To(Equal(atc.Version{"digest": "sha256:tag-resolved"}))
			})
		})
	})
})
