package workflow

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/configvalidate"
)

const (
	repositoryV1 snapshot.TypeRef = "repository/v1"
	repositoryV2 snapshot.TypeRef = "repository/v2"
	reviewV1     snapshot.TypeRef = "review/v1"
)

func TestTypeCheckSequenceAndAnnotation(t *testing.T) {
	agent := typedAgent("same-display-name", "normalize", []string{"before"}, map[string]atc.SnapshotInputConfig{
		"before": {Type: repositoryV1},
	}, []string{"after"}, map[string]atc.SnapshotOutputConfig{
		"after": {Type: repositoryV1},
	})
	task := typedTask("same-display-name", "review", []string{"after"}, map[string]atc.SnapshotInputConfig{
		"after": {Type: repositoryV1},
	}, []string{"finding"}, map[string]atc.SnapshotOutputConfig{
		"finding": {Type: reviewV1},
	})
	function := &FunctionConfig{
		SignatureVersion: 1,
		Inputs:           []snapshot.Port{{Name: "before", Type: repositoryV1}},
		Outputs: []FunctionOutput{{
			Port: snapshot.Port{Name: "review", Type: reviewV1},
			From: "finding",
		}},
		Plan: []atc.Step{{Config: agent}, {Config: task}},
	}

	if err := TypeCheckFunction(function); err != nil {
		t.Fatalf("TypeCheckFunction: %v", err)
	}
	if got := function.Outputs[0].From; got != "finding" {
		t.Fatalf("public source changed to %q", got)
	}
	if got := task.SnapshotOutputs["finding"].Retention; got != "" {
		t.Fatalf("pure type check annotated retention %q", got)
	}

	if err := AnnotatePublicOutputs(function, 17); err != nil {
		t.Fatalf("AnnotatePublicOutputs: %v", err)
	}
	want := atc.SnapshotOutputConfig{
		Type:                 reviewV1,
		Retention:            snapshot.RetentionClassWorkflow,
		WorkflowPort:         "review",
		WorkflowDefinitionID: 17,
		WorkflowRunID:        "((workflow_run_id))",
	}
	if got := task.SnapshotOutputs["finding"]; got != want {
		t.Fatalf("producer annotation = %+v, want %+v", got, want)
	}
	if got := agent.SnapshotOutputs["after"].Retention; got != "" {
		t.Fatalf("intermediate output retention = %q, want empty", got)
	}
}

func TestTypeCheckTaskMappingsUseEffectiveArtifactNames(t *testing.T) {
	task := typedTask("mapped", "mapped", []string{"logical-input"}, map[string]atc.SnapshotInputConfig{
		"repo": {Type: repositoryV1},
	}, []string{"logical-output"}, map[string]atc.SnapshotOutputConfig{
		"finding": {Type: reviewV1},
	})
	task.InputMapping = map[string]string{"logical-input": "repo"}
	task.OutputMapping = map[string]string{"logical-output": "finding"}
	function := &FunctionConfig{
		SignatureVersion: 1,
		Inputs:           []snapshot.Port{{Name: "repo", Type: repositoryV1}},
		Outputs: []FunctionOutput{{
			Port: snapshot.Port{Name: "public-review", Type: reviewV1},
			From: "finding",
		}},
		Plan: []atc.Step{{Config: task}},
	}

	if err := TypeCheckFunction(function); err != nil {
		t.Fatalf("TypeCheckFunction: %v", err)
	}
	if err := AnnotatePublicOutputs(function, 23); err != nil {
		t.Fatalf("AnnotatePublicOutputs: %v", err)
	}
	if got := task.SnapshotOutputs["finding"].WorkflowPort; got != "public-review" {
		t.Fatalf("workflow port = %q, want public-review", got)
	}
}

func TestTypeCheckFunctionIDsAreGlobalAndPathRich(t *testing.T) {
	tests := []struct {
		name string
		plan []atc.Step
		want []string
	}{
		{
			name: "blank typed node ID",
			plan: []atc.Step{{Config: typedAgent("review", " ", nil, nil, []string{"review"}, map[string]atc.SnapshotOutputConfig{
				"review": {Type: reviewV1},
			})}},
			want: []string{"plan[0]", "function_id", "nonblank"},
		},
		{
			name: "duplicate across wrapper and hook",
			plan: []atc.Step{{Config: &atc.OnFailureStep{
				Step: typedAgent("main", "duplicate", nil, nil, nil, nil),
				Hook: atc.Step{Config: &atc.DoStep{Steps: []atc.Step{{Config: typedTask("hook", "duplicate", nil, nil, nil, nil)}}}},
			}}},
			want: []string{"function_id", `"duplicate"`, "plan[0]", "plan[0].on_failure.do.steps[0]"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			function := &FunctionConfig{SignatureVersion: 1, Plan: test.plan}
			err := TypeCheckFunction(function)
			if err == nil {
				t.Fatal("TypeCheckFunction succeeded")
			}
			for _, part := range test.want {
				if !strings.Contains(err.Error(), part) {
					t.Fatalf("error %q does not contain %q", err, part)
				}
			}
		})
	}
}

func TestTypeCheckOptionalPresenceAndExactTypes(t *testing.T) {
	tests := []struct {
		name             string
		inputOptional    bool
		consumerOptional bool
		consumerType     snapshot.TypeRef
		outputOptional   bool
		publicOptional   bool
		want             string
	}{
		{name: "optional input to optional consumer", inputOptional: true, consumerOptional: true, consumerType: repositoryV1},
		{name: "optional input to required consumer", inputOptional: true, consumerType: repositoryV1, want: "conditional"},
		{name: "optional input wrong exact type", inputOptional: true, consumerOptional: true, consumerType: repositoryV2, want: "type mismatch"},
		{name: "optional output to optional public output", consumerType: repositoryV1, outputOptional: true, publicOptional: true},
		{name: "optional output to required public output", consumerType: repositoryV1, outputOptional: true, want: "conditional"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			producer := typedAgent("producer", "producer", []string{"source"}, map[string]atc.SnapshotInputConfig{
				"source": {Type: test.consumerType, Optional: test.consumerOptional},
			}, []string{"result"}, map[string]atc.SnapshotOutputConfig{
				"result": {Type: reviewV1, Optional: test.outputOptional},
			})
			function := &FunctionConfig{
				SignatureVersion: 1,
				Inputs:           []snapshot.Port{{Name: "source", Type: repositoryV1, Optional: test.inputOptional}},
				Outputs: []FunctionOutput{{
					Port: snapshot.Port{Name: "result", Type: reviewV1, Optional: test.publicOptional},
					From: "result",
				}},
				Plan: []atc.Step{{Config: producer}},
			}
			err := TypeCheckFunction(function)
			if test.want == "" {
				if err != nil {
					t.Fatalf("TypeCheckFunction: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestTypeCheckLoadSnapshotIsATypedProducer(t *testing.T) {
	load := func(name string, typ snapshot.TypeRef, optional bool) atc.Step {
		return atc.Step{Config: &atc.LoadSnapshotStep{
			Name: name, ID: "1", Type: typ, Optional: optional,
		}}
	}
	consumer := func(optional bool, typ snapshot.TypeRef) atc.Step {
		return atc.Step{Config: typedAgent("consume", "consume", []string{"repo"}, map[string]atc.SnapshotInputConfig{
			"repo": {Type: typ, Optional: optional},
		}, nil, nil)}
	}

	t.Run("required loads satisfy required typed consumers", func(t *testing.T) {
		function := &FunctionConfig{SignatureVersion: 1, Plan: []atc.Step{
			load("repo", repositoryV1, false),
			consumer(false, repositoryV1),
		}}
		if err := TypeCheckFunction(function); err != nil {
			t.Fatalf("TypeCheckFunction: %v", err)
		}
	})

	t.Run("optional loads are conditional", func(t *testing.T) {
		optional := &FunctionConfig{SignatureVersion: 1, Plan: []atc.Step{
			load("repo", repositoryV1, true),
			consumer(true, repositoryV1),
		}}
		if err := TypeCheckFunction(optional); err != nil {
			t.Fatalf("optional consumer: %v", err)
		}

		required := &FunctionConfig{SignatureVersion: 1, Plan: []atc.Step{
			load("repo", repositoryV1, true),
			consumer(false, repositoryV1),
		}}
		if err := TypeCheckFunction(required); err == nil || !strings.Contains(err.Error(), "conditional") {
			t.Fatalf("error = %v, want conditional binding", err)
		}
	})

	t.Run("loads preserve exact types", func(t *testing.T) {
		function := &FunctionConfig{SignatureVersion: 1, Plan: []atc.Step{
			load("repo", repositoryV1, false),
			consumer(false, repositoryV2),
		}}
		if err := TypeCheckFunction(function); err == nil || !strings.Contains(err.Error(), "type mismatch") {
			t.Fatalf("error = %v, want type mismatch", err)
		}
	})

	t.Run("parallel loads use the ordinary collision rules", func(t *testing.T) {
		parallel := &atc.InParallelStep{Config: atc.InParallelConfig{Steps: []atc.Step{
			load("repo", repositoryV1, false),
			load("repo", repositoryV1, false),
		}}}
		function := &FunctionConfig{SignatureVersion: 1, Plan: []atc.Step{{Config: parallel}}}
		if err := TypeCheckFunction(function); err == nil || !strings.Contains(err.Error(), `parallel branches both produce "repo"`) {
			t.Fatalf("error = %v, want parallel producer collision", err)
		}
	})
}

func TestTypeCheckRejectsUntypedAndAmbiguousFlow(t *testing.T) {
	tests := []struct {
		name     string
		function *FunctionConfig
		want     string
	}{
		{
			name: "typed input is consumed without a declaration",
			function: &FunctionConfig{SignatureVersion: 1, Inputs: []snapshot.Port{{Name: "repo", Type: repositoryV1}}, Plan: []atc.Step{{Config: &atc.AgentStep{
				Name: "plain", Prompt: "work", Inputs: []string{"repo"},
			}}}},
			want: "without input_types",
		},
		{
			name: "get shadows typed input",
			function: &FunctionConfig{SignatureVersion: 1, Inputs: []snapshot.Port{{Name: "repo", Type: repositoryV1}}, Plan: []atc.Step{
				{Config: &atc.GetStep{Name: "repo"}},
				{Config: typedAgent("consumer", "consumer", []string{"repo"}, map[string]atc.SnapshotInputConfig{"repo": {Type: repositoryV1}}, nil, nil)},
			}},
			want: "untyped producer",
		},
		{
			name:     "public output is untyped",
			function: &FunctionConfig{SignatureVersion: 1, Outputs: []FunctionOutput{{Port: snapshot.Port{Name: "repo", Type: repositoryV1}, From: "repo"}}, Plan: []atc.Step{{Config: &atc.GetStep{Name: "repo"}}}},
			want:     "untyped producer",
		},
		{
			name:     "public input pass through",
			function: &FunctionConfig{SignatureVersion: 1, Inputs: []snapshot.Port{{Name: "repo", Type: repositoryV1}}, Outputs: []FunctionOutput{{Port: snapshot.Port{Name: "repo", Type: repositoryV1}, From: "repo"}}, Plan: []atc.Step{{Config: &atc.LoadVarStep{Name: "ignored", File: "elsewhere"}}}},
			want:     "concrete typed producer",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := TypeCheckFunction(test.function)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestTypeCheckCompositionSemantics(t *testing.T) {
	t.Run("do is sequential", func(t *testing.T) {
		function := functionExporting("review", &atc.DoStep{Steps: []atc.Step{
			{Config: typedAgent("produce", "produce", nil, nil, []string{"repo"}, map[string]atc.SnapshotOutputConfig{"repo": {Type: repositoryV1}})},
			{Config: typedAgent("consume", "consume", []string{"repo"}, map[string]atc.SnapshotInputConfig{"repo": {Type: repositoryV1}}, []string{"review"}, map[string]atc.SnapshotOutputConfig{"review": {Type: reviewV1}})},
		}})
		if err := TypeCheckFunction(function); err != nil {
			t.Fatalf("TypeCheckFunction: %v", err)
		}
	})

	t.Run("parallel siblings cannot feed each other", func(t *testing.T) {
		parallel := &atc.InParallelStep{Config: atc.InParallelConfig{Steps: []atc.Step{
			{Config: typedAgent("produce", "produce", nil, nil, []string{"repo"}, map[string]atc.SnapshotOutputConfig{"repo": {Type: repositoryV1}})},
			{Config: typedAgent("consume", "consume", []string{"repo"}, map[string]atc.SnapshotInputConfig{"repo": {Type: repositoryV1}}, nil, nil)},
		}}}
		err := TypeCheckFunction(&FunctionConfig{SignatureVersion: 1, Plan: []atc.Step{{Config: parallel}}})
		if err == nil || !strings.Contains(err.Error(), "use before produce") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("parallel unique outputs merge", func(t *testing.T) {
		parallel := &atc.InParallelStep{Config: atc.InParallelConfig{Steps: []atc.Step{
			{Config: typedAgent("repo", "repo", nil, nil, []string{"repo"}, map[string]atc.SnapshotOutputConfig{"repo": {Type: repositoryV1}})},
			{Config: typedAgent("review", "review", nil, nil, []string{"review"}, map[string]atc.SnapshotOutputConfig{"review": {Type: reviewV1}})},
		}}}
		consumer := typedAgent("join", "join", []string{"repo", "review"}, map[string]atc.SnapshotInputConfig{
			"repo": {Type: repositoryV1}, "review": {Type: reviewV1},
		}, nil, nil)
		function := &FunctionConfig{SignatureVersion: 1, Plan: []atc.Step{{Config: parallel}, {Config: consumer}}}
		if err := TypeCheckFunction(function); err != nil {
			t.Fatalf("TypeCheckFunction: %v", err)
		}
	})

	t.Run("parallel duplicate outputs conflict", func(t *testing.T) {
		parallel := &atc.InParallelStep{Config: atc.InParallelConfig{Steps: []atc.Step{
			{Config: typedAgent("one", "one", nil, nil, []string{"same"}, map[string]atc.SnapshotOutputConfig{"same": {Type: repositoryV1}})},
			{Config: typedAgent("two", "two", nil, nil, []string{"same"}, map[string]atc.SnapshotOutputConfig{"same": {Type: repositoryV1}})},
		}}}
		err := TypeCheckFunction(&FunctionConfig{SignatureVersion: 1, Plan: []atc.Step{{Config: parallel}}})
		if err == nil || !strings.Contains(err.Error(), `parallel branches both produce "same"`) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("try discards required and optional writes", func(t *testing.T) {
		for _, optional := range []bool{false, true} {
			producer := typedAgent("produce", "produce", nil, nil, []string{"review"}, map[string]atc.SnapshotOutputConfig{"review": {Type: reviewV1, Optional: optional}})
			function := functionExporting("review", &atc.TryStep{Step: atc.Step{Config: producer}})
			err := TypeCheckFunction(function)
			if err == nil || !strings.Contains(err.Error(), "use before produce") {
				t.Fatalf("optional=%v error=%v", optional, err)
			}
		}
	})

	t.Run("try invalidates an incompatible typed shadow", func(t *testing.T) {
		shadow := typedAgent("shadow", "shadow", nil, nil, []string{"repo"}, map[string]atc.SnapshotOutputConfig{"repo": {Type: repositoryV2}})
		consumer := typedAgent("consume", "consume", []string{"repo"}, map[string]atc.SnapshotInputConfig{"repo": {Type: repositoryV1}}, nil, nil)
		function := &FunctionConfig{
			SignatureVersion: 1,
			Inputs:           []snapshot.Port{{Name: "repo", Type: repositoryV1}},
			Plan: []atc.Step{
				{Config: &atc.TryStep{Step: atc.Step{Config: shadow}}},
				{Config: consumer},
			},
		}
		err := TypeCheckFunction(function)
		if err == nil || (!strings.Contains(err.Error(), "ambiguous") && !strings.Contains(err.Error(), "untyped")) {
			t.Fatalf("error = %v, want incompatible try shadow rejection", err)
		}
	})

	t.Run("try may-writes conflict across parallel branches", func(t *testing.T) {
		parallel := &atc.InParallelStep{Config: atc.InParallelConfig{Steps: []atc.Step{
			{Config: &atc.TryStep{Step: atc.Step{Config: typedAgent("maybe", "maybe", nil, nil, []string{"same"}, map[string]atc.SnapshotOutputConfig{"same": {Type: repositoryV1}})}}},
			{Config: typedAgent("always", "always", nil, nil, []string{"same"}, map[string]atc.SnapshotOutputConfig{"same": {Type: repositoryV1}})},
		}}}
		err := TypeCheckFunction(&FunctionConfig{SignatureVersion: 1, Plan: []atc.Step{{Config: parallel}}})
		if err == nil || !strings.Contains(err.Error(), `parallel branches both produce "same"`) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("try includes non-success hook may-writes", func(t *testing.T) {
		failureHook := typedAgent("repair", "repair", nil, nil, []string{"repo"}, map[string]atc.SnapshotOutputConfig{"repo": {Type: repositoryV2}})
		wrapped := &atc.OnFailureStep{
			Step: &atc.AgentStep{Name: "main", Prompt: "work"},
			Hook: atc.Step{Config: failureHook},
		}
		consumer := typedAgent("consume", "consume", []string{"repo"}, map[string]atc.SnapshotInputConfig{"repo": {Type: repositoryV1}}, nil, nil)
		function := &FunctionConfig{
			SignatureVersion: 1,
			Inputs:           []snapshot.Port{{Name: "repo", Type: repositoryV1}},
			Plan: []atc.Step{
				{Config: &atc.TryStep{Step: atc.Step{Config: wrapped}}},
				{Config: consumer},
			},
		}
		err := TypeCheckFunction(function)
		if err == nil || !strings.Contains(err.Error(), "ambiguous") {
			t.Fatalf("error = %v, want non-success-hook may-write ambiguity", err)
		}
	})

	t.Run("retry and timeout propagate one producer", func(t *testing.T) {
		producer := typedAgent("produce", "produce", nil, nil, []string{"review"}, map[string]atc.SnapshotOutputConfig{"review": {Type: reviewV1}})
		wrapped := &atc.TimeoutStep{Duration: "1m", Step: &atc.RetryStep{Attempts: 3, Step: producer}}
		if err := TypeCheckFunction(functionExporting("review", wrapped)); err != nil {
			t.Fatalf("TypeCheckFunction: %v", err)
		}
	})

	t.Run("success hook outputs escape", func(t *testing.T) {
		main := typedAgent("produce", "produce", nil, nil, []string{"repo"}, map[string]atc.SnapshotOutputConfig{"repo": {Type: repositoryV1}})
		hook := typedAgent("review", "review", []string{"repo"}, map[string]atc.SnapshotInputConfig{"repo": {Type: repositoryV1}}, []string{"review"}, map[string]atc.SnapshotOutputConfig{"review": {Type: reviewV1}})
		if err := TypeCheckFunction(functionExporting("review", &atc.OnSuccessStep{Step: main, Hook: atc.Step{Config: hook}})); err != nil {
			t.Fatalf("TypeCheckFunction: %v", err)
		}
	})

	for _, hookKind := range []string{"failure", "error", "abort"} {
		t.Run(hookKind+" hook output does not escape", func(t *testing.T) {
			main := typedAgent("main", "main", nil, nil, []string{"main"}, map[string]atc.SnapshotOutputConfig{"main": {Type: repositoryV1}})
			hook := typedAgent("hook", "hook", nil, nil, []string{"review"}, map[string]atc.SnapshotOutputConfig{"review": {Type: reviewV1}})
			var wrapped atc.StepConfig
			switch hookKind {
			case "failure":
				wrapped = &atc.OnFailureStep{Step: main, Hook: atc.Step{Config: hook}}
			case "error":
				wrapped = &atc.OnErrorStep{Step: main, Hook: atc.Step{Config: hook}}
			case "abort":
				wrapped = &atc.OnAbortStep{Step: main, Hook: atc.Step{Config: hook}}
			}
			err := TypeCheckFunction(functionExporting("review", wrapped))
			if err == nil || !strings.Contains(err.Error(), "use before produce") {
				t.Fatalf("error = %v", err)
			}
		})
	}

	t.Run("non-success hooks are checked while main success output escapes", func(t *testing.T) {
		main := typedAgent("main", "main", nil, nil, []string{"review"}, map[string]atc.SnapshotOutputConfig{"review": {Type: reviewV1}})
		hook := typedAgent("hook", "hook", []string{"missing"}, map[string]atc.SnapshotInputConfig{"missing": {Type: repositoryV1}}, nil, nil)
		wrapped := &atc.OnFailureStep{Step: main, Hook: atc.Step{Config: hook}}
		err := TypeCheckFunction(functionExporting("review", wrapped))
		if err == nil || !strings.Contains(err.Error(), "plan[0].on_failure") || !strings.Contains(err.Error(), "use before produce") {
			t.Fatalf("error = %v", err)
		}

		validHook := typedAgent("hook", "hook", nil, nil, nil, nil)
		wrapped.Hook = atc.Step{Config: validHook}
		if err := TypeCheckFunction(functionExporting("review", wrapped)); err != nil {
			t.Fatalf("main output did not escape: %v", err)
		}
	})

	t.Run("ensure main output is conditional at hook entry", func(t *testing.T) {
		main := typedAgent("main", "main", nil, nil, []string{"repo"}, map[string]atc.SnapshotOutputConfig{"repo": {Type: repositoryV1}})
		requiredHook := typedAgent("ensure", "ensure", []string{"repo"}, map[string]atc.SnapshotInputConfig{"repo": {Type: repositoryV1}}, nil, nil)
		err := TypeCheckFunction(&FunctionConfig{SignatureVersion: 1, Plan: []atc.Step{{Config: &atc.EnsureStep{Step: main, Hook: atc.Step{Config: requiredHook}}}}})
		if err == nil || !strings.Contains(err.Error(), "conditional") {
			t.Fatalf("error = %v", err)
		}

		optionalHook := typedAgent("ensure", "ensure", []string{"repo"}, map[string]atc.SnapshotInputConfig{"repo": {Type: repositoryV1, Optional: true}}, []string{"review"}, map[string]atc.SnapshotOutputConfig{"review": {Type: reviewV1}})
		if err := TypeCheckFunction(functionExporting("review", &atc.EnsureStep{Step: main, Hook: atc.Step{Config: optionalHook}})); err != nil {
			t.Fatalf("optional ensure input: %v", err)
		}
	})

	t.Run("across is rejected until expanded plans can be frozen", func(t *testing.T) {
		inputOnly := typedAgent("inspect", "inspect", []string{"repo"}, map[string]atc.SnapshotInputConfig{"repo": {Type: repositoryV1}}, nil, nil)
		function := &FunctionConfig{
			SignatureVersion: 1,
			Inputs:           []snapshot.Port{{Name: "repo", Type: repositoryV1}},
			Plan:             []atc.Step{{Config: &atc.AcrossStep{Step: inputOnly}}},
		}
		err := TypeCheckFunction(function)
		if err == nil || !strings.Contains(err.Error(), "across") || !strings.Contains(err.Error(), "exact execution provenance") {
			t.Fatalf("error = %v, want immutable-provenance rejection", err)
		}
	})
}

func TestTypeCheckRejectsOptionalShadowAndDoesNotPartiallyAnnotate(t *testing.T) {
	first := typedAgent("first", "first", nil, nil, []string{"review"}, map[string]atc.SnapshotOutputConfig{"review": {Type: reviewV1}})
	second := typedAgent("second", "second", nil, nil, []string{"review"}, map[string]atc.SnapshotOutputConfig{"review": {Type: reviewV1, Optional: true}})
	function := &FunctionConfig{SignatureVersion: 1, Plan: []atc.Step{{Config: first}, {Config: second}}}
	if err := TypeCheckFunction(function); err == nil || !strings.Contains(err.Error(), "optional output") {
		t.Fatalf("error = %v", err)
	}

	producer := typedAgent("producer", "producer", nil, nil, []string{"review"}, map[string]atc.SnapshotOutputConfig{"review": {Type: reviewV1}})
	invalid := &FunctionConfig{
		SignatureVersion: 1,
		Outputs: []FunctionOutput{
			{Port: snapshot.Port{Name: "first", Type: reviewV1}, From: "review"},
			{Port: snapshot.Port{Name: "second", Type: reviewV1}, From: "review"},
		},
		Plan: []atc.Step{{Config: producer}},
	}
	if err := AnnotatePublicOutputs(invalid, 17); err == nil || !strings.Contains(err.Error(), "same internal output") {
		t.Fatalf("error = %v", err)
	}
	if got := producer.SnapshotOutputs["review"]; got.Retention != "" || got.WorkflowPort != "" || got.WorkflowDefinitionID != 0 || got.WorkflowRunID != "" {
		t.Fatalf("failed annotation partially mutated producer: %+v", got)
	}

	invalidProducer := typedAgent("invalid", "invalid", nil, nil, nil, map[string]atc.SnapshotOutputConfig{"review": {Type: reviewV1}})
	invalidMembership := &FunctionConfig{
		SignatureVersion: 1,
		Outputs:          []FunctionOutput{{Port: snapshot.Port{Name: "review", Type: reviewV1}, From: "review"}},
		Plan:             []atc.Step{{Config: invalidProducer}},
	}
	if err := AnnotatePublicOutputs(invalidMembership, 17); err == nil || !strings.Contains(err.Error(), "does not name a declared agent output") {
		t.Fatalf("ordinary membership error = %v", err)
	}
	if got := invalidProducer.SnapshotOutputs["review"]; got.Retention != "" {
		t.Fatalf("ordinary validation failure partially annotated producer: %+v", got)
	}
}

func TestTypeCheckOptionalPublicOutputAnnotationAndInvalidDefinitionID(t *testing.T) {
	producer := typedAgent("producer", "producer", nil, nil, []string{"review"}, map[string]atc.SnapshotOutputConfig{
		"review": {Type: reviewV1, Optional: true},
	})
	function := &FunctionConfig{
		SignatureVersion: 1,
		Outputs: []FunctionOutput{{
			Port: snapshot.Port{Name: "optional-review", Type: reviewV1, Optional: true},
			From: "review",
		}},
		Plan: []atc.Step{{Config: producer}},
	}
	if err := AnnotatePublicOutputs(function, 0); err == nil || !strings.Contains(err.Error(), "positive") {
		t.Fatalf("definition ID error = %v", err)
	}
	if got := producer.SnapshotOutputs["review"].Retention; got != "" {
		t.Fatalf("invalid definition ID partially annotated retention %q", got)
	}
	if err := AnnotatePublicOutputs(function, 29); err != nil {
		t.Fatalf("AnnotatePublicOutputs: %v", err)
	}
	got := producer.SnapshotOutputs["review"]
	if !got.Optional || got.Retention != snapshot.RetentionClassWorkflow || got.WorkflowPort != "optional-review" || got.WorkflowDefinitionID != 29 || got.WorkflowRunID != "((workflow_run_id))" {
		t.Fatalf("optional annotation = %+v", got)
	}
}

type unsupportedFlowStep struct{}

func (*unsupportedFlowStep) Visit(visitor atc.StepVisitor) error {
	return visitor.VisitLoadVar(&atc.LoadVarStep{Name: "not-really-load-var"})
}

func TestTypeCheckRejectsFutureStepKindsWithoutSemantics(t *testing.T) {
	function := &FunctionConfig{SignatureVersion: 1, Plan: []atc.Step{{Config: &unsupportedFlowStep{}}}}
	err := TypeCheckFunction(function)
	if err == nil || !strings.Contains(err.Error(), "unsupported step config") || !strings.Contains(err.Error(), "unsupportedFlowStep") {
		t.Fatalf("error = %v", err)
	}
}

func TestTypeCheckFunctionOrdinaryConcourseValidation(t *testing.T) {
	function := &FunctionConfig{
		SignatureVersion: 1,
		Plan: []atc.Step{{Config: &atc.TaskStep{
			Name:       "invalid-task",
			FunctionID: "invalid-task",
			Config: &atc.TaskConfig{
				Run: atc.TaskRunConfig{Path: "/bin/true"},
			},
		}}},
	}

	warnings, err := ValidateFunction(function)
	if err == nil || !strings.Contains(err.Error(), "missing 'platform'") {
		t.Fatalf("error = %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %+v, want none", warnings)
	}

	config := FunctionATCConfig(function)
	if !config.Template || len(config.Jobs) != 1 || config.Jobs[0].Name != "entry" || !reflect.DeepEqual(config.Jobs[0].PlanSequence, function.Plan) {
		t.Fatalf("FunctionATCConfig = %+v", config)
	}
	ordinaryWarnings, ordinaryErrors := configvalidate.Validate(config)
	if len(ordinaryErrors) == 0 || !reflect.DeepEqual(warnings, ordinaryWarnings) {
		t.Fatalf("normal validation = warnings %+v errors %+v", ordinaryWarnings, ordinaryErrors)
	}
}

func TestTypeCheckFunctionPreservesOrdinaryWarnings(t *testing.T) {
	function := &FunctionConfig{
		SignatureVersion: 1,
		Plan: []atc.Step{{Config: &atc.AgentStep{
			Name:   "_warning-name",
			Prompt: "work",
		}}},
	}
	warnings, err := ValidateFunction(function)
	if err != nil {
		t.Fatalf("ValidateFunction: %v", err)
	}
	wantWarnings, wantErrors := configvalidate.Validate(FunctionATCConfig(function))
	if len(wantErrors) != 0 {
		t.Fatalf("ordinary errors = %+v", wantErrors)
	}
	if len(warnings) == 0 || !reflect.DeepEqual(warnings, wantWarnings) {
		t.Fatalf("warnings = %+v, want exact ordinary warnings %+v", warnings, wantWarnings)
	}
}

func TestTypeCheckFunctionDelegatesOrdinaryStepAndDeclarationErrors(t *testing.T) {
	rawUnknown := json.RawMessage(`true`)
	tests := []struct {
		name     string
		function *FunctionConfig
		want     string
	}{
		{
			name: "missing task config",
			function: &FunctionConfig{SignatureVersion: 1, Plan: []atc.Step{{Config: &atc.TaskStep{
				Name: "missing",
			}}}},
			want: "must specify either `file:` or `config:`",
		},
		{
			name: "agent prompt exclusivity",
			function: &FunctionConfig{SignatureVersion: 1, Plan: []atc.Step{{Config: &atc.AgentStep{
				Name: "prompt", Prompt: "literal", PromptFile: "source.md",
			}}}},
			want: "must specify one of `prompt:` or `prompt_file:`",
		},
		{
			name: "reserved sidecar",
			function: &FunctionConfig{SignatureVersion: 1, Plan: []atc.Step{{Config: &atc.AgentStep{
				Name: "sidecar", Prompt: "work", Sidecars: []atc.SidecarSource{{Config: &atc.SidecarConfig{Name: "main", Image: "example/image@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}}},
			}}}},
			want: "reserved container name",
		},
		{
			name:     "unknown resource",
			function: &FunctionConfig{SignatureVersion: 1, Plan: []atc.Step{{Config: &atc.GetStep{Name: "missing"}}}},
			want:     "unknown resource",
		},
		{
			name:     "bad attempts",
			function: &FunctionConfig{SignatureVersion: 1, Plan: []atc.Step{{Config: &atc.RetryStep{Attempts: 0, Step: &atc.LoadVarStep{Name: "value", File: "value.json"}}}}},
			want:     "must be greater than 0",
		},
		{
			name:     "bad timeout",
			function: &FunctionConfig{SignatureVersion: 1, Plan: []atc.Step{{Config: &atc.TimeoutStep{Duration: "not-a-duration", Step: &atc.LoadVarStep{Name: "value", File: "value.json"}}}}},
			want:     "invalid duration",
		},
		{
			name:     "unknown prototype",
			function: &FunctionConfig{SignatureVersion: 1, Plan: []atc.Step{{Config: &atc.RunStep{Message: "run", Type: "missing"}}}},
			want:     "unknown prototype",
		},
		{
			name: "known prototype is rejected before execution admission",
			function: &FunctionConfig{
				SignatureVersion: 1,
				Prototypes:       atc.Prototypes{{Name: "known", Type: "mock", Source: atc.Source{"uri": "example"}}},
				Plan:             []atc.Step{{Config: &atc.RunStep{Message: "run", Type: "known"}}},
			},
			want: "prototype run steps are not executable",
		},
		{
			name: "unused resource",
			function: &FunctionConfig{
				SignatureVersion: 1,
				Resources:        atc.ResourceConfigs{{Name: "unused", Type: "mock"}},
				Plan:             []atc.Step{{Config: &atc.LoadVarStep{Name: "value", File: "value.json"}}},
			},
			want: "unused",
		},
		{
			name: "unknown embedded field",
			function: &FunctionConfig{SignatureVersion: 1, Plan: []atc.Step{{
				Config:        &atc.LoadVarStep{Name: "value", File: "value.json"},
				UnknownFields: map[string]*json.RawMessage{"future": &rawUnknown},
			}}},
			want: "unknown step field",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ValidateFunction(test.function)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestTypeCheckCompileDefinitionRunsOrdinaryValidationAndPreservesLegacy(t *testing.T) {
	invalid := Manifest{"workflow.yml": `
schema_version: 3
name: invalid
signature_version: 1
inputs: []
outputs: []
plan:
- task: invalid
  function_id: invalid
  config:
    run: {path: /bin/true}
`}
	if _, err := CompileDefinition(invalid); err == nil || !strings.Contains(err.Error(), "missing 'platform'") {
		t.Fatalf("CompileDefinition error = %v", err)
	}

	legacyManifest := Manifest{"workflow.yml": `
schema_version: 2
name: legacy
trigger:
  type: manual
workspace:
  type: git
  repo: example/repo
prompts:
  work: do it
steps:
- agent: work
  prompt: work
  outputs: [workspace]
`}
	want, err := Compile(legacyManifest)
	if err != nil {
		t.Fatalf("Compile legacy: %v", err)
	}
	got, err := CompileDefinition(legacyManifest)
	if err != nil {
		t.Fatalf("CompileDefinition legacy: %v", err)
	}
	if !reflect.DeepEqual(got.Legacy, want) {
		t.Fatalf("legacy compile changed:\n got %+v\nwant %+v", got.Legacy, want)
	}
}

func functionExporting(from string, step atc.StepConfig) *FunctionConfig {
	return &FunctionConfig{
		SignatureVersion: 1,
		Outputs: []FunctionOutput{{
			Port: snapshot.Port{Name: from, Type: reviewV1},
			From: from,
		}},
		Plan: []atc.Step{{Config: step}},
	}
}

func typedAgent(
	name string,
	functionID string,
	inputs []string,
	inputTypes map[string]atc.SnapshotInputConfig,
	outputs []string,
	outputTypes map[string]atc.SnapshotOutputConfig,
) *atc.AgentStep {
	return &atc.AgentStep{
		Name:            name,
		FunctionID:      functionID,
		Prompt:          "work",
		Inputs:          inputs,
		Outputs:         outputs,
		SnapshotInputs:  inputTypes,
		SnapshotOutputs: outputTypes,
	}
}

func typedTask(
	name string,
	functionID string,
	inputs []string,
	inputTypes map[string]atc.SnapshotInputConfig,
	outputs []string,
	outputTypes map[string]atc.SnapshotOutputConfig,
) *atc.TaskStep {
	taskInputs := make([]atc.TaskInputConfig, len(inputs))
	for index, name := range inputs {
		taskInputs[index] = atc.TaskInputConfig{Name: name}
	}
	taskOutputs := make([]atc.TaskOutputConfig, len(outputs))
	for index, name := range outputs {
		taskOutputs[index] = atc.TaskOutputConfig{Name: name}
	}
	return &atc.TaskStep{
		Name:       name,
		FunctionID: functionID,
		Config: &atc.TaskConfig{
			Platform: "linux",
			Run:      atc.TaskRunConfig{Path: "/bin/true"},
			Inputs:   taskInputs,
			Outputs:  taskOutputs,
		},
		SnapshotInputs:  inputTypes,
		SnapshotOutputs: outputTypes,
	}
}
