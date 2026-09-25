package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

var protocolBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "jetbridge-adapter-contract.")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	protocolBinary = filepath.Join(dir, "adapter")
	build := exec.Command("go", "build", "-o", protocolBinary, ".")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		os.RemoveAll(dir)
		os.Exit(1)
	}
	// Reuse this protocol suite against an exec-preserving integration
	// launcher rather than copying its refusal, selection and drain cases.
	if launcher := os.Getenv("BRINE_PROTOCOL_LAUNCHER"); launcher != "" {
		if !filepath.IsAbs(launcher) {
			fmt.Fprintln(os.Stderr, "BRINE_PROTOCOL_LAUNCHER must be an absolute path")
			os.RemoveAll(dir)
			os.Exit(1)
		}
		if err := os.Setenv("BRINE_ADAPTER_BINARY", protocolBinary); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.RemoveAll(dir)
			os.Exit(1)
		}
		// Keep every launcher guard invocation, but compile its test binary
		// once from the same source as this suite's fresh adapter. Results and
		// feature-file reads are never cached; the binary dies with this suite.
		guards := filepath.Join(dir, "guards.test")
		guardBuild := exec.Command("go", "test", "-c", "-o", guards, "../../steps")
		guardBuild.Stdout, guardBuild.Stderr = os.Stdout, os.Stderr
		if err := guardBuild.Run(); err != nil {
			os.RemoveAll(dir)
			os.Exit(1)
		}
		if err := os.Setenv("BRINE_GUARD_TEST_BINARY", guards); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.RemoveAll(dir)
			os.Exit(1)
		}
		protocolBinary = launcher
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// These tests invoke the real adapter and CLI. Unknown steps are protocol
// specimens, not substitutes for JetBridge behavior or production coverage.
func TestAdapterRefusesMissingSelection(t *testing.T) {
	for _, verb := range []string{"run", "check"} {
		for _, args := range [][]string{{verb}, {verb, "--document"}, {verb, "--features", "ignored.feature"}} {
			t.Run(strings.Join(args, " "), func(t *testing.T) {
				out, stderr, code := invoke(t, protocolBinary, args...)
				if code != 2 || len(out) != 0 || !strings.Contains(stderr, "--document") {
					t.Fatalf("wanted refusal with no stdout: code=%d stdout=%s stderr=%s", code, out, stderr)
				}
			})
		}
	}
}

func TestRegistryDocumentUsesProtocolStdout(t *testing.T) {
	out, stderr, code := invoke(t, protocolBinary, "catalog", "--document")
	if code != 0 || stderr != "" {
		t.Fatalf("catalog failed: %d %s", code, stderr)
	}
	var doc struct {
		Kind         string
		Capabilities struct{ ExecutionDocument int }
		Steps        []json.RawMessage
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Kind != "registry" || doc.Capabilities.ExecutionDocument != 1 || len(doc.Steps) == 0 {
		t.Fatalf("missing registry/capability/vocabulary: %s", out)
	}
}

func TestDocumentOwnsSelection(t *testing.T) {
	for _, tc := range []struct {
		name        string
		flags       []string
		wantNames   []string
		wantSkipped bool
	}{
		{"tag exclusion announced", []string{"--exclude-tags", "@skip"}, []string{"selected", "excluded"}, true},
		{"body line omits sibling", []string{"--line", "3"}, []string{"selected"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, doc := selectionDocument(t, "run", tc.flags...)
			// Delete the source after core reads it: the adapter must execute
			// the embedded text, never reload or filter a second time.
			if err := os.Remove(filepath.Join(dir, "subject.feature")); err != nil {
				t.Fatal(err)
			}
			path := writeJSON(t, dir, doc)
			out, stderr, code := invoke(t, protocolBinary, "run", "--document", path,
				"--features", "/does/not/exist", "--tags", "@nothing")
			// Adapter exit codes count failures, not undefined steps. The CLI
			// enforces the undefined budget; assert the actual wire outcome here.
			if code != 0 {
				t.Fatalf("unexpected adapter exit %d: %s %s", code, out, stderr)
			}
			events := decodeEvents(t, out)
			var names []string
			skipped := false
			steps := 0
			undefined := 0
			for _, event := range events {
				switch event["type"] {
				case "scenario_start":
					names = append(names, event["name"].(string))
				case "scenario_end":
					if event["status"] == "skipped" {
						skipped = true
					}
				case "step_start":
					steps++
				case "step_end":
					if event["status"] == "undefined" {
						undefined++
					}
				}
			}
			if strings.Join(names, "|") != strings.Join(tc.wantNames, "|") || skipped != tc.wantSkipped || steps != 1 || undefined != 1 {
				t.Fatalf("selection mismatch names=%v skipped=%v steps=%d undefined=%d events=%s", names, skipped, steps, undefined, out)
			}
		})
	}
}

func TestDocumentCheckEchoesRoster(t *testing.T) {
	dir, doc := selectionDocument(t, "check")
	roster := []any{"the roster belongs to core, not the adapter"}
	doc["projections"].(map[string]any)["checkStartRoster"] = roster
	if err := os.Remove(filepath.Join(dir, "subject.feature")); err != nil {
		t.Fatal(err)
	}
	out, stderr, code := invoke(t, protocolBinary, "check", "--document", writeJSON(t, dir, doc))
	if code != 1 {
		t.Fatalf("wanted invalid check: %d %s %s", code, out, stderr)
	}
	events := decodeEvents(t, out)
	checks := 0
	for _, event := range events {
		if event["type"] == "scenario_check" {
			checks++
		}
	}
	got, _ := json.Marshal(events[0]["features"])
	want, _ := json.Marshal(roster)
	if events[0]["type"] != "check_start" || !bytes.Equal(got, want) || checks != 2 {
		t.Fatalf("missing check population/roster: %s", out)
	}
}

func TestDocumentRefusals(t *testing.T) {
	for _, tc := range []struct{ fault, message string }{
		{"directive", "invented-directive"},
		{"kind", "registry"},
		{"version", "future"},
		{"json", "not JSON"},
	} {
		t.Run(tc.fault, func(t *testing.T) {
			dir, doc := selectionDocument(t, "run")
			switch tc.fault {
			case "directive":
				feature := doc["features"].([]any)[0].(map[string]any)
				feature["scenarios"].([]any)[0].(map[string]any)["directive"] = "invented-directive"
			case "kind":
				doc["kind"] = "registry"
			case "version":
				doc["brineDocument"] = 999
			}
			path := writeJSON(t, dir, doc)
			if tc.fault == "json" {
				if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			out, stderr, code := invoke(t, protocolBinary, "run", "--document", path)
			if code != 2 || len(out) != 0 || !strings.Contains(stderr, tc.message) {
				t.Fatalf("wanted named %s refusal: %d %s %.1000s", tc.fault, code, out, stderr)
			}
		})
	}
}

func selectionDocument(t *testing.T, verb string, flags ...string) (string, map[string]any) {
	return documentForFeature(t, verb,
		"Feature: document ownership\n  Scenario: selected\n    Given an undefined protocol specimen\n  @skip\n  Scenario: excluded\n    Given another undefined protocol specimen\n",
		flags...)
}

func documentForFeature(t *testing.T, verb, feature string, flags ...string) (string, map[string]any) {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		".brine":          fmt.Sprintf("runner:\n  name: jetbridge\n  binary: %q\n  contract: 5\ncontract: 5\nfeatures: '*.feature'\n", protocolBinary),
		"subject.feature": feature,
	}
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	args := append([]string{"document", dir, "--json", "--verb", verb}, flags...)
	out, stderr, code := invoke(t, "brine", args...)
	if code != 0 {
		t.Fatalf("document producer: %d %s %s", code, out, stderr)
	}
	var docs []map[string]any
	if err := json.Unmarshal(out, &docs); err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("wanted one manifest document, got %d", len(docs))
	}
	return dir, docs[0]
}

func TestHoldPreservesThenDrainsRealResources(t *testing.T) {
	// The registrar owns a real API pod and database. Its recorded disposer
	// performs a UID-guarded API deletion; no substitute registry is installed.
	dir, doc := documentForFeature(t, "run",
		"Feature: held registration\n  Scenario: held worker\n"+
			"    Given a Kubernetes worker registrar for namespace \"held-worker\"\n"+
			"    And 1 pods belonging to \"this worker\" exist\n"+
			"    When the worker registers itself\n"+
			"    Then it reports 1 active containers\n")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := drainingCommand(ctx, protocolBinary, "run", "--document", writeJSON(t, dir, doc), "--hold")
	scratch := ownedResourceScratch(t)
	cmd.Env = append(os.Environ(), "TMPDIR="+scratch)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	held, passed, drained := false, false, false
	var controlPlaneDirs []string
	var protocolErr error
	var stdoutLines []string
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		stdoutLines = append(stdoutLines, scanner.Text())
		var event map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			protocolErr = err
			_ = cmd.Process.Signal(syscall.SIGTERM)
			continue
		}
		switch event["type"] {
		case "scenario_end":
			passed = event["status"] == "passed"
		case "hold_ready":
			if held || !passed || drained {
				protocolErr = fmt.Errorf("hold must follow a passing scenario without teardown: %v", event)
			}
			resources, _ := json.Marshal(event["resources"])
			if !bytes.Contains(resources, []byte("jetbridge-db")) {
				protocolErr = fmt.Errorf("held run omitted its live database: %s", resources)
			}
			held = true
			controlPlaneDirs, err = filepath.Glob(filepath.Join(scratch, "k8s_test_framework_*"))
			if err != nil || len(controlPlaneDirs) == 0 {
				protocolErr = fmt.Errorf("held control plane has no owned working directories: %v", err)
			}
			// Exercise the same CommandContext cancellation used by invoke.
			// Its SIGTERM must drain, not leave envtest children behind.
			cancel()
		case "recorder_drain_started":
			if !held || event["disposer_count"] != float64(1) {
				protocolErr = fmt.Errorf("pod disposer must remain registered until release: %v", event)
			}
		case "recorder_drain_completed":
			drained = event["disposers_drained"] == float64(1) && event["partial"] == false
		}
	}
	waitErr := cmd.Wait()
	exit, ok := waitErr.(*exec.ExitError)
	if ctx.Err() != context.Canceled || scanner.Err() != nil || protocolErr != nil || !held || !drained || !ok || exit.ExitCode() != 143 {
		// The adapter's stderr is dominated by postgres initdb chatter, so a
		// head-truncated dump buries the real error. Print the tail instead,
		// plus every line that looks diagnostic, so the actual factory error
		// (e.g. missing otelcol/auth binaries) is visible.
		const tailBytes = 4096
		full := stderr.String()
		tail := full
		if len(tail) > tailBytes {
			tail = tail[len(tail)-tailBytes:]
		}
		var diagnostic []string
		for _, line := range append(strings.Split(full, "\n"), stdoutLines...) {
			lower := strings.ToLower(line)
			if strings.Contains(lower, "error") || strings.Contains(lower, "failed") || strings.Contains(lower, "scenario_end") {
				diagnostic = append(diagnostic, line)
			}
		}
		t.Fatalf("hold/drain failed: held=%v drained=%v wait=%v context=%v scan=%v protocol=%v stderr(tail %dB)=%s\ndiagnostic lines:\n%s",
			held, drained, waitErr, ctx.Err(), scanner.Err(), protocolErr, tailBytes, tail, strings.Join(diagnostic, "\n"))
	}
	for _, path := range controlPlaneDirs {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("cancelled adapter retained owned control-plane directory %s: %v", path, err)
		}
	}
	t.Logf("context cancellation drained the real registrar and removed its control-plane directories")
}

func writeJSON(t *testing.T, dir string, doc map[string]any) string {
	t.Helper()
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "execution.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func decodeEvents(t *testing.T, out []byte) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(out), []byte("\n")) {
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("invalid event %s: %v", line, err)
		}
		events = append(events, event)
	}
	if len(events) == 0 {
		t.Fatal("empty event stream")
	}
	return events
}

func invoke(t *testing.T, binary string, args ...string) ([]byte, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := drainingCommand(ctx, binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("command timed out: %s %v; cleanup wait=%v; stdout=%.2000s stderr=%.2000s", binary, args, err, stdout.Bytes(), stderr.String())
	}
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return stdout.Bytes(), stderr.String(), exit.ExitCode()
		}
		t.Fatalf("start %s: %v", binary, err)
	}
	return stdout.Bytes(), stderr.String(), 0
}

// Share the real SIGTERM drain path between ordinary command deadlines and
// the held-resource cancellation check. The execution deadline remains a
// failure; this grace only permits resource cleanup before forced exit.
func drainingCommand(ctx context.Context, binary string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 45 * time.Second
	return cmd
}

func ownedResourceScratch(t *testing.T) string {
	t.Helper()
	scratch, err := os.MkdirTemp("", "brine-held-resource-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(scratch); err != nil {
			t.Error(err)
		}
	})
	// PostgreSQL drops privileges and needs to traverse this owned parent.
	if err := os.Chmod(scratch, 0755); err != nil {
		t.Fatal(err)
	}
	return scratch
}
