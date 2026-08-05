package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/bench/harness/casespec"
)

// The corpus used to leave a withheld spec's repository destination implied
// by spelling it withheld_test_paths. That dialect is deliberately refused
// rather than guessed at: the dangerous outcome is not an error, it is a
// silent wrong restore that grades a correct fix as a miss. Every corpus case
// has been normalized to withheld_tests (see corpus_shape_test.go), so this
// guard now exists only to keep a regressed case from being silently
// mis-scored instead of loudly rejected.
func TestRestoreWithheldRejectsTheLegacySpelling(t *testing.T) {
	caseDir := t.TempDir()
	tree := t.TempDir()

	leg := casespec.Leg{
		Cmd:               "go test ./agent/harvest/ -count=1",
		WithheldTestPaths: []string{"ground_truth/grading_tests/trailer_test.go"},
	}
	err := restoreWithheld(caseDir, tree, leg)
	if err == nil {
		t.Fatal("restoreWithheld() accepted the legacy withheld_test_paths spelling")
	}
	if !strings.Contains(err.Error(), "legacy withheld_test_paths spelling") {
		t.Fatalf("error does not name the gap: %v", err)
	}
}

func TestRestoreWithheldCopiesASpecToItsDeclaredDestination(t *testing.T) {
	caseDir := t.TempDir()
	tree := t.TempDir()
	relative := "ci-agent/devmcp/server_test.go"
	source := filepath.Join(caseDir, "ground_truth", "withheld_tests", relative)
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("package devmcp_test\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	leg := casespec.Leg{Cmd: "go test ./devmcp/", WithheldTests: []casespec.WithheldSpec{{
		Source:      filepath.Join("ground_truth", "withheld_tests", relative),
		Destination: relative,
	}}}
	if err := restoreWithheld(caseDir, tree, leg); err != nil {
		t.Fatalf("restoreWithheld() = %v", err)
	}
	restored, err := os.ReadFile(filepath.Join(tree, relative))
	if err != nil {
		t.Fatalf("spec was not restored: %v", err)
	}
	if string(restored) != "package devmcp_test\n" {
		t.Fatalf("restored contents = %q", restored)
	}
}

// A leg whose own cmd restores its spec via $CASE_DIR must not also be restored
// by fixgrade — doing both would be harmless here, but the spec is still
// declared (self_restoring: true) so the destination is known to callers like
// moveAsideAgentTests that need it even though fixgrade itself skips it.
func TestRestoreWithheldDefersToALegThatRestoresItself(t *testing.T) {
	leg := casespec.Leg{
		Cmd: `cp $CASE_DIR/ground_truth/withheld_tests/x_test.go agent/schema/ && go test -C agent/schema ./...`,
		WithheldTests: []casespec.WithheldSpec{{
			Source: "ground_truth/withheld_tests/x_test.go", Destination: "agent/schema/x_test.go", SelfRestoring: true,
		}},
	}
	if err := restoreWithheld(t.TempDir(), t.TempDir(), leg); err != nil {
		t.Fatalf("restoreWithheld() = %v", err)
	}
}

// A case that pins an explicit repository materialize directive must be
// refused before any leg runs, not silently checked out plain — the directive
// exists because a plain `git worktree add` would leave something the case
// withholds reachable (this is exactly what all four negative cases in the
// corpus currently do, which is why none of them are gradable by fixgrade
// yet). The refusal is the behavior under test, and it must name the case and
// carry the directive so an operator can tell this apart from a broken
// harness.
func TestRunRefusesACaseWithAMaterializeDirective(t *testing.T) {
	corpus := t.TempDir()
	caseDir := filepath.Join(corpus, "materialize-guard")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const directive = "git clone --no-hardlinks <src> <dir>\ngit checkout --detach deadbeef\n"
	caseYAML := "schema: benchmark-case/v1\n" +
		"id: materialize-guard\n" +
		"signature:\n" +
		"  inputs:\n" +
		"    repository: repository/v1\n" +
		"pre_state:\n" +
		"  repository:\n" +
		"    repo: jetbridge\n" +
		"    ref: deadbeef\n" +
		"    materialize: |\n" +
		"      git clone --no-hardlinks <src> <dir>\n" +
		"      git checkout --detach deadbeef\n"
	if err := os.WriteFile(filepath.Join(caseDir, "case.yaml"), []byte(caseYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := filepath.Join(t.TempDir(), "empty.patch")
	if err := os.WriteFile(patch, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := run(corpus, "materialize-guard", patch, t.TempDir(), t.TempDir(), false, 4000)
	if err == nil {
		t.Fatal("run() accepted a case with a materialize directive instead of refusing it")
	}
	if !strings.Contains(err.Error(), "materialize-guard") {
		t.Fatalf("error does not name the case: %v", err)
	}
	if !strings.Contains(err.Error(), directive) {
		t.Fatalf("error does not carry the directive: %v", err)
	}
}

// Silently dropping a leg reads as "everything passed". Every skip must be
// reported back to the caller.
func TestRunnableLegsDropsGatedLegsAndSaysWhy(t *testing.T) {
	legs := []casespec.Leg{
		{Cmd: "primary", Role: "primary"},
		{Cmd: "unmarked"},
		{Cmd: "fallback", Role: "fallback-only"},
		{Cmd: "rewrites-history", Destructive: true},
	}
	kept, skipped := runnableLegs(legs)
	if len(kept) != 2 || kept[0].Cmd != "primary" || kept[1].Cmd != "unmarked" {
		t.Fatalf("kept = %v, want the primary and unmarked legs", kept)
	}
	if len(skipped) != 2 {
		t.Fatalf("skipped = %v, want one report per dropped leg", skipped)
	}
	joined := strings.Join(skipped, "\n")
	for _, want := range []string{"fallback-only", "destructive"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("skip reports do not mention %q: %v", want, skipped)
		}
	}
}

// The overlay replaces a package's spec wholesale. A regression test the task
// asked the agent to ADD in that package would then redeclare the restored
// file's helpers and fail to compile — a false failure against a correct fix.
func TestMoveAsideAgentTestsMovesOnlyAddedSpecsInARestoredPackage(t *testing.T) {
	tree := t.TempDir()
	stash := filepath.Join(t.TempDir(), "stash")
	files := []string{
		"ci-agent/devmcp/server_test.go", // the withheld spec itself: keep
		"ci-agent/devmcp/panic_test.go",  // agent-authored, same package: move
		"ci-agent/devmcp/server.go",      // production code: keep
		"ci-agent/runner/runner_test.go", // different package: keep
	}
	for _, relative := range files {
		full := filepath.Join(tree, relative)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	moved, err := moveAsideAgentTests(tree, stash,
		[]string{"ci-agent/devmcp/server_test.go"}, files)
	if err != nil {
		t.Fatalf("moveAsideAgentTests() = %v", err)
	}
	if len(moved) != 1 || moved[0] != "ci-agent/devmcp/panic_test.go" {
		t.Fatalf("moved = %v, want only the agent-added spec in the restored package", moved)
	}
	if _, err := os.Stat(filepath.Join(tree, "ci-agent/devmcp/panic_test.go")); !os.IsNotExist(err) {
		t.Fatal("the agent-added spec is still in the tree")
	}
	for _, kept := range []string{"ci-agent/devmcp/server_test.go", "ci-agent/runner/runner_test.go", "ci-agent/devmcp/server.go"} {
		if _, err := os.Stat(filepath.Join(tree, kept)); err != nil {
			t.Fatalf("%s should have been left in place: %v", kept, err)
		}
	}
}

// An over-correction guard is a pass_to_pass leg that needs the withheld spec
// restored (fix-jb-003, fix-ld-002). Running it against a tree that never got
// the spec does not error, it silently reports the wrong thing.
func TestKeepLegRestoresItsOwnWithheldSpec(t *testing.T) {
	caseDir := t.TempDir()
	tree := t.TempDir()
	relative := "agent/harvest/trailer_test.go"
	source := filepath.Join(caseDir, "ground_truth", "withheld_tests", relative)
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("package harvest\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	leg := casespec.Leg{Cmd: "go test ./agent/harvest/", WithheldTests: []casespec.WithheldSpec{{
		Source:      filepath.Join("ground_truth", "withheld_tests", relative),
		Destination: relative,
	}}}
	if err := restoreWithheld(caseDir, tree, leg); err != nil {
		t.Fatalf("restoreWithheld() = %v", err)
	}
	if _, err := os.Stat(filepath.Join(tree, relative)); err != nil {
		t.Fatalf("keep-leg spec was not restored: %v", err)
	}
}

func TestVerdictForANegativeCaseIsNotAFailure(t *testing.T) {
	// A negative case has no fail_to_pass leg because declining is correct.
	// Its mechanical legs can only establish that nothing was broken; whether
	// declining was right is a judge call fixgrade does not make.
	legs := []legResult{{Name: "keep", OK: true}}
	if got := verdictForLegs(legs, false); got != "kept-green-judge-required" {
		t.Fatalf("verdict = %q, want kept-green-judge-required", got)
	}
	broken := []legResult{{Name: "keep", OK: false}}
	if got := verdictForLegs(broken, false); got != "fail" {
		t.Fatalf("verdict = %q, want fail", got)
	}
}

func TestRestoreWithheldUsesTheDeclaredDestination(t *testing.T) {
	caseDir := t.TempDir()
	tree := t.TempDir()
	source := filepath.Join(caseDir, "ground_truth", "grading_tests", "trailer_test.go")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("package harvest\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	leg := casespec.Leg{
		Cmd: "go test ./agent/harvest/",
		WithheldTests: []casespec.WithheldSpec{{
			Source:      "ground_truth/grading_tests/trailer_test.go",
			Destination: "agent/harvest/trailer_test.go",
		}},
	}
	if err := restoreWithheld(caseDir, tree, leg); err != nil {
		t.Fatalf("restoreWithheld() = %v", err)
	}
	if _, err := os.Stat(filepath.Join(tree, "agent/harvest/trailer_test.go")); err != nil {
		t.Fatalf("spec was not restored at its declared destination: %v", err)
	}
}

func TestRestoreWithheldSkipsASelfRestoringSpec(t *testing.T) {
	leg := casespec.Leg{
		Cmd: `cp $CASE_DIR/ground_truth/withheld_tests/x_test.go agent/schema/`,
		WithheldTests: []casespec.WithheldSpec{{
			Source: "ground_truth/withheld_tests/x_test.go", Destination: "agent/schema/x_test.go", SelfRestoring: true,
		}},
	}
	if err := restoreWithheld(t.TempDir(), t.TempDir(), leg); err != nil {
		t.Fatalf("restoreWithheld() = %v", err)
	}
}

func TestVerdictTreatsAPassingRedLegAsAnInvalidRun(t *testing.T) {
	for _, testCase := range []struct {
		name string
		legs []legResult
		want string
	}{
		{"all legs met their expectation", []legResult{
			{Name: "red", OK: true}, {Name: "keep", OK: true}, {Name: "green", OK: true}}, "pass"},
		{"the withheld spec still fails", []legResult{
			{Name: "red", OK: true}, {Name: "keep", OK: true}, {Name: "green", OK: false}}, "fail"},
		// A red leg that succeeded means the case never distinguished a fix from
		// nothing, so no verdict about the agent can be drawn from the other legs.
		{"pre-state did not fail", []legResult{
			{Name: "red", OK: false}, {Name: "keep", OK: true}, {Name: "green", OK: true}},
			"invalid-red-leg-passed"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := verdict(testCase.legs); got != testCase.want {
				t.Fatalf("verdict() = %q, want %q", got, testCase.want)
			}
		})
	}
}
