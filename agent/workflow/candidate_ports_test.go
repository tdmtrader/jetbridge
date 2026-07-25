package workflow

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
)

const selectionV1 snapshot.TypeRef = "selection/v1"

// judgeFunction is a selecting node exposed to two candidate ports and one
// non-candidate context port, which is exactly the shape the platform's target
// model describes for a judge.
func judgeFunction(inputTypes map[string]atc.SnapshotInputConfig, outputTypes map[string]atc.SnapshotOutputConfig) *FunctionConfig {
	judge := typedAgent(
		"judge", "judge",
		[]string{"base", "left", "right"}, inputTypes,
		[]string{"selection"}, outputTypes,
	)
	return &FunctionConfig{
		SignatureVersion: 1,
		Inputs: []snapshot.Port{
			{Name: "base", Type: repositoryV1},
			{Name: "left", Type: repositoryChangeV1},
			{Name: "right", Type: repositoryChangeV1},
		},
		Outputs: []FunctionOutput{{
			Port: snapshot.Port{Name: "selection", Type: selectionV1},
			From: "selection",
		}},
		Plan: []atc.Step{{Config: judge}},
	}
}

func TestTypeCheckAcceptsDeclaredCandidatePortsBesideContextPorts(t *testing.T) {
	function := judgeFunction(map[string]atc.SnapshotInputConfig{
		"base":  {Type: repositoryV1},
		"left":  {Type: repositoryChangeV1, Candidate: true},
		"right": {Type: repositoryChangeV1, Candidate: true},
	}, map[string]atc.SnapshotOutputConfig{
		"selection": {Type: selectionV1},
	})

	if err := TypeCheckFunction(function); err != nil {
		t.Fatalf("TypeCheckFunction: %v", err)
	}
}

func TestParseCarriesCandidatePortDeclarationsFromSource(t *testing.T) {
	source := `schema_version: 3
name: judge-candidates
signature_version: 1
inputs:
  - name: base
    type: repository/v1
  - name: left
    type: repository-change/v1
  - name: right
    type: repository-change/v1
outputs:
  - name: selection
    type: selection/v1
    from: selection
plan:
  - agent: judge
    function_id: judge
    prompt: Choose the better change relative to the base repository.
    inputs: [base, left, right]
    outputs: [selection]
    input_types:
      base: {type: repository/v1}
      left: {type: repository-change/v1, candidate: true}
      right: {type: repository-change/v1, candidate: true}
    output_types:
      selection: selection/v1
`
	definition, err := ParseCompiled([]byte(source))
	if err != nil {
		t.Fatalf("ParseCompiled: %v", err)
	}
	judge, ok := definition.Function.Plan[0].Config.(*atc.AgentStep)
	if !ok {
		t.Fatalf("plan[0] is %T, want *atc.AgentStep", definition.Function.Plan[0].Config)
	}
	for name, want := range map[string]bool{"left": true, "right": true, "base": false} {
		if got := judge.SnapshotInputs[name].Candidate; got != want {
			t.Fatalf("input_types[%q].candidate = %t, want %t", name, got, want)
		}
	}
	if err := TypeCheckFunction(definition.Function); err != nil {
		t.Fatalf("TypeCheckFunction: %v", err)
	}
}

func TestTypeCheckRejectsIncoherentCandidatePortDeclarations(t *testing.T) {
	for name, testCase := range map[string]struct {
		inputTypes  map[string]atc.SnapshotInputConfig
		outputTypes map[string]atc.SnapshotOutputConfig
		wantError   string
	}{
		"selection output without any candidate port": {
			inputTypes: map[string]atc.SnapshotInputConfig{
				"base":  {Type: repositoryV1},
				"left":  {Type: repositoryChangeV1},
				"right": {Type: repositoryChangeV1},
			},
			outputTypes: map[string]atc.SnapshotOutputConfig{"selection": {Type: selectionV1}},
			wantError:   "at least one candidate input port",
		},
		"candidate ports without a selection output": {
			inputTypes: map[string]atc.SnapshotInputConfig{
				"base":  {Type: repositoryV1},
				"left":  {Type: repositoryChangeV1, Candidate: true},
				"right": {Type: repositoryChangeV1, Candidate: true},
			},
			outputTypes: map[string]atc.SnapshotOutputConfig{"selection": {Type: reviewV1}},
			wantError:   "selection/v1 output",
		},
		"candidate ports of differing types": {
			inputTypes: map[string]atc.SnapshotInputConfig{
				"base":  {Type: repositoryV1},
				"left":  {Type: repositoryChangeV1, Candidate: true},
				"right": {Type: reviewV1, Candidate: true},
			},
			outputTypes: map[string]atc.SnapshotOutputConfig{"selection": {Type: selectionV1}},
			wantError:   "one common snapshot type",
		},
		// An optional candidate makes the declared candidate set run-dependent:
		// when the port is unbound the executor drops it, so the set the selection
		// validator treats as authority is no longer the set that was declared.
		// Candidacy is a static port declaration, so the two must not be combined.
		"a candidate port that is also optional": {
			inputTypes: map[string]atc.SnapshotInputConfig{
				"base":  {Type: repositoryV1},
				"left":  {Type: repositoryChangeV1, Candidate: true},
				"right": {Type: repositoryChangeV1, Candidate: true, Optional: true},
			},
			outputTypes: map[string]atc.SnapshotOutputConfig{"selection": {Type: selectionV1}},
			wantError:   "candidate input port cannot be optional",
		},
	} {
		t.Run(name, func(t *testing.T) {
			function := judgeFunction(testCase.inputTypes, testCase.outputTypes)
			// keep the public output type consistent with the node's output
			function.Outputs[0].Port.Type = testCase.outputTypes["selection"].Type
			err := TypeCheckFunction(function)
			if err == nil || !strings.Contains(err.Error(), testCase.wantError) {
				t.Fatalf("TypeCheckFunction error = %v, want it to contain %q", err, testCase.wantError)
			}
		})
	}
}
