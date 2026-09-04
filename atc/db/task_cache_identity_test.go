package db_test

import (
	"context"
	"errors"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/configvalidate"
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

var _ = Describe("Run task cache scope", func() {
	// A run payload's caches are keyed on template identity, so every run of a
	// template that declares `caches:` writes into one shared, node-local
	// directory that nothing ever sweeps: the artifact daemon skips /caches/
	// entirely and the task cache collector only deletes DB rows. Growth is
	// therefore unbounded and unreclaimable, so a run payload gets no cache
	// scope at all unless the template asks for one.
	runPayloadEntryBuild := func(name string, config atc.Config) db.Build {
		GinkgoHelper()

		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: name}, config, 0, false)
		Expect(err).NotTo(HaveOccurred())

		creation, err := db.NewPipelineRunFactory(dbConn, lockFactory).CreateRun(context.Background(), template, db.RunParams{}, "creator")
		Expect(err).NotTo(HaveOccurred())
		Expect(creation.EntryBuilds).To(HaveLen(1))

		build, found, err := buildFactory.Build(creation.EntryBuilds[0].ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		return build
	}

	taskCacheRows := func() int {
		GinkgoHelper()

		var count int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM task_caches`).Scan(&count)).To(Succeed())
		return count
	}

	// registerCaches, the only writer of task_caches for a build, runs exactly
	// once per task step and only when the build resolved a cache scope. This
	// mirrors that gate so the spec measures rows, not just the boolean.
	registerCachesFor := func(build db.Build) {
		GinkgoHelper()

		identity, found := build.TaskCacheIdentity()
		if !found {
			return
		}
		_, err := taskCacheFactory.FindOrCreate(identity, "task", "cache")
		Expect(err).NotTo(HaveOccurred())
	}

	It("resolves no cache scope and writes no task_caches rows for a template that does not opt in", func() {
		build := runPayloadEntryBuild("default-cache-scope-template", atc.Config{
			Template: true,
			Jobs:     atc.JobConfigs{{Name: "deploy"}},
		})

		_, found := build.TaskCacheIdentity()
		Expect(found).To(BeFalse(), "a run payload must have no writable task cache scope by default")

		registerCachesFor(build)
		Expect(taskCacheRows()).To(BeZero())
	})

	It("resolves the shared template scope when the template opts in", func() {
		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "opted-in-cache-scope-template"}, atc.Config{
			Template:   true,
			CacheScope: atc.CacheScopeTemplate,
			Jobs:       atc.JobConfigs{{Name: "deploy"}},
		}, 0, false)
		Expect(err).NotTo(HaveOccurred())

		creation, err := db.NewPipelineRunFactory(dbConn, lockFactory).CreateRun(context.Background(), template, db.RunParams{}, "creator")
		Expect(err).NotTo(HaveOccurred())
		build, found, err := buildFactory.Build(creation.EntryBuilds[0].ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())

		identity, found := build.TaskCacheIdentity()
		Expect(found).To(BeTrue())
		Expect(identity).To(Equal(atc.TaskCacheIdentity{
			TeamID:             defaultTeam.ID(),
			TemplatePipelineID: template.ID(),
			RunJobName:         "deploy",
		}))

		registerCachesFor(build)
		Expect(taskCacheRows()).To(Equal(1))
	})

	It("keeps the opt-in on the template row and off the run payload config", func() {
		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "payload-cache-scope-template"}, atc.Config{
			Template:   true,
			CacheScope: atc.CacheScopeTemplate,
			Jobs:       atc.JobConfigs{{Name: "deploy"}},
		}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(template.CacheScope()).To(Equal(atc.CacheScopeTemplate))

		creation, err := db.NewPipelineRunFactory(dbConn, lockFactory).CreateRun(context.Background(), template, db.RunParams{}, "creator")
		Expect(err).NotTo(HaveOccurred())
		Expect(creation.Config.CacheScope).To(BeEmpty(), "a materialized payload config carries no cache scope of its own")

		payload, found, err := defaultTeam.Pipeline(atc.PipelineRef{Name: "payload-cache-scope-template", InstanceVars: atc.InstanceVars{"run": float64(creation.Run.Number())}})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(payload.CacheScope()).To(BeEmpty())
	})

	It("leaves ordinary job-scoped caches alone", func() {
		build, err := defaultJob.CreateBuild("someone")
		Expect(err).NotTo(HaveOccurred())

		identity, found := build.TaskCacheIdentity()
		Expect(found).To(BeTrue())
		Expect(identity).To(Equal(atc.TaskCacheIdentity{JobID: defaultJob.ID()}))
	})

	It("refuses a cache scope on a pipeline that is not a template", func() {
		err := configvalidate.ValidateTemplateDeclaration(atc.PipelineRef{Name: "ordinary"}, atc.Config{
			CacheScope: atc.CacheScopeTemplate,
			Jobs:       atc.JobConfigs{{Name: "deploy"}},
		})
		Expect(err).To(MatchError("cache_scope is only valid on templates"))
	})

	It("refuses an unknown cache scope on a template", func() {
		err := configvalidate.ValidateTemplateDeclaration(atc.PipelineRef{Name: "template"}, atc.Config{
			Template:   true,
			CacheScope: "everything",
			Jobs:       atc.JobConfigs{{Name: "deploy"}},
		})
		Expect(err).To(MatchError(`cache_scope must be "template" or "none"`))
	})
})
