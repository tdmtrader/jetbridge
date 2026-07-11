package db_test

import (
	"fmt"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("PipelineRunFactory", func() {
	var (
		factory  db.PipelineRunFactory
		template db.Pipeline
	)

	templateConfig := atc.Config{
		Template: true,
		Params: []atc.ParamSchema{
			{Name: "greeting", Type: "string", Default: "hello"},
		},
		Resources: atc.ResourceConfigs{
			// marker exercises both reserved vars: ((run)) = per-template
			// number, ((run_id)) = global pipeline_runs.id (F30, 2026-07-09)
			{Name: "some-resource", Type: "some-base-resource-type", Source: atc.Source{"some": "((greeting))", "marker": "run-((run))-id-((run_id))"}},
		},
		Jobs: atc.JobConfigs{
			{
				Name: "entry",
				PlanSequence: []atc.Step{
					{Config: &atc.TaskStep{Name: "t", ConfigPath: "task.yml"}},
				},
			},
			{
				Name: "downstream",
				PlanSequence: []atc.Step{
					{Config: &atc.GetStep{Name: "some-resource", Passed: []string{"entry"}, Trigger: true}},
				},
			},
		},
	}

	BeforeEach(func() {
		// logger and checkFactory are db-suite globals (db_suite_test.go:70/:47);
		// the CheckFactory is injected so CreateRun itself enqueues the frozen
		// check set (F27, 2026-07-09)
		factory = db.NewPipelineRunFactory(logger, dbConn, lockFactory, checkFactory)

		var err error
		template, _, err = defaultTeam.SavePipeline(
			atc.PipelineRef{Name: "run-template"}, templateConfig, db.ConfigVersion(0), false)
		Expect(err).ToNot(HaveOccurred())
	})

	It("creates numbered runs with materialized instance pipelines and entry builds", func() {
		run, err := factory.CreateRun(template.ID(), nil, "some-user")
		Expect(err).ToNot(HaveOccurred())

		Expect(run.Number()).To(Equal(1))
		Expect(run.Status()).To(Equal(db.PipelineRunRunning))
		Expect(run.CreatedBy()).To(Equal("some-user"))
		Expect(run.Params()).To(Equal(map[string]any{"greeting": "hello"}))
		Expect(run.TemplatePipelineID()).To(Equal(template.ID()))

		instance, found, err := run.InstancePipeline()
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(instance.InstanceVars()).To(Equal(atc.InstanceVars{"run": float64(1)}))
		Expect(instance.Template()).To(BeTrue())

		instanceConfig, err := instance.Config()
		Expect(err).ToNot(HaveOccurred())
		Expect(instanceConfig.Resources[0].Source["some"]).To(Equal("hello"))
		// F30 (2026-07-09): ((run_id)) resolved to the pre-allocated
		// pipeline_runs.id, ((run)) to the per-template number
		Expect(instanceConfig.Resources[0].Source["marker"]).To(Equal(fmt.Sprintf("run-1-id-%d", run.ID())))
		// the downstream get has passed: [entry], so it KEEPS trigger: true
		// (passed-chain flow); only non-passed gets are stripped
		Expect(instanceConfig.Jobs[1].Inputs()[0].Trigger).To(BeTrue())

		entryJob, found, err := instance.Job("entry")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		pending, err := entryJob.GetPendingBuilds()
		Expect(err).ToNot(HaveOccurred())
		Expect(pending).To(HaveLen(1))

		downstreamJob, _, err := instance.Job("downstream")
		Expect(err).ToNot(HaveOccurred())
		pending, err = downstreamJob.GetPendingBuilds()
		Expect(err).ToNot(HaveOccurred())
		Expect(pending).To(BeEmpty())

		second, err := factory.CreateRun(template.ID(), map[string]any{"greeting": "hi"}, "some-user")
		Expect(err).ToNot(HaveOccurred())
		Expect(second.Number()).To(Equal(2))
	})

	It("rejects invalid params and non-templates", func() {
		_, err := factory.CreateRun(template.ID(), map[string]any{"bogus": "x"}, "u")
		Expect(err).To(MatchError(ContainSubstring(`unknown param "bogus"`)))

		_, err = factory.CreateRun(defaultPipeline.ID(), nil, "u")
		Expect(err).To(MatchError(db.ErrNotATemplate))
	})

	It("gets and lists runs", func() {
		one, err := factory.CreateRun(template.ID(), nil, "u")
		Expect(err).ToNot(HaveOccurred())
		_, err = factory.CreateRun(template.ID(), nil, "u")
		Expect(err).ToNot(HaveOccurred())

		got, found, err := factory.GetRun(template.ID(), 1)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(got.ID()).To(Equal(one.ID()))

		_, found, err = factory.GetRun(template.ID(), 99)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())

		runs, err := factory.ListRuns(template.ID(), 10)
		Expect(err).ToNot(HaveOccurred())
		Expect(runs).To(HaveLen(2))
		Expect(runs[0].Number()).To(Equal(2)) // newest first

		running, err := factory.RunningRuns()
		Expect(err).ToNot(HaveOccurred())
		Expect(len(running)).To(BeNumerically(">=", 2))
	})

	// F27 (2026-07-09): the frozen-check enqueue lives in the FACTORY, not
	// the API handler — lidar excludes template pipelines, so a run created
	// by an in-process consumer (dispatch, experiments) whose entry job has
	// a get step would otherwise pend forever on an empty version set.
	It("enqueues the frozen check set at creation so get-step entry jobs get versions", func() {
		getEntryConfig := atc.Config{
			Template: true,
			Resources: atc.ResourceConfigs{
				{Name: "some-resource", Type: "some-base-resource-type", Source: atc.Source{"some": "source"}},
			},
			Jobs: atc.JobConfigs{
				{Name: "entry-get", PlanSequence: []atc.Step{
					{Config: &atc.GetStep{Name: "some-resource", Trigger: true}},
				}},
			},
		}
		getTemplate, _, err := defaultTeam.SavePipeline(
			atc.PipelineRef{Name: "frozen-check-template"}, getEntryConfig, db.ConfigVersion(0), false)
		Expect(err).ToNot(HaveOccurred())

		run, err := factory.CreateRun(getTemplate.ID(), nil, "some-user")
		Expect(err).ToNot(HaveOccurred())

		instance, found, err := run.InstancePipeline()
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())

		resource, found, err := instance.Resource("some-resource")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())

		// exactly one manually-triggered check build persisted for the
		// instance resource (TryCreateCheck toDB=true writes a builds row
		// with resource_id set)
		var checkBuilds int
		err = dbConn.QueryRow(
			`SELECT COUNT(*) FROM builds WHERE resource_id = $1`, resource.ID()).
			Scan(&checkBuilds)
		Expect(err).ToNot(HaveOccurred())
		Expect(checkBuilds).To(Equal(1))
	})
})
