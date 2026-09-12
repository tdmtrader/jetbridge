package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Every destructive call in both daemons, pinned, with what admits it.
//
// The behavioural cases next door prove that the paths a test drives refuse a
// held source. They cannot prove anything about a path nobody drove, and the
// classifier landed wired into exactly one of eleven such paths. This guard is
// the other half: it reads the SOURCE, finds every Remove/RemoveAll/Rename in
// cmd/artifact-daemon and cmd/hangar-output-daemon, and requires each one to be
// either admitted by a named guard or listed as exempt with the reason.
//
// It is deliberately not clever. There is no reachability analysis and no
// attempt to decide from the AST whether a call is dangerous, because a guard
// that decides for you is a guard that can be argued into deciding wrongly and
// then reports nothing. It asks a person to write down, once, why each call may
// destroy something -- and it fails when the answer stops being true.

// destructiveVerbs are the calls that can lose a source.
var destructiveVerbs = map[string]bool{"Remove": true, "RemoveAll": true, "Rename": true}

// admission is what makes one destructive call legitimate.
//
// guard is an identifier that must appear in the body of guardedIn. That is
// what makes this guard fail when somebody deletes the classifier call from a
// handler: the site is still there, the pin still says it is admitted, and the
// thing that admits it is gone.
type admission struct {
	// guard is the identifier that admits the call. Empty means exempt.
	guard string
	// guardedIn is the function whose body must contain guard, when the
	// admission lives in a caller rather than at the call itself. Empty means
	// the site's own function.
	guardedIn string
	// ledgerGuard is the SECOND question a destructive site that takes a
	// caller-chosen name has to have asked. The first is "is this path a
	// source a capture holds"; this one is "is this path the ledger that
	// answers that". They are separate fields because they were separate
	// defects: the classifier was wired into every path here while the records
	// it reads were served, replaced and removed by the same routes, and a
	// single field would have let one stand in for the other.
	ledgerGuard string
	// ledgerGuardedIn is the function whose body must contain ledgerGuard. It
	// is usually not the site's own function: the refusal lives at the door a
	// key or a path comes through, which is what makes it true of routes
	// nobody has written yet.
	ledgerGuardedIn string
	// why is the reason, and it is required for every entry. An exempt site
	// with no reason is an unreviewed site.
	why string
}

// destructiveInventory is the pinned set: site → admission, with the number of
// identical calls in that function.
//
// The key is "file | function | receiver.method(firstArgument)". Line numbers
// are deliberately absent: they change on every edit and would make this a
// guard people update without reading.
var destructiveInventory = map[string]struct {
	count int
	admission
}{
	// ---- the artifact daemon's destructive paths ----

	"artifact-daemon/server.go | Server.handleDeleteArtifact | s.root.RemoveAll(osName())": {1, admission{
		guard:           "refuseIfCaptureHeld",
		ledgerGuard:     "refuseControlDirectory",
		ledgerGuardedIn: "validateRequestKey",
		why: "DELETE /artifacts/ is what the Reaper's cleanupDaemonSetArtifacts calls, by step " +
			"handle. TODO(phase-4): this is also the seam the ticketed control-init operation " +
			"will call when DaemonSetBackend.BuildCleanupInitContainer's `rm -rf` init container " +
			"is replaced (Phase 3 Green box 4, last sentence). That replacement is in " +
			"atc/worker/jetbridge/storage_daemonset.go and is Phase 4's; until it lands the " +
			"init container destroys this path's bytes without passing through here at all.",
	}},
	"artifact-daemon/server.go | Server.handleStreamIn | stepsRoot.RemoveAll(key)": {1, admission{
		guard:           "refuseIfCaptureHeld",
		ledgerGuard:     "refuseControlDirectory",
		ledgerGuardedIn: "validateRequestKey",
		why: "stream-in replaces: it clears the key and renames a fresh tree over it. Its key " +
			"is relative to steps/ and could not name the real control directory; the refusal " +
			"at the door costs a name nothing may create anyway.",
	}},
	"artifact-daemon/server.go | Server.handleStreamIn | stepsRoot.RemoveAll(tmpName)": {1, admission{
		why: "removes the temp directory this call just created, on every error path.",
	}},
	"artifact-daemon/server.go | Server.handlePutArtifact | s.root.Rename(tmpKey)": {1, admission{
		guard:           "refuseIfCaptureHeld",
		ledgerGuard:     "refuseControlDirectory",
		ledgerGuardedIn: "validateRequestKey",
		why:             "the ordinary PUT replaces the bytes at its key.",
	}},
	"artifact-daemon/server.go | Server.handlePutArtifact | s.root.Remove(tmpKey)": {4, admission{
		why: "removes the temp file this call just created, on each error path.",
	}},
	"artifact-daemon/server.go | Server.copyArtifact | parent.RemoveAll(base)": {1, admission{
		guard: "refuseIfCaptureHeldPath", guardedIn: "Server.resolveOne",
		ledgerGuard:     "refuseControlDirectory",
		ledgerGuardedIn: "validateResolveDest",
		why: "copyArtifact clears the resolve DESTINATION. The destination is caller-supplied " +
			"and is checked once in resolveOne, which covers all three resolve branches.",
	}},
	"artifact-daemon/server.go | Server.copyArtifact | parent.RemoveAll(tmp)": {3, admission{
		why: "removes the temp copy this call just created, on each error path.",
	}},
	"artifact-daemon/peers.go | PeerResolver.FetchInto | parent.RemoveAll(base)": {1, admission{
		guard: "refuseIfCaptureHeldPath", guardedIn: "Server.resolveOne",
		ledgerGuard:     "refuseControlDirectory",
		ledgerGuardedIn: "validateResolveDest",
		why: "the peer branch's writer clears the same caller-supplied destination copyArtifact " +
			"does, and reaches it only through resolveOne.",
	}},
	"artifact-daemon/peers.go | PeerResolver.FetchInto | parent.RemoveAll(tmpDir)": {2, admission{
		why: "removes the temp directory this fetch just created.",
	}},
	"artifact-daemon/peers.go | extractTarInto | parent.RemoveAll(tmp)": {2, admission{
		why: "removes the temp directory this extraction just created.",
	}},
	"artifact-daemon/containment.go | promoteDir | parent.Rename(tmp)": {1, admission{
		guard: "refuseIfCaptureHeld", guardedIn: "Server.handleStreamIn",
		ledgerGuard:     "refuseControlDirectory",
		ledgerGuardedIn: "validateRequestKey",
		why: "the shared temp-to-final promote. Its three callers -- handleStreamIn, " +
			"copyArtifact and FetchInto -- each clear the target first and are each guarded " +
			"above; the promote itself renames a name this process minted.",
	}},
	"artifact-daemon/containment.go | Server.removeCreated | s.root.Remove(?)": {1, admission{
		why: "unwinds directories this same call created, on its error path.",
	}},
	"artifact-daemon/containment.go | Server.removeCreated | root.Remove(path.Base())": {1, admission{
		why: "unwinds directories this same call created, on its error path.",
	}},
	"artifact-daemon/sweeper.go | Sweeper.removeStepDir | os.RemoveAll(handleDir)": {1, admission{
		guard: "captureLedger",
		why: "the TTL sweep. Nothing refreshes a held source's mtime, so this is the path a " +
			"source waiting to be sealed is most likely to meet.",
	}},
	"artifact-daemon/sweeper.go | Sweeper.sweep | os.Remove(filePath)": {1, admission{
		why: "legacy flat files directly under artifacts/. A source incarnation is a directory " +
			"under steps/ and can never appear here; the loop skips every subdirectory.",
	}},
	"artifact-daemon/durable_tier.go | DurableTier.Restore | parent.RemoveAll(tmpDir)": {1, admission{
		why: "removes the temp directory this restore just created.",
	}},
	"artifact-daemon/alias_store.go | AliasStore.Save | os.Rename(tmpPath)": {1, admission{
		why: "replaces the alias store's own file. It is this daemon's record, not a source.",
	}},
	"artifact-daemon/alias_store.go | AliasStore.Save | os.Remove(tmpPath)": {1, admission{
		why: "removes the temp file this save just created.",
	}},

	// Registry.Remove entries evict a MAP entry, not bytes. They are here
	// because a registry eviction is how a source becomes unfindable, and the
	// three below all evict an entry whose location is already gone.
	"artifact-daemon/containment.go | Server.lookupRegistry | s.registry.Remove(key)": {1, admission{
		why: "evicts an entry whose stored location no longer exists on disk. The bytes are " +
			"already gone; the remap/reuse rule lives in Registry.RegisterAlias.",
	}},
	"artifact-daemon/server.go | Server.handleHeadResourceCache | s.registry.Remove(key)": {1, admission{
		why: "evicts an entry whose stored location no longer exists on disk.",
	}},
	"artifact-daemon/server.go | Server.handleGetResourceCache | s.registry.Remove(key)": {1, admission{
		why: "evicts an entry whose stored location no longer exists on disk.",
	}},
	"artifact-daemon/durable_handlers.go | Server.handleDurableRestore | s.registry.Remove(req.Key)": {1, admission{
		why: "evicts an entry whose restore failed, so the next resolve re-fetches rather than " +
			"serving a location that was never populated.",
	}},

	// ---- the output daemon's own authority ----
	//
	// The output daemon destroys NOTHING. finishRelease used to remove the
	// released incarnation and was pinned here; a release releases the HOLD
	// now, and the incarnation is the step's own output, aliased read-only at
	// the ordinary path and subject to the artifact daemon's ordinary
	// lifecycle. Deleting a settled incarnation is reclamation by policy, and
	// when that lands it is a new entry here rather than this one returning.
	//
	// The two entries left are record management: neither touches a source.

	"hangar-output-daemon/control_store.go | controlStore.put | store.root.Rename(temp)": {1, admission{
		why: "the ledger's own atomic record replacement. This IS the writer authority the " +
			"artifact daemon's read-only classifier reads; it touches no source.",
	}},
	"hangar-output-daemon/control_store.go | controlStore.quarantineRecord | store.root.Rename(name)": {1, admission{
		why: "moves a torn or unsupported record aside so an operator can read it. It touches " +
			"no source, and the daemon stays unready until it is resolved.",
	}},
	"hangar-output-daemon/config.go | Config.PrepareScratch | os.RemoveAll(filepath.Join())": {1, admission{
		why: "sweeps what a KILLED canonicalization left in the scratch volume, at startup, " +
			"before the listener exists. It touches no source: the scratch directory is the " +
			"canonicalizer's own temporary parent, it holds assembled canonical.tar copies and " +
			"nothing a capture can hold, and only hangar-tree-* entries are removed. A " +
			"restarted daemon owns no in-flight canonicalization, every capture is retried " +
			"under its capture fence, and SealedIncarnation re-derives the tree from the held " +
			"source rather than from scratch. Leaving the residue is the finding: output " +
			"plaintext outliving its capture on the node, and a scratch volume that does not " +
			"start empty under a crash-loop, which is what the chart's sizeLimit arithmetic " +
			"assumes.",
	}},
}

type destructiveSite struct {
	key      string
	function string
}

func scanDestructiveCalls(t *testing.T, dirs ...string) (map[string]int, map[string]string) {
	t.Helper()

	counts := map[string]int{}
	bodies := map[string]string{}

	for _, dir := range dirs {
		// The label is the package DIRECTORY's name, resolved absolutely: this
		// test runs with "." as its own package, and a key that read "./" for
		// one daemon and "hangar-output-daemon/" for the other would name the
		// same kind of thing two ways.
		absolute, err := filepath.Abs(dir)
		if err != nil {
			t.Fatalf("resolving %s: %v", dir, err)
		}
		label := filepath.Base(absolute)

		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("reading %s: %v", dir, err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			fset := token.NewFileSet()
			path := filepath.Join(dir, name)
			source, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v", path, err)
			}
			file, err := parser.ParseFile(fset, path, source, 0)
			if err != nil {
				t.Fatalf("parsing %s: %v", path, err)
			}

			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				function := fn.Name.Name
				if fn.Recv != nil && len(fn.Recv.List) > 0 {
					function = exprText(fn.Recv.List[0].Type) + "." + fn.Name.Name
				}
				bodies[function] = string(source[fn.Body.Pos()-1 : fn.Body.End()-1])

				ast.Inspect(fn.Body, func(node ast.Node) bool {
					call, ok := node.(*ast.CallExpr)
					if !ok {
						return true
					}
					selector, ok := call.Fun.(*ast.SelectorExpr)
					if !ok || !destructiveVerbs[selector.Sel.Name] {
						return true
					}
					argument := "?"
					if len(call.Args) > 0 {
						argument = exprText(call.Args[0])
					}
					counts[fmt.Sprintf("%s/%s | %s | %s.%s(%s)",
						label, name, function,
						exprText(selector.X), selector.Sel.Name, argument)]++

					return true
				})
			}
		}
	}

	return counts, bodies
}

// exprText renders just enough of an expression to identify a call site. It is
// not a printer: a call's arguments are dropped, so `osName(loc)` is
// `osName()`, because the identity that matters is which name is destroyed,
// not how it was spelled.
func exprText(expr ast.Expr) string {
	switch node := expr.(type) {
	case *ast.Ident:
		return node.Name
	case *ast.StarExpr:
		return exprText(node.X)
	case *ast.SelectorExpr:
		return exprText(node.X) + "." + node.Sel.Name
	case *ast.CallExpr:
		return exprText(node.Fun) + "()"
	}

	return "?"
}

func TestArchitecture_EveryDestructiveCallIsAdmittedByANamedGuardOrPinnedAsExempt(t *testing.T) {
	found, bodies := scanDestructiveCalls(t, ".", filepath.Join("..", "hangar-output-daemon"))

	total := 0
	for _, count := range found {
		total += count
	}
	// Non-vacuity, and the reason this number is written down. A guard whose
	// scan silently matched nothing passes, and a scan that stops finding
	// calls -- a renamed directory, a parse that quietly failed -- looks
	// exactly like a daemon that stopped destroying things.
	const pinnedTotal = 32
	if total != pinnedTotal {
		t.Errorf("found %d destructive calls across both daemons and %d are pinned. "+
			"Every Remove/RemoveAll/Rename must be listed in destructiveInventory with the "+
			"guard that admits it or the reason it needs none.", total, pinnedTotal)
	}
	if len(found) == 0 {
		t.Fatal("scanned no destructive calls at all — this guard cannot fail")
	}

	var missing, stale []string
	for key, count := range found {
		pinned, ok := destructiveInventory[key]
		if !ok {
			missing = append(missing, fmt.Sprintf("%s (x%d)", key, count))
			continue
		}
		if pinned.count != count {
			t.Errorf("%s: %d calls, %d pinned. A new one was added beside a reviewed one, "+
				"which is how an unguarded call hides behind a guarded one.", key, count, pinned.count)
		}
	}
	for key := range destructiveInventory {
		if _, ok := found[key]; !ok {
			stale = append(stale, key)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)

	for _, key := range missing {
		t.Errorf("unpinned destructive call: %s\n"+
			"    Add it to destructiveInventory with the guard that admits it, or with the "+
			"reason it destroys nothing a capture can hold.", key)
	}
	for _, key := range stale {
		t.Errorf("pinned destructive call no longer exists: %s\n"+
			"    It was renamed, moved or removed, and its entry now guards nothing.", key)
	}

	// The half that catches a REMOVED guard rather than an added call.
	for key, pinned := range destructiveInventory {
		if pinned.why == "" {
			t.Errorf("%s is pinned with no reason", key)
		}
		for _, named := range []struct{ identifier, in, lost string }{
			{pinned.guard, pinned.guardedIn, "this call now destroys a held source"},
			{pinned.ledgerGuard, pinned.ledgerGuardedIn,
				"this call now reaches the ledger that says what is held"},
		} {
			if named.identifier == "" {
				continue
			}
			where := named.in
			if where == "" {
				_, function, _ := strings.Cut(key, " | ")
				function, _, _ = strings.Cut(function, " | ")
				where = strings.TrimSpace(function)
			}
			body, ok := bodies[where]
			if !ok {
				t.Errorf("%s names %s as the function that guards it, and there is no such "+
					"function", key, where)
				continue
			}
			if !strings.Contains(body, named.identifier) {
				t.Errorf("%s is pinned as admitted by %q in %s, and %s no longer mentions it. "+
					"Either the guard was removed and %s, or the guard moved and this entry "+
					"is wrong.", key, named.identifier, where, where, named.lost)
			}
		}
	}
}
