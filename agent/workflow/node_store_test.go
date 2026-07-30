package workflow_test

import (
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
)

func TestNodeDefinitionsStructurallyCompatible(t *testing.T) {
	defaultMode := "standard"
	previous := workflow.CompiledNodeDefinition{
		Parameters: []workflow.NodeParameter{{Name: "MODE", Default: &defaultMode}},
		Function: workflow.FunctionConfig{
			Inputs: []snapshot.Port{{Name: "source", Type: "repository/v1"}},
			Outputs: []workflow.FunctionOutput{{
				Port: snapshot.Port{Name: "result", Type: "review/v1"},
				From: "result",
			}},
		},
	}

	for name, mutate := range map[string]func(*workflow.CompiledNodeDefinition){
		"removes an output": func(candidate *workflow.CompiledNodeDefinition) {
			candidate.Function.Outputs = nil
		},
		"changes an input contract": func(candidate *workflow.CompiledNodeDefinition) {
			candidate.Function.Inputs[0].Optional = true
		},
		"adds a required input": func(candidate *workflow.CompiledNodeDefinition) {
			candidate.Function.Inputs = append(candidate.Function.Inputs, snapshot.Port{Name: "policy", Type: "policy/v1"})
		},
		"removes a parameter": func(candidate *workflow.CompiledNodeDefinition) {
			candidate.Parameters = nil
		},
		"makes a defaulted parameter required": func(candidate *workflow.CompiledNodeDefinition) {
			candidate.Parameters[0].Default = nil
		},
		"adds a required parameter": func(candidate *workflow.CompiledNodeDefinition) {
			candidate.Parameters = append(candidate.Parameters, workflow.NodeParameter{Name: "STRICT"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := previous
			candidate.Function.Inputs = append([]snapshot.Port(nil), previous.Function.Inputs...)
			candidate.Function.Outputs = append([]workflow.FunctionOutput(nil), previous.Function.Outputs...)
			candidate.Parameters = append([]workflow.NodeParameter(nil), previous.Parameters...)
			mutate(&candidate)
			if workflow.NodeDefinitionsStructurallyCompatible(previous, candidate) {
				t.Fatal("incompatible successor accepted")
			}
		})
	}

	compatible := previous
	compatible.Function.Inputs = append(
		append([]snapshot.Port(nil), previous.Function.Inputs...),
		snapshot.Port{Name: "policy", Type: "policy/v1", Optional: true},
	)
	compatible.Parameters = append(
		append([]workflow.NodeParameter(nil), previous.Parameters...),
		workflow.NodeParameter{Name: "STRICT", Default: &defaultMode},
	)
	if !workflow.NodeDefinitionsStructurallyCompatible(previous, compatible) {
		t.Fatal("compatible successor rejected")
	}
}
