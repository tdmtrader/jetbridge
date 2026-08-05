// Command fixgrade mechanically grades one small-fix bench case from the patch
// a node produced.
//
// It answers the only question a small-fix node's output can be graded on
// objectively: does the delivered change make the withheld spec go from red to
// green without breaking what already worked?
//
// An ordinary case runs three legs, in this order:
//
//	red    the case's fail_to_pass command at pre-state, with the withheld
//	       tests restored and NO patch applied. It must fail. A red leg that
//	       passes means the case, the toolchain, or the materialization is
//	       broken — never that the node did well.
//	keep   the case's pass_to_pass commands on the agent's tree as delivered.
//	       A leg that declares its own withheld specs (an over-correction
//	       guard) runs against a private copy of the delivered tree with those
//	       specs restored into it; a leg that declares none runs against the
//	       delivered tree exactly as the agent left it.
//	green  the fail_to_pass command on the agent's tree with the withheld
//	       tests restored over it.
//
// A negative case — one whose correct answer is to decline the change, so it
// declares no fail_to_pass leg at all — runs only the keep legs: there is no
// red-to-green test to run. Its verdict is "kept-green-judge-required" rather
// than "pass", naming that the mechanical legs only proved nothing regressed;
// whether declining was the right call is a judge call this command does not
// make.
//
// Every leg is scored on the process EXIT CODE, never on parsed spec counts:
// the strongest pre-state signals in this corpus are crash-shaped (fix-jb-001
// dies with a raw `panic: tool exploded` and the remaining specs never report).
//
// The green leg moves agent-authored *_test.go files in a restored package
// aside before running. That is not leniency — several cases ask the agent to
// add a regression spec to the very file the overlay replaces, so a correct fix
// would otherwise fail to compile on a redeclared helper. The moved files are
// listed in the report so the agent's own test can be scored separately, which
// is a rubric judgment this command deliberately does not make.
//
// A case whose repository pre-state pins an explicit materialize directive is
// refused outright, before any leg runs: a plain checkout would not honor
// whatever that directive exists to prevent, so this command does not attempt
// one. All four negative cases currently in the corpus pin such a directive,
// which means the negative-case verdict above is exercised by this package's
// tests but not yet by any real corpus case.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/concourse/concourse/bench/harness/casespec"
)

type legResult struct {
	Name     string `json:"name"`
	Cmd      string `json:"cmd"`
	ExitCode int    `json:"exit_code"`
	Expected string `json:"expected"`
	OK       bool   `json:"ok"`
	Duration string `json:"duration"`
	Output   string `json:"output,omitempty"`
}

type report struct {
	Case         string      `json:"case"`
	Verdict      string      `json:"verdict"`
	PatchApplied bool        `json:"patch_applied"`
	ChangedFiles []string    `json:"changed_files"`
	MovedAside   []string    `json:"moved_aside,omitempty"`
	Legs         []legResult `json:"legs"`
	Caveats      []string    `json:"caveats,omitempty"`
	Notes        []string    `json:"notes,omitempty"`
}

func main() {
	var (
		corpus     = flag.String("corpus", "bench/corpus", "corpus directory")
		caseID     = flag.String("case", "", "case id, e.g. fix-jb-001")
		patchPath  = flag.String("patch", "", "the patch the node produced")
		sourceRepo = flag.String("source-repo", ".", "git repository holding the case's pinned pre-state commit")
		workDir    = flag.String("work", "", "scratch directory (default: a fresh temp dir)")
		keepWork   = flag.Bool("keep", false, "keep the scratch directory")
		maxOutput  = flag.Int("max-output", 4000, "bytes of each leg's output to retain")
		jsonOut    = flag.Bool("json", false, "emit the report as JSON")
	)
	flag.Parse()

	if *caseID == "" || *patchPath == "" {
		fmt.Fprintln(os.Stderr, "fixgrade: --case and --patch are required")
		os.Exit(2)
	}
	result, err := run(*corpus, *caseID, *patchPath, *sourceRepo, *workDir, *keepWork, *maxOutput)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fixgrade: %v\n", err)
		os.Exit(2)
	}
	if *jsonOut {
		encoded, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(encoded))
	} else {
		printReport(result)
	}
	// "kept-green-judge-required" is a negative case's clean outcome: its
	// mechanical legs only prove nothing regressed, which is not a failure —
	// whether declining was the right call is a judge call this command does
	// not make. Any other non-"pass" verdict (fail, invalid-red-leg-passed,
	// patch-rejected, or a future verdict this switch does not yet know about)
	// means something the mechanical legs COULD check did not check out, so it
	// fails closed to a nonzero exit.
	switch result.Verdict {
	case "pass", "kept-green-judge-required":
	default:
		os.Exit(1)
	}
}

func run(corpus, caseID, patchPath, sourceRepo, workDir string, keepWork bool, maxOutput int) (*report, error) {
	spec, err := casespec.Load(corpus, caseID)
	if err != nil {
		return nil, err
	}
	grading, err := casespec.LoadGrading(corpus, caseID)
	if err != nil {
		return nil, err
	}
	// A case with no fail_to_pass leg is not automatically ungradable: it may be
	// a negative case, where declining the change is the correct answer and
	// there is deliberately no red-to-green test. That is handled below, once
	// runnableLegs has told us whether any fail_to_pass leg actually runs.
	repositoryPort, found := spec.Ports["repository"]
	if !found || !repositoryPort.SourceTree() {
		return nil, fmt.Errorf("case %s has no pinned repository pre-state", caseID)
	}
	if repositoryPort.Materialize != "" {
		// This is not this command failing to cope with an odd case — it is the
		// case's own safety property. fixgrade only knows how to materialize
		// pre-state with a plain `git worktree add`, which checks out the ref but
		// leaves the source repository's other refs and reflog intact; a
		// directive exists specifically when that would expose something the
		// case withholds (neg-jb-001's answer key sits three commits downstream
		// on a branch a plain checkout would leave reachable). Grading a case
		// like that correctly requires running its exact procedure, which this
		// command does not implement, so it refuses rather than guessing.
		return nil, fmt.Errorf(
			"case %s pins an explicit repository materialize directive: a plain checkout would expose history "+
				"this case deliberately withholds, and grading it correctly requires running that materialize "+
				"procedure, which this command does not implement.\ndirective:\n%s",
			caseID, repositoryPort.Materialize)
	}
	absolutePatch, err := filepath.Abs(patchPath)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(absolutePatch); err != nil {
		return nil, fmt.Errorf("patch: %w", err)
	}

	autoWorkDir := workDir == ""
	if autoWorkDir {
		workDir, err = os.MkdirTemp("", "fixgrade-"+caseID+"-")
		if err != nil {
			return nil, err
		}
	} else if err := os.MkdirAll(workDir, 0o755); err != nil {
		return nil, err
	}
	// The scratch trees are worktrees of the source repository, so removing the
	// directories is not enough by itself: the source keeps administrative
	// entries for them, and `git worktree prune` only drops an entry once it can
	// see that its working directory is actually gone.
	//
	// Registering this defer BEFORE the os.RemoveAll defer below is load-bearing,
	// not incidental — do not "tidy" the order. Go runs deferred calls LIFO
	// (last registered, first run), so registering prune FIRST here means it
	// runs LAST, after RemoveAll has already deleted the directories, and it can
	// see them missing and actually clean up. Registering it the other way
	// around — as a prior version of this file did — makes prune run first,
	// while the directories still exist, so it finds nothing to prune; RemoveAll
	// then deletes them anyway, leaving orphaned entries in $GIT_DIR/worktrees
	// for some later, unrelated invocation to discover and clean up instead of
	// this one.
	//
	// Skipped entirely when --keep is set: the scratch directories deliberately
	// survive (see the RemoveAll guard below), so their worktrees are still
	// valid on disk, and there is nothing this invocation should prune.
	if !keepWork {
		defer func() { _, _ = gitOutput(sourceRepo, "worktree", "prune") }()
	}
	if autoWorkDir && !keepWork {
		defer os.RemoveAll(workDir)
	}

	result := &report{Case: caseID, Caveats: grading.Comments}
	if grading.Protocol != "" {
		result.Caveats = append(result.Caveats, grading.Protocol)
	}
	if grading.PostgresRequired {
		result.Notes = append(result.Notes, "case declares postgres_required: legs will fail without a running PostgreSQL")
	}
	if grading.ClusterRequired {
		result.Notes = append(result.Notes, "case declares cluster_required: legs will fail without a cluster")
	}

	deliveredTree := filepath.Join(workDir, "delivered")
	if err := checkout(sourceRepo, repositoryPort.Ref, deliveredTree); err != nil {
		return nil, fmt.Errorf("materialize pre-state: %w", err)
	}
	pristineTree := filepath.Join(workDir, "pristine")
	if err := checkout(sourceRepo, repositoryPort.Ref, pristineTree); err != nil {
		return nil, fmt.Errorf("materialize pre-state: %w", err)
	}

	caseDir, err := filepath.Abs(filepath.Join(corpus, caseID))
	if err != nil {
		return nil, err
	}
	failLegs, skippedFail := runnableLegs(grading.FailToPass)
	passLegs, skippedPass := runnableLegs(grading.PassToPass)
	result.Notes = append(result.Notes, append(skippedFail, skippedPass...)...)
	negative := len(failLegs) == 0
	if negative {
		result.Notes = append(result.Notes,
			"case declares no fail_to_pass leg: declining is a correct answer here, so the "+
				"mechanical legs can only show nothing regressed. Score the disposition against "+
				"the case's rubric.")
	}

	// red: the pristine pre-state with the withheld spec restored must fail.
	// A negative case has no fail_to_pass leg, so there is no red leg to run.
	if !negative {
		if err := restoreWithheld(caseDir, pristineTree, failLegs[0]); err != nil {
			return nil, fmt.Errorf("restore withheld tests into pre-state: %w", err)
		}
		result.Legs = append(result.Legs, execLeg("red", failLegs[0].Cmd, pristineTree, false, caseDir, maxOutput))
	}

	// Apply the node's patch to the delivered tree.
	if output, err := gitOutput(deliveredTree, "apply", "--index", "--whitespace=nowarn", absolutePatch); err != nil {
		result.Verdict = "patch-rejected"
		result.Notes = append(result.Notes, "git apply --index failed: "+strings.TrimSpace(output))
		return result, nil
	}
	result.PatchApplied = true
	changed, err := gitOutput(deliveredTree, "diff", "--cached", "--name-only")
	if err != nil {
		return nil, err
	}
	result.ChangedFiles = nonEmptyLines(changed)

	// keep: pass_to_pass on the agent's tree as delivered. A leg that declares
	// withheld specs is an over-correction guard and needs them restored, so it
	// gets its own copy — a restore must never leak into a later leg or into the
	// delivered tree that other legs read.
	for index, leg := range passLegs {
		name := "keep"
		if len(passLegs) > 1 {
			name = fmt.Sprintf("keep-%d", index+1)
		}
		legTree := deliveredTree
		if needsRestore(leg) {
			legTree = filepath.Join(workDir, fmt.Sprintf("keep-%d", index+1))
			if err := copyTree(deliveredTree, legTree); err != nil {
				return nil, fmt.Errorf("copy delivered tree for %s: %w", name, err)
			}
			if err := restoreWithheld(caseDir, legTree, leg); err != nil {
				return nil, fmt.Errorf("restore withheld tests for %s: %w", name, err)
			}
		}
		result.Legs = append(result.Legs, execLeg(name, leg.Cmd, legTree, true, caseDir, maxOutput))
	}

	// green: overlay each withheld spec onto its own copy of the delivered tree.
	// Each fail_to_pass leg gets a fresh copy: a leg's restore must not be
	// visible to the next one. A negative case has no fail_to_pass leg, so
	// there is nothing to overlay — failLegs is empty and this loop is a no-op,
	// but the guard states that intent rather than leaving it implicit.
	if !negative {
		for index, leg := range failLegs {
			name := "green"
			if len(failLegs) > 1 {
				name = fmt.Sprintf("green-%d", index+1)
			}
			overlayTree := filepath.Join(workDir, fmt.Sprintf("overlay-%d", index+1))
			if err := copyTree(deliveredTree, overlayTree); err != nil {
				return nil, fmt.Errorf("copy delivered tree: %w", err)
			}
			moved, err := moveAsideAgentTests(overlayTree, filepath.Join(workDir, fmt.Sprintf("agent-tests-%d", index+1)),
				withheldDestinations(leg), result.ChangedFiles)
			if err != nil {
				return nil, err
			}
			result.MovedAside = append(result.MovedAside, moved...)
			if err := restoreWithheld(caseDir, overlayTree, leg); err != nil {
				return nil, fmt.Errorf("restore withheld tests over delivered tree: %w", err)
			}
			result.Legs = append(result.Legs, execLeg(name, leg.Cmd, overlayTree, true, caseDir, maxOutput))
		}
	}

	result.Verdict = verdictForLegs(result.Legs, !negative)
	if keepWork {
		result.Notes = append(result.Notes, "scratch retained at "+workDir)
	}
	return result, nil
}

// verdict is deliberately strict. A red leg that did not fail invalidates the
// whole run, because it means the case never distinguished a fix from nothing.
func verdict(legs []legResult) string { return verdictForLegs(legs, true) }

// verdictForLegs scores the run. hasRedLeg distinguishes an ordinary case from a
// negative one, where declining is the correct answer and there is no
// fail_to_pass leg to go green.
func verdictForLegs(legs []legResult, hasRedLeg bool) string {
	for _, leg := range legs {
		if leg.Name == "red" && !leg.OK {
			return "invalid-red-leg-passed"
		}
	}
	for _, leg := range legs {
		if !leg.OK {
			return "fail"
		}
	}
	if !hasRedLeg {
		// Nothing regressed, which is all the mechanical legs can establish here.
		return "kept-green-judge-required"
	}
	return "pass"
}

func execLeg(name, command, dir string, expectSuccess bool, caseDir string, maxOutput int) legResult {
	expected := "nonzero exit"
	if expectSuccess {
		expected = "exit 0"
	}
	started := time.Now()
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = dir
	// A hermetic-looking environment would break every Go case: the toolchain
	// needs PATH, HOME and the module cache. Inheriting is correct here — the
	// corpus's own leakage contract is about what the SOLVER sees, and the
	// grader runs after the solver is done.
	// CASE_DIR is exported because some legs restore their own withheld spec
	// from it (fix-jb-004). Legs that do not reference it are unaffected.
	cmd.Env = append(os.Environ(), "CASE_DIR="+caseDir)
	output, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
			output = append(output, []byte("\nfixgrade: "+err.Error())...)
		}
	}
	return legResult{
		Name: name, Cmd: command, ExitCode: exitCode, Expected: expected,
		OK:       (exitCode == 0) == expectSuccess,
		Duration: time.Since(started).Round(time.Millisecond).String(),
		Output:   tail(string(output), maxOutput),
	}
}

func checkout(sourceRepo, ref, destination string) error {
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return err
	}
	if output, err := gitOutput(sourceRepo, "cat-file", "-e", ref+"^{commit}"); err != nil {
		return fmt.Errorf("commit %s is not in %s: %s", ref, sourceRepo, strings.TrimSpace(output))
	}
	// A detached worktree, not a clone: it is fast, it gives `git apply --index`
	// a real index to work against, and it cannot move any ref in the source.
	if output, err := gitOutput(sourceRepo, "worktree", "add", "--detach", "--force", destination, ref); err != nil {
		return fmt.Errorf("git worktree add: %s", strings.TrimSpace(output))
	}
	return nil
}

// restoreWithheld copies each declared withheld spec into the tree, from its
// case-relative source to its repository-relative destination.
//
// A spec marked self_restoring is skipped: the leg's own cmd puts it in place
// (via $CASE_DIR, `git show`, or `git apply`), and restoring it here as well
// would race that cmd rather than help it.
//
// The legacy withheld_test_paths spelling is refused outright rather than
// guessed at: it names a path but never states which end is the case-relative
// source and which is the repository-relative destination, and a wrong guess
// does not error, it silently grades a correct fix as a miss. Every corpus
// case has been normalized to withheld_tests, and the shape guard in
// corpus_shape_test.go keeps it that way.
func restoreWithheld(caseDir, tree string, leg casespec.Leg) error {
	if len(leg.WithheldTestPaths) > 0 {
		return fmt.Errorf(
			"leg uses the legacy withheld_test_paths spelling, which leaves the repository "+
				"destination implied; normalize the case to withheld_tests with an explicit "+
				"source and destination (paths: %v)", leg.WithheldTestPaths)
	}
	for _, spec := range leg.WithheldTests {
		if spec.SelfRestoring {
			continue
		}
		if spec.Source == "" || spec.Destination == "" {
			return fmt.Errorf("withheld spec needs both source and destination, got %+v", spec)
		}
		contents, err := os.ReadFile(filepath.Join(caseDir, spec.Source))
		if err != nil {
			return fmt.Errorf("withheld spec %s: %w", spec.Source, err)
		}
		destination := filepath.Join(tree, spec.Destination)
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(destination, contents, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// needsRestore reports whether restoring leg's withheld specs would change
// anything in a tree. A leg whose specs are all self-restoring needs no
// restore — its own cmd puts them in place — so a caller deciding whether a
// leg needs a private tree copy should treat it the same as a leg with no
// withheld specs at all.
func needsRestore(leg casespec.Leg) bool {
	for _, spec := range leg.WithheldTests {
		if !spec.SelfRestoring {
			return true
		}
	}
	return false
}

// withheldDestinations lists the repository-relative destination of every
// withheld spec a leg declares, self-restoring ones included: the leg still
// overwrites that destination even when it restores the spec itself via its
// own cmd, so an agent-authored test sitting at that path still needs moving
// aside before the leg runs.
func withheldDestinations(leg casespec.Leg) []string {
	destinations := make([]string, 0, len(leg.WithheldTests))
	for _, spec := range leg.WithheldTests {
		destinations = append(destinations, spec.Destination)
	}
	return destinations
}

// runnableLegs drops legs the corpus marks as not-by-default and reports what
// it dropped. A bounded grader that silently skips a leg reads as "everything
// passed" when it did not.
func runnableLegs(legs []casespec.Leg) (kept []casespec.Leg, skipped []string) {
	for index, leg := range legs {
		switch {
		case leg.Destructive:
			skipped = append(skipped, fmt.Sprintf("leg %d skipped: marked destructive (%s)", index+1, leg.Cmd))
		case leg.Role != "" && leg.Role != "primary":
			skipped = append(skipped, fmt.Sprintf("leg %d skipped: role=%q (%s)", index+1, leg.Role, leg.Cmd))
		default:
			kept = append(kept, leg)
		}
	}
	return kept, skipped
}

// moveAsideAgentTests removes agent-authored *_test.go files that sit in a
// package whose spec is about to be restored. Only files the agent ADDED are
// moved; a test file it merely edited is replaced by the restore itself.
func moveAsideAgentTests(tree, stash string, withheldPaths, changedFiles []string) ([]string, error) {
	packages := map[string]struct{}{}
	withheld := map[string]struct{}{}
	for _, relative := range withheldPaths {
		packages[filepath.Dir(relative)] = struct{}{}
		withheld[filepath.Clean(relative)] = struct{}{}
	}
	var moved []string
	for _, relative := range changedFiles {
		relative = filepath.Clean(relative)
		if _, isWithheld := withheld[relative]; isWithheld {
			continue
		}
		if !strings.HasSuffix(relative, "_test.go") {
			continue
		}
		if _, inPackage := packages[filepath.Dir(relative)]; !inPackage {
			continue
		}
		source := filepath.Join(tree, relative)
		if _, err := os.Stat(source); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		destination := filepath.Join(stash, relative)
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return nil, err
		}
		if err := os.Rename(source, destination); err != nil {
			return nil, err
		}
		moved = append(moved, relative)
	}
	sort.Strings(moved)
	return moved, nil
}

func copyTree(source, destination string) error {
	output, err := exec.Command("cp", "-a", source, destination).CombinedOutput()
	if err != nil {
		return fmt.Errorf("cp -a: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func nonEmptyLines(text string) []string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, strings.TrimSpace(line))
		}
	}
	return lines
}

func tail(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return "...(truncated)...\n" + text[len(text)-limit:]
}

func printReport(result *report) {
	fmt.Printf("case      %s\n", result.Case)
	fmt.Printf("verdict   %s\n", result.Verdict)
	fmt.Printf("patch     applied=%t changed=%d\n", result.PatchApplied, len(result.ChangedFiles))
	for _, file := range result.ChangedFiles {
		fmt.Printf("          %s\n", file)
	}
	if len(result.MovedAside) > 0 {
		fmt.Printf("moved     %s  (agent-authored, score separately against the rubric)\n",
			strings.Join(result.MovedAside, ", "))
	}
	for _, leg := range result.Legs {
		status := "FAIL"
		if leg.OK {
			status = "ok"
		}
		fmt.Printf("%-6s    %-4s exit=%d expected=%s  %s\n", leg.Name, status, leg.ExitCode, leg.Expected, leg.Duration)
	}
	for _, note := range result.Notes {
		fmt.Printf("note      %s\n", note)
	}
	if len(result.Caveats) > 0 {
		fmt.Printf("\nThis case carries grading prose the structured legs do not encode. Read it:\n")
		for _, caveat := range result.Caveats {
			for _, line := range strings.Split(strings.TrimSpace(caveat), "\n") {
				fmt.Printf("  | %s\n", line)
			}
			fmt.Println()
		}
	}
}
