package harvest_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/concourse/concourse/agent/harvest"
)

// gitFixture builds a two-commit repo: baseFiles are committed first
// (returned baseSHA points at that commit), then headFiles are written and
// committed on top so ChangedPaths(dir, baseSHA) == the headFiles set.
func gitFixture(t *testing.T, baseFiles, headFiles map[string]string) (dir, baseSHA string) {
	t.Helper()
	dir = t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	writeAll := func(files map[string]string) {
		for name, content := range files {
			full := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
	run("init", "-q")
	writeAll(baseFiles)
	run("add", "-A")
	run("commit", "-q", "-m", "base")
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	baseSHA = string(out[:len(out)-1]) // strip trailing newline
	writeAll(headFiles)
	run("add", "-A")
	run("commit", "-q", "-m", "head")
	return dir, baseSHA
}

func TestElmGateNotApplicableWhenElmUntouched(t *testing.T) {
	dir, base := gitFixture(t,
		map[string]string{"README.md": "hi\n"},
		map[string]string{"README.md": "hi there\n"}, // no web/elm change
	)
	policy := harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "elm-build", Scope: "full"}}}

	outcomes, err := harvest.RunGates(policy, dir, base, nil)
	if err != nil {
		t.Fatalf("RunGates: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Status != "ok" {
		t.Fatalf("outcomes = %+v, want a single ok gate (elm untouched => not applicable)", outcomes)
	}
	if !contains(outcomes[0].Detail, "not applicable") {
		t.Errorf("detail = %q, want it to say the gate was not applicable", outcomes[0].Detail)
	}
}

// TestElmGateNotApplicableForNonBundleElmPaths locks in the narrowed
// applicability (WF-2 review fix): edits under web/elm that do NOT feed the
// compiled bundle — tests, benchmarks, elm dotfiles — must NOT demand a bundle
// regeneration. A correct rebuild of such a change produces a byte-identical
// bundle, so requiring elm.min.js in the diff would be an unsatisfiable
// false-failure. Each of these must read as "not applicable" (ok), and elm is
// never invoked, so no stub is needed.
func TestElmGateNotApplicableForNonBundleElmPaths(t *testing.T) {
	for _, path := range []string{
		"web/elm/tests/MainTests.elm",
		"web/elm/benchmarks/Bench.elm",
		"web/elm/.gitignore",
	} {
		t.Run(path, func(t *testing.T) {
			dir, base := gitFixture(t,
				map[string]string{path: "before\n"},
				map[string]string{path: "after\n"}, // only a non-src/non-elm.json web/elm path
			)
			policy := harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "elm-build", Scope: "full"}}}

			outcomes, err := harvest.RunGates(policy, dir, base, nil)
			if err != nil {
				t.Fatalf("RunGates: %v", err)
			}
			if len(outcomes) != 1 || outcomes[0].Status != "ok" {
				t.Fatalf("outcomes = %+v, want a single ok gate (%s does not feed the bundle => not applicable)", outcomes, path)
			}
			if !contains(outcomes[0].Detail, "not applicable") {
				t.Errorf("detail = %q, want it to say the gate was not applicable", outcomes[0].Detail)
			}
		})
	}
}

// TestElmGateAppliesToElmJson pins that web/elm/elm.json (the dependency
// manifest, which DOES change the compiled bundle) is a bundle-feeding path:
// changing it without regenerating the bundle FAILS the stale-bundle guard.
func TestElmGateAppliesToElmJson(t *testing.T) {
	stubElm(t, 0) // compiles fine
	dir, baseSHA := gitFixture(t,
		map[string]string{
			"web/elm/src/Main.elm":  "module Main exposing (..)\nx = 1\n",
			"web/elm/elm.json":      "{\"deps\":1}\n",
			"web/public/elm.min.js": "old\n",
		},
		map[string]string{"web/elm/elm.json": "{\"deps\":2}\n"}, // dep bump, bundle NOT regenerated
	)
	policy := harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "elm-build", Scope: "full"}}}

	outcomes, err := harvest.RunGates(policy, dir, baseSHA, nil)
	if err != nil {
		t.Fatalf("RunGates: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Status != "failed" {
		t.Fatalf("outcomes = %+v, want failed (elm.json changed, bundle stale)", outcomes)
	}
	if !contains(outcomes[0].Detail, "stale-bundle guard") {
		t.Errorf("detail = %q, want it to name the stale-bundle guard", outcomes[0].Detail)
	}
}

// stubElm writes an executable that exits with the given code, and points
// HARVEST_ELM_CLI at it so runElmBuildGate's compile step is deterministic
// without a real Elm toolchain (mirrors the judge's HARVEST_JUDGE_CLI seam).
func stubElm(t *testing.T, exitCode int) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "elm-stub")
	script := "#!/bin/sh\necho 'stub elm'\nexit " + itoa(exitCode) + "\n"
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HARVEST_ELM_CLI", path)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	return string(rune('0' + n)) // single-digit exit codes only
}

// elmChange is the minimal web/elm edit that trips the gate's applicability.
func elmChange() (base, head map[string]string, withBundle map[string]string) {
	base = map[string]string{
		"web/elm/src/Main.elm":  "module Main exposing (..)\nx = 1\n",
		"web/public/elm.min.js": "old\n",
	}
	head = map[string]string{
		"web/elm/src/Main.elm": "module Main exposing (..)\nx = 2\n", // elm changed, bundle NOT
	}
	withBundle = map[string]string{
		"web/elm/src/Main.elm":  "module Main exposing (..)\nx = 2\n",
		"web/public/elm.min.js": "new\n", // elm AND bundle changed
	}
	return
}

func TestElmGateFailsWhenBundleNotRegenerated(t *testing.T) {
	stubElm(t, 0) // compiles fine
	base, head, _ := elmChange()
	dir, baseSHA := gitFixture(t, base, head)
	policy := harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "elm-build", Scope: "full"}}}

	outcomes, err := harvest.RunGates(policy, dir, baseSHA, nil)
	if err != nil {
		t.Fatalf("RunGates: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Status != "failed" {
		t.Fatalf("outcomes = %+v, want failed (elm changed, bundle stale)", outcomes)
	}
	if !contains(outcomes[0].Detail, "stale-bundle guard") {
		t.Errorf("detail = %q, want it to name the stale-bundle guard", outcomes[0].Detail)
	}
}

func TestElmGatePassesWhenBundleRegenerated(t *testing.T) {
	stubElm(t, 0)
	base, _, withBundle := elmChange()
	dir, baseSHA := gitFixture(t, base, withBundle)
	policy := harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "elm-build", Scope: "full"}}}

	outcomes, err := harvest.RunGates(policy, dir, baseSHA, nil)
	if err != nil {
		t.Fatalf("RunGates: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Status != "ok" {
		t.Fatalf("outcomes = %+v, want ok (elm changed AND bundle regenerated)", outcomes)
	}
}

func TestElmGateFailsWhenSourceDoesNotCompile(t *testing.T) {
	stubElm(t, 1) // compile error
	base, _, withBundle := elmChange()
	dir, baseSHA := gitFixture(t, base, withBundle) // bundle present, but elm make fails
	policy := harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "elm-build", Scope: "full"}}}

	outcomes, err := harvest.RunGates(policy, dir, baseSHA, nil)
	if err != nil {
		t.Fatalf("RunGates: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Status != "failed" {
		t.Fatalf("outcomes = %+v, want failed (compile error)", outcomes)
	}
	if !contains(outcomes[0].Detail, "elm make --optimize failed") {
		t.Errorf("detail = %q, want it to name the compile failure", outcomes[0].Detail)
	}
}

func TestElmGateErrorsWithoutMergeBase(t *testing.T) {
	stubElm(t, 0)
	base, _, withBundle := elmChange()
	dir, _ := gitFixture(t, base, withBundle)
	policy := harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "elm-build", Scope: "full"}}}

	outcomes, err := harvest.RunGates(policy, dir, "", nil) // empty base => fail-closed
	if err != nil {
		t.Fatalf("RunGates: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Status != "error" {
		t.Fatalf("outcomes = %+v, want error (no merge-base, fail-closed)", outcomes)
	}
	if !contains(outcomes[0].Detail, "fail-closed") {
		t.Errorf("detail = %q, want it to say fail-closed", outcomes[0].Detail)
	}
}

func TestElmGateErrorsWhenElmBinaryMissing(t *testing.T) {
	t.Setenv("HARVEST_ELM_CLI", "/nonexistent/elm-binary")
	base, _, withBundle := elmChange()
	dir, baseSHA := gitFixture(t, base, withBundle)
	policy := harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "elm-build", Scope: "full"}}}

	outcomes, err := harvest.RunGates(policy, dir, baseSHA, nil)
	if err != nil {
		t.Fatalf("RunGates: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Status != "error" {
		t.Fatalf("outcomes = %+v, want error (elm toolchain missing is a tooling fault, not a failure)", outcomes)
	}
	if !contains(outcomes[0].Detail, "could not run elm") {
		t.Errorf("detail = %q, want it to name the missing toolchain", outcomes[0].Detail)
	}
}

// TestElmGateRequiresFullScope pins that elm-build obeys the same scope
// guard as every other v0.5 gate: only "full" is enforceable in-pod.
func TestElmGateRequiresFullScope(t *testing.T) {
	base, _, withBundle := elmChange()
	dir, baseSHA := gitFixture(t, base, withBundle)
	policy := harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "elm-build", Scope: "affected"}}}

	outcomes, err := harvest.RunGates(policy, dir, baseSHA, nil)
	if err != nil {
		t.Fatalf("RunGates: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Status != "error" {
		t.Fatalf("outcomes = %+v, want error (non-full scope is unenforceable)", outcomes)
	}
}
