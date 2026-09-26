package client

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/detached"
	"github.com/concourse/concourse/agent/implement"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/santhosh-tekuri/jsonschema/v5"
)

// connect serves the implement tools for p's destination in memory.
func connect(t *testing.T, p *platform, authFile string) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	server := mcp.NewServer(&mcp.Implementation{Name: "implement-tools-test", Version: "1"}, nil)
	RegisterTools(server, p.client(t), MCPOptions{Team: team, Template: template, AuthFile: authFile})
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { serverSession.Close() })
	session, err := mcp.NewClient(&mcp.Implementation{Name: "implement-tools-test", Version: "1"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

// call invokes a tool and decodes its structured content into out, requiring
// the call to succeed.
func call(t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any, out any) {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || result.StructuredContent == nil {
		t.Fatalf("%s failed: %s", name, toolText(result))
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatal(err)
	}
}

// refused invokes a tool that must report a tool error containing want.
func refused(t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any, want string) {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || result.StructuredContent != nil || !strings.Contains(toolText(result), want) {
		t.Fatalf("%s was not refused with %q: %s", name, want, toolText(result))
	}
}

func toolText(result *mcp.CallToolResult) string {
	var text []string
	for _, content := range result.Content {
		if t, ok := content.(*mcp.TextContent); ok {
			text = append(text, t.Text)
		}
	}
	return strings.Join(text, "\n")
}

func TestImplementToolsListing(t *testing.T) {
	session := connect(t, newPlatform(t), "")
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		if tool.OutputSchema == nil || strings.Contains(string(schema), "auth") {
			t.Fatalf("%s has no typed output, or takes credentials as an argument: %s", tool.Name, schema)
		}
		if readOnly := tool.Annotations != nil && tool.Annotations.ReadOnlyHint; readOnly == (tool.Name == "implement_submit") {
			t.Fatalf("%s has the wrong read-only hint", tool.Name)
		}
	}
	sort.Strings(names)
	if strings.Join(names, " ") != "implement_result implement_status implement_submit" {
		t.Fatalf("unexpected implement tools: %v", names)
	}
}

// Status and both results read through the tools are the ones the shared
// client verified: the change against the Run's binding, and the validation
// against the change.
func TestImplementToolsReadAVerifiedRun(t *testing.T) {
	s := sealedSnapshot(t)
	summary, patch := publishedChange(t, s, runID)
	p := newPlatform(t)
	session := connect(t, p, "")
	refused(t, session, "implement_result", map[string]any{"run": number}, "still running")
	p.publishResults(t, map[string]map[string][]byte{
		ChangeResult:     {implement.SummaryFile: summary, implement.PatchFile: patch},
		ValidationResult: {implement.ValidationFile: publishedValidation(t, summary, patch, nil)},
	})

	var run detached.RunObservation
	call(t, session, "implement_status", map[string]any{"run": number}, &run)
	if run.ID != runID || run.Number != number || run.Terminal == nil || len(run.Terminal.Results) != 2 {
		t.Fatalf("status does not observe the terminal Run and its two results: %+v", run)
	}

	var change ResultOutput
	call(t, session, "implement_result", map[string]any{"run": number}, &change)
	if change.Result != ChangeResult || change.Validation != nil || change.Summary == nil || change.Summary.RunID == nil || *change.Summary.RunID != runID || change.Patch == nil || *change.Patch != string(patch) {
		t.Fatalf("result is not the verified change: %+v", change)
	}
	// What the tool returns still parses as the change it published.
	republished, err := json.Marshal(change.Summary)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := implement.ParseChange(republished, []byte(*change.Patch), s); err != nil {
		t.Fatalf("the returned change does not apply to its snapshot: %v", err)
	}

	var validation ResultOutput
	call(t, session, "implement_result", map[string]any{"run": number, "result": ValidationResult}, &validation)
	// A failing command is a result, not a tool error.
	if validation.Result != ValidationResult || validation.Summary != nil || validation.Patch != nil || validation.Validation == nil ||
		validation.Validation.Outcome != implement.ValidationFailed || validation.Validation.PatchDigest != change.Summary.PatchDigest {
		t.Fatalf("result is not the validation of the Run's change: %+v", validation)
	}
	refused(t, session, "implement_result", map[string]any{"run": number, "result": "findings"}, "result must be")
	refused(t, session, "implement_submit", map[string]any{"input": s.Dir, "receipt": filepath.Join(t.TempDir(), "request.json")}, "--auth-file")
}

func TestImplementResultToolRefusesAnUnattestedValidation(t *testing.T) {
	s := sealedSnapshot(t)
	summary, patch := publishedChange(t, s, runID)
	p := newPlatform(t)
	p.publishResults(t, map[string]map[string][]byte{
		ChangeResult: {implement.SummaryFile: summary, implement.PatchFile: patch},
		ValidationResult: {implement.ValidationFile: publishedValidation(t, summary, patch, func(v map[string]any) {
			v["patch_digest"] = strings.Repeat("0", 64)
		})},
	})
	session := connect(t, p, "")
	refused(t, session, "implement_result", map[string]any{"run": number, "result": ValidationResult}, "patch digest")

	p = newPlatform(t)
	p.publish(t, map[string][]byte{implement.SummaryFile: summary, implement.PatchFile: patch})
	session = connect(t, p, "")
	refused(t, session, "implement_result", map[string]any{"run": number, "result": ValidationResult}, ErrNoValidation.Error())
	var change ResultOutput
	call(t, session, "implement_result", map[string]any{"run": number}, &change)
}

func TestImplementSubmitToolUsesTheStartupAuthFile(t *testing.T) {
	p := newPlatform(t)
	s := sealedSnapshot(t)
	root := t.TempDir()
	auth := filepath.Join(root, "auth.json")
	if err := os.WriteFile(auth, []byte(`{"synthetic":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	session := connect(t, p, auth)
	var submitted Submission
	call(t, session, "implement_submit", map[string]any{"input": s.Dir, "receipt": filepath.Join(root, "request.json")}, &submitted)
	if !submitted.Ready || submitted.RunID != runID || submitted.Handle != handle() {
		t.Fatalf("submission did not reach readiness: %+v", submitted)
	}
	if len(p.credentials) == 0 || p.credentials[0] != ChangeResult || p.uploads[SnapshotInput] == nil {
		t.Fatalf("the tool did not submit the implement workload: %v %v", p.credentials, p.uploads)
	}
}

// The advertised output schema is the published implement/v1 and
// implement-validation/v1 contracts, and admits exactly one of the two.
func TestImplementResultSchema(t *testing.T) {
	schema, err := jsonschema.CompileString("result.json", string(resultSchema()))
	if err != nil {
		t.Fatal(err)
	}
	s := sealedSnapshot(t)
	summaryJSON, patch := publishedChange(t, s, runID)
	var summary, validation any
	if err := json.Unmarshal(summaryJSON, &summary); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(publishedValidation(t, summaryJSON, patch, nil), &validation); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		output map[string]any
		valid  bool
	}{
		"change":               {map[string]any{"result": "change", "summary": summary, "patch": string(patch)}, true},
		"empty change":         {map[string]any{"result": "change", "summary": summary, "patch": ""}, true},
		"validation":           {map[string]any{"result": "validation", "validation": validation}, true},
		"change without patch": {map[string]any{"result": "change", "summary": summary}, false},
		"validation as change": {map[string]any{"result": "change", "validation": validation}, false},
		"both":                 {map[string]any{"result": "validation", "validation": validation, "summary": summary, "patch": string(patch)}, false},
		"another result":       {map[string]any{"result": "findings", "validation": validation}, false},
		"summary off contract": {map[string]any{"result": "change", "summary": map[string]any{"summary": "x"}, "patch": ""}, false},
		"unknown field":        {map[string]any{"result": "validation", "validation": validation, "extra": true}, false},
	} {
		t.Run(name, func(t *testing.T) {
			// Round-trip through JSON so numbers are what a client decodes.
			data, err := json.Marshal(c.output)
			if err != nil {
				t.Fatal(err)
			}
			var value any
			if err := json.Unmarshal(data, &value); err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(value); (err == nil) != c.valid {
				t.Fatalf("valid=%v, got %v", c.valid, err)
			}
		})
	}
}
