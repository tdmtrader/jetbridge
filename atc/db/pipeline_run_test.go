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

	_ = sql.ErrNoRows
})
