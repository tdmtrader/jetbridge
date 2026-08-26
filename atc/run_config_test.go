package atc_test

import (
	"encoding/json"

	. "github.com/concourse/concourse/atc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Run config materialization", func() {
	It("interpolates normalized parameters and authoritative run values without mutating the template", func() {
		// This fails if materialization mutates the template, lets caller-supplied
		// reserved values win, or resolves runtime variable-source references.
		config := Config{
			Template: true,
			Params:   []ParamSchema{{Name: "environment", Type: ParamTypeString, Default: "staging"}},
			Jobs: JobConfigs{{
				Name: "deploy-((environment))-((run))-((run_id))",
				PlanSequence: []Step{{Config: &TaskStep{
					Name:   "deploy-((environment))",
					Config: &TaskConfig{Platform: "linux", Run: TaskRunConfig{Path: "echo", Args: []string{"((runtime:token))"}}},
				}}},
			}},
		}

		result, err := MaterializeRunConfig(config, RunIdentity{Number: 12, ID: 99}, RunParams{
			"run":    "untrusted",
			"run_id": "untrusted",
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(result.Config.Template).To(BeFalse())
		Expect(result.Config.Jobs[0].Name).To(Equal("deploy-staging-12-99"))
		Expect(result.Config.Jobs[0].PlanSequence[0].Config.(*TaskStep).Config.Run.Args).To(Equal([]string{"((runtime:token))"}))
		Expect(config.Template).To(BeTrue())
		Expect(config.Jobs[0].Name).To(Equal("deploy-((environment))-((run))-((run_id))"))
		Expect(json.Valid(result.CanonicalJSON)).To(BeTrue())
		Expect(result.CanonicalJSON).To(MatchJSON(`{"jobs":[{"name":"deploy-staging-12-99","plan":[{"task":"deploy-staging","config":{"platform":"linux","run":{"path":"echo","args":["((runtime:token))"]}}}]}]}`))
	})

	It("clears source triggers but preserves passed triggers in the materialized graph", func() {
		// This fails if a run can be automatically started by a new source version,
		// or if a passed edge no longer triggers its reachable job.
		config := Config{Jobs: JobConfigs{
			{Name: "entry", PlanSequence: []Step{{Config: &GetStep{Name: "source", Trigger: true}}}},
			{Name: "deploy", PlanSequence: []Step{{Config: &GetStep{Name: "source", Passed: []string{"entry"}, Trigger: true}}}},
		}}

		result, err := MaterializeRunConfig(config, RunIdentity{}, RunParams{})

		Expect(err).NotTo(HaveOccurred())
		Expect(result.Config.Jobs[0].Inputs()[0].Trigger).To(BeFalse())
		Expect(result.Config.Jobs[1].Inputs()[0].Trigger).To(BeTrue())
	})

	It("rejects colliding materialized job names", func() {
		// This fails if one run silently overwrites another job in its materialized graph.
		_, err := MaterializeRunConfig(Config{
			Params: []ParamSchema{{Name: "environment", Type: ParamTypeString}},
			Jobs:   JobConfigs{{Name: "deploy-((environment))"}, {Name: "deploy-staging"}},
		}, RunIdentity{}, RunParams{"environment": "staging"})

		Expect(err).To(MatchError(ContainSubstring("duplicate job name deploy-staging")))
	})

	It("maps dynamic job names to their source policy keys", func() {
		// This fails if later policy lookup uses an interpolated job name instead of
		// the template job identity from which it was produced.
		result, err := MaterializeRunConfig(Config{
			Params: []ParamSchema{{Name: "environment", Type: ParamTypeString}},
			Jobs: JobConfigs{
				{Name: "entry"},
				{Name: "deploy-((environment))", PlanSequence: []Step{{Config: &GetStep{Passed: []string{"entry"}, Trigger: true}}}},
			},
		}, RunIdentity{}, RunParams{"environment": "staging"})

		Expect(err).NotTo(HaveOccurred())
		Expect(result.PolicyKeyByJobName).To(Equal(map[string]string{
			"deploy-staging": "deploy-((environment))",
		}))
	})

	It("clears the template declaration from the materialized config and payload", func() {
		// This fails if a materialized run keeps its template's parameter schema or
		// retention policy. Nothing reads either from a payload: the effective template
		// config is rebuilt from the template row, every retention predicate joins
		// pipeline_runs back to its template, and a parameter schema is presented only
		// for a template. Keeping them makes `fly get-pipeline` on a run emit a config
		// that ValidateTemplateDeclaration refuses with "params are only valid on
		// templates", while the config hash digests a schema the payload does not have.
		keepLast := 5
		ttlDays := 30
		config := Config{
			Template: true,
			Params: []ParamSchema{{
				Name: "environment", Type: ParamTypeString, Default: "staging",
			}},
			RunRetention: &RunRetentionConfig{KeepLast: &keepLast, TTLDays: &ttlDays},
			Jobs:         JobConfigs{{Name: "entry-((environment))", PlanSequence: []Step{}}},
		}

		result, err := MaterializeRunConfig(config, RunIdentity{}, RunParams{})

		Expect(err).NotTo(HaveOccurred())
		Expect(result.Config.Template).To(BeFalse())
		Expect(result.Config.Params).To(BeNil())
		Expect(result.Config.RunRetention).To(BeNil())
		// The declaration is still what drives interpolation; only the result is free of it.
		Expect(result.Config.Jobs[0].Name).To(Equal("entry-staging"))
		Expect(result.CanonicalJSON).To(MatchJSON(`{
			"jobs": [{"name":"entry-staging","plan":[]}]
		}`))
		// The template itself is not mutated.
		Expect(config.Params).To(HaveLen(1))
		Expect(config.RunRetention).NotTo(BeNil())
	})
})

var _ = Describe("Run expected jobs", func() {
	It("includes the triggered entry fixed point but excludes manual branches and disconnected cycles", func() {
		// This fails if automatic run completion waits for jobs that no automatic
		// path can schedule, or omits a job reachable from an entry.
		result, err := MaterializeRunConfig(Config{
			Params: []ParamSchema{{Name: "environment", Type: ParamTypeString}},
			Jobs: JobConfigs{
				{Name: "entry", PlanSequence: []Step{{Config: &GetStep{Name: "source", Trigger: true}}}},
				{Name: "deploy-((environment))", PlanSequence: []Step{{Config: &GetStep{Passed: []string{"entry"}, Trigger: true}}}},
				{Name: "manual", PlanSequence: []Step{{Config: &GetStep{Passed: []string{"entry"}}}}},
				{Name: "cycle-a", PlanSequence: []Step{{Config: &GetStep{Passed: []string{"cycle-b"}, Trigger: true}}}},
				{Name: "cycle-b", PlanSequence: []Step{{Config: &GetStep{Passed: []string{"cycle-a"}, Trigger: true}}}},
			},
		}, RunIdentity{}, RunParams{"environment": "staging"})

		Expect(err).NotTo(HaveOccurred())
		Expect(result.EntryJobNames).To(Equal([]string{"entry"}))
		Expect(result.ExpectedJobNames).To(Equal(map[string]bool{
			"entry": true, "deploy-staging": true,
		}))
	})

	It("requires every passed dependency of a triggered job to be expected", func() {
		// This fails if a triggered get is treated as sufficient while an untriggered
		// passed constraint would leave the job permanently manual-only.
		result, err := MaterializeRunConfig(Config{Jobs: JobConfigs{
			{Name: "entry", PlanSequence: []Step{{Config: &GetStep{Name: "source", Trigger: true}}}},
			{Name: "manual", PlanSequence: []Step{{Config: &GetStep{Name: "source", Passed: []string{"entry"}}}}},
			{Name: "deploy", PlanSequence: []Step{
				{Config: &GetStep{Name: "source", Passed: []string{"entry"}, Trigger: true}},
				{Config: &GetStep{Name: "other-source", Passed: []string{"manual"}}},
			}},
		}}, RunIdentity{}, RunParams{})

		Expect(err).NotTo(HaveOccurred())
		Expect(result.ExpectedJobNames).To(Equal(map[string]bool{"entry": true}))
	})
})
