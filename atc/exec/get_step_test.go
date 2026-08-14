package exec_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"code.cloudfoundry.org/clock"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/engine"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/atc/policy"
	"github.com/concourse/concourse/atc/resource"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/runtime/runtimetest"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/tracing"
	"github.com/concourse/concourse/vars"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func persistedFinishGets(fixture *execDBFixture, build db.Build) []event.FinishGet {
	GinkgoHelper()
	var finished []event.FinishGet
	for _, e := range execBuildEvents(fixture, build) {
		if finish, ok := e.(event.FinishGet); ok {
			finished = append(finished, finish)
		}
	}
	return finished
}

var _ = Describe("GetStep", func() {
	var (
		ctx    context.Context
		cancel func()

		workerPool      exec.Pool
		workerSeeds     []runtimeWorkerSeed
		workerPoolReady bool
		artifactless    bool
		chosenWorker    *runtimetest.Worker
		chosenContainer *runtimetest.WorkerContainer
		globalWorker    *runtimetest.Worker
		globalContainer *runtimetest.WorkerContainer
		getVolume       *runtimetest.Volume

		fixture              *execDBFixture
		targetTeam           db.Team
		realPipeline         db.Pipeline
		realBuild            db.Build
		pipelineResource     db.Resource
		resourceCacheFactory db.ResourceCacheFactory
		loadOwnedCache       func() db.ResourceCache

		delegateFactory exec.GetDelegateFactory

		lockFactory       lock.LockFactory
		contendingGetLock lock.Lock

		stepper      exec.Stepper
		imageStepper *imageFetchStepper

		getPlan *atc.GetPlan

		runState           exec.RunState
		artifactRepository *build.Repository

		getStep exec.Step
		stepOk  bool
		stepErr error

		containerMetadata = db.ContainerMetadata{
			WorkingDirectory: resource.ResourcesDir("get"),
			PipelineID:       4567,
			Type:             db.ContainerTypeGet,
			StepName:         "some-step",
		}

		stepMetadata  exec.StepMetadata
		expectedOwner db.ContainerOwner

		planID = atc.PlanID("56")

		defaultGetTimeout time.Duration = 0
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		fixture = useExecDB()
		targetTeam, realPipeline, _, realBuild = createExecJobBuild(
			fixture,
			"some-team",
			atc.PipelineRef{Name: "some-pipeline"},
			atc.Config{
				Resources: atc.ResourceConfigs{{Name: "some-resource", Type: "some-base-type", Source: atc.Source{"some": "super-secret-source"}}},
				Jobs:      atc.JobConfigs{{Name: "some-job"}},
			},
			"some-user",
		)
		stepMetadata = exec.StepMetadata{
			TeamID:       targetTeam.ID(),
			TeamName:     targetTeam.Name(),
			BuildID:      realBuild.ID(),
			BuildName:    realBuild.Name(),
			PipelineID:   realPipeline.ID(),
			PipelineName: realPipeline.Name(),
		}
		expectedOwner = db.NewBuildStepContainerOwner(stepMetadata.BuildID, planID, stepMetadata.TeamID)

		resourceCacheFactory = fixture.ResourceCacheFactory
		loadOwnedCache = func() db.ResourceCache {
			var resourceCacheID int
			err := fixture.Conn.QueryRow(
				"SELECT resource_cache_id FROM resource_cache_uses WHERE build_id = $1",
				realBuild.ID(),
			).Scan(&resourceCacheID)
			Expect(err).NotTo(HaveOccurred())
			cache, found, err := fixture.ResourceCacheFactory.FindResourceCacheByID(resourceCacheID)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			return cache
		}

		chosenWorker = runtimetest.NewWorker("worker").
			WithContainer(
				expectedOwner,
				runtimetest.NewContainer().WithProcess(
					runtime.ProcessSpec{
						ID:   "resource",
						Path: "/opt/resource/in",
						Args: []string{resource.ResourcesDir("get")},
					},
					runtimetest.ProcessStub{},
				),
				nil,
			)
		chosenContainer = chosenWorker.Containers[0]
		getVolume = runtimetest.NewVolume("get-volume")
		chosenContainer.Mounts = []runtime.VolumeMount{
			{
				Volume:    getVolume,
				MountPath: resource.ResourcesDir("get"),
			},
		}
		globalWorker = runtimetest.NewWorker("global-worker").
			WithContainer(
				expectedOwner,
				runtimetest.NewContainer().WithProcess(
					runtime.ProcessSpec{
						ID:   "resource",
						Path: "/opt/resource/in",
						Args: []string{resource.ResourcesDir("get")},
					},
					runtimetest.ProcessStub{},
				),
				nil,
			)
		globalContainer = globalWorker.Containers[0]
		globalContainer.Mounts = []runtime.VolumeMount{{
			Volume:    runtimetest.NewVolume("global-get-volume"),
			MountPath: resource.ResourcesDir("get"),
		}}
		workerSeeds = []runtimeWorkerSeed{
			{Model: chosenWorker, Team: targetTeam},
			{Model: globalWorker},
		}
		workerPoolReady = false
		artifactless = false

		lockFactory = fixture.LockFactory
		contendingGetLock = nil

		imageStepper = new(imageFetchStepper)
		stepper = noopStepper

		runState = exec.NewRunState(func(plan atc.Plan) exec.Step {
			return stepper(plan)
		}, vars.StaticVariables{
			"source-var": "super-secret-source",
			"params-var": "super-secret-params",
		})
		artifactRepository = runState.ArtifactRepository()

		delegateFactory = getDelegateFactory(func(state exec.RunState) exec.GetDelegate {
			return engine.NewGetDelegate(realBuild, planID, state, clock.NewClock(), policy.NoopChecker{})
		})

		getPlan = &atc.GetPlan{
			Name: "some-name",
			Type: "some-base-type",
			TypeImage: atc.TypeImage{
				BaseType: "some-base-type",
			},
			Resource: "some-resource",
			Source:   atc.Source{"some": "((source-var))"},
			Params:   atc.Params{"some": "((params-var))"},
			Version:  &atc.Version{"some": "version"},
		}

		var found bool
		var err error
		pipelineResource, found, err = realPipeline.Resource("some-resource")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		resourceConfig, err := fixture.ResourceConfigFactory.FindOrCreateResourceConfig(
			"some-base-type",
			atc.Source{"some": "super-secret-source"},
			nil,
		)
		Expect(err).NotTo(HaveOccurred())
		resourceID := pipelineResource.ID()
		resourceScope, err := resourceConfig.FindOrCreateScope(&resourceID)
		Expect(err).NotTo(HaveOccurred())
		Expect(pipelineResource.SetResourceConfigScope(resourceScope)).To(Succeed())
		Expect(resourceScope.SaveVersions(db.SpanContext{}, []atc.Version{{"some": "version"}})).To(Succeed())
		_, err = pipelineResource.Reload()
		Expect(err).NotTo(HaveOccurred())
		_, err = pipelineResource.UpdateMetadata(
			atc.Version{"some": "version"},
			db.ResourceConfigMetadataFields{{Name: "some", Value: "old-metadata"}},
		)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		cancel()
	})

	JustBeforeEach(func() {
		if !workerPoolReady {
			workerPool = saveRuntimeWorkerPool(fixture, workerSeeds...)
		}
		if artifactless {
			runtimeWorkers := runtimeWorkerFactory{
				chosenWorker.Name(): artifactlessWorker{Worker: chosenWorker},
				globalWorker.Name(): globalWorker,
			}
			workerPool = worker.NewPool(runtimeWorkers, worker.DB{
				WorkerFactory: fixture.WorkerFactory,
				TeamFactory:   fixture.TeamFactory,
				VolumeRepo:    db.NewVolumeRepository(fixture.Conn),
			})
		}

		plan := atc.Plan{
			ID:  atc.PlanID(planID),
			Get: getPlan,
		}

		getStep = exec.NewGetStep(
			plan.ID,
			*plan.Get,
			stepMetadata,
			containerMetadata,
			lockFactory,
			resourceCacheFactory,
			delegateFactory,
			workerPool,
			defaultGetTimeout,
		)

		if contendingGetLock == nil {
			stepOk, stepErr = getStep.Run(ctx, runState)
			return
		}

		runDone := make(chan struct{})
		go func() {
			stepOk, stepErr = getStep.Run(ctx, runState)
			close(runDone)
		}()

		Eventually(func() string {
			return execBuildLog(fixture, realBuild, event.OriginSourceStderr)
		}).Should(ContainSubstring("waiting to acquire resource lock"))
		Consistently(runDone).ShouldNot(BeClosed())

		Expect(contendingGetLock.Release()).To(Succeed())
		contendingGetLock = nil
		Eventually(runDone).Should(BeClosed())
	})

	Describe("persisted PostgreSQL state", func() {
		It("creates the resource cache row", func() {
			var count int
			err := fixture.Conn.QueryRow(
				"SELECT count(*) FROM resource_cache_uses WHERE build_id = $1",
				realBuild.ID(),
			).Scan(&count)
			Expect(err).NotTo(HaveOccurred())
			Expect(count).To(Equal(1))
		})
	})

	It("constructs the resource cache correctly", func() {
		cache := loadOwnedCache()
		Expect(cache.Version()).To(Equal(atc.Version{"some": "version"}))
		config := cache.ResourceConfig()
		Expect(config.OriginBaseResourceType().Name).To(Equal("some-base-type"))
		Expect(config.CreatedByResourceCache()).To(BeNil())
		sourceBytes, err := json.Marshal(atc.Source{"some": "super-secret-source"})
		Expect(err).NotTo(HaveOccurred())
		expectedSourceHash := fmt.Sprintf("%x", sha256.Sum256(sourceBytes))
		var sourceHash string
		err = fixture.Conn.QueryRow("SELECT source_hash FROM resource_configs WHERE id = $1", config.ID()).Scan(&sourceHash)
		Expect(err).NotTo(HaveOccurred())
		Expect(sourceHash).To(Equal(expectedSourceHash))

		paramsBytes, err := json.Marshal(atc.Params{"some": "super-secret-params"})
		Expect(err).NotTo(HaveOccurred())
		expectedParamsHash := fmt.Sprintf("%x", sha256.Sum256(paramsBytes))
		var paramsHash string
		err = fixture.Conn.QueryRow("SELECT params_hash FROM resource_caches WHERE id = $1", cache.ID()).Scan(&paramsHash)
		Expect(err).NotTo(HaveOccurred())
		Expect(paramsHash).To(Equal(expectedParamsHash))
	})

	Context("when using a dynamic version source", func() {
		versionPlanID := atc.PlanID("some-plan-id")

		BeforeEach(func() {
			getPlan.Version = nil
			getPlan.VersionFrom = &versionPlanID
		})

		Context("when the version exists in the build results", func() {
			var version atc.Version

			BeforeEach(func() {
				version = atc.Version{"foo": "bar"}
				runState.StoreResult(versionPlanID, version)
			})

			It("uses the version to create a resource cache", func() {
				Expect(loadOwnedCache().Version()).To(Equal(version))
			})
		})

		Context("when the version does not exist in the build results", func() {
			It("can't resolve version and errors", func() {
				Expect(stepErr).To(Equal(exec.ErrResultMissing))
			})
		})
	})

	Context("when tracing is enabled", func() {
		var spanRecorder *tracetest.SpanRecorder
		var runSpanContext oteltrace.SpanContext

		BeforeEach(func() {
			spanRecorder = new(tracetest.SpanRecorder)
			tp := trace.NewTracerProvider(trace.WithSpanProcessor(spanRecorder), trace.WithSyncer(tracetest.NewInMemoryExporter()))
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

	It("runs with the correct ContainerSpec", func() {
		Expect(chosenContainer.Spec).To(Equal(
			&runtime.ContainerSpec{
				ImageSpec: runtime.ImageSpec{
					ResourceType: "some-base-type",
				},
				TeamID:         stepMetadata.TeamID,
				TeamName:       stepMetadata.TeamName,
				Type:           containerMetadata.Type,
				Env:            stepMetadata.Env(),
				Dir:            resource.ResourcesDir("get"),
				CertsBindMount: true,
			},
		))
	})

	Describe("retrieve from cache or run get step", func() {
		BeforeEach(func() {
			exec.GetResourceLockInterval = 10 * time.Millisecond
		})

		Context("when the cache is present on the selected worker", func() {
			var (
				cacheVolume *runtimetest.Volume
				cached      db.ResourceCache
			)

			BeforeEach(func() {
				chosenContainer.ProcessDefs[0].Stub.Err = "should not run"

				cacheVolume = runtimetest.NewVolume("cache-volume")
				var err error
				cached, err = fixture.ResourceCacheFactory.FindOrCreateResourceCache(
					db.ForBuild(realBuild.ID()),
					"some-base-type",
					atc.Version{"some": "version"},
					atc.Source{"some": "super-secret-source"},
					atc.Params{"some": "super-secret-params"},
					nil,
				)
				Expect(err).NotTo(HaveOccurred())
				Expect(fixture.ResourceCacheFactory.UpdateResourceCacheMetadata(
					cached,
					atc.Metadata{{Name: "some", Value: "metadata"}},
				)).To(Succeed())

				chosenWorker = chosenWorker.WithVolumes(cacheVolume)
				workerSeeds[0].Model = chosenWorker
				workerPool = saveRuntimeWorkerPool(fixture, workerSeeds...)
				workerPoolReady = true

				_, err = targetTeam.SaveWorker(atc.Worker{
					Name:     chosenWorker.Name(),
					Platform: "linux",
					ResourceTypes: []atc.WorkerResourceType{{
						Type:    "some-base-type",
						Image:   "some-base-type-image",
						Version: "some-base-type-version",
					}},
				}, 0)
				Expect(err).NotTo(HaveOccurred())

				creating, err := db.NewVolumeRepository(fixture.Conn).CreateVolumeWithHandle(
					cacheVolume.Handle(), targetTeam.ID(), chosenWorker.Name(), db.VolumeTypeResource,
				)
				Expect(err).NotTo(HaveOccurred())
				created, err := creating.Created()
				Expect(err).NotTo(HaveOccurred())
				workerResourceCache, err := created.InitializeResourceCache(cached)
				Expect(err).NotTo(HaveOccurred())
				Expect(workerResourceCache).NotTo(BeNil())
			})

			It("succeeds", func() {
				Expect(stepErr).ToNot(HaveOccurred())
			})

			It("logs a message to stderr", func() {
				Expect(execBuildLog(fixture, realBuild, event.OriginSourceStderr)).To(MatchRegexp(`INFO.*found.*cache`))
			})

			It("loads metadata from the persisted cache row", func() {
				cache := loadOwnedCache()
				Expect(cache.ID()).To(Equal(cached.ID()))
				metadata, err := fixture.ResourceCacheFactory.ResourceCacheMetadata(cache)
				Expect(err).NotTo(HaveOccurred())
				Expect(metadata).To(Equal(db.ResourceConfigMetadataFields{{Name: "some", Value: "metadata"}}))
			})

			It("uses the matching persisted and runtime cache volume on the selected worker", func() {
				persistedVolume, found, err := db.NewVolumeRepository(fixture.Conn).
					FindResourceCacheVolume(chosenWorker.Name(), cached, realBuild.StartTime())
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(persistedVolume.Handle()).To(Equal(cacheVolume.Handle()))
				Expect(persistedVolume.WorkerName()).To(Equal(chosenWorker.Name()))
				Expect(chosenContainer.RunningProcesses()).To(BeEmpty())
				Expect(globalContainer.RunningProcesses()).To(BeEmpty())
			})

			It("[GS-04] registers the cached artifact with fromCache=true", func() {
				artifact, fromCache, found := artifactRepository.ArtifactFor(build.ArtifactName(getPlan.Name))
				Expect(found).To(BeTrue())
				Expect(fromCache).To(BeTrue())
				Expect(artifact).To(Equal(cacheVolume))
			})
		})

		Context("when the cache is missing from the selected worker", func() {
			BeforeEach(func() {
				chosenContainer.ProcessDefs[0].Stub.Output = resource.VersionResult{
					Version:  atc.Version{"some": "version"},
					Metadata: atc.Metadata{{Name: "some", Value: "metadata"}},
				}
			})

			It("succeeds", func() {
				Expect(stepErr).ToNot(HaveOccurred())
			})

			It("stores the resource cache as the step result", func() {
				var result exec.GetResult
				Expect(runState.Result(planID, &result)).To(BeTrue())
				Expect(result.Name).To(Equal(getPlan.Name))
				Expect(result.ResourceCache.ID()).To(Equal(loadOwnedCache().ID()))
			})

			It("finishes the step via the delegate", func() {
				finished := persistedFinishGets(fixture, realBuild)
				Expect(finished).To(HaveLen(1))
				Expect(finished[0].ExitStatus).To(Equal(0))
				Expect(finished[0].FetchedVersion).To(Equal(atc.Version{"some": "version"}))
				Expect(finished[0].FetchedMetadata).To(Equal(atc.Metadata{{Name: "some", Value: "metadata"}}))
			})

			It("does not log any info messages", func() {
				Expect(execBuildLog(fixture, realBuild, event.OriginSourceStderr)).ToNot(ContainSubstring("INFO"))
			})

			Context("when the lock is held by another session", func() {
				BeforeEach(func() {
					resourceCache, err := fixture.ResourceCacheFactory.FindOrCreateResourceCache(
						db.ForBuild(realBuild.ID()),
						"some-base-type",
						atc.Version{"some": "version"},
						atc.Source{"some": "super-secret-source"},
						atc.Params{"some": "super-secret-params"},
						nil,
					)
					Expect(err).NotTo(HaveOccurred())

					contender := execLockFactory()
					var acquired bool
					contendingGetLock, acquired, err = contender.Acquire(
						testLogger,
						lock.NewResourceGetLockID(strconv.Itoa(resourceCache.ID())+"-"+chosenWorker.Name()),
					)
					Expect(err).NotTo(HaveOccurred())
					Expect(acquired).To(BeTrue())
					DeferCleanup(func() {
						if contendingGetLock != nil {
							Expect(contendingGetLock.Release()).To(Succeed())
						}
					})
				})

				It("succeeds", func() {
					Expect(stepErr).ToNot(HaveOccurred())
				})

				It("logs a message to stderr", func() {
					Expect(execBuildLog(fixture, realBuild, event.OriginSourceStderr)).To(MatchRegexp(`INFO.*waiting.*lock`))
				})

				It("[GS-03] runs the get script once the independently held lock is free", func() {
					finished := persistedFinishGets(fixture, realBuild)
					Expect(finished).To(HaveLen(1))
					Expect(finished[0].ExitStatus).To(Equal(0))
					Expect(finished[0].FetchedVersion).To(Equal(atc.Version{"some": "version"}))
					Expect(finished[0].FetchedMetadata).To(Equal(atc.Metadata{{Name: "some", Value: "metadata"}}))
				})
			})
		})
	})

	Describe("worker selection", func() {
		It("emits a SelectedWorker event", func() {
			Expect(persistedSelectedWorkers(fixture, realBuild)).To(Equal([]string{"worker"}))
		})

		It("runs on the exact-team worker and leaves the global worker idle", func() {
			Expect(chosenContainer.RunningProcesses()).To(HaveLen(1))
			Expect(globalContainer.RunningProcesses()).To(BeEmpty())
		})

		Context("when selecting a worker fails", func() {
			BeforeEach(func() {
				otherTeam, err := fixture.TeamFactory.CreateTeam(atc.Team{Name: "other-team"})
				Expect(err).NotTo(HaveOccurred())
				workerSeeds = []runtimeWorkerSeed{{Model: chosenWorker, Team: otherTeam}}
			})

			It("returns the no-compatible-worker error", func() {
				Expect(stepErr).To(MatchError(worker.ErrNoWorkers))
			})
		})
	})

	Context("when the plan specifies a timeout", func() {
		BeforeEach(func() {
			getPlan.Timeout = "1ms"

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

		It("[SE-04] calls Errored but not Finished on timeout", func() {
			Expect(execBuildErrorMessages(fixture, realBuild)).To(HaveLen(1))
			Expect(persistedFinishGets(fixture, realBuild)).To(BeEmpty())
		})

		Context("when the timeout is bogus", func() {
			BeforeEach(func() {
				getPlan.Timeout = "bogus"
			})

			It("fails miserably", func() {
				Expect(stepErr).To(MatchError("parse timeout: time: invalid duration \"bogus\""))
			})
		})
	})

	Context("when there is default get timeout", func() {
		BeforeEach(func() {
			defaultGetTimeout = time.Minute * 30
		})

		It("enforces it on the get", func() {
			t, ok := chosenContainer.ContextOfRun().Deadline()
			Expect(ok).To(BeTrue())
			Expect(t).To(BeTemporally("~", time.Now().Add(time.Minute*30), time.Minute))
		})
	})

	Context("when there is default get timeout and the plan specifies a timeout also", func() {
		BeforeEach(func() {
			defaultGetTimeout = time.Minute * 30
			getPlan.Timeout = "1h"
		})

		It("enforces the plan's timeout on the get step", func() {
			t, ok := chosenContainer.ContextOfRun().Deadline()
			Expect(ok).To(BeTrue())
			Expect(t).To(BeTemporally("~", time.Now().Add(time.Hour), time.Minute))
		})
	})

	Context("when using a custom resource type", func() {
		var (
			fetchedImageArtifact *runtimetest.Volume
			imageResourceCache   db.ResourceCache
		)

		BeforeEach(func() {
			getPlan.TypeImage.GetPlan = &atc.Plan{
				ID: "1/image-get",
				Get: &atc.GetPlan{
					Name:   "some-custom-type",
					Type:   "another-custom-type",
					Source: atc.Source{"some-custom": "((source-var))"},
					Params: atc.Params{"some-custom": "((params-var))"},
				},
			}

			getPlan.TypeImage.CheckPlan = &atc.Plan{
				ID: "1/image-check",
				Check: &atc.CheckPlan{
					Name:   "some-custom-type",
					Type:   "another-custom-type",
					Source: atc.Source{"some-custom": "((source-var))"},
				},
			}

			getPlan.Type = "some-custom-type"
			getPlan.TypeImage.BaseType = "registry-image"

			fetchedImageArtifact = runtimetest.NewVolume("some-volume")

			var err error
			imageResourceCache, err = fixture.ResourceCacheFactory.FindOrCreateResourceCache(
				db.ForInMemoryBuild(123, time.Unix(1, 0)),
				"another-custom-type",
				atc.Version{"some": "image-version"},
				atc.Source{"some-custom": "super-secret-source"},
				atc.Params{"some-custom": "super-secret-params"},
				nil,
			)
			Expect(err).NotTo(HaveOccurred())

			imageStepper.artifact = fetchedImageArtifact
			imageStepper.cache = imageResourceCache
			stepper = imageStepper.step
		})

		It("uses the same imageResourceCache to create the resourceCache", func() {
			cache := loadOwnedCache()
			Expect(cache.ResourceConfig().CreatedByResourceCache().ID()).To(Equal(imageResourceCache.ID()))
		})

		It("uses the fetched resource type image for the container", func() {
			Expect(chosenContainer.Spec.ImageSpec.Privileged).To(BeFalse())
		})

		It("runs the custom resource type on the exact-team worker", func() {
			Expect(chosenContainer.RunningProcesses()).To(HaveLen(1))
			Expect(globalContainer.RunningProcesses()).To(BeEmpty())
		})

		It("runs with the correct ImageSpec", func() {
			Expect(chosenContainer.Spec.ImageSpec).To(Equal(runtime.ImageSpec{
				ImageArtifact: fetchedImageArtifact,
				ResourceType:  "some-custom-type",
			}))
		})

		Context("when the resource type is privileged", func() {
			BeforeEach(func() {
				getPlan.TypeImage.Privileged = true
			})

			It("fetches the image with privileged", func() {
				Expect(chosenContainer.Spec.ImageSpec.Privileged).To(BeTrue())
			})
		})
	})

	Context("when running the script returns an err", func() {
		disaster := errors.New("oh no")

		BeforeEach(func() {
			chosenContainer.ProcessDefs[0].Stub.Err = disaster.Error()
		})

		It("returns an err", func() {
			Expect(chosenContainer.RunningProcesses()).To(HaveLen(1))
			Expect(stepErr).To(MatchError(disaster))
			Expect(stepOk).To(BeFalse())
		})
	})

	Context("when the script succeeds", func() {
		BeforeEach(func() {
			chosenContainer.ProcessDefs[0].Stub.Output = resource.VersionResult{
				Version:  atc.Version{"some": "version"},
				Metadata: atc.Metadata{{Name: "some", Value: "metadata"}},
			}
		})

		It("registers the resulting artifact in the RunState.ArtifactRepository", func() {
			artifact, fromCache, found := artifactRepository.ArtifactFor(build.ArtifactName(getPlan.Name))
			Expect(artifact).To(Equal(getVolume))
			Expect(found).To(BeTrue())
			Expect(fromCache).To(BeFalse())
		})

		It("initializes the resource cache on the get volume", func() {
			Expect(getVolume.ResourceCacheInitialized).To(BeTrue())
		})

		It("stores the resource cache as the step result", func() {
			var result exec.GetResult
			Expect(runState.Result(planID, &result)).To(BeTrue())
			Expect(result.Name).To(Equal(getPlan.Name))
			Expect(result.ResourceCache.ID()).To(Equal(loadOwnedCache().ID()))
		})

		It("marks the step as succeeded", func() {
			Expect(stepOk).To(BeTrue())
		})

		It("finishes the step via the delegate", func() {
			finished := persistedFinishGets(fixture, realBuild)
			Expect(finished).To(HaveLen(1))
			Expect(finished[0].ExitStatus).To(Equal(0))
			Expect(finished[0].FetchedVersion).To(Equal(atc.Version{"some": "version"}))
			Expect(finished[0].FetchedMetadata).To(Equal(atc.Metadata{{Name: "some", Value: "metadata"}}))
		})

		It("saves the version for the resource", func() {
			version, found, err := pipelineResource.FindVersion(atc.Version{"some": "version"})
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			reloaded, err := version.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(reloaded).To(BeTrue())
			Expect(version.Metadata()).To(Equal(db.ResourceConfigMetadataFields{{Name: "some", Value: "metadata"}}))
		})

		It("adds metadata to the build variables", func() {
			value, _, _ := runState.Get(vars.Reference{Source: ".", Path: getPlan.Name, Fields: []string{"some"}})
			Expect(value).To(Equal("metadata"))
		})

		It("does not return an err", func() {
			Expect(stepErr).ToNot(HaveOccurred())
		})

		It("[SE-04] calls Finished but not Errored on success", func() {
			Expect(persistedFinishGets(fixture, realBuild)).To(HaveLen(1))
			Expect(execBuildErrorMessages(fixture, realBuild)).To(BeEmpty())
		})

		Context("when the source has a repository and version has a digest", func() {
			BeforeEach(func() {
				getPlan.Source = atc.Source{
					"repository": "my-org/my-app",
				}
				getPlan.Version = &atc.Version{
					"digest": "sha256:normalpath123",
				}
				chosenContainer.ProcessDefs[0].Stub.Output = resource.VersionResult{
					Version: atc.Version{"digest": "sha256:normalpath123"},
				}
			})

			It("registers the image ref URL in the artifact repository", func() {
				imageRef, found := artifactRepository.ImageRefFor(build.ArtifactName("some-name"))
				Expect(found).To(BeTrue())
				Expect(imageRef).To(Equal("docker:///my-org/my-app@sha256:normalpath123"))
			})
		})

		Context("when the source has a repository and tag but no digest", func() {
			BeforeEach(func() {
				getPlan.Source = atc.Source{
					"repository": "my-org/my-app",
					"tag":        "v2",
				}
				getPlan.Version = &atc.Version{
					"ref": "abc123",
				}
				chosenContainer.ProcessDefs[0].Stub.Output = resource.VersionResult{
					Version: atc.Version{"ref": "abc123"},
				}
			})

			It("registers the image ref URL with the tag", func() {
				imageRef, found := artifactRepository.ImageRefFor(build.ArtifactName("some-name"))
				Expect(found).To(BeTrue())
				Expect(imageRef).To(Equal("docker:///my-org/my-app:v2"))
			})
		})

		Context("when the source has no repository field", func() {
			It("does not register an image ref", func() {
				_, found := artifactRepository.ImageRefFor(build.ArtifactName("some-name"))
				Expect(found).To(BeFalse())
			})
		})
	})

	Context("when get script fails", func() {
		BeforeEach(func() {
			chosenContainer.ProcessDefs[0].Stub.ExitStatus = 1
		})

		It("does NOT mark the step as succeeded", func() {
			Expect(stepOk).To(BeFalse())
		})

		It("finishes the step via the delegate", func() {
			finished := persistedFinishGets(fixture, realBuild)
			Expect(finished).To(HaveLen(1))
			Expect(finished[0].ExitStatus).ToNot(Equal(0))
			Expect(finished[0].FetchedVersion).To(BeNil())
			Expect(finished[0].FetchedMetadata).To(BeNil())
		})

		It("does not return an err", func() {
			Expect(stepErr).ToNot(HaveOccurred())
		})

		It("does not update the resource version", func() {
			version, found, err := pipelineResource.FindVersion(atc.Version{"some": "version"})
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			reloaded, err := version.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(reloaded).To(BeTrue())
			Expect(version.Metadata()).To(Equal(db.ResourceConfigMetadataFields{{Name: "some", Value: "old-metadata"}}))
		})
	})

	Context("registry-image get step", func() {
		Context("when the resource type is registry-image (no skip_download)", func() {
			BeforeEach(func() {
				getPlan.Type = "registry-image"
				getPlan.Source = atc.Source{
					"repository": "my-org/my-image",
					"tag":        "latest",
				}
				getPlan.Version = &atc.Version{
					"digest": "sha256:abc123def456",
				}
				getPlan.Params = atc.Params{}
				getPlan.TypeImage = atc.TypeImage{
					BaseType: "registry-image",
				}
			})

			It("performs the full get step (selects a worker and creates a container)", func() {
				Expect(stepErr).ToNot(HaveOccurred())
				Expect(stepOk).To(BeTrue())
				Expect(chosenContainer.RunningProcesses()).To(HaveLen(1))
				Expect(globalContainer.RunningProcesses()).To(BeEmpty())
			})

			It("stores a GetResult in the run state", func() {
				var result exec.GetResult
				found := runState.Result(planID, &result)
				Expect(found).To(BeTrue())
				Expect(result.Name).To(Equal("some-name"))
			})

			It("registers the image ref URL for downstream task steps", func() {
				imageRef, found := artifactRepository.ImageRefFor(build.ArtifactName("some-name"))
				Expect(found).To(BeTrue())
				Expect(imageRef).To(Equal("docker:///my-org/my-image@sha256:abc123def456"))
			})

			It("registers the volume as an artifact", func() {
				artifact, _, found := artifactRepository.ArtifactFor(build.ArtifactName("some-name"))
				Expect(found).To(BeTrue())
				Expect(artifact).ToNot(BeNil())
			})
		})

		Context("when the resource type is a custom type without image: field", func() {
			BeforeEach(func() {
				getPlan.Type = "s3-image"
				getPlan.Source = atc.Source{
					"bucket": "my-bucket",
				}
				getPlan.Version = &atc.Version{
					"version": "1.0",
				}
				getPlan.Params = atc.Params{}
				getPlan.TypeImage = atc.TypeImage{
					BaseType: "registry-image",
				}
			})

			It("still runs the full get step", func() {
				Expect(chosenContainer.RunningProcesses()).To(HaveLen(1))
				Expect(globalContainer.RunningProcesses()).To(BeEmpty())
			})

			It("does not register an image ref (no repository in source)", func() {
				_, found := artifactRepository.ImageRefFor(build.ArtifactName("some-name"))
				Expect(found).To(BeFalse())
			})
		})

		Context("when the resource type is NOT registry-image", func() {
			BeforeEach(func() {
				getPlan.Type = "git"
				getPlan.TypeImage = atc.TypeImage{
					BaseType: "git",
				}
			})

			It("still runs the full get step", func() {
				Expect(chosenContainer.RunningProcesses()).To(HaveLen(1))
				Expect(globalContainer.RunningProcesses()).To(BeEmpty())
			})
		})

		Context("when a custom resource type has a repository and digest in source", func() {
			BeforeEach(func() {
				getPlan.Type = "gar-image"
				getPlan.Source = atc.Source{
					"repository": "us-docker.pkg.dev/my-project/repo/my-image",
				}
				getPlan.Version = &atc.Version{
					"digest": "sha256:customdigest999",
				}
				getPlan.Params = atc.Params{}
				getPlan.TypeImage = atc.TypeImage{
					BaseType: "registry-image",
				}
			})

			It("registers the image ref URL even though type is not registry-image", func() {
				imageRef, found := artifactRepository.ImageRefFor(build.ArtifactName("some-name"))
				Expect(found).To(BeTrue())
				Expect(imageRef).To(Equal("docker:///us-docker.pkg.dev/my-project/repo/my-image@sha256:customdigest999"))
			})
		})
	})

	Context("skip_download", func() {
		BeforeEach(func() {
			getPlan.Type = "registry-image"
			getPlan.SkipDownload = true
			getPlan.Source = atc.Source{
				"repository": "my-org/my-image",
				"tag":        "latest",
			}
			getPlan.Version = &atc.Version{
				"digest": "sha256:abc123def456",
			}
			getPlan.Params = atc.Params{}
			getPlan.TypeImage = atc.TypeImage{
				BaseType: "registry-image",
			}
		})

		It("does not select a worker or create a container", func() {
			Expect(stepErr).ToNot(HaveOccurred())
			Expect(stepOk).To(BeTrue())
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

		It("registers a nil artifact", func() {
			artifact, _, found := artifactRepository.ArtifactFor(build.ArtifactName("some-name"))
			Expect(found).To(BeTrue())
			Expect(artifact).To(BeNil())
		})

		It("registers the image ref URL", func() {
			imageRef, found := artifactRepository.ImageRefFor(build.ArtifactName("some-name"))
			Expect(found).To(BeTrue())
			Expect(imageRef).To(Equal("docker:///my-org/my-image@sha256:abc123def456"))
		})

		It("emits Finished with exit status 0", func() {
			finished := persistedFinishGets(fixture, realBuild)
			Expect(finished).To(HaveLen(1))
			Expect(finished[0].ExitStatus).To(Equal(0))
		})
	})

	Context("when the worker hands back no artifact for the fetched volume", func() {
		BeforeEach(func() {
			chosenContainer.ProcessDefs[0].Stub.Output = resource.VersionResult{
				Version:  atc.Version{"some": "version"},
				Metadata: atc.Metadata{{Name: "some", Value: "metadata"}},
			}
			artifactless = true
		})

		It("errors instead of reporting a successful get", func() {
			Expect(stepErr).To(MatchError(ContainSubstring("produced no artifact")))
			Expect(stepOk).To(BeFalse())
		})

		It("registers no artifact", func() {
			_, _, found := artifactRepository.ArtifactFor(build.ArtifactName(getPlan.Name))
			Expect(found).To(BeFalse())
		})

		It("stores no result for the step", func() {
			var result exec.GetResult
			Expect(runState.Result(planID, &result)).To(BeFalse())
		})

		It("never finishes the step with exit status 0", func() {
			Expect(persistedFinishGets(fixture, realBuild)).To(BeEmpty())
		})
	})

})

// artifactlessWorker keeps every behavior of the real worker and hands back no
// artifact for the volume the get step just fetched.
type artifactlessWorker struct {
	runtime.Worker
}

func (artifactlessWorker) ArtifactFromVolume(runtime.Volume) runtime.Artifact {
	return nil
}
