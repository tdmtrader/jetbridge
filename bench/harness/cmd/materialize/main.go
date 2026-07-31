// Command materialize turns one bench corpus case's pre-state into typed
// snapshots on a fly target and prints the `fly agent nodes run` input flags.
//
//	materialize -case review-jb-004 -target home
//
// EXPOSURE CONTRACT: only task/ and the pre-state tree are uploaded. case.yaml,
// notes.md and ground_truth/ are harness-side and never leave this machine.
//
// Source trees are materialized with `git archive`, which produces the tree at
// the pinned ref with no .git directory. That is not incidental: several cases
// (review-jb-001, neg-cc-001) have a terminal commit that is a direct child of
// their pre_state and reachable from branch tips, so a clone would hand the
// solver the answer key. Those cases carry an explicit `materialize:` directive
// saying so, and this tool refuses to guess when it sees one it cannot honor.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/concourse/concourse/bench/harness/casespec"
)

// repoRoots maps a corpus repo label to a local checkout.
var repoRoots = map[string]string{
	"jetbridge":          "/Users/tdmtrader/concourse/concourse",
	"concourse-upstream": "/Users/tdmtrader/concourse/concourse",
	"lightingdesign":     "/Users/tdmtrader/LightingDesign",
}

func main() {
	caseID := flag.String("case", "", "corpus case id, e.g. review-jb-004 (required)")
	target := flag.String("target", "home", "fly target")
	corpus := flag.String("corpus", "bench/corpus", "path to the corpus directory")
	flyPath := flag.String("fly", "fly", "path to a fly binary built from this tree")
	dryRun := flag.Bool("dry-run", false, "resolve and stage inputs without creating snapshots")
	flag.Parse()

	if *caseID == "" {
		flag.Usage()
		os.Exit(2)
	}

	spec, err := casespec.Load(*corpus, *caseID)
	if err != nil {
		fail(err)
	}

	fmt.Fprintf(os.Stderr, "# case %s\n", spec.ID)

	staging, err := os.MkdirTemp("", "bench-materialize-")
	if err != nil {
		fail(err)
	}
	defer os.RemoveAll(staging)

	// Deterministic order so the printed flags are stable run to run.
	ports := make([]string, 0, len(spec.Inputs))
	for name := range spec.Inputs {
		ports = append(ports, name)
	}
	sort.Strings(ports)

	flags := make([]string, 0, len(ports))
	for _, name := range ports {
		port := spec.Ports[name]
		typeRef := spec.Inputs[name]

		dir := filepath.Join(staging, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fail(err)
		}

		switch {
		case port.SourceTree():
			if err := materializeTree(port, dir); err != nil {
				fail(fmt.Errorf("port %q: %w", name, err))
			}
		case port.Path != "":
			source := filepath.Join(spec.Dir, port.Path)
			if err := copyFile(source, filepath.Join(dir, filepath.Base(port.Path))); err != nil {
				fail(fmt.Errorf("port %q: %w", name, err))
			}
		default:
			fail(fmt.Errorf("port %q has neither ref nor path", name))
		}

		if *dryRun {
			count, bytes := measure(dir)
			fmt.Fprintf(os.Stderr, "# %-14s %-22s %d files, %d bytes (dry run)\n", name, typeRef, count, bytes)
			flags = append(flags, "--input "+name+"=<dry-run>")
			continue
		}

		id, err := createSnapshot(*flyPath, *target, typeRef, dir)
		if err != nil {
			fail(fmt.Errorf("port %q: %w", name, err))
		}
		fmt.Fprintf(os.Stderr, "# %-14s %-22s %s\n", name, typeRef, id)
		flags = append(flags, "--input "+name+"="+id)
	}

	fmt.Println(strings.Join(flags, " "))
}

// materializeTree writes the pinned tree with no version-control history.
func materializeTree(port casespec.Port, dir string) error {
	root, found := repoRoots[port.Repo]
	if !found {
		return fmt.Errorf("unknown repo label %q", port.Repo)
	}
	// Honor an explicit directive only when it is the git-archive form this
	// tool implements; anything else (e.g. neg-cc-001's refs-deleted clone of
	// public upstream history) must be done deliberately, not guessed at.
	if directive := strings.TrimSpace(port.Materialize); directive != "" && !strings.Contains(directive, "git archive") {
		return fmt.Errorf("case declares a materialize directive this tool does not implement; do it by hand:\n%s", directive)
	}

	archive := exec.Command("git", "-C", root, "archive", port.Ref)
	untar := exec.Command("tar", "-x", "-C", dir)
	pipe, err := archive.StdoutPipe()
	if err != nil {
		return err
	}
	untar.Stdin = pipe
	archive.Stderr = os.Stderr
	untar.Stderr = os.Stderr

	if err := untar.Start(); err != nil {
		return err
	}
	if err := archive.Run(); err != nil {
		return fmt.Errorf("git archive %s: %w", port.Ref, err)
	}
	return untar.Wait()
}

func createSnapshot(flyPath, target, typeRef, dir string) (string, error) {
	cmd := exec.Command(flyPath, "-t", target, "agent", "snapshots", "create",
		"--type", typeRef, "--from", dir, "--json")
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("fly agent snapshots create: %w", err)
	}
	var created struct {
		ID any `json:"id"`
	}
	if err := json.Unmarshal(out, &created); err != nil {
		return "", fmt.Errorf("decode snapshot response %q: %w", strings.TrimSpace(string(out)), err)
	}
	switch id := created.ID.(type) {
	case string:
		if id != "" {
			return id, nil
		}
	case float64:
		return fmt.Sprintf("%d", int64(id)), nil
	}
	return "", fmt.Errorf("snapshot response carried no id: %s", strings.TrimSpace(string(out)))
}

func copyFile(source, destination string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(destination, data, 0o644)
}

func measure(dir string) (int, int64) {
	var count int
	var total int64
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		count++
		total += info.Size()
		return nil
	})
	return count, total
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "materialize: %v\n", err)
	os.Exit(1)
}
