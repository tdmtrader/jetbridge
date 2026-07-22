package db_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("WorkflowRunTemplateFactory", func() {
	var factory db.WorkflowRunTemplateFactory

	config := atc.Config{
		Template: true,
		Jobs: atc.JobConfigs{{
			Name:         "run",
			PlanSequence: []atc.Step{{Config: &atc.TaskStep{Name: "work", ConfigPath: "task.yml"}}},
		}},
	}

	BeforeEach(func() {
		factory = db.NewWorkflowRunTemplateFactory(dbConn, lockFactory)
	})

	It("creates the immutable template and ownership marker together", func() {
		pipeline, created, err := factory.SaveWorkflowRunTemplate(
			context.Background(), defaultTeam.ID(), atc.PipelineRef{Name: "owned-workflow-template"}, config,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())

		owned, err := factory.IsWorkflowRunTemplate(context.Background(), pipeline.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(owned).To(BeTrue())

		ordinary, _, err := defaultTeam.SavePipeline(
			atc.PipelineRef{Name: "ordinary-run-template"}, config, db.ConfigVersion(0), false,
		)
		Expect(err).NotTo(HaveOccurred())
		owned, err = factory.IsWorkflowRunTemplate(context.Background(), ordinary.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(owned).To(BeFalse())
	})

	It("never claims an existing ordinary pipeline even when its config is exact", func() {
		ordinary, _, err := defaultTeam.SavePipeline(
			atc.PipelineRef{Name: "occupied-workflow-template"}, config, db.ConfigVersion(0), false,
		)
		Expect(err).NotTo(HaveOccurred())

		_, _, err = factory.SaveWorkflowRunTemplate(
			context.Background(), defaultTeam.ID(), atc.PipelineRef{Name: ordinary.Name()}, config,
		)
		Expect(errors.Is(err, db.ErrConfigComparisonFailed)).To(BeTrue())

		owned, err := factory.IsWorkflowRunTemplate(context.Background(), ordinary.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(owned).To(BeFalse())
	})

	It("allows only the workflow owner to clean up a template with no execution history", func() {
		pipeline, created, err := factory.SaveWorkflowRunTemplate(
			context.Background(), defaultTeam.ID(), atc.PipelineRef{Name: "unused-owned-workflow-template"}, config,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())

		destroyed, err := factory.DestroyUnusedWorkflowRunTemplate(context.Background(), pipeline.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(BeTrue())

		_, found, err := defaultTeam.Pipeline(atc.PipelineRef{Name: pipeline.Name()})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
		owned, err := factory.IsWorkflowRunTemplate(context.Background(), pipeline.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(owned).To(BeFalse())
	})

	It("preserves a workflow template once execution history references it", func() {
		pipeline, created, err := factory.SaveWorkflowRunTemplate(
			context.Background(), defaultTeam.ID(), atc.PipelineRef{Name: "used-owned-workflow-template"}, config,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		_, err = dbConn.Exec(`
			INSERT INTO pipeline_runs (template_pipeline_id, number, params, created_by)
			VALUES ($1, 1, '{}', 'workflow-owner')
		`, pipeline.ID())
		Expect(err).NotTo(HaveOccurred())

		destroyed, err := factory.DestroyUnusedWorkflowRunTemplate(context.Background(), pipeline.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(BeFalse())

		_, found, err := defaultTeam.Pipeline(atc.PipelineRef{Name: pipeline.Name()})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		owned, err := factory.IsWorkflowRunTemplate(context.Background(), pipeline.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(owned).To(BeTrue())
	})

	It("refuses to register an instance or non-template pipeline", func() {
		_, _, err := factory.SaveWorkflowRunTemplate(
			context.Background(), defaultTeam.ID(), atc.PipelineRef{Name: "not-template"}, atc.Config{},
		)
		Expect(err).To(MatchError(db.ErrNotATemplate))

		_, _, err = factory.SaveWorkflowRunTemplate(
			context.Background(), defaultTeam.ID(), atc.PipelineRef{Name: "instance", InstanceVars: atc.InstanceVars{"run": 1}}, config,
		)
		Expect(err).To(MatchError(db.ErrNotATemplate))
	})

	It("rejects every ordinary mutation of an owned base template", func() {
		mutations := []struct {
			name    string
			prepare func(db.Pipeline) error
			mutate  func(db.Pipeline) error
		}{
			{name: "config replacement", mutate: func(p db.Pipeline) error {
				_, _, err := defaultTeam.SavePipeline(
					atc.PipelineRef{Name: p.Name()}, config, p.ConfigVersion(), false,
				)
				return err
			}},
			{name: "rename", mutate: func(p db.Pipeline) error {
				_, err := defaultTeam.RenamePipeline(p.Name(), p.Name()+"-renamed")
				return err
			}},
			{name: "pause", mutate: func(p db.Pipeline) error { return p.Pause("alice") }},
			{name: "unpause", prepare: func(p db.Pipeline) error {
				_, err := dbConn.Exec(`UPDATE pipelines SET paused = true WHERE id = $1`, p.ID())
				return err
			}, mutate: func(p db.Pipeline) error { return p.Unpause() }},
			{name: "archive", mutate: func(p db.Pipeline) error { return p.Archive() }},
			{name: "expose", mutate: func(p db.Pipeline) error { return p.Expose() }},
			{name: "hide", prepare: func(p db.Pipeline) error {
				_, err := dbConn.Exec(`UPDATE pipelines SET public = true WHERE id = $1`, p.ID())
				return err
			}, mutate: func(p db.Pipeline) error { return p.Hide() }},
			{name: "destroy", mutate: func(p db.Pipeline) error { return p.Destroy() }},
		}

		for i, mutation := range mutations {
			ref := atc.PipelineRef{Name: fmt.Sprintf("immutable-workflow-template-%d", i)}
			pipeline, created, err := factory.SaveWorkflowRunTemplate(
				context.Background(), defaultTeam.ID(), ref, config,
			)
			Expect(err).NotTo(HaveOccurred(), mutation.name)
			Expect(created).To(BeTrue(), mutation.name)
			if mutation.prepare != nil {
				Expect(mutation.prepare(pipeline)).To(Succeed(), mutation.name)
				_, err = pipeline.Reload()
				Expect(err).NotTo(HaveOccurred(), mutation.name)
			}
			beforeVersion := pipeline.ConfigVersion()
			beforePaused := pipeline.Paused()
			beforeArchived := pipeline.Archived()
			beforePublic := pipeline.Public()

			err = mutation.mutate(pipeline)
			Expect(errors.Is(err, db.ErrWorkflowRunTemplateImmutable)).To(BeTrue(), mutation.name)

			stored, found, err := defaultTeam.Pipeline(ref)
			Expect(err).NotTo(HaveOccurred(), mutation.name)
			Expect(found).To(BeTrue(), mutation.name)
			Expect(stored.Name()).To(Equal(ref.Name), mutation.name)
			Expect(stored.ConfigVersion()).To(Equal(beforeVersion), mutation.name)
			Expect(stored.Paused()).To(Equal(beforePaused), mutation.name)
			Expect(stored.Archived()).To(Equal(beforeArchived), mutation.name)
			Expect(stored.Public()).To(Equal(beforePublic), mutation.name)
			owned, err := factory.IsWorkflowRunTemplate(context.Background(), stored.ID())
			Expect(err).NotTo(HaveOccurred(), mutation.name)
			Expect(owned).To(BeTrue(), mutation.name)
		}
	})
})
