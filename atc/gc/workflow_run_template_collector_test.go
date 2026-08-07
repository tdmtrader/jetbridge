package gc_test

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/gc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("WorkflowRunTemplateCollector", func() {
	var (
		ctx       context.Context
		collector GcCollector
		factory   db.WorkflowRunTemplateFactory
		lifecycle db.WorkflowRunTemplateLifecycle
	)

	const retirement = 30 * 24 * time.Hour

	templateConfig := atc.Config{
		Template: true,
		Jobs: atc.JobConfigs{{
			Name:         "run",
			PlanSequence: []atc.Step{{Config: &atc.TaskStep{Name: "work", ConfigPath: "task.yml"}}},
		}},
	}

	BeforeEach(func() {
		ctx = context.Background()
		factory = db.NewWorkflowRunTemplateFactory(dbConn, lockFactory)
		lifecycle = db.NewWorkflowRunTemplateLifecycle(dbConn)
		collector = gc.NewWorkflowRunTemplateCollector(lifecycle, time.Hour, retirement)
	})

	saveTemplate := func(name string) db.Pipeline {
		template, created, err := factory.SaveWorkflowRunTemplate(
			ctx,
			defaultTeam.ID(),
			atc.PipelineRef{Name: name},
			templateConfig,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		return template
	}

	// Ownership age, rather than pipeline configuration age, controls the
	// abandoned grace period.
	ageOwnership := func(template db.Pipeline, age time.Duration) db.Pipeline {
		_, err := dbConn.Exec(`
			UPDATE agent_workflow_run_templates
			SET created_at = now() - $2::interval
			WHERE pipeline_id = $1
		`, template.ID(), age.String())
		Expect(err).NotTo(HaveOccurred())
		return template
	}

	pipelineExists := func(template db.Pipeline) bool {
		var exists bool
		Expect(dbConn.QueryRow(`
			SELECT EXISTS (SELECT 1 FROM pipelines WHERE id = $1)
		`, template.ID()).Scan(&exists)).To(Succeed())
		return exists
	}

	supersededWorkflowDefinition := func(name string) int {
		var citedID int
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by, schema_version, signature_version, live)
			VALUES ($1, 1, $2, 'schema_version: 3', 'collector-test', 3, 1, false)
			RETURNING id
		`, name, strings.Repeat("a", 64)).Scan(&citedID)).To(Succeed())
		_, err := dbConn.Exec(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by, schema_version, signature_version, live)
			VALUES ($1, 2, $2, 'schema_version: 3', 'collector-test', 3, 1, true)
		`, name, strings.Repeat("b", 64))
		Expect(err).NotTo(HaveOccurred())
		return citedID
	}

	// retiredTemplate creates every row required by the retirement predicate:
	// a superseded definition, archived terminal execution, archived instance,
	// and terminal durable citation with a complete execution link.
	retiredTemplate := func(
		templateName string,
		definitionName string,
		idempotencyKey string,
		completedAgo time.Duration,
	) db.Pipeline {
		citedDefinitionID := supersededWorkflowDefinition(definitionName)
		template := ageOwnership(saveTemplate(templateName), completedAgo+2*time.Hour)
		instance, created, err := defaultTeam.SavePipeline(
			atc.PipelineRef{Name: template.Name(), InstanceVars: atc.InstanceVars{"run": 1}},
			atc.Config{Jobs: atc.JobConfigs{{Name: "run"}}},
			db.ConfigVersion(0),
			false,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		_, err = dbConn.Exec(`
			UPDATE pipelines
			SET archived = true, paused = true
			WHERE id = $1
		`, instance.ID())
		Expect(err).NotTo(HaveOccurred())

		var pipelineRunID int
		Expect(dbConn.QueryRow(`
			INSERT INTO pipeline_runs
				(template_pipeline_id, instance_pipeline_id, number, status, archived, created_at, completed_at)
			VALUES ($1, $2, 1, 'succeeded', true,
				now() - ($3::interval + interval '2 hours'),
				now() - $3::interval)
			RETURNING id
		`, template.ID(), instance.ID(), completedAgo.String()).Scan(&pipelineRunID)).To(Succeed())

		_, err = dbConn.Exec(`
			INSERT INTO agent_workflow_runs
				(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
				 schema_version, signature_version, definition_content_hash, idempotency_key,
				 parameterized_config, parameterized_config_hash, origin_kind, origin_reference,
				 created_by, status, pipeline_run_id, template_pipeline_id, instance_pipeline_id,
				 concrete_config, concrete_config_hash, created_at, updated_at, started_at, completed_at)
			VALUES ($1, $2, $3, $4, 1, 3, 1, $5, $6, '{}'::jsonb, $5,
				'collector-test', 'retirement', 'collector-test', 'succeeded', $7, $8, $9,
				'{}'::jsonb, $5,
				now() - ($10::interval + interval '2 hours'),
				now() - $10::interval,
				now() - ($10::interval + interval '1 hour'),
				now() - $10::interval)
		`, defaultTeam.ID(), defaultTeam.Name(), citedDefinitionID, definitionName,
			strings.Repeat("a", 64), idempotencyKey, pipelineRunID, template.ID(), instance.ID(),
			completedAgo.String())
		Expect(err).NotTo(HaveOccurred())
		return template
	}

	It("removes an abandoned template after the grace period and retains a fresh peer", func() {
		expired := ageOwnership(saveTemplate("collector-abandoned-expired"), 2*time.Hour)
		fresh := ageOwnership(saveTemplate("collector-abandoned-fresh"), 30*time.Minute)

		Expect(collector.Run(ctx)).To(Succeed())

		Expect(pipelineExists(expired)).To(BeFalse())
		Expect(pipelineExists(fresh)).To(BeTrue())
	})

	It("removes a fully eligible retired template and retains a peer inside the retirement period", func() {
		expired := retiredTemplate(
			"collector-retired-expired",
			"collector-retired-expired-definition",
			"collector-retired-expired-key",
			31*24*time.Hour,
		)
		fresh := retiredTemplate(
			"collector-retired-fresh",
			"collector-retired-fresh-definition",
			"collector-retired-fresh-key",
			29*24*time.Hour,
		)

		Expect(collector.Run(ctx)).To(Succeed())

		Expect(pipelineExists(expired)).To(BeFalse())
		Expect(pipelineExists(fresh)).To(BeTrue())
	})

	It("still removes an abandoned template when retirement is disabled without removing an eligible retired template", func() {
		abandoned := ageOwnership(saveTemplate("collector-disabled-abandoned"), 2*time.Hour)
		retired := retiredTemplate(
			"collector-disabled-retired",
			"collector-disabled-retired-definition",
			"collector-disabled-retired-key",
			31*24*time.Hour,
		)
		disabled := gc.NewWorkflowRunTemplateCollector(lifecycle, time.Hour, 0)

		Expect(disabled.Run(ctx)).To(Succeed())

		Expect(pipelineExists(abandoned)).To(BeFalse())
		Expect(pipelineExists(retired)).To(BeTrue())
	})

	It("returns the abandoned-pass failure without starting retirement", func() {
		// A real connection cannot deterministically inject a lifecycle method
		// failure, so this fake is limited to the collector's error boundary.
		fault := new(dbfakes.FakeWorkflowRunTemplateLifecycle)
		var callOrder []string
		fault.RemoveAbandonedWorkflowRunTemplatesStub = func(context.Context, time.Duration, int) (int, error) {
			callOrder = append(callOrder, "abandoned")
			return 0, errors.New("nope")
		}
		fault.RemoveRetiredWorkflowRunTemplatesStub = func(context.Context, time.Duration, int) (int, error) {
			callOrder = append(callOrder, "retired")
			return 0, nil
		}
		failing := gc.NewWorkflowRunTemplateCollector(fault, time.Hour, retirement)

		Expect(failing.Run(ctx)).To(MatchError("nope"))

		Expect(callOrder).To(Equal([]string{"abandoned"}))
		Expect(fault.RemoveAbandonedWorkflowRunTemplatesCallCount()).To(Equal(1))
		callCtx, gracePeriod, limit := fault.RemoveAbandonedWorkflowRunTemplatesArgsForCall(0)
		Expect(callCtx).To(Equal(ctx))
		Expect(gracePeriod).To(Equal(time.Hour))
		Expect(limit).To(BeNumerically(">", 0))
		Expect(limit).To(BeNumerically("<=", db.MaxAbandonedWorkflowRunTemplateBatch))
		Expect(fault.RemoveRetiredWorkflowRunTemplatesCallCount()).To(Equal(0))
	})

	It("returns the retired-pass failure after the abandoned pass", func() {
		// A real connection cannot deterministically inject a lifecycle method
		// failure, so this fake is limited to the collector's error boundary.
		fault := new(dbfakes.FakeWorkflowRunTemplateLifecycle)
		var callOrder []string
		fault.RemoveAbandonedWorkflowRunTemplatesStub = func(context.Context, time.Duration, int) (int, error) {
			callOrder = append(callOrder, "abandoned")
			return 0, nil
		}
		fault.RemoveRetiredWorkflowRunTemplatesStub = func(context.Context, time.Duration, int) (int, error) {
			callOrder = append(callOrder, "retired")
			return 0, errors.New("retired-nope")
		}
		failing := gc.NewWorkflowRunTemplateCollector(fault, time.Hour, retirement)

		Expect(failing.Run(ctx)).To(MatchError("retired-nope"))

		Expect(callOrder).To(Equal([]string{"abandoned", "retired"}))
		Expect(fault.RemoveAbandonedWorkflowRunTemplatesCallCount()).To(Equal(1))
		callCtx, gracePeriod, abandonedLimit := fault.RemoveAbandonedWorkflowRunTemplatesArgsForCall(0)
		Expect(callCtx).To(Equal(ctx))
		Expect(gracePeriod).To(Equal(time.Hour))
		Expect(abandonedLimit).To(BeNumerically(">", 0))
		Expect(abandonedLimit).To(BeNumerically("<=", db.MaxAbandonedWorkflowRunTemplateBatch))
		Expect(fault.RemoveRetiredWorkflowRunTemplatesCallCount()).To(Equal(1))
		callCtx, retirementPeriod, retiredLimit := fault.RemoveRetiredWorkflowRunTemplatesArgsForCall(0)
		Expect(callCtx).To(Equal(ctx))
		Expect(retirementPeriod).To(Equal(retirement))
		Expect(retiredLimit).To(BeNumerically(">", 0))
		Expect(retiredLimit).To(BeNumerically("<=", db.MaxRetiredWorkflowRunTemplateBatch))
	})
})
