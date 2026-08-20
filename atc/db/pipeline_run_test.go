package db_test

import (
	"database/sql"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("PipelineRun", func() {
	It("hydrates run ownership through pipeline scoped objects", func() {
		var templateID, runID, childID int
		Expect(dbConn.QueryRow(`INSERT INTO pipelines(team_id, name, template, secondary_ordering) VALUES ($1, 'run-base', true, 1) RETURNING id`, defaultTeam.ID()).Scan(&templateID)).To(Succeed())

		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.QueryRow(`INSERT INTO pipeline_runs(template_pipeline_id, number, params, status, created_by, config_hash) VALUES ($1, 1, '{}', 'running', 'creator', 'hash') RETURNING id`, templateID).Scan(&runID)).To(Succeed())
		Expect(tx.QueryRow(`INSERT INTO pipelines(team_id, name, instance_vars, pipeline_run_id, secondary_ordering) VALUES ($1, 'run-base', '{"run":1}', $2, 1) RETURNING id`, defaultTeam.ID(), runID).Scan(&childID)).To(Succeed())
		Expect(tx.Commit()).To(Succeed())

		pipeline, found, err := defaultTeam.Pipeline(atc.PipelineRef{Name: "run-base", InstanceVars: atc.InstanceVars{"run": float64(1)}})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		id, found := pipeline.PipelineRunID()
		Expect(found).To(BeTrue())
		Expect(id).To(Equal(runID))
		Expect(pipeline.BasePipelineID()).To(Equal(templateID))
		baseRef, found := pipeline.BasePipelineRef()
		Expect(found).To(BeTrue())
		Expect(baseRef).To(Equal(atc.PipelineRef{Name: "run-base"}))

		_, err = dbConn.Exec(`INSERT INTO jobs(name, pipeline_id, config, active, run_expected, run_policy_key) VALUES ('run-job', $1, '{}', true, true, 'policy')`, childID)
		Expect(err).NotTo(HaveOccurred())
		job, found, err := pipeline.Job("run-job")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(job.RunExpected()).To(BeTrue())
		Expect(job.RunPolicyKey()).To(Equal("policy"))
		jobRunID, jobHasRun := job.PipelineRunID()
		Expect(jobHasRun).To(BeTrue())
		Expect(jobRunID).To(Equal(runID))
	})

	It("does not classify an ordinary run-shaped instance as a payload", func() {
		ordinary, created, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "ordinary", InstanceVars: atc.InstanceVars{"run": float64(7)}}, atc.Config{}, db.ConfigVersion(0), false)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		_, found := ordinary.PipelineRunID()
		Expect(found).To(BeFalse())
	})

	It("hydrates a detached run build's base template from its durable run identity", func() {
		var templateID, runID, childID, jobID, buildID int
		Expect(dbConn.QueryRow(`INSERT INTO pipelines(team_id, name, template, secondary_ordering) VALUES ($1, 'detached-base', true, 1) RETURNING id`, defaultTeam.ID()).Scan(&templateID)).To(Succeed())

		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.QueryRow(`INSERT INTO pipeline_runs(template_pipeline_id, number, params, status, created_by, config_hash) VALUES ($1, 1, '{}', 'running', 'creator', 'hash') RETURNING id`, templateID).Scan(&runID)).To(Succeed())
		Expect(tx.QueryRow(`INSERT INTO pipelines(team_id, name, instance_vars, pipeline_run_id, secondary_ordering) VALUES ($1, 'detached-base', '{"run":1}', $2, 1) RETURNING id`, defaultTeam.ID(), runID).Scan(&childID)).To(Succeed())
		Expect(tx.Commit()).To(Succeed())

		Expect(dbConn.QueryRow(`INSERT INTO jobs(name, pipeline_id, config, active) VALUES ('run-job', $1, '{}', true) RETURNING id`, childID).Scan(&jobID)).To(Succeed())
		Expect(dbConn.QueryRow(`INSERT INTO builds(name, status, team_id, job_id, pipeline_id, pipeline_run_id, run_job_name, run_job_key) VALUES ('1', 'pending', $1, $2, $3, $4, 'run-job', 'policy') RETURNING id`, defaultTeam.ID(), jobID, childID, runID).Scan(&buildID)).To(Succeed())
		_, err = dbConn.Exec(`UPDATE builds SET job_id = NULL, pipeline_id = NULL WHERE id = $1`, buildID)
		Expect(err).NotTo(HaveOccurred())

		build, found, err := buildFactory.Build(buildID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		buildRunID, hasRun := build.PipelineRunID()
		Expect(hasRun).To(BeTrue())
		Expect(buildRunID).To(Equal(runID))
		Expect(build.BasePipelineID()).To(Equal(templateID))
		identity, found := build.TaskCacheIdentity()
		Expect(found).To(BeTrue())
		Expect(identity).To(Equal(atc.TaskCacheIdentity{
			TeamID:             defaultTeam.ID(),
			TemplatePipelineID: templateID,
			RunJobName:         "run-job",
		}))
		baseRef, found := build.BasePipelineRef()
		Expect(found).To(BeTrue())
		Expect(baseRef).To(Equal(atc.PipelineRef{Name: "detached-base"}))
	})

	It("hydrates a live unstamped run check build's base template without stamping run identity", func() {
		var templateID, runID, childID, resourceID, buildID int
		Expect(dbConn.QueryRow(`INSERT INTO pipelines(team_id, name, template, secondary_ordering) VALUES ($1, 'live-base', true, 1) RETURNING id`, defaultTeam.ID()).Scan(&templateID)).To(Succeed())

		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.QueryRow(`INSERT INTO pipeline_runs(template_pipeline_id, number, params, status, created_by, config_hash) VALUES ($1, 1, '{}', 'running', 'creator', 'hash') RETURNING id`, templateID).Scan(&runID)).To(Succeed())
		Expect(tx.QueryRow(`INSERT INTO pipelines(team_id, name, instance_vars, pipeline_run_id, secondary_ordering) VALUES ($1, 'live-base', '{"run":1}', $2, 1) RETURNING id`, defaultTeam.ID(), runID).Scan(&childID)).To(Succeed())
		Expect(tx.Commit()).To(Succeed())

		Expect(dbConn.QueryRow(`INSERT INTO resources(name, pipeline_id, type, config, active) VALUES ('run-resource', $1, 'some-base-resource-type', '{}', true) RETURNING id`, childID).Scan(&resourceID)).To(Succeed())
		Expect(dbConn.QueryRow(`INSERT INTO builds(name, status, team_id, resource_id, pipeline_id) VALUES ('check', 'pending', $1, $2, $3) RETURNING id`, defaultTeam.ID(), resourceID, childID).Scan(&buildID)).To(Succeed())

		build, found, err := buildFactory.Build(buildID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		buildRunID, hasRun := build.PipelineRunID()
		Expect(hasRun).To(BeFalse())
		Expect(buildRunID).To(BeZero())
		Expect(build.BasePipelineID()).To(Equal(templateID))
		_, found = build.TaskCacheIdentity()
		Expect(found).To(BeFalse())
		baseRef, found := build.BasePipelineRef()
		Expect(found).To(BeTrue())
		Expect(baseRef).To(Equal(atc.PipelineRef{Name: "live-base"}))
	})

	It("excludes payloads from normal pipeline lists while retaining templates and ordinary instances", func() {
		var templateID, runID int
		Expect(dbConn.QueryRow(`INSERT INTO pipelines(team_id, name, template, secondary_ordering) VALUES ($1, 'listed-base', true, 1) RETURNING id`, defaultTeam.ID()).Scan(&templateID)).To(Succeed())
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.QueryRow(`INSERT INTO pipeline_runs(template_pipeline_id, number, params, status, created_by, config_hash) VALUES ($1, 1, '{}', 'running', 'creator', 'hash') RETURNING id`, templateID).Scan(&runID)).To(Succeed())
		_, err = tx.Exec(`INSERT INTO pipelines(team_id, name, instance_vars, pipeline_run_id, secondary_ordering) VALUES ($1, 'listed-base', '{"run":1}', $2, 1)`, defaultTeam.ID(), runID)
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.Commit()).To(Succeed())
		_, created, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "ordinary-listed", InstanceVars: atc.InstanceVars{"run": float64(2)}}, atc.Config{}, db.ConfigVersion(0), false)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())

		pipelines, err := defaultTeam.Pipelines()
		Expect(err).NotTo(HaveOccurred())
		refs := make([]atc.PipelineRef, len(pipelines))
		for i, pipeline := range pipelines {
			refs[i] = pipeline.PipelineRef()
		}
		Expect(refs).To(ContainElement(atc.PipelineRef{Name: "listed-base"}))
		Expect(refs).To(ContainElement(atc.PipelineRef{Name: "ordinary-listed", InstanceVars: atc.InstanceVars{"run": float64(2)}}))
		Expect(refs).NotTo(ContainElement(atc.PipelineRef{Name: "listed-base", InstanceVars: atc.InstanceVars{"run": float64(1)}}))
	})

	It("classifies templates and payloads by each runtime query's role", func() {
		config := atc.Config{
			Jobs: atc.JobConfigs{{
				Name: "runtime-job",
				PlanSequence: []atc.Step{{
					Config: &atc.GetStep{Name: "runtime-resource"},
				}},
			}},
			Resources: atc.ResourceConfigs{{
				Name: "runtime-resource", Type: "some-base-resource-type", Source: atc.Source{"source": "runtime"},
			}},
			ResourceTypes: atc.ResourceTypes{{
				Name: "runtime-type", Type: "some-base-resource-type", Source: atc.Source{"source": "runtime"},
			}},
		}

		template, created, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "runtime-base"}, config, db.ConfigVersion(0), false)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		_, err = dbConn.Exec(`UPDATE pipelines SET template = true WHERE id = $1`, template.ID())
		Expect(err).NotTo(HaveOccurred())

		var runID int
		Expect(dbConn.QueryRow(`INSERT INTO pipeline_runs(template_pipeline_id, number, params, status, created_by, config_hash) VALUES ($1, 1, '{}', 'succeeded', 'creator', 'hash') RETURNING id`, template.ID()).Scan(&runID)).To(Succeed())

		var payloadID, payloadResourceID, payloadJobID int
		Expect(dbConn.QueryRow(`INSERT INTO pipelines(team_id, name, instance_vars, pipeline_run_id, secondary_ordering) VALUES ($1, 'runtime-base', '{"run":1}', $2, 1) RETURNING id`, defaultTeam.ID(), runID).Scan(&payloadID)).To(Succeed())
		Expect(dbConn.QueryRow(`INSERT INTO resources(name, pipeline_id, type, config, active) VALUES ('runtime-resource', $1, 'some-base-resource-type', '{"source":{"source":"runtime"}}', true) RETURNING id`, payloadID).Scan(&payloadResourceID)).To(Succeed())
		_, err = dbConn.Exec(`INSERT INTO resource_types(name, pipeline_id, type, config, active) VALUES ('runtime-type', $1, 'some-base-resource-type', '{"source":{"source":"runtime"}}', true)`, payloadID)
		Expect(err).NotTo(HaveOccurred())
		Expect(dbConn.QueryRow(`INSERT INTO jobs(name, pipeline_id, config, active) VALUES ('runtime-job', $1, '{}', true) RETURNING id`, payloadID).Scan(&payloadJobID)).To(Succeed())
		_, err = dbConn.Exec(`INSERT INTO job_inputs(name, job_id, resource_id) VALUES ('runtime-resource', $1, $2)`, payloadJobID, payloadResourceID)
		Expect(err).NotTo(HaveOccurred())

		payload, found, err := defaultTeam.Pipeline(atc.PipelineRef{Name: "runtime-base", InstanceVars: atc.InstanceVars{"run": float64(1)}})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())

		templateJob, found, err := template.Job("runtime-job")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		payloadJob, found, err := payload.Job("runtime-job")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(templateJob.RequestSchedule()).To(Succeed())
		Expect(payloadJob.RequestSchedule()).To(Succeed())

		pipelineFactory := db.NewPipelineFactory(dbConn, lockFactory)
		pipelines, err := pipelineFactory.PipelinesToSchedule()
		Expect(err).NotTo(HaveOccurred())
		scheduledPipelines := map[int]bool{}
		for _, pipeline := range pipelines {
			scheduledPipelines[pipeline.ID()] = true
		}
		Expect(scheduledPipelines).To(HaveKey(payloadID))
		Expect(scheduledPipelines).NotTo(HaveKey(template.ID()))

		jobFactory := db.NewJobFactory(dbConn, lockFactory)
		scheduledJobs, err := jobFactory.JobsToSchedule()
		Expect(err).NotTo(HaveOccurred())
		scheduledJobPipelines := map[int]bool{}
		for _, job := range scheduledJobs {
			scheduledJobPipelines[job.PipelineID()] = true
		}
		Expect(scheduledJobPipelines).To(HaveKey(payloadID))
		Expect(scheduledJobPipelines).NotTo(HaveKey(template.ID()))

		visibleJobs, err := jobFactory.VisibleJobs([]string{"default-team"})
		Expect(err).NotTo(HaveOccurred())
		for _, job := range visibleJobs {
			Expect(job.PipelineID).NotTo(Equal(payloadID))
		}
		allJobs, err := jobFactory.AllActiveJobs()
		Expect(err).NotTo(HaveOccurred())
		for _, job := range allJobs {
			Expect(job.PipelineID).NotTo(Equal(payloadID))
		}
		payloadDashboard, err := payload.Dashboard()
		Expect(err).NotTo(HaveOccurred())
		Expect(payloadDashboard).To(HaveLen(1))
		Expect(payloadDashboard[0].Name).To(Equal("runtime-job"))

		resources, err := checkFactory.Resources()
		Expect(err).NotTo(HaveOccurred())
		candidateResources := map[int]bool{}
		for _, resource := range resources {
			candidateResources[resource.PipelineID()] = true
		}
		Expect(candidateResources).To(HaveKey(payloadID))
		Expect(candidateResources).NotTo(HaveKey(template.ID()))

		resourceTypes, err := checkFactory.ResourceTypesByPipeline()
		Expect(err).NotTo(HaveOccurred())
		Expect(resourceTypes).To(HaveKey(payloadID))
		Expect(resourceTypes).NotTo(HaveKey(template.ID()))
	})

	_ = sql.ErrNoRows
})
