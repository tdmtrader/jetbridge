package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"code.cloudfoundry.org/clock/fakeclock"
	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/engine"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/atc/exec/execfakes"
	"github.com/concourse/concourse/atc/policy"
	"github.com/concourse/concourse/atc/policy/policyfakes"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/runtime/runtimetest"
	"github.com/concourse/concourse/vars"
)

var _ = Describe("BuildStepDelegate", func() {
	var (
		logger            *lagertest.TestLogger
		fakeBuild         *dbfakes.FakeBuild
		fakeClock         *fakeclock.FakeClock
		planID            atc.PlanID
		runState          *execfakes.FakeRunState
		fakePolicyChecker *policyfakes.FakeChecker

		credVars vars.StaticVariables

		now = time.Date(1991, 6, 3, 5, 30, 0, 0, time.UTC)

		delegate exec.BuildStepDelegate
	)

	BeforeEach(func() {
		logger = lagertest.NewTestLogger("test")

		fakeBuild = new(dbfakes.FakeBuild)
		fakeClock = fakeclock.NewFakeClock(now)
		credVars = vars.StaticVariables{
			"source-param": "super-secret-source",
			"git-key":      "{\n123\n456\n789\n}\n",
		}
		planID = "some-plan-id"

		runState = new(execfakes.FakeRunState)

		repo := build.NewRepository()
		runState.ArtifactRepositoryReturns(repo)

		fakePolicyChecker = new(policyfakes.FakeChecker)

		delegate = engine.NewBuildStepDelegate(fakeBuild, planID, runState, fakeClock, fakePolicyChecker, false)
	})

	Describe("Initializing", func() {
		JustBeforeEach(func() {
			delegate.Initializing(logger)
		})

		It("saves an event", func() {
			Expect(fakeBuild.SaveEventCallCount()).To(Equal(1))
			event := fakeBuild.SaveEventArgsForCall(0)
			Expect(event.EventType()).To(Equal(atc.EventType("initialize")))
		})
	})

	Describe("Finished", func() {
		JustBeforeEach(func() {
			delegate.Finished(logger, true)
		})

		It("saves an event", func() {
			Expect(fakeBuild.SaveEventCallCount()).To(Equal(1))
			event := fakeBuild.SaveEventArgsForCall(0)
			Expect(event.EventType()).To(Equal(atc.EventType("finish")))
		})
	})

	Describe("FetchImage", func() {
		var delegate exec.BuildStepDelegate

		var expectedCheckPlan, expectedGetPlan *atc.Plan
		var volume *runtimetest.Volume
		var fakeResourceCache *dbfakes.FakeResourceCache

		var privileged bool

		var imageSpec runtime.ImageSpec
		var fetchErr error
		var resourceCache db.ResourceCache

		var runPlans []atc.Plan
		var stepper exec.Stepper
		var parentRunState exec.RunState

		BeforeEach(func() {
			repo := build.NewRepository()
			runState.ArtifactRepositoryReturns(repo)

			runState.GetStub = vars.StaticVariables{
				"source-var": "super-secret-source",
				"params-var": "super-secret-params",
			}.Get

			runPlans = nil

			expectedCheckPlan = &atc.Plan{
				ID: planID + "/image-check",
				Check: &atc.CheckPlan{
					Name:   "image",
					Type:   "docker",
					Source: atc.Source{"some": "((source-var))"},
					Tags:   atc.Tags{"some", "tags"},
				},
			}

			expectedGetPlan = &atc.Plan{
				ID: planID + "/image-get",
				Get: &atc.GetPlan{
					Name:    "image",
					Type:    "docker",
					Source:  atc.Source{"some": "((source-var))"},
					Version: &atc.Version{"some": "version"},
					Params:  atc.Params{"some": "((params-var))"},
					Tags:    atc.Tags{"some", "tags"},
				},
			}

			stepper = func(p atc.Plan) exec.Step {
				runPlans = append(runPlans, p)

				fakeResourceCache = new(dbfakes.FakeResourceCache)
				fakeResourceCache.IDReturns(123)
				volume = runtimetest.NewVolume("image-handle")

				step := new(execfakes.FakeStep)
				step.RunStub = func(_ context.Context, state exec.RunState) (bool, error) {
					state.ArtifactRepository().RegisterArtifact("image", volume, false)
					state.StoreResult(expectedGetPlan.ID, exec.GetResult{
						Name:          "image",
						ResourceCache: fakeResourceCache,
					})
					return true, nil
				}
				return step
			}

			parentRunState = exec.NewRunState(stepper, nil)

			privileged = false

		})

		JustBeforeEach(func() {
			delegate = engine.NewBuildStepDelegate(fakeBuild, planID, parentRunState, fakeClock, fakePolicyChecker, false)
			imageSpec, resourceCache, fetchErr = delegate.FetchImage(context.TODO(), *expectedGetPlan, expectedCheckPlan, privileged)
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

		It("returns back the resource cache stored in the result", func() {
			Expect(resourceCache.ID()).To(Equal(fakeResourceCache.ID()))
		})

		It("runs both check and get plans using the child state", func() {
			Expect(runPlans).To(Equal([]atc.Plan{
				*expectedCheckPlan,
				*expectedGetPlan,
			}))
		})

		It("records the resource cache as an image resource for the build", func() {
			Expect(fakeBuild.SaveImageResourceVersionCallCount()).To(Equal(1))
			Expect(fakeBuild.SaveImageResourceVersionArgsForCall(0)).To(Equal(fakeResourceCache))
		})

		Context("when privileged", func() {
			BeforeEach(func() {
				privileged = true
			})

			It("returns a privileged image spec", func() {
				Expect(imageSpec).To(Equal(runtime.ImageSpec{
					ImageArtifact: volume,
					ResourceType:  "image",
					Privileged:    true,
				}))
			})
		})

		Describe("policy checking", func() {
			BeforeEach(func() {
				fakeBuild.TeamNameReturns("some-team")
				fakeBuild.PipelineNameReturns("some-pipeline")
			})

			Context("when the action does not need to be checked", func() {
				BeforeEach(func() {
					fakePolicyChecker.ShouldCheckActionReturns(false)
				})

				It("succeeds", func() {
					Expect(fetchErr).ToNot(HaveOccurred())
				})

				It("checked if ActionUseImage is enabled", func() {
					Expect(fakePolicyChecker.ShouldCheckActionCallCount()).To(Equal(1))
					action := fakePolicyChecker.ShouldCheckActionArgsForCall(0)
					Expect(action).To(Equal(policy.ActionUseImage))
				})

				It("does not check", func() {
					Expect(fakePolicyChecker.CheckCallCount()).To(Equal(0))
				})
			})

			Context("when the action needs to be checked", func() {
				var fakeCheckResult *policyfakes.FakePolicyCheckResult
				BeforeEach(func() {
					fakeCheckResult = new(policyfakes.FakePolicyCheckResult)
					fakePolicyChecker.CheckReturns(fakeCheckResult, nil)
					fakePolicyChecker.ShouldCheckActionReturns(true)
				})

				It("policy check should be done", func() {
					Expect(fakePolicyChecker.CheckCallCount()).To(Equal(1))
				})

				Context("when the check fails", func() {
					BeforeEach(func() {
						fakePolicyChecker.CheckReturns(nil, errors.New("some-error"))
					})

					It("should fail", func() {
						Expect(fetchErr).To(HaveOccurred())
						Expect(fetchErr.Error()).To(Equal("policy check: some-error"))
					})
				})

				Context("when the check is not allowed", func() {
					BeforeEach(func() {
						fakeCheckResult.AllowedReturns(false)
						fakeCheckResult.ShouldBlockReturns(true)
						fakeCheckResult.MessagesReturns([]string{"reasonA", "reasonB"})
					})

					It("should fail", func() {
						Expect(fetchErr).To(HaveOccurred())
						Expect(fetchErr.Error()).To(ContainSubstring("policy check failed"))
						Expect(fetchErr.Error()).To(ContainSubstring("reasonA"))
						Expect(fetchErr.Error()).To(ContainSubstring("reasonB"))
					})
				})

				// This test case should do same thing as "when the check is allowed",
				// thus this case only verifies policy check warning messages.
				Context("when the check is not allowed but non-block", func() {
					BeforeEach(func() {
						fakeCheckResult.AllowedReturns(false)
						fakeCheckResult.ShouldBlockReturns(false)
						fakeCheckResult.MessagesReturns([]string{"reasonA", "reasonB"})
					})

					It("succeeds", func() {
						Expect(fetchErr).ToNot(HaveOccurred())
					})

					It("log warning messages", func() {
						e := fakeBuild.SaveEventArgsForCall(0)
						Expect(e.EventType()).To(Equal(event.EventTypeLog))
						Expect(e.(event.Log).Origin).To(Equal(event.Origin{
							ID:     "some-plan-id",
							Source: event.OriginSourceStderr,
						}))
						Expect(e.(event.Log).Payload).To(ContainSubstring("policy check failed"))
						Expect(e.(event.Log).Payload).To(ContainSubstring("reasonA"))
						Expect(e.(event.Log).Payload).To(ContainSubstring("reasonB"))

						e = fakeBuild.SaveEventArgsForCall(1)
						Expect(e.EventType()).To(Equal(event.EventTypeLog))
						Expect(e.(event.Log).Origin).To(Equal(event.Origin{
							ID:     "some-plan-id",
							Source: event.OriginSourceStderr,
						}))
						Expect(e.(event.Log).Payload).To(ContainSubstring("WARNING: unblocking from the policy check failure for soft enforcement"))
					})
				})

				Context("when the check is allowed", func() {
					BeforeEach(func() {
						fakeCheckResult.AllowedReturns(true)
					})

					It("succeeds", func() {
						Expect(fetchErr).ToNot(HaveOccurred())
					})

					It("should not log policy check warning", func() {
						for i := 0; i < fakeBuild.SaveEventCallCount(); i++ {
							e := fakeBuild.SaveEventArgsForCall(i)
							if logEvent, ok := e.(event.Log); ok {
								Expect(logEvent.Payload).ToNot(ContainSubstring("WARNING: unblocking from the policy check failure for soft enforcement"))
							}
						}
					})

					It("checked with the right values", func() {
						Expect(fakePolicyChecker.CheckCallCount()).To(Equal(1))
						input := fakePolicyChecker.CheckArgsForCall(0)
						Expect(input).To(Equal(policy.PolicyCheckInput{
							Action:   policy.ActionUseImage,
							Team:     "some-team",
							Pipeline: "some-pipeline",
							Data: map[string]any{
								"image_type":   "docker",
								"image_source": atc.Source{"some": "((source-var))"},
								"privileged":   false,
							},
						}))
					})

					Context("when the image source contains credentials", func() {
						BeforeEach(func() {
							expectedGetPlan.Get.Source = atc.Source{"some": "super-secret-source"}

							parentRunState.AddLocalVar("source-var", "super-secret-source", true)
						})

						It("redacts the value prior to checking", func() {
							Expect(fakePolicyChecker.CheckCallCount()).To(Equal(1))
							input := fakePolicyChecker.CheckArgsForCall(0)
							Expect(input).To(Equal(policy.PolicyCheckInput{
								Action:   policy.ActionUseImage,
								Team:     "some-team",
								Pipeline: "some-pipeline",
								Data: map[string]any{
									"image_type":   "docker",
									"image_source": atc.Source{"some": "((redacted))"},
									"privileged":   false,
								},
							}))
						})
					})

					Context("when privileged", func() {
						BeforeEach(func() {
							privileged = true
						})

						It("checks with privileged", func() {
							Expect(fakePolicyChecker.CheckCallCount()).To(Equal(1))
							input := fakePolicyChecker.CheckArgsForCall(0)
							Expect(input).To(Equal(policy.PolicyCheckInput{
								Action:   policy.ActionUseImage,
								Team:     "some-team",
								Pipeline: "some-pipeline",
								Data: map[string]any{
									"image_type":   "docker",
									"image_source": atc.Source{"some": "((source-var))"},
									"privileged":   true,
								},
							}))
						})
					})
				})
			})
		})

		Context("when there is no check plan", func() {
			BeforeEach(func() {
				expectedCheckPlan = nil
			})

			It("does not run a CheckPlan", func() {
				Expect(runPlans).To(Equal([]atc.Plan{
					*expectedGetPlan,
				}))
			})
		})

		Context("when no resource factories are set", func() {
			It("runs both check and get plans (no metadata-only shortcut)", func() {
				runPlans = nil
				nativeDelegate := engine.NewBuildStepDelegate(fakeBuild, planID, parentRunState, fakeClock, fakePolicyChecker, false)
				_, resCache, err := nativeDelegate.FetchImage(context.TODO(), *expectedGetPlan, expectedCheckPlan, false)
				Expect(err).ToNot(HaveOccurred())

				By("running both check and get plans")
				Expect(runPlans).To(Equal([]atc.Plan{*expectedCheckPlan, *expectedGetPlan}))

				By("returning a resource cache from the get step")
				Expect(resCache).ToNot(BeNil())
			})
		})

		Context("when the custom type is not registry-image (e.g. git-backed)", func() {
			BeforeEach(func() {
				expectedGetPlan = &atc.Plan{
					ID: planID + "/image-get",
					Get: &atc.GetPlan{
						Name:   "git-with-ado",
						Type:   "git",
						Source: atc.Source{"uri": "https://dev.azure.com/org/repo"},
					},
				}
				expectedCheckPlan = &atc.Plan{
					ID: planID + "/image-check",
					Check: &atc.CheckPlan{
						Name:   "git-with-ado",
						Type:   "git",
						Source: atc.Source{"uri": "https://dev.azure.com/org/repo"},
					},
				}
			})

			It("sets ImageSpec.ResourceType to the custom type name from the get plan", func() {
				Expect(fetchErr).ToNot(HaveOccurred())

				By("ResourceType is set to the custom type name so resolveImage can map it")
				Expect(imageSpec.ResourceType).To(Equal("git-with-ado"))
			})

			It("still includes the artifact", func() {
				Expect(fetchErr).ToNot(HaveOccurred())
				Expect(imageSpec.ImageArtifact).ToNot(BeNil())
			})

			It("has an empty ImageURL since git types don't produce Docker URLs", func() {
				Expect(fetchErr).ToNot(HaveOccurred())
				Expect(imageSpec.ImageURL).To(BeEmpty())
			})
		})

		Context("when the custom type is registry-image", func() {
			BeforeEach(func() {
				fakeResourceCache = new(dbfakes.FakeResourceCache)
				fakeResourceCache.IDReturns(999)
				fakeResourceCache.VersionReturns(atc.Version{"digest": "sha256:abc123"})

				expectedGetPlan = &atc.Plan{
					ID: planID + "/image-get",
					Get: &atc.GetPlan{
						Name:   "my-custom-registry-type",
						Type:   "registry-image",
						Source: atc.Source{"repository": "my-org/custom-image"},
					},
				}
				expectedCheckPlan = &atc.Plan{
					ID: planID + "/image-check",
					Check: &atc.CheckPlan{
						Name:   "my-custom-registry-type",
						Type:   "registry-image",
						Source: atc.Source{"repository": "my-org/custom-image"},
					},
				}

				stepper = func(p atc.Plan) exec.Step {
					runPlans = append(runPlans, p)

					vol := runtimetest.NewVolume("registry-image-handle")
					step := new(execfakes.FakeStep)
					step.RunStub = func(_ context.Context, state exec.RunState) (bool, error) {
						state.ArtifactRepository().RegisterArtifact("image", vol, false)
						state.StoreResult(expectedGetPlan.ID, exec.GetResult{
							Name:          "image",
							ResourceCache: fakeResourceCache,
						})
						return true, nil
					}
					return step
				}

				parentRunState = exec.NewRunState(stepper, nil)
			})

			It("uses ImageURL from the registry source (no ResourceType needed)", func() {
				Expect(fetchErr).ToNot(HaveOccurred())
				Expect(imageSpec.ImageURL).To(Equal("docker:///my-org/custom-image@sha256:abc123"))
			})

			It("does not set ResourceType since ImageURL is sufficient", func() {
				Expect(fetchErr).ToNot(HaveOccurred())
				Expect(imageSpec.ResourceType).To(BeEmpty())
			})
		})

	})

	Describe("Metadata-only FetchImage", func() {
		var (
			nativeDelegate            exec.BuildStepDelegate
			fakeResourceConfigFactory *dbfakes.FakeResourceConfigFactory
			fakeResourceCacheFactory  *dbfakes.FakeResourceCacheFactory
			fakeResourceConfig        *dbfakes.FakeResourceConfig
			fakeScope                 *dbfakes.FakeResourceConfigScope
			fakeVersion               *dbfakes.FakeResourceConfigVersion
			fakeMetadataCache         *dbfakes.FakeResourceCache
			registryGetPlan           *atc.Plan
			registryCheckPlan         *atc.Plan
			runPlans                  []atc.Plan
		)

		BeforeEach(func() {
			fakeResourceConfigFactory = new(dbfakes.FakeResourceConfigFactory)
			fakeResourceCacheFactory = new(dbfakes.FakeResourceCacheFactory)
			fakeResourceConfig = new(dbfakes.FakeResourceConfig)
			fakeScope = new(dbfakes.FakeResourceConfigScope)
			fakeVersion = new(dbfakes.FakeResourceConfigVersion)
			fakeMetadataCache = new(dbfakes.FakeResourceCache)

			// Wire the DB chain: factory → config → scope → version
			fakeResourceConfigFactory.FindOrCreateResourceConfigReturns(fakeResourceConfig, nil)
			fakeResourceConfig.FindOrCreateScopeReturns(fakeScope, nil)
			fakeScope.LatestVersionReturns(fakeVersion, true, nil)
			fakeVersion.VersionReturns(db.Version{"digest": "sha256:abc123"})

			fakeResourceCacheFactory.FindOrCreateResourceCacheReturns(fakeMetadataCache, nil)
			fakeMetadataCache.IDReturns(456)

			fakeBuild.IDReturns(42)

			registryCheckPlan = &atc.Plan{
				ID: planID + "/image-check",
				Check: &atc.CheckPlan{
					Name:   "image",
					Type:   "registry-image",
					Source: atc.Source{"repository": "my-registry/my-image", "tag": "latest"},
				},
			}
			registryGetPlan = &atc.Plan{
				ID: planID + "/image-get",
				Get: &atc.GetPlan{
					Name:   "image",
					Type:   "registry-image",
					Source: atc.Source{"repository": "my-registry/my-image", "tag": "latest"},
					Params: atc.Params{},
				},
			}

			runPlans = nil

			stepper := func(p atc.Plan) exec.Step {
				runPlans = append(runPlans, p)

				rCache := new(dbfakes.FakeResourceCache)
				rCache.IDReturns(789)
				vol := runtimetest.NewVolume("fallback-image-handle")

				step := new(execfakes.FakeStep)
				step.RunStub = func(_ context.Context, state exec.RunState) (bool, error) {
					state.ArtifactRepository().RegisterArtifact("image", vol, false)
					state.StoreResult(registryGetPlan.ID, exec.GetResult{
						Name:          "image",
						ResourceCache: rCache,
					})
					return true, nil
				}
				return step
			}

			parentRunState := exec.NewRunState(stepper, nil)
			nativeDelegate = engine.NewBuildStepDelegateWithFactories(
				fakeBuild, planID, parentRunState, fakeClock, fakePolicyChecker, false,
				fakeResourceConfigFactory, fakeResourceCacheFactory,
			)
		})

		It("resolves the image from DB without running any plans", func() {
			spec, cache, err := nativeDelegate.FetchImage(context.TODO(), *registryGetPlan, registryCheckPlan, false)
			Expect(err).ToNot(HaveOccurred())

			By("not executing any plans")
			Expect(runPlans).To(BeEmpty())

			By("returning an ImageSpec with the correct ImageURL")
			Expect(spec.ImageURL).To(Equal("docker:///my-registry/my-image@sha256:abc123"))
			Expect(spec.ImageArtifact).To(BeNil())
			Expect(spec.Privileged).To(BeFalse())

			By("returning the metadata resource cache")
			Expect(cache.ID()).To(Equal(456))
		})

		It("saves the image resource version for build tracking", func() {
			_, _, err := nativeDelegate.FetchImage(context.TODO(), *registryGetPlan, registryCheckPlan, false)
			Expect(err).ToNot(HaveOccurred())
			Expect(fakeBuild.SaveImageResourceVersionCallCount()).To(Equal(1))
			Expect(fakeBuild.SaveImageResourceVersionArgsForCall(0)).To(Equal(fakeMetadataCache))
		})

		It("creates the resource cache with the correct arguments", func() {
			_, _, err := nativeDelegate.FetchImage(context.TODO(), *registryGetPlan, registryCheckPlan, false)
			Expect(err).ToNot(HaveOccurred())

			Expect(fakeResourceCacheFactory.FindOrCreateResourceCacheCallCount()).To(Equal(1))
			user, typeName, version, source, params, parentCache := fakeResourceCacheFactory.FindOrCreateResourceCacheArgsForCall(0)
			Expect(user).ToNot(BeNil())
			Expect(typeName).To(Equal("registry-image"))
			Expect(version).To(Equal(atc.Version{"digest": "sha256:abc123"}))
			Expect(source).To(Equal(atc.Source{"repository": "my-registry/my-image", "tag": "latest"}))
			Expect(params).To(BeEmpty())
			Expect(parentCache).To(BeNil())
		})

		It("passes privileged through to the ImageSpec", func() {
			spec, _, err := nativeDelegate.FetchImage(context.TODO(), *registryGetPlan, registryCheckPlan, true)
			Expect(err).ToNot(HaveOccurred())
			Expect(spec.Privileged).To(BeTrue())
		})

		Context("when no cached version exists in DB", func() {
			BeforeEach(func() {
				fakeScope.LatestVersionReturns(nil, false, nil)
			})

			It("falls back to running check+get plans", func() {
				spec, cache, err := nativeDelegate.FetchImage(context.TODO(), *registryGetPlan, registryCheckPlan, false)
				Expect(err).ToNot(HaveOccurred())

				By("running check and get plans as fallback")
				Expect(runPlans).To(HaveLen(2))

				By("returning an ImageSpec with an artifact from the fallback get")
				Expect(spec.ImageArtifact).ToNot(BeNil())

				By("returning the resource cache from the fallback get")
				Expect(cache.ID()).To(Equal(789))
			})
		})

		Context("when the resource type is not registry-image", func() {
			BeforeEach(func() {
				registryGetPlan.Get.Type = "custom-type"
			})

			It("falls back to running check+get plans", func() {
				_, cache, err := nativeDelegate.FetchImage(context.TODO(), *registryGetPlan, registryCheckPlan, false)
				Expect(err).ToNot(HaveOccurred())

				By("running plans because non-registry-image type falls back")
				Expect(runPlans).To(HaveLen(2))
				Expect(cache.ID()).To(Equal(789))
			})
		})

		Context("when the type produces registry-image", func() {
			BeforeEach(func() {
				registryGetPlan.Get.Type = "custom-oci-fetcher"
				registryGetPlan.Get.Produces = "registry-image"
			})

			It("resolves the image from DB via metadata-only path", func() {
				spec, cache, err := nativeDelegate.FetchImage(context.TODO(), *registryGetPlan, registryCheckPlan, false)
				Expect(err).ToNot(HaveOccurred())

				By("not executing any plans")
				Expect(runPlans).To(BeEmpty())

				By("returning an ImageSpec with the correct ImageURL")
				Expect(spec.ImageURL).To(Equal("docker:///my-registry/my-image@sha256:abc123"))
				Expect(spec.ImageArtifact).To(BeNil())

				By("returning the metadata resource cache")
				Expect(cache.ID()).To(Equal(456))
			})
		})

		Context("when the type is not registry-image and does NOT produce registry-image", func() {
			BeforeEach(func() {
				registryGetPlan.Get.Type = "custom-type"
				registryGetPlan.Get.Produces = ""
			})

			It("falls back to running check+get plans", func() {
				_, cache, err := nativeDelegate.FetchImage(context.TODO(), *registryGetPlan, registryCheckPlan, false)
				Expect(err).ToNot(HaveOccurred())

				By("running plans because type doesn't produce registry-image")
				Expect(runPlans).To(HaveLen(2))
				Expect(cache.ID()).To(Equal(789))
			})
		})
	})

	Describe("ConstructAcrossSubsteps", func() {
		planIDPtr := func(p atc.PlanID) *atc.PlanID {
			return &p
		}

		It("constructs the across substeps and emits them as a build event", func() {
			template := []byte(`{
				"id": "on-success-id",
				"on_success": {
					"step": {
						"id": "put-id",
						"put": {
							"name": "((.:v1))",
							"type": "some-type",
							"params": {
								"p1": "((.:v2))",
								"p2": "howdy ((.:v3))",
								"untouched": "((v1))"
							}
						}
					},
					"on_success": {
						"id": "get-id",
						"get": {
							"name": "((.:v1))",
							"type": "some-type",
							"version_from": "put-id"
						}
					}
				}
			}`)
			substeps, err := delegate.ConstructAcrossSubsteps(template, []atc.AcrossVar{
				{Var: "v1"},
				{Var: "v2"},
				{Var: "v3"},
			}, [][]any{
				{"a1", "b1", "c1"},
				{"a1", "b1", "c2"},
			})
			Expect(err).ToNot(HaveOccurred())

			expectedSubstepPlans := []atc.VarScopedPlan{
				{
					Values: []any{"a1", "b1", "c1"},
					Step: atc.Plan{
						ID: "some-plan-id/0/0",
						OnSuccess: &atc.OnSuccessPlan{
							Step: atc.Plan{
								ID: "some-plan-id/0/1",
								Put: &atc.PutPlan{
									Name: "a1",
									Type: "some-type",
									Params: atc.Params{
										"p1":        "b1",
										"p2":        "howdy c1",
										"untouched": "((v1))",
									},
								},
							},
							Next: atc.Plan{
								ID: "some-plan-id/0/2",
								Get: &atc.GetPlan{
									Name:        "a1",
									Type:        "some-type",
									VersionFrom: planIDPtr("some-plan-id/0/1"),
								},
							},
						},
					},
				},
				{
					Values: []any{"a1", "b1", "c2"},
					Step: atc.Plan{
						ID: "some-plan-id/1/0",
						OnSuccess: &atc.OnSuccessPlan{
							Step: atc.Plan{
								ID: "some-plan-id/1/1",
								Put: &atc.PutPlan{
									Name: "a1",
									Type: "some-type",
									Params: atc.Params{
										"p1":        "b1",
										"p2":        "howdy c2",
										"untouched": "((v1))",
									},
								},
							},
							Next: atc.Plan{
								ID: "some-plan-id/1/2",
								Get: &atc.GetPlan{
									Name:        "a1",
									Type:        "some-type",
									VersionFrom: planIDPtr("some-plan-id/1/1"),
								},
							},
						},
					},
				},
			}

			By("interpolating the var values into the substep plans")
			Expect(substeps).To(Equal(expectedSubstepPlans))

			By("emitting the public plans as a build event")
			Expect(fakeBuild.SaveEventCallCount()).To(Equal(1))
			Expect(fakeBuild.SaveEventArgsForCall(0)).To(Equal(event.AcrossSubsteps{
				Time: now.Unix(),
				Substeps: []*json.RawMessage{
					expectedSubstepPlans[0].Public(),
					expectedSubstepPlans[1].Public(),
				},
				Origin: event.Origin{
					ID: "some-plan-id",
				},
			}))
		})

		It("doesn't transform Get.VersionFrom if the referenced PlanID isn't a part of the substep", func() {
			template := []byte(`{
				"id": "get-id",
				"get": {
					"name": "((.:v1))",
					"type": "some-type",
					"version_from": "external-id"
				}
			}`)
			substeps, err := delegate.ConstructAcrossSubsteps(template, []atc.AcrossVar{
				{Var: "v1"},
			}, [][]any{
				{"a1"},
			})
			Expect(err).ToNot(HaveOccurred())

			expectedSubstepPlans := []atc.VarScopedPlan{
				{
					Values: []any{"a1"},
					Step: atc.Plan{
						ID: "some-plan-id/0/0",
						Get: &atc.GetPlan{
							Name:        "a1",
							Type:        "some-type",
							VersionFrom: planIDPtr("external-id"),
						},
					},
				},
			}

			By("interpolating the var values into the substep plans")
			Expect(substeps).To(Equal(expectedSubstepPlans))

			By("emitting the public plans as a build event")
			Expect(fakeBuild.SaveEventCallCount()).To(Equal(1))
			Expect(fakeBuild.SaveEventArgsForCall(0)).To(Equal(event.AcrossSubsteps{
				Time: now.Unix(),
				Substeps: []*json.RawMessage{
					expectedSubstepPlans[0].Public(),
				},
				Origin: event.Origin{
					ID: "some-plan-id",
				},
			}))
		})

		It("handles all PlanID fields in the atc.Plan", func() {
			// handledFields are the fields of type PlanID that are properly
			// handled in ConstructAcrossSubsteps (i.e. that get mapped to the
			// appropriate plan ID within the substep). If a new PlanID field
			// is added to atc.Plan or one of its subtypes, it must be properly
			// handled and added to this list.
			handledFields := []string{"ID", "Get.VersionFrom"}

			var dereference func(reflect.Type) reflect.Type
			dereference = func(rt reflect.Type) reflect.Type {
				switch rt.Kind() {
				case reflect.Pointer, reflect.Array, reflect.Slice:
					return dereference(rt.Elem())
				default:
					return rt
				}
			}

			planIDType := reflect.TypeOf(atc.PlanID(""))

			seen := map[reflect.Type]bool{}
			var walk func([]string, reflect.Type)
			walk = func(paths []string, rt reflect.Type) {
				rt = dereference(rt)

				fieldPath := strings.Join(paths, ".")
				if rt == planIDType && !slices.Contains(handledFields, fieldPath) {
					Fail(fmt.Sprintf("ConstructAcrossSubsteps does not handle PlanID field %q", fieldPath))
				}

				// Avoid recursing infinitely since atc.Plan is a recursive type
				if seen[rt] {
					return
				}
				seen[rt] = true

				if rt.Kind() == reflect.Map {
					walk(append(paths, "key"), rt.Key())
					walk(append(paths, "value"), rt.Elem())
				}

				if rt.Kind() == reflect.Struct {
					for i := 0; i < rt.NumField(); i++ {
						field := rt.Field(i)
						walk(append(paths, field.Name), field.Type)
					}
				}
			}

			walk(nil, reflect.TypeOf(atc.Plan{}))
		})
	})

	Describe("Stdout", func() {
		var writer io.Writer

		BeforeEach(func() {
			writer = delegate.Stdout()
		})

		Describe("writing to the writer", func() {
			var writtenBytes int
			var writeErr error

			Context("when saving the event succeeds", func() {
				BeforeEach(func() {
					fakeBuild.SaveEventReturns(nil)
					writtenBytes, writeErr = writer.Write([]byte("hello\nworld"))
					writer.(io.Closer).Close()
				})

				It("returns the length of the string, and no error", func() {
					Expect(writtenBytes).To(Equal(len("hello\nworld")))
					Expect(writeErr).ToNot(HaveOccurred())
				})

				It("saves a log event", func() {
					Expect(fakeBuild.SaveEventCallCount()).To(Equal(2))
					Expect(fakeBuild.SaveEventArgsForCall(0)).To(Equal(event.Log{
						Time:    now.Unix(),
						Payload: "hello\n",
						Origin: event.Origin{
							Source: event.OriginSourceStdout,
							ID:     "some-plan-id",
						},
					}))
					Expect(fakeBuild.SaveEventArgsForCall(1)).To(Equal(event.Log{
						Time:    now.Unix(),
						Payload: "world",
						Origin: event.Origin{
							Source: event.OriginSourceStdout,
							ID:     "some-plan-id",
						},
					}))
				})
			})

			Context("when saving the event fails", func() {
				disaster := errors.New("nope")

				BeforeEach(func() {
					fakeBuild.SaveEventReturns(disaster)
					writtenBytes, writeErr = writer.Write([]byte("hello\nworld"))
					writer.(io.Closer).Close()
				})

				It("returns 0 length, and the error", func() {
					Expect(writtenBytes).To(Equal(0))
					Expect(writeErr).To(Equal(disaster))
				})
			})

			Context("caches any text after \\n or \\r", func() {
				Context("when the data only contains \\n", func() {
					BeforeEach(func() {
						fakeBuild.SaveEventReturns(nil)
						writtenBytes, writeErr = writer.Write([]byte("hello\nworld"))
					})

					It("returns the length of the string, and no error", func() {
						Expect(writtenBytes).To(Equal(len("hello\nworld")))
						Expect(writeErr).ToNot(HaveOccurred())
					})

					It("saves two log events", func() {
						Expect(fakeBuild.SaveEventCallCount()).To(Equal(1))
						Expect(fakeBuild.SaveEventArgsForCall(0)).To(Equal(event.Log{
							Time:    now.Unix(),
							Payload: "hello\n",
							Origin: event.Origin{
								Source: event.OriginSourceStdout,
								ID:     "some-plan-id",
							},
						}))

						writer.(io.Closer).Close()
						Expect(fakeBuild.SaveEventCallCount()).To(Equal(2), "second event is cached and only written after writer.Close() is called")
						Expect(fakeBuild.SaveEventArgsForCall(1)).To(Equal(event.Log{
							Time:    now.Unix(),
							Payload: "world",
							Origin: event.Origin{
								Source: event.OriginSourceStdout,
								ID:     "some-plan-id",
							},
						}))
					})
				})

				Context("when the data only contains \\r", func() {
					BeforeEach(func() {
						fakeBuild.SaveEventReturns(nil)
						writtenBytes, writeErr = writer.Write([]byte("hello\rworld"))
					})

					It("returns the length of the string, and no error", func() {
						Expect(writtenBytes).To(Equal(len("hello\rworld")))
						Expect(writeErr).ToNot(HaveOccurred())
					})

					It("saves two log events, breaking on \\r", func() {
						Expect(fakeBuild.SaveEventCallCount()).To(Equal(1))
						Expect(fakeBuild.SaveEventArgsForCall(0)).To(Equal(event.Log{
							Time:    now.Unix(),
							Payload: "hello\r",
							Origin: event.Origin{
								Source: event.OriginSourceStdout,
								ID:     "some-plan-id",
							},
						}))

						writer.(io.Closer).Close()
						Expect(fakeBuild.SaveEventCallCount()).To(Equal(2), "second event is cached and only written after writer.Close() is called")
						Expect(fakeBuild.SaveEventArgsForCall(1)).To(Equal(event.Log{
							Time:    now.Unix(),
							Payload: "world",
							Origin: event.Origin{
								Source: event.OriginSourceStdout,
								ID:     "some-plan-id",
							},
						}))
					})
				})

				Context("when the data contains \\n and \\r", func() {
					BeforeEach(func() {
						fakeBuild.SaveEventReturns(nil)
						writtenBytes, writeErr = writer.Write([]byte("hello\nbeautiful\rworld\n"))
						writer.(io.Closer).Close()
					})

					It("returns the length of the string, and no error", func() {
						Expect(writtenBytes).To(Equal(len("hello\nbeautiful\rworld\n")))
						Expect(writeErr).ToNot(HaveOccurred())
					})

					It("writer prefers breaking on the last \\n over \\r", func() {
						Expect(fakeBuild.SaveEventCallCount()).To(Equal(1))
						Expect(fakeBuild.SaveEventArgsForCall(0)).To(Equal(event.Log{
							Time:    now.Unix(),
							Payload: "hello\nbeautiful\rworld\n",
							Origin: event.Origin{
								Source: event.OriginSourceStdout,
								ID:     "some-plan-id",
							},
						}))
					})
				})

				Context("when the data does not contain \\n or \\r", func() {
					BeforeEach(func() {
						fakeBuild.SaveEventReturns(nil)
						writtenBytes, writeErr = writer.Write([]byte("hello world"))
					})

					It("returns the length of the string, no error, and does not log the event", func() {
						Expect(writtenBytes).To(Equal(len("hello world")))
						Expect(writeErr).ToNot(HaveOccurred())
						Expect(fakeBuild.SaveEventCallCount()).To(Equal(0), "first payload should be entirely cached")
					})

					It("flushes the payload after 1 second", func() {
						Expect(fakeBuild.SaveEventCallCount()).To(Equal(0), "first payload should be entirely cached")

						fakeClock.Increment(time.Second)
						writtenBytes, writeErr = writer.Write([]byte("!!!"))
						Expect(writtenBytes).To(Equal(len("!!!")))
						Expect(writeErr).ToNot(HaveOccurred())

						Expect(fakeBuild.SaveEventCallCount()).To(Equal(1), "entire payload should be flushed")
						Expect(fakeBuild.SaveEventArgsForCall(0)).To(Equal(event.Log{
							Time:    fakeClock.Now().Unix(),
							Payload: "hello world!!!",
							Origin: event.Origin{
								Source: event.OriginSourceStdout,
								ID:     "some-plan-id",
							},
						}))

						writer.(io.Closer).Close()
						Expect(fakeBuild.SaveEventCallCount()).To(Equal(1), "no more events should be written")
					})
				})
			})
		})
	})

	Describe("Stderr", func() {
		var writer io.Writer

		BeforeEach(func() {
			writer = delegate.Stderr()
		})

		Describe("writing to the writer", func() {
			var writtenBytes int
			var writeErr error

			JustBeforeEach(func() {
				writtenBytes, writeErr = writer.Write([]byte("hello\n"))
				writer.(io.Closer).Close()
			})

			Context("when saving the event succeeds", func() {
				BeforeEach(func() {
					fakeBuild.SaveEventReturns(nil)
				})

				It("returns the length of the string, and no error", func() {
					Expect(writtenBytes).To(Equal(len("hello\n")))
					Expect(writeErr).ToNot(HaveOccurred())
				})

				It("saves a log event", func() {
					Expect(fakeBuild.SaveEventCallCount()).To(Equal(1))
					Expect(fakeBuild.SaveEventArgsForCall(0)).To(Equal(event.Log{
						Time:    now.Unix(),
						Payload: "hello\n",
						Origin: event.Origin{
							Source: event.OriginSourceStderr,
							ID:     "some-plan-id",
						},
					}))
				})
			})

			Context("when saving the event fails", func() {
				disaster := errors.New("nope")

				BeforeEach(func() {
					fakeBuild.SaveEventReturns(disaster)
				})

				It("returns 0 length, and the error", func() {
					Expect(writtenBytes).To(Equal(0))
					Expect(writeErr).To(Equal(disaster))
				})
			})
		})
	})

	Describe("Errored", func() {
		JustBeforeEach(func() {
			delegate.Errored(logger, "fake error message")
		})

		Context("when saving the event succeeds", func() {
			BeforeEach(func() {
				fakeBuild.SaveEventReturns(nil)
			})

			It("saves it with the current time", func() {
				Expect(fakeBuild.SaveEventCallCount()).To(Equal(1))
				Expect(fakeBuild.SaveEventArgsForCall(0)).To(Equal(event.Error{
					Time:    now.Unix(),
					Message: "fake error message",
					Origin: event.Origin{
						ID: "some-plan-id",
					},
				}))
			})
		})

		Context("when saving the event fails", func() {
			disaster := errors.New("nope")

			BeforeEach(func() {
				fakeBuild.SaveEventReturns(disaster)
			})

			It("logs an error", func() {
				logs := logger.Logs()
				Expect(len(logs)).To(Equal(1))
				Expect(logs[0].Message).To(Equal("test.failed-to-save-error-event"))
				Expect(logs[0].Data).To(Equal(lager.Data{"error": "nope"}))
			})
		})
	})

	Describe("Secrets redaction", func() {
		var (
			runState     exec.RunState
			writer       io.Writer
			writtenBytes int
			writeErr     error
		)

		BeforeEach(func() {
			runState = exec.NewRunState(noopStepper, credVars)
			delegate = engine.NewBuildStepDelegate(fakeBuild, "some-plan-id", runState, fakeClock, fakePolicyChecker, false)

			runState.Get(vars.Reference{Path: "source-param"})
			runState.Get(vars.Reference{Path: "git-key"})
		})

		Context("Stdout", func() {
			Context("single-line secret", func() {
				JustBeforeEach(func() {
					writer = delegate.Stdout()
					writtenBytes, writeErr = writer.Write([]byte("ok super-secret-source ok"))
					writer.(io.Closer).Close()
				})

				It("should be redacted", func() {
					Expect(writeErr).To(BeNil())
					Expect(writtenBytes).To(Equal(len("ok super-secret-source ok")))
					Expect(fakeBuild.SaveEventCallCount()).To(Equal(1))
					Expect(fakeBuild.SaveEventArgsForCall(0)).To(Equal(event.Log{
						Time:    now.Unix(),
						Payload: "ok ((redacted)) ok",
						Origin: event.Origin{
							Source: event.OriginSourceStdout,
							ID:     "some-plan-id",
						},
					}))
				})
			})

			Context("multi-line secret", func() {
				var logLines string

				JustBeforeEach(func() {
					logLines = "ok123ok\nok456ok\nok789ok\n"
					writer = delegate.Stdout()
					writtenBytes, writeErr = writer.Write([]byte(logLines))
					writer.(io.Closer).Close()
				})

				It("should be redacted", func() {
					Expect(writeErr).To(BeNil())
					Expect(writtenBytes).To(Equal(len(logLines)))
					Expect(fakeBuild.SaveEventCallCount()).To(Equal(1))
					Expect(fakeBuild.SaveEventArgsForCall(0)).To(Equal(event.Log{
						Time:    now.Unix(),
						Payload: "ok((redacted))ok\nok((redacted))ok\nok((redacted))ok\n",
						Origin: event.Origin{
							Source: event.OriginSourceStdout,
							ID:     "some-plan-id",
						},
					}))
				})
			})

			Context("multi-line secret with random log chunk", func() {
				JustBeforeEach(func() {
					writer = delegate.Stdout()
					writtenBytes, writeErr = writer.Write([]byte("ok123ok\nok4"))
					writtenBytes, writeErr = writer.Write([]byte("56ok\nok789ok\n"))
					writer.(io.Closer).Close()
				})

				It("should be redacted", func() {
					Expect(fakeBuild.SaveEventCallCount()).To(Equal(2))
					Expect(fakeBuild.SaveEventArgsForCall(0)).To(Equal(event.Log{
						Time:    now.Unix(),
						Payload: "ok((redacted))ok\n",
						Origin: event.Origin{
							Source: event.OriginSourceStdout,
							ID:     "some-plan-id",
						},
					}))
					Expect(fakeBuild.SaveEventArgsForCall(1)).To(Equal(event.Log{
						Time:    now.Unix(),
						Payload: "ok((redacted))ok\nok((redacted))ok\n",
						Origin: event.Origin{
							Source: event.OriginSourceStdout,
							ID:     "some-plan-id",
						},
					}))
				})
			})

			Context("is disabled", func() {
				BeforeEach(func() {
					delegate = engine.NewBuildStepDelegate(fakeBuild, "some-plan-id", runState, fakeClock, fakePolicyChecker, true)
				})

				It("does not redact secrets", func() {
					writer = delegate.Stdout()
					writtenBytes, writeErr = writer.Write([]byte("ok super-secret-source ok"))
					writer.(io.Closer).Close()
					Expect(writeErr).To(BeNil())
					Expect(writtenBytes).To(Equal(len("ok super-secret-source ok")))
					Expect(fakeBuild.SaveEventCallCount()).To(Equal(1))
					Expect(fakeBuild.SaveEventArgsForCall(0)).To(Equal(event.Log{
						Time:    now.Unix(),
						Payload: "ok super-secret-source ok",
						Origin: event.Origin{
							Source: event.OriginSourceStdout,
							ID:     "some-plan-id",
						},
					}))
				})
			})
		})

		Context("Stderr", func() {
			Context("single-line secret", func() {
				JustBeforeEach(func() {
					writer = delegate.Stderr()
					writtenBytes, writeErr = writer.Write([]byte("ok super-secret-source ok"))
					writer.(io.Closer).Close()
				})

				It("should be redacted", func() {
					Expect(writeErr).To(BeNil())
					Expect(writtenBytes).To(Equal(len("ok super-secret-source ok")))
					Expect(fakeBuild.SaveEventCallCount()).To(Equal(1))
					Expect(fakeBuild.SaveEventArgsForCall(0)).To(Equal(event.Log{
						Time:    now.Unix(),
						Payload: "ok ((redacted)) ok",
						Origin: event.Origin{
							Source: event.OriginSourceStderr,
							ID:     "some-plan-id",
						},
					}))
				})
			})

			Context("multi-line secret", func() {
				var logLines string

				JustBeforeEach(func() {
					logLines = "{\nok123ok\nok456ok\nok789ok\n}\n"
					writer = delegate.Stderr()
					writtenBytes, writeErr = writer.Write([]byte(logLines))
					writer.(io.Closer).Close()
				})

				It("should be redacted", func() {
					Expect(writeErr).To(BeNil())
					Expect(writtenBytes).To(Equal(len(logLines)))
					Expect(fakeBuild.SaveEventCallCount()).To(Equal(1))
					Expect(fakeBuild.SaveEventArgsForCall(0)).To(Equal(event.Log{
						Time:    now.Unix(),
						Payload: "{\nok((redacted))ok\nok((redacted))ok\nok((redacted))ok\n}\n",
						Origin: event.Origin{
							Source: event.OriginSourceStderr,
							ID:     "some-plan-id",
						},
					}))
				})
			})

			Context("multi-line secret with random log chunk", func() {
				JustBeforeEach(func() {
					writer = delegate.Stderr()
					writtenBytes, writeErr = writer.Write([]byte("ok123ok\nok4"))
					writtenBytes, writeErr = writer.Write([]byte("56ok\nok789ok\n"))
					writer.(io.Closer).Close()
				})

				It("should be redacted", func() {
					Expect(fakeBuild.SaveEventCallCount()).To(Equal(2))
					Expect(fakeBuild.SaveEventArgsForCall(0)).To(Equal(event.Log{
						Time:    now.Unix(),
						Payload: "ok((redacted))ok\n",
						Origin: event.Origin{
							Source: event.OriginSourceStderr,
							ID:     "some-plan-id",
						},
					}))
					Expect(fakeBuild.SaveEventArgsForCall(1)).To(Equal(event.Log{
						Time:    now.Unix(),
						Payload: "ok((redacted))ok\nok((redacted))ok\n",
						Origin: event.Origin{
							Source: event.OriginSourceStderr,
							ID:     "some-plan-id",
						},
					}))
				})
			})

			Context("is disabled", func() {
				BeforeEach(func() {
					delegate = engine.NewBuildStepDelegate(fakeBuild, "some-plan-id", runState, fakeClock, fakePolicyChecker, true)
				})

				It("does not redact secrets", func() {
					writer = delegate.Stderr()
					writtenBytes, writeErr = writer.Write([]byte("ok super-secret-source ok"))
					writer.(io.Closer).Close()
					Expect(writeErr).To(BeNil())
					Expect(writtenBytes).To(Equal(len("ok super-secret-source ok")))
					Expect(fakeBuild.SaveEventCallCount()).To(Equal(1))
					Expect(fakeBuild.SaveEventArgsForCall(0)).To(Equal(event.Log{
						Time:    now.Unix(),
						Payload: "ok super-secret-source ok",
						Origin: event.Origin{
							Source: event.OriginSourceStderr,
							ID:     "some-plan-id",
						},
					}))
				})
			})
		})
	})

	Describe("ContainerOwner", func() {
		JustBeforeEach(func() {
			delegate.ContainerOwner("some-plan")
		})

		It("should delegate to build", func() {
			Expect(fakeBuild.ContainerOwnerCallCount()).To(Equal(1))

			planId := fakeBuild.ContainerOwnerArgsForCall(0)
			Expect(planId).To(Equal(atc.PlanID("some-plan")))
		})
	})

	Describe("StreamingVolume", func() {
		JustBeforeEach(func() {
			delegate.StreamingVolume(logger, "some-volume", "src-worker", "dest-worker")
		})

		It("saves an event", func() {
			Expect(fakeBuild.SaveEventCallCount()).To(Equal(1))
			e := fakeBuild.SaveEventArgsForCall(0)
			Expect(e.EventType()).To(Equal(atc.EventType("streaming-volume")))
			Expect(e.(event.StreamingVolume).Volume).To(Equal("some-volume"))
			Expect(e.(event.StreamingVolume).SourceWorker).To(Equal("src-worker"))
			Expect(e.(event.StreamingVolume).DestWorker).To(Equal("dest-worker"))
		})
	})

	Describe("WaitingForStreamedVolume", func() {
		JustBeforeEach(func() {
			delegate.WaitingForStreamedVolume(logger, "some-volume", "dest-worker")
		})

		It("saves an event", func() {
			Expect(fakeBuild.SaveEventCallCount()).To(Equal(1))
			e := fakeBuild.SaveEventArgsForCall(0)
			Expect(e.EventType()).To(Equal(atc.EventType("waiting-for-streamed-volume")))
			Expect(e.(event.WaitingForStreamedVolume).Volume).To(Equal("some-volume"))
			Expect(e.(event.WaitingForStreamedVolume).DestWorker).To(Equal("dest-worker"))
		})
	})
})
