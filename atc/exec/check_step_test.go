package exec_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"code.cloudfoundry.org/clock"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/engine"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/imageresolver"
	"github.com/concourse/concourse/atc/imageresolver/imageresolvertesting"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/policy"
	"github.com/concourse/concourse/atc/resource"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/runtime/runtimetest"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/tracing"
	"github.com/concourse/concourse/vars"
	"github.com/google/go-containerregistry/pkg/authn"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/time/rate"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// scopeDeletingCheckDelegate models the real race where GC deletes a scope
// after it is created but before the check points a pipeline resource at it.
// It records no calls and injects no database error: PointToCheckedConfig sees
// the foreign-key violation produced by the now-absent persisted row.
type scopeDeletingCheckDelegate struct {
	exec.CheckDelegate
	conn db.DbConn
}

func (delegate scopeDeletingCheckDelegate) FindOrCreateScope(config db.ResourceConfig) (db.ResourceConfigScope, error) {
	scope, err := delegate.CheckDelegate.FindOrCreateScope(config)
	if err != nil {
		return nil, err
	}

	result, err := delegate.conn.Exec(`DELETE FROM resource_config_scopes WHERE id = $1`, scope.ID())
	if err != nil {
		return nil, fmt.Errorf("delete scope after creation: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("count deleted scopes: %w", err)
	}
	if deleted != 1 {
		return nil, fmt.Errorf("delete scope after creation: deleted %d rows", deleted)
	}

	return scope, nil
}

func newCheckStepRegistry() *imageresolvertesting.Registry {
	GinkgoHelper()
	registry := imageresolvertesting.NewRegistry()
	DeferCleanup(registry.Close)
	return registry
}

func pushCheckStepImage(registry *imageresolvertesting.Registry, repository, tag string) string {
	GinkgoHelper()
	digest, err := registry.Push(repository, tag)
	Expect(err).NotTo(HaveOccurred())
	return digest
}

func checkStepHeadRequests(registry *imageresolvertesting.Registry) []imageresolvertesting.Request {
	GinkgoHelper()
	var heads []imageresolvertesting.Request
	for _, request := range registry.DrainRequests() {
		if request.Method == http.MethodHead {
			heads = append(heads, request)
		}
	}
	return heads
}

var _ = Describe("CheckStep", func() {
	var (
		ctx    context.Context
		cancel context.CancelFunc

		planID                atc.PlanID
		runState              exec.RunState
		fixture               *execDBFixture
		resourceConfigFactory db.ResourceConfigFactory
		expectedConfig        db.ResourceConfig
		resourceConfigScope   db.ResourceConfigScope
		realBuild             db.Build
		realPipeline          db.Pipeline
		targetTeam            db.Team
		delegateFactory       exec.CheckDelegateFactory
		defaultTimeout        time.Duration = 0

		deleteScopeAfterCreation bool

		stepper      exec.Stepper
		imageStepper *imageFetchStepper

		workerPool      exec.Pool
		workerSeeds     []runtimeWorkerSeed
		chosenWorker    *runtimetest.Worker
		chosenContainer *runtimetest.WorkerContainer
		globalWorker    *runtimetest.Worker
		globalContainer *runtimetest.WorkerContainer

		stepMetadata      exec.StepMetadata
		checkStep         exec.Step
		checkPlan         atc.CheckPlan
		containerMetadata db.ContainerMetadata

		stepOk  bool
		stepErr error

		expectedOwner db.ContainerOwner

		checkStepOpts []exec.CheckStepOption
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		fixture = useExecDB()
		team, pipeline, _, build := createExecJobBuild(
			fixture,
			"some-team",
			atc.PipelineRef{Name: "some-pipeline"},
			atc.Config{
				Jobs: atc.JobConfigs{{Name: "some-job"}},
				Resources: atc.ResourceConfigs{{
					Name:   "some-resource",
					Type:   "some-base-type",
					Source: atc.Source{"some": "super-secret-source"},
				}},
				ResourceTypes: atc.ResourceTypes{{
					Name:   "some-resource-type",
					Type:   "some-base-type",
					Source: atc.Source{"some": "super-secret-source"},
				}},
			},
			"some-user",
		)
		targetTeam = team
		realBuild = build
		realPipeline = pipeline
		resourceConfigFactory = fixture.ResourceConfigFactory
		var err error
		expectedConfig, err = resourceConfigFactory.FindOrCreateResourceConfig(
			"some-base-type",
			atc.Source{"some": "super-secret-source"},
			nil,
		)
		Expect(err).NotTo(HaveOccurred())
		resourceConfigScope, err = expectedConfig.FindOrCreateScope(nil)
		Expect(err).NotTo(HaveOccurred())

		planID = "some-plan-id"

		imageStepper = new(imageFetchStepper)
		stepper = noopStepper

		runState = exec.NewRunState(func(plan atc.Plan) exec.Step {
			return stepper(plan)
		}, vars.StaticVariables{"source-var": "super-secret-source"})

		deleteScopeAfterCreation = false

		delegateFactory = checkDelegateFactory(func(state exec.RunState) exec.CheckDelegate {
			realDelegate := engine.NewCheckDelegate(
				realBuild,
				atc.Plan{ID: planID, Check: &checkPlan},
				state,
				clock.NewClock(),
				db.NewResourceCheckRateLimiter(rate.Inf, 0, time.Minute, nil, time.Minute, clock.NewClock()),
				policy.NoopChecker{},
			)
			if deleteScopeAfterCreation {
				return scopeDeletingCheckDelegate{
					CheckDelegate: realDelegate,
					conn:          fixture.Conn,
				}
			}
			return realDelegate
		})

		stepMetadata = exec.StepMetadata{
			TeamID:  targetTeam.ID(),
			BuildID: realBuild.ID(),
		}
		expectedOwner = db.NewBuildStepContainerOwner(stepMetadata.BuildID, planID, stepMetadata.TeamID)

		chosenWorker = runtimetest.NewWorker("worker").
			WithContainer(
				expectedOwner,
				runtimetest.NewContainer().WithProcess(
					runtime.ProcessSpec{
						Path: "/opt/resource/check",
					},
					runtimetest.ProcessStub{},
				),
				nil,
			)
		chosenContainer = chosenWorker.Containers[0]
		globalWorker = runtimetest.NewWorker("global-worker").
			WithContainer(
				expectedOwner,
				runtimetest.NewContainer().WithProcess(
					runtime.ProcessSpec{
						Path: "/opt/resource/check",
					},
					runtimetest.ProcessStub{},
				),
				nil,
			)
		globalContainer = globalWorker.Containers[0]
		workerSeeds = []runtimeWorkerSeed{
			{Model: chosenWorker, Team: targetTeam},
			{Model: globalWorker},
		}

		containerMetadata = db.ContainerMetadata{}

		checkPlan = atc.CheckPlan{
			Name:   "some-name",
			Type:   "some-base-type",
			Source: atc.Source{"some": "((source-var))"},
			TypeImage: atc.TypeImage{
				BaseType: "some-base-type",
			},
		}

		containerMetadata = db.ContainerMetadata{
			User: "test-user",
		}

	})

	AfterEach(func() {
		cancel()
	})

	JustBeforeEach(func() {
		workerPool = saveRuntimeWorkerPool(fixture, workerSeeds...)
		checkStep = exec.NewCheckStep(
			planID,
			checkPlan,
			stepMetadata,
			resourceConfigFactory,
			containerMetadata,
			workerPool,
			delegateFactory,
			defaultTimeout,
			checkStepOpts...,
		)

		stepOk, stepErr = checkStep.Run(ctx, runState)
	})

	Context("with a reasonable configuration", func() {
		It("emits an Initializing event", func() {
			Expect(execBuildEventTypes(fixture, realBuild)).To(ContainElement(event.EventTypeInitializeCheck))
		})

		Context("when not running", func() {
			BeforeEach(func() {
				checkPlan.Interval = atc.CheckEvery{Never: true}
			})

			It("doesn't run the step and succeeds", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(stepOk).To(BeTrue())

				Expect(chosenContainer.RunningProcesses()).To(BeEmpty())
			})

			Context("when there is a latest version", func() {
				BeforeEach(func() {
					Expect(resourceConfigScope.SaveVersions(
						db.SpanContext{},
						[]atc.Version{{"some": "latest-version"}},
					)).To(Succeed())
				})

				It("stores the latest version as the step result", func() {
					var val atc.Version
					Expect(runState.Result(planID, &val)).To(BeTrue())
					Expect(val).To(Equal(atc.Version{"some": "latest-version"}))
				})
			})

			Context("when there is no version", func() {
				It("does not store a version", func() {
					var dst any
					Expect(runState.Result(planID, &dst)).To(BeFalse())
				})
			})
		})

		Context("running", func() {
			var invokedResource resource.Resource

			BeforeEach(func() {
				invokedResource = resource.Resource{}

				chosenContainer.ProcessDefs[0].Stub.Do = func(_ context.Context, p *runtimetest.Process) error {
					return json.NewDecoder(p.Stdin()).Decode(&invokedResource)
				}
			})

			Context("when given a from version", func() {
				BeforeEach(func() {
					checkPlan.FromVersion = atc.Version{"from": "version"}
				})

				It("constructs the resource with the version", func() {
					Expect(invokedResource.Version).To(Equal(checkPlan.FromVersion))
				})
			})

			Context("when not given a from version", func() {
				BeforeEach(func() {
					checkPlan.FromVersion = nil
					Expect(resourceConfigScope.SaveVersions(
						db.SpanContext{},
						[]atc.Version{{"latest": "version"}},
					)).To(Succeed())
				})

				It("finds the latest version itself - it's a strong, independent check step who dont need no plan", func() {
					Expect(invokedResource.Version).To(Equal(atc.Version{"latest": "version"}))
				})
			})

			Describe("worker selection", func() {
				It("emits a SelectedWorker event", func() {
					Expect(persistedSelectedWorkers(fixture, realBuild)).To(Equal([]string{"worker"}))
				})

				Context("when an exact-team worker and a global worker are available", func() {
					It("runs on the exact-team worker", func() {
						Expect(chosenContainer.RunningProcesses()).To(HaveLen(1))
						Expect(globalContainer.RunningProcesses()).To(BeEmpty())

						databaseWorker, found, err := fixture.WorkerFactory.GetWorker(chosenWorker.Name())
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						Expect(databaseWorker.TeamID()).To(Equal(targetTeam.ID()))

						databaseGlobalWorker, found, err := fixture.WorkerFactory.GetWorker(globalWorker.Name())
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						Expect(databaseGlobalWorker.TeamID()).To(BeZero())
					})
				})

				Context("when selecting a worker fails", func() {
					BeforeEach(func() {
						otherTeam, err := fixture.TeamFactory.CreateTeam(atc.Team{Name: "other-team"})
						Expect(err).NotTo(HaveOccurred())
						workerSeeds = []runtimeWorkerSeed{{Model: chosenWorker, Team: otherTeam}}
					})

					It("returns the no-compatible-worker error", func() {
						Expect(errors.Is(stepErr, worker.ErrNoWorkers)).To(BeTrue())
					})
				})
			})

			Describe("running the check step", func() {
				Context("when using a custom resource type", func() {
					var (
						fetchedImageArtifact *runtimetest.Volume
						imageResourceCache   db.ResourceCache
					)

					BeforeEach(func() {
						checkPlan.TypeImage.GetPlan = &atc.Plan{
							ID: "1/image-get",
							Get: &atc.GetPlan{
								Name:   "some-custom-type",
								Type:   "another-custom-type",
								Source: atc.Source{"some-custom": "((source-var))"},
								Params: atc.Params{"some-custom": "((params-var))"},
							},
						}

						checkPlan.TypeImage.CheckPlan = &atc.Plan{
							ID: "1/image-check",
							Check: &atc.CheckPlan{
								Name:   "some-custom-type",
								Type:   "another-custom-type",
								Source: atc.Source{"some-custom": "((source-var))"},
							},
						}

						checkPlan.Type = "some-custom-type"

						fetchedImageArtifact = runtimetest.NewVolume("some-volume")

						var err error
						imageResourceCache, err = fixture.ResourceCacheFactory.FindOrCreateResourceCache(
							db.ForBuild(realBuild.ID()),
							"another-custom-type",
							atc.Version{"some": "image-version"},
							atc.Source{"some-custom": "super-secret-source"},
							nil,
							nil,
						)
						Expect(err).NotTo(HaveOccurred())

						imageStepper.artifact = fetchedImageArtifact
						imageStepper.cache = imageResourceCache
						stepper = imageStepper.step
					})

					It("uses the fetched resource type image", func() {
						Expect(chosenContainer.Spec.ImageSpec.Privileged).To(BeFalse())
					})

					It("sets the image spec in the container spec", func() {
						Expect(chosenContainer.Spec.ImageSpec).To(Equal(runtime.ImageSpec{
							ImageArtifact: fetchedImageArtifact,
							ResourceType:  "some-custom-type",
						}))
					})

					It("creates the resource config using the image resource cache", func() {
						expectedCustomConfig, err := fixture.ResourceConfigFactory.FindOrCreateResourceConfig(
							"some-custom-type",
							atc.Source{"some": "super-secret-source"},
							imageResourceCache,
						)
						Expect(err).NotTo(HaveOccurred())
						Expect(expectedCustomConfig.CreatedByResourceCache()).NotTo(BeNil())
						Expect(expectedCustomConfig.CreatedByResourceCache().ID()).To(Equal(imageResourceCache.ID()))
					})

					Context("when the resource type is privileged", func() {
						BeforeEach(func() {
							checkPlan.TypeImage.Privileged = true
						})

						It("fetches the image with privileged", func() {
							Expect(chosenContainer.Spec.ImageSpec.Privileged).To(BeTrue())
						})
					})

					Context("when the timeout is bogus", func() {
						BeforeEach(func() {
							checkPlan.Timeout = "bogus"
						})

						It("fails miserably", func() {
							Expect(stepErr).To(MatchError(ContainSubstring("parse timeout: time: invalid duration \"bogus\"")))
						})
					})
				})

				Context("when there is default check timeout", func() {
					BeforeEach(func() {
						defaultTimeout = time.Minute * 30
					})

					It("enforces it on the check", func() {
						t, ok := chosenContainer.ContextOfRun().Deadline()
						Expect(ok).To(BeTrue())
						Expect(t).To(BeTemporally("~", time.Now().Add(time.Minute*30), time.Minute))
					})
				})

				Context("when there is default check timeout and the plan specifies a timeout also", func() {
					BeforeEach(func() {
						defaultTimeout = time.Minute * 30
						checkPlan.Timeout = "1h"
					})

					It("enforces the plan's timeout on the check", func() {
						t, ok := chosenContainer.ContextOfRun().Deadline()
						Expect(ok).To(BeTrue())
						Expect(t).To(BeTemporally("~", time.Now().Add(time.Hour), time.Minute))
					})
				})

				Context("when the plan is for a resource", func() {
					BeforeEach(func() {
						checkPlan.Resource = "some-resource"
						resourceID, found, err := realPipeline.ResourceID(checkPlan.Resource)
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						resourceConfigScope, err = expectedConfig.FindOrCreateScope(&resourceID)
						Expect(err).NotTo(HaveOccurred())
					})

					It("points the resource or resource type to the scope", func() {
						resource, found, err := realPipeline.Resource("some-resource")
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						Expect(resource.ResourceConfigScopeID()).To(Equal(resourceConfigScope.ID()))
					})

					It("uses build step container owner", func() {
						Expect(chosenContainer.RunningProcesses()).To(HaveLen(1))
					})

					It("persists the build-backed check start state", func() {
						lastCheck, err := resourceConfigScope.LastCheck()
						Expect(err).NotTo(HaveOccurred())
						Expect(lastCheck.StartTime).To(BeTemporally("~", time.Now(), time.Minute))
						var lastCheckBuildID sql.NullInt64
						Expect(fixture.Conn.QueryRow(
							`SELECT last_check_build_id FROM resource_config_scopes WHERE id = $1`,
							resourceConfigScope.ID(),
						).Scan(&lastCheckBuildID)).To(Succeed())
						Expect(lastCheckBuildID.Valid).To(BeTrue())
						Expect(lastCheckBuildID.Int64).To(Equal(int64(realBuild.ID())))
					})

					It("releases the production resource-check lock", func() {
						checkedResourceConfigID := resourceConfigScope.ResourceConfig().ID()
						Expect(checkedResourceConfigID).To(Equal(expectedConfig.ID()))
						independentFactory := execLockFactory()
						acquiredLock, acquired, err := independentFactory.Acquire(
							testLogger,
							lock.NewResourceConfigCheckingLockID(checkedResourceConfigID),
						)
						Expect(err).NotTo(HaveOccurred())
						Expect(acquired).To(BeTrue())
						Expect(acquiredLock.Release()).To(Succeed())
					})
				})

				Context("when the plan is nested", func() {
					BeforeEach(func() {
						checkPlan.Resource = ""
						checkPlan.ResourceType = "some-resource-type"
					})

					It("points the resource or resource type to the scope", func() {
						resourceType, found, err := realPipeline.ResourceType("some-resource-type")
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						Expect(resourceType.ResourceConfigScopeID()).To(Equal(resourceConfigScope.ID()))
					})

					It("uses the build step container owner", func() {
						Expect(chosenContainer.RunningProcesses()).To(HaveLen(1))
					})

					It("persists nested check state without a build association", func() {
						lastCheck, err := resourceConfigScope.LastCheck()
						Expect(err).NotTo(HaveOccurred())
						Expect(lastCheck.StartTime).To(BeTemporally("~", time.Now(), time.Minute))
						var lastCheckBuildID sql.NullInt64
						Expect(fixture.Conn.QueryRow(
							`SELECT last_check_build_id FROM resource_config_scopes WHERE id = $1`,
							resourceConfigScope.ID(),
						).Scan(&lastCheckBuildID)).To(Succeed())
						Expect(lastCheckBuildID.Valid).To(BeFalse())
					})
				})

				Context("when the plan specifies a timeout", func() {
					BeforeEach(func() {
						checkPlan.Timeout = "1ms"

						chosenContainer.ProcessDefs[0].Stub.Do = func(ctx context.Context, _ *runtimetest.Process) error {
							select {
							case <-ctx.Done():
								return fmt.Errorf("wrapped: %w", ctx.Err())
							case <-time.After(100 * time.Millisecond):
								return nil
							}
						}
					})

					It("fails without error", func() {
						Expect(stepOk).To(BeFalse())
						Expect(stepErr).To(BeNil())
					})

					It("emits an Errored event", func() {
						Expect(execBuildErrorMessages(fixture, realBuild)).To(Equal([]string{exec.TimeoutLogMessage}))
					})
				})

				Context("uses containerspec", func() {
					It("with certs volume mount", func() {
						Expect(chosenContainer.Spec.CertsBindMount).To(BeTrue())
					})

					It("uses base type for image", func() {
						Expect(chosenContainer.Spec.ImageSpec).To(Equal(runtime.ImageSpec{
							ResourceType: "some-base-type",
						}))
					})

					It("does not set the workdir", func() {
						Expect(chosenContainer.Spec.Dir).To(Equal(""))
					})

					Context("when tracing is enabled", func() {
						var spanRecorder *tracetest.SpanRecorder
						var runSpanContext trace.SpanContext

						BeforeEach(func() {
							spanRecorder = new(tracetest.SpanRecorder)
							tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder), sdktrace.WithSyncer(tracetest.NewInMemoryExporter()))
							tracing.ConfigureTraceProvider(tp)

							chosenContainer.ProcessDefs[0].Stub.Do = func(ctx context.Context, _ *runtimetest.Process) error {
								runSpanContext = tracing.FromContext(ctx).SpanContext()
								return nil
							}
						})

						AfterEach(func() {
							tracing.Configured = false
						})

						It("populates the TRACEPARENT env var", func() {
							Expect(chosenContainer.Spec.Env).To(ContainElement(MatchRegexp(`TRACEPARENT=.+`)))
						})

						It("propagates the step span into the container run", func() {
							ended := spanRecorder.Ended()
							Expect(ended).To(HaveLen(1))
							Expect(runSpanContext).To(Equal(ended[0].SpanContext()))
						})

						It("adds state-transition span events", func() {
							ended := spanRecorder.Ended()
							Expect(ended).To(HaveLen(1))
							eventNames := []string{}
							for _, e := range ended[0].Events() {
								eventNames = append(eventNames, e.Name)
							}
							Expect(eventNames).To(ContainElement("step.initializing"))
							Expect(eventNames).To(ContainElement("step.starting"))
							Expect(eventNames).To(ContainElement("step.finished"))
						})
					})
				})
			})

			Context("with tracing configured", func() {
				var (
					exporter      *tracetest.InMemoryExporter
					tracedVersion atc.Version
				)

				BeforeEach(func() {
					exporter = tracetest.NewInMemoryExporter()
					tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
					tracing.ConfigureTraceProvider(tp)

					tracedVersion = atc.Version{"trace": "persisted"}
					chosenContainer.ProcessDefs[0].Stub.Output = []atc.Version{tracedVersion}
				})

				AfterEach(func() {
					tracing.Configured = false
				})

				It("propagates span context to scope", func() {
					version, found, err := resourceConfigScope.FindVersion(tracedVersion)
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())
					reloaded, err := version.Reload()
					Expect(err).NotTo(HaveOccurred())
					Expect(reloaded).To(BeTrue())
					var traceID string
					for _, span := range exporter.GetSpans() {
						if span.Name == "check" {
							traceID = span.SpanContext.TraceID().String()
						}
					}
					Expect(traceID).NotTo(BeEmpty())
					traceParent := version.SpanContext().Get("traceparent")
					Expect(traceParent).To(ContainSubstring(traceID))
				})
			})

			Context("having RunCheckStep succeed", func() {
				BeforeEach(func() {
					chosenContainer.ProcessDefs[0].Stub.Output = []atc.Version{
						{"version": "1"},
						{"version": "2"},
					}
				})

				It("succeeds", func() {
					Expect(stepOk).To(BeTrue())
				})

				It("saves the versions to the config scope", func() {
					Expect(resourceConfigScope.ResourceConfig().ID()).To(Equal(expectedConfig.ID()))
					Expect(resourceConfigScope.ResourceConfig().OriginBaseResourceType().Name).To(Equal("some-base-type"))

					for _, expectedVersion := range []atc.Version{{"version": "1"}, {"version": "2"}} {
						version, found, err := resourceConfigScope.FindVersion(expectedVersion)
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						reloaded, err := version.Reload()
						Expect(err).NotTo(HaveOccurred())
						Expect(reloaded).To(BeTrue())
						Expect(atc.Version(version.Version())).To(Equal(expectedVersion))
					}
				})

				It("stores the latest version as the step result", func() {
					var val atc.Version
					Expect(runState.Result(planID, &val)).To(BeTrue())
					persistedVersion, found, err := resourceConfigScope.FindVersion(atc.Version{"version": "2"})
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())
					reloaded, err := persistedVersion.Reload()
					Expect(err).NotTo(HaveOccurred())
					Expect(reloaded).To(BeTrue())
					Expect(val).To(Equal(atc.Version(persistedVersion.Version())))
				})

				It("emits a successful Finished event", func() {
					Expect(execBuildFinishEvents(fixture, realBuild)).To(HaveLen(1))
					Expect(execBuildFinishEvents(fixture, realBuild)[0].Succeeded).To(BeTrue())
				})

				Context("when no versions are returned", func() {
					BeforeEach(func() {
						chosenContainer.ProcessDefs[0].Stub.Output = []atc.Version{}
					})

					It("succeeds", func() {
						Expect(stepErr).ToNot(HaveOccurred())
						Expect(stepOk).To(BeTrue())
					})

					It("does not store a version", func() {
						var dst any
						Expect(runState.Result(planID, &dst)).To(BeFalse())
					})
				})

				Context("before running the check", func() {
					var (
						lastCheckAtRun db.LastCheck
						lastCheckErr   error
					)

					BeforeEach(func() {
						chosenContainer.ProcessDefs[0].Stub.Do = func(_ context.Context, _ *runtimetest.Process) error {
							lastCheckAtRun, lastCheckErr = resourceConfigScope.LastCheck()
							return nil
						}
					})

					It("updates the scope's last check start time", func() {
						Expect(lastCheckErr).NotTo(HaveOccurred())
						Expect(lastCheckAtRun.StartTime).To(BeTemporally("~", time.Now(), time.Minute))
						Expect(chosenContainer.RunningProcesses()).To(HaveLen(1))
					})
				})

				Context("after saving", func() {
					It("updates the scope's last check end time", func() {
						lastCheck, err := resourceConfigScope.LastCheck()
						Expect(err).NotTo(HaveOccurred())
						Expect(lastCheck.EndTime).To(BeTemporally(">=", lastCheck.StartTime))
						Expect(lastCheck.Succeeded).To(BeTrue())
					})

				})
			})

			Context("having the check step erroring", func() {
				BeforeEach(func() {
					chosenContainer.ProcessDefs[0].Stub.Err = "run-check-step-err"
				})

				It("errors", func() {
					Expect(stepErr).To(MatchError(ContainSubstring("run-check-step-err")))
				})

				It("updates the scope's last check end time", func() {
					lastCheck, err := resourceConfigScope.LastCheck()
					Expect(err).NotTo(HaveOccurred())
					Expect(lastCheck.EndTime).To(BeTemporally("~", time.Now(), time.Minute))
					Expect(lastCheck.Succeeded).To(BeFalse())
				})

				// Finished is for script success/failure, whereas this is an error
				It("does not emit a Finished event", func() {
					Expect(execBuildFinishEvents(fixture, realBuild)).To(BeEmpty())
				})
			})

			Context("with a script failure", func() {
				BeforeEach(func() {
					chosenContainer.ProcessDefs[0].Stub.ExitStatus = 42
				})

				It("does not error", func() {
					// don't return an error - the script output has already been
					// printed, and emitting an errored event would double it up
					Expect(stepErr).ToNot(HaveOccurred())
				})

				It("updates the scope's last check end time", func() {
					lastCheck, err := resourceConfigScope.LastCheck()
					Expect(err).NotTo(HaveOccurred())
					Expect(lastCheck.EndTime).To(BeTemporally("~", time.Now(), time.Minute))
					Expect(lastCheck.Succeeded).To(BeFalse())
				})

				It("emits a failed Finished event", func() {
					Expect(execBuildFinishEvents(fixture, realBuild)).To(HaveLen(1))
					Expect(execBuildFinishEvents(fixture, realBuild)[0].Succeeded).To(BeFalse())
				})
			})

			Context("when SaveVersions fails with FK violation (scope deleted by GC)", func() {
				BeforeEach(func() {
					chosenContainer.ProcessDefs[0].Stub.Output = []atc.Version{{"version": "deleted-scope"}}
					chosenContainer.ProcessDefs[0].Stub.Do = func(context.Context, *runtimetest.Process) error {
						result, err := fixture.Conn.Exec(
							`DELETE FROM resource_config_scopes WHERE id = $1`,
							resourceConfigScope.ID(),
						)
						Expect(err).NotTo(HaveOccurred())
						deleted, err := result.RowsAffected()
						Expect(err).NotTo(HaveOccurred())
						Expect(deleted).To(Equal(int64(1)))
						return nil
					}
				})

				It("does not error", func() {
					Expect(stepErr).NotTo(HaveOccurred())
				})

				It("finishes with failure (non-fatal)", func() {
					Expect(execBuildFinishEvents(fixture, realBuild)).To(HaveLen(1))
					Expect(execBuildFinishEvents(fixture, realBuild)[0].Succeeded).To(BeFalse())
					var count int
					Expect(fixture.Conn.QueryRow(
						`SELECT COUNT(*) FROM resource_config_scopes WHERE id = $1`,
						resourceConfigScope.ID(),
					).Scan(&count)).To(Succeed())
					Expect(count).To(BeZero())
				})
			})
		})
	})

	Context("having credentials in the config", func() {
		BeforeEach(func() {
			checkPlan.Source = atc.Source{"some": "((missing-cred))"}
		})

		Context("having cred evaluation failing", func() {
			It("errors", func() {
				Expect(stepErr).To(HaveOccurred())
			})
		})
	})

	Context("when PointToCheckedConfig fails with FK violation (scope deleted by GC)", func() {
		BeforeEach(func() {
			checkPlan.Resource = "some-resource"
			resourceID, found, err := realPipeline.ResourceID(checkPlan.Resource)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			resourceConfigScope, err = expectedConfig.FindOrCreateScope(&resourceID)
			Expect(err).NotTo(HaveOccurred())
			deleteScopeAfterCreation = true
		})

		It("does not error", func() {
			Expect(stepErr).NotTo(HaveOccurred())
		})

		It("finishes with failure (non-fatal)", func() {
			Expect(execBuildFinishEvents(fixture, realBuild)).To(HaveLen(1))
			Expect(execBuildFinishEvents(fixture, realBuild)[0].Succeeded).To(BeFalse())
			var count int
			Expect(fixture.Conn.QueryRow(
				`SELECT COUNT(*) FROM resource_config_scopes WHERE id = $1`,
				resourceConfigScope.ID(),
			).Scan(&count)).To(Succeed())
			Expect(count).To(BeZero())
		})
	})

	// MO-02, MO-03, MO-04: Check metrics
	Describe("Check Metrics", func() {
		BeforeEach(func() {
			// Drain any leftover metric state from other tests
			metric.Metrics.ChecksStarted.Delta()
			metric.Metrics.ChecksFinishedWithSuccess.Delta()
			metric.Metrics.ChecksFinishedWithError.Delta()
		})

		Context("when a check succeeds with versions", func() {
			BeforeEach(func() {
				chosenContainer.ProcessDefs[0].Stub.Output = []atc.Version{
					{"version": "1"},
				}
			})

			// MO-02: ChecksStarted incremented at execution start
			It("increments ChecksStarted", func() {
				Expect(stepOk).To(BeTrue())
				Expect(metric.Metrics.ChecksStarted.Delta()).To(BeNumerically("==", 1))
			})

			// MO-03: ChecksFinishedWithSuccess incremented on success
			It("increments ChecksFinishedWithSuccess", func() {
				Expect(stepOk).To(BeTrue())
				Expect(metric.Metrics.ChecksFinishedWithSuccess.Delta()).To(BeNumerically("==", 1))
			})

			It("does not increment ChecksFinishedWithError", func() {
				Expect(stepOk).To(BeTrue())
				Expect(metric.Metrics.ChecksFinishedWithError.Delta()).To(BeNumerically("==", 0))
			})
		})

		Context("when a check fails with non-zero exit", func() {
			BeforeEach(func() {
				chosenContainer.ProcessDefs[0].Stub.ExitStatus = 1
			})

			// MO-04: ChecksFinishedWithError incremented on failure
			It("increments ChecksFinishedWithError", func() {
				Expect(stepOk).To(BeFalse())
				Expect(metric.Metrics.ChecksFinishedWithError.Delta()).To(BeNumerically("==", 1))
			})

			It("increments ChecksStarted", func() {
				Expect(stepOk).To(BeFalse())
				Expect(metric.Metrics.ChecksStarted.Delta()).To(BeNumerically("==", 1))
			})

			It("does not increment ChecksFinishedWithSuccess", func() {
				Expect(stepOk).To(BeFalse())
				Expect(metric.Metrics.ChecksFinishedWithSuccess.Delta()).To(BeNumerically("==", 0))
			})
		})

		Context("when a check times out", func() {
			BeforeEach(func() {
				checkPlan.Timeout = "1ms"

				chosenContainer.ProcessDefs[0].Stub.Do = func(ctx context.Context, _ *runtimetest.Process) error {
					select {
					case <-ctx.Done():
						return fmt.Errorf("wrapped: %w", ctx.Err())
					case <-time.After(100 * time.Millisecond):
						return nil
					}
				}
			})

			It("increments ChecksFinishedWithError on timeout", func() {
				Expect(stepOk).To(BeFalse())
				Expect(metric.Metrics.ChecksFinishedWithError.Delta()).To(BeNumerically("==", 1))
			})

			It("increments ChecksStarted", func() {
				Expect(stepOk).To(BeFalse())
				Expect(metric.Metrics.ChecksStarted.Delta()).To(BeNumerically("==", 1))
			})
		})

		Context("when WaitToRun returns run=false", func() {
			BeforeEach(func() {
				checkPlan.Interval = atc.CheckEvery{Never: true}
			})

			It("does not increment ChecksStarted", func() {
				Expect(stepOk).To(BeTrue())
				Expect(metric.Metrics.ChecksStarted.Delta()).To(BeNumerically("==", 0))
			})
		})
	})

	Context("native resolution for registry-image", func() {
		var registry *imageresolvertesting.Registry

		BeforeEach(func() {
			registry = newCheckStepRegistry()
			registry.DrainRequests()
			checkStepOpts = []exec.CheckStepOption{
				exec.WithCheckResolver(imageresolver.NewResolver(authn.DefaultKeychain)),
			}

			checkPlan = atc.CheckPlan{
				Name: "some-registry-image",
				Type: "registry-image",
				Source: atc.Source{
					"repository": registry.Host() + "/my-project/my-image",
					"tag":        "latest",
				},
				TypeImage: atc.TypeImage{
					BaseType: "registry-image",
				},
			}
		})

		AfterEach(func() {
			checkStepOpts = nil
		})

		Context("when the resolver succeeds", func() {
			var digest string

			BeforeEach(func() {
				digest = pushCheckStepImage(registry, "my-project/my-image", "latest")
				registry.DrainRequests()
			})

			It("resolves natively without creating a container", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(stepOk).To(BeTrue())

				Expect(checkStepHeadRequests(registry)).To(ContainElement(imageresolvertesting.Request{
					Method: http.MethodHead,
					Path:   "/v2/my-project/my-image/manifests/latest",
				}))

				Expect(chosenContainer.RunningProcesses()).To(BeEmpty())
				Expect(globalContainer.RunningProcesses()).To(BeEmpty())
				Expect(persistedSelectedWorkers(fixture, realBuild)).To(BeEmpty())

				var containerCount, volumeCount, artifactCount int
				Expect(fixture.Conn.QueryRow(`
					SELECT
						(SELECT COUNT(*) FROM containers),
						(SELECT COUNT(*) FROM volumes),
						(SELECT COUNT(*) FROM worker_artifacts)
				`).Scan(&containerCount, &volumeCount, &artifactCount)).To(Succeed())
				Expect(containerCount).To(BeZero())
				Expect(volumeCount).To(BeZero())
				Expect(artifactCount).To(BeZero())
			})

			It("saves the resolved version", func() {
				config, err := fixture.ResourceConfigFactory.FindOrCreateResourceConfig(
					checkPlan.Type,
					checkPlan.Source,
					nil,
				)
				Expect(err).NotTo(HaveOccurred())
				scope, err := config.FindOrCreateScope(nil)
				Expect(err).NotTo(HaveOccurred())
				expectedVersion := atc.Version{"digest": digest}
				version, found, err := scope.FindVersion(expectedVersion)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				reloaded, err := version.Reload()
				Expect(err).NotTo(HaveOccurred())
				Expect(reloaded).To(BeTrue())
				Expect(atc.Version(version.Version())).To(Equal(expectedVersion))
			})

			It("stores the result in run state", func() {
				var val atc.Version
				Expect(runState.Result(planID, &val)).To(BeTrue())
				Expect(val).To(Equal(atc.Version{"digest": digest}))
			})

			It("updates check timestamps and metrics", func() {
				Expect(metric.Metrics.ChecksStarted.Delta()).To(BeNumerically(">=", 1))
				Expect(metric.Metrics.ChecksFinishedWithSuccess.Delta()).To(BeNumerically(">=", 1))
			})
		})

		Context("with explicit username/password", func() {
			var digest string

			BeforeEach(func() {
				digest = pushCheckStepImage(registry, "image", "latest")
				registry.RequireBasicAuth("my-user", "my-pass")
				registry.DrainRequests()
				checkPlan.Source = atc.Source{
					"repository": registry.Host() + "/image",
					"username":   "my-user",
					"password":   "my-pass",
				}
			})

			It("passes BasicAuth to the resolver", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(checkStepHeadRequests(registry)).To(ContainElement(imageresolvertesting.Request{
					Method:       http.MethodHead,
					Path:         "/v2/image/manifests/latest",
					HasBasicAuth: true,
				}))
				var version atc.Version
				Expect(runState.Result(planID, &version)).To(BeTrue())
				Expect(version).To(Equal(atc.Version{"digest": digest}))
			})
		})

		Context("when the resolver fails", func() {
			var rejectedDigest string

			BeforeEach(func() {
				rejectedDigest = pushCheckStepImage(registry, "my-project/my-image", "latest")
				registry.RequireBasicAuth("required-user", "required-password")
				registry.DrainRequests()
			})

			It("returns an error and does not save versions", func() {
				Expect(stepErr).To(HaveOccurred())
				Expect(stepErr.Error()).To(ContainSubstring("401 Unauthorized"))
				config, err := fixture.ResourceConfigFactory.FindOrCreateResourceConfig(
					checkPlan.Type,
					checkPlan.Source,
					nil,
				)
				Expect(err).NotTo(HaveOccurred())
				scope, err := config.FindOrCreateScope(nil)
				Expect(err).NotTo(HaveOccurred())
				_, found, err := scope.FindVersion(atc.Version{"digest": rejectedDigest})
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeFalse())
			})

			It("increments the error metric", func() {
				Expect(metric.Metrics.ChecksFinishedWithError.Delta()).To(BeNumerically(">=", 1))
			})
		})

		Context("when the type is not registry-image", func() {
			BeforeEach(func() {
				checkPlan.Type = "git"
				checkPlan.Source = atc.Source{"uri": "https://example.com/repo.git"}
				checkPlan.TypeImage = atc.TypeImage{BaseType: "git"}
			})

			It("falls back to container-based check", func() {
				Expect(checkStepHeadRequests(registry)).To(BeEmpty())
				Expect(chosenContainer.RunningProcesses()).To(HaveLen(1))
				Expect(globalContainer.RunningProcesses()).To(BeEmpty())
			})
		})
	})
})
