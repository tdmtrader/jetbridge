package db_test

import (
	"context"
	"errors"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Task cache identity", func() {
	It("clears a shared run cache from either a live run job or a literal template job", func() {
		// This fails if cache clearing keeps an ephemeral run job ID instead of
		// the base template and materialized job name.
		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "cache-clear-template"}, atc.Config{
			Template: true,
			Jobs:     atc.JobConfigs{{Name: "deploy"}},
		}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		creation, err := db.NewPipelineRunFactory(dbConn, lockFactory).CreateRun(context.Background(), template, db.RunParams{}, "creator")
		Expect(err).NotTo(HaveOccurred())
		payload, found, err := defaultTeam.Pipeline(atc.PipelineRef{Name: "cache-clear-template", InstanceVars: atc.InstanceVars{"run": float64(creation.Run.Number())}})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		runJob, found, err := payload.Job("deploy")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		identity, err := runJob.TaskCacheIdentity()
		Expect(err).NotTo(HaveOccurred())
		Expect(identity).To(Equal(atc.TaskCacheIdentity{TeamID: defaultTeam.ID(), TemplatePipelineID: template.ID(), RunJobName: "deploy"}))
		_, err = taskCacheFactory.FindOrCreate(identity, "task", "cache")
		Expect(err).NotTo(HaveOccurred())
		deleted, err := runJob.ClearTaskCache("task", "cache")
		Expect(err).NotTo(HaveOccurred())
		Expect(deleted).To(Equal(int64(1)))
		_, found, err = taskCacheFactory.Find(identity, "task", "cache")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
		_, err = taskCacheFactory.FindOrCreate(identity, "task", "cache")
		Expect(err).NotTo(HaveOccurred())

		baseJob, found, err := template.Job("deploy")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		deleted, err = baseJob.ClearTaskCache("task", "cache")
		Expect(err).NotTo(HaveOccurred())
		Expect(deleted).To(Equal(int64(1)))
		_, found, err = taskCacheFactory.Find(identity, "task", "cache")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	It("retains run caches through payload deletion and template config cleanup", func() {
		// This fails if run-form rows follow disposable payload jobs through
		// deletion or the ordinary config-cleanup join.
		config := atc.Config{Template: true, Jobs: atc.JobConfigs{{Name: "deploy"}}}
		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "retained-run-cache-template"}, config, 0, false)
		Expect(err).NotTo(HaveOccurred())
		creation, err := db.NewPipelineRunFactory(dbConn, lockFactory).CreateRun(context.Background(), template, db.RunParams{}, "creator")
		Expect(err).NotTo(HaveOccurred())
		identity := atc.TaskCacheIdentity{TeamID: defaultTeam.ID(), TemplatePipelineID: template.ID(), RunJobName: "deploy"}
		_, err = taskCacheFactory.FindOrCreate(identity, "task", "cache")
		Expect(err).NotTo(HaveOccurred())
		reclaimRunPayloadForTest(template, creation.Run)
		_, found, err := taskCacheFactory.Find(identity, "task", "cache")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())

		_, _, err = defaultTeam.SavePipeline(atc.PipelineRef{Name: "retained-run-cache-template"}, config, template.ConfigVersion(), false)
		Expect(err).NotTo(HaveOccurred())
		_, found, err = taskCacheFactory.Find(identity, "task", "cache")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
	})

	It("deletes run caches when their base template is deleted", func() {
		// This fails if a base pipeline can be removed while its shared cache row survives.
		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "cascade-run-cache-template"}, atc.Config{Template: true, Jobs: atc.JobConfigs{{Name: "deploy"}}}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		identity := atc.TaskCacheIdentity{TeamID: defaultTeam.ID(), TemplatePipelineID: template.ID(), RunJobName: "deploy"}
		_, err = taskCacheFactory.FindOrCreate(identity, "task", "cache")
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`DELETE FROM pipelines WHERE id = $1`, template.ID())
		Expect(err).NotTo(HaveOccurred())
		_, found, err := taskCacheFactory.Find(identity, "task", "cache")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	It("requires a live run job to clear an interpolated template job cache", func() {
		// This fails if a dynamic base job name is treated as one materialized cache scope.
		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "dynamic-cache-template"}, atc.Config{
			Template: true,
			Params:   []atc.ParamSchema{{Name: "environment", Type: atc.ParamTypeString, Required: true}},
			Jobs:     atc.JobConfigs{{Name: "deploy-((environment))"}},
		}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		job, found, err := template.Job("deploy-((environment))")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		_, err = job.TaskCacheIdentity()
		var conflict db.TaskCacheIdentityConflictError
		Expect(errors.As(err, &conflict)).To(BeTrue())
	})
})
