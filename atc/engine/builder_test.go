package engine_test

import (
	"net/url"
	"sync"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/engine"
	"github.com/concourse/concourse/atc/exec"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// recordingCoreStepFactory records what the stepper factory asked of the core
// factory and hands back the step scripted for that kind, or an identity step.
//
// The stepper factory is a dispatcher: its whole output is which core
// constructor it calls, with which plan and which metadata, and the real
// constructors fold those into unexported fields of steps that need a worker
// pool to run. Recording the call is the only way to see the dispatch.
type coreStepCall struct {
	plan              atc.Plan
	stepMetadata      exec.StepMetadata
	containerMetadata db.ContainerMetadata
}

type recordingCoreStepFactory struct {
	mu    sync.Mutex
	calls map[string][]coreStepCall
	steps map[string]exec.Step
}

func (f *recordingCoreStepFactory) record(kind string, plan atc.Plan, stepMetadata exec.StepMetadata, containerMetadata db.ContainerMetadata) exec.Step {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = map[string][]coreStepCall{}
	}
	f.calls[kind] = append(f.calls[kind], coreStepCall{plan, stepMetadata, containerMetadata})
	if step, ok := f.steps[kind]; ok {
		return step
	}
	return exec.IdentityStep{}
}

func (f *recordingCoreStepFactory) callCount(kind string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls[kind])
}

func (f *recordingCoreStepFactory) argsForCall(kind string, i int) (atc.Plan, exec.StepMetadata, db.ContainerMetadata) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := f.calls[kind][i]
	return call.plan, call.stepMetadata, call.containerMetadata
}

func (f *recordingCoreStepFactory) GetStep(p atc.Plan, s exec.StepMetadata, c db.ContainerMetadata, _ engine.DelegateFactory) exec.Step {
	return f.record("get", p, s, c)
}
func (f *recordingCoreStepFactory) PutStep(p atc.Plan, s exec.StepMetadata, c db.ContainerMetadata, _ engine.DelegateFactory) exec.Step {
	return f.record("put", p, s, c)
}
func (f *recordingCoreStepFactory) TaskStep(p atc.Plan, s exec.StepMetadata, c db.ContainerMetadata, _ engine.DelegateFactory) exec.Step {
	return f.record("task", p, s, c)
}
func (f *recordingCoreStepFactory) CheckStep(p atc.Plan, s exec.StepMetadata, c db.ContainerMetadata, _ engine.DelegateFactory) exec.Step {
	return f.record("check", p, s, c)
}
func (f *recordingCoreStepFactory) SetPipelineStep(p atc.Plan, s exec.StepMetadata, _ engine.DelegateFactory) exec.Step {
	return f.record("set_pipeline", p, s, db.ContainerMetadata{})
}
func (f *recordingCoreStepFactory) RunPipelineStep(p atc.Plan, s exec.StepMetadata, _ engine.DelegateFactory) exec.Step {
	return f.record("run_pipeline", p, s, db.ContainerMetadata{})
}
func (f *recordingCoreStepFactory) LoadVarStep(p atc.Plan, s exec.StepMetadata, _ engine.DelegateFactory) exec.Step {
	return f.record("load_var", p, s, db.ContainerMetadata{})
}
func (f *recordingCoreStepFactory) ArtifactInputStep(p atc.Plan, _ db.Build) exec.Step {
	return f.record("artifact_input", p, exec.StepMetadata{}, db.ContainerMetadata{})
}
func (f *recordingCoreStepFactory) ArtifactOutputStep(p atc.Plan, _ db.Build) exec.Step {
	return f.record("artifact_output", p, exec.StepMetadata{}, db.ContainerMetadata{})
}

var _ = Describe("Builder", func() {

	Describe("BuildStep", func() {

		var (
			recordedCoreSteps *recordingCoreStepFactory
			fixture           *engineDBFixture

			planFactory    atc.PlanFactory
			stepperFactory engine.StepperFactory
		)

		BeforeEach(func() {
			fixture = useEngineDB()
			recordedCoreSteps = new(recordingCoreStepFactory)

			stepperFactory = engine.NewStepperFactory(
				recordedCoreSteps,
				"http://example.com",
				newCheckRateLimiter(),
				fixture.WorkerFactory,
				nil,
				nil,
				nil,
				nil,
			)

			planFactory = atc.NewPlanFactory(123)
		})

		Context("with a build", func() {
			var (
				build    db.Build
				pipeline db.Pipeline
				job      db.Job

				expectedPlan                     atc.Plan
				expectedMetadataWithCreatedBy    exec.StepMetadata
				expectedMetadataWithoutCreatedBy exec.StepMetadata
			)

			BeforeEach(func() {
				_, pipeline, job, build = createEngineJobBuild(
					fixture,
					"some-team",
					atc.PipelineRef{
						Name:         "some-pipeline",
						InstanceVars: atc.InstanceVars{"branch": "master"},
					},
					atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
					"some-user",
				)
				expectedMetadataWithoutCreatedBy = exec.StepMetadata{
					BuildID:              build.ID(),
					BuildName:            build.Name(),
					TeamID:               pipeline.TeamID(),
					TeamName:             "some-team",
					JobID:                job.ID(),
					JobName:              "some-job",
					PipelineID:           pipeline.ID(),
					PipelineName:         "some-pipeline",
					PipelineInstanceVars: atc.InstanceVars{"branch": "master"},
					InstanceVarsQuery:    url.Values{"vars.branch": []string{`"master"`}},
					ExternalURL:          "http://example.com",
					TaskCacheIdentity:    &atc.TaskCacheIdentity{JobID: job.ID()},
				}

				expectedMetadataWithCreatedBy = expectedMetadataWithoutCreatedBy
				expectedMetadataWithCreatedBy.CreatedBy = "some-user"
			})

			Context("when the build has not been started", func() {
				It("errors, because only a started build carries a supported schema", func() {
					Expect(build.Schema()).To(BeEmpty())

					_, err := stepperFactory.StepperForBuild(build)
					Expect(err).To(HaveOccurred())
				})
			})

			Context("when the build has been started", func() {
				JustBeforeEach(func() {
					started, err := build.Start(expectedPlan)
					Expect(err).ToNot(HaveOccurred())
					Expect(started).To(BeTrue())
					Expect(build.Reload()).To(BeTrue())
					Expect(build.Schema()).To(Equal("exec.v2"))

					stepper, err := stepperFactory.StepperForBuild(build)
					Expect(err).ToNot(HaveOccurred())

					stepper(build.PrivatePlan())
				})

				Context("with a putget in an in_parallel", func() {
					var (
						putPlan               atc.Plan
						dependentGetPlan      atc.Plan
						otherPutPlan          atc.Plan
						otherDependentGetPlan atc.Plan
					)

					BeforeEach(func() {
						putPlan = planFactory.NewPlan(atc.PutPlan{
							Name:                 "some-put",
							Resource:             "some-output-resource",
							Type:                 "put",
							Source:               atc.Source{"some": "source"},
							Params:               atc.Params{"some": "params"},
							ExposeBuildCreatedBy: true,
						})

						otherPutPlan = planFactory.NewPlan(atc.PutPlan{
							Name:                 "some-put-2",
							Resource:             "some-output-resource-2",
							Type:                 "put",
							Source:               atc.Source{"some": "source-2"},
							Params:               atc.Params{"some": "params-2"},
							ExposeBuildCreatedBy: true,
						})

						expectedPlan = planFactory.NewPlan(atc.InParallelPlan{
							Steps: []atc.Plan{
								planFactory.NewPlan(atc.OnSuccessPlan{
									Step: putPlan,
									Next: dependentGetPlan,
								}),
								planFactory.NewPlan(atc.OnSuccessPlan{
									Step: otherPutPlan,
									Next: otherDependentGetPlan,
								}),
							},
						})
					})

					Context("constructing outputs", func() {
						It("constructs the put correctly", func() {
							plan, stepMetadata, containerMetadata := recordedCoreSteps.argsForCall("put", 0)
							Expect(plan).To(Equal(putPlan))
							Expect(stepMetadata).To(Equal(expectedMetadataWithCreatedBy))
							Expect(containerMetadata).To(Equal(db.ContainerMetadata{
								Type:                 db.ContainerTypePut,
								StepName:             "some-put",
								PipelineID:           pipeline.ID(),
								PipelineName:         "some-pipeline",
								PipelineInstanceVars: "{\"branch\":\"master\"}",
								JobID:                job.ID(),
								JobName:              "some-job",
								BuildID:              build.ID(),
								BuildName:            build.Name(),
							}))

							plan, stepMetadata, containerMetadata = recordedCoreSteps.argsForCall("put", 1)
							Expect(plan).To(Equal(otherPutPlan))
							Expect(stepMetadata).To(Equal(expectedMetadataWithCreatedBy))
							Expect(containerMetadata).To(Equal(db.ContainerMetadata{
								Type:                 db.ContainerTypePut,
								StepName:             "some-put-2",
								PipelineID:           pipeline.ID(),
								PipelineName:         "some-pipeline",
								PipelineInstanceVars: "{\"branch\":\"master\"}",
								JobID:                job.ID(),
								JobName:              "some-job",
								BuildID:              build.ID(),
								BuildName:            build.Name(),
							}))
						})
					})
				})

				Context("with a putget in a parallel", func() {
					var (
						putPlan               atc.Plan
						dependentGetPlan      atc.Plan
						otherPutPlan          atc.Plan
						otherDependentGetPlan atc.Plan
					)

					BeforeEach(func() {
						putPlan = planFactory.NewPlan(atc.PutPlan{
							Name:                 "some-put",
							Resource:             "some-output-resource",
							Type:                 "put",
							Source:               atc.Source{"some": "source"},
							Params:               atc.Params{"some": "params"},
							ExposeBuildCreatedBy: true,
						})

						otherPutPlan = planFactory.NewPlan(atc.PutPlan{
							Name:                 "some-put-2",
							Resource:             "some-output-resource-2",
							Type:                 "put",
							Source:               atc.Source{"some": "source-2"},
							Params:               atc.Params{"some": "params-2"},
							ExposeBuildCreatedBy: true,
						})

						expectedPlan = planFactory.NewPlan(atc.InParallelPlan{
							Steps: []atc.Plan{
								planFactory.NewPlan(atc.OnSuccessPlan{
									Step: putPlan,
									Next: dependentGetPlan,
								}),
								planFactory.NewPlan(atc.OnSuccessPlan{
									Step: otherPutPlan,
									Next: otherDependentGetPlan,
								}),
							},
							Limit:    1,
							FailFast: true,
						})
					})

					Context("constructing outputs", func() {
						It("constructs the put correctly", func() {
							plan, stepMetadata, containerMetadata := recordedCoreSteps.argsForCall("put", 0)
							Expect(plan).To(Equal(putPlan))
							Expect(stepMetadata).To(Equal(expectedMetadataWithCreatedBy))
							Expect(containerMetadata).To(Equal(db.ContainerMetadata{
								Type:                 db.ContainerTypePut,
								StepName:             "some-put",
								PipelineID:           pipeline.ID(),
								PipelineName:         "some-pipeline",
								PipelineInstanceVars: "{\"branch\":\"master\"}",
								JobID:                job.ID(),
								JobName:              "some-job",
								BuildID:              build.ID(),
								BuildName:            build.Name(),
							}))

							plan, stepMetadata, containerMetadata = recordedCoreSteps.argsForCall("put", 1)
							Expect(plan).To(Equal(otherPutPlan))
							Expect(stepMetadata).To(Equal(expectedMetadataWithCreatedBy))
							Expect(containerMetadata).To(Equal(db.ContainerMetadata{
								Type:                 db.ContainerTypePut,
								StepName:             "some-put-2",
								PipelineID:           pipeline.ID(),
								PipelineName:         "some-pipeline",
								PipelineInstanceVars: "{\"branch\":\"master\"}",
								JobID:                job.ID(),
								JobName:              "some-job",
								BuildID:              build.ID(),
								BuildName:            build.Name(),
							}))
						})
					})
				})

				Context("with a retry plan", func() {
					var (
						getPlan        atc.Plan
						taskPlan       atc.Plan
						inParallelPlan atc.Plan
						parallelPlan   atc.Plan
						doPlan         atc.Plan
						timeoutPlan    atc.Plan
						retryPlanTwo   atc.Plan
					)

					BeforeEach(func() {
						getPlan = planFactory.NewPlan(atc.GetPlan{
							Name:     "some-get",
							Resource: "some-input-resource",
							Type:     "get",
							Source:   atc.Source{"some": "source"},
							Params:   atc.Params{"some": "params"},
						})

						taskPlan = planFactory.NewPlan(atc.TaskPlan{
							Name:       "some-task",
							Privileged: false,
							Tags:       atc.Tags{"some", "task", "tags"},
							ConfigPath: "some-config-path",
						})

						retryPlanTwo = planFactory.NewPlan(atc.RetryPlan{
							taskPlan,
							taskPlan,
						})

						inParallelPlan = planFactory.NewPlan(atc.InParallelPlan{Steps: []atc.Plan{retryPlanTwo}})

						parallelPlan = planFactory.NewPlan(atc.InParallelPlan{
							Steps:    []atc.Plan{inParallelPlan},
							Limit:    1,
							FailFast: true,
						})

						doPlan = planFactory.NewPlan(atc.DoPlan{parallelPlan})

						timeoutPlan = planFactory.NewPlan(atc.TimeoutPlan{
							Step:     doPlan,
							Duration: "1m",
						})

						expectedPlan = planFactory.NewPlan(atc.RetryPlan{
							getPlan,
							timeoutPlan,
							getPlan,
						})
					})

					It("constructs the retry correctly", func() {
						Expect(*expectedPlan.Retry).To(HaveLen(3))
					})

					It("constructs the first get correctly", func() {
						plan, stepMetadata, containerMetadata := recordedCoreSteps.argsForCall("get", 0)
						expectedPlan := getPlan
						expectedPlan.Attempts = []int{1}
						Expect(plan).To(Equal(expectedPlan))
						Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
						Expect(containerMetadata).To(Equal(db.ContainerMetadata{
							Type:                 db.ContainerTypeGet,
							StepName:             "some-get",
							PipelineID:           pipeline.ID(),
							PipelineName:         "some-pipeline",
							PipelineInstanceVars: "{\"branch\":\"master\"}",
							JobID:                job.ID(),
							JobName:              "some-job",
							BuildID:              build.ID(),
							BuildName:            build.Name(),
							Attempt:              "1",
						}))
					})

					It("constructs the second get correctly", func() {
						plan, stepMetadata, containerMetadata := recordedCoreSteps.argsForCall("get", 1)
						expectedPlan := getPlan
						expectedPlan.Attempts = []int{3}
						Expect(plan).To(Equal(expectedPlan))
						Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
						Expect(containerMetadata).To(Equal(db.ContainerMetadata{
							Type:                 db.ContainerTypeGet,
							StepName:             "some-get",
							PipelineID:           pipeline.ID(),
							PipelineName:         "some-pipeline",
							PipelineInstanceVars: "{\"branch\":\"master\"}",
							JobID:                job.ID(),
							JobName:              "some-job",
							BuildID:              build.ID(),
							BuildName:            build.Name(),
							Attempt:              "3",
						}))
					})

					It("constructs nested retries correctly", func() {
						Expect(*retryPlanTwo.Retry).To(HaveLen(2))
					})

					It("constructs nested steps correctly", func() {
						plan, stepMetadata, containerMetadata := recordedCoreSteps.argsForCall("task", 0)
						expectedPlan := taskPlan
						expectedPlan.Attempts = []int{2, 1}
						Expect(plan).To(Equal(expectedPlan))
						Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
						Expect(containerMetadata).To(Equal(db.ContainerMetadata{
							Type:                 db.ContainerTypeTask,
							StepName:             "some-task",
							PipelineID:           pipeline.ID(),
							PipelineName:         "some-pipeline",
							PipelineInstanceVars: "{\"branch\":\"master\"}",
							JobID:                job.ID(),
							JobName:              "some-job",
							BuildID:              build.ID(),
							BuildName:            build.Name(),
							Attempt:              "2.1",
						}))

						plan, stepMetadata, containerMetadata = recordedCoreSteps.argsForCall("task", 1)
						expectedPlan = taskPlan
						expectedPlan.Attempts = []int{2, 2}
						Expect(plan).To(Equal(expectedPlan))
						Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
						Expect(containerMetadata).To(Equal(db.ContainerMetadata{
							Type:                 db.ContainerTypeTask,
							StepName:             "some-task",
							PipelineID:           pipeline.ID(),
							PipelineName:         "some-pipeline",
							PipelineInstanceVars: "{\"branch\":\"master\"}",
							JobID:                job.ID(),
							JobName:              "some-job",
							BuildID:              build.ID(),
							BuildName:            build.Name(),
							Attempt:              "2.2",
						}))
					})
				})

				Context("with a plan where conditional steps are inside retries", func() {
					var (
						onAbortPlan   atc.Plan
						onErrorPlan   atc.Plan
						onSuccessPlan atc.Plan
						onFailurePlan atc.Plan
						ensurePlan    atc.Plan
						leafPlan      atc.Plan
					)

					BeforeEach(func() {
						leafPlan = planFactory.NewPlan(atc.TaskPlan{
							Name:       "some-task",
							Privileged: false,
							Tags:       atc.Tags{"some", "task", "tags"},
							ConfigPath: "some-config-path",
						})

						onAbortPlan = planFactory.NewPlan(atc.OnAbortPlan{
							Step: leafPlan,
							Next: leafPlan,
						})

						onErrorPlan = planFactory.NewPlan(atc.OnErrorPlan{
							Step: onAbortPlan,
							Next: leafPlan,
						})

						onSuccessPlan = planFactory.NewPlan(atc.OnSuccessPlan{
							Step: onErrorPlan,
							Next: leafPlan,
						})

						onFailurePlan = planFactory.NewPlan(atc.OnFailurePlan{
							Step: onSuccessPlan,
							Next: leafPlan,
						})

						ensurePlan = planFactory.NewPlan(atc.EnsurePlan{
							Step: onFailurePlan,
							Next: leafPlan,
						})

						expectedPlan = planFactory.NewPlan(atc.RetryPlan{
							ensurePlan,
						})
					})

					It("constructs nested steps correctly", func() {
						Expect(recordedCoreSteps.callCount("task")).To(Equal(6))

						_, _, containerMetadata := recordedCoreSteps.argsForCall("task", 0)
						Expect(containerMetadata.Attempt).To(Equal("1"))
						_, _, containerMetadata = recordedCoreSteps.argsForCall("task", 1)
						Expect(containerMetadata.Attempt).To(Equal("1"))
						_, _, containerMetadata = recordedCoreSteps.argsForCall("task", 2)
						Expect(containerMetadata.Attempt).To(Equal("1"))
						_, _, containerMetadata = recordedCoreSteps.argsForCall("task", 3)
						Expect(containerMetadata.Attempt).To(Equal("1"))
						_, _, containerMetadata = recordedCoreSteps.argsForCall("task", 4)
						Expect(containerMetadata.Attempt).To(Equal("1"))
					})
				})

				Context("with a basic plan", func() {

					Context("that contains inputs", func() {
						BeforeEach(func() {
							expectedPlan = planFactory.NewPlan(atc.GetPlan{
								Name:     "some-input",
								Resource: "some-input-resource",
								Type:     "get",
								Tags:     []string{"some", "get", "tags"},
								Version:  &atc.Version{"some": "version"},
								Source:   atc.Source{"some": "source"},
								Params:   atc.Params{"some": "params"},
							})
						})

						It("constructs inputs correctly", func() {
							plan, stepMetadata, containerMetadata := recordedCoreSteps.argsForCall("get", 0)
							Expect(plan).To(Equal(expectedPlan))
							Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
							Expect(containerMetadata).To(Equal(db.ContainerMetadata{
								Type:                 db.ContainerTypeGet,
								StepName:             "some-input",
								PipelineID:           pipeline.ID(),
								PipelineName:         "some-pipeline",
								PipelineInstanceVars: "{\"branch\":\"master\"}",
								JobID:                job.ID(),
								JobName:              "some-job",
								BuildID:              build.ID(),
								BuildName:            build.Name(),
							}))
						})
					})

					Context("that contains tasks", func() {
						BeforeEach(func() {
							expectedPlan = planFactory.NewPlan(atc.TaskPlan{
								Name:          "some-task",
								ConfigPath:    "some-input/build.yml",
								InputMapping:  map[string]string{"foo": "bar"},
								OutputMapping: map[string]string{"baz": "qux"},
							})
						})

						It("constructs tasks correctly", func() {
							plan, stepMetadata, containerMetadata := recordedCoreSteps.argsForCall("task", 0)
							Expect(plan).To(Equal(expectedPlan))
							Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
							Expect(containerMetadata).To(Equal(db.ContainerMetadata{
								Type:                 db.ContainerTypeTask,
								StepName:             "some-task",
								PipelineID:           pipeline.ID(),
								PipelineName:         "some-pipeline",
								PipelineInstanceVars: "{\"branch\":\"master\"}",
								JobID:                job.ID(),
								JobName:              "some-job",
								BuildID:              build.ID(),
								BuildName:            build.Name(),
							}))
						})
					})

					Context("that contains a set_pipeline step", func() {
						BeforeEach(func() {
							expectedPlan = planFactory.NewPlan(atc.SetPipelinePlan{
								Name:     "some-pipeline",
								File:     "some-input/pipeline.yml",
								VarFiles: []string{"foo", "bar"},
								Vars:     map[string]any{"baz": "qux"},
							})
						})

						It("constructs set_pipeline correctly", func() {
							plan, stepMetadata, _ := recordedCoreSteps.argsForCall("set_pipeline", 0)
							Expect(plan).To(Equal(expectedPlan))
							Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
						})
					})

					Context("that contains a run_pipeline step", func() {
						BeforeEach(func() {
							expectedPlan = planFactory.NewPlan(atc.RunPipelinePlan{
								Name:   "some-template",
								Params: atc.RunParams{"ref": "abc123"},
							})
						})

						It("constructs run_pipeline correctly", func() {
							plan, stepMetadata, _ := recordedCoreSteps.argsForCall("run_pipeline", 0)
							Expect(plan).To(Equal(expectedPlan))
							Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
						})
					})

					Context("that contains a load_var step", func() {
						BeforeEach(func() {
							expectedPlan = planFactory.NewPlan(atc.LoadVarPlan{
								Name: "some-var",
								File: "some-input/data.yml",
							})
						})

						It("constructs load_var correctly", func() {
							plan, stepMetadata, _ := recordedCoreSteps.argsForCall("load_var", 0)
							Expect(plan).To(Equal(expectedPlan))
							Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
						})
					})

					Context("that contains a check step", func() {
						BeforeEach(func() {
							expectedPlan = planFactory.NewPlan(atc.CheckPlan{
								Name: "some-check",
							})
						})

						It("constructs the step correctly", func() {
							plan, stepMetadata, containerMetadata := recordedCoreSteps.argsForCall("check", 0)
							Expect(plan).To(Equal(expectedPlan))
							Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
							Expect(containerMetadata).To(Equal(db.ContainerMetadata{
								Type:                 db.ContainerTypeCheck,
								StepName:             "some-check",
								PipelineID:           pipeline.ID(),
								PipelineName:         "some-pipeline",
								PipelineInstanceVars: `{"branch":"master"}`,
								JobID:                job.ID(),
								JobName:              "some-job",
								BuildID:              build.ID(),
								BuildName:            build.Name(),
							}))
						})
					})

					Context("that contains outputs", func() {
						var (
							putPlan          atc.Plan
							dependentGetPlan atc.Plan
						)

						BeforeEach(func() {
							putPlan = planFactory.NewPlan(atc.PutPlan{
								Name:                 "some-put",
								Resource:             "some-output-resource",
								Tags:                 []string{"some", "putget", "tags"},
								Type:                 "put",
								Source:               atc.Source{"some": "source"},
								Params:               atc.Params{"some": "params"},
								ExposeBuildCreatedBy: true,
							})

							dependentGetPlan = planFactory.NewPlan(atc.GetPlan{
								Name:        "some-get",
								Resource:    "some-input-resource",
								Tags:        []string{"some", "putget", "tags"},
								Type:        "get",
								VersionFrom: &putPlan.ID,
								Source:      atc.Source{"some": "source"},
								Params:      atc.Params{"another": "params"},
							})

							expectedPlan = planFactory.NewPlan(atc.OnSuccessPlan{
								Step: putPlan,
								Next: dependentGetPlan,
							})
						})

						It("constructs the put correctly", func() {
							plan, stepMetadata, containerMetadata := recordedCoreSteps.argsForCall("put", 0)
							Expect(plan).To(Equal(putPlan))
							Expect(stepMetadata).To(Equal(expectedMetadataWithCreatedBy))
							Expect(containerMetadata).To(Equal(db.ContainerMetadata{
								Type:                 db.ContainerTypePut,
								StepName:             "some-put",
								PipelineID:           pipeline.ID(),
								PipelineName:         "some-pipeline",
								PipelineInstanceVars: "{\"branch\":\"master\"}",
								JobID:                job.ID(),
								JobName:              "some-job",
								BuildID:              build.ID(),
								BuildName:            build.Name(),
							}))
						})

						It("constructs the dependent get correctly", func() {
							plan, stepMetadata, containerMetadata := recordedCoreSteps.argsForCall("get", 0)
							Expect(plan).To(Equal(dependentGetPlan))
							Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
							Expect(containerMetadata).To(Equal(db.ContainerMetadata{
								Type:                 db.ContainerTypeGet,
								StepName:             "some-get",
								PipelineID:           pipeline.ID(),
								PipelineName:         "some-pipeline",
								PipelineInstanceVars: "{\"branch\":\"master\"}",
								JobID:                job.ID(),
								JobName:              "some-job",
								BuildID:              build.ID(),
								BuildName:            build.Name(),
							}))
						})
					})
				})

				Context("running hooked composes", func() {
					Context("with all the hooks", func() {
						var (
							inputPlan          atc.Plan
							failureTaskPlan    atc.Plan
							successTaskPlan    atc.Plan
							completionTaskPlan atc.Plan
							nextTaskPlan       atc.Plan
						)

						BeforeEach(func() {
							inputPlan = planFactory.NewPlan(atc.GetPlan{
								Name: "some-input",
							})
							failureTaskPlan = planFactory.NewPlan(atc.TaskPlan{
								Name:   "some-failure-task",
								Config: &atc.TaskConfig{},
							})
							successTaskPlan = planFactory.NewPlan(atc.TaskPlan{
								Name:   "some-success-task",
								Config: &atc.TaskConfig{},
							})
							completionTaskPlan = planFactory.NewPlan(atc.TaskPlan{
								Name:   "some-completion-task",
								Config: &atc.TaskConfig{},
							})
							nextTaskPlan = planFactory.NewPlan(atc.TaskPlan{
								Name:   "some-next-task",
								Config: &atc.TaskConfig{},
							})

							expectedPlan = planFactory.NewPlan(atc.OnSuccessPlan{
								Step: planFactory.NewPlan(atc.EnsurePlan{
									Step: planFactory.NewPlan(atc.OnSuccessPlan{
										Step: planFactory.NewPlan(atc.OnFailurePlan{
											Step: inputPlan,
											Next: failureTaskPlan,
										}),
										Next: successTaskPlan,
									}),
									Next: completionTaskPlan,
								}),
								Next: nextTaskPlan,
							})
						})

						It("constructs the step correctly", func() {
							Expect(recordedCoreSteps.callCount("get")).To(Equal(1))
							plan, stepMetadata, containerMetadata := recordedCoreSteps.argsForCall("get", 0)
							Expect(plan).To(Equal(inputPlan))
							Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
							Expect(containerMetadata).To(Equal(db.ContainerMetadata{
								PipelineID:           pipeline.ID(),
								PipelineName:         "some-pipeline",
								PipelineInstanceVars: "{\"branch\":\"master\"}",
								JobID:                job.ID(),
								JobName:              "some-job",
								BuildID:              build.ID(),
								BuildName:            build.Name(),
								StepName:             "some-input",
								Type:                 db.ContainerTypeGet,
							}))
						})

						It("constructs the completion hook correctly", func() {
							Expect(recordedCoreSteps.callCount("task")).To(Equal(4))
							plan, stepMetadata, containerMetadata := recordedCoreSteps.argsForCall("task", 2)
							Expect(plan).To(Equal(completionTaskPlan))
							Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
							Expect(containerMetadata).To(Equal(db.ContainerMetadata{
								PipelineID:           pipeline.ID(),
								PipelineName:         "some-pipeline",
								PipelineInstanceVars: "{\"branch\":\"master\"}",
								JobID:                job.ID(),
								JobName:              "some-job",
								BuildID:              build.ID(),
								BuildName:            build.Name(),
								StepName:             "some-completion-task",
								Type:                 db.ContainerTypeTask,
							}))
						})

						It("constructs the failure hook correctly", func() {
							Expect(recordedCoreSteps.callCount("task")).To(Equal(4))
							plan, stepMetadata, containerMetadata := recordedCoreSteps.argsForCall("task", 0)
							Expect(plan).To(Equal(failureTaskPlan))
							Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
							Expect(containerMetadata).To(Equal(db.ContainerMetadata{
								PipelineID:           pipeline.ID(),
								PipelineName:         "some-pipeline",
								PipelineInstanceVars: "{\"branch\":\"master\"}",
								JobID:                job.ID(),
								JobName:              "some-job",
								BuildID:              build.ID(),
								BuildName:            build.Name(),
								StepName:             "some-failure-task",
								Type:                 db.ContainerTypeTask,
							}))
						})

						It("constructs the success hook correctly", func() {
							Expect(recordedCoreSteps.callCount("task")).To(Equal(4))
							plan, stepMetadata, containerMetadata := recordedCoreSteps.argsForCall("task", 1)
							Expect(plan).To(Equal(successTaskPlan))
							Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
							Expect(containerMetadata).To(Equal(db.ContainerMetadata{
								PipelineID:           pipeline.ID(),
								PipelineName:         "some-pipeline",
								PipelineInstanceVars: "{\"branch\":\"master\"}",
								JobID:                job.ID(),
								JobName:              "some-job",
								BuildID:              build.ID(),
								BuildName:            build.Name(),
								StepName:             "some-success-task",
								Type:                 db.ContainerTypeTask,
							}))
						})

						It("constructs the next step correctly", func() {
							Expect(recordedCoreSteps.callCount("task")).To(Equal(4))
							plan, stepMetadata, containerMetadata := recordedCoreSteps.argsForCall("task", 3)
							Expect(plan).To(Equal(nextTaskPlan))
							Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
							Expect(containerMetadata).To(Equal(db.ContainerMetadata{
								PipelineID:           pipeline.ID(),
								PipelineName:         "some-pipeline",
								PipelineInstanceVars: "{\"branch\":\"master\"}",
								JobID:                job.ID(),
								JobName:              "some-job",
								BuildID:              build.ID(),
								BuildName:            build.Name(),
								StepName:             "some-next-task",
								Type:                 db.ContainerTypeTask,
							}))
						})
					})
				})

				Context("running try steps", func() {
					var inputPlan atc.Plan

					BeforeEach(func() {
						inputPlan = planFactory.NewPlan(atc.GetPlan{
							Name: "some-input",
						})

						expectedPlan = planFactory.NewPlan(atc.TryPlan{
							Step: inputPlan,
						})
					})

					It("constructs the step correctly", func() {
						Expect(recordedCoreSteps.callCount("get")).To(Equal(1))
						plan, stepMetadata, containerMetadata := recordedCoreSteps.argsForCall("get", 0)
						Expect(plan).To(Equal(inputPlan))
						Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
						Expect(containerMetadata).To(Equal(db.ContainerMetadata{
							Type:                 db.ContainerTypeGet,
							StepName:             "some-input",
							PipelineID:           pipeline.ID(),
							PipelineName:         "some-pipeline",
							PipelineInstanceVars: "{\"branch\":\"master\"}",
							JobID:                job.ID(),
							JobName:              "some-job",
							BuildID:              build.ID(),
							BuildName:            build.Name(),
						}))
					})
				})

				// SF-10: Unknown plan type returns IdentityStep
				Context("with a plan matching no known type", func() {
					It("returns an IdentityStep", func() {
						// Construct a Plan with only an ID — no recognized type fields set
						unknownPlan := atc.Plan{ID: "unknown-plan-id"}

						stepper, err := stepperFactory.StepperForBuild(build)
						Expect(err).ToNot(HaveOccurred())

						step := stepper(unknownPlan)
						Expect(step).To(Equal(exec.IdentityStep{}))
					})
				})
			})
		})
	})
})
