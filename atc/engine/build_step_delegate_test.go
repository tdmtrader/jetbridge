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
	"github.com/concourse/concourse/atc/engine"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/imageresolver"
	"github.com/concourse/concourse/atc/imageresolver/imageresolvertesting"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/vars"
)

// A build persists the events handed to it but keeps no record of the call, so
// what the delegate emitted, and in what order, is only observable from
// outside. saveErr injects the one save failure a healthy build cannot produce.
type eventRecordingBuild struct {
	db.Build

	saveErr error
	events  []atc.Event
}

func (b *eventRecordingBuild) SaveEvent(ev atc.Event) error {
	b.events = append(b.events, ev)
	if b.saveErr != nil {
		return b.saveErr
	}
	return b.Build.SaveEvent(ev)
}

// latestVersionErrorScope preserves a healthy persisted scope while injecting
// the one LatestVersion failure that PostgreSQL cannot produce selectively.
type latestVersionErrorScope struct {
	db.ResourceConfigScope
	err error
}

func (scope latestVersionErrorScope) LatestVersion() (db.ResourceConfigVersion, bool, error) {
	return nil, false, scope.err
}

// fixedScopeResourceConfig routes the healthy resource config to the
// selectively failing scope without replacing any other config behavior.
type fixedScopeResourceConfig struct {
	db.ResourceConfig
	scope db.ResourceConfigScope
}

func (config fixedScopeResourceConfig) FindOrCreateScope(*int) (db.ResourceConfigScope, error) {
	return config.scope, nil
}

// fixedResourceConfigFactory routes only this lookup through the decorated
// healthy config; all other factory behavior remains real PostgreSQL behavior.
type fixedResourceConfigFactory struct {
	db.ResourceConfigFactory
	resourceConfig db.ResourceConfig
}

func (factory fixedResourceConfigFactory) FindOrCreateResourceConfig(
	string,
	atc.Source,
	db.ResourceCache,
) (db.ResourceConfig, error) {
	return factory.resourceConfig, nil
}

var _ = Describe("BuildStepDelegate", func() {
	var (
		logger    *lagertest.TestLogger
		fakeClock *fakeclock.FakeClock
		planID    atc.PlanID
		runState  exec.RunState

		credVars vars.StaticVariables

		now = time.Date(1991, 6, 3, 5, 30, 0, 0, time.UTC)

		fixture   *engineDBFixture
		realBuild db.Build
		build     *eventRecordingBuild

		delegate exec.BuildStepDelegate
	)

	BeforeEach(func() {
		logger = lagertest.NewTestLogger("test")

		fakeClock = fakeclock.NewFakeClock(now)
		credVars = vars.StaticVariables{
			"source-param": "super-secret-source",
			"git-key":      "{\n123\n456\n789\n}\n",
		}
		planID = "some-plan-id"

		runState = exec.NewRunState(noopStepper, credVars)

		fixture = useEngineDB()
		_, _, _, realBuild = createEngineJobBuild(
			fixture,
			"some-team",
			atc.PipelineRef{Name: "some-pipeline"},
			atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
			"some-user",
		)
		build = &eventRecordingBuild{Build: realBuild}
	})

	Describe("persisted PostgreSQL state", func() {
		BeforeEach(func() {
			delegate = engine.NewBuildStepDelegate(realBuild, planID, runState, fakeClock, false)
		})

		It("persists the initializing event", func() {
			delegate.Initializing(logger)

			persisted := consumeEngineBuildEvent(realBuild, 0)
			Expect(persisted.EventType()).To(Equal(atc.EventType("initialize")))
		})

		It("persists the finished event", func() {
			delegate.Finished(logger, true)

			persisted := consumeEngineBuildEvent(realBuild, 0)
			Expect(persisted.EventType()).To(Equal(atc.EventType("finish")))
			Expect(persisted).To(BeAssignableToTypeOf(event.Finish{}))
			Expect(persisted.(event.Finish).Succeeded).To(BeTrue())
		})
	})

	// Every value below comes from the same real build row; the only seams are
	// the recorded SaveEvent calls the build itself does not expose and, where a
	// spec needs one, an injected save failure.
	Describe("runtime behavior over the real build", func() {
		BeforeEach(func() {
			delegate = engine.NewBuildStepDelegate(build, planID, runState, fakeClock, false)
		})

		Describe("FetchImage", func() {
			// An image is a registry-image reference the delegate resolves
			// natively: a digest from the reference's scope, or from the
			// registry once. Nothing runs as a step; the stepper fails the
			// spec if anything does.
			var (
				nativeDelegate  exec.BuildStepDelegate
				resourceConfig  db.ResourceConfig
				scope           db.ResourceConfigScope
				registryGetPlan *atc.Plan
				runPlans        []atc.Plan
				fakeResolver    *imageresolvertesting.FakeResolver
			)

			failingStepper := func(p atc.Plan) exec.Step {
				runPlans = append(runPlans, p)
				Fail("no step may run for an image")
				return nil
			}

			buildDelegate := func(configFactory db.ResourceConfigFactory, cacheFactory db.ResourceCacheFactory, resolver imageresolver.Resolver) exec.BuildStepDelegate {
				return engine.NewBuildStepDelegate(
					realBuild, planID, exec.NewRunState(failingStepper, nil), fakeClock, false,
					engine.WithResourceFactories(configFactory, cacheFactory, resolver),
				)
			}

			BeforeEach(func() {
				registryGetPlan = &atc.Plan{
					ID: planID + "/image-get",
					Get: &atc.GetPlan{
						Name:   "image",
						Type:   "registry-image",
						Source: atc.Source{"repository": "my-registry/my-image", "tag": "latest"},
						Params: atc.Params{},
					},
				}

				var err error
				resourceConfig, err = fixture.ResourceConfigFactory.FindOrCreateResourceConfig(
					"registry-image", registryGetPlan.Get.Source, nil,
				)
				Expect(err).NotTo(HaveOccurred())
				scope, err = resourceConfig.FindOrCreateScope(nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(scope.SaveVersions(
					db.SpanContext{}, []atc.Version{{"digest": "sha256:abc123"}},
				)).To(Succeed())

				runPlans = nil
				fakeResolver = new(imageresolvertesting.FakeResolver)
				nativeDelegate = buildDelegate(fixture.ResourceConfigFactory, fixture.ResourceCacheFactory, nil)
			})

			It("resolves the image from the reference's scope without running any step", func() {
				spec, cache, err := nativeDelegate.FetchImage(context.TODO(), *registryGetPlan, false)
				Expect(err).ToNot(HaveOccurred())
				Expect(runPlans).To(BeEmpty())

				Expect(spec).To(Equal(runtime.ImageSpec{ImageURL: "docker:///my-registry/my-image@sha256:abc123"}))

				By("returning the image's resource cache, the parent the step's config is created under")
				Expect(cache.ID()).To(BeNumerically(">", 0))
				Expect(cache.Version()).To(Equal(atc.Version{"digest": "sha256:abc123"}))
				Expect(cache.ResourceConfig().ID()).To(Equal(resourceConfig.ID()))
				Expect(cache.BaseResourceType().Name).To(Equal("registry-image"))
			})

			It("records the resource cache as an image resource for the build", func() {
				_, cache, err := nativeDelegate.FetchImage(context.TODO(), *registryGetPlan, false)
				Expect(err).ToNot(HaveOccurred())
				var count int
				Expect(fixture.Conn.QueryRow(`
				SELECT COUNT(*) FROM build_image_resource_caches
				WHERE build_id = $1 AND resource_cache_id = $2
			`, realBuild.ID(), cache.ID()).Scan(&count)).To(Succeed())
				Expect(count).To(Equal(1))
			})

			It("passes privileged through to the ImageSpec", func() {
				spec, _, err := nativeDelegate.FetchImage(context.TODO(), *registryGetPlan, true)
				Expect(err).ToNot(HaveOccurred())
				Expect(spec.Privileged).To(BeTrue())
			})

			It("pins the reference by tag when the version carries no digest", func() {
				Expect(scope.SaveVersions(db.SpanContext{}, []atc.Version{{"tag": "latest"}})).To(Succeed())
				spec, _, err := nativeDelegate.FetchImage(context.TODO(), *registryGetPlan, false)
				Expect(err).ToNot(HaveOccurred())
				Expect(spec.ImageURL).To(Equal("docker:///my-registry/my-image:latest"))
			})

			Context("when the image's type is not registry-image", func() {
				BeforeEach(func() {
					registryGetPlan.Get.Type = "custom-type"
				})

				It("refuses it by name: nothing fetches an image through another type", func() {
					_, _, err := nativeDelegate.FetchImage(context.TODO(), *registryGetPlan, false)
					Expect(err).To(MatchError(atc.UnsupportedImageTypeError{Type: "custom-type"}))
					Expect(err.Error()).To(ContainSubstring("an image declared with type 'custom-type' is not registry-image"))
					Expect(runPlans).To(BeEmpty())
				})
			})

			Context("when the plan is not a get", func() {
				It("refuses it", func() {
					_, _, err := nativeDelegate.FetchImage(context.TODO(), atc.Plan{ID: "x", Check: &atc.CheckPlan{}}, false)
					Expect(err).To(MatchError("image plan is not a get"))
				})
			})

			Context("when the delegate has no resource factories", func() {
				It("fails rather than fetch the image another way", func() {
					bare := engine.NewBuildStepDelegate(realBuild, planID, exec.NewRunState(failingStepper, nil), fakeClock, false)
					_, _, err := bare.FetchImage(context.TODO(), *registryGetPlan, false)
					Expect(err).To(MatchError("image fetch needs the resource config and resource cache factories"))
					Expect(runPlans).To(BeEmpty())
				})
			})

			Context("when the source has no repository", func() {
				BeforeEach(func() {
					registryGetPlan.Get.Source = atc.Source{"tag": "latest"}
					registryGetPlan.Get.Version = &atc.Version{"digest": "sha256:abc123"}
				})

				It("cannot construct an image reference", func() {
					_, _, err := nativeDelegate.FetchImage(context.TODO(), *registryGetPlan, false)
					Expect(err).To(MatchError(ContainSubstring("cannot construct image URL")))
				})
			})

			Context("when loading the latest resource config version fails", func() {
				BeforeEach(func() {
					wrappedScope := latestVersionErrorScope{
						ResourceConfigScope: scope,
						err:                 errors.New("latest version failed"),
					}
					wrappedConfig := fixedScopeResourceConfig{
						ResourceConfig: resourceConfig,
						scope:          wrappedScope,
					}
					wrappedFactory := fixedResourceConfigFactory{
						ResourceConfigFactory: fixture.ResourceConfigFactory,
						resourceConfig:        wrappedConfig,
					}
					nativeDelegate = buildDelegate(wrappedFactory, fixture.ResourceCacheFactory, fakeResolver)
				})

				It("returns the database error", func() {
					_, _, err := nativeDelegate.FetchImage(context.TODO(), *registryGetPlan, false)
					Expect(err).To(MatchError("get latest version: latest version failed"))
					Expect(runPlans).To(BeEmpty())
				})
			})

			Context("when no cached version exists but a resolver is available", func() {
				BeforeEach(func() {
					registryGetPlan.Get.Source = atc.Source{"repository": "my-registry/no-cache", "tag": "latest"}
					fakeResolver.ResolveReturns("sha256:resolved999", nil)
					nativeDelegate = buildDelegate(fixture.ResourceConfigFactory, fixture.ResourceCacheFactory, fakeResolver)
				})

				It("asks the registry once and saves the answer to the reference's scope", func() {
					spec, cache, err := nativeDelegate.FetchImage(context.TODO(), *registryGetPlan, false)
					Expect(err).ToNot(HaveOccurred())
					Expect(runPlans).To(BeEmpty())

					Expect(fakeResolver.ResolveCallCount()).To(Equal(1))
					_, repo, tag, auth := fakeResolver.ResolveArgsForCall(0)
					Expect(repo).To(Equal("my-registry/no-cache"))
					Expect(tag).To(Equal("latest"))
					Expect(auth).To(BeNil())

					config, err := fixture.ResourceConfigFactory.FindOrCreateResourceConfig(
						"registry-image", registryGetPlan.Get.Source, nil,
					)
					Expect(err).NotTo(HaveOccurred())
					resolvedScope, err := config.FindOrCreateScope(nil)
					Expect(err).NotTo(HaveOccurred())
					latest, found, err := resolvedScope.LatestVersion()
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())
					Expect(atc.Version(latest.Version())).To(Equal(atc.Version{"digest": "sha256:resolved999"}))

					Expect(spec.ImageURL).To(Equal("docker:///my-registry/no-cache@sha256:resolved999"))
					Expect(cache.Version()).To(Equal(atc.Version{"digest": "sha256:resolved999"}))
				})
			})

			Context("when the source carries basic auth credentials", func() {
				BeforeEach(func() {
					fakeResolver.ResolveReturns("sha256:authed123", nil)
					registryGetPlan.Get.Source = atc.Source{
						"repository": "private-registry.com/my-image",
						"tag":        "v2",
						"username":   "myuser",
						"password":   "mypass",
					}
					nativeDelegate = buildDelegate(fixture.ResourceConfigFactory, fixture.ResourceCacheFactory, fakeResolver)
				})

				It("passes them to the resolver", func() {
					spec, _, err := nativeDelegate.FetchImage(context.TODO(), *registryGetPlan, false)
					Expect(err).ToNot(HaveOccurred())

					Expect(fakeResolver.ResolveCallCount()).To(Equal(1))
					_, repo, tag, auth := fakeResolver.ResolveArgsForCall(0)
					Expect(repo).To(Equal("private-registry.com/my-image"))
					Expect(tag).To(Equal("v2"))
					Expect(auth).ToNot(BeNil())
					Expect(auth.Username).To(Equal("myuser"))
					Expect(auth.Password).To(Equal("mypass"))

					Expect(spec.ImageURL).To(Equal("docker:///private-registry.com/my-image@sha256:authed123"))
				})
			})

			Context("when the registry resolve fails", func() {
				BeforeEach(func() {
					registryGetPlan.Get.Source = atc.Source{"repository": "my-registry/failing", "tag": "latest"}
					fakeResolver.ResolveReturns("", fmt.Errorf("registry unreachable"))
					nativeDelegate = buildDelegate(fixture.ResourceConfigFactory, fixture.ResourceCacheFactory, fakeResolver)
				})

				It("returns the error", func() {
					_, _, err := nativeDelegate.FetchImage(context.TODO(), *registryGetPlan, false)
					Expect(err).To(MatchError(ContainSubstring("on-demand image resolve")))
					Expect(err).To(MatchError(ContainSubstring("registry unreachable")))
					Expect(runPlans).To(BeEmpty())
				})
			})

			Context("when no cached version exists and no resolver is available", func() {
				BeforeEach(func() {
					registryGetPlan.Get.Source = atc.Source{"repository": "my-registry/unseen", "tag": "latest"}
				})

				It("fails: an unseen reference is an error, never a fetch by another means", func() {
					_, _, err := nativeDelegate.FetchImage(context.TODO(), *registryGetPlan, false)
					Expect(err).To(MatchError(`no cached version for image "image" and no image resolver`))
					Expect(runPlans).To(BeEmpty())
				})
			})

			Context("when the plan pins a digest", func() {
				BeforeEach(func() {
					registryGetPlan.Get.Source = atc.Source{"repository": "my-registry/pinned"}
					registryGetPlan.Get.Version = &atc.Version{"digest": "sha256:pinned777"}
					fakeResolver.ResolveReturns("sha256:resolver-must-not-be-used", nil)
					nativeDelegate = buildDelegate(fixture.ResourceConfigFactory, fixture.ResourceCacheFactory, fakeResolver)
				})

				It("uses the pinned digest without consulting the scope or the registry", func() {
					spec, cache, err := nativeDelegate.FetchImage(context.TODO(), *registryGetPlan, false)
					Expect(err).ToNot(HaveOccurred())
					Expect(fakeResolver.ResolveCallCount()).To(BeZero())
					Expect(spec.ImageURL).To(Equal("docker:///my-registry/pinned@sha256:pinned777"))
					Expect(cache.Version()).To(Equal(atc.Version{"digest": "sha256:pinned777"}))

					config, err := fixture.ResourceConfigFactory.FindOrCreateResourceConfig(
						"registry-image", registryGetPlan.Get.Source, nil,
					)
					Expect(err).NotTo(HaveOccurred())
					pinnedScope, err := config.FindOrCreateScope(nil)
					Expect(err).NotTo(HaveOccurred())
					_, found, err := pinnedScope.LatestVersion()
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeFalse(), "a pin is one caller's choice, never published as the scope's latest")
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
				Expect(len(build.events)).To(Equal(1))
				Expect(build.events[0]).To(Equal(event.AcrossSubsteps{
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
				Expect(len(build.events)).To(Equal(1))
				Expect(build.events[0]).To(Equal(event.AcrossSubsteps{
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
						writtenBytes, writeErr = writer.Write([]byte("hello\nworld"))
						writer.(io.Closer).Close()
					})

					It("returns the length of the string, and no error", func() {
						Expect(writtenBytes).To(Equal(len("hello\nworld")))
						Expect(writeErr).ToNot(HaveOccurred())
					})

					It("saves a log event", func() {
						Expect(len(build.events)).To(Equal(2))
						Expect(build.events[0]).To(Equal(event.Log{
							Time:    now.Unix(),
							Payload: "hello\n",
							Origin: event.Origin{
								Source: event.OriginSourceStdout,
								ID:     "some-plan-id",
							},
						}))
						Expect(build.events[1]).To(Equal(event.Log{
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
						build.saveErr = disaster
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
							writtenBytes, writeErr = writer.Write([]byte("hello\nworld"))
						})

						It("returns the length of the string, and no error", func() {
							Expect(writtenBytes).To(Equal(len("hello\nworld")))
							Expect(writeErr).ToNot(HaveOccurred())
						})

						It("saves two log events", func() {
							Expect(len(build.events)).To(Equal(1))
							Expect(build.events[0]).To(Equal(event.Log{
								Time:    now.Unix(),
								Payload: "hello\n",
								Origin: event.Origin{
									Source: event.OriginSourceStdout,
									ID:     "some-plan-id",
								},
							}))

							writer.(io.Closer).Close()
							Expect(len(build.events)).To(Equal(2), "second event is cached and only written after writer.Close() is called")
							Expect(build.events[1]).To(Equal(event.Log{
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
							writtenBytes, writeErr = writer.Write([]byte("hello\rworld"))
						})

						It("returns the length of the string, and no error", func() {
							Expect(writtenBytes).To(Equal(len("hello\rworld")))
							Expect(writeErr).ToNot(HaveOccurred())
						})

						It("saves two log events, breaking on \\r", func() {
							Expect(len(build.events)).To(Equal(1))
							Expect(build.events[0]).To(Equal(event.Log{
								Time:    now.Unix(),
								Payload: "hello\r",
								Origin: event.Origin{
									Source: event.OriginSourceStdout,
									ID:     "some-plan-id",
								},
							}))

							writer.(io.Closer).Close()
							Expect(len(build.events)).To(Equal(2), "second event is cached and only written after writer.Close() is called")
							Expect(build.events[1]).To(Equal(event.Log{
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
							writtenBytes, writeErr = writer.Write([]byte("hello\nbeautiful\rworld\n"))
							writer.(io.Closer).Close()
						})

						It("returns the length of the string, and no error", func() {
							Expect(writtenBytes).To(Equal(len("hello\nbeautiful\rworld\n")))
							Expect(writeErr).ToNot(HaveOccurred())
						})

						It("writer prefers breaking on the last \\n over \\r", func() {
							Expect(len(build.events)).To(Equal(1))
							Expect(build.events[0]).To(Equal(event.Log{
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
							writtenBytes, writeErr = writer.Write([]byte("hello world"))
						})

						It("returns the length of the string, no error, and does not log the event", func() {
							Expect(writtenBytes).To(Equal(len("hello world")))
							Expect(writeErr).ToNot(HaveOccurred())
							Expect(len(build.events)).To(Equal(0), "first payload should be entirely cached")
						})

						It("flushes the payload after 1 second", func() {
							Expect(len(build.events)).To(Equal(0), "first payload should be entirely cached")

							fakeClock.Increment(time.Second)
							writtenBytes, writeErr = writer.Write([]byte("!!!"))
							Expect(writtenBytes).To(Equal(len("!!!")))
							Expect(writeErr).ToNot(HaveOccurred())

							Expect(len(build.events)).To(Equal(1), "entire payload should be flushed")
							Expect(build.events[0]).To(Equal(event.Log{
								Time:    fakeClock.Now().Unix(),
								Payload: "hello world!!!",
								Origin: event.Origin{
									Source: event.OriginSourceStdout,
									ID:     "some-plan-id",
								},
							}))

							writer.(io.Closer).Close()
							Expect(len(build.events)).To(Equal(1), "no more events should be written")
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
					It("returns the length of the string, and no error", func() {
						Expect(writtenBytes).To(Equal(len("hello\n")))
						Expect(writeErr).ToNot(HaveOccurred())
					})

					It("saves a log event", func() {
						Expect(len(build.events)).To(Equal(1))
						Expect(build.events[0]).To(Equal(event.Log{
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
						build.saveErr = disaster
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
				It("saves it with the current time", func() {
					Expect(len(build.events)).To(Equal(1))
					Expect(build.events[0]).To(Equal(event.Error{
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
					build.saveErr = disaster
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
				delegate = engine.NewBuildStepDelegate(build, "some-plan-id", runState, fakeClock, false)

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
						Expect(len(build.events)).To(Equal(1))
						Expect(build.events[0]).To(Equal(event.Log{
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
						Expect(len(build.events)).To(Equal(1))
						Expect(build.events[0]).To(Equal(event.Log{
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
						Expect(len(build.events)).To(Equal(2))
						Expect(build.events[0]).To(Equal(event.Log{
							Time:    now.Unix(),
							Payload: "ok((redacted))ok\n",
							Origin: event.Origin{
								Source: event.OriginSourceStdout,
								ID:     "some-plan-id",
							},
						}))
						Expect(build.events[1]).To(Equal(event.Log{
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
						delegate = engine.NewBuildStepDelegate(build, "some-plan-id", runState, fakeClock, true)
					})

					It("does not redact secrets", func() {
						writer = delegate.Stdout()
						writtenBytes, writeErr = writer.Write([]byte("ok super-secret-source ok"))
						writer.(io.Closer).Close()
						Expect(writeErr).To(BeNil())
						Expect(writtenBytes).To(Equal(len("ok super-secret-source ok")))
						Expect(len(build.events)).To(Equal(1))
						Expect(build.events[0]).To(Equal(event.Log{
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
						Expect(len(build.events)).To(Equal(1))
						Expect(build.events[0]).To(Equal(event.Log{
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
						Expect(len(build.events)).To(Equal(1))
						Expect(build.events[0]).To(Equal(event.Log{
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
						Expect(len(build.events)).To(Equal(2))
						Expect(build.events[0]).To(Equal(event.Log{
							Time:    now.Unix(),
							Payload: "ok((redacted))ok\n",
							Origin: event.Origin{
								Source: event.OriginSourceStderr,
								ID:     "some-plan-id",
							},
						}))
						Expect(build.events[1]).To(Equal(event.Log{
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
						delegate = engine.NewBuildStepDelegate(build, "some-plan-id", runState, fakeClock, true)
					})

					It("does not redact secrets", func() {
						writer = delegate.Stderr()
						writtenBytes, writeErr = writer.Write([]byte("ok super-secret-source ok"))
						writer.(io.Closer).Close()
						Expect(writeErr).To(BeNil())
						Expect(writtenBytes).To(Equal(len("ok super-secret-source ok")))
						Expect(len(build.events)).To(Equal(1))
						Expect(build.events[0]).To(Equal(event.Log{
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
			It("should delegate to build", func() {
				Expect(delegate.ContainerOwner("some-plan")).To(Equal(
					db.NewBuildStepContainerOwner(realBuild.ID(), "some-plan", realBuild.TeamID()),
				))
			})
		})

	})
})
