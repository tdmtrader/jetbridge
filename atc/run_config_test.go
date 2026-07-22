package atc_test

import (
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Template pipeline config", func() {
	It("round-trips template, params and run_retention through UnmarshalConfig", func() {
		payload := []byte(`
template: true
params:
- name: commit
  type: string
  format: positive-decimal-int64
  required: true
- name: suite
  type: enum
  values: [unit, integration]
  default: unit
run_retention:
  keep_last: 5
  ttl_days: 7
jobs:
- name: entry
  plan:
  - task: t
    file: task.yml
`)
		var config atc.Config
		err := atc.UnmarshalConfig(payload, &config)
		Expect(err).ToNot(HaveOccurred())
		Expect(config.Template).To(BeTrue())
		Expect(config.Params).To(HaveLen(2))
		Expect(config.Params[0].Name).To(Equal("commit"))
		Expect(config.Params[0].Type).To(Equal("string"))
		Expect(config.Params[0].Format).To(Equal(atc.ParamFormatPositiveDecimalInt64))
		Expect(config.Params[0].Required).To(BeTrue())
		Expect(config.Params[1].Values).To(Equal([]string{"unit", "integration"}))
		Expect(config.Params[1].Default).To(Equal("unit"))
		Expect(config.RunRetention.KeepLast).To(Equal(5))
		Expect(config.RunRetention.TTLDays).To(Equal(7))
	})
})

var _ = Describe("MaterializeRunConfig", func() {
	template := atc.Config{
		Template: true,
		Params:   []atc.ParamSchema{{Name: "ref", Type: "string"}},
		Resources: atc.ResourceConfigs{
			{Name: "repo", Type: "git", Source: atc.Source{"branch": "((ref))", "uri": "((repo_uri))"}},
		},
		Jobs: atc.JobConfigs{
			{
				Name: "entry",
				PlanSequence: []atc.Step{
					{Config: &atc.GetStep{Name: "repo", Trigger: true}},
				},
			},
			{
				Name: "downstream",
				PlanSequence: []atc.Step{
					{Config: &atc.GetStep{Name: "repo", Passed: []string{"entry"}, Trigger: true}},
				},
			},
		},
	}

	It("resolves params and the run number, strips external triggers, keeps passed-chain triggers and unknown vars", func() {
		out, err := atc.MaterializeRunConfig(template, 42, 9001, map[string]any{"ref": "abc123"})
		Expect(err).ToNot(HaveOccurred())

		Expect(out.Template).To(BeTrue())
		Expect(out.Resources[0].Source["branch"]).To(Equal("abc123"))
		// unresolved vars are left for runtime var sources
		Expect(out.Resources[0].Source["uri"]).To(Equal("((repo_uri))"))

		// external-version triggering is suppressed: the entry get (no
		// passed:) loses trigger: true...
		Expect(out.Jobs[0].Inputs()[0].Trigger).To(BeFalse(), "entry get must not trigger on resource versions")
		// ...but passed: chains keep flowing — the scheduler only creates
		// builds for trigger: true inputs, so downstream gets MUST keep it
		Expect(out.Jobs[1].Inputs()[0].Trigger).To(BeTrue(), "downstream passed: get must keep trigger for chain flow")
	})

	It("makes ((run)) available and gives it precedence over params", func() {
		withRun := template
		withRun.Resources = atc.ResourceConfigs{
			{Name: "repo", Type: "git", Source: atc.Source{"tag": "run-((run))"}},
		}
		out, err := atc.MaterializeRunConfig(withRun, 7, 9001, map[string]any{"run": "hijack"})
		Expect(err).ToNot(HaveOccurred())
		Expect(out.Resources[0].Source["tag"]).To(Equal("run-7"))
	})

	// F30 (2026-07-09): ((run_id)) carries the globally-unique
	// pipeline_runs.id — the value §8.1 AGENT_PIPELINE_RUN_ID is defined as.
	// ((run)) is only the per-template run NUMBER and collides across
	// templates; renderers keying cross-template state must use ((run_id)).
	It("makes ((run_id)) available as the global pipeline_runs.id and gives it precedence over params", func() {
		withRunID := template
		withRunID.Resources = atc.ResourceConfigs{
			{Name: "repo", Type: "git", Source: atc.Source{"tag": "run-((run))-id-((run_id))"}},
		}
		out, err := atc.MaterializeRunConfig(withRunID, 7, 9001, map[string]any{"run_id": "hijack"})
		Expect(err).ToNot(HaveOccurred())
		Expect(out.Resources[0].Source["tag"]).To(Equal("run-7-id-9001"))
	})

	It("preserves an interpolated quoted workflow run ID above 2^53 exactly", func() {
		withWorkflowRun := template
		withWorkflowRun.Jobs = atc.JobConfigs{{
			Name: "entry",
			PlanSequence: []atc.Step{{Config: &atc.AgentStep{
				Name:    "review",
				Prompt:  "review it",
				Outputs: []string{"review"},
				SnapshotOutputs: map[string]atc.SnapshotOutputConfig{
					"review": {
						Type:                 snapshot.TypeRef("review/v1"),
						Retention:            snapshot.RetentionClassWorkflow,
						WorkflowPort:         "review",
						WorkflowDefinitionID: 17,
						WorkflowRunID:        "((workflow_run_id))",
					},
				},
			}}},
		}}

		out, err := atc.MaterializeRunConfig(withWorkflowRun, 7, 9001, map[string]any{
			"workflow_run_id": "9007199254740993",
		})
		Expect(err).ToNot(HaveOccurred())
		agent := out.Jobs[0].PlanSequence[0].Config.(*atc.AgentStep)
		Expect(agent.SnapshotOutputs["review"].WorkflowRunID).To(Equal("9007199254740993"))
	})

	It("materializes quoted load_snapshot identifiers without numeric coercion", func() {
		withSnapshot := template
		withSnapshot.Template = true
		withSnapshot.Params = []atc.ParamSchema{
			{Name: "subject_id", Type: "string", Format: atc.ParamFormatZeroOrPositiveDecimalInt64, Default: "0"},
			{Name: "workflow_run_id", Type: "string", Format: atc.ParamFormatPositiveDecimalInt64, Required: true},
		}
		withSnapshot.Jobs = atc.JobConfigs{{
			Name: "entry",
			PlanSequence: []atc.Step{{Config: &atc.LoadSnapshotStep{
				Name: "subject", ID: "((subject_id))", Type: snapshot.TypeRef("review/v1"),
				Optional: true, WorkflowRunID: "((workflow_run_id))",
			}}},
		}}

		out, err := atc.MaterializeRunConfig(withSnapshot, 7, 9001, map[string]any{
			"subject_id":      "9007199254740993",
			"workflow_run_id": "9223372036854775807",
		})
		Expect(err).ToNot(HaveOccurred())
		load := out.Jobs[0].PlanSequence[0].Config.(*atc.LoadSnapshotStep)
		Expect(load.ID).To(Equal("9007199254740993"))
		Expect(load.WorkflowRunID).To(Equal("9223372036854775807"))

		validated, err := atc.ValidateRunParams(withSnapshot.Params, map[string]any{
			"workflow_run_id": "9223372036854775807",
		})
		Expect(err).ToNot(HaveOccurred())
		out, err = atc.MaterializeRunConfig(withSnapshot, 8, 9002, validated)
		Expect(err).ToNot(HaveOccurred())
		load = out.Jobs[0].PlanSequence[0].Config.(*atc.LoadSnapshotStep)
		Expect(load.ID).To(Equal("0"))

		_, err = atc.ValidateRunParams(withSnapshot.Params, map[string]any{
			"subject_id":      float64(1),
			"workflow_run_id": "1",
		})
		Expect(err).To(MatchError(ContainSubstring("expected string")))
	})
})

var _ = Describe("Config.EntryJobs", func() {
	It("returns jobs with no passed constraints", func() {
		config := atc.Config{
			Jobs: atc.JobConfigs{
				{Name: "no-inputs", PlanSequence: []atc.Step{
					{Config: &atc.TaskStep{Name: "t", ConfigPath: "task.yml"}},
				}},
				{Name: "entry-get", PlanSequence: []atc.Step{
					{Config: &atc.GetStep{Name: "repo"}},
				}},
				{Name: "downstream", PlanSequence: []atc.Step{
					{Config: &atc.GetStep{Name: "repo", Passed: []string{"entry-get"}}},
				}},
			},
		}
		Expect(config.EntryJobs()).To(Equal([]string{"no-inputs", "entry-get"}))
	})
})
