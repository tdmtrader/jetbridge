package commands

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/experiment"
	"github.com/concourse/concourse/agent/pagination"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/fly/rc"
	"github.com/concourse/concourse/fly/rc/rcfakes"
	"github.com/concourse/concourse/go-concourse/concourse/concoursefakes"
)

const largeAgentExperimentID = "9007199254740993"

func TestParseAgentExperimentIDsPreservesExactInt64Identity(t *testing.T) {
	for _, raw := range []string{"1", largeAgentExperimentID, "9223372036854775807"} {
		id, err := parseAgentExperimentID(raw)
		if err != nil || id.String() != raw {
			t.Fatalf("parseAgentExperimentID(%q) = %q, %v", raw, id.String(), err)
		}
		cellID, err := parseAgentExperimentCellID(raw)
		if err != nil || cellID.String() != raw {
			t.Fatalf("parseAgentExperimentCellID(%q) = %q, %v", raw, cellID.String(), err)
		}
	}
	for _, raw := range []string{"", "0", "-1", "+1", " 1", "1 ", "01", "1.0", "9223372036854775808"} {
		if _, err := parseAgentExperimentID(raw); err == nil {
			t.Fatalf("parseAgentExperimentID(%q) succeeded", raw)
		}
		if _, err := parseAgentExperimentCellID(raw); err == nil {
			t.Fatalf("parseAgentExperimentCellID(%q) succeeded", raw)
		}
	}
}

func TestParseAgentExperimentVariantReference(t *testing.T) {
	tests := []struct {
		raw  string
		want agentExperimentVariantReference
	}{
		{raw: "control=review@3", want: agentExperimentVariantReference{Label: "control", WorkflowName: "review", Version: 3}},
		{raw: "candidate=code-review@42#review", want: agentExperimentVariantReference{Label: "candidate", WorkflowName: "code-review", Version: 42, FunctionID: "review"}},
	}
	for _, test := range tests {
		got, err := parseAgentExperimentVariantReference(test.raw)
		if err != nil || got != test.want {
			t.Fatalf("parseAgentExperimentVariantReference(%q) = %#v, %v; want %#v", test.raw, got, err, test.want)
		}
	}
	for _, raw := range []string{"", "review@3", "=review@3", "x=@3", "x=review", "x=review@0", "x=review@01", "x=review@3#", "x=review@3#a#b"} {
		if _, err := parseAgentExperimentVariantReference(raw); err == nil {
			t.Fatalf("parseAgentExperimentVariantReference(%q) succeeded", raw)
		}
	}
}

func TestParseAgentExperimentVariantReferenceAcceptsANodeTarget(t *testing.T) {
	reference, err := parseAgentExperimentVariantReference("early=node:small-fix@4")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if reference.Label != "early" || reference.WorkflowName != "small-fix" ||
		reference.Version != 4 || !reference.Node {
		t.Fatalf("reference = %+v", reference)
	}
	if reference.FunctionID != "" {
		t.Fatalf("a node variant selects no function, got %q", reference.FunctionID)
	}
}

func TestParseAgentExperimentVariantReferenceRejectsANodeFunction(t *testing.T) {
	// A node is one leaf; naming a function inside it is meaningless.
	if _, err := parseAgentExperimentVariantReference("early=node:small-fix@4#review"); err == nil {
		t.Fatal("a node variant must not accept a function ID")
	}
}

func TestParseAgentExperimentFixtureFlags(t *testing.T) {
	inputs, err := parseAgentExperimentInputs([]string{
		"repo=9007199254740993",
		"ticket=9223372036854775807",
	})
	if err != nil {
		t.Fatal(err)
	}
	if inputs["repo"].String() != largeAgentExperimentID || inputs["ticket"].String() != "9223372036854775807" {
		t.Fatalf("inputs = %#v", inputs)
	}

	assertions, err := parseAgentExperimentAssertions([]string{
		"defects=lt:1.5",
		"quality=lte:2",
		"latency=gt:0",
		"coverage=gte:0.8",
		"score=between:-1.25:4.5",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []experiment.Assertion{
		{Metric: "defects", Comparator: experiment.ComparatorLT, Thresholds: []float64{1.5}},
		{Metric: "quality", Comparator: experiment.ComparatorLTE, Thresholds: []float64{2}},
		{Metric: "latency", Comparator: experiment.ComparatorGT, Thresholds: []float64{0}},
		{Metric: "coverage", Comparator: experiment.ComparatorGTE, Thresholds: []float64{0.8}},
		{Metric: "score", Comparator: experiment.ComparatorBetween, Thresholds: []float64{-1.25, 4.5}},
	}
	if !reflect.DeepEqual(assertions, want) {
		t.Fatalf("assertions = %#v, want %#v", assertions, want)
	}

	for _, values := range [][]string{
		{"repo=0"}, {"repo=01"}, {"repo=9007199254740993", "repo=2"}, {"=1"}, {"repo=1=2"},
	} {
		if _, err := parseAgentExperimentInputs(values); err == nil {
			t.Fatalf("parseAgentExperimentInputs(%q) succeeded", values)
		}
	}
	for _, values := range [][]string{
		{""}, {"metric"}, {"metric=eq:1"}, {"metric=lt"}, {"metric=lt:1:2"},
		{"metric=between:1"}, {"metric=between:2:1"}, {"metric=gte:NaN"},
		{"metric=gte:+Inf"}, {"metric=lt:1", "metric=gt:0"},
	} {
		if _, err := parseAgentExperimentAssertions(values); err == nil {
			t.Fatalf("parseAgentExperimentAssertions(%q) succeeded", values)
		}
	}
}

func TestExperimentsAddFixtureGetsThenRevisionedPutsQuotedInputs(t *testing.T) {
	stored := testStoredExperiment()
	var requestCount atomic.Int32
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requestCount.Add(1)
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1/agent/experiments/"+largeAgentExperimentID:
			writeAgentExperimentTestJSON(t, response, stored)
		case request.Method == http.MethodPut && request.URL.Path == "/api/v1/agent/experiments/"+largeAgentExperimentID:
			var mutation struct {
				Revision   int64                 `json:"revision"`
				Definition experiment.Definition `json:"definition"`
			}
			decodeAgentExperimentTestJSON(t, request, &mutation)
			if mutation.Revision != 7 {
				t.Fatalf("revision = %d, want 7", mutation.Revision)
			}
			fixtures := mutation.Definition.Fixtures
			if len(fixtures) != 2 {
				t.Fatalf("fixtures = %#v", fixtures)
			}
			fixture := fixtures[1]
			if fixture.Label != "adversarial" || fixture.Role != experiment.FixtureNegativeControl || fixture.Inputs["repo"].String() != largeAgentExperimentID {
				t.Fatalf("fixture = %#v", fixture)
			}
			if len(fixture.Assertions) != 1 || fixture.Assertions[0].Comparator != experiment.ComparatorBetween {
				t.Fatalf("fixture assertions = %#v", fixture.Assertions)
			}
			stored.Definition = mutation.Definition
			stored.Revision = 8
			writeAgentExperimentTestJSON(t, response, stored)
		default:
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.Path)
		}
	})

	command := ExperimentsAddFixtureCommand{
		Role:       "negative-control",
		Inputs:     []string{"repo=" + largeAgentExperimentID},
		Assertions: []string{"defects=between:1:3"},
		Revision:   7,
	}
	command.Args.Experiment = largeAgentExperimentID
	command.Args.Label = "adversarial"
	var output bytes.Buffer
	if err := command.execute(agentExperimentTestTarget(handler), &output); err != nil {
		t.Fatal(err)
	}
	if requestCount.Load() != 2 {
		t.Fatalf("request count = %d, want 2", requestCount.Load())
	}
	if !strings.Contains(output.String(), largeAgentExperimentID) || !strings.Contains(output.String(), "8") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestExperimentsAddFixtureRejectsMalformedAssertionBeforeAnyRequest(t *testing.T) {
	var requestCount atomic.Int32
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requestCount.Add(1)
	})

	command := ExperimentsAddFixtureCommand{
		Role:       "negative-control",
		Inputs:     []string{"repo=1"},
		Assertions: []string{"quality=between:1"},
	}
	command.Args.Experiment = largeAgentExperimentID
	command.Args.Label = "broken"
	err := command.execute(agentExperimentTestTarget(handler), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "between") {
		t.Fatalf("error = %v", err)
	}
	if requestCount.Load() != 0 {
		t.Fatalf("malformed assertion sent %d requests", requestCount.Load())
	}
}

func TestExperimentsAddVariantResolvesPinnedWorkflowAndUsesFetchedRevision(t *testing.T) {
	stored := testStoredExperiment()
	definition := testAgentExperimentWorkflowDefinition(t)
	var paths []string
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.Method+" "+request.URL.Path)
		switch request.Method + " " + request.URL.Path {
		case "GET /api/v1/agent/experiments/" + largeAgentExperimentID:
			writeAgentExperimentTestJSON(t, response, stored)
		case "GET /api/v1/agent/workflows/review/versions/4":
			writeAgentExperimentTestJSON(t, response, definition)
		case "PUT /api/v1/agent/experiments/" + largeAgentExperimentID:
			var mutation struct {
				Revision   int64                 `json:"revision"`
				Definition experiment.Definition `json:"definition"`
			}
			decodeAgentExperimentTestJSON(t, request, &mutation)
			if mutation.Revision != stored.Revision {
				t.Fatalf("revision = %d, want fetched %d", mutation.Revision, stored.Revision)
			}
			if len(mutation.Definition.Variants) != 2 {
				t.Fatalf("variants = %#v", mutation.Definition.Variants)
			}
			variant := mutation.Definition.Variants[1]
			if variant.Label != "candidate" || variant.Control || variant.Target.Kind != experiment.TargetWorkflow ||
				variant.Target.WorkflowName != "review" || variant.Target.DefinitionID != int64(definition.ID) || variant.Target.Version != definition.Version {
				t.Fatalf("variant = %#v", variant)
			}
			wantHash, err := experiment.HashSignature(stored.Definition.Signature)
			if err != nil {
				t.Fatal(err)
			}
			if variant.SignatureHash != wantHash {
				t.Fatalf("signature hash = %q, want %q", variant.SignatureHash, wantHash)
			}
			stored.Definition = mutation.Definition
			stored.Revision++
			writeAgentExperimentTestJSON(t, response, stored)
		default:
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.Path)
		}
	})

	command := ExperimentsAddVariantCommand{}
	command.Args.Experiment = largeAgentExperimentID
	command.Args.Variant = "candidate=review@4"
	if err := command.execute(agentExperimentTestTarget(handler), io.Discard); err != nil {
		t.Fatal(err)
	}
	wantPaths := []string{
		"GET /api/v1/agent/experiments/" + largeAgentExperimentID,
		"GET /api/v1/agent/workflows/review/versions/4",
		"PUT /api/v1/agent/experiments/" + largeAgentExperimentID,
	}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Fatalf("paths = %#v, want %#v", paths, wantPaths)
	}
}

func TestExperimentsAddVariantResolvesPinnedNodeAndSetsParameters(t *testing.T) {
	stored := testStoredExperiment()
	node := testAgentExperimentNodeDefinition(t)
	var paths []string
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.Method+" "+request.URL.Path)
		switch request.Method + " " + request.URL.Path {
		case "GET /api/v1/agent/experiments/" + largeAgentExperimentID:
			writeAgentExperimentTestJSON(t, response, stored)
		case "GET /api/v1/agent/nodes/small-fix/versions/4":
			writeAgentExperimentTestJSON(t, response, node)
		case "PUT /api/v1/agent/experiments/" + largeAgentExperimentID:
			var mutation struct {
				Revision   int64                 `json:"revision"`
				Definition experiment.Definition `json:"definition"`
			}
			decodeAgentExperimentTestJSON(t, request, &mutation)
			if len(mutation.Definition.Variants) != 2 {
				t.Fatalf("variants = %#v", mutation.Definition.Variants)
			}
			variant := mutation.Definition.Variants[1]
			if variant.Label != "early" || variant.Control || variant.Target.Kind != experiment.TargetNode ||
				variant.Target.WorkflowName != "small-fix" || variant.Target.DefinitionID != int64(node.ID) ||
				variant.Target.Version != node.Version || variant.Target.FunctionID != "" ||
				variant.Target.NodeParameters["max_turns"] != "12" || len(variant.Target.NodeParameters) != 1 {
				t.Fatalf("variant = %#v", variant)
			}
			wantHash, err := experiment.HashSignature(stored.Definition.Signature)
			if err != nil {
				t.Fatal(err)
			}
			if variant.SignatureHash != wantHash {
				t.Fatalf("signature hash = %q, want %q", variant.SignatureHash, wantHash)
			}
			stored.Definition = mutation.Definition
			stored.Revision++
			writeAgentExperimentTestJSON(t, response, stored)
		default:
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.Path)
		}
	})

	command := ExperimentsAddVariantCommand{Param: []string{"max_turns=12"}}
	command.Args.Experiment = largeAgentExperimentID
	command.Args.Variant = "early=node:small-fix@4"
	if err := command.execute(agentExperimentTestTarget(handler), io.Discard); err != nil {
		t.Fatal(err)
	}
	wantPaths := []string{
		"GET /api/v1/agent/experiments/" + largeAgentExperimentID,
		"GET /api/v1/agent/nodes/small-fix/versions/4",
		"PUT /api/v1/agent/experiments/" + largeAgentExperimentID,
	}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Fatalf("paths = %#v, want %#v", paths, wantPaths)
	}
}

func TestExperimentsAddVariantRejectsParamForNonNodeVariant(t *testing.T) {
	var requestCount atomic.Int32
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requestCount.Add(1)
	})

	command := ExperimentsAddVariantCommand{Param: []string{"max_turns=12"}}
	command.Args.Experiment = largeAgentExperimentID
	command.Args.Variant = "candidate=review@4"
	err := command.execute(agentExperimentTestTarget(handler), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--param") || !strings.Contains(err.Error(), "node") {
		t.Fatalf("error = %v", err)
	}
	if requestCount.Load() != 0 {
		t.Fatalf("rejected --param still sent %d requests", requestCount.Load())
	}
}

func TestParseAgentExperimentNodeParametersRejectsMalformedValues(t *testing.T) {
	params, err := parseAgentExperimentNodeParameters([]string{"max_turns=12", "model=opus"})
	if err != nil || params["max_turns"] != "12" || params["model"] != "opus" || len(params) != 2 {
		t.Fatalf("params = %#v, err = %v", params, err)
	}
	for _, values := range [][]string{
		{"max_turns"},                    // no "="
		{"=12"},                          // empty key
		{"max_turns="},                   // empty value
		{" max_turns=12"},                // key not trimmed
		{"max_turns=12", "max_turns=13"}, // duplicate key
	} {
		if _, err := parseAgentExperimentNodeParameters(values); err == nil {
			t.Fatalf("parseAgentExperimentNodeParameters(%q) succeeded", values)
		}
	}
}

func TestResolveAgentExperimentVariantExtractsPinnedFunctionSignature(t *testing.T) {
	definition := testAgentExperimentWorkflowDefinition(t)
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/v1/agent/workflows/review/versions/4" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		writeAgentExperimentTestJSON(t, response, definition)
	})
	reference, err := parseAgentExperimentVariantReference("candidate=review@4#review")
	if err != nil {
		t.Fatal(err)
	}
	variant, err := resolveAgentExperimentVariant(agentExperimentTestTarget(handler), reference, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if variant.Target.Kind != experiment.TargetFunction || variant.Target.FunctionID != "review" ||
		variant.Target.DefinitionID != 42 || variant.Target.Version != 4 {
		t.Fatalf("target = %#v", variant.Target)
	}
	extracted, err := workflow.ExtractFunctionTarget(definition, "review")
	if err != nil {
		t.Fatal(err)
	}
	wantHash, err := experiment.HashSignature(extracted.Signature)
	if err != nil {
		t.Fatal(err)
	}
	if variant.SignatureHash != wantHash {
		t.Fatalf("signature hash = %q, want %q", variant.SignatureHash, wantHash)
	}
}

func TestExperimentMutationSurfacesServerRevisionConflict(t *testing.T) {
	stored := testStoredExperiment()
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			writeAgentExperimentTestJSON(t, response, stored)
		case http.MethodPut:
			response.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(response, `{"error":"Conflict","message":"experiment: revision conflict"}`)
		}
	})

	command := ExperimentsAddFixtureCommand{Role: "normal", Inputs: []string{"repo=1"}, Revision: 6}
	command.Args.Experiment = largeAgentExperimentID
	command.Args.Label = "new-fixture"
	err := command.execute(agentExperimentTestTarget(handler), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "409 Conflict") || !strings.Contains(err.Error(), "revision conflict") {
		t.Fatalf("error = %v", err)
	}
}

func TestExperimentRevisionActionsPostExactIDAndRevision(t *testing.T) {
	for _, action := range []string{"validate", "start", "cancel"} {
		t.Run(action, func(t *testing.T) {
			stored := testStoredExperiment()
			handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodPost || request.URL.Path != "/api/v1/agent/experiments/"+largeAgentExperimentID+"/"+action {
					t.Fatalf("request = %s %s", request.Method, request.URL.Path)
				}
				var body map[string]int64
				decodeAgentExperimentTestJSON(t, request, &body)
				if body["revision"] != 7 {
					t.Fatalf("body = %#v", body)
				}
				if action == "validate" {
					writeAgentExperimentTestJSON(t, response, agentExperimentValidationResult{Valid: true, Revision: 7, ExpectedCells: 10})
				} else {
					stored.Revision++
					writeAgentExperimentTestJSON(t, response, stored)
				}
			})

			var output bytes.Buffer
			target := agentExperimentTestTarget(handler)
			var err error
			switch action {
			case "validate":
				command := ExperimentsValidateCommand{Revision: 7}
				command.Args.Experiment = largeAgentExperimentID
				err = command.execute(target, &output)
			case "start":
				command := ExperimentsStartCommand{Revision: 7}
				command.Args.Experiment = largeAgentExperimentID
				err = command.execute(target, &output)
			case "cancel":
				command := ExperimentsCancelCommand{Revision: 7}
				command.Args.Experiment = largeAgentExperimentID
				err = command.execute(target, &output)
			}
			if err != nil {
				t.Fatal(err)
			}
			if output.Len() == 0 {
				t.Fatal("command produced no output")
			}
		})
	}
}

func TestRenderAgentExperimentsAndCellsAsTablesOrLosslessJSON(t *testing.T) {
	stored := testStoredExperiment()
	for _, jsonOutput := range []bool{false, true} {
		var output bytes.Buffer
		if err := renderAgentExperiments(&output, []agentExperimentRecord{stored}, jsonOutput, true); err != nil {
			t.Fatal(err)
		}
		text := output.String()
		if !strings.Contains(text, largeAgentExperimentID) || !strings.Contains(text, "review-prompts") {
			t.Fatalf("experiment output json=%v: %q", jsonOutput, text)
		}
		if jsonOutput && !strings.Contains(text, `"id": "`+largeAgentExperimentID+`"`) {
			t.Fatalf("experiment ID not quoted in JSON: %q", text)
		}
	}

	cell := agentExperimentCell{
		ID: 9007199254740995, ExperimentID: 9007199254740993,
		FixtureID: 9007199254740997, FixtureLabel: "fixture-a", FixtureRole: experiment.FixtureNormal,
		VariantID: 9007199254740999, VariantLabel: "control", Repetition: 1, Status: experiment.CellValidMeasurement,
	}
	for _, jsonOutput := range []bool{false, true} {
		var output bytes.Buffer
		if err := renderAgentExperimentCells(&output, []agentExperimentCell{cell}, jsonOutput, true); err != nil {
			t.Fatal(err)
		}
		text := output.String()
		if !strings.Contains(text, "9007199254740995") || !strings.Contains(text, "fixture-a") || !strings.Contains(text, "valid_measurement") {
			t.Fatalf("cell output json=%v: %q", jsonOutput, text)
		}
		if jsonOutput && !strings.Contains(text, `"id": "9007199254740995"`) {
			t.Fatalf("cell ID not quoted in JSON: %q", text)
		}
	}
}

func TestRenderSingleAgentExperimentAndCellAsJSONObject(t *testing.T) {
	stored := testStoredExperiment()
	var experimentOutput bytes.Buffer
	if err := renderAgentExperiment(&experimentOutput, stored, true, true); err != nil {
		t.Fatal(err)
	}
	if text := strings.TrimSpace(experimentOutput.String()); !strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") {
		t.Fatalf("single experiment JSON = %q", text)
	}

	cell := agentExperimentCell{
		ID: 9007199254740995, ExperimentID: stored.ID,
		FixtureID: 9007199254740997, VariantID: 9007199254740999,
	}
	var cellOutput bytes.Buffer
	if err := renderAgentExperimentCell(&cellOutput, cell, true, true); err != nil {
		t.Fatal(err)
	}
	if text := strings.TrimSpace(cellOutput.String()); !strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") {
		t.Fatalf("single cell JSON = %q", text)
	}
}

func TestRenderAgentExperimentScorecardTableAndJSON(t *testing.T) {
	scorecard := experiment.Scorecard{
		ExperimentID: 9007199254740993,
		Control:      "control",
		Variants: map[string]experiment.VariantScore{
			"control": {
				ExpectedCells: 5, ValidCells: 5, ValidCoverage: 1,
				Metrics: map[string]experiment.Distribution{
					"quality": {Count: 5, Mean: 0.2, Median: 0.19, StdDev: 0.03, Min: 0.15, Max: 0.25},
				},
			},
			"candidate": {
				ExpectedCells: 5, ValidCells: 5, ValidCoverage: 1,
				Metrics: map[string]experiment.Distribution{
					"quality": {Count: 5, Mean: 0.45, Median: 0.44, StdDev: 0.04, Min: 0.4, Max: 0.5},
				},
			},
		},
		Comparisons: map[string]map[string]experiment.PairedComparison{
			"candidate": {
				"quality": {
					Variant: "candidate", Control: "control", Metric: "quality", PairedCount: 5,
					MeanDelta: 0.25, ConfidenceLow: 0.1, ConfidenceHigh: 0.4,
					Recommendation: experiment.RecommendationWinner, Winner: "candidate",
				},
			},
		},
	}
	var table bytes.Buffer
	if err := renderAgentExperimentScorecard(&table, scorecard, false, true); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"candidate", "control", "quality", "0.2", "0.45", "0.25", "winner"} {
		if !strings.Contains(table.String(), want) {
			t.Fatalf("table = %q; missing %q", table.String(), want)
		}
	}
	var jsonOutput bytes.Buffer
	if err := renderAgentExperimentScorecard(&jsonOutput, scorecard, true, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jsonOutput.String(), `"experiment_id": "`+largeAgentExperimentID+`"`) {
		t.Fatalf("scorecard JSON = %q", jsonOutput.String())
	}
}

func TestReadAgentExperimentDefinitionRejectsNumericSnapshotIDs(t *testing.T) {
	definition := testStoredExperiment().Definition
	payload, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	numeric := strings.Replace(string(payload), `"101"`, `101`, 1)
	path := filepath.Join(t.TempDir(), "experiment.json")
	if err := os.WriteFile(path, []byte(numeric), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readAgentExperimentDefinition(path); err == nil || !strings.Contains(err.Error(), "quoted") {
		t.Fatalf("error = %v", err)
	}
}

func TestExperimentsCreateAndUpdateSendCompleteDefinitionBodies(t *testing.T) {
	stored := testStoredExperiment()
	payload, err := json.Marshal(stored.Definition)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "experiment.json")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, action := range []string{"create", "update"} {
		t.Run(action, func(t *testing.T) {
			handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if action == "create" {
					if request.Method != http.MethodPost || request.URL.Path != "/api/v1/agent/experiments" {
						t.Fatalf("request = %s %s", request.Method, request.URL.Path)
					}
					var definition experiment.Definition
					decodeAgentExperimentTestJSON(t, request, &definition)
					if definition.Fixtures[0].Inputs["repo"].String() != "101" {
						t.Fatalf("definition = %#v", definition)
					}
				} else {
					if request.Method != http.MethodPut || request.URL.Path != "/api/v1/agent/experiments/"+largeAgentExperimentID {
						t.Fatalf("request = %s %s", request.Method, request.URL.Path)
					}
					var mutation struct {
						Revision   int64                 `json:"revision"`
						Definition experiment.Definition `json:"definition"`
					}
					decodeAgentExperimentTestJSON(t, request, &mutation)
					if mutation.Revision != 7 || mutation.Definition.Name != stored.Definition.Name {
						t.Fatalf("mutation = %#v", mutation)
					}
				}
				writeAgentExperimentTestJSON(t, response, stored)
			})

			var output bytes.Buffer
			if action == "create" {
				command := ExperimentsCreateCommand{Json: true}
				command.Args.Path = path
				err = command.execute(agentExperimentTestTarget(handler), &output)
			} else {
				command := ExperimentsUpdateCommand{Revision: 7, Json: true}
				command.Args.Experiment = largeAgentExperimentID
				command.Args.Path = path
				err = command.execute(agentExperimentTestTarget(handler), &output)
			}
			if err != nil {
				t.Fatal(err)
			}
			if text := strings.TrimSpace(output.String()); !strings.HasPrefix(text, "{") || !strings.Contains(text, `"id": "`+largeAgentExperimentID+`"`) {
				t.Fatalf("output = %q", text)
			}
		})
	}
}

func TestExperimentReadCommandsUseCanonicalPathsAndJSONShapes(t *testing.T) {
	stored := testStoredExperiment()
	cell := agentExperimentCell{
		ID: 9007199254740995, ExperimentID: stored.ID,
		FixtureID: 9007199254740997, FixtureLabel: "fixture-a", FixtureRole: experiment.FixtureNormal,
		VariantID: 9007199254740999, VariantLabel: "control", Repetition: 1, Status: experiment.CellValidMeasurement,
	}
	scorecard := experiment.Scorecard{
		ExperimentID: stored.ID, Control: "control",
		Variants:    map[string]experiment.VariantScore{"control": {ExpectedCells: 5, ValidCells: 5, ValidCoverage: 1}},
		Comparisons: map[string]map[string]experiment.PairedComparison{},
	}
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Fatalf("method = %s", request.Method)
		}
		switch request.URL.Path {
		case "/api/v1/agent/experiments":
			writeAgentExperimentTestJSON(t, response, []agentExperimentRecord{stored})
		case "/api/v1/agent/experiments/" + largeAgentExperimentID:
			writeAgentExperimentTestJSON(t, response, stored)
		case "/api/v1/agent/experiments/" + largeAgentExperimentID + "/cells":
			writeAgentExperimentTestJSON(t, response, []agentExperimentCell{cell})
		case "/api/v1/agent/experiments/" + largeAgentExperimentID + "/cells/9007199254740995":
			writeAgentExperimentTestJSON(t, response, cell)
		case "/api/v1/agent/experiments/" + largeAgentExperimentID + "/scorecard":
			writeAgentExperimentTestJSON(t, response, scorecard)
		default:
			t.Fatalf("unexpected path %s", request.URL.Path)
		}
	})
	target := agentExperimentTestTarget(handler)

	var listOutput bytes.Buffer
	if err := (&ExperimentsListCommand{Json: true}).execute(target, &listOutput); err != nil {
		t.Fatal(err)
	}
	if text := strings.TrimSpace(listOutput.String()); !strings.HasPrefix(text, "[") {
		t.Fatalf("list JSON = %q", text)
	}

	show := ExperimentsShowCommand{Json: true}
	show.Args.Experiment = largeAgentExperimentID
	var showOutput bytes.Buffer
	if err := show.execute(target, &showOutput); err != nil {
		t.Fatal(err)
	}
	if text := strings.TrimSpace(showOutput.String()); !strings.HasPrefix(text, "{") || !strings.Contains(text, largeAgentExperimentID) {
		t.Fatalf("show JSON = %q", text)
	}

	cells := ExperimentsCellsCommand{Json: true}
	cells.Args.Experiment = largeAgentExperimentID
	var cellsOutput bytes.Buffer
	if err := cells.execute(target, &cellsOutput); err != nil {
		t.Fatal(err)
	}
	if text := strings.TrimSpace(cellsOutput.String()); !strings.HasPrefix(text, "[") {
		t.Fatalf("cells JSON = %q", text)
	}

	cells.Args.Cell = "9007199254740995"
	var cellOutput bytes.Buffer
	if err := cells.execute(target, &cellOutput); err != nil {
		t.Fatal(err)
	}
	if text := strings.TrimSpace(cellOutput.String()); !strings.HasPrefix(text, "{") || !strings.Contains(text, "9007199254740995") {
		t.Fatalf("cell JSON = %q", text)
	}

	scorecardCommand := ExperimentsScorecardCommand{}
	scorecardCommand.Args.Experiment = largeAgentExperimentID
	var scorecardOutput bytes.Buffer
	if err := scorecardCommand.execute(target, &scorecardOutput); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(scorecardOutput.String(), "control") {
		t.Fatalf("scorecard output = %q", scorecardOutput.String())
	}
}

func TestExperimentListTraversesEveryDurableHistoryPage(t *testing.T) {
	first := testStoredExperiment()
	first.ID = 102
	second := first
	second.ID = 101
	cursor, err := pagination.Encode(pagination.Cursor{CreatedAt: first.CreatedAt, ID: int64(first.ID)})
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls++
		if request.URL.Path != agentExperimentsPath || request.URL.Query().Get("limit") != strconv.Itoa(experiment.MaxListedExperiments) {
			t.Fatalf("request = %s?%s", request.URL.Path, request.URL.RawQuery)
		}
		switch calls {
		case 1:
			if request.URL.Query().Get("cursor") != "" {
				t.Fatalf("first cursor = %q", request.URL.Query().Get("cursor"))
			}
			response.Header().Set("X-Next-Cursor", cursor)
			writeAgentExperimentTestJSON(t, response, []agentExperimentRecord{first})
		case 2:
			if request.URL.Query().Get("cursor") != cursor {
				t.Fatalf("second cursor = %q, want %q", request.URL.Query().Get("cursor"), cursor)
			}
			writeAgentExperimentTestJSON(t, response, []agentExperimentRecord{second})
		default:
			t.Fatalf("unexpected page request %d", calls)
		}
	})
	var output bytes.Buffer
	if err := (&ExperimentsListCommand{Json: true}).execute(agentExperimentTestTarget(handler), &output); err != nil {
		t.Fatal(err)
	}
	var values []agentExperimentRecord
	if err := json.Unmarshal(output.Bytes(), &values); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(values) != 2 || values[0].ID != first.ID || values[1].ID != second.ID {
		t.Fatalf("calls/values = %d/%#v", calls, values)
	}
}

func testStoredExperiment() agentExperimentRecord {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	signature := workflow.PublicSignature{
		Inputs:  []workflow.SignaturePort{{Name: "repo", Type: "repository/v1"}},
		Outputs: []workflow.SignaturePort{{Name: "review", Type: "review/v1"}},
	}
	hash, _ := experiment.HashSignature(signature)
	return agentExperimentRecord{
		ID: 9007199254740993, TeamName: "main", Revision: 7,
		CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
		Definition: experiment.Definition{
			Name: "review-prompts", State: experiment.StateDraft, Signature: signature,
			Variants: []experiment.Variant{{
				Label: "control", Control: true, SignatureHash: hash,
				Target: experiment.Target{Kind: experiment.TargetWorkflow, WorkflowName: "review", DefinitionID: 41, Version: 3},
			}},
			Fixtures: []experiment.Fixture{{
				Label: "fixture-a", Role: experiment.FixtureNormal,
				Inputs: map[string]snapshot.SnapshotID{"repo": 101},
			}},
			Repetitions: 5,
		},
	}
}

func testAgentExperimentWorkflowDefinition(t *testing.T) workflow.Definition {
	t.Helper()
	compiled, err := workflow.CompileDefinition(workflow.Manifest{"workflow.yml": `schema_version: 3
name: review
signature_version: 1
inputs:
  - {name: repo, type: repository/v1}
outputs:
  - {name: review, type: review/v1, from: review}
plan:
  - agent: review
    function_id: review
    prompt: Review the repository.
    inputs: [repo]
    outputs: [review]
    input_types:
      repo: {type: repository/v1}
    output_types:
      review: review/v1
`})
	if err != nil {
		t.Fatal(err)
	}
	return workflow.Definition{
		ID: 42, Name: "review", Version: 4, SchemaVersion: 3, SignatureVersion: 1,
		Compiled: *compiled,
	}
}

// testAgentExperimentNodeDefinition compiles a minimal reusable node whose
// public signature (input "repo" repository/v1, output "review" review/v1)
// intentionally matches testStoredExperiment's frozen experiment signature,
// so a resolved node variant's signature hash can be compared directly
// against it the same way the workflow-variant tests do.
func testAgentExperimentNodeDefinition(t *testing.T) workflow.NodeDefinition {
	t.Helper()
	compiled, err := workflow.CompileNodeDefinition(workflow.Manifest{workflow.NodeFileName: `schema_version: 1
name: small-fix
inputs:
  - {name: repo, type: repository/v1}
outputs:
  - {name: review, type: review/v1}
parameters:
  - {name: max_turns, default: "5"}
step:
  agent: fix
  prompt: Fix the bug.
  inputs: [repo]
  outputs: [review]
`})
	if err != nil {
		t.Fatal(err)
	}
	return workflow.NodeDefinition{
		ID: 41, Name: "small-fix", Version: 4, ContentHash: "abc123",
		Compiled: *compiled,
	}
}

func agentExperimentTestTarget(handler http.Handler) rc.Target {
	client := new(concoursefakes.FakeClient)
	client.HTTPClientReturns(&http.Client{Transport: agentExperimentRoundTripper(func(request *http.Request) (*http.Response, error) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Result(), nil
	})})
	target := new(rcfakes.FakeTarget)
	target.URLReturns("http://agent.test")
	target.ClientReturns(client)
	return target
}

type agentExperimentRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip agentExperimentRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func decodeAgentExperimentTestJSON(t *testing.T, request *http.Request, destination any) {
	t.Helper()
	defer request.Body.Close()
	if got := request.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if err := json.NewDecoder(request.Body).Decode(destination); err != nil {
		t.Fatal(err)
	}
}

func writeAgentExperimentTestJSON(t *testing.T, response http.ResponseWriter, value any) {
	t.Helper()
	response.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(response).Encode(value); err != nil {
		t.Fatal(err)
	}
}
