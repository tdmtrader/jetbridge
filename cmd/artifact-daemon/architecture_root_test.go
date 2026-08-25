package main

// Structural guards for the handle-based containment.
//
// Each fails when its scan matches nothing, and each states its own blind
// spot. A guard that hides its approximation is the shape this proposal keeps
// finding — an earlier round produced exactly that defect ("N6 defeatable AST
// guard"), and the fix was to say in the test what the guard cannot see.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

func productionFiles(t *testing.T) map[string]*ast.File {
	t.Helper()
	fset := token.NewFileSet()
	out := map[string]*ast.File{}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, n, nil, 0)
		if err != nil {
			// Not fatal: unparseable debris is the build's problem, and
			// Fataling here turns unrelated mess into a guard failure.
			t.Logf("skipping unparseable %s: %v", n, err)
			continue
		}
		out[n] = f
	}
	if len(out) == 0 {
		t.Fatal("scanned no production files — this guard cannot fail")
	}
	return out
}

// AC4 — the duplicate extractor is gone and must not come back. server.go
// consumes archives only through the shared extractor.
func TestArchitecture_ServerHasNoTarReader(t *testing.T) {
	files := productionFiles(t)

	var readers []string
	for name, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "NewReader" {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "tar" {
				readers = append(readers, name)
			}
			return true
		})
	}

	if len(readers) == 0 {
		t.Fatal("found no tar.NewReader anywhere — the token moved and this guard is inert")
	}
	for _, name := range readers {
		if name == "server.go" {
			t.Errorf("server.go builds a tar.NewReader again; archives are consumed through "+
				"extractTarToRoot. Found in: %v", readers)
		}
	}
	t.Logf("tar.NewReader present in %v; server.go is not among them", readers)
}

// AC7 — a symlink is never created without the target being validated first.
// os.Root contains traversal THROUGH a link but will happily create one
// pointing out, which is Track 1's finding and the reason this guard exists.
//
// Blind spot, stated: this checks that each function creating a symlink also
// calls the validator. It does not prove the call happens on the same entry,
// or first. A syntactic guard cannot; the ordering is covered by the
// behavioural tests instead.
func TestArchitecture_SymlinkCreationIsValidated(t *testing.T) {
	files := productionFiles(t)

	creators := map[string]bool{}
	validators := map[string]bool{}

	for name, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}
			ast.Inspect(fn.Body, func(m ast.Node) bool {
				call, ok := m.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fun := call.Fun.(type) {
				case *ast.SelectorExpr:
					if fun.Sel.Name == "Symlink" {
						creators[name+":"+fn.Name.Name] = true
					}
				case *ast.Ident:
					if fun.Name == "validateSymlinkTarget" {
						validators[name+":"+fn.Name.Name] = true
					}
				}
				return true
			})
			return true
		})
	}

	if len(creators) == 0 {
		t.Fatal("found no symlink creation in production code — this guard is inert")
	}
	for where := range creators {
		if !validators[where] {
			t.Errorf("%s creates a symlink without calling validateSymlinkTarget in the same "+
				"function; os.Root will create an outward link", where)
		}
	}
	t.Logf("symlink creators: %v — all validated", keysOf(creators))
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// AC5 — no production filesystem call joins an untrusted key onto a storage
// path and then writes through the result.
//
// I did NOT implement this initially, arguing instead in a commit that no
// unrouted request-derived join remained. The argument was wrong: handlePut
// and handleHead were both still ambient, and this guard finds exactly that.
// An argument is not a gate.
//
// BLIND SPOT, stated rather than hidden: "joins an untrusted key" is a
// dataflow property and this is a syntactic scan. It approximates it by
// flagging any function that BOTH joins onto a storage path AND performs an
// ambient mutating os.* call — so a function that joins for a lock key only
// (which R12 requires) is flagged too, and must be listed below with its
// reason. That list is the approximation made visible; it is not an allowlist
// of things exempt from the rule.
func TestArchitecture_NoAmbientWriteThroughAJoinedKey(t *testing.T) {
	files := productionFiles(t)

	// Functions that legitimately join for a LOCK KEY or a REGISTRY VALUE,
	// which R12 requires to stay absolute, and whose writes go through a
	// handle. Each is here because its containment comes from somewhere the
	// scanner cannot see.
	//
	// Kept MINIMAL on purpose. The first version listed seven functions and the
	// liveness assertion below showed five of them never tripped the guard at
	// all — pre-emptive exemptions for code that did not need them, which is
	// both noise and a mask: a future regression in one of those five would
	// have been silently excused by an entry written before the regression
	// existed. An exemption is only legitimate once the scan actually flags the
	// thing.
	known := map[string]string{
		"sweeper.go:Sweeper.sweep": "joins are CONSTANT suffixes (steps, artifacts) and os.ReadDir-derived names — no untrusted input reaches them. Deliberately not migrated: R12 names ScanHostPath as a coin flip whose losing side is a SILENT read/sweep guard failure, and uniformity is not worth that trade",
	}

	mutating := map[string]bool{
		"Create": true, "CreateTemp": true, "MkdirAll": true, "Mkdir": true,
		"MkdirTemp": true, "Remove": true, "RemoveAll": true, "Rename": true,
		"OpenFile": true, "WriteFile": true, "Symlink": true, "Link": true, "Chmod": true,
	}

	type site struct{ joins, writes bool }
	found := map[string]*site{}

	for name, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}
			recv := ""
			if fn.Recv != nil && len(fn.Recv.List) > 0 {
				if st, ok := fn.Recv.List[0].Type.(*ast.StarExpr); ok {
					if id, ok := st.X.(*ast.Ident); ok {
						recv = id.Name + "."
					}
				}
			}
			where := name + ":" + recv + fn.Name.Name
			ast.Inspect(fn.Body, func(m ast.Node) bool {
				call, ok := m.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, _ := sel.X.(*ast.Ident)
				if pkg == nil {
					return true
				}
				if pkg.Name == "filepath" && sel.Sel.Name == "Join" {
					for _, a := range call.Args {
						if s, ok := a.(*ast.SelectorExpr); ok && strings.Contains(s.Sel.Name, "toragePath") {
							if found[where] == nil {
								found[where] = &site{}
							}
							found[where].joins = true
						}
						if id, ok := a.(*ast.Ident); ok && strings.Contains(id.Name, "toragePath") {
							if found[where] == nil {
								found[where] = &site{}
							}
							found[where].joins = true
						}
					}
				}
				if pkg.Name == "os" && mutating[sel.Sel.Name] {
					if found[where] == nil {
						found[where] = &site{}
					}
					found[where].writes = true
				}
				return true
			})
			return true
		})
	}

	var scanned int
	for _, s := range found {
		if s.joins {
			scanned++
		}
	}
	if scanned == 0 {
		t.Fatal("guard found no storage-path joins at all — it is inert")
	}

	matched := map[string]bool{}

	for where, s := range found {
		if !s.joins || !s.writes {
			continue
		}
		if reason, ok := known[where]; ok {
			matched[where] = true
			t.Logf("known: %s — %s", where, reason)
			continue
		}
		t.Errorf("%s joins a key onto a storage path AND performs an ambient mutating os.* call. "+
			"Either route the write through a handle, or add it above with the reason its "+
			"containment comes from elsewhere.", where)
	}
	// The map must be falsifiable too, or it is an allowlist in costume.
	//
	// An entry that matches nothing is a permanent exemption no scan can
	// retire — and this guard acquired one within a single commit, when
	// artifactPath was deleted in the same change that listed it. The
	// scanned==0 check above protects the scan; this protects the map.
	for where := range known {
		if !matched[where] {
			t.Errorf("known entry %q matched nothing — the function was renamed, moved or "+
				"deleted, and the exemption is now stale. Remove it; a reason that guards "+
				"nothing is an allowlist in costume.", where)
		}
	}

	t.Logf("scanned %d functions joining a storage path; %d known entries all live",
		scanned, len(known))
}
