package exec_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"code.cloudfoundry.org/clock"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/engine"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/atc/policy"
	"github.com/concourse/concourse/atc/runtime/runtimetest"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/vars"
)

// deniedPolicyCheck is the result a policy agent returns when it blocks an
// action; the policy package keeps its own result type unexported.
type deniedPolicyCheck struct {
	messages []string
}

func (result deniedPolicyCheck) Allowed() bool      { return false }
func (result deniedPolicyCheck) ShouldBlock() bool  { return true }
func (result deniedPolicyCheck) Messages() []string { return result.messages }

// setPipelineDenyingChecker is a policy.Checker wired the way a deployment with
// a policy agent is: it screens the set_pipeline action, and denies it.
type setPipelineDenyingChecker struct {
	policy.NoopChecker
	messages []string
}

func (checker setPipelineDenyingChecker) ShouldCheckAction(action string) bool {
	return action == policy.ActionRunSetPipeline
}

func (checker setPipelineDenyingChecker) Check(policy.PolicyCheckInput) (policy.PolicyCheckResult, error) {
	return deniedPolicyCheck{messages: checker.messages}, nil
}

func persistedSetPipelineChanges(fixture *execDBFixture, build db.Build) []bool {
	GinkgoHelper()
	var changes []bool
	for _, e := range execBuildEvents(fixture, build) {
		if changed, ok := e.(event.SetPipelineChanged); ok {
			changes = append(changes, changed.Changed)
		}
	}
	return changes
}

func setPipelineTestConfig(runArgs ...string) atc.Config {
	if len(runArgs) == 0 {
		runArgs = []string{"feature/foo"}
	}

	return atc.Config{
		Jobs: atc.JobConfigs{
			{
				Name: "some-job",
				PlanSequence: []atc.Step{
					{
						Config: &atc.TaskStep{
							Name: "some-task",
							Config: &atc.TaskConfig{
								Platform: "linux",
								ImageResource: &atc.ImageResource{
									Type:   "registry-image",
									Source: atc.Source{"repository": "busybox"},
								},
								Run: atc.TaskRunConfig{
									Path: "echo",
									Args: append([]string(nil), runArgs...),
								},
							},
						},
					},
				},
			},
		},
	}
}

var _ = Describe("SetPipelineStep", func() {

	const badPipelineContentWithInvalidSyntax = `
---
jobs:
- name:
`

	const badPipelineWithDuplicateKeys = `
---
resources:
- name: shadowed-resource
  type: some-type
  source:
    source-config: some-value
resources:
- name: some-resource
  type: some-type
  source:
    source-config: some-value
jobs:
- name: job
  plan:
  - get: some-resource
`

	const pipelineWithMergeKeys = `
---
base_job: &base_job
  name: job
  plan:
    - get: resource-that-doesnt-exist

resources:
  - name: some-resource
    type: some-type
    source:
      source-config: some-value
jobs:
  - <<: *base_job
    plan:
      - get: some-resource
`

	const badPipelineContentWithEmptyContent = `
---
`

	const pipelineContent = `
---
jobs:
- name: some-job
  plan:
  - task: some-task
    config:
      platform: linux
      image_resource:
        type: registry-image
        source: {repository: busybox}
      run:
        path: echo
        args:
         - ((branch))
`

	var (
		ctx        context.Context
		cancel     func()
		testLogger *lagertest.TestLogger

		fixture         *execDBFixture
		currentTeam     db.Team
		currentPipeline db.Pipeline
		currentJob      db.Job
		realBuild       db.Build
		currentRef      atc.PipelineRef
		targetRef       atc.PipelineRef

		policyChecker   policy.Checker
		delegateFactory exec.SetPipelineStepDelegateFactory

		streamer exec.Streamer

		spPlan              *atc.SetPipelinePlan
		pipelineFileContent string
		artifactRepository  *build.Repository
		state               exec.RunState

		spStep  exec.Step
		stepOk  bool
		stepErr error

		stepMetadata exec.StepMetadata

		planID = "56"
	)

	BeforeEach(func() {
		testLogger = lagertest.NewTestLogger("set-pipeline-action-test")
		ctx, cancel = context.WithCancel(context.Background())
		ctx = lagerctx.NewContext(ctx, testLogger)
		fixture = useExecDB()
		currentRef = atc.PipelineRef{
			Name:         "parent-pipeline",
			InstanceVars: atc.InstanceVars{"branch": "feature/foo"},
		}
		currentTeam, currentPipeline, currentJob, realBuild = createExecJobBuild(
			fixture,
			"some-team",
			currentRef,
			atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
			"some-user",
		)
		targetRef = atc.PipelineRef{
			Name:         "some-pipeline",
			InstanceVars: atc.InstanceVars{"branch": "feature/foo"},
		}
		state = exec.NewRunState(noopStepper, vars.StaticVariables{"source-param": "super-secret-source"})
		artifactRepository = state.ArtifactRepository()
		pipelineFileContent = ""

		policyChecker = policy.NoopChecker{}

		delegateFactory = setPipelineStepDelegateFactory(func(state exec.RunState) exec.SetPipelineStepDelegate {
			return engine.NewSetPipelineStepDelegate(realBuild, atc.PlanID(planID), state, clock.NewClock(), policyChecker)
		})

		stepMetadata = exec.StepMetadata{
			TeamID:               currentTeam.ID(),
			TeamName:             currentTeam.Name(),
			JobID:                currentJob.ID(),
			JobName:              currentJob.Name(),
			BuildID:              realBuild.ID(),
			BuildName:            realBuild.Name(),
			PipelineID:           currentPipeline.ID(),
			PipelineName:         currentPipeline.Name(),
			PipelineInstanceVars: currentPipeline.InstanceVars(),
		}

		streamer = worker.NewStreamer(compression.NewGzipCompression())

		spPlan = &atc.SetPipelinePlan{
			Name:         "some-pipeline",
			File:         "some-resource/pipeline.yml",
			Vars:         map[string]any{"branch": "feature/this-should-be-overridden-by-instance-var-with-same-name"},
			InstanceVars: atc.InstanceVars{"branch": "feature/foo"},
		}
	})

	AfterEach(func() {
		cancel()
	})

	JustBeforeEach(func() {
		volume := runtimetest.NewVolume("some-handle")
		if pipelineFileContent != "" {
			volume = volume.WithContent(runtimetest.VolumeContent{
				"pipeline.yml": {Data: []byte(pipelineFileContent)},
			})
		}
		artifactRepository.RegisterArtifact("some-resource", volume, false)

		plan := atc.Plan{
			ID:          atc.PlanID(planID),
			SetPipeline: spPlan,
		}

		spStep = exec.NewSetPipelineStep(
			plan.ID,
			*plan.SetPipeline,
			stepMetadata,
			delegateFactory,
			fixture.TeamFactory,
			fixture.BuildFactory,
			streamer,
		)

		stepOk, stepErr = spStep.Run(ctx, state)
	})

	Describe("persisted PostgreSQL success", func() {
		BeforeEach(func() {
			pipelineFileContent = pipelineContent
		})

		It("persists the target reference and parent hierarchy", func() {
			pipeline, found, err := currentTeam.Pipeline(targetRef)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			reloaded, err := pipeline.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(reloaded).To(BeTrue())
			Expect(pipeline.Name()).To(Equal(targetRef.Name))
			Expect(pipeline.InstanceVars()).To(Equal(targetRef.InstanceVars))
			config, err := pipeline.Config()
			Expect(err).NotTo(HaveOccurred())
			Expect(config).To(Equal(setPipelineTestConfig()))
			Expect(pipeline.ParentJobID()).To(Equal(currentJob.ID()))
			Expect(pipeline.ParentBuildID()).To(Equal(realBuild.ID()))
		})
	})

	Context("when file is not configured", func() {
		BeforeEach(func() {
			spPlan = &atc.SetPipelinePlan{
				Name: "some-pipeline",
			}
		})

		It("should fail with error of file not configured", func() {
			Expect(stepErr).To(HaveOccurred())
			Expect(stepErr.Error()).To(Equal("file is not specified"))
		})
	})

	Context("when file is configured", func() {
		Context("pipeline file not exist", func() {
			It("should fail with error of file not configured", func() {
				Expect(stepErr).To(MatchError(exec.FileNotFoundError{
					Name:     "some-resource",
					FilePath: "pipeline.yml",
				}))
			})
		})

		Context("when pipeline file exists but has bad syntax", func() {
			BeforeEach(func() {
				pipelineFileContent = badPipelineContentWithInvalidSyntax
			})

			It("should not return error", func() {
				Expect(stepErr).NotTo(HaveOccurred())
			})

			It("should have an error message printed to stderr", func() {
				Expect(execBuildLog(fixture, realBuild, event.OriginSourceStderr)).To(MatchRegexp(`(?s)invalid pipeline:.*- invalid jobs:`))
			})

			It("should finish unsuccessfully", func() {
				Expect(execBuildFinishEvents(fixture, realBuild)).To(HaveLen(1))
				Expect(execBuildFinishEvents(fixture, realBuild)[0].Succeeded).To(BeFalse())
			})
		})

		Context("when pipeline file exists but has duplicate keys", func() {
			BeforeEach(func() {
				pipelineFileContent = badPipelineWithDuplicateKeys
			})

			It("should not return error", func() {
				Expect(stepErr).NotTo(HaveOccurred())
			})

			It("should have an error message printed to stderr", func() {
				Expect(execBuildLog(fixture, realBuild, event.OriginSourceStderr)).To(MatchRegexp(`(?s)error parsing pipeline:.*mapping key "resources" already defined`))
			})

			It("should finish unsuccessfully", func() {
				Expect(execBuildFinishEvents(fixture, realBuild)).To(HaveLen(1))
				Expect(execBuildFinishEvents(fixture, realBuild)[0].Succeeded).To(BeFalse())
			})
		})

		Context("when pipeline file exists and has merge keys", func() {
			BeforeEach(func() {
				pipelineFileContent = pipelineWithMergeKeys
			})

			It("should not return error", func() {
				Expect(stepErr).NotTo(HaveOccurred())
			})

			It("should finish successfully", func() {
				Expect(execBuildFinishEvents(fixture, realBuild)).To(HaveLen(1))
				Expect(execBuildFinishEvents(fixture, realBuild)[0].Succeeded).To(BeTrue())
			})
		})

		Context("when pipeline file exists but is empty", func() {
			BeforeEach(func() {
				pipelineFileContent = badPipelineContentWithEmptyContent
			})

			It("should return an error", func() {
				Expect(stepErr).NotTo(HaveOccurred())
			})

			It("should log an error message", func() {
				Expect(execBuildLog(fixture, realBuild, event.OriginSourceStderr)).To(ContainSubstring("pipeline must contain at least one job"))
			})

			It("should not update the job and build id", func() {
				reloaded, err := currentPipeline.Reload()
				Expect(err).NotTo(HaveOccurred())
				Expect(reloaded).To(BeTrue())
				Expect(currentPipeline.ParentJobID()).To(BeZero())
				Expect(currentPipeline.ParentBuildID()).To(BeZero())
				_, found, err := currentTeam.Pipeline(targetRef)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeFalse())
			})
		})

		Context("when pipeline file is good", func() {
			BeforeEach(func() {
				pipelineFileContent = pipelineContent
			})

			Context("when specified pipeline not found", func() {
				It("should save the pipeline", func() {
					pipeline, found, err := currentTeam.Pipeline(targetRef)
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())
					reloaded, err := pipeline.Reload()
					Expect(err).NotTo(HaveOccurred())
					Expect(reloaded).To(BeTrue())
					Expect(pipeline.Name()).To(Equal(targetRef.Name))
					Expect(pipeline.InstanceVars()).To(Equal(targetRef.InstanceVars))
					config, err := pipeline.Config()
					Expect(err).NotTo(HaveOccurred())
					Expect(config).To(Equal(setPipelineTestConfig()))
					Expect(pipeline.ParentJobID()).To(Equal(currentJob.ID()))
					Expect(pipeline.ParentBuildID()).To(Equal(realBuild.ID()))
					Expect(pipeline.Paused()).To(BeFalse())
				})

				It("should stdout have message", func() {
					Expect(execBuildLog(fixture, realBuild, event.OriginSourceStdout)).To(ContainSubstring("done"))
				})
			})

			Context("when specified pipeline exists already", func() {
				var existingPipeline db.Pipeline

				BeforeEach(func() {
					changedConfig := setPipelineTestConfig("hello world")
					var err error
					existingPipeline, _, err = currentTeam.SavePipeline(targetRef, changedConfig, 0, false)
					Expect(err).NotTo(HaveOccurred())
				})

				Context("when no diff", func() {
					BeforeEach(func() {
						unchangedConfig := setPipelineTestConfig()
						var err error
						existingPipeline, _, err = currentTeam.SavePipeline(
							targetRef,
							unchangedConfig,
							existingPipeline.ConfigVersion(),
							false,
						)
						Expect(err).NotTo(HaveOccurred())
					})

					It("should log 'no changes to apply'", func() {
						Expect(execBuildLog(fixture, realBuild, event.OriginSourceStdout)).To(ContainSubstring("no changes to apply."))
					})

					It("should send a set pipeline changed event", func() {
						Expect(persistedSetPipelineChanges(fixture, realBuild)).To(Equal([]bool{false}))
					})

					It("should update the job and build id", func() {
						pipeline, found, err := currentTeam.Pipeline(targetRef)
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						reloaded, err := pipeline.Reload()
						Expect(err).NotTo(HaveOccurred())
						Expect(reloaded).To(BeTrue())
						Expect(pipeline.ParentJobID()).To(Equal(currentJob.ID()))
						Expect(pipeline.ParentBuildID()).To(Equal(realBuild.ID()))
					})
				})

				Context("when there are some diff", func() {
					It("should log diff", func() {
						Expect(execBuildLog(fixture, realBuild, event.OriginSourceStdout)).To(ContainSubstring("job some-job has changed:"))
					})

					It("should send a set pipeline changed event", func() {
						Expect(persistedSetPipelineChanges(fixture, realBuild)).To(Equal([]bool{true}))

						pipeline, found, err := currentTeam.Pipeline(targetRef)
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						reloaded, err := pipeline.Reload()
						Expect(err).NotTo(HaveOccurred())
						Expect(reloaded).To(BeTrue())
						config, err := pipeline.Config()
						Expect(err).NotTo(HaveOccurred())
						Expect(config).To(Equal(setPipelineTestConfig()))
					})
				})

				Context("when policy check fails", func() {
					BeforeEach(func() {
						policyChecker = setPipelineDenyingChecker{messages: []string{"policy-check-error"}}
					})

					It("should return error", func() {
						Expect(stepErr).To(MatchError(policy.PolicyCheckNotPass{
							Messages: []string{"policy-check-error"},
						}))
					})

					It("should leave the existing config in place", func() {
						pipeline, found, err := currentTeam.Pipeline(targetRef)
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						config, err := pipeline.Config()
						Expect(err).NotTo(HaveOccurred())
						Expect(config).To(Equal(setPipelineTestConfig("hello world")))
					})
				})

				Context("when a newer build has already set the pipeline", func() {
					var newerBuild db.Build

					BeforeEach(func() {
						var err error
						newerBuild, err = currentJob.CreateBuild("newer-user")
						Expect(err).NotTo(HaveOccurred())
						newerConfig := setPipelineTestConfig("newer-build")
						newerPipeline, _, err := newerBuild.SavePipeline(
							targetRef,
							currentTeam.ID(),
							newerConfig,
							existingPipeline.ConfigVersion(),
							false,
						)
						Expect(err).NotTo(HaveOccurred())
						existingPipeline = newerPipeline
					})

					It("logs a warning", func() {
						Expect(execBuildLog(fixture, realBuild, event.OriginSourceStderr)).To(ContainSubstring("WARNING: the pipeline was not saved because it was already saved by a newer build"))
					})

					It("does not fail the step", func() {
						Expect(stepErr).ToNot(HaveOccurred())
						Expect(stepOk).To(BeTrue())
					})

					It("retains the newer build's persisted pipeline", func() {
						pipeline, found, err := currentTeam.Pipeline(targetRef)
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						config, err := pipeline.Config()
						Expect(err).NotTo(HaveOccurred())
						Expect(config).To(Equal(setPipelineTestConfig("newer-build")))
						Expect(pipeline.ParentBuildID()).To(Equal(newerBuild.ID()))
					})
				})

				It("should save the pipeline un-paused", func() {
					pipeline, found, err := currentTeam.Pipeline(targetRef)
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())
					reloaded, err := pipeline.Reload()
					Expect(err).NotTo(HaveOccurred())
					Expect(reloaded).To(BeTrue())
					Expect(pipeline.InstanceVars()).To(Equal(targetRef.InstanceVars))
					Expect(pipeline.Paused()).To(BeFalse())
					Expect(pipeline.ParentJobID()).To(Equal(currentJob.ID()))
					Expect(pipeline.ParentBuildID()).To(Equal(realBuild.ID()))
				})

				It("should stdout have message", func() {
					Expect(execBuildLog(fixture, realBuild, event.OriginSourceStdout)).To(MatchRegexp(`(?s)setting pipeline: some-pipeline.*done`))
				})

				It("should finish successfully", func() {
					Expect(execBuildFinishEvents(fixture, realBuild)).To(HaveLen(1))
					Expect(execBuildFinishEvents(fixture, realBuild)[0].Succeeded).To(BeTrue())
				})
			})

			Context("when set-pipeline self", func() {
				BeforeEach(func() {
					spPlan = &atc.SetPipelinePlan{
						Name:         "self",
						File:         "some-resource/pipeline.yml",
						Team:         "foo-team",
						InstanceVars: atc.InstanceVars{"branch": "feature/foo"},
					}
				})

				It("should save the pipeline itself", func() {
					pipeline, found, err := currentTeam.Pipeline(currentRef)
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())
					reloaded, err := pipeline.Reload()
					Expect(err).NotTo(HaveOccurred())
					Expect(reloaded).To(BeTrue())
					Expect(pipeline.Name()).To(Equal(currentRef.Name))
					Expect(pipeline.InstanceVars()).To(Equal(currentRef.InstanceVars))
					config, err := pipeline.Config()
					Expect(err).NotTo(HaveOccurred())
					Expect(config).To(Equal(setPipelineTestConfig()))
					Expect(pipeline.ParentJobID()).To(Equal(currentJob.ID()))
					Expect(pipeline.ParentBuildID()).To(Equal(realBuild.ID()))
				})

				It("should save to the current team", func() {
					pipeline, found, err := currentTeam.Pipeline(currentRef)
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())
					Expect(pipeline.TeamID()).To(Equal(currentTeam.ID()))
					_, found, err = fixture.TeamFactory.FindTeam("foo-team")
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeFalse())
				})
			})

			Context("when team is configured", func() {
				var targetTeam db.Team

				BeforeEach(func() {
					var err error
					targetTeam, err = fixture.TeamFactory.CreateTeam(atc.Team{Name: "target-team"})
					Expect(err).NotTo(HaveOccurred())
				})

				Context("when team is set to the empty string", func() {
					BeforeEach(func() {
						spPlan.Team = ""
					})

					It("should finish successfully", func() {
						pipeline, found, err := currentTeam.Pipeline(targetRef)
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue())
						Expect(pipeline.TeamID()).To(Equal(currentTeam.ID()))
						Expect(execBuildFinishEvents(fixture, realBuild)).To(HaveLen(1))
						Expect(execBuildFinishEvents(fixture, realBuild)[0].Succeeded).To(BeTrue())
					})
				})

				Context("when team does not exist", func() {
					BeforeEach(func() {
						spPlan.Team = "not-found"
					})

					It("should return error", func() {
						Expect(stepErr).To(HaveOccurred())
						Expect(stepErr.Error()).To(Equal("team not-found not found"))
					})
				})

				Context("when team exists", func() {
					Context("when the target team is the current team", func() {
						BeforeEach(func() {
							spPlan.Team = currentTeam.Name()
						})

						It("should finish successfully", func() {
							pipeline, found, err := currentTeam.Pipeline(targetRef)
							Expect(err).NotTo(HaveOccurred())
							Expect(found).To(BeTrue())
							Expect(pipeline.TeamID()).To(Equal(currentTeam.ID()))
							Expect(execBuildFinishEvents(fixture, realBuild)).To(HaveLen(1))
							Expect(execBuildFinishEvents(fixture, realBuild)[0].Succeeded).To(BeTrue())
						})
					})

					Context("when the team is not the current team", func() {
						BeforeEach(func() {
							spPlan.Team = targetTeam.Name()
						})

						Context("when the current team is an admin team", func() {
							BeforeEach(func() {
								adminTeam, err := fixture.TeamFactory.CreateDefaultTeamIfNotExists()
								Expect(err).NotTo(HaveOccurred())
								stepMetadata.TeamID = adminTeam.ID()
								stepMetadata.TeamName = adminTeam.Name()
							})

							It("should finish successfully", func() {
								pipeline, found, err := targetTeam.Pipeline(targetRef)
								Expect(err).NotTo(HaveOccurred())
								Expect(found).To(BeTrue())
								Expect(pipeline.TeamID()).To(Equal(targetTeam.ID()))
								Expect(pipeline.ParentJobID()).To(Equal(currentJob.ID()))
								Expect(pipeline.ParentBuildID()).To(Equal(realBuild.ID()))
								Expect(execBuildFinishEvents(fixture, realBuild)).To(HaveLen(1))
								Expect(execBuildFinishEvents(fixture, realBuild)[0].Succeeded).To(BeTrue())
							})
						})

						Context("when the current team is not an admin team", func() {
							It("should return error", func() {

								Expect(stepErr).To(HaveOccurred())
								Expect(stepErr.Error()).To(Equal(
									"only main team can set another team's pipeline",
								))
							})
						})
					})
				})
			})
		})
	})
})
