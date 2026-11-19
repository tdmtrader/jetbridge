package concourse

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// THE RUN CONTRACT'S SINGLE PATHS, STATED AS SOURCE PROPERTIES.
//
// Durable Run cancellation rests on four structural claims, none of which any
// one Run can observe (durable_run_cancellation_control T5):
//
//   - one function writes a Run's status: the v2 terminalizer. A second is a
//     second way to publish an outcome, which is what a "cancelling" or
//     API-only status change would be;
//   - one function records the cancellation request and fence. The API and the
//     deadline follow-on are callers of it, not further entry points, so this
//     counts writers, never callers;
//   - core's run packages cannot kill anything Kubernetes-specifically: they do
//     not link a Kubernetes client or the JetBridge runtime, so stopping work is
//     only ever the executor-neutral node protocol behind an interface;
//
// Each check is a pure function over what the scan found, so each is driven
// below with a scan that finds nothing and must object, like
// TestUnpinnedAgenticPackagesGuardFailsOnAnEmptyScan.

// goSourceFile is one parsed production Go file of the root module.
type goSourceFile struct {
	rel  string // slash-separated, relative to the repository root
	file *ast.File
}

// productionGoFiles parses every non-test Go file of the root module that
// include accepts. Nested modules are other modules -- the brine suite's step
// fixtures write Run rows directly on purpose -- so their trees are skipped.
func productionGoFiles(t *testing.T, include func(rel string) bool) []goSourceFile {
	t.Helper()

	root := repositoryRoot()
	fset := token.NewFileSet()
	var files []goSourceFile
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "vendor", ".git", ".claude", "node_modules", "testdata":
				return filepath.SkipDir
			}
			if path != root {
				if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
					return filepath.SkipDir
				}
			}

			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(relative)
		if !include(rel) {
			return nil
		}
		// Comments are left out: prose may name the seam it keeps.
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		files = append(files, goSourceFile{rel: rel, file: file})

		return nil
	})
	if err != nil {
		t.Fatalf("scanning the repository: %v", err)
	}

	return files
}

// pipelineRunUpdate captures the SET list of an UPDATE of the Run header.
var pipelineRunUpdate = regexp.MustCompile(`(?is)\bUPDATE\s+pipeline_runs\b(?:\s+(?:AS\s+)?[a-z_]+)??\s+SET\s+(.*?)(?:\bWHERE\b|\bFROM\b|\bRETURNING\b|$)`)

// runHeaderColumns names Run header columns two ways: inside a SQL SET list,
// and as the bare column name a query builder's Set takes.
type runHeaderColumns struct{ inSQL, name *regexp.Regexp }

var (
	runStatusColumn = runHeaderColumns{
		inSQL: regexp.MustCompile(`(?i)(?:^|[\s,.])status\s*=`),
		name:  regexp.MustCompile(`^status$`),
	}
	runFenceColumns = runHeaderColumns{
		inSQL: regexp.MustCompile(`(?i)(?:^|[\s,.])(?:cancel_requested_at|cancel_requested_by|cancel_reason)\s*=`),
		name:  regexp.MustCompile(`^(?:cancel_requested_at|cancel_requested_by|cancel_reason)$`),
	}
)

// stringArgument returns call's first argument when it is a string literal.
func stringArgument(call *ast.CallExpr) (string, bool) {
	if len(call.Args) == 0 {
		return "", false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	text, err := strconv.Unquote(lit.Value)
	return text, err == nil
}

// localAssignments records every expression assigned to each local name in
// decl, so a builder or a map held in a variable can be followed back.
func localAssignments(decl ast.Decl) map[string][]ast.Expr {
	locals := map[string][]ast.Expr{}
	ast.Inspect(decl, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.AssignStmt:
			if len(n.Lhs) == len(n.Rhs) {
				for k, lhs := range n.Lhs {
					if ident, ok := lhs.(*ast.Ident); ok {
						locals[ident.Name] = append(locals[ident.Name], n.Rhs[k])
					}
				}
			}
		case *ast.ValueSpec:
			if len(n.Names) == len(n.Values) {
				for k, ident := range n.Names {
					locals[ident.Name] = append(locals[ident.Name], n.Values[k])
				}
			}
		}
		return true
	})
	return locals
}

// builderTable follows a query builder chain (psql.Update("t").Set(...)...)
// back to its Update call and returns the table, through builders held in
// local variables. It reports false when the chain has no visible Update.
func builderTable(expr ast.Expr, locals map[string][]ast.Expr, seen map[string]bool) (string, bool) {
	for {
		switch e := expr.(type) {
		case *ast.CallExpr:
			selector, ok := e.Fun.(*ast.SelectorExpr)
			if !ok {
				return "", false
			}
			if selector.Sel.Name == "Update" {
				return stringArgument(e)
			}
			expr = selector.X
		case *ast.Ident:
			if seen[e.Name] {
				return "", false
			}
			seen[e.Name] = true
			for _, assigned := range locals[e.Name] {
				if table, ok := builderTable(assigned, locals, seen); ok {
					return table, true
				}
			}
			return "", false
		default:
			return "", false
		}
	}
}

// setMapNames reports whether a SetMap argument may assign a column column
// matches: a map literal (a map[string]any, an sq.Eq) is read key by key,
// through a local variable if need be; a map the scan cannot see may set
// anything, so it counts.
func setMapNames(expr ast.Expr, locals map[string][]ast.Expr, column *regexp.Regexp) bool {
	switch e := expr.(type) {
	case *ast.CompositeLit:
		for _, element := range e.Elts {
			pair, ok := element.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := pair.Key.(*ast.BasicLit)
			if !ok || key.Kind != token.STRING {
				return true
			}
			if name, err := strconv.Unquote(key.Value); err == nil && column.MatchString(name) {
				return true
			}
		}
		return false
	case *ast.Ident:
		assigned := locals[e.Name]
		if len(assigned) == 0 {
			return true
		}
		for _, value := range assigned {
			literal, ok := value.(*ast.CompositeLit)
			if !ok || setMapNames(literal, nil, column) {
				return true
			}
		}
		return false
	default:
		return true
	}
}

// runHeaderWriters names every declaration ("file:Func") that updates
// pipeline_runs and assigns a column column matches: in a SQL string literal,
// or through a Set or SetMap on a query builder chained from
// Update("pipeline_runs"). A column name or map the scan cannot read counts.
func runHeaderWriters(files []goSourceFile, column runHeaderColumns) []string {
	var writers []string
	for _, source := range files {
		for _, decl := range source.file.Decls {
			name := "<package scope>"
			if fn, ok := decl.(*ast.FuncDecl); ok {
				name = fn.Name.Name
			}
			writes := false
			locals := localAssignments(decl)
			ast.Inspect(decl, func(node ast.Node) bool {
				// A query builder writes the column only when this Set or
				// SetMap belongs to an Update of pipeline_runs.
				if call, ok := node.(*ast.CallExpr); ok && len(call.Args) > 0 {
					if selector, ok := call.Fun.(*ast.SelectorExpr); ok && (selector.Sel.Name == "Set" || selector.Sel.Name == "SetMap") {
						if table, ok := builderTable(selector.X, locals, map[string]bool{}); ok && table == "pipeline_runs" {
							if selector.Sel.Name == "Set" {
								if text, ok := stringArgument(call); !ok || column.name.MatchString(text) {
									writes = true
								}
							} else if setMapNames(call.Args[0], locals, column.name) {
								writes = true
							}
						}
					}
				}
				lit, ok := node.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				text, err := strconv.Unquote(lit.Value)
				if err != nil {
					return true
				}
				for _, update := range pipelineRunUpdate.FindAllStringSubmatch(text, -1) {
					if column.inSQL.MatchString(update[1]) {
						writes = true
					}
				}

				return true
			})
			if writes {
				writers = append(writers, source.rel+":"+name)
			}
		}
	}
	sort.Strings(writers)

	return writers
}

// exactlyOneWriter is the rule both single-path guards share.
func exactlyOneWriter(what string, scanned int, writers []string, want string) []string {
	switch {
	case scanned == 0:
		return []string{fmt.Sprintf("the %s scan matched no file; the rule would pass vacuously", what)}
	case len(writers) == 0:
		return []string{fmt.Sprintf("no declaration writes the %s. %s is expected to; if it moved, "+
			"the pattern is now guarding a string", what, want)}
	case len(writers) > 1 || writers[0] != want:
		return []string{fmt.Sprintf("the %s has %d writers, %v; exactly one, %s, is allowed", what, len(writers), writers, want)}
	}

	return nil
}

const (
	runStatusWriter = "atc/db/pipeline_run_result.go:finalizeOutputRun"
	runFenceWriter  = "atc/db/pipeline_run_cancellation.go:acceptRunCancellation"
)

func TestExactlyOneFunctionWritesARunsStatusAndOneRecordsItsCancellationFence(t *testing.T) {
	files := productionGoFiles(t, func(string) bool { return true })
	if len(files) < 500 {
		t.Fatalf("parsed only %d production Go files; the walk failed", len(files))
	}

	for _, problem := range exactlyOneWriter("v2 Run status", len(files), runHeaderWriters(files, runStatusColumn), runStatusWriter) {
		t.Error(problem + ". A Run's status changes once, in the one terminal publication; " +
			"cancellation and ordinary completion both publish through it.")
	}
	for _, problem := range exactlyOneWriter("Run cancellation request and fence", len(files), runHeaderWriters(files, runFenceColumns), runFenceWriter) {
		t.Error(problem + ". The API and any deadline scanner call the one accept/fence " +
			"operation; a second writer is a second way to stop a Run.")
	}
}

// No step configuration can ask for an added capability or a host namespace,
// because nothing that builds a step pod sets one and a sidecar has no field
// to carry one (step_pod_privilege_gate, spec amendment B1 §1). This test
// keeps it so: it fails when production code in atc/worker/jetbridge sets
// Capabilities, HostNetwork, HostPID or HostIPC, or when SidecarConfig has a
// field that could carry a security request. The step pod admission refuses
// such a pod only under --kubernetes-step-privilege=granted; under any, the
// default, it checks only hostPath volumes and their mounts.
func TestNoStepPodCodeSetsACapabilityOrAHostNamespace(t *testing.T) {
	files := productionGoFiles(t, func(rel string) bool {
		return filepath.ToSlash(filepath.Dir(rel)) == "atc/worker/jetbridge"
	})
	for _, problem := range podEscalationFieldsSet(files) {
		t.Error(problem + ". Step pods never add capabilities or join a host namespace.")
	}

	sidecar := productionGoFiles(t, func(rel string) bool { return filepath.ToSlash(rel) == "atc/sidecar.go" })
	for _, problem := range sidecarSecurityFields(sidecar) {
		t.Error(problem + ". A sidecar's security comes from the runtime, never from its config.")
	}
}

var podEscalationField = map[string]bool{"Capabilities": true, "HostNetwork": true, "HostPID": true, "HostIPC": true}

// podEscalationFieldsSet reports every composite-literal key or assignment
// that sets a capability set or a host namespace.
func podEscalationFieldsSet(files []goSourceFile) []string {
	if len(files) == 0 {
		return []string{"the step pod scan matched no file; the rule would pass vacuously"}
	}
	var problems []string
	for _, source := range files {
		ast.Inspect(source.file, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.KeyValueExpr:
				if key, ok := n.Key.(*ast.Ident); ok && podEscalationField[key.Name] {
					problems = append(problems, fmt.Sprintf("%s sets %s", source.rel, key.Name))
				}
			case *ast.AssignStmt:
				for _, lhs := range n.Lhs {
					if sel, ok := lhs.(*ast.SelectorExpr); ok && podEscalationField[sel.Sel.Name] {
						problems = append(problems, fmt.Sprintf("%s sets %s", source.rel, sel.Sel.Name))
					}
				}
			}
			return true
		})
	}
	sort.Strings(problems)

	return problems
}

var securityFieldName = regexp.MustCompile(`(?i)privileg|capabilit|securitycontext|hostnetwork|hostpid|hostipc|runas|seccomp|apparmor|selinux`)

// sidecarSecurityFields reports a SidecarConfig field that could carry a
// security request.
func sidecarSecurityFields(files []goSourceFile) []string {
	var problems []string
	found := false
	for _, source := range files {
		ast.Inspect(source.file, func(node ast.Node) bool {
			spec, ok := node.(*ast.TypeSpec)
			if !ok || spec.Name.Name != "SidecarConfig" {
				return true
			}
			found = true
			if st, ok := spec.Type.(*ast.StructType); ok {
				for _, field := range st.Fields.List {
					for _, name := range field.Names {
						if securityFieldName.MatchString(name.Name) {
							problems = append(problems, fmt.Sprintf("%s: SidecarConfig.%s", source.rel, name.Name))
						}
					}
				}
			}
			return false
		})
	}
	if !found {
		return []string{"no SidecarConfig type was found; the rule would pass vacuously"}
	}

	return problems
}

// kubernetesReach reports every run package whose linked dependencies include
// a Kubernetes client or the JetBridge runtime. deps maps each run package to
// its `go list -deps` listing.
func kubernetesReach(deps map[string][]string) []string {
	if len(deps) == 0 {
		return []string{"the run-package dependency scan listed no package; the rule would pass vacuously"}
	}
	var problems []string
	for pkg, listed := range deps {
		if len(listed) == 0 {
			problems = append(problems, fmt.Sprintf("%s listed no dependency at all; the listing failed", pkg))
		}
		for _, dep := range listed {
			if strings.HasPrefix(dep, "k8s.io/") || strings.HasPrefix(dep, modulePrefix+"atc/worker/jetbridge") {
				problems = append(problems, fmt.Sprintf("%s links %s", pkg, dep))
			}
		}
	}
	sort.Strings(problems)

	return problems
}

func TestTheRunPackagesCannotKillAnythingKubernetesSpecifically(t *testing.T) {
	deps := map[string][]string{}
	for _, pkg := range []string{"atc/runs", "atc/api/pipelinerunserver", "atc/db"} {
		out, err := exec.Command("go", "list", "-deps", "./"+pkg).Output()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				t.Fatalf("go list -deps %s failed: %v\n%s", pkg, err, ee.Stderr)
			}
			t.Fatalf("go list -deps %s failed: %v", pkg, err)
		}
		deps[pkg] = strings.Fields(string(out))
	}

	for _, problem := range kubernetesReach(deps) {
		t.Error(problem + ". Run cancellation stops work only through the executor-neutral " +
			"node protocol (runs.CancellationSourcePlane); a Pod deletion is not an " +
			"acknowledgement and has no business being reachable from core.")
	}
}

// Each guard above objects to a scan that found nothing, and recognises the
// thing it forbids in a fixture of its own.
func TestTheRunContractGuardsFailOnAnEmptyScan(t *testing.T) {
	parse := func(rel, src string) goSourceFile {
		file, err := parser.ParseFile(token.NewFileSet(), rel, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parsing fixture: %v", err)
		}

		return goSourceFile{rel: rel, file: file}
	}

	t.Run("single writers", func(t *testing.T) {
		if problems := exactlyOneWriter("status", 0, nil, "a.go:f"); len(problems) == 0 {
			t.Error("the single-writer rule passed on a scan of no file")
		}
		if problems := exactlyOneWriter("status", 10, nil, "a.go:f"); len(problems) == 0 {
			t.Error("the single-writer rule passed when no writer was found at all")
		}

		files := []goSourceFile{
			parse("a.go", "package a\nfunc f() { _ = `UPDATE pipeline_runs SET status=$2,completed_at=$3 WHERE id=$1` }\n"),
			parse("b.go", "package b\nconst q = \"UPDATE pipeline_runs r SET\\n cancel_requested_at = now() WHERE r.id=$1\"\n"),
			parse("c.go", "package c\nfunc g() { _ = `UPDATE pipeline_runs SET reclaim_retry_after=$2 WHERE status='running'` }\n"),
			parse("d.go", "package d\nfunc h() { _ = `UPDATE pipeline_runs_other SET status=$1` }\n"),
			parse("e.go", "package e\nfunc k() { psql.Update(\"pipeline_runs\").Set(\"status\", 1) }\n"),
			parse("f.go", "package f\nfunc m() { psql.Update(\"pipelines\").Set(\"status\", 1) }\n"),
			parse("g.go", "package g\nfunc n() { psql.Update(\"pipeline_runs\").SetMap(map[string]any{\"status\": 1}) }\n"),
			parse("h.go", "package h\nfunc o() { psql.Update(\"pipeline_runs\").SetMap(sq.Eq{\"reclaim_retry_after\": 1}) }\n"),
			// Two statements in one function: each Set belongs to its own Update.
			parse("i.go", "package i\nfunc p() { psql.Update(\"pipeline_runs\").Set(\"reclaim_retry_after\", 1); psql.Update(\"builds\").Set(\"status\", 1) }\n"),
			// A map held in a local variable is resolved to its literal.
			parse("j.go", "package j\nfunc q() { values := sq.Eq{\"status\": 1}; psql.Update(\"pipeline_runs\").SetMap(values) }\n"),
			// A map the scan cannot see counts as a write: it may set any column.
			parse("k.go", "package k\nfunc r(values map[string]any) { psql.Update(\"pipeline_runs\").SetMap(values) }\n"),
			// A builder held in a local variable keeps its table.
			parse("l.go", "package l\nfunc s() { q := psql.Update(\"pipeline_runs\"); q = q.Set(\"status\", 1) }\n"),
			parse("m.go", "package m\nfunc t(values map[string]any) { psql.Update(\"builds\").SetMap(values) }\n"),
		}
		status := runHeaderWriters(files, runStatusColumn)
		wantStatus := []string{"a.go:f", "e.go:k", "g.go:n", "j.go:q", "k.go:r", "l.go:s"}
		if !slices.Equal(status, wantStatus) {
			t.Errorf("status writers = %v, want %v (a WHERE clause, another table or another column is not a write; an uninspectable map on pipeline_runs is)", status, wantStatus)
		}
		fence := runHeaderWriters(files, runFenceColumns)
		if wantFence := []string{"b.go:<package scope>", "k.go:r"}; !slices.Equal(fence, wantFence) {
			t.Errorf("fence writers = %v, want %v", fence, wantFence)
		}
		if problems := exactlyOneWriter("status", len(files), status, "a.go:f"); len(problems) == 0 {
			t.Error("the single-writer rule accepted a second writer")
		}
		if problems := exactlyOneWriter("status", len(files), status[:1], "a.go:f"); len(problems) != 0 {
			t.Errorf("the single-writer rule objected to the one allowed writer: %v", problems)
		}
	})

	t.Run("pod escalation fields", func(t *testing.T) {
		if problems := podEscalationFieldsSet(nil); len(problems) == 0 {
			t.Error("the escalation rule passed on a scan of no file")
		}
		for name, src := range map[string]string{
			"capabilities": "package a\nvar c = corev1.SecurityContext{Capabilities: &corev1.Capabilities{}}\n",
			"host network": "package a\nvar s = corev1.PodSpec{HostNetwork: true}\n",
			"assigned PID": "package a\nfunc f(s *corev1.PodSpec) { s.HostPID = true }\n",
		} {
			if problems := podEscalationFieldsSet([]goSourceFile{parse("a.go", src)}); len(problems) == 0 {
				t.Errorf("the escalation rule missed %s", name)
			}
		}
		if problems := sidecarSecurityFields(nil); len(problems) == 0 {
			t.Error("the sidecar rule passed with no SidecarConfig")
		}
		if problems := sidecarSecurityFields([]goSourceFile{parse("s.go", "package a\ntype SidecarConfig struct { Name string; Privileged bool }\n")}); len(problems) == 0 {
			t.Error("the sidecar rule missed a Privileged field")
		}
		if problems := sidecarSecurityFields([]goSourceFile{parse("s.go", "package a\ntype SidecarConfig struct { Name string; Image string }\n")}); len(problems) != 0 {
			t.Errorf("the sidecar rule objected to ordinary fields: %v", problems)
		}
	})

	t.Run("kubernetes reach", func(t *testing.T) {
		if problems := kubernetesReach(nil); len(problems) == 0 {
			t.Error("the Kubernetes rule passed on a listing of no package")
		}
		if problems := kubernetesReach(map[string][]string{"atc/runs": nil}); len(problems) == 0 {
			t.Error("the Kubernetes rule passed on an empty dependency listing")
		}
		if problems := kubernetesReach(map[string][]string{"atc/runs": {"context", "k8s.io/client-go/kubernetes"}}); len(problems) == 0 {
			t.Error("the Kubernetes rule missed a linked Kubernetes client")
		}
		if problems := kubernetesReach(map[string][]string{"atc/runs": {"context", modulePrefix + "atc/worker/jetbridge"}}); len(problems) == 0 {
			t.Error("the Kubernetes rule missed the linked JetBridge runtime")
		}
		if problems := kubernetesReach(map[string][]string{"atc/runs": {"context"}}); len(problems) != 0 {
			t.Errorf("the Kubernetes rule objected to a clean listing: %v", problems)
		}
	})
}

// A landing queue admits Runs as its team with nothing to verify against
// (ADR-0011), so whoever can construct a queue principal can act as any team.
// Only the landing queue's own component may, and this is the pin: every
// non-test file that names runs.QueuePrincipal is atc/landing or atc/runs.
func TestTheQueuePrincipalIsConstructedOnlyByTheLandingQueue(t *testing.T) {
	root := repositoryRoot()
	var offenders []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "node_modules" || entry.Name() == ".claude" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel := filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator)))
		if strings.HasPrefix(rel, "atc/runs/") || strings.HasPrefix(rel, "atc/landing/") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(body), "QueuePrincipal") {
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) != 0 {
		t.Fatalf("runs.QueuePrincipal is constructed outside atc/landing: %v", offenders)
	}
}
