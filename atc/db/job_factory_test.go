package db_test

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("JobFactory", func() {
	var jobFactory db.JobFactory

	BeforeEach(func() {
		jobFactory = db.NewJobFactory(dbConn, lockFactory)
	})

	Context("when there are public and private pipelines", func() {
		var publicPipeline db.Pipeline

		BeforeEach(func() {
			otherTeam, err := teamFactory.CreateTeam(atc.Team{Name: "other-team"})
			Expect(err).NotTo(HaveOccurred())

			publicPipeline, _, err = otherTeam.SavePipeline(atc.PipelineRef{Name: "public-pipeline", InstanceVars: atc.InstanceVars{"branch": "master"}}, atc.Config{
				Jobs: atc.JobConfigs{
					{
						Name: "public-pipeline-job-1",
						PlanSequence: []atc.Step{
							{
								Config: &atc.GetStep{
									Name: "some-resource",
								},
							},
							{
								Config: &atc.GetStep{
									Name: "some-other-resource",
								},
							},
							{
								Config: &atc.PutStep{
									Name: "some-resource",
								},
							},
						},
					},
					{
						Name: "public-pipeline-job-2",
						PlanSequence: []atc.Step{
							{
								Config: &atc.GetStep{
									Name:   "some-resource",
									Passed: []string{"public-pipeline-job-1"},
								},
							},
							{
								Config: &atc.GetStep{
									Name:   "some-other-resource",
									Passed: []string{"public-pipeline-job-1"},
								},
							},
							{
								Config: &atc.GetStep{
									Name:     "resource",
									Resource: "some-resource",
								},
							},
							{
								Config: &atc.PutStep{
									Name:     "resource",
									Resource: "some-resource",
								},
							},
							{
								Config: &atc.PutStep{
									Name: "some-resource",
								},
							},
						},
					},
					{
						Name: "public-pipeline-job-3",
						PlanSequence: []atc.Step{
							{
								Config: &atc.GetStep{
									Name:   "some-resource",
									Passed: []string{"public-pipeline-job-1", "public-pipeline-job-2"},
								},
							},
						},
					},
				},
				Resources: atc.ResourceConfigs{
					{
						Name: "some-resource",
						Type: "some-type",
					},
					{
						Name: "some-other-resource",
						Type: "some-type",
					},
				},
			}, db.ConfigVersion(0), false)
			Expect(err).ToNot(HaveOccurred())
			Expect(publicPipeline.Expose()).To(Succeed())

			_, _, err = otherTeam.SavePipeline(atc.PipelineRef{Name: "private-pipeline"}, atc.Config{
				Jobs: atc.JobConfigs{
					{
						Name: "private-pipeline-job",
						PlanSequence: []atc.Step{
							{
								Config: &atc.GetStep{
									Name: "some-resource",
								},
							},
							{
								Config: &atc.PutStep{
									Name: "some-resource",
								},
							},
						},
					},
				},
				Resources: atc.ResourceConfigs{
					{
						Name: "some-resource",
						Type: "some-type",
					},
				},
			}, db.ConfigVersion(0), false)
			Expect(err).ToNot(HaveOccurred())
		})

		Describe("VisibleJobs", func() {
			It("returns jobs in the provided teams and jobs in public pipelines", func() {
				visibleJobs, err := jobFactory.VisibleJobs([]string{"default-team"})
				Expect(err).ToNot(HaveOccurred())

				Expect(len(visibleJobs)).To(Equal(4))
				Expect(visibleJobs[0].Name).To(Equal("some-job"))
				Expect(visibleJobs[1].Name).To(Equal("public-pipeline-job-1"))
				Expect(visibleJobs[2].Name).To(Equal("public-pipeline-job-2"))
				Expect(visibleJobs[3].Name).To(Equal("public-pipeline-job-3"))

				Expect(visibleJobs[0].Inputs).To(BeNil())
				Expect(visibleJobs[1].Inputs).To(Equal([]atc.JobInputSummary{
					{
						Name:     "some-other-resource",
						Resource: "some-other-resource",
					},
					{
						Name:     "some-resource",
						Resource: "some-resource",
					},
				}))
				Expect(visibleJobs[2].Inputs).To(Equal([]atc.JobInputSummary{
					{
						Name:     "resource",
						Resource: "some-resource",
					},
					{
						Name:     "some-other-resource",
						Resource: "some-other-resource",
						Passed:   []string{"public-pipeline-job-1"},
					},
					{
						Name:     "some-resource",
						Resource: "some-resource",
						Passed:   []string{"public-pipeline-job-1"},
					},
				}))
				Expect(visibleJobs[3].Inputs).To(Equal([]atc.JobInputSummary{
					{
						Name:     "some-resource",
						Resource: "some-resource",
						Passed:   []string{"public-pipeline-job-1", "public-pipeline-job-2"},
					},
				}))

				Expect(visibleJobs[0].Outputs).To(BeNil())
				Expect(visibleJobs[1].Outputs).To(Equal([]atc.JobOutputSummary{
					{
						Name:     "some-resource",
						Resource: "some-resource",
					},
				}))
				Expect(visibleJobs[2].Outputs).To(Equal([]atc.JobOutputSummary{
					{
						Name:     "resource",
						Resource: "some-resource",
					},
					{
						Name:     "some-resource",
						Resource: "some-resource",
					},
				}))
				Expect(visibleJobs[3].Outputs).To(BeNil())
			})

			It("returns next build, latest completed build, and transition build for each job", func() {
				job, found, err := defaultPipeline.Job("some-job")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())

				transitionBuild, err := job.CreateBuild(defaultBuildCreatedBy)
				Expect(err).ToNot(HaveOccurred())

				err = transitionBuild.Finish(db.BuildStatusSucceeded)
				Expect(err).ToNot(HaveOccurred())

				found, err = transitionBuild.Reload()
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())

				finishedBuild, err := job.CreateBuild(defaultBuildCreatedBy)
				Expect(err).ToNot(HaveOccurred())

				err = finishedBuild.Finish(db.BuildStatusSucceeded)
				Expect(err).ToNot(HaveOccurred())

				found, err = finishedBuild.Reload()
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())

				nextBuild, err := job.CreateBuild(defaultBuildCreatedBy)
				Expect(err).ToNot(HaveOccurred())

				visibleJobs, err := jobFactory.VisibleJobs([]string{"default-team"})
				Expect(err).ToNot(HaveOccurred())

				Expect(visibleJobs[0].Name).To(Equal("some-job"))
				Expect(visibleJobs[0].NextBuild.ID).To(Equal(nextBuild.ID()))
				Expect(visibleJobs[0].NextBuild.Name).To(Equal(nextBuild.Name()))
				Expect(visibleJobs[0].NextBuild.JobName).To(Equal(nextBuild.JobName()))
				Expect(visibleJobs[0].NextBuild.PipelineID).To(Equal(nextBuild.PipelineID()))
				Expect(visibleJobs[0].NextBuild.PipelineName).To(Equal(nextBuild.PipelineName()))
				Expect(visibleJobs[0].NextBuild.PipelineInstanceVars).To(Equal(nextBuild.PipelineInstanceVars()))
				Expect(visibleJobs[0].NextBuild.TeamName).To(Equal(nextBuild.TeamName()))
				Expect(visibleJobs[0].NextBuild.Status).To(Equal(atc.BuildStatus(nextBuild.Status())))
				Expect(visibleJobs[0].NextBuild.StartTime).To(Equal(nextBuild.StartTime().Unix()))
				Expect(visibleJobs[0].NextBuild.EndTime).To(Equal(nextBuild.EndTime().Unix()))

				Expect(visibleJobs[0].FinishedBuild.ID).To(Equal(finishedBuild.ID()))
				Expect(visibleJobs[0].FinishedBuild.Name).To(Equal(finishedBuild.Name()))
				Expect(visibleJobs[0].FinishedBuild.JobName).To(Equal(finishedBuild.JobName()))
				Expect(visibleJobs[0].FinishedBuild.PipelineID).To(Equal(finishedBuild.PipelineID()))
				Expect(visibleJobs[0].FinishedBuild.PipelineName).To(Equal(finishedBuild.PipelineName()))
				Expect(visibleJobs[0].FinishedBuild.PipelineInstanceVars).To(Equal(finishedBuild.PipelineInstanceVars()))
				Expect(visibleJobs[0].FinishedBuild.TeamName).To(Equal(finishedBuild.TeamName()))
				Expect(visibleJobs[0].FinishedBuild.Status).To(Equal(atc.BuildStatus(finishedBuild.Status())))
				Expect(visibleJobs[0].FinishedBuild.StartTime).To(Equal(finishedBuild.StartTime().Unix()))
				Expect(visibleJobs[0].FinishedBuild.EndTime).To(Equal(finishedBuild.EndTime().Unix()))

				Expect(visibleJobs[0].TransitionBuild.ID).To(Equal(transitionBuild.ID()))
				Expect(visibleJobs[0].TransitionBuild.Name).To(Equal(transitionBuild.Name()))
				Expect(visibleJobs[0].TransitionBuild.JobName).To(Equal(transitionBuild.JobName()))
				Expect(visibleJobs[0].TransitionBuild.PipelineID).To(Equal(transitionBuild.PipelineID()))
				Expect(visibleJobs[0].TransitionBuild.PipelineName).To(Equal(transitionBuild.PipelineName()))
				Expect(visibleJobs[0].TransitionBuild.PipelineInstanceVars).To(Equal(transitionBuild.PipelineInstanceVars()))
				Expect(visibleJobs[0].TransitionBuild.TeamName).To(Equal(transitionBuild.TeamName()))
				Expect(visibleJobs[0].TransitionBuild.Status).To(Equal(atc.BuildStatus(transitionBuild.Status())))
				Expect(visibleJobs[0].TransitionBuild.StartTime).To(Equal(transitionBuild.StartTime().Unix()))
				Expect(visibleJobs[0].TransitionBuild.EndTime).To(Equal(transitionBuild.EndTime().Unix()))
			})
		})

		Describe("AllActiveJobs", func() {
			It("return all private and public pipelines", func() {
				allJobs, err := jobFactory.AllActiveJobs()
				Expect(err).ToNot(HaveOccurred())

				Expect(len(allJobs)).To(Equal(5))
				Expect(allJobs[0].Name).To(Equal("some-job"))
				Expect(allJobs[1].Name).To(Equal("public-pipeline-job-1"))
				Expect(allJobs[2].Name).To(Equal("public-pipeline-job-2"))
				Expect(allJobs[3].Name).To(Equal("public-pipeline-job-3"))
				Expect(allJobs[4].Name).To(Equal("private-pipeline-job"))

				Expect(allJobs[0].Inputs).To(BeNil())
				Expect(allJobs[1].Inputs).To(Equal([]atc.JobInputSummary{
					{
						Name:     "some-other-resource",
						Resource: "some-other-resource",
					},
					{
						Name:     "some-resource",
						Resource: "some-resource",
					},
				}))
				Expect(allJobs[2].Inputs).To(Equal([]atc.JobInputSummary{
					{
						Name:     "resource",
						Resource: "some-resource",
					},
					{
						Name:     "some-other-resource",
						Resource: "some-other-resource",
						Passed:   []string{"public-pipeline-job-1"},
					},
					{
						Name:     "some-resource",
						Resource: "some-resource",
						Passed:   []string{"public-pipeline-job-1"},
					},
				}))
				Expect(allJobs[3].Inputs).To(Equal([]atc.JobInputSummary{
					{
						Name:     "some-resource",
						Resource: "some-resource",
						Passed:   []string{"public-pipeline-job-1", "public-pipeline-job-2"},
					},
				}))
				Expect(allJobs[4].Inputs).To(Equal([]atc.JobInputSummary{
					{
						Name:     "some-resource",
						Resource: "some-resource",
					},
				}))

				Expect(allJobs[0].Outputs).To(BeNil())
				Expect(allJobs[1].Outputs).To(Equal([]atc.JobOutputSummary{
					{
						Name:     "some-resource",
						Resource: "some-resource",
					},
				}))
				Expect(allJobs[2].Outputs).To(Equal([]atc.JobOutputSummary{
					{
						Name:     "resource",
						Resource: "some-resource",
					},
					{
						Name:     "some-resource",
						Resource: "some-resource",
					},
				}))
				Expect(allJobs[3].Outputs).To(BeNil())
				Expect(allJobs[4].Outputs).To(Equal([]atc.JobOutputSummary{
					{
						Name:     "some-resource",
						Resource: "some-resource",
					},
				}))
			})
		})
	})

})

var _ = Context("SchedulerResource", func() {
	var resource db.SchedulerResource

	BeforeEach(func() {
		resource = db.SchedulerResource{
			Name: "some-name",
			Type: "some-type",
			Source: atc.Source{
				"some-key": "some-value",
			},
			ExposeBuildCreatedBy: true,
		}
	})

	Context("ApplySourceDefaults", func() {
		var resourceTypes atc.ResourceTypes

		BeforeEach(func() {
			resourceTypes = atc.ResourceTypes{
				{
					Name:     "some-type",
					Defaults: atc.Source{"default-key": "default-value"},
				},
			}
		})

		JustBeforeEach(func() {
			resource.ApplySourceDefaults(resourceTypes)
		})

		It("should apply defaults", func() {
			Expect(resource).To(Equal(db.SchedulerResource{
				Name: "some-name",
				Type: "some-type",
				Source: atc.Source{
					"some-key":    "some-value",
					"default-key": "default-value",
				},
				ExposeBuildCreatedBy: true,
			}))
		})

		Context("when the parent resource is not found", func() {
			BeforeEach(func() {
				resourceTypes = atc.ResourceTypes{}
				atc.LoadBaseResourceTypeDefaults(map[string]atc.Source{
					"some-type": {"default-key": "default-value"},
				})
			})

			AfterEach(func() {
				atc.LoadBaseResourceTypeDefaults(map[string]atc.Source{})
			})

			It("should apply defaults using the base resource type", func() {
				Expect(resource).To(Equal(db.SchedulerResource{
					Name: "some-name",
					Type: "some-type",
					Source: atc.Source{
						"some-key":    "some-value",
						"default-key": "default-value",
					},
					ExposeBuildCreatedBy: true,
				}))
			})
		})
	})
})
