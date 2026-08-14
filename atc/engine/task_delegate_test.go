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
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/imageresolver"
	"github.com/concourse/concourse/atc/imageresolver/imageresolvertesting"
	"github.com/concourse/concourse/atc/policy"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/runtime/runtimetest"
	"github.com/concourse/concourse/vars"
)

var noopStepper exec.Stepper = func(atc.Plan) exec.Step {
	Fail("cannot create substep")
	return nil
}

type stepFunc func(context.Context, exec.RunState) (bool, error)

func (f stepFunc) Run(ctx context.Context, state exec.RunState) (bool, error) {
	return f(ctx, state)
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

		delegate = NewTaskDelegate(realBuild, planID, state, fakeClock, policy.NoopChecker{}, fixture.WorkerFactory, fixture.LockFactory).(*taskDelegate)

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
		var delegate exec.TaskDelegate

		var expectedCheckPlan, expectedGetPlan atc.Plan
		var types atc.ResourceTypes
		var imageResource atc.ImageResource

		var volume *runtimetest.Volume
		var persistedResourceCache db.ResourceCache

		var runPlans []atc.Plan
		var stepper exec.Stepper

		var tags []string
		var privileged bool

		var imageSpec runtime.ImageSpec
		var fetchErr error

		BeforeEach(func() {
			atc.DefaultCheckInterval = 1 * time.Minute
			volume = runtimetest.NewVolume("some-volume")

			runPlans = nil
			stepper = func(p atc.Plan) exec.Step {
				runPlans = append(runPlans, p)

				return stepFunc(func(_ context.Context, state exec.RunState) (bool, error) {
					if p.Get != nil {
						source := p.Get.Source
						params := p.Get.Params
						if source["some"] == "((source-var))" {
							source = atc.Source{"some": "super-secret-source"}
						}
						if params["some"] == "((params-var))" {
							params = atc.Params{"some": "super-secret-params"}
						}
						version := atc.Version{"some": "version"}
						if p.Get.Version != nil {
							version = *p.Get.Version
						} else if p.Get.Type == "registry-image" {
							version = atc.Version{"digest": "sha256:plan-path"}
						}
						persistedResourceCache = createTaskDelegateCache(
							fixture, realBuild, p.Get.Type, version, source, params,
						)
						state.ArtifactRepository().RegisterArtifact("image", volume, false)
						state.StoreResult(expectedGetPlan.ID, exec.GetResult{
							Name:          "image",
							ResourceCache: persistedResourceCache,
						})
					}
					return true, nil
				})
			}

			runState := exec.NewRunState(stepper, nil)
			delegate = NewTaskDelegate(realBuild, planID, runState, fakeClock, policy.NoopChecker{}, fixture.WorkerFactory, fixture.LockFactory)

			imageResource = atc.ImageResource{
				Type:   "docker",
				Source: atc.Source{"some": "((source-var))"},
				Params: atc.Params{"some": "((params-var))"},
				Tags:   atc.Tags{"some", "tags"},
			}

			types = atc.ResourceTypes{
				{
					Name:   "some-custom-type",
					Type:   "another-custom-type",
					Source: atc.Source{"some-custom": "((source-var))"},
					Params: atc.Params{"some-custom": "((params-var))"},
				},
				{
					Name:       "another-custom-type",
					Type:       "registry-image",
					Source:     atc.Source{"another-custom": "((source-var))"},
					Privileged: true,
				},
			}

			expectedCheckPlan = atc.Plan{
				ID: planID + "/image-check",
				Check: &atc.CheckPlan{
					Name:   "image",
					Type:   "docker",
					Source: atc.Source{"some": "((source-var))"},
					TypeImage: atc.TypeImage{
						BaseType: "docker",
					},
					Tags: atc.Tags{"some", "tags"},
					Interval: atc.CheckEvery{
						Interval: 1 * time.Minute,
					},
				},
			}

			expectedGetPlan = atc.Plan{
				ID: planID + "/image-get",
				Get: &atc.GetPlan{
					Name:   "image",
					Type:   "docker",
					Source: atc.Source{"some": "((source-var))"},
					TypeImage: atc.TypeImage{
						BaseType: "docker",
					},
					VersionFrom: &expectedCheckPlan.ID,
					Params:      atc.Params{"some": "((params-var))"},
					Tags:        atc.Tags{"some", "tags"},
				},
			}
		})

		AfterEach(func() {
			atc.DefaultCheckInterval = 0
		})

		JustBeforeEach(func() {
			imageSpec, fetchErr = delegate.FetchImage(context.TODO(), imageResource, types, privileged, tags, false)
		})

		It("succeeds", func() {
			Expect(fetchErr).ToNot(HaveOccurred())
			Expect(persistedResourceCache).NotTo(BeNil())
			Expect(taskDelegateCacheAssociationCount(fixture, realBuild, persistedResourceCache)).To(Equal(1))
		})

		It("returns an image spec containing the artifact", func() {
			Expect(imageSpec).To(Equal(runtime.ImageSpec{
				ImageArtifact: volume,
				ResourceType:  "image",
				Privileged:    false,
			}))
		})

		It("generates and runs a check and get plan", func() {
			Expect(runPlans).To(Equal([]atc.Plan{
				expectedCheckPlan,
				expectedGetPlan,
			}))
		})

		It("sends events for image check and get", func() {
			e := consumeTaskDelegateBuildEvent(fixture, realBuild, 2, 0)
			Expect(e).To(Equal(event.ImageCheck{
				Time: 675927000,
				Origin: event.Origin{
					ID: event.OriginID(planID),
				},
				PublicPlan: expectedCheckPlan.Public(),
			}))

			e = consumeTaskDelegateBuildEvent(fixture, realBuild, 2, 1)
			Expect(e).To(Equal(event.ImageGet{
				Time: 675927000,
				Origin: event.Origin{
					ID: event.OriginID(planID),
				},
				PublicPlan: expectedGetPlan.Public(),
			}))
		})

		Context("when the check plan is nil", func() {
			BeforeEach(func() {
				imageResource.Version = atc.Version{"some": "version"}
				expectedGetPlan.Get.Version = &atc.Version{"some": "version"}
			})

			It("only saves an ImageGet event", func() {
				e := consumeTaskDelegateBuildEvent(fixture, realBuild, 1, 0)
				Expect(e).To(Equal(event.ImageGet{
					Time: 675927000,
					Origin: event.Origin{
						ID: event.OriginID(planID),
					},
					PublicPlan: expectedGetPlan.Public(),
				}))
			})
		})

		Context("FetchImage event emission", func() {
			BeforeEach(func() {
				imageResource = atc.ImageResource{
					Type:   "registry-image",
					Source: atc.Source{"repository": "my-repo", "tag": "latest"},
					Tags:   atc.Tags{"some", "tags"},
				}

				types = atc.ResourceTypes{}

				expectedCheckPlan = atc.Plan{
					ID: planID + "/image-check",
					Check: &atc.CheckPlan{
						Name:   "image",
						Type:   "registry-image",
						Source: atc.Source{"repository": "my-repo", "tag": "latest"},
						TypeImage: atc.TypeImage{
							BaseType: "registry-image",
						},
						Tags: atc.Tags{"some", "tags"},
						Interval: atc.CheckEvery{
							Interval: 1 * time.Minute,
						},
					},
				}

				expectedGetPlan = atc.Plan{
					ID: planID + "/image-get",
					Get: &atc.GetPlan{
						Name:   "image",
						Type:   "registry-image",
						Source: atc.Source{"repository": "my-repo", "tag": "latest"},
						TypeImage: atc.TypeImage{
							BaseType: "registry-image",
						},
						VersionFrom: &expectedCheckPlan.ID,
						Tags:        atc.Tags{"some", "tags"},
					},
				}

				runState := exec.NewRunState(stepper, nil)
				delegate = NewTaskDelegate(realBuild, planID, runState, fakeClock, policy.NoopChecker{}, fixture.WorkerFactory, fixture.LockFactory)
			})

			It("succeeds", func() {
				Expect(fetchErr).ToNot(HaveOccurred())
			})

			It("runs both plans and returns ImageSpec with artifact and URL", func() {
				Expect(imageSpec.ImageArtifact).ToNot(BeNil())
				Expect(imageSpec.ImageURL).ToNot(BeEmpty())
			})

			It("still saves ImageCheck event for build log continuity", func() {
				e := consumeTaskDelegateBuildEvent(fixture, realBuild, 2, 0)
				Expect(e).To(Equal(event.ImageCheck{
					Time: 675927000,
					Origin: event.Origin{
						ID: event.OriginID(planID),
					},
					PublicPlan: expectedCheckPlan.Public(),
				}))
			})

			It("still saves ImageGet event for build log continuity", func() {
				e := consumeTaskDelegateBuildEvent(fixture, realBuild, 2, 1)
				Expect(e).To(Equal(event.ImageGet{
					Time: 675927000,
					Origin: event.Origin{
						ID: event.OriginID(planID),
					},
					PublicPlan: expectedGetPlan.Public(),
				}))
			})
		})

		Context("integration: end-to-end custom resource type on K8s", func() {
			It("resolves a custom registry-image resource type with digest via FetchImagePlan", func() {
				// Simulate the real pipeline flow:
				// 1. FetchImagePlan generates check+get plans (no static version)
				// 2. Check runs and stores resolved version with digest
				// 3. Get step runs (in production, short-circuits in get_step.go)
				// 4. FetchImage returns ImageSpec with ResourceCache for config chain

				customImage := atc.ImageResource{
					Name:   "image",
					Type:   "registry-image",
					Source: atc.Source{"repository": "my-org/custom-resource", "tag": "2.0"},
				}

				// FetchImagePlan is what the real pipeline planner uses
				getPlan, checkPlan := atc.FetchImagePlan(planID, customImage, atc.ResourceTypes{}, atc.Tags{"k8s"}, false, nil)
				Expect(checkPlan).ToNot(BeNil(), "check plan should be generated when no version is specified")
				Expect(getPlan.Get.Version).To(BeNil(), "get plan should not have a static version")
				Expect(getPlan.Get.VersionFrom).To(Equal(&checkPlan.ID), "get plan should reference check plan for version")

				// Set up a stepper that simulates check storing version AND
				// get step storing a GetResult with ResourceCache (as the real
				// get_step short-circuit does on K8s).
				cache := createTaskDelegateCache(
					fixture,
					realBuild,
					"registry-image",
					atc.Version{"digest": "sha256:e2d4a1f5c8b9"},
					atc.Source{"repository": "my-org/custom-resource", "tag": "2.0"},
					nil,
				)
				var integrationRunPlans []atc.Plan
				integrationStepper := func(p atc.Plan) exec.Step {
					integrationRunPlans = append(integrationRunPlans, p)
					return stepFunc(func(_ context.Context, state exec.RunState) (bool, error) {
						if p.Check != nil {
							state.StoreResult(p.ID, atc.Version{"digest": "sha256:e2d4a1f5c8b9"})
						}
						if p.Get != nil {
							state.ArtifactRepository().RegisterArtifact("image", nil, false)
							state.StoreResult(p.ID, exec.GetResult{
								Name:          "image",
								ResourceCache: cache,
							})
						}
						return true, nil
					})
				}

				integrationState := exec.NewRunState(integrationStepper, nil)
				nativeDelegate := NewTaskDelegate(realBuild, planID, integrationState, fakeClock, policy.NoopChecker{}, fixture.WorkerFactory, fixture.LockFactory)

				imgSpec, fetchErr := nativeDelegate.FetchImage(
					context.TODO(), customImage, atc.ResourceTypes{}, false, atc.Tags{"k8s"}, false,
				)
				Expect(fetchErr).ToNot(HaveOccurred())

				By("running both check and get plans")
				Expect(integrationRunPlans).To(HaveLen(2))
				Expect(integrationRunPlans[0].Check).ToNot(BeNil())
				Expect(integrationRunPlans[1].Get).ToNot(BeNil())

				By("returning an ImageURL pinned to the checked digest")
				Expect(imgSpec.ImageURL).To(Equal("docker:///my-org/custom-resource@sha256:e2d4a1f5c8b9"))

				By("saving both ImageCheck and ImageGet events for build log continuity")
				checkEvent := consumeTaskDelegateBuildEvent(fixture, realBuild, 4, 2)
				Expect(checkEvent.EventType()).To(Equal(atc.EventType("image-check")))
				getEvent := consumeTaskDelegateBuildEvent(fixture, realBuild, 4, 3)
				Expect(getEvent.EventType()).To(Equal(atc.EventType("image-get")))
				Expect(taskDelegateCacheAssociationCount(fixture, realBuild, cache)).To(Equal(1))
			})

			It("resolves a custom resource type with pinned version (no check plan)", func() {
				// When a version is pinned, FetchImagePlan generates no check plan.
				// The get step still runs (and short-circuits in production).
				pinnedImage := atc.ImageResource{
					Name:    "image",
					Type:    "registry-image",
					Source:  atc.Source{"repository": "my-org/pinned-resource"},
					Version: atc.Version{"digest": "sha256:pinned999"},
				}

				getPlan, checkPlan := atc.FetchImagePlan(planID, pinnedImage, atc.ResourceTypes{}, nil, false, nil)
				Expect(checkPlan).To(BeNil(), "no check plan when version is pinned")
				Expect(getPlan.Get.Version).ToNot(BeNil())

				cache := createTaskDelegateCache(
					fixture,
					realBuild,
					"registry-image",
					atc.Version{"digest": "sha256:pinned999"},
					atc.Source{"repository": "my-org/pinned-resource"},
					nil,
				)
				var integrationRunPlans []atc.Plan
				integrationStepper := func(p atc.Plan) exec.Step {
					integrationRunPlans = append(integrationRunPlans, p)
					return stepFunc(func(_ context.Context, state exec.RunState) (bool, error) {
						if p.Get != nil {
							state.ArtifactRepository().RegisterArtifact("image", nil, false)
							state.StoreResult(p.ID, exec.GetResult{
								Name:          "image",
								ResourceCache: cache,
							})
						}
						return true, nil
					})
				}

				integrationState := exec.NewRunState(integrationStepper, nil)
				nativeDelegate := NewTaskDelegate(realBuild, planID, integrationState, fakeClock, policy.NoopChecker{}, fixture.WorkerFactory, fixture.LockFactory)

				imgSpec, fetchErr := nativeDelegate.FetchImage(
					context.TODO(), pinnedImage, atc.ResourceTypes{}, false, nil, false,
				)
				Expect(fetchErr).ToNot(HaveOccurred())

				By("running the get plan (no check needed)")
				Expect(integrationRunPlans).To(HaveLen(1))
				Expect(integrationRunPlans[0].Get).ToNot(BeNil())

				By("returning an ImageURL with the pinned digest")
				Expect(imgSpec.ImageURL).To(Equal("docker:///my-org/pinned-resource@sha256:pinned999"))
				Expect(taskDelegateCacheAssociationCount(fixture, realBuild, cache)).To(Equal(1))
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
				policyChecker:         policy.NoopChecker{},
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
				return stepFunc(func(_ context.Context, s exec.RunState) (bool, error) {
					return true, nil
				})
			}

			td, executedPlans := buildTaskDelegate(noopStepper)

			imgSpec, err := td.FetchImage(
				context.TODO(),
				atc.ImageResource{Name: "image", Type: "registry-image", Source: source},
				atc.ResourceTypes{},
				false, nil, false,
			)
			Expect(err).ToNot(HaveOccurred())

			Expect(*executedPlans).To(BeEmpty(), "no check+get pods should be spawned")
			Expect(imgSpec.ImageURL).To(Equal("docker:///my-org/my-image@sha256:metadata42"))
			Expect(imgSpec.ImageArtifact).To(BeNil(), "no volume artifact expected")
			cache := taskDelegateAssociatedCache(fixture, realBuild)
			Expect(cache.Version()).To(Equal(atc.Version{"digest": "sha256:metadata42"}))
		})

		It("falls back to check+get plans when no cached version exists", func() {
			source := atc.Source{"repository": "my-org/uncached"}
			scope := findTaskDelegateScope(fixture, "registry-image", source)
			latest, found, err := scope.LatestVersion()
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
			Expect(latest).To(BeNil())

			fallbackCache := createTaskDelegateCache(
				fixture, realBuild, "registry-image",
				atc.Version{"digest": "sha256:fallback123"}, source, nil,
			)

			fallbackStepper := func(p atc.Plan) exec.Step {
				return stepFunc(func(_ context.Context, s exec.RunState) (bool, error) {
					if p.Check != nil {
						s.StoreResult(p.ID, atc.Version{"digest": "sha256:fallback123"})
					}
					if p.Get != nil {
						vol := runtimetest.NewVolume("fallback-vol")
						s.ArtifactRepository().RegisterArtifact("image", vol, false)
						s.StoreResult(p.ID, exec.GetResult{
							Name:          "image",
							ResourceCache: fallbackCache,
						})
					}
					return true, nil
				})
			}

			td, executedPlans := buildTaskDelegate(fallbackStepper)

			imgSpec, err := td.FetchImage(
				context.TODO(),
				atc.ImageResource{Name: "image", Type: "registry-image", Source: source},
				atc.ResourceTypes{},
				false, nil, false,
			)
			Expect(err).ToNot(HaveOccurred())

			Expect(*executedPlans).To(HaveLen(2), "should spawn check+get plans as fallback")
			Expect(imgSpec.ImageURL).To(Equal("docker:///my-org/uncached@sha256:fallback123"))
			Expect(imgSpec.ImageArtifact).ToNot(BeNil(), "fallback should produce an artifact")
			Expect(taskDelegateCacheAssociationCount(fixture, realBuild, fallbackCache)).To(Equal(1))
		})

		It("transitions from fallback to cached resolution across runs", func() {
			source := atc.Source{"repository": "my-org/evolving"}
			scope := findTaskDelegateScope(fixture, "registry-image", source)
			fallbackCache := createTaskDelegateCache(
				fixture, realBuild, "registry-image", atc.Version{"digest": "sha256:v1"}, source, nil,
			)

			fallbackStepper := func(p atc.Plan) exec.Step {
				return stepFunc(func(_ context.Context, s exec.RunState) (bool, error) {
					if p.Check != nil {
						s.StoreResult(p.ID, atc.Version{"digest": "sha256:v1"})
					}
					if p.Get != nil {
						vol := runtimetest.NewVolume("v1-vol")
						s.ArtifactRepository().RegisterArtifact("image", vol, false)
						s.StoreResult(p.ID, exec.GetResult{Name: "image", ResourceCache: fallbackCache})
					}
					return true, nil
				})
			}

			td1, plans1 := buildTaskDelegate(fallbackStepper)
			spec1, err := td1.FetchImage(
				context.TODO(),
				atc.ImageResource{Name: "image", Type: "registry-image", Source: source},
				atc.ResourceTypes{}, false, nil, false,
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(*plans1).To(HaveLen(2), "first run falls back to plans")
			Expect(spec1.ImageURL).To(Equal("docker:///my-org/evolving@sha256:v1"))
			Expect(taskDelegateCacheAssociationCount(fixture, realBuild, fallbackCache)).To(Equal(1))

			Expect(scope.SaveVersions(
				db.SpanContext{}, []atc.Version{{"digest": "sha256:v2-cached"}},
			)).To(Succeed())

			noopStepper := func(p atc.Plan) exec.Step {
				return stepFunc(func(_ context.Context, s exec.RunState) (bool, error) { return true, nil })
			}

			td2, plans2 := buildTaskDelegate(noopStepper)
			spec2, err := td2.FetchImage(
				context.TODO(),
				atc.ImageResource{Name: "image", Type: "registry-image", Source: source},
				atc.ResourceTypes{}, false, nil, false,
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(*plans2).To(BeEmpty(), "second run uses cached version, no pods")
			Expect(spec2.ImageURL).To(Equal("docker:///my-org/evolving@sha256:v2-cached"))
			cached := createTaskDelegateCache(
				fixture, realBuild, "registry-image", atc.Version{"digest": "sha256:v2-cached"}, source, nil,
			)
			Expect(taskDelegateCacheAssociationCount(fixture, realBuild, cached)).To(Equal(1))
		})

		It("saves image resource version for build tracking", func() {
			source := atc.Source{"repository": "my-org/tracked"}
			saveTaskDelegateVersion(fixture, "registry-image", source, atc.Version{"digest": "sha256:metadata42"})

			noopStepper := func(p atc.Plan) exec.Step {
				return stepFunc(func(_ context.Context, s exec.RunState) (bool, error) { return true, nil })
			}

			td, _ := buildTaskDelegate(noopStepper)

			_, err := td.FetchImage(
				context.TODO(),
				atc.ImageResource{Name: "image", Type: "registry-image", Source: source},
				atc.ResourceTypes{}, false, nil, false,
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
				return stepFunc(func(_ context.Context, s exec.RunState) (bool, error) { return true, nil })
			}

			td, _ := buildTaskDelegate(noopStepper)

			imgSpec, err := td.FetchImage(
				context.TODO(),
				atc.ImageResource{Name: "image", Type: "registry-image", Source: source},
				atc.ResourceTypes{}, false, nil, false,
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(imgSpec.ImageURL).To(HavePrefix("docker:///"))
			Expect(imgSpec.ImageURL).To(ContainSubstring("@sha256:"))
			Expect(imgSpec.ImageURL).To(ContainSubstring("gcr.io/my-project/worker-image"))
		})

		It("emits ImageCheck and ImageGet events even when using cached resolution", func() {
			source := atc.Source{"repository": "my-org/events-test"}
			saveTaskDelegateVersion(fixture, "registry-image", source, atc.Version{"digest": "sha256:metadata42"})

			noopStepper := func(p atc.Plan) exec.Step {
				return stepFunc(func(_ context.Context, s exec.RunState) (bool, error) { return true, nil })
			}

			td, _ := buildTaskDelegate(noopStepper)

			_, err := td.FetchImage(
				context.TODO(),
				atc.ImageResource{Name: "image", Type: "registry-image", Source: source},
				atc.ResourceTypes{}, false, nil, false,
			)
			Expect(err).ToNot(HaveOccurred())

			Expect(consumeTaskDelegateBuildEvent(fixture, realBuild, 2, 0).EventType()).To(Equal(atc.EventType("image-check")))
			Expect(consumeTaskDelegateBuildEvent(fixture, realBuild, 2, 1).EventType()).To(Equal(atc.EventType("image-get")))
		})

		It("falls back to plans for a non-registry-image type without image: field", func() {
			source := atc.Source{"bucket": "my-bucket"}
			fallbackCache := createTaskDelegateCache(
				fixture, realBuild, "s3-resource", atc.Version{"digest": "sha256:custom123"}, source, nil,
			)

			fallbackStepper := func(p atc.Plan) exec.Step {
				return stepFunc(func(_ context.Context, s exec.RunState) (bool, error) {
					if p.Check != nil {
						s.StoreResult(p.ID, atc.Version{"digest": "sha256:custom123"})
					}
					if p.Get != nil {
						vol := runtimetest.NewVolume("custom-vol")
						s.ArtifactRepository().RegisterArtifact("image", vol, false)
						s.StoreResult(p.ID, exec.GetResult{
							Name:          "image",
							ResourceCache: fallbackCache,
						})
					}
					return true, nil
				})
			}

			td, executedPlans := buildTaskDelegate(fallbackStepper)

			imgSpec, err := td.FetchImage(
				context.TODO(),
				atc.ImageResource{Name: "image", Type: "s3-resource", Source: source},
				atc.ResourceTypes{},
				false, nil, false,
			)
			Expect(err).ToNot(HaveOccurred())

			Expect(*executedPlans).To(HaveLen(2), "non-registry type must spawn check+get pods")
			Expect(imgSpec.ImageArtifact).ToNot(BeNil(), "plan-based path returns artifact")
			Expect(taskDelegateCacheAssociationCount(fixture, realBuild, fallbackCache)).To(Equal(1))
		})

		It("falls back gracefully when DB metadata lookup fails", func() {
			source := atc.Source{"repository": "my-org/db-fail-test"}
			fallbackCache := createTaskDelegateCache(
				fixture, realBuild, "registry-image", atc.Version{"digest": "sha256:dbfail"}, source, nil,
			)
			resourceConfigFactory = db.NewResourceConfigFactory(ClosedEngineCloneConn(), fixture.LockFactory)
			imageResolver = nil

			fallbackStepper := func(p atc.Plan) exec.Step {
				return stepFunc(func(_ context.Context, s exec.RunState) (bool, error) {
					if p.Check != nil {
						s.StoreResult(p.ID, atc.Version{"digest": "sha256:dbfail"})
					}
					if p.Get != nil {
						vol := runtimetest.NewVolume("dbfail-vol")
						s.ArtifactRepository().RegisterArtifact("image", vol, false)
						s.StoreResult(p.ID, exec.GetResult{
							Name:          "image",
							ResourceCache: fallbackCache,
						})
					}
					return true, nil
				})
			}

			td, executedPlans := buildTaskDelegate(fallbackStepper)

			imgSpec, err := td.FetchImage(
				context.TODO(),
				atc.ImageResource{Name: "image", Type: "registry-image", Source: source},
				atc.ResourceTypes{}, false, nil, false,
			)
			Expect(err).ToNot(HaveOccurred())

			Expect(*executedPlans).To(HaveLen(2), "DB failure should trigger plan-based fallback")
			Expect(imgSpec.ImageURL).To(Equal("docker:///my-org/db-fail-test@sha256:dbfail"))
			Expect(imgSpec.ImageArtifact).ToNot(BeNil())
			Expect(taskDelegateCacheAssociationCount(fixture, realBuild, fallbackCache)).To(Equal(1))
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
				atc.ResourceTypes{}, false, nil, false,
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
				atc.ResourceTypes{}, false, nil, false,
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
				atc.ResourceTypes{}, false, nil, false,
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

		It("works without resource factories (fallback-only mode)", func() {
			source := atc.Source{"repository": "my-org/no-factory"}
			fallbackCache := createTaskDelegateCache(
				fixture, realBuild, "registry-image", atc.Version{"digest": "sha256:nofactory"}, source, nil,
			)

			fallbackStepper := func(p atc.Plan) exec.Step {
				return stepFunc(func(_ context.Context, s exec.RunState) (bool, error) {
					if p.Check != nil {
						s.StoreResult(p.ID, atc.Version{"digest": "sha256:nofactory"})
					}
					if p.Get != nil {
						vol := runtimetest.NewVolume("nofactory-vol")
						s.ArtifactRepository().RegisterArtifact("image", vol, false)
						s.StoreResult(p.ID, exec.GetResult{
							Name:          "image",
							ResourceCache: fallbackCache,
						})
					}
					return true, nil
				})
			}

			resourceConfigFactory = nil
			resourceCacheFactory = nil
			td, executedPlans := buildTaskDelegate(fallbackStepper)

			imgSpec, err := td.FetchImage(
				context.TODO(),
				atc.ImageResource{Name: "image", Type: "registry-image", Source: source},
				atc.ResourceTypes{}, false, nil, false,
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(*executedPlans).To(HaveLen(2))
			Expect(imgSpec.ImageURL).To(Equal("docker:///my-org/no-factory@sha256:nofactory"))
			Expect(imgSpec.ImageArtifact).ToNot(BeNil(), "without factories, always uses plan-based path")
			Expect(taskDelegateCacheAssociationCount(fixture, realBuild, fallbackCache)).To(Equal(1))
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
					policyChecker:         policy.NoopChecker{},
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
					atc.ResourceTypes{}, false, nil, false,
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
					atc.ResourceTypes{}, false, nil, false,
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
					atc.ResourceTypes{}, false, nil, false,
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
					atc.ResourceTypes{}, false, nil, false,
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
					atc.ResourceTypes{}, false, nil, false,
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
					atc.ResourceTypes{}, false, nil, false,
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
					atc.ResourceTypes{}, false, nil, false,
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
