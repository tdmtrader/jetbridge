package harvest_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/harvest"
)

// writeFixtureModule materializes a throwaway Go module at dir/name so
// the v0.5 fixed command map (`go build|test|vet ./...`) has something
// real to execute (RunGates has no fake-executor seam — it shells out).
func writeFixtureModule(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", full, err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}
	return dir
}

const passingModGo = "module fixture\n\ngo 1.21\n"
const passingMain = "package p\n\nfunc F() int { return 1 }\n"
const passingTest = "package p\n\nimport \"testing\"\n\nfunc TestF(t *testing.T) {\n\tif F() != 1 {\n\t\tt.Fatal(\"wrong\")\n\t}\n}\n"

func passingModule(t *testing.T) string {
	return writeFixtureModule(t, map[string]string{
		"go.mod":    passingModGo,
		"main.go":   passingMain,
		"f_test.go": passingTest,
	})
}

func TestRunGatesBuildAndTestPass(t *testing.T) {
	workspace := passingModule(t)
	policy := harvest.GatePolicy{Gates: []harvest.Gate{
		{Gate: "build", Scope: "full"},
		{Gate: "test", Scope: "full"},
	}}

	outcomes, err := harvest.RunGates(policy, workspace, "", nil)
	if err != nil {
		t.Fatalf("RunGates: %v", err)
	}
	if len(outcomes) != 2 {
		t.Fatalf("outcomes = %+v, want 2", outcomes)
	}
	for _, o := range outcomes {
		if o.Status != "ok" {
			t.Errorf("gate %s: status = %q, want ok (detail: %s)", o.Gate, o.Status, o.Detail)
		}
		if o.Attempt != 1 {
			t.Errorf("gate %s: attempt = %d, want 1", o.Gate, o.Attempt)
		}
		if o.Flaky {
			t.Errorf("gate %s: flaky = true on a clean pass", o.Gate)
		}
		if o.DurationSeconds < 0 {
			t.Errorf("gate %s: duration = %v", o.Gate, o.DurationSeconds)
		}
	}
}

func TestRunGatesTestFailureStopsTheSequence(t *testing.T) {
	workspace := writeFixtureModule(t, map[string]string{
		"go.mod":    passingModGo,
		"main.go":   passingMain,
		"f_test.go": "package p\n\nimport \"testing\"\n\nfunc TestF(t *testing.T) {\n\tt.Fatal(\"always fails\")\n}\n",
	})
	policy := harvest.GatePolicy{Gates: []harvest.Gate{
		{Gate: "build", Scope: "full"},
		{Gate: "test", Scope: "full"},
		{Gate: "lint", Scope: "full"}, // must not run: sequence stops at the first non-ok gate
	}}

	outcomes, err := harvest.RunGates(policy, workspace, "", nil)
	if err != nil {
		t.Fatalf("RunGates: %v", err)
	}
	if len(outcomes) != 2 {
		t.Fatalf("outcomes = %+v, want 2 (build ok, test failed, lint never runs)", outcomes)
	}
	if outcomes[0].Status != "ok" {
		t.Errorf("build outcome = %+v, want ok", outcomes[0])
	}
	if outcomes[1].Status != "failed" {
		t.Errorf("test outcome = %+v, want failed", outcomes[1])
	}
	if outcomes[1].Attempt != 1 {
		t.Errorf("test outcome attempt = %d, want 1 (no retries configured)", outcomes[1].Attempt)
	}
}

func TestRunGatesVetFailureIsLintFailed(t *testing.T) {
	workspace := writeFixtureModule(t, map[string]string{
		"go.mod":  passingModGo,
		"main.go": "package p\n\nimport \"fmt\"\n\nfunc F() {\n\tfmt.Printf(\"%d\\n\", \"oops\")\n}\n",
	})
	policy := harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "lint", Scope: "full"}}}

	outcomes, err := harvest.RunGates(policy, workspace, "", nil)
	if err != nil {
		t.Fatalf("RunGates: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Status != "failed" {
		t.Errorf("outcomes = %+v, want one failed lint gate (go vet catches the Printf mismatch)", outcomes)
	}
}

func TestRunGatesUnknownGateErrorsAsToolingFault(t *testing.T) {
	workspace := passingModule(t)
	policy := harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "typo", Scope: "full"}}}

	outcomes, err := harvest.RunGates(policy, workspace, "", nil)
	if err != nil {
		t.Fatalf("RunGates: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Status != "error" {
		t.Fatalf("outcomes = %+v, want one errored gate (unknown gate is a tooling fault, not a failure)", outcomes)
	}
	if outcomes[0].Attempt != 1 {
		t.Errorf("unknown-gate attempt = %d, want 1 (errors are never retried)", outcomes[0].Attempt)
	}
}

func TestRunGatesUnknownGateIsNeverRetried(t *testing.T) {
	workspace := passingModule(t)
	policy := harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "typo", Scope: "full", Retries: 2}}}

	outcomes, err := harvest.RunGates(policy, workspace, "", nil)
	if err != nil {
		t.Fatalf("RunGates: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Status != "error" || outcomes[0].Attempt != 1 {
		t.Errorf("outcomes = %+v, want a single error attempt despite retries: 2 (§6.3: error is never retried)", outcomes)
	}
}

func TestRunGatesNonFullScopeErrorsNamingDevMCP(t *testing.T) {
	workspace := passingModule(t)
	for _, scope := range []string{"affected", "affected_then_full"} {
		policy := harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "build", Scope: scope}}}
		outcomes, err := harvest.RunGates(policy, workspace, "", nil)
		if err != nil {
			t.Fatalf("RunGates: %v", err)
		}
		if len(outcomes) != 1 || outcomes[0].Status != "error" {
			t.Fatalf("scope %q: outcomes = %+v, want one errored gate", scope, outcomes)
		}
		if !containsDevMCP(outcomes[0].Detail) {
			t.Errorf("scope %q: detail = %q, want it to name dev-mcp as the waiting executor", scope, outcomes[0].Detail)
		}
	}
}

func containsDevMCP(s string) bool {
	return len(s) > 0 && (contains(s, "dev-mcp") || contains(s, "dev_mcp"))
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// TestRunGatesRetriesFailedGateAndSurfacesFlaky pins the §6.3 flake
// stance: a fixture whose test reads a counter file so attempt 1 fails
// and attempt 2 passes records ok/flaky:true/attempt:2 — flakiness is
// surfaced, never hidden.
func TestRunGatesRetriesFailedGateAndSurfacesFlaky(t *testing.T) {
	workspace := writeFixtureModule(t, map[string]string{
		"go.mod":  passingModGo,
		"main.go": passingMain,
		"flaky_test.go": `package p

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestFlaky(t *testing.T) {
	data, _ := os.ReadFile("counter.txt")
	n, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	n++
	if err := os.WriteFile("counter.txt", []byte(strconv.Itoa(n)), 0644); err != nil {
		t.Fatal(err)
	}
	if n < 2 {
		t.Fatalf("attempt %d: not yet", n)
	}
}
`,
	})
	policy := harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "test", Scope: "full", Retries: 1}}}

	outcomes, err := harvest.RunGates(policy, workspace, "", nil)
	if err != nil {
		t.Fatalf("RunGates: %v", err)
	}
	if len(outcomes) != 1 {
		t.Fatalf("outcomes = %+v, want 1", outcomes)
	}
	o := outcomes[0]
	if o.Status != "ok" {
		t.Fatalf("outcome = %+v, want ok on the retry pass", o)
	}
	if o.Attempt != 2 {
		t.Errorf("attempt = %d, want 2", o.Attempt)
	}
	if !o.Flaky {
		t.Errorf("flaky = false, want true — a pass-on-retry must surface flakiness, never hide it")
	}
}

func TestRunGatesExhaustsRetriesAndStaysFailed(t *testing.T) {
	workspace := writeFixtureModule(t, map[string]string{
		"go.mod":    passingModGo,
		"main.go":   passingMain,
		"f_test.go": "package p\n\nimport \"testing\"\n\nfunc TestF(t *testing.T) {\n\tt.Fatal(\"always fails\")\n}\n",
	})
	policy := harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "test", Scope: "full", Retries: 2}}}

	outcomes, err := harvest.RunGates(policy, workspace, "", nil)
	if err != nil {
		t.Fatalf("RunGates: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Status != "failed" {
		t.Fatalf("outcomes = %+v, want a single failed gate after exhausting retries", outcomes)
	}
	if outcomes[0].Attempt != 3 {
		t.Errorf("attempt = %d, want 3 (1 initial + 2 retries)", outcomes[0].Attempt)
	}
	if outcomes[0].Flaky {
		t.Errorf("flaky = true, want false — a gate that never passes is not flaky")
	}
}

func TestRunGatesPerGateTimeout(t *testing.T) {
	workspace := writeFixtureModule(t, map[string]string{
		"go.mod":       passingModGo,
		"main.go":      passingMain,
		"slow_test.go": "package p\n\nimport (\n\t\"testing\"\n\t\"time\"\n)\n\nfunc TestSlow(t *testing.T) {\n\ttime.Sleep(2 * time.Second)\n}\n",
	})
	policy := harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "test", Scope: "full", Timeout: "200ms"}}}

	start := time.Now()
	outcomes, err := harvest.RunGates(policy, workspace, "", nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("RunGates: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Status != "error" {
		t.Fatalf("outcomes = %+v, want one errored gate on timeout", outcomes)
	}
	if elapsed > 5*time.Second {
		t.Errorf("RunGates took %v, want it to respect the short per-gate timeout", elapsed)
	}
}
