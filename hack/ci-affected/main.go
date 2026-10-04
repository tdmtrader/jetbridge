// Command ci-affected prints the Go packages whose tests a change can affect.
//
// It is the selector behind `hack/ci-check.sh --affected` and
// `hack/test-affected.sh`: the inner loop runs only these packages, and the
// full unit tier stays the merge gate. A package is affected when its tests
// import -- directly or transitively -- a package containing a changed file.
//
// Usage:
//
//	ci-affected [-tree <dir>] <base-ref> <ref>
//	ci-affected <base-ref> WORKTREE
//
// The diff is `git diff --name-only <base-ref>...<ref>` in the current
// repository; the package graph is read from -tree (default: the current
// directory), which must hold <ref>'s tree so the graph matches the change.
// WORKTREE instead diffs the working tree, untracked files included.
//
// It prints ALL instead of a list whenever it cannot be sure the list is
// complete: go.mod or go.sum changed, or a changed file belongs to no Go
// package and is not documentation. A selector that guesses small is worse
// than none -- the inner loop would go green on a change it never tested.
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// extraTargets are packages that read files outside their own directory, so
// a change under the prefix affects them although no import says so.
var extraTargets = map[string][]string{
	"deploy/": {"deploy/chart/tests"},
	// Scripts and tools under hack/ are read by the root package's
	// architecture_test.go and by nothing else.
	"hack/": {"."},
}

func main() {
	args := os.Args[1:]
	tree := "."
	if len(args) >= 2 && args[0] == "-tree" {
		tree, args = args[1], args[2:]
	}
	if len(args) != 2 {
		fatalf("usage: ci-affected [-tree <dir>] <base-ref> <ref>")
	}

	changed, err := changedFiles(args[0], args[1])
	if err != nil {
		fatalf("%v", err)
	}
	module, graph, dirs, err := packageGraph(tree)
	if err != nil {
		fatalf("%v", err)
	}

	pkgs, all := affected(changed, module, graph, dirs)
	if all {
		fmt.Println("ALL")
		return
	}
	for _, p := range pkgs {
		fmt.Println(p)
	}
}

// affected maps changed files to the packages they live in, then selects every
// package whose test build depends on one of them. all is true when the change
// cannot be bounded.
func affected(changed []string, module string, graph map[string][]string, dirs map[string]string) (pkgs []string, all bool) {
	touched := map[string]bool{}
	// direct packages read a changed file at test time; they are selected
	// themselves, but nothing that imports them is, since the file is not
	// compiled into them.
	direct := map[string]bool{}
	for _, file := range changed {
		switch {
		case file == "go.mod" || file == "go.sum":
			fmt.Fprintf(os.Stderr, "ci-affected: %s changed; every package is affected\n", file)
			return nil, true
		case isDoc(file):
			continue
		}
		for prefix, targets := range extraTargets {
			if strings.HasPrefix(file, prefix) {
				for _, t := range targets {
					if t == "." {
						direct[module] = true
					} else {
						direct[module+"/"+t] = true
					}
				}
			}
		}
		pkg, ok := owningPackage(file, module, dirs)
		if !ok {
			if hasExtraTarget(file) {
				continue
			}
			fmt.Fprintf(os.Stderr, "ci-affected: %s belongs to no Go package; cannot bound the change\n", file)
			return nil, true
		}
		touched[pkg] = true
	}

	for pkg, deps := range graph {
		if touched[pkg] || direct[pkg] {
			pkgs = append(pkgs, pkg)
			continue
		}
		for _, d := range deps {
			if touched[d] {
				pkgs = append(pkgs, pkg)
				break
			}
		}
	}
	sort.Strings(pkgs)
	return pkgs, false
}

func hasExtraTarget(file string) bool {
	for prefix := range extraTargets {
		if strings.HasPrefix(file, prefix) {
			return true
		}
	}
	return false
}

func isDoc(file string) bool {
	return strings.HasSuffix(file, ".md") || strings.HasPrefix(file, "docs/")
}

// owningPackage is the package in the nearest directory at or above file that
// holds one. The module root counts only for files directly in it: a file in a
// package-less subdirectory is not the root package's business, and is
// reported unowned instead.
func owningPackage(file, module string, dirs map[string]string) (string, bool) {
	dir := filepath.Dir(file)
	for {
		if pkg, ok := dirs[dir]; ok {
			if dir == "." && filepath.Dir(file) != "." {
				return "", false
			}
			return pkg, true
		}
		if dir == "." {
			return "", false
		}
		dir = filepath.Dir(dir)
	}
}

// changedFiles is the change since base's merge-base with ref. The ref
// WORKTREE means the working tree as it stands: committed, staged, unstaged
// and untracked changes alike, which is what an inner loop is testing.
func changedFiles(base, ref string) ([]string, error) {
	if ref != worktree {
		out, err := exec.Command("git", "diff", "--name-only", base+"..."+ref).Output()
		if err != nil {
			return nil, fmt.Errorf("git diff %s...%s: %w", base, ref, err)
		}
		return strings.Fields(string(out)), nil
	}
	mb, err := exec.Command("git", "merge-base", base, "HEAD").Output()
	if err != nil {
		return nil, fmt.Errorf("git merge-base %s HEAD: %w", base, err)
	}
	tracked, err := exec.Command("git", "diff", "--name-only", strings.TrimSpace(string(mb))).Output()
	if err != nil {
		return nil, fmt.Errorf("git diff against the merge-base: %w", err)
	}
	untracked, err := exec.Command("git", "ls-files", "--others", "--exclude-standard").Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	return append(strings.Fields(string(tracked)), strings.Fields(string(untracked))...), nil
}

const worktree = "WORKTREE"

// packageGraph lists every package with its test build's dependencies, keyed
// by import path, and each package's directory relative to the module root.
func packageGraph(tree string) (string, map[string][]string, map[string]string, error) {
	cmd := exec.Command("go", "list", "-test", "-f", "{{.ImportPath}}\t{{.ForTest}}\t{{.Dir}}\t{{.Module.Dir}}\t{{.Module.Path}}\t{{join .Deps \" \"}}", "./...")
	cmd.Dir = tree
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", nil, nil, fmt.Errorf("go list: %w\n%s", err, stderr.String())
	}

	var module string
	graph := map[string][]string{}
	dirs := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 1<<20), 1<<26)
	for scanner.Scan() {
		f := strings.SplitN(scanner.Text(), "\t", 6)
		if len(f) != 6 {
			continue
		}
		importPath, forTest, dir, moduleDir, modulePath, deps := f[0], f[1], f[2], f[3], f[4], f[5]
		module = modulePath
		// "p [p.test]" and "p_test [p.test]" are p's test build; "p.test"
		// is the generated main. All of them fold into p.
		pkg := strings.SplitN(importPath, " ", 2)[0]
		if forTest != "" {
			pkg = forTest
		}
		if strings.HasSuffix(pkg, ".test") {
			continue
		}
		if rel, err := filepath.Rel(moduleDir, dir); err == nil && importPath == pkg {
			dirs[rel] = pkg
		}
		for _, d := range strings.Fields(deps) {
			graph[pkg] = append(graph[pkg], strings.SplitN(d, " ", 2)[0])
		}
		if _, ok := graph[pkg]; !ok {
			graph[pkg] = nil
		}
	}
	return module, graph, dirs, scanner.Err()
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ci-affected: "+format+"\n", args...)
	os.Exit(1)
}
