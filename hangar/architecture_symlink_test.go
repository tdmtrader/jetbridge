package hangar

// Hangar is the SECOND containment implementation in this tree. Core's daemon
// has its own (cmd/artifact-daemon/containment.go), guarded by
// TestArchitecture_SymlinkCreationIsValidated, which scans os.ReadDir(".") in
// its own package directory and therefore never sees hangar/. Nothing else
// stops a third symlink creator being added here without a validator, or
// hangar's validators drifting below core's rule.
//
// This file is that guard, in three parts:
//
//  1. TestArchitectureSymlinkCreationIsValidated — the same AST scan core runs,
//     over hangar's production files, extended to unix.Symlinkat and to both
//     hangar validators, and pinned to the two creators that exist today so it
//     cannot go half-inert when one of them moves.
//  2. TestSymlinkValidatorsAreAtLeastAsStrictAsCore — the parity table: on every
//     input core refuses, both hangar validators must refuse too.
//  3. TestCoreSymlinkValidatorHasNotDrifted — the table compares against a COPY
//     of core's validator, so the copy is checked against core's real source on
//     every run. A change to either side turns the table red instead of leaving
//     it quietly comparing against a fossil.
//
// Why the parity check lives HERE and not in cmd/artifact-daemon: neither
// package can import the other's symbols. cleanSymlinkTarget and
// validateContainedSymlink are unexported in hangar; validateSymlinkTarget is
// unexported in package main, which is not importable at all. So one side must
// be a copy, and it has to be the side that can call the other two directly —
// this package. Importing cmd/artifact-daemon from a hangar test would also
// break hangar's design rule of having no first-party dependencies
// (TestArchitectureHasNoAgentImports); reading core's source as TEXT keeps that
// rule intact and still fails when core changes. The long-term fix the reviewer
// named — lift one rule into an importable package and have hangar call it —
// deletes this file's parts 2 and 3.

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// coreSymlinkValidatorSource is core's validator, and coreValidatorCopy below
// is its behavioural copy. Part 3 keeps them in step.
const (
	coreSymlinkValidatorSource = "../cmd/artifact-daemon/containment.go"
	coreSymlinkValidatorName   = "validateSymlinkTarget"
	thisFile                   = "architecture_symlink_test.go"
	localCoreCopyName          = "coreValidateSymlinkTarget"
)

// coreValidateSymlinkTarget is a behavioural copy of
// cmd/artifact-daemon/containment.go validateSymlinkTarget as of 594609e92d,
// with refused() replaced by fmt.Errorf (refused is a package-main helper and
// its message text is not part of the rule). Do not "improve" it: its whole job
// is to be what core does. TestCoreSymlinkValidatorHasNotDrifted fails if core
// changes and this does not.
func coreValidateSymlinkTarget(entryName, linkname string) error {
	if linkname == "" {
		return fmt.Errorf("symlink entry %q has an empty target", entryName)
	}

	if filepath.IsAbs(linkname) {
		return fmt.Errorf("symlink entry %q targets an absolute path %q", entryName, linkname)
	}

	resolved := filepath.Clean(filepath.Join(filepath.Dir(entryName), linkname))

	if resolved == ".." || strings.HasPrefix(resolved, ".."+string(filepath.Separator)) {
		return fmt.Errorf("symlink entry %q targets %q, which resolves outside the destination (%q)", entryName, linkname, resolved)
	}

	return nil
}

// TestArchitectureSymlinkCreationIsValidated mirrors core's AC7 guard for
// hangar.
//
// Same blind spot as core's, stated: this proves each creating function also
// calls a validator, not that it validates THAT entry or does so first. The
// ordering is covered behaviourally (tree_test.go, materializer_test.go).
func TestArchitectureSymlinkCreationIsValidated(t *testing.T) {
	// Both of hangar's symlink validators. A creator satisfying either one is
	// accepted; which one is right depends on whether the input is a caller's
	// tar (cleanSymlinkTarget) or the daemon's own captured tree
	// (validateContainedSymlink).
	validators := map[string]bool{"cleanSymlinkTarget": true, "validateContainedSymlink": true}

	// The creators that exist today. Pinned so the guard cannot pass while
	// silently seeing only one of them — a file rename, a package split or a
	// build tag that hides a file would otherwise look like "all validated".
	// Adding a THIRD creator is fine (it just has to validate); losing one of
	// these two means re-checking this list on purpose.
	expected := map[string]bool{
		"tree.go:extractTar":                  true,
		"materializer_unix.go:copyOpenedTree": true,
	}

	creators := map[string]bool{}
	validated := map[string]bool{}
	used := map[string]bool{}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		// Parsed regardless of build tags: a creator behind //go:build
		// windows is still a creator, and the guard must see it.
		if entry.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			where := name + ":" + fn.Name.Name
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fun := call.Fun.(type) {
				case *ast.SelectorExpr:
					// os.Root.Symlink (tree.go) and unix.Symlinkat
					// (materializer_unix.go). Core's guard knows only
					// Symlink, which is why it would have missed the
					// second site even if it scanned this directory.
					if fun.Sel.Name == "Symlink" || fun.Sel.Name == "Symlinkat" {
						creators[where] = true
					}
				case *ast.Ident:
					if validators[fun.Name] {
						validated[where] = true
						used[fun.Name] = true
					}
				}
				return true
			})
		}
	}

	if len(creators) == 0 {
		t.Fatal("found no symlink creation in hangar production code — this guard is inert")
	}
	for where := range creators {
		if !validated[where] {
			t.Errorf("%s creates a symlink without calling a hangar symlink validator "+
				"(%v) in the same function; the tree root contains traversal THROUGH a "+
				"link but will happily create one pointing out", where, keysOfBool(validators))
		}
	}
	for where := range expected {
		if !creators[where] {
			t.Errorf("expected symlink creator %s was not found — either it moved (update "+
				"this list deliberately, after checking the new site validates) or this "+
				"guard has stopped seeing part of the package", where)
		}
	}
	for validator := range validators {
		if !used[validator] {
			t.Errorf("%s is listed as a hangar symlink validator but no production function "+
				"calls it; either it is dead or the guard is accepting a creator that "+
				"validates nothing", validator)
		}
	}
	t.Logf("symlink creators: %v — all validated by %v", keysOfBool(creators), keysOfBool(used))
}

// TestSymlinkValidatorsAreAtLeastAsStrictAsCore is the parity table. The
// property is one-directional on purpose: hangar may refuse MORE than core
// (cleanSymlinkTarget bounds target length, rejects backslashes and drive-like
// targets; validateContainedSymlink rejects NUL), but must never accept
// something core refuses. If a future edit weakens either validator below core's
// rule, this is the test that reddens.
func TestSymlinkValidatorsAreAtLeastAsStrictAsCore(t *testing.T) {
	cases := []struct {
		name, entry, target string
	}{
		{"absolute", "l", "/etc/passwd"},
		{"absolute root", "l", "/"},
		{"dotdot escape", "l", ".."},
		{"dotdot slash escape", "l", "../x"},
		{"nested dotdot escape", "d/l", "../../x"},
		{"nested dotdot inside", "d/l", "../f"},
		{"deep then escape", "l", "a/b/../../../x"},
		{"self dot", "a", "."},
		{"through self dot (esc -> a/..)", "esc", "a/.."},
		{"empty", "l", ""},
		{"NUL", "l", "a\x00b"},
		{"NUL escape", "l", "..\x00"},
		{"trailing slash", "l", "sub/"},
		{"trailing slash escape", "l", "../"},
		{"dot slash", "l", "./f"},
		{"backslash", "l", `..\..\x`},
		{"drive-like", "l", "C:/x"},
		{"long", "l", strings.Repeat("a", 5000)},
		{"double slash", "l", "a//b"},
		{"dotdot in middle inside", "l", "a/../b"},
	}
	for _, c := range cases {
		core := coreValidateSymlinkTarget(c.entry, c.target)
		_, clean := cleanSymlinkTarget(c.entry, c.target)
		contained := validateContainedSymlink(c.entry, c.target)
		t.Logf("%-32s core=%-6v cleanSymlinkTarget=%-6v validateContainedSymlink=%-6v",
			c.name, core == nil, clean == nil, contained == nil)
		if core != nil && clean == nil {
			t.Errorf("%s (%q -> %q): core refuses but hangar cleanSymlinkTarget accepts",
				c.name, c.entry, c.target)
		}
		if core != nil && contained == nil {
			t.Errorf("%s (%q -> %q): core refuses but hangar validateContainedSymlink accepts",
				c.name, c.entry, c.target)
		}
	}
}

// TestCoreSymlinkValidatorHasNotDrifted compares the copy above against core's
// real source. Without it the parity table degrades into a comparison against
// whatever core looked like the day someone typed the copy.
//
// The comparison is a structural fingerprint, not the source text: it ignores
// comments, formatting and the MESSAGE argument of refused()/fmt.Errorf(), and
// keeps everything that decides an outcome — control flow, operators, the
// functions called, and every other literal (".." matters; the sentence
// explaining ".." does not).
func TestCoreSymlinkValidatorHasNotDrifted(t *testing.T) {
	core := funcDecl(t, coreSymlinkValidatorSource, coreSymlinkValidatorName)
	copied := funcDecl(t, thisFile, localCoreCopyName)

	coreRule := ruleFingerprint(core)
	copiedRule := ruleFingerprint(copied)
	if coreRule != copiedRule {
		t.Errorf("core %s and this package's copy %s no longer describe the same rule.\n"+
			"Re-read %s, update %s to match, and re-check the parity table — hangar's\n"+
			"validators are only known to be at least as strict as a copy that is current.\n"+
			"core: %s\ncopy: %s",
			coreSymlinkValidatorName, localCoreCopyName, coreSymlinkValidatorSource,
			localCoreCopyName, coreRule, copiedRule)
	}
	t.Logf("core rule fingerprint: %s", coreRule)
}

func funcDecl(t *testing.T, path, name string) *ast.FuncDecl {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v — this guard cannot run", path, err)
	}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == name && fn.Body != nil {
			return fn
		}
	}
	t.Fatalf("%s does not define %s — it was renamed or removed, and the parity table "+
		"is no longer comparing against anything", path, name)
	return nil
}

// errorConstructors are the calls whose FIRST argument is an operator-facing
// message rather than part of the rule.
var errorConstructors = map[string]bool{"refused": true, "fmt.Errorf": true, "errors.New": true}

func ruleFingerprint(fn *ast.FuncDecl) string {
	skip := map[ast.Node]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !errorConstructors[calleeName(call.Fun)] {
			return true
		}
		// Drop the constructor's identity and its message; keep the
		// arguments, which name the values the rule decided on.
		skip[call.Fun] = true
		if len(call.Args) > 0 {
			if _, isLiteral := call.Args[0].(*ast.BasicLit); isLiteral {
				skip[call.Args[0]] = true
			}
		}
		return true
	})

	var out []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if n == nil || skip[n] {
			return false
		}
		switch node := n.(type) {
		case *ast.Ident:
			out = append(out, "id:"+node.Name)
		case *ast.BasicLit:
			out = append(out, "lit:"+node.Value)
		case *ast.BinaryExpr:
			out = append(out, "op:"+node.Op.String())
		case *ast.UnaryExpr:
			out = append(out, "unary:"+node.Op.String())
		case *ast.AssignStmt:
			out = append(out, "assign:"+node.Tok.String())
		case *ast.CallExpr:
			out = append(out, "call")
		case *ast.IfStmt:
			out = append(out, "if")
		case *ast.ForStmt, *ast.RangeStmt:
			out = append(out, "loop")
		case *ast.ReturnStmt:
			out = append(out, "return")
		case *ast.SwitchStmt, *ast.TypeSwitchStmt:
			out = append(out, "switch")
		}
		return true
	})
	return strings.Join(out, " ")
}

func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		if pkg, ok := f.X.(*ast.Ident); ok {
			return pkg.Name + "." + f.Sel.Name
		}
		return f.Sel.Name
	}
	return ""
}

func keysOfBool(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestCanonicalizerChainedSymlinks is the behavioural half the lexical
// validators cannot cover: archives whose links pass every per-entry check but
// chain through each other.
//
// `a -> .` then `esc -> a/..` is ACCEPTED, and so it is by core's lexical
// validator — that is parity, not a gap. In both designs containment of such a
// link rests on readers never following it (os.Root in core, per-component
// O_NOFOLLOW plus sameOpenEntryAt in hangar). Chaining through a link in the
// NAME rather than the target is refused outright by validateHostParents.
func TestCanonicalizerChainedSymlinks(t *testing.T) {
	build := func(entries ...*tar.Header) []byte {
		var buf bytes.Buffer
		w := tar.NewWriter(&buf)
		for _, h := range entries {
			if err := w.WriteHeader(h); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	cases := []struct {
		name    string
		archive []byte
		wantOK  bool
	}{
		{"a -> . then esc -> a/.. (target chains through a link; lexically inside)", build(
			&tar.Header{Name: "a", Typeflag: tar.TypeSymlink, Linkname: ".", Mode: 0o777},
			&tar.Header{Name: "esc", Typeflag: tar.TypeSymlink, Linkname: "a/..", Mode: 0o777},
		), true},
		{"a -> . then a/b -> .. (name chains through a link)", build(
			&tar.Header{Name: "a", Typeflag: tar.TypeSymlink, Linkname: ".", Mode: 0o777},
			&tar.Header{Name: "a/b", Typeflag: tar.TypeSymlink, Linkname: "..", Mode: 0o777},
		), false},
		{"a -> . then a/f.txt regular through a link", build(
			&tar.Header{Name: "a", Typeflag: tar.TypeSymlink, Linkname: ".", Mode: 0o777},
			&tar.Header{Name: "a/f.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: 0},
		), false},
		{"trailing-slash target", build(
			&tar.Header{Name: "d", Typeflag: tar.TypeDir, Mode: 0o755},
			&tar.Header{Name: "l", Typeflag: tar.TypeSymlink, Linkname: "d/", Mode: 0o777},
		), true},
	}
	for _, c := range cases {
		tree, err := (Canonicalizer{TempDir: t.TempDir(), MaxEntries: 10, MaxContentBytes: 1024}).
			Capture(context.Background(), bytes.NewReader(c.archive))
		if tree != nil {
			tree.Close()
		}
		if (err == nil) != c.wantOK {
			t.Errorf("%s: accepted=%v want %v (err=%v)", c.name, err == nil, c.wantOK, err)
		}
	}
}
