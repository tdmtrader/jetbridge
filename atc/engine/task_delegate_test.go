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
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/db/lock/lockfakes"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/exec/execfakes"
	"github.com/concourse/concourse/atc/imageresolver/imageresolvertesting"
	"github.com/concourse/concourse/atc/policy/policyfakes"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/runtime/runtimetest"
	"github.com/concourse/concourse/vars"
)

var noopStepper exec.Stepper = func(atc.Plan) exec.Step {
	Fail("cannot create substep")
	return nil
}

var _ = Describe("TaskDelegate", func() {
	var (
		logger            *lagertest.TestLogger
		fakeBuild         *dbfakes.FakeBuild
		fakeClock         *fakeclock.FakeClock
		fakePolicyChecker *policyfakes.FakeChecker
		fakeWorkerFactory *dbfakes.FakeWorkerFactory
		fakeLockFactory   *lockfakes.FakeLockFactory

		state exec.RunState

		now = time.Date(1991, 6, 3, 5, 30, 0, 0, time.UTC)

		delegate *taskDelegate
		planID   = atc.PlanID("some-plan-id")

		exitStatus exec.ExitStatus
	)

	BeforeEach(func() {
		logger = lagertest.NewTestLogger("test")

		fakeBuild = new(dbfakes.FakeBuild)
		fakeClock = fakeclock.NewFakeClock(now)
		credVars := vars.StaticVariables{
			"source-param": "super-secret-source",
			"git-key":      "{\n123\n456\n789\n}\n",
		}
		state = exec.NewRunState(noopStepper, credVars)

		fakePolicyChecker = new(policyfakes.FakeChecker)
		fakeWorkerFactory = new(dbfakes.FakeWorkerFactory)
		fakeLockFactory = new(lockfakes.FakeLockFactory)

		delegate = NewTaskDelegate(fakeBuild, planID, state, fakeClock, fakePolicyChecker, fakeWorkerFactory, fakeLockFactory).(*taskDelegate)

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
			Expect(fakeBuild.SaveEventCallCount()).To(Equal(1))
			event := fakeBuild.SaveEventArgsForCall(0)
			Expect(event.EventType()).To(Equal(atc.EventType("initialize-task")))
		})

		It("calls SaveEvent with the taskConfig", func() {
			Expect(fakeBuild.SaveEventCallCount()).To(Equal(1))
			event := fakeBuild.SaveEventArgsForCall(0)
			Expect(json.Marshal(event)).To(MatchJSON(`{
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
			Expect(fakeBuild.SaveEventCallCount()).To(Equal(1))
			event := fakeBuild.SaveEventArgsForCall(0)
			Expect(event.EventType()).To(Equal(atc.EventType("start-task")))
		})

		It("calls SaveEvent with the taskConfig", func() {
			Expect(fakeBuild.SaveEventCallCount()).To(Equal(1))
			event := fakeBuild.SaveEventArgsForCall(0)
			Expect(json.Marshal(event)).To(MatchJSON(`{
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
			Expect(fakeBuild.SaveEventCallCount()).To(Equal(1))
			event := fakeBuild.SaveEventArgsForCall(0)
			Expect(event.EventType()).To(Equal(atc.EventType("finish-task")))
		})
	})

	Describe("SidecarWriter", func() {
		It("returns a non-nil writer", func() {
			w := delegate.SidecarWriter("postgres")
			Expect(w).ToNot(BeNil())
		})

		It("writes produce Log events with the sidecar plan ID as origin", func() {
			w := delegate.SidecarWriter("postgres")
			_, writeErr := w.Write([]byte("starting postgres"))
			Expect(writeErr).ToNot(HaveOccurred())

			// Close the writer to flush
			w.(io.Closer).Close()

			Expect(fakeBuild.SaveEventCallCount()).To(BeNumerically(">=", 1))
			ev := fakeBuild.SaveEventArgsForCall(0)
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
			Expect(fakeBuild.SaveEventCallCount()).To(Equal(2))

			ev0 := fakeBuild.SaveEventArgsForCall(0)
			Expect(ev0.EventType()).To(Equal(atc.EventType("sidecar")))

			ev1 := fakeBuild.SaveEventArgsForCall(1)
			Expect(ev1.EventType()).To(Equal(atc.EventType("sidecar")))
		})

		It("emits events with the parent plan ID as origin", func() {
			Expect(fakeBuild.SaveEventCallCount()).To(Equal(2))

			ev0 := fakeBuild.SaveEventArgsForCall(0)
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

			ev1 := fakeBuild.SaveEventArgsForCall(1)
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
				Expect(fakeBuild.SaveEventCallCount()).To(Equal(0))
			})
		})
	})

	Describe("FetchImage", func() {
		var delegate exec.TaskDelegate

		var expectedCheckPlan, expectedGetPlan atc.Plan
		var types atc.ResourceTypes
		var imageResource atc.ImageResource

		var volume *runtimetest.Volume
		var fakeResourceCache *dbfakes.FakeResourceCache

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

				step := new(execfakes.FakeStep)
				fakeResourceCache = new(dbfakes.FakeResourceCache)
				step.RunStub = func(_ context.Context, state exec.RunState) (bool, error) {
					if p.Get != nil {
						state.ArtifactRepository().RegisterArtifact("image", volume, false)
						state.StoreResult(expectedGetPlan.ID, exec.GetResult{
							Name:          "image",
							ResourceCache: fakeResourceCache,
						})
					}
					return true, nil
				}
				return step
			}

			runState := exec.NewRunState(stepper, nil)
			delegate = NewTaskDelegate(fakeBuild, planID, runState, fakeClock, fakePolicyChecker, fakeWorkerFactory, fakeLockFactory)

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
			Expect(fakeBuild.SaveEventCallCount()).To(Equal(2))
			e := fakeBuild.SaveEventArgsForCall(0)
			Expect(e).To(Equal(event.ImageCheck{
				Time: 675927000,
				Origin: event.Origin{
					ID: event.OriginID(planID),
				},
				PublicPlan: expectedCheckPlan.Public(),
			}))

			e = fakeBuild.SaveEventArgsForCall(1)
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
				Expect(fakeBuild.SaveEventCallCount()).To(Equal(1))
				e := fakeBuild.SaveEventArgsForCall(0)
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
				delegate = NewTaskDelegate(fakeBuild, planID, runState, fakeClock, fakePolicyChecker, fakeWorkerFactory, fakeLockFactory)
			})

			It("succeeds", func() {
				Expect(fetchErr).ToNot(HaveOccurred())
			})

			It("runs both plans and returns ImageSpec with artifact and URL", func() {
				Expect(imageSpec.ImageArtifact).ToNot(BeNil())
				Expect(imageSpec.ImageURL).ToNot(BeEmpty())
			})

			It("still saves ImageCheck event for build log continuity", func() {
				Expect(fakeBuild.SaveEventCallCount()).To(BeNumerically(">=", 1))
				e := fakeBuild.SaveEventArgsForCall(0)
				Expect(e).To(Equal(event.ImageCheck{
					Time: 675927000,
					Origin: event.Origin{
						ID: event.OriginID(planID),
					},
					PublicPlan: expectedCheckPlan.Public(),
				}))
			})

			It("still saves ImageGet event for build log continuity", func() {
				Expect(fakeBuild.SaveEventCallCount()).To(BeNumerically(">=", 2))
				e := fakeBuild.SaveEventArgsForCall(1)
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
				fakeCache := new(dbfakes.FakeResourceCache)
				var integrationRunPlans []atc.Plan
				integrationStepper := func(p atc.Plan) exec.Step {
					integrationRunPlans = append(integrationRunPlans, p)
					step := new(execfakes.FakeStep)
					step.RunStub = func(_ context.Context, state exec.RunState) (bool, error) {
						if p.Check != nil {
							state.StoreResult(p.ID, atc.Version{"digest": "sha256:e2d4a1f5c8b9"})
						}
						if p.Get != nil {
							fakeCache.VersionReturns(atc.Version{"digest": "sha256:e2d4a1f5c8b9"})
							state.ArtifactRepository().RegisterArtifact("image", nil, false)
							state.StoreResult(p.ID, exec.GetResult{
								Name:          "image",
								ResourceCache: fakeCache,
							})
						}
						return true, nil
					}
					return step
				}

				integrationState := exec.NewRunState(integrationStepper, nil)
				nativeDelegate := NewTaskDelegate(fakeBuild, planID, integrationState, fakeClock, fakePolicyChecker, fakeWorkerFactory, fakeLockFactory)

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
				Expect(fakeBuild.SaveEventCallCount()).To(BeNumerically(">=", 2))
				checkEvent := fakeBuild.SaveEventArgsForCall(fakeBuild.SaveEventCallCount() - 2)
				Expect(checkEvent.EventType()).To(Equal(atc.EventType("image-check")))
				getEvent := fakeBuild.SaveEventArgsForCall(fakeBuild.SaveEventCallCount() - 1)
				Expect(getEvent.EventType()).To(Equal(atc.EventType("image-get")))
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

				fakeCache := new(dbfakes.FakeResourceCache)
				var integrationRunPlans []atc.Plan
				integrationStepper := func(p atc.Plan) exec.Step {
					integrationRunPlans = append(integrationRunPlans, p)
					step := new(execfakes.FakeStep)
					step.RunStub = func(_ context.Context, state exec.RunState) (bool, error) {
						if p.Get != nil {
							fakeCache.VersionReturns(atc.Version{"digest": "sha256:pinned999"})
							state.ArtifactRepository().RegisterArtifact("image", nil, false)
							state.StoreResult(p.ID, exec.GetResult{
								Name:          "image",
								ResourceCache: fakeCache,
							})
						}
						return true, nil
					}
					return step
				}

				integrationState := exec.NewRunState(integrationStepper, nil)
				nativeDelegate := NewTaskDelegate(fakeBuild, planID, integrationState, fakeClock, fakePolicyChecker, fakeWorkerFactory, fakeLockFactory)

				imgSpec, fetchErr := nativeDelegate.FetchImage(
					context.TODO(), pinnedImage, atc.ResourceTypes{}, false, nil, false,
				)
				Expect(fetchErr).ToNot(HaveOccurred())

				By("running the get plan (no check needed)")
				Expect(integrationRunPlans).To(HaveLen(1))
				Expect(integrationRunPlans[0].Get).ToNot(BeNil())

				By("returning an ImageURL with the pinned digest")
				Expect(imgSpec.ImageURL).To(Equal("docker:///my-org/pinned-resource@sha256:pinned999"))
			})
		})
	})

	Describe("integration: FetchImage via DelegateFactory", func() {
		// These tests exercise the full production code path:
		// DelegateFactory.TaskDelegate() → configureDelegate() → FetchImage()
		// No internal type assertions or direct struct manipulation.

		var (
			fakeResourceConfigFactory *dbfakes.FakeResourceConfigFactory
			fakeResourceCacheFactory  *dbfakes.FakeResourceCacheFactory
			fakeResourceConfig        *dbfakes.FakeResourceConfig
			fakeScope                 *dbfakes.FakeResourceConfigScope
			fakeVersion               *dbfakes.FakeResourceConfigVersion
			fakeMetadataCache         *dbfakes.FakeResourceCache

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
				build:                 fakeBuild,
				plan:                  plan,
				policyChecker:         fakePolicyChecker,
				dbWorkerFactory:       fakeWorkerFactory,
				lockFactory:           fakeLockFactory,
				resourceConfigFactory: fakeResourceConfigFactory,
				resourceCacheFactory:  fakeResourceCacheFactory,
			}

			td := delegateFactory.TaskDelegate(state)
			return td, &executedPlans
		}

		BeforeEach(func() {
			fakeResourceConfigFactory = new(dbfakes.FakeResourceConfigFactory)
			fakeResourceCacheFactory = new(dbfakes.FakeResourceCacheFactory)
			fakeResourceConfig = new(dbfakes.FakeResourceConfig)
			fakeScope = new(dbfakes.FakeResourceConfigScope)
			fakeVersion = new(dbfakes.FakeResourceConfigVersion)
			fakeMetadataCache = new(dbfakes.FakeResourceCache)

			fakeResourceConfigFactory.FindOrCreateResourceConfigReturns(fakeResourceConfig, nil)
			fakeResourceConfig.FindOrCreateScopeReturns(fakeScope, nil)
			fakeScope.LatestVersionReturns(fakeVersion, true, nil)
			fakeVersion.VersionReturns(db.Version{"digest": "sha256:metadata42"})

			fakeResourceCacheFactory.FindOrCreateResourceCacheReturns(fakeMetadataCache, nil)
			fakeMetadataCache.IDReturns(999)

			fakeBuild.IDReturns(42)
		})

		It("resolves a registry-image type without spawning extra pods when the version is cached", func() {
			noopStepper := func(p atc.Plan) exec.Step {
				step := new(execfakes.FakeStep)
				step.RunStub = func(_ context.Context, s exec.RunState) (bool, error) {
					return true, nil
				}
				return step
			}

			td, executedPlans := buildTaskDelegate(noopStepper)

			imgSpec, err := td.FetchImage(
				context.TODO(),
				atc.ImageResource{Name: "image", Type: "registry-image", Source: atc.Source{"repository": "my-org/my-image"}},
				atc.ResourceTypes{},
				false, nil, false,
			)
			Expect(err).ToNot(HaveOccurred())

			Expect(*executedPlans).To(BeEmpty(), "no check+get pods should be spawned")
			Expect(imgSpec.ImageURL).To(Equal("docker:///my-org/my-image@sha256:metadata42"))
			Expect(imgSpec.ImageArtifact).To(BeNil(), "no volume artifact expected")
		})

		It("falls back to check+get plans when no cached version exists", func() {
			fakeScope.LatestVersionReturns(nil, false, nil)

			fallbackCache := new(dbfakes.FakeResourceCache)
			fallbackCache.IDReturns(111)
			fallbackCache.VersionReturns(atc.Version{"digest": "sha256:fallback123"})

			fallbackStepper := func(p atc.Plan) exec.Step {
				step := new(execfakes.FakeStep)
				step.RunStub = func(_ context.Context, s exec.RunState) (bool, error) {
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
				}
				return step
			}

			td, executedPlans := buildTaskDelegate(fallbackStepper)

			imgSpec, err := td.FetchImage(
				context.TODO(),
				atc.ImageResource{Name: "image", Type: "registry-image", Source: atc.Source{"repository": "my-org/uncached"}},
				atc.ResourceTypes{},
				false, nil, false,
			)
			Expect(err).ToNot(HaveOccurred())

			Expect(*executedPlans).To(HaveLen(2), "should spawn check+get plans as fallback")
			Expect(imgSpec.ImageURL).To(Equal("docker:///my-org/uncached@sha256:fallback123"))
			Expect(imgSpec.ImageArtifact).ToNot(BeNil(), "fallback should produce an artifact")
		})

		It("transitions from fallback to cached resolution across runs", func() {
			// First run: empty cache
			fakeScope.LatestVersionReturns(nil, false, nil)

			fallbackCache := new(dbfakes.FakeResourceCache)
			fallbackCache.IDReturns(111)
			fallbackCache.VersionReturns(atc.Version{"digest": "sha256:v1"})

			fallbackStepper := func(p atc.Plan) exec.Step {
				step := new(execfakes.FakeStep)
				step.RunStub = func(_ context.Context, s exec.RunState) (bool, error) {
					if p.Check != nil {
						s.StoreResult(p.ID, atc.Version{"digest": "sha256:v1"})
					}
					if p.Get != nil {
						vol := runtimetest.NewVolume("v1-vol")
						s.ArtifactRepository().RegisterArtifact("image", vol, false)
						s.StoreResult(p.ID, exec.GetResult{Name: "image", ResourceCache: fallbackCache})
					}
					return true, nil
				}
				return step
			}

			td1, plans1 := buildTaskDelegate(fallbackStepper)
			spec1, err := td1.FetchImage(
				context.TODO(),
				atc.ImageResource{Name: "image", Type: "registry-image", Source: atc.Source{"repository": "my-org/evolving"}},
				atc.ResourceTypes{}, false, nil, false,
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(*plans1).To(HaveLen(2), "first run falls back to plans")
			Expect(spec1.ImageURL).To(Equal("docker:///my-org/evolving@sha256:v1"))

			// Second run: lidar has now cached a newer version
			fakeScope.LatestVersionReturns(fakeVersion, true, nil)
			fakeVersion.VersionReturns(db.Version{"digest": "sha256:v2-cached"})

			noopStepper := func(p atc.Plan) exec.Step {
				step := new(execfakes.FakeStep)
				step.RunStub = func(_ context.Context, s exec.RunState) (bool, error) { return true, nil }
				return step
			}

			td2, plans2 := buildTaskDelegate(noopStepper)
			spec2, err := td2.FetchImage(
				context.TODO(),
				atc.ImageResource{Name: "image", Type: "registry-image", Source: atc.Source{"repository": "my-org/evolving"}},
				atc.ResourceTypes{}, false, nil, false,
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(*plans2).To(BeEmpty(), "second run uses cached version, no pods")
			Expect(spec2.ImageURL).To(Equal("docker:///my-org/evolving@sha256:v2-cached"))
		})

		It("saves image resource version for build tracking", func() {
			noopStepper := func(p atc.Plan) exec.Step {
				step := new(execfakes.FakeStep)
				step.RunStub = func(_ context.Context, s exec.RunState) (bool, error) { return true, nil }
				return step
			}

			td, _ := buildTaskDelegate(noopStepper)

			_, err := td.FetchImage(
				context.TODO(),
				atc.ImageResource{Name: "image", Type: "registry-image", Source: atc.Source{"repository": "my-org/tracked"}},
				atc.ResourceTypes{}, false, nil, false,
			)
			Expect(err).ToNot(HaveOccurred())

			Expect(fakeBuild.SaveImageResourceVersionCallCount()).To(Equal(1))
			savedCache := fakeBuild.SaveImageResourceVersionArgsForCall(0)
			Expect(savedCache.ID()).To(Equal(999))
		})

		It("produces a valid docker:// URL with digest for all registry-image resolutions", func() {
			noopStepper := func(p atc.Plan) exec.Step {
				step := new(execfakes.FakeStep)
				step.RunStub = func(_ context.Context, s exec.RunState) (bool, error) { return true, nil }
				return step
			}

			td, _ := buildTaskDelegate(noopStepper)

			imgSpec, err := td.FetchImage(
				context.TODO(),
				atc.ImageResource{Name: "image", Type: "registry-image", Source: atc.Source{"repository": "gcr.io/my-project/worker-image"}},
				atc.ResourceTypes{}, false, nil, false,
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(imgSpec.ImageURL).To(HavePrefix("docker:///"))
			Expect(imgSpec.ImageURL).To(ContainSubstring("@sha256:"))
			Expect(imgSpec.ImageURL).To(ContainSubstring("gcr.io/my-project/worker-image"))
		})

		It("emits ImageCheck and ImageGet events even when using cached resolution", func() {
			noopStepper := func(p atc.Plan) exec.Step {
				step := new(execfakes.FakeStep)
				step.RunStub = func(_ context.Context, s exec.RunState) (bool, error) { return true, nil }
				return step
			}

			td, _ := buildTaskDelegate(noopStepper)

			_, err := td.FetchImage(
				context.TODO(),
				atc.ImageResource{Name: "image", Type: "registry-image", Source: atc.Source{"repository": "my-org/events-test"}},
				atc.ResourceTypes{}, false, nil, false,
			)
			Expect(err).ToNot(HaveOccurred())

			// TaskDelegate.FetchImage always saves ImageCheck and ImageGet events
			// for build log continuity, regardless of whether plans actually run.
			Expect(fakeBuild.SaveEventCallCount()).To(BeNumerically(">=", 1))
			var eventTypes []atc.EventType
			for i := 0; i < fakeBuild.SaveEventCallCount(); i++ {
				eventTypes = append(eventTypes, fakeBuild.SaveEventArgsForCall(i).EventType())
			}
			Expect(eventTypes).To(ContainElement(atc.EventType("image-get")))
		})

		It("falls back to plans for a non-registry-image type without image: field", func() {
			fallbackCache := new(dbfakes.FakeResourceCache)
			fallbackCache.IDReturns(222)
			fallbackCache.VersionReturns(atc.Version{"digest": "sha256:custom123"})

			fallbackStepper := func(p atc.Plan) exec.Step {
				step := new(execfakes.FakeStep)
				step.RunStub = func(_ context.Context, s exec.RunState) (bool, error) {
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
				}
				return step
			}

			td, executedPlans := buildTaskDelegate(fallbackStepper)

			imgSpec, err := td.FetchImage(
				context.TODO(),
				atc.ImageResource{Name: "image", Type: "s3-resource", Source: atc.Source{"bucket": "my-bucket"}},
				atc.ResourceTypes{},
				false, nil, false,
			)
			Expect(err).ToNot(HaveOccurred())

			Expect(*executedPlans).To(HaveLen(2), "non-registry type must spawn check+get pods")
			Expect(imgSpec.ImageArtifact).ToNot(BeNil(), "plan-based path returns artifact")
		})

		It("falls back gracefully when DB metadata lookup fails", func() {
			fakeResourceConfigFactory.FindOrCreateResourceConfigReturns(nil, fmt.Errorf("db connection lost"))

			fallbackCache := new(dbfakes.FakeResourceCache)
			fallbackCache.IDReturns(333)
			fallbackCache.VersionReturns(atc.Version{"digest": "sha256:dbfail"})

			fallbackStepper := func(p atc.Plan) exec.Step {
				step := new(execfakes.FakeStep)
				step.RunStub = func(_ context.Context, s exec.RunState) (bool, error) {
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
				}
				return step
			}

			td, executedPlans := buildTaskDelegate(fallbackStepper)

			imgSpec, err := td.FetchImage(
				context.TODO(),
				atc.ImageResource{Name: "image", Type: "registry-image", Source: atc.Source{"repository": "my-org/db-fail-test"}},
				atc.ResourceTypes{}, false, nil, false,
			)
			Expect(err).ToNot(HaveOccurred())

			Expect(*executedPlans).To(HaveLen(2), "DB failure should trigger plan-based fallback")
			Expect(imgSpec.ImageURL).To(Equal("docker:///my-org/db-fail-test@sha256:dbfail"))
			Expect(imgSpec.ImageArtifact).ToNot(BeNil())
		})

		It("does not spawn any pods when resource factories are injected and cache is warm", func() {
			// This is the key optimization: with warm cache, zero pods for type images.
			noopStepper := func(p atc.Plan) exec.Step {
				Fail("no steps should be created when cache is warm")
				return nil
			}

			td, _ := buildTaskDelegate(noopStepper)

			imgSpec, err := td.FetchImage(
				context.TODO(),
				atc.ImageResource{Name: "image", Type: "registry-image", Source: atc.Source{"repository": "my-org/warm-cache"}},
				atc.ResourceTypes{}, false, nil, false,
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(imgSpec.ImageURL).To(Equal("docker:///my-org/warm-cache@sha256:metadata42"))
		})

		It("resolves on-demand via resolver when no cached version exists", func() {
			fakeScope.LatestVersionReturns(nil, false, nil)

			fakeResolver := new(imageresolvertesting.FakeResolver)
			fakeResolver.ResolveReturns("sha256:ondemand456", nil)

			noopStepper := func(p atc.Plan) exec.Step {
				Fail("no steps should be created when resolver handles resolution")
				return nil
			}

			state := exec.NewRunState(noopStepper, nil)
			plan := atc.Plan{ID: planID}

			df := DelegateFactory{
				build:                 fakeBuild,
				plan:                  plan,
				policyChecker:         fakePolicyChecker,
				dbWorkerFactory:       fakeWorkerFactory,
				lockFactory:           fakeLockFactory,
				resourceConfigFactory: fakeResourceConfigFactory,
				resourceCacheFactory:  fakeResourceCacheFactory,
				imageResolver:         fakeResolver,
			}
			td := df.TaskDelegate(state)

			imgSpec, err := td.FetchImage(
				context.TODO(),
				atc.ImageResource{Name: "image", Type: "registry-image", Source: atc.Source{"repository": "my-org/on-demand"}},
				atc.ResourceTypes{}, false, nil, false,
			)
			Expect(err).ToNot(HaveOccurred())

			By("resolving via the resolver")
			Expect(fakeResolver.ResolveCallCount()).To(Equal(1))
			_, repo, _, _ := fakeResolver.ResolveArgsForCall(0)
			Expect(repo).To(Equal("my-org/on-demand"))

			By("saving the resolved version to DB")
			Expect(fakeScope.SaveVersionsCallCount()).To(Equal(1))

			By("returning the correct image URL")
			Expect(imgSpec.ImageURL).To(Equal("docker:///my-org/on-demand@sha256:ondemand456"))
			Expect(imgSpec.ImageArtifact).To(BeNil())
		})

		It("returns error when resolver fails (no fallback to pods)", func() {
			fakeScope.LatestVersionReturns(nil, false, nil)

			fakeResolver := new(imageresolvertesting.FakeResolver)
			fakeResolver.ResolveReturns("", fmt.Errorf("registry timeout"))

			noopStepper := func(p atc.Plan) exec.Step {
				Fail("no steps should be created when resolver is configured")
				return nil
			}

			state := exec.NewRunState(noopStepper, nil)
			plan := atc.Plan{ID: planID}

			df := DelegateFactory{
				build:                 fakeBuild,
				plan:                  plan,
				policyChecker:         fakePolicyChecker,
				dbWorkerFactory:       fakeWorkerFactory,
				lockFactory:           fakeLockFactory,
				resourceConfigFactory: fakeResourceConfigFactory,
				resourceCacheFactory:  fakeResourceCacheFactory,
				imageResolver:         fakeResolver,
			}
			td := df.TaskDelegate(state)

			_, err := td.FetchImage(
				context.TODO(),
				atc.ImageResource{Name: "image", Type: "registry-image", Source: atc.Source{"repository": "my-org/fail-test"}},
				atc.ResourceTypes{}, false, nil, false,
			)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("on-demand image resolve"))
			Expect(err.Error()).To(ContainSubstring("registry timeout"))
		})

		It("works without resource factories (fallback-only mode)", func() {
			// Simulate pre-injection state where factories are nil
			fallbackCache := new(dbfakes.FakeResourceCache)
			fallbackCache.IDReturns(444)
			fallbackCache.VersionReturns(atc.Version{"digest": "sha256:nofactory"})

			fallbackStepper := func(p atc.Plan) exec.Step {
				step := new(execfakes.FakeStep)
				step.RunStub = func(_ context.Context, s exec.RunState) (bool, error) {
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
				}
				return step
			}

			state := exec.NewRunState(fallbackStepper, nil)
			plan := atc.Plan{ID: planID}

			// Create factory WITHOUT resource factories
			df := DelegateFactory{
				build:         fakeBuild,
				plan:          plan,
				policyChecker: fakePolicyChecker,
			}
			td := df.TaskDelegate(state)

			imgSpec, err := td.FetchImage(
				context.TODO(),
				atc.ImageResource{Name: "image", Type: "registry-image", Source: atc.Source{"repository": "my-org/no-factory"}},
				atc.ResourceTypes{}, false, nil, false,
			)
			Expect(err).ToNot(HaveOccurred())
			Expect(imgSpec.ImageURL).To(Equal("docker:///my-org/no-factory@sha256:nofactory"))
			Expect(imgSpec.ImageArtifact).ToNot(BeNil(), "without factories, always uses plan-based path")
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
					build:                 fakeBuild,
					plan:                  atc.Plan{ID: planID},
					policyChecker:         fakePolicyChecker,
					dbWorkerFactory:       fakeWorkerFactory,
					lockFactory:           fakeLockFactory,
					resourceConfigFactory: fakeResourceConfigFactory,
					resourceCacheFactory:  fakeResourceCacheFactory,
					imageResolver:         fakeResolver,
				}
				return df.TaskDelegate(exec.NewRunState(failStepper, nil))
			}

			BeforeEach(func() {
				fakeResolver = new(imageresolvertesting.FakeResolver)
				fakeResolver.ResolveReturns("sha256:resolver-must-not-be-used", nil)

				// Park a *different* version in the DB cache so that a passing
				// spec can only be reading the pin, never the cached value.
				fakeVersion.VersionReturns(db.Version{"digest": "sha256:stale-cached"})
			})

			It("uses the pinned digest without calling the image resolver", func() {
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

				By("never reaching out to the registry")
				Expect(fakeResolver.ResolveCallCount()).To(Equal(0))

				By("returning an image URL pinned to the requested digest")
				Expect(imgSpec.ImageURL).To(Equal("docker:///registry.home/agent-runner@" + pinnedDigest))
				Expect(imgSpec.ImageArtifact).To(BeNil(), "no volume artifact on the metadata path")
			})

			It("succeeds on a cold cache even when the registry is unreachable", func() {
				// Exactly the live failure: nothing cached for
				// registry.home/agent-runner, so the pre-fix code did an
				// on-demand resolve of ":latest" and died on the registry's
				// TLS certificate. A pinned digest needs no registry at all.
				fakeScope.LatestVersionReturns(nil, false, nil)
				fakeResolver.ResolveReturns("", fmt.Errorf(
					`Get "https://registry.home/v2/": tls: failed to verify certificate`))

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
			})

			It("neither reads nor writes the resource config scope when pinned", func() {
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

				By("skipping the resource config/scope lookup entirely")
				Expect(fakeResourceConfigFactory.FindOrCreateResourceConfigCallCount()).To(Equal(0))
				Expect(fakeResourceConfig.FindOrCreateScopeCallCount()).To(Equal(0))
				Expect(fakeScope.LatestVersionCallCount()).To(Equal(0))

				By("not publishing the pin as the scope's latest version")
				Expect(fakeScope.SaveVersionsCallCount()).To(Equal(0))
			})

			It("still builds and saves a resource cache for the pinned version", func() {
				td := buildResolvingTaskDelegate()

				_, err := td.FetchImage(
					context.TODO(),
					atc.ImageResource{
						Name:    "image",
						Type:    "registry-image",
						Source:  atc.Source{"repository": "registry.home/agent-runner"},
						Params:  atc.Params{"some": "param"},
						Version: atc.Version{"digest": pinnedDigest},
					},
					atc.ResourceTypes{}, false, nil, false,
				)
				Expect(err).ToNot(HaveOccurred())

				By("keying the cache on the pinned version")
				Expect(fakeResourceCacheFactory.FindOrCreateResourceCacheCallCount()).To(Equal(1))
				user, typeName, version, source, params, parentCache := fakeResourceCacheFactory.FindOrCreateResourceCacheArgsForCall(0)
				Expect(user).To(Equal(db.ForBuild(42)), "cache must stay anchored to the build for GC")
				Expect(typeName).To(Equal("registry-image"))
				Expect(version).To(Equal(atc.Version{"digest": pinnedDigest}))
				Expect(source).To(Equal(atc.Source{"repository": "registry.home/agent-runner"}))
				Expect(params).To(Equal(atc.Params{"some": "param"}))
				Expect(parentCache).To(BeNil())

				By("recording the image resource version on the build")
				Expect(fakeBuild.SaveImageResourceVersionCallCount()).To(Equal(1))
				Expect(fakeBuild.SaveImageResourceVersionArgsForCall(0).ID()).To(Equal(999))
			})

			It("uses the cached DB version, not the resolver, when nothing is pinned", func() {
				td := buildResolvingTaskDelegate()

				imgSpec, err := td.FetchImage(
					context.TODO(),
					atc.ImageResource{
						Name:   "image",
						Type:   "registry-image",
						Source: atc.Source{"repository": "registry.home/agent-runner"},
					},
					atc.ResourceTypes{}, false, nil, false,
				)
				Expect(err).ToNot(HaveOccurred())

				Expect(fakeScope.LatestVersionCallCount()).To(Equal(1))
				Expect(fakeResolver.ResolveCallCount()).To(Equal(0), "a warm cache must not hit the registry")
				Expect(imgSpec.ImageURL).To(Equal("docker:///registry.home/agent-runner@sha256:stale-cached"))
			})

			It("falls back to an on-demand resolve when nothing is pinned and nothing is cached", func() {
				fakeScope.LatestVersionReturns(nil, false, nil)
				fakeResolver.ResolveReturns("sha256:resolved-on-demand", nil)

				td := buildResolvingTaskDelegate()

				imgSpec, err := td.FetchImage(
					context.TODO(),
					atc.ImageResource{
						Name:   "image",
						Type:   "registry-image",
						Source: atc.Source{"repository": "registry.home/agent-runner", "tag": "v1"},
					},
					atc.ResourceTypes{}, false, nil, false,
				)
				Expect(err).ToNot(HaveOccurred())

				Expect(fakeResolver.ResolveCallCount()).To(Equal(1))
				_, repo, tag, _ := fakeResolver.ResolveArgsForCall(0)
				Expect(repo).To(Equal("registry.home/agent-runner"))
				Expect(tag).To(Equal("v1"))
				Expect(fakeScope.SaveVersionsCallCount()).To(Equal(1), "resolved versions are still cached back")
				Expect(imgSpec.ImageURL).To(Equal("docker:///registry.home/agent-runner@sha256:resolved-on-demand"))
			})

			It("resolves on demand when the pin carries no digest", func() {
				// atc.FetchImagePlan pins a non-nil but *empty* version whenever
				// the ImageResource carries one, which is what
				// agent/resourcecapture produces for a tag-based reference.
				// An empty pin identifies nothing, so it must not short-circuit.
				fakeScope.LatestVersionReturns(nil, false, nil)
				fakeResolver.ResolveReturns("sha256:tag-resolved", nil)

				td := buildResolvingTaskDelegate()

				imgSpec, err := td.FetchImage(
					context.TODO(),
					atc.ImageResource{
						Name:    "image",
						Type:    "registry-image",
						Source:  atc.Source{"repository": "registry.home/agent-runner", "tag": "latest"},
						Version: atc.Version{},
					},
					atc.ResourceTypes{}, false, nil, false,
				)
				Expect(err).ToNot(HaveOccurred())

				Expect(fakeResolver.ResolveCallCount()).To(Equal(1))
				Expect(imgSpec.ImageURL).To(Equal("docker:///registry.home/agent-runner@sha256:tag-resolved"))
			})
		})
	})
})
