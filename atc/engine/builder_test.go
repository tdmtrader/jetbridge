package engine_test

import (
	"errors"
	"strings"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock/lockfakes"
	"github.com/concourse/concourse/atc/engine"
	"github.com/concourse/concourse/atc/engine/enginefakes"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/policy/policyfakes"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// A healthy persisted build always reports its stored schema. This adapter
// isolates the unsupported-schema branch without replacing the build row.
type schemaResultBuild struct {
	db.Build
	schema string
}

func (build schemaResultBuild) Schema() string {
	return build.schema
}

// A healthy clone cannot fail only the workflow-run association lookup while
// retaining the job, pipeline, and build identity used by the step builder.
type workflowAssociationResultBuild struct {
	db.Build
	association db.AgentWorkflowRunBuildAssociation
	found       bool
	err         error
	calls       *int
}

func (build workflowAssociationResultBuild) AgentWorkflowRunAssociation() (db.AgentWorkflowRunBuildAssociation, bool, error) {
	if build.calls != nil {
		(*build.calls)++
	}
	return build.association, build.found, build.err
}

// A healthy clone cannot fail or synthesize only the resource-capture
// association while retaining the rest of the persisted build identity.
type resourceCaptureAssociationResultBuild struct {
	db.Build
	association db.ResourceCaptureBuildAssociation
	found       bool
	err         error
	calls       *int
}

func (build resourceCaptureAssociationResultBuild) ResourceCaptureTemplateAssociation() (db.ResourceCaptureBuildAssociation, bool, error) {
	if build.calls != nil {
		(*build.calls)++
	}
	return build.association, build.found, build.err
}

// CreatedBy nil and blank are algorithm inputs that Job.CreateBuild cannot
// both encode while preserving one healthy persisted build fixture.
type createdByResultBuild struct {
	db.Build
	createdBy *string
}

func (build createdByResultBuild) CreatedBy() *string {
	return build.createdBy
}

var _ = Describe("Builder", func() {

	Describe("BuildStep", func() {

		var (
			fakeCoreStepFactory *enginefakes.FakeCoreStepFactory
			fakeRateLimiter     *enginefakes.FakeRateLimiter
			fakePolicyChecker   *policyfakes.FakeChecker
			fakeLockFactory     *lockfakes.FakeLockFactory

			planFactory    atc.PlanFactory
			stepperFactory engine.StepperFactory
		)

		BeforeEach(func() {
			fakeCoreStepFactory = new(enginefakes.FakeCoreStepFactory)
			fakeRateLimiter = new(enginefakes.FakeRateLimiter)
			fakePolicyChecker = new(policyfakes.FakeChecker)
			fakeLockFactory = new(lockfakes.FakeLockFactory)

			planFactory = atc.NewPlanFactory(123)
		})

		Context("with a build", func() {
			var (
				team      db.Team
				pipeline  db.Pipeline
				job       db.Job
				realBuild db.Build
				testBuild db.Build

				expectedPlan                     atc.Plan
				expectedMetadataWithCreatedBy    exec.StepMetadata
				expectedMetadataWithoutCreatedBy exec.StepMetadata
			)

			BeforeEach(func() {
				fixture := useEngineDB()
				team, pipeline, job, realBuild = createEngineJobBuild(
					fixture,
					"some-team",
					atc.PipelineRef{
						Name:         "some-pipeline",
						InstanceVars: atc.InstanceVars{"branch": "master"},
					},
					atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
					"some-user",
				)
				started, err := realBuild.Start(atc.Plan{})
				Expect(err).NotTo(HaveOccurred())
				Expect(started).To(BeTrue())
				found, err := realBuild.Reload()
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				testBuild = realBuild

				stepperFactory = engine.NewStepperFactory(
					fakeCoreStepFactory,
					"http://example.com",
					fakeRateLimiter,
					fakePolicyChecker,
					fixture.WorkerFactory,
					fakeLockFactory,
					nil,
					nil,
					nil,
				)

				expectedMetadataWithCreatedBy = exec.StepMetadata{
					BuildID:              realBuild.ID(),
					BuildName:            realBuild.Name(),
					TeamID:               team.ID(),
					TeamName:             team.Name(),
					JobID:                job.ID(),
					JobName:              job.Name(),
					PipelineID:           pipeline.ID(),
					PipelineName:         pipeline.Name(),
					PipelineInstanceVars: pipeline.InstanceVars(),
					InstanceVarsQuery:    realBuild.PipelineRef().QueryParams(),
					ExternalURL:          "http://example.com",
					CreatedBy:            "some-user",
					SnapshotCreatedBy:    "some-user",
				}

				expectedMetadataWithoutCreatedBy = exec.StepMetadata{
					BuildID:              realBuild.ID(),
					BuildName:            realBuild.Name(),
					TeamID:               team.ID(),
					TeamName:             team.Name(),
					JobID:                job.ID(),
					JobName:              job.Name(),
					PipelineID:           pipeline.ID(),
					PipelineName:         pipeline.Name(),
					PipelineInstanceVars: pipeline.InstanceVars(),
					InstanceVarsQuery:    realBuild.PipelineRef().QueryParams(),
					ExternalURL:          "http://example.com",
					SnapshotCreatedBy:    "some-user",
				}
			})

			Describe("retained fault seams", func() {
				Context("when the build has the wrong schema", func() {
					BeforeEach(func() {
						testBuild = schemaResultBuild{Build: realBuild, schema: "not-schema"}
					})

					It("errors", func() {
						_, err := stepperFactory.StepperForBuild(testBuild)
						Expect(err).To(HaveOccurred())
					})
				})

				Context("when the workflow-run association lookup fails", func() {
					BeforeEach(func() {
						testBuild = workflowAssociationResultBuild{
							Build: realBuild,
							err:   errors.New("association unavailable"),
						}
					})

					It("fails closed before constructing a stepper", func() {
						_, err := stepperFactory.StepperForBuild(testBuild)
						Expect(err).To(MatchError(ContainSubstring("association unavailable")))
						Expect(fakeCoreStepFactory.TaskStepCallCount()).To(Equal(0))
						Expect(fakeCoreStepFactory.AgentStepCallCount()).To(Equal(0))
					})
				})

				Context("when the resource-capture association lookup fails", func() {
					BeforeEach(func() {
						testBuild = resourceCaptureAssociationResultBuild{
							Build: realBuild,
							err:   errors.New("capture association unavailable"),
						}
					})

					It("fails closed before constructing a stepper", func() {
						_, err := stepperFactory.StepperForBuild(testBuild)
						Expect(err).To(MatchError(ContainSubstring("capture association unavailable")))
						Expect(fakeCoreStepFactory.TaskStepCallCount()).To(Equal(0))
					})
				})

				Context("when a build claims to be both a workflow run and a resource capture", func() {
					BeforeEach(func() {
						workflowBuild := workflowAssociationResultBuild{
							Build: realBuild,
							association: db.AgentWorkflowRunBuildAssociation{
								WorkflowDefinitionID: 77, WorkflowVersion: 3,
								WorkflowRunID: snapshot.WorkflowRunID(9007199254740993),
							},
							found: true,
						}
						testBuild = resourceCaptureAssociationResultBuild{
							Build: workflowBuild,
							association: db.ResourceCaptureBuildAssociation{
								TemplatePipelineID: 41,
								TemplateName:       "agent-resource-capture-" + strings.Repeat("a", 24) + "-" + strings.Repeat("b", 12),
							},
							found: true,
						}
					})

					It("refuses to build a stepper at all", func() {
						_, err := stepperFactory.StepperForBuild(testBuild)
						Expect(err).To(MatchError(ContainSubstring("both a workflow run and a resource capture")))
						Expect(fakeCoreStepFactory.TaskStepCallCount()).To(Equal(0))
					})
				})
			})

			Describe("persisted PostgreSQL state", func() {
				Context("when the build has the right schema", func() {
					JustBeforeEach(func() {
						stepper, err := stepperFactory.StepperForBuild(testBuild)
						Expect(err).ToNot(HaveOccurred())

						stepper(expectedPlan)
					})

					Describe("retained association and principal seams", func() {
						Describe("snapshot producer principal fallback", func() {
							BeforeEach(func() {
								expectedPlan = planFactory.NewPlan(atc.InParallelPlan{Steps: []atc.Plan{
									planFactory.NewPlan(atc.TaskPlan{Name: "typed-task"}),
									planFactory.NewPlan(atc.AgentPlan{Name: "typed-agent", Prompt: "p"}),
								}})
							})

							assertFallback := func() {
								Expect(fakeCoreStepFactory.TaskStepCallCount()).To(Equal(1))
								_, taskMetadata, _, _ := fakeCoreStepFactory.TaskStepArgsForCall(0)
								Expect(taskMetadata.SnapshotCreatedBy).To(Equal("concourse"))
								Expect(taskMetadata.CreatedBy).To(BeEmpty())

								Expect(fakeCoreStepFactory.AgentStepCallCount()).To(Equal(1))
								_, agentMetadata, _, _ := fakeCoreStepFactory.AgentStepArgsForCall(0)
								Expect(agentMetadata.SnapshotCreatedBy).To(Equal("concourse"))
								Expect(agentMetadata.CreatedBy).To(BeEmpty())
							}

							Context("when the build creator is absent", func() {
								BeforeEach(func() {
									testBuild = createdByResultBuild{Build: realBuild}
								})

								It("uses the automated principal without exposing it", assertFallback)
							})

							Context("when the build creator is blank", func() {
								BeforeEach(func() {
									blank := "  "
									testBuild = createdByResultBuild{Build: realBuild, createdBy: &blank}
								})

								It("uses the automated principal without exposing it", assertFallback)
							})
						})

						Context("with an authenticated workflow-run build association", func() {
							var associationCalls int

							BeforeEach(func() {
								association := db.AgentWorkflowRunBuildAssociation{
									WorkflowDefinitionID: 77,
									WorkflowVersion:      3,
									WorkflowRunID:        snapshot.WorkflowRunID(9007199254740993),
								}
								testBuild = workflowAssociationResultBuild{
									Build:       realBuild,
									association: association,
									found:       true,
									calls:       &associationCalls,
								}
								expectedPlan = planFactory.NewPlan(atc.InParallelPlan{Steps: []atc.Plan{
									planFactory.NewPlan(atc.TaskPlan{Name: "typed-task"}),
									planFactory.NewPlan(atc.AgentPlan{Name: "typed-agent", Prompt: "p"}),
								}})
							})

							It("copies the exact pair to every task and agent without re-querying", func() {
								Expect(associationCalls).To(Equal(1))

								Expect(fakeCoreStepFactory.TaskStepCallCount()).To(Equal(1))
								_, taskMetadata, _, _ := fakeCoreStepFactory.TaskStepArgsForCall(0)
								Expect(taskMetadata.WorkflowDefinitionID).NotTo(BeNil())
								Expect(*taskMetadata.WorkflowDefinitionID).To(Equal(77))
								Expect(taskMetadata.WorkflowVersion).NotTo(BeNil())
								Expect(*taskMetadata.WorkflowVersion).To(Equal(3))
								Expect(taskMetadata.WorkflowRunID).NotTo(BeNil())
								Expect(*taskMetadata.WorkflowRunID).To(Equal(snapshot.WorkflowRunID(9007199254740993)))

								Expect(fakeCoreStepFactory.AgentStepCallCount()).To(Equal(1))
								_, agentMetadata, _, _ := fakeCoreStepFactory.AgentStepArgsForCall(0)
								Expect(agentMetadata.WorkflowDefinitionID).NotTo(BeNil())
								Expect(*agentMetadata.WorkflowDefinitionID).To(Equal(77))
								Expect(agentMetadata.WorkflowVersion).NotTo(BeNil())
								Expect(*agentMetadata.WorkflowVersion).To(Equal(3))
								Expect(agentMetadata.WorkflowRunID).NotTo(BeNil())
								Expect(*agentMetadata.WorkflowRunID).To(Equal(snapshot.WorkflowRunID(9007199254740993)))
							})
						})

						Context("with an authenticated resource-capture template association", func() {
							templateName := "agent-resource-capture-" + strings.Repeat("a", 24) + "-" + strings.Repeat("b", 12)
							var associationCalls int

							BeforeEach(func() {
								testBuild = resourceCaptureAssociationResultBuild{
									Build: realBuild,
									association: db.ResourceCaptureBuildAssociation{
										TemplatePipelineID: 41,
										TemplateName:       templateName,
									},
									found: true,
									calls: &associationCalls,
								}
								expectedPlan = planFactory.NewPlan(atc.TaskPlan{Name: "seal-snapshot"})
							})

							It("copies the server-owned capture template into step metadata without re-querying", func() {
								Expect(associationCalls).To(Equal(1))
								Expect(fakeCoreStepFactory.TaskStepCallCount()).To(Equal(1))
								_, taskMetadata, _, _ := fakeCoreStepFactory.TaskStepArgsForCall(0)
								Expect(taskMetadata.ResourceCaptureTemplate).To(Equal(templateName))
								Expect(taskMetadata.WorkflowDefinitionID).To(BeNil())
							})
						})
					})

					Context("without a resource-capture association", func() {
						BeforeEach(func() {
							expectedPlan = planFactory.NewPlan(atc.TaskPlan{Name: "seal-snapshot"})
						})

						It("leaves the capture anchor empty so any authority in the plan stays inert", func() {
							Expect(fakeCoreStepFactory.TaskStepCallCount()).To(Equal(1))
							_, taskMetadata, _, _ := fakeCoreStepFactory.TaskStepArgsForCall(0)
							Expect(taskMetadata.ResourceCaptureTemplate).To(BeEmpty())
						})
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
								plan, stepMetadata, containerMetadata, _ := fakeCoreStepFactory.PutStepArgsForCall(0)
								Expect(plan).To(Equal(putPlan))
								Expect(stepMetadata).To(Equal(expectedMetadataWithCreatedBy))
								Expect(containerMetadata).To(Equal(db.ContainerMetadata{
									Type:                 db.ContainerTypePut,
									StepName:             "some-put",
									PipelineID:           pipeline.ID(),
									PipelineName:         pipeline.Name(),
									PipelineInstanceVars: "{\"branch\":\"master\"}",
									JobID:                job.ID(),
									JobName:              job.Name(),
									BuildID:              realBuild.ID(),
									BuildName:            realBuild.Name(),
								}))

								plan, stepMetadata, containerMetadata, _ = fakeCoreStepFactory.PutStepArgsForCall(1)
								Expect(plan).To(Equal(otherPutPlan))
								Expect(stepMetadata).To(Equal(expectedMetadataWithCreatedBy))
								Expect(containerMetadata).To(Equal(db.ContainerMetadata{
									Type:                 db.ContainerTypePut,
									StepName:             "some-put-2",
									PipelineID:           pipeline.ID(),
									PipelineName:         pipeline.Name(),
									PipelineInstanceVars: "{\"branch\":\"master\"}",
									JobID:                job.ID(),
									JobName:              job.Name(),
									BuildID:              realBuild.ID(),
									BuildName:            realBuild.Name(),
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
								plan, stepMetadata, containerMetadata, _ := fakeCoreStepFactory.PutStepArgsForCall(0)
								Expect(plan).To(Equal(putPlan))
								Expect(stepMetadata).To(Equal(expectedMetadataWithCreatedBy))
								Expect(containerMetadata).To(Equal(db.ContainerMetadata{
									Type:                 db.ContainerTypePut,
									StepName:             "some-put",
									PipelineID:           pipeline.ID(),
									PipelineName:         pipeline.Name(),
									PipelineInstanceVars: "{\"branch\":\"master\"}",
									JobID:                job.ID(),
									JobName:              job.Name(),
									BuildID:              realBuild.ID(),
									BuildName:            realBuild.Name(),
								}))

								plan, stepMetadata, containerMetadata, _ = fakeCoreStepFactory.PutStepArgsForCall(1)
								Expect(plan).To(Equal(otherPutPlan))
								Expect(stepMetadata).To(Equal(expectedMetadataWithCreatedBy))
								Expect(containerMetadata).To(Equal(db.ContainerMetadata{
									Type:                 db.ContainerTypePut,
									StepName:             "some-put-2",
									PipelineID:           pipeline.ID(),
									PipelineName:         pipeline.Name(),
									PipelineInstanceVars: "{\"branch\":\"master\"}",
									JobID:                job.ID(),
									JobName:              job.Name(),
									BuildID:              realBuild.ID(),
									BuildName:            realBuild.Name(),
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
							plan, stepMetadata, containerMetadata, _ := fakeCoreStepFactory.GetStepArgsForCall(0)
							expectedPlan := getPlan
							expectedPlan.Attempts = []int{1}
							Expect(plan).To(Equal(expectedPlan))
							Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
							Expect(containerMetadata).To(Equal(db.ContainerMetadata{
								Type:                 db.ContainerTypeGet,
								StepName:             "some-get",
								PipelineID:           pipeline.ID(),
								PipelineName:         pipeline.Name(),
								PipelineInstanceVars: "{\"branch\":\"master\"}",
								JobID:                job.ID(),
								JobName:              job.Name(),
								BuildID:              realBuild.ID(),
								BuildName:            realBuild.Name(),
								Attempt:              "1",
							}))
						})

						It("constructs the second get correctly", func() {
							plan, stepMetadata, containerMetadata, _ := fakeCoreStepFactory.GetStepArgsForCall(1)
							expectedPlan := getPlan
							expectedPlan.Attempts = []int{3}
							Expect(plan).To(Equal(expectedPlan))
							Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
							Expect(containerMetadata).To(Equal(db.ContainerMetadata{
								Type:                 db.ContainerTypeGet,
								StepName:             "some-get",
								PipelineID:           pipeline.ID(),
								PipelineName:         pipeline.Name(),
								PipelineInstanceVars: "{\"branch\":\"master\"}",
								JobID:                job.ID(),
								JobName:              job.Name(),
								BuildID:              realBuild.ID(),
								BuildName:            realBuild.Name(),
								Attempt:              "3",
							}))
						})

						It("constructs nested retries correctly", func() {
							Expect(*retryPlanTwo.Retry).To(HaveLen(2))
						})

						It("constructs nested steps correctly", func() {
							plan, stepMetadata, containerMetadata, _ := fakeCoreStepFactory.TaskStepArgsForCall(0)
							expectedPlan := taskPlan
							expectedPlan.Attempts = []int{2, 1}
							Expect(plan).To(Equal(expectedPlan))
							Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
							Expect(containerMetadata).To(Equal(db.ContainerMetadata{
								Type:                 db.ContainerTypeTask,
								StepName:             "some-task",
								PipelineID:           pipeline.ID(),
								PipelineName:         pipeline.Name(),
								PipelineInstanceVars: "{\"branch\":\"master\"}",
								JobID:                job.ID(),
								JobName:              job.Name(),
								BuildID:              realBuild.ID(),
								BuildName:            realBuild.Name(),
								Attempt:              "2.1",
							}))

							plan, stepMetadata, containerMetadata, _ = fakeCoreStepFactory.TaskStepArgsForCall(1)
							expectedPlan = taskPlan
							expectedPlan.Attempts = []int{2, 2}
							Expect(plan).To(Equal(expectedPlan))
							Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
							Expect(containerMetadata).To(Equal(db.ContainerMetadata{
								Type:                 db.ContainerTypeTask,
								StepName:             "some-task",
								PipelineID:           pipeline.ID(),
								PipelineName:         pipeline.Name(),
								PipelineInstanceVars: "{\"branch\":\"master\"}",
								JobID:                job.ID(),
								JobName:              job.Name(),
								BuildID:              realBuild.ID(),
								BuildName:            realBuild.Name(),
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
							Expect(fakeCoreStepFactory.TaskStepCallCount()).To(Equal(6))

							_, _, containerMetadata, _ := fakeCoreStepFactory.TaskStepArgsForCall(0)
							Expect(containerMetadata.Attempt).To(Equal("1"))
							_, _, containerMetadata, _ = fakeCoreStepFactory.TaskStepArgsForCall(1)
							Expect(containerMetadata.Attempt).To(Equal("1"))
							_, _, containerMetadata, _ = fakeCoreStepFactory.TaskStepArgsForCall(2)
							Expect(containerMetadata.Attempt).To(Equal("1"))
							_, _, containerMetadata, _ = fakeCoreStepFactory.TaskStepArgsForCall(3)
							Expect(containerMetadata.Attempt).To(Equal("1"))
							_, _, containerMetadata, _ = fakeCoreStepFactory.TaskStepArgsForCall(4)
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
								plan, stepMetadata, containerMetadata, _ := fakeCoreStepFactory.GetStepArgsForCall(0)
								Expect(plan).To(Equal(expectedPlan))
								Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
								Expect(containerMetadata).To(Equal(db.ContainerMetadata{
									Type:                 db.ContainerTypeGet,
									StepName:             "some-input",
									PipelineID:           pipeline.ID(),
									PipelineName:         pipeline.Name(),
									PipelineInstanceVars: "{\"branch\":\"master\"}",
									JobID:                job.ID(),
									JobName:              job.Name(),
									BuildID:              realBuild.ID(),
									BuildName:            realBuild.Name(),
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
								plan, stepMetadata, containerMetadata, _ := fakeCoreStepFactory.TaskStepArgsForCall(0)
								Expect(plan).To(Equal(expectedPlan))
								Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
								Expect(containerMetadata).To(Equal(db.ContainerMetadata{
									Type:                 db.ContainerTypeTask,
									StepName:             "some-task",
									PipelineID:           pipeline.ID(),
									PipelineName:         pipeline.Name(),
									PipelineInstanceVars: "{\"branch\":\"master\"}",
									JobID:                job.ID(),
									JobName:              job.Name(),
									BuildID:              realBuild.ID(),
									BuildName:            realBuild.Name(),
									Attempt:              "0",
								}))
								Expect(containerMetadata.Attempt).To(Equal("0"))
							})
						})

						Context("that contains a run step", func() {
							BeforeEach(func() {
								expectedPlan = planFactory.NewPlan(atc.RunPlan{
									Message: "some-message",
									Type:    "some-prototype",
									Object:  atc.Params{"some": "params"},
								})
							})

							It("constructs run step correctly", func() {
								plan, stepMetadata, _, _ := fakeCoreStepFactory.RunStepArgsForCall(0)
								Expect(plan).To(Equal(expectedPlan))
								Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
							})
						})

						Context("that contains an agent step", func() {
							BeforeEach(func() {
								expectedPlan = planFactory.NewPlan(atc.AgentPlan{
									Name:   "write-spec",
									Prompt: "p",
								})
							})

							It("constructs agent step correctly", func() {
								Expect(fakeCoreStepFactory.AgentStepCallCount()).To(Equal(1))
								plan, stepMetadata, containerMetadata, _ := fakeCoreStepFactory.AgentStepArgsForCall(0)
								Expect(plan).To(Equal(expectedPlan))
								Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
								Expect(containerMetadata).To(Equal(db.ContainerMetadata{
									Type:                 db.ContainerTypeAgent,
									StepName:             "write-spec",
									PipelineID:           pipeline.ID(),
									PipelineName:         pipeline.Name(),
									PipelineInstanceVars: "{\"branch\":\"master\"}",
									JobID:                job.ID(),
									JobName:              job.Name(),
									BuildID:              realBuild.ID(),
									BuildName:            realBuild.Name(),
									Attempt:              "0",
								}))
								Expect(containerMetadata.Attempt).To(Equal("0"))
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
								plan, stepMetadata, _ := fakeCoreStepFactory.SetPipelineStepArgsForCall(0)
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
								plan, stepMetadata, _ := fakeCoreStepFactory.LoadVarStepArgsForCall(0)
								Expect(plan).To(Equal(expectedPlan))
								Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
							})
						})

						Context("that contains a load_snapshot step", func() {
							BeforeEach(func() {
								expectedPlan = planFactory.NewPlan(atc.LoadSnapshotPlan{
									Name: "subject", ID: "9007199254740993", Type: "review/v1",
									WorkflowRunID: "9223372036854775807",
								})
							})

							It("constructs load_snapshot with server-derived metadata", func() {
								plan, stepMetadata, _ := fakeCoreStepFactory.LoadSnapshotStepArgsForCall(0)
								Expect(plan).To(Equal(expectedPlan))
								Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
							})
						})

						Context("that contains an await_snapshot step", func() {
							BeforeEach(func() {
								expectedPlan = planFactory.NewPlan(atc.AwaitSnapshotPlan{
									Name: "answer", Question: "question", Type: "human-answer/v1",
									OnTimeout: atc.AwaitSnapshotOnTimeoutFail, WorkflowRunID: "9223372036854775807",
								})
							})

							It("constructs await_snapshot with server-derived metadata", func() {
								plan, stepMetadata, _ := fakeCoreStepFactory.AwaitSnapshotStepArgsForCall(0)
								Expect(plan).To(Equal(expectedPlan))
								Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
							})
						})

						Context("that contains a publish_snapshot step", func() {
							BeforeEach(func() {
								expectedPlan = planFactory.NewPlan(atc.PublishSnapshotPlan{
									Name: "publish-change", Publisher: publisher.GitPublisher,
									Input: "change", InputType: "repository-change/v1",
									Destination: "github.example/team/repo", Mode: publisher.ModePullRequest,
									Parameters:            map[string]string{"source_branch": "agent/change", "target_branch": "main"},
									ApprovalPolicyVersion: "engineering/v2",
								})
							})

							It("constructs publish_snapshot with authenticated server metadata", func() {
								plan, stepMetadata, _ := fakeCoreStepFactory.PublishSnapshotStepArgsForCall(0)
								Expect(plan).To(Equal(expectedPlan))
								Expect(stepMetadata).To(Equal(expectedMetadataWithCreatedBy))
							})
						})

						Context("that contains a check step", func() {
							BeforeEach(func() {
								expectedPlan = planFactory.NewPlan(atc.CheckPlan{
									Name: "some-check",
								})
							})

							It("constructs the step correctly", func() {
								plan, stepMetadata, containerMetadata, _ := fakeCoreStepFactory.CheckStepArgsForCall(0)
								Expect(plan).To(Equal(expectedPlan))
								Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
								Expect(containerMetadata).To(Equal(db.ContainerMetadata{
									Type:                 db.ContainerTypeCheck,
									StepName:             "some-check",
									PipelineID:           pipeline.ID(),
									PipelineName:         pipeline.Name(),
									PipelineInstanceVars: `{"branch":"master"}`,
									JobID:                job.ID(),
									JobName:              job.Name(),
									BuildID:              realBuild.ID(),
									BuildName:            realBuild.Name(),
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
								plan, stepMetadata, containerMetadata, _ := fakeCoreStepFactory.PutStepArgsForCall(0)
								Expect(plan).To(Equal(putPlan))
								Expect(stepMetadata).To(Equal(expectedMetadataWithCreatedBy))
								Expect(containerMetadata).To(Equal(db.ContainerMetadata{
									Type:                 db.ContainerTypePut,
									StepName:             "some-put",
									PipelineID:           pipeline.ID(),
									PipelineName:         pipeline.Name(),
									PipelineInstanceVars: "{\"branch\":\"master\"}",
									JobID:                job.ID(),
									JobName:              job.Name(),
									BuildID:              realBuild.ID(),
									BuildName:            realBuild.Name(),
								}))
							})

							It("constructs the dependent get correctly", func() {
								plan, stepMetadata, containerMetadata, _ := fakeCoreStepFactory.GetStepArgsForCall(0)
								Expect(plan).To(Equal(dependentGetPlan))
								Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
								Expect(containerMetadata).To(Equal(db.ContainerMetadata{
									Type:                 db.ContainerTypeGet,
									StepName:             "some-get",
									PipelineID:           pipeline.ID(),
									PipelineName:         pipeline.Name(),
									PipelineInstanceVars: "{\"branch\":\"master\"}",
									JobID:                job.ID(),
									JobName:              job.Name(),
									BuildID:              realBuild.ID(),
									BuildName:            realBuild.Name(),
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
								Expect(fakeCoreStepFactory.GetStepCallCount()).To(Equal(1))
								plan, stepMetadata, containerMetadata, _ := fakeCoreStepFactory.GetStepArgsForCall(0)
								Expect(plan).To(Equal(inputPlan))
								Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
								Expect(containerMetadata).To(Equal(db.ContainerMetadata{
									PipelineID:           pipeline.ID(),
									PipelineName:         pipeline.Name(),
									PipelineInstanceVars: "{\"branch\":\"master\"}",
									JobID:                job.ID(),
									JobName:              job.Name(),
									BuildID:              realBuild.ID(),
									BuildName:            realBuild.Name(),
									StepName:             "some-input",
									Type:                 db.ContainerTypeGet,
								}))
							})

							It("constructs the completion hook correctly", func() {
								Expect(fakeCoreStepFactory.TaskStepCallCount()).To(Equal(4))
								plan, stepMetadata, containerMetadata, _ := fakeCoreStepFactory.TaskStepArgsForCall(2)
								Expect(plan).To(Equal(completionTaskPlan))
								Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
								Expect(containerMetadata).To(Equal(db.ContainerMetadata{
									PipelineID:           pipeline.ID(),
									PipelineName:         pipeline.Name(),
									PipelineInstanceVars: "{\"branch\":\"master\"}",
									JobID:                job.ID(),
									JobName:              job.Name(),
									BuildID:              realBuild.ID(),
									BuildName:            realBuild.Name(),
									StepName:             "some-completion-task",
									Type:                 db.ContainerTypeTask,
									Attempt:              "0",
								}))
							})

							It("constructs the failure hook correctly", func() {
								Expect(fakeCoreStepFactory.TaskStepCallCount()).To(Equal(4))
								plan, stepMetadata, containerMetadata, _ := fakeCoreStepFactory.TaskStepArgsForCall(0)
								Expect(plan).To(Equal(failureTaskPlan))
								Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
								Expect(containerMetadata).To(Equal(db.ContainerMetadata{
									PipelineID:           pipeline.ID(),
									PipelineName:         pipeline.Name(),
									PipelineInstanceVars: "{\"branch\":\"master\"}",
									JobID:                job.ID(),
									JobName:              job.Name(),
									BuildID:              realBuild.ID(),
									BuildName:            realBuild.Name(),
									StepName:             "some-failure-task",
									Type:                 db.ContainerTypeTask,
									Attempt:              "0",
								}))
							})

							It("constructs the success hook correctly", func() {
								Expect(fakeCoreStepFactory.TaskStepCallCount()).To(Equal(4))
								plan, stepMetadata, containerMetadata, _ := fakeCoreStepFactory.TaskStepArgsForCall(1)
								Expect(plan).To(Equal(successTaskPlan))
								Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
								Expect(containerMetadata).To(Equal(db.ContainerMetadata{
									PipelineID:           pipeline.ID(),
									PipelineName:         pipeline.Name(),
									PipelineInstanceVars: "{\"branch\":\"master\"}",
									JobID:                job.ID(),
									JobName:              job.Name(),
									BuildID:              realBuild.ID(),
									BuildName:            realBuild.Name(),
									StepName:             "some-success-task",
									Type:                 db.ContainerTypeTask,
									Attempt:              "0",
								}))
							})

							It("constructs the next step correctly", func() {
								Expect(fakeCoreStepFactory.TaskStepCallCount()).To(Equal(4))
								plan, stepMetadata, containerMetadata, _ := fakeCoreStepFactory.TaskStepArgsForCall(3)
								Expect(plan).To(Equal(nextTaskPlan))
								Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
								Expect(containerMetadata).To(Equal(db.ContainerMetadata{
									PipelineID:           pipeline.ID(),
									PipelineName:         pipeline.Name(),
									PipelineInstanceVars: "{\"branch\":\"master\"}",
									JobID:                job.ID(),
									JobName:              job.Name(),
									BuildID:              realBuild.ID(),
									BuildName:            realBuild.Name(),
									StepName:             "some-next-task",
									Type:                 db.ContainerTypeTask,
									Attempt:              "0",
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
							Expect(fakeCoreStepFactory.GetStepCallCount()).To(Equal(1))
							plan, stepMetadata, containerMetadata, _ := fakeCoreStepFactory.GetStepArgsForCall(0)
							Expect(plan).To(Equal(inputPlan))
							Expect(stepMetadata).To(Equal(expectedMetadataWithoutCreatedBy))
							Expect(containerMetadata).To(Equal(db.ContainerMetadata{
								Type:                 db.ContainerTypeGet,
								StepName:             "some-input",
								PipelineID:           pipeline.ID(),
								PipelineName:         pipeline.Name(),
								PipelineInstanceVars: "{\"branch\":\"master\"}",
								JobID:                job.ID(),
								JobName:              job.Name(),
								BuildID:              realBuild.ID(),
								BuildName:            realBuild.Name(),
							}))
						})
					})

					// SF-10: Unknown plan type returns IdentityStep
					Context("with a plan matching no known type", func() {
						It("returns an IdentityStep", func() {
							// Construct a Plan with only an ID — no recognized type fields set
							unknownPlan := atc.Plan{ID: "unknown-plan-id"}

							stepper, err := stepperFactory.StepperForBuild(testBuild)
							Expect(err).ToNot(HaveOccurred())

							step := stepper(unknownPlan)
							Expect(step).To(Equal(exec.IdentityStep{}))
						})
					})
				})
			})
		})
	})
})
