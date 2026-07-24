package atc_test

import (
	"encoding/json"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"sigs.k8s.io/yaml"
)

type StepsSuite struct {
	suite.Suite
	*require.Assertions
}

type StepTest struct {
	Title string

	ConfigYAML string
	StepConfig atc.StepConfig

	UnknownFields map[string]*json.RawMessage
	Err           string
}

var factoryTests = []StepTest{
	{
		Title: "await_snapshot is a visible typed producer with quoted durable identifiers",
		ConfigYAML: `
			await_snapshot: answer
			question: question
			type: human-answer/v1
			on_timeout: default
			default_snapshot_id: "9007199254740993"
			workflow_run_id: "9223372036854775807"
			timeout: 1h
		`,
		StepConfig: &atc.TimeoutStep{
			Duration: "1h",
			Step: &atc.AwaitSnapshotStep{
				Name: "answer", Question: "question", Type: snapshot.TypeRef("human-answer/v1"),
				OnTimeout: atc.AwaitSnapshotOnTimeoutDefault, DefaultSnapshotID: "9007199254740993",
				WorkflowRunID: "9223372036854775807",
			},
		},
	},
	{
		Title: "load_snapshot step preserves quoted identifiers",
		ConfigYAML: `
			load_snapshot: subject
			id: "9007199254740993"
			type: review/v1
			workflow_run_id: "9223372036854775807"
		`,
		StepConfig: &atc.LoadSnapshotStep{
			Name:          "subject",
			ID:            "9007199254740993",
			Type:          snapshot.TypeRef("review/v1"),
			WorkflowRunID: "9223372036854775807",
		},
	},
	{
		Title: "load_snapshot composes with ordinary modifiers",
		ConfigYAML: `
			load_snapshot: subject
			id: "1"
			type: review/v1
			timeout: 1h
		`,
		StepConfig: &atc.TimeoutStep{
			Duration: "1h",
			Step: &atc.LoadSnapshotStep{
				Name: "subject", ID: "1", Type: snapshot.TypeRef("review/v1"),
			},
		},
	},
	{
		Title: "get step",
		ConfigYAML: `
			get: some-name
			resource: some-resource
			params: {some: params}
			version: {some: version}
			tags: [tag-1, tag-2]
			timeout: 1h
		`,
		StepConfig: &atc.GetStep{
			Name:     "some-name",
			Resource: "some-resource",
			Params:   atc.Params{"some": "params"},
			Version:  &atc.VersionConfig{Pinned: atc.Version{"some": "version"}},
			Tags:     []string{"tag-1", "tag-2"},
			Timeout:  "1h",
		},
	},
	{
		Title: "get step with skip_download",
		ConfigYAML: `
			get: my-image
			skip_download: true
		`,
		StepConfig: &atc.GetStep{
			Name:         "my-image",
			SkipDownload: true,
		},
	},
	{
		Title: "put step",

		ConfigYAML: `
			put: some-name
			resource: some-resource
			params: {some: params}
			tags: [tag-1, tag-2]
			inputs: all
			get_params: {some: get-params}
			timeout: 1h
		`,
		StepConfig: &atc.PutStep{
			Name:      "some-name",
			Resource:  "some-resource",
			Params:    atc.Params{"some": "params"},
			Tags:      []string{"tag-1", "tag-2"},
			Inputs:    &atc.InputsConfig{All: true},
			GetParams: atc.Params{"some": "get-params"},
			Timeout:   "1h",
		},
	},
	{
		Title: "task step",

		ConfigYAML: `
			task: some-task
			privileged: true
			hermetic: true
			config:
			  platform: linux
			  run: {path: hello}
			file: some-task-file
			vars: {some: vars}
			params: {SOME: PARAMS}
			tags: [tag-1, tag-2]
			input_mapping: {generic: specific}
			output_mapping: {specific: generic}
			image: some-image
			timeout: 1h
		`,

		StepConfig: &atc.TaskStep{
			Name:       "some-task",
			Privileged: true,
			Hermetic:   true,
			Config: &atc.TaskConfig{
				Platform: "linux",
				Run:      atc.TaskRunConfig{Path: "hello"},
			},
			ConfigPath:        "some-task-file",
			Vars:              atc.Params{"some": "vars"},
			Params:            atc.TaskEnv{"SOME": "PARAMS"},
			Tags:              []string{"tag-1", "tag-2"},
			InputMapping:      map[string]string{"generic": "specific"},
			OutputMapping:     map[string]string{"specific": "generic"},
			ImageArtifactName: "some-image",
			Timeout:           "1h",
		},
	},
	{
		Title: "task step with container limits",

		ConfigYAML: `
			task: some-task
			privileged: true
			config:
			  platform: linux
			  run: {path: hello}
			file: some-task-file
			vars: {some: vars}
			container_limits: {cpu: 10, memory: 1024}
			params: {SOME: PARAMS}
			tags: [tag-1, tag-2]
			input_mapping: {generic: specific}
			output_mapping: {specific: generic}
			image: some-image
			timeout: 1h
		`,

		StepConfig: &atc.TaskStep{
			Name:       "some-task",
			Privileged: true,
			Config: &atc.TaskConfig{
				Platform: "linux",
				Run:      atc.TaskRunConfig{Path: "hello"},
			},
			ConfigPath:        "some-task-file",
			Vars:              atc.Params{"some": "vars"},
			Params:            atc.TaskEnv{"SOME": "PARAMS"},
			Limits:            &atc.ContainerLimits{CPU: newCPULimit(10), Memory: newMemoryLimit(1024)},
			Tags:              []string{"tag-1", "tag-2"},
			InputMapping:      map[string]string{"generic": "specific"},
			OutputMapping:     map[string]string{"specific": "generic"},
			ImageArtifactName: "some-image",
			Timeout:           "1h",
		},
	},
	{
		Title: "task step with container requests",

		ConfigYAML: `
			task: some-task
			file: some-task-file
			container_requests: {cpu: 512, memory: 1073741824}
		`,

		StepConfig: &atc.TaskStep{
			Name:       "some-task",
			ConfigPath: "some-task-file",
			Requests:   &atc.ContainerLimits{CPU: newCPULimit(512), Memory: newMemoryLimit(1073741824)},
		},
	},
	{
		Title: "task step with both limits and requests",

		ConfigYAML: `
			task: some-task
			file: some-task-file
			container_limits: {cpu: 2048, memory: 4294967296}
			container_requests: {cpu: 512, memory: 1073741824}
		`,

		StepConfig: &atc.TaskStep{
			Name:       "some-task",
			ConfigPath: "some-task-file",
			Limits:     &atc.ContainerLimits{CPU: newCPULimit(2048), Memory: newMemoryLimit(4294967296)},
			Requests:   &atc.ContainerLimits{CPU: newCPULimit(512), Memory: newMemoryLimit(1073741824)},
		},
	},
	{
		Title: "task step with sidecars",

		ConfigYAML: `
			task: some-task
			file: some-task-file
			sidecars:
			- my-repo/ci/sidecars/postgres.yml
			- my-repo/ci/sidecars/redis.yml
		`,

		StepConfig: &atc.TaskStep{
			Name:       "some-task",
			ConfigPath: "some-task-file",
			Sidecars:   []atc.SidecarSource{{File: "my-repo/ci/sidecars/postgres.yml"}, {File: "my-repo/ci/sidecars/redis.yml"}},
		},
	},
	{
		Title: "task step with non-string params",

		ConfigYAML: `
			task: some-task
			file: some-task-file
			params:
			  NUMBER: 42
			  FLOAT: 1.5
			  BOOL: yes
			  OBJECT: {foo: bar}
		`,

		StepConfig: &atc.TaskStep{
			Name:       "some-task",
			ConfigPath: "some-task-file",
			Params: atc.TaskEnv{
				"NUMBER": "42",
				"FLOAT":  "1.5",
				"BOOL":   "true",
				"OBJECT": `{"foo":"bar"}`,
			},
		},
	},
	{
		Title: "run step",

		ConfigYAML: `
			run: some-message
			type: some-prototype
			privileged: true
			params:
			  foo: {bar: [123, 456]}
			  baz: qux
			tags: [tag-1, tag-2]
			container_limits: {cpu: 10, memory: 1024}
			timeout: 1h
		`,

		StepConfig: &atc.RunStep{
			Message:    "some-message",
			Type:       "some-prototype",
			Privileged: true,
			Params: atc.Params{
				"foo": map[string]any{
					"bar": []any{123.0, 456.0},
				},
				"baz": "qux",
			},
			Tags:    []string{"tag-1", "tag-2"},
			Limits:  &atc.ContainerLimits{CPU: newCPULimit(10), Memory: newMemoryLimit(1024)},
			Timeout: "1h",
		},
	},
	{
		Title: "run step with container requests",

		ConfigYAML: `
			run: some-message
			type: some-prototype
			container_limits: {cpu: 2048, memory: 4294967296}
			container_requests: {cpu: 512, memory: 1073741824}
		`,

		StepConfig: &atc.RunStep{
			Message:  "some-message",
			Type:     "some-prototype",
			Limits:   &atc.ContainerLimits{CPU: newCPULimit(2048), Memory: newMemoryLimit(4294967296)},
			Requests: &atc.ContainerLimits{CPU: newCPULimit(512), Memory: newMemoryLimit(1073741824)},
		},
	},
	{
		Title: "agent step",

		ConfigYAML: `
			agent: write-spec
			hermetic: true
			prompt: |
			  Read the ticket, explore the repo, submit a spec.
			model: claude-sonnet-4-5
			max_turns: 80
			budget_slice_usd: 2.5
			output_schema: repo/schemas/spec.json
			sidecars:
			- name: platform
			  image: ghcr.io/tdmtrader/mcp-platform:v1.0.0
			inputs: [repo]
			outputs: [workspace]
			env: {BASE_REF: main}
			timeout: 1h
		`,

		StepConfig: &atc.AgentStep{
			Name:           "write-spec",
			Hermetic:       true,
			Prompt:         "Read the ticket, explore the repo, submit a spec.\n",
			Model:          "claude-sonnet-4-5",
			MaxTurns:       80,
			BudgetSliceUSD: 2.5,
			OutputSchema:   "repo/schemas/spec.json",
			Sidecars: []atc.SidecarSource{
				{Config: &atc.SidecarConfig{Name: "platform", Image: "ghcr.io/tdmtrader/mcp-platform:v1.0.0"}},
			},
			Inputs:  []string{"repo"},
			Outputs: []string{"workspace"},
			Env:     map[string]string{"BASE_REF": "main"},
			Timeout: "1h",
		},
	},
	{
		Title: "agent step with prompt file",

		ConfigYAML: `
			agent: implement
			prompt_file: repo/prompts/implement.md
			system_prompt_file: repo/prompts/system.md
			context_files: [repo/context/conventions.md, repo/context/testing.md]
			skills: [testing]
			capabilities: [dev]
		`,

		StepConfig: &atc.AgentStep{
			Name:             "implement",
			PromptFile:       "repo/prompts/implement.md",
			SystemPromptFile: "repo/prompts/system.md",
			ContextFiles:     []string{"repo/context/conventions.md", "repo/context/testing.md"},
			Skills:           []string{"testing"},
			Capabilities:     []string{"dev"},
		},
	},
	{
		Title: "set_pipeline step",

		ConfigYAML: `
			set_pipeline: some-pipeline
			file: some-pipeline-file
			vars: {some: vars}
			var_files: [file-1, file-2]
			instance_vars: {branch: feature/foo}
		`,

		StepConfig: &atc.SetPipelineStep{
			Name:         "some-pipeline",
			File:         "some-pipeline-file",
			Vars:         atc.Params{"some": "vars"},
			VarFiles:     []string{"file-1", "file-2"},
			InstanceVars: atc.InstanceVars{"branch": "feature/foo"},
		},
	},
	{
		Title: "load_var step",

		ConfigYAML: `
			load_var: some-var
			file: some-var-file
			format: raw
			reveal: true
		`,

		StepConfig: &atc.LoadVarStep{
			Name:   "some-var",
			File:   "some-var-file",
			Format: "raw",
			Reveal: true,
		},
	},
	{
		Title: "try step",

		ConfigYAML: `
			try:
			  load_var: some-var
			  file: some-file
		`,

		StepConfig: &atc.TryStep{
			Step: atc.Step{
				Config: &atc.LoadVarStep{
					Name: "some-var",
					File: "some-file",
				},
			},
		},
	},
	{
		Title: "do step",

		ConfigYAML: `
			do:
			- load_var: some-var
			  file: some-file
			- load_var: some-other-var
			  file: some-other-file
		`,

		StepConfig: &atc.DoStep{
			Steps: []atc.Step{
				{
					Config: &atc.LoadVarStep{
						Name: "some-var",
						File: "some-file",
					},
				},
				{
					Config: &atc.LoadVarStep{
						Name: "some-other-var",
						File: "some-other-file",
					},
				},
			},
		},
	},
	{
		Title: "in_parallel step with simple list",

		ConfigYAML: `
			in_parallel:
			- load_var: some-var
			  file: some-file
			- load_var: some-other-var
			  file: some-other-file
		`,

		StepConfig: &atc.InParallelStep{
			Config: atc.InParallelConfig{
				Steps: []atc.Step{
					{
						Config: &atc.LoadVarStep{
							Name: "some-var",
							File: "some-file",
						},
					},
					{
						Config: &atc.LoadVarStep{
							Name: "some-other-var",
							File: "some-other-file",
						},
					},
				},
			},
		},
	},
	{
		Title: "in_parallel step with config",

		ConfigYAML: `
			in_parallel:
			  steps:
			  - load_var: some-var
			    file: some-file
			  - load_var: some-other-var
			    file: some-other-file
			  limit: 3
			  fail_fast: true
		`,

		StepConfig: &atc.InParallelStep{
			Config: atc.InParallelConfig{
				Steps: []atc.Step{
					{
						Config: &atc.LoadVarStep{
							Name: "some-var",
							File: "some-file",
						},
					},
					{
						Config: &atc.LoadVarStep{
							Name: "some-other-var",
							File: "some-other-file",
						},
					},
				},
				Limit:    3,
				FailFast: true,
			},
		},
	},
	{
		Title: "across step",

		ConfigYAML: `
			load_var: some-var
			file: some-file
			across:
			- var: var1
			  values: [1, 2, 3]
			  max_in_flight: 3
			- var: var2
			  values: ((something))
			  max_in_flight: all
			- var: var3
			  values: [{a: "a", b: "b"}]
			fail_fast: true
		`,

		StepConfig: &atc.AcrossStep{
			Step: &atc.LoadVarStep{
				Name: "some-var",
				File: "some-file",
			},
			Vars: []atc.AcrossVarConfig{
				{
					Var:         "var1",
					Values:      []any{float64(1), float64(2), float64(3)},
					MaxInFlight: &atc.MaxInFlightConfig{Limit: 3},
				},
				{
					Var:         "var2",
					Values:      "((something))",
					MaxInFlight: &atc.MaxInFlightConfig{All: true},
				},
				{
					Var:    "var3",
					Values: []any{map[string]any{"a": "a", "b": "b"}},
				},
			},
			FailFast: true,
		},
	},
	{
		Title: "across step with invalid field",

		ConfigYAML: `
			load_var: some-var
			file: some-file
			across:
			- var: var1
			  values: [1, 2, 3]
			  bogus_field: lol what ru gonna do about it 
		`,

		Err: `error unmarshaling JSON: while decoding JSON: malformed across step: json: unknown field "bogus_field"`,
	},
	{
		Title: "across step with invalid max_in_flight",

		ConfigYAML: `
			load_var: some-var
			file: some-file
			across:
			- var: var1
			  values: [1, 2, 3]
			  max_in_flight: some
		`,

		Err: `error unmarshaling JSON: while decoding JSON: malformed across step: invalid max_in_flight "some"`,
	},
	{
		Title: "timeout modifier",

		ConfigYAML: `
			load_var: some-var
			file: some-file
			timeout: 1h
		`,

		StepConfig: &atc.TimeoutStep{
			Step: &atc.LoadVarStep{
				Name: "some-var",
				File: "some-file",
			},
			Duration: "1h",
		},
	},
	{
		Title: "attempts modifier",

		ConfigYAML: `
			load_var: some-var
			file: some-file
			attempts: 3
		`,

		StepConfig: &atc.RetryStep{
			Step: &atc.LoadVarStep{
				Name: "some-var",
				File: "some-file",
			},
			Attempts: 3,
		},
	},
	{
		Title: "precedence of all hooks and modifiers",

		ConfigYAML: `
			load_var: some-var
			file: some-file
			timeout: 1h
			attempts: 3
			across:
			- var: version
			  values: [v1, v2, v3]
			on_success:
			  load_var: success-var
			  file: success-file
			on_failure:
			  load_var: failure-var
			  file: failure-file
			on_abort:
			  load_var: abort-var
			  file: abort-file
			on_error:
			  load_var: error-var
			  file: error-file
			ensure:
			  load_var: ensure-var
			  file: ensure-file
		`,

		StepConfig: &atc.EnsureStep{
			Step: &atc.OnErrorStep{
				Step: &atc.OnAbortStep{
					Step: &atc.OnFailureStep{
						Step: &atc.OnSuccessStep{
							Step: &atc.AcrossStep{
								Step: &atc.RetryStep{
									Step: &atc.TimeoutStep{
										Step: &atc.LoadVarStep{
											Name: "some-var",
											File: "some-file",
										},
										Duration: "1h",
									},
									Attempts: 3,
								},
								Vars: []atc.AcrossVarConfig{
									{
										Var:    "version",
										Values: []any{"v1", "v2", "v3"},
									},
								},
							},
							Hook: atc.Step{
								Config: &atc.LoadVarStep{
									Name: "success-var",
									File: "success-file",
								},
							},
						},
						Hook: atc.Step{
							Config: &atc.LoadVarStep{
								Name: "failure-var",
								File: "failure-file",
							},
						},
					},
					Hook: atc.Step{
						Config: &atc.LoadVarStep{
							Name: "abort-var",
							File: "abort-file",
						},
					},
				},
				Hook: atc.Step{
					Config: &atc.LoadVarStep{
						Name: "error-var",
						File: "error-file",
					},
				},
			},
			Hook: atc.Step{
				Config: &atc.LoadVarStep{
					Name: "ensure-var",
					File: "ensure-file",
				},
			},
		},
	},
	{
		Title: "unknown field with get step",

		ConfigYAML: `
			get: some-name
			bogus: foo
		`,

		StepConfig: &atc.GetStep{
			Name: "some-name",
		},

		UnknownFields: map[string]*json.RawMessage{"bogus": rawMessage(`"foo"`)},
	},
	{
		Title: "multiple steps defined",

		ConfigYAML: `
			put: some-name
			get: some-other-name
		`,

		StepConfig: &atc.PutStep{
			Name: "some-name",
		},

		UnknownFields: map[string]*json.RawMessage{"get": rawMessage(`"some-other-name"`)},
	},
	{
		Title: "step cannot contain only modifiers",

		ConfigYAML: `
			attempts: 2
		`,

		StepConfig: &atc.RetryStep{
			Attempts: 2,
		},

		Err: "no core step type declared (e.g. get, put, task, etc.)",
	},
}

func (test StepTest) Run(s *StepsSuite) {
	cleanIndents := strings.ReplaceAll(test.ConfigYAML, "\t", "")

	var step atc.Step
	actualErr := yaml.Unmarshal([]byte(cleanIndents), &step)
	if test.Err != "" {
		s.Contains(actualErr.Error(), test.Err)
		return
	} else {
		s.NoError(actualErr)
	}

	s.Equal(test.StepConfig, step.Config)
	s.Equal(test.UnknownFields, step.UnknownFields)

	remarshalled, err := json.Marshal(step)
	s.NoError(err)

	var reStep atc.Step
	err = yaml.Unmarshal(remarshalled, &reStep)
	s.NoError(err)

	s.Equal(test.StepConfig, reStep.Config)
}

func (s *StepsSuite) TestFactory() {
	for _, test := range factoryTests {
		s.Run(test.Title, func() {
			test.Run(s)
		})
	}
}

func (s *StepsSuite) TestRejectsRetiredHarvestAsUnknownCoreStep() {
	var step atc.Step
	err := yaml.Unmarshal([]byte(`
harvest: push-branch
workspace: workspace
repo: example/repo
`), &step)
	s.ErrorIs(err, atc.ErrNoStepConfigured)
}

func (s *StepsSuite) TestSnapshotPortConfigs() {
	s.Run("strict configs and canonical output form", func() {
		var input atc.SnapshotInputConfig
		err := json.Unmarshal([]byte(`{"type":"repository/v1","optional":true}`), &input)
		s.NoError(err)
		s.Equal(atc.SnapshotInputConfig{Type: snapshot.TypeRef("repository/v1"), Optional: true}, input)

		var scalar atc.SnapshotOutputConfig
		err = json.Unmarshal([]byte(`"review/v1"`), &scalar)
		s.NoError(err)
		s.Equal(atc.SnapshotOutputConfig{Type: snapshot.TypeRef("review/v1")}, scalar)
		encoded, err := json.Marshal(scalar)
		s.NoError(err)
		s.JSONEq(`{"type":"review/v1"}`, string(encoded))

		var long atc.SnapshotOutputConfig
		err = json.Unmarshal([]byte(`{
			"type":"repository-change/v1",
			"optional":true,
			"retention":"workflow",
			"workflow_port":"change",
			"workflow_definition_id":17,
			"workflow_run_id":"9007199254740993",
			"source_metadata":{"adapter":"resource-version","operation_key":"capture-key"}
		}`), &long)
		s.NoError(err)
		s.Equal("9007199254740993", long.WorkflowRunID)
		s.JSONEq(`{"adapter":"resource-version","operation_key":"capture-key"}`, string(long.SourceMetadata))
		encoded, err = json.Marshal(long)
		s.NoError(err)
		s.JSONEq(`{
			"type":"repository-change/v1",
			"optional":true,
			"retention":"workflow",
			"workflow_port":"change",
			"workflow_definition_id":17,
			"workflow_run_id":"9007199254740993",
			"source_metadata":{"adapter":"resource-version","operation_key":"capture-key"}
		}`, string(encoded))
	})

	s.Run("rejects non-object inputs and malformed nested values", func() {
		for _, test := range []struct {
			name    string
			payload string
			target  any
		}{
			{name: "input shorthand", payload: `"repository/v1"`, target: &atc.SnapshotInputConfig{}},
			{name: "input unknown field", payload: `{"type":"repository/v1","typo":true}`, target: &atc.SnapshotInputConfig{}},
			{name: "input trailing value", payload: `{"type":"repository/v1"} {}`, target: &atc.SnapshotInputConfig{}},
			{name: "output unknown field", payload: `{"type":"review/v1","typo":true}`, target: &atc.SnapshotOutputConfig{}},
			{name: "output trailing value", payload: `{"type":"review/v1"} {}`, target: &atc.SnapshotOutputConfig{}},
			{name: "output source metadata scalar", payload: `{"type":"review/v1","source_metadata":"spoofed"}`, target: &atc.SnapshotOutputConfig{}},
			{name: "output source metadata array", payload: `{"type":"review/v1","source_metadata":[]}`, target: &atc.SnapshotOutputConfig{}},
			{name: "output source metadata null", payload: `{"type":"review/v1","source_metadata":null}`, target: &atc.SnapshotOutputConfig{}},
			{name: "numeric workflow run id", payload: `{"type":"review/v1","retention":"workflow","workflow_port":"review","workflow_definition_id":1,"workflow_run_id":9007199254740993}`, target: &atc.SnapshotOutputConfig{}},
		} {
			s.Run(test.name, func() {
				s.Error(json.Unmarshal([]byte(test.payload), test.target))
			})
		}

		_, err := json.Marshal(atc.SnapshotInputConfig{Type: snapshot.TypeRef("not-a-versioned-type")})
		s.Error(err)
		_, err = json.Marshal(atc.SnapshotOutputConfig{Type: snapshot.TypeRef("not-a-versioned-type")})
		s.Error(err)
		_, err = json.Marshal(atc.SnapshotOutputConfig{
			Type:           snapshot.TypeRef("review/v1"),
			SourceMetadata: json.RawMessage(`{"value":"` + strings.Repeat("x", 16*1024) + `"}`),
		})
		s.Error(err)
	})

	s.Run("task and agent declarations round trip without changing legacy fields", func() {
		payload := []byte(`
jobs:
- name: typed
  plan:
  - task: transform
    function_id: transform-repository
    config:
      platform: linux
      run: {path: /bin/true}
      inputs: [{name: source}]
      outputs: [{name: result}]
    input_mapping: {source: repository}
    output_mapping: {result: change}
    input_types:
      repository: {type: repository/v1, optional: true}
    output_types:
      change:
        type: repository-change/v1
        retention: workflow
        workflow_port: change
        workflow_definition_id: 17
        workflow_run_id: "9007199254740993"
  - agent: review
    function_id: review-change
    prompt: review it
    capabilities: [dev, jira]
    inputs: [change]
    outputs: [review]
    input_types:
      change: {type: repository-change/v1}
    output_types:
      review: review/v1
    output_schema: repo/schemas/review.json
`)
		var config atc.Config
		s.NoError(atc.UnmarshalConfig(payload, &config))
		s.Len(config.Jobs[0].PlanSequence, 2)

		task := config.Jobs[0].PlanSequence[0].Config.(*atc.TaskStep)
		s.Equal("transform-repository", task.FunctionID)
		s.Equal([]atc.TaskInputConfig{{Name: "source"}}, task.Config.Inputs)
		s.Equal([]atc.TaskOutputConfig{{Name: "result"}}, task.Config.Outputs)
		s.Equal(map[string]string{"source": "repository"}, task.InputMapping)
		s.Equal(map[string]string{"result": "change"}, task.OutputMapping)
		s.Equal(snapshot.TypeRef("repository/v1"), task.SnapshotInputs["repository"].Type)
		s.Equal("9007199254740993", task.SnapshotOutputs["change"].WorkflowRunID)

		agent := config.Jobs[0].PlanSequence[1].Config.(*atc.AgentStep)
		s.Equal("review-change", agent.FunctionID)
		s.Equal([]string{"dev", "jira"}, agent.Capabilities)
		s.Equal([]string{"change"}, agent.Inputs)
		s.Equal([]string{"review"}, agent.Outputs)
		s.Equal("repo/schemas/review.json", agent.OutputSchema)
		s.Equal(snapshot.TypeRef("review/v1"), agent.SnapshotOutputs["review"].Type)

		encoded, err := json.Marshal(config)
		s.NoError(err)
		var roundTripped atc.Config
		s.NoError(atc.UnmarshalConfig(encoded, &roundTripped))
		s.Equal(config, roundTripped)

		var raw map[string]any
		s.NoError(json.Unmarshal(encoded, &raw))
		jobs := raw["jobs"].([]any)
		plan := jobs[0].(map[string]any)["plan"].([]any)
		agentWire := plan[1].(map[string]any)
		s.Equal("review-change", agentWire["function_id"])
		s.Equal("repo/schemas/review.json", agentWire["output_schema"])
		outputTypes := agentWire["output_types"].(map[string]any)
		s.Equal(map[string]any{"type": "review/v1"}, outputTypes["review"])
	})
}

func (s *StepsSuite) TestLoadSnapshotIdentifiersAreQuotedCanonicalStrings() {
	for _, test := range []struct {
		name    string
		payload string
	}{
		{"numeric id", `{"load_snapshot":"subject","id":9007199254740993,"type":"review/v1"}`},
		{"empty id", `{"load_snapshot":"subject","id":"","type":"review/v1"}`},
		{"whitespace id", `{"load_snapshot":"subject","id":" 1","type":"review/v1"}`},
		{"leading zero", `{"load_snapshot":"subject","id":"01","type":"review/v1"}`},
		{"signed", `{"load_snapshot":"subject","id":"+1","type":"review/v1"}`},
		{"overflow", `{"load_snapshot":"subject","id":"9223372036854775808","type":"review/v1"}`},
		{"required zero", `{"load_snapshot":"subject","id":"0","type":"review/v1"}`},
		{"workflow zero", `{"load_snapshot":"subject","id":"1","type":"review/v1","workflow_run_id":"0"}`},
		{"numeric workflow id", `{"load_snapshot":"subject","id":"1","type":"review/v1","workflow_run_id":1}`},
		{"invalid type", `{"load_snapshot":"subject","id":"1","type":"review"}`},
		{"unknown field", `{"load_snapshot":"subject","id":"1","type":"review/v1","typo":true}`},
		{"embedded parameter", `{"load_snapshot":"subject","id":"prefix-((subject_id))","type":"review/v1"}`},
	} {
		s.Run(test.name, func() {
			var step atc.Step
			s.Error(json.Unmarshal([]byte(test.payload), &step))
		})
	}

	var optional atc.Step
	s.NoError(json.Unmarshal([]byte(`{"load_snapshot":"subject","id":"0","type":"review/v1","optional":true}`), &optional))
	s.Equal("0", optional.Config.(*atc.LoadSnapshotStep).ID)

	var direct atc.LoadSnapshotStep
	s.Error(direct.UnmarshalJSON([]byte(`{"load_snapshot":"subject","id":"1","type":"review/v1"} {}`)))
}

func (s *StepsSuite) TestAwaitSnapshotWireContractIsStrictAndPinned() {
	invalid := []struct {
		name    string
		payload string
	}{
		{"missing question", `{"await_snapshot":"answer","type":"human-answer/v1","on_timeout":"fail"}`},
		{"question and merge approval", `{"await_snapshot":"answer","question":"question","merge_approval":{"input":"change","publisher":"git-publisher/v1","destination":"git.example/repo","parameters":{"target_branch":"main","expected_base_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"approval_policy_version":"engineering/v1","prompt":"Merge?"},"type":"human-answer/v1","on_timeout":"fail"}`},
		{"merge approval default", `{"await_snapshot":"answer","merge_approval":{"input":"change","publisher":"git-publisher/v1","destination":"git.example/repo","parameters":{"target_branch":"main","expected_base_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"approval_policy_version":"engineering/v1","prompt":"Merge?"},"type":"human-answer/v1","on_timeout":"default","default_snapshot_id":"1"}`},
		{"wrong output type", `{"await_snapshot":"answer","question":"question","type":"review/v1","on_timeout":"fail"}`},
		{"unknown timeout policy", `{"await_snapshot":"answer","question":"question","type":"human-answer/v1","on_timeout":"continue"}`},
		{"default lacks snapshot", `{"await_snapshot":"answer","question":"question","type":"human-answer/v1","on_timeout":"default"}`},
		{"fail carries default", `{"await_snapshot":"answer","question":"question","type":"human-answer/v1","on_timeout":"fail","default_snapshot_id":"1"}`},
		{"numeric default", `{"await_snapshot":"answer","question":"question","type":"human-answer/v1","on_timeout":"default","default_snapshot_id":9007199254740993}`},
		{"zero default", `{"await_snapshot":"answer","question":"question","type":"human-answer/v1","on_timeout":"default","default_snapshot_id":"0"}`},
		{"numeric run", `{"await_snapshot":"answer","question":"question","type":"human-answer/v1","on_timeout":"fail","workflow_run_id":1}`},
		{"wrong parameter", `{"await_snapshot":"answer","question":"question","type":"human-answer/v1","on_timeout":"fail","workflow_run_id":"((other))"}`},
		{"unknown field", `{"await_snapshot":"answer","question":"question","type":"human-answer/v1","on_timeout":"fail","typo":true}`},
	}
	for _, test := range invalid {
		s.Run(test.name, func() {
			var step atc.Step
			s.Error(json.Unmarshal([]byte(test.payload), &step))
		})
	}

	var direct atc.AwaitSnapshotStep
	s.Error(direct.UnmarshalJSON([]byte(`{"await_snapshot":"answer","question":"question","type":"human-answer/v1","on_timeout":"fail"} {}`)))

	var merge atc.Step
	s.NoError(json.Unmarshal([]byte(`{
		"await_snapshot":"approval",
		"merge_approval":{
			"input":"change",
			"publisher":"git-publisher/v1",
			"destination":"git.example/repo",
			"parameters":{"target_branch":"main","expected_base_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			"approval_policy_version":"engineering/v1",
			"prompt":"Merge this exact change?"
		},
		"type":"human-answer/v1",
		"on_timeout":"fail"
	}`), &merge))
	s.Equal("change", merge.Config.(*atc.AwaitSnapshotStep).MergeApproval.Input)
}

func rawMessage(s string) *json.RawMessage {
	raw := json.RawMessage(s)
	return &raw
}

func newCPULimit(cpuLimit uint64) *atc.CPULimit {
	limit := atc.CPULimit(cpuLimit)
	return &limit
}

func newMemoryLimit(memoryLimit uint64) *atc.MemoryLimit {
	limit := atc.MemoryLimit(memoryLimit)
	return &limit
}
