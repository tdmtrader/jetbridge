package jetbridge

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar/executioncontrol"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// exactRoutingContainer is a Run-owned exact step on a job-runtime worker.
func exactRoutingContainer(t *testing.T, clientset *fake.Clientset, kind db.ContainerType, gate bool) *Container {
	t.Helper()
	container := jobRuntimeContainer(t, clientset, kind, runtime.ContainerSpec{
		Dir: "/tmp/build/task",
		ExecutionControl: &runtime.ExecutionControl{
			Version:    runtime.ExecutionControlVersion,
			Phase:      runtime.ControlPhaseAdmitted,
			Identity:   executioncontrol.Identity{ExecutionID: "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", Fence: 1},
			Endpoint:   "https://127.0.0.1:1",
			Capability: "base-capability",
		},
	})
	container.exactTasksOnJobRuntime = gate
	container.outputControls = staticOutputControls{}
	container.executor = &controlExecutor{run: func(int) error { return nil }}
	return container
}

// Production keeps a Run-owned exact execution on a pause pod under the job
// runtime: with the test gate unset, Run creates a pod the web execs into
// and no Job. With it set, only an exact task leaves the pause pod.
func TestAnExactExecutionStaysOnThePausePodUnlessATestRoutesIt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		kind  db.ContainerType
		gate  bool
		toJob bool
	}{
		{"an exact task, gate unset", db.ContainerTypeTask, false, false},
		{"an exact check, gate unset", db.ContainerTypeCheck, false, false},
		{"an exact get, gate set", db.ContainerTypeGet, true, false},
		{"an exact task, gate set", db.ContainerTypeTask, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clientset := fake.NewSimpleClientset()
			container := exactRoutingContainer(t, clientset, tc.kind, tc.gate)
			if got := container.jobRuntime(); got != tc.toJob {
				t.Fatalf("jobRuntime() = %v, want %v", got, tc.toJob)
			}
			if tc.toJob {
				return
			}
			process, err := container.Run(context.Background(), runtime.ProcessSpec{Path: "/bin/sh", Args: []string{"-c", "true"}},
				runtime.ProcessIO{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if _, ok := process.(*execProcess); !ok {
				t.Errorf("Run returned %T, want the exec runtime's process", process)
			}
			jobs, _ := clientset.BatchV1().Jobs("test-ns").List(context.Background(), metav1.ListOptions{})
			if len(jobs.Items) != 0 {
				t.Errorf("%d step Jobs created for an exact execution", len(jobs.Items))
			}
			pod, err := clientset.CoreV1().Pods("test-ns").Get(context.Background(), container.podName, metav1.GetOptions{})
			if err != nil {
				t.Fatalf("no pause pod: %v", err)
			}
			if pod.Labels[runtimeLabelKey] == StepRuntimeJob {
				t.Error("the pause pod carries the job runtime label")
			}
		})
	}
}

// The gate is a test's, never production's. It is an unexported field, so
// only this package can set it, and no file of the package outside its tests
// does: the one assignment copies a worker's value into the container it
// makes. A test sets it on a container (here) or on a worker
// (RouteExactTasksToJobRuntimeForTesting, a _test file), which no production
// binary contains.
func TestNothingInProductionSetsTheExactJobRuntimeGate(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		writes, err := exactGateWrites(file, body)
		if err != nil {
			t.Fatal(err)
		}
		for _, write := range writes {
			t.Errorf("%s sets the exact job-runtime gate: only a test may", write)
		}
	}
}

// The search reads the syntax, not the text: every way to set the field is
// one it finds, and the worker's copy is the one it allows.
func TestTheExactJobRuntimeGateSearchFindsEveryWrite(t *testing.T) {
	for _, tc := range []struct {
		name  string
		body  string
		found int
	}{
		{"the worker's copy", `container.exactTasksOnJobRuntime = w.exactTasksOnJobRuntime`, 0},
		{"a read", `if c.exactTasksOnJobRuntime == true { return }`, 0},
		{"an assignment", `c.exactTasksOnJobRuntime = true`, 1},
		{"an assignment with no space", `c.exactTasksOnJobRuntime=true`, 1},
		{"a copy from something else", `c.exactTasksOnJobRuntime = other.gate`, 1},
		{"its own negation", `c.exactTasksOnJobRuntime = !c.exactTasksOnJobRuntime`, 1},
		{"a tuple assignment", `n, c.exactTasksOnJobRuntime = 1, true`, 1},
		{"a tuple from a call", `c.exactTasksOnJobRuntime, err = parse()`, 1},
		{"a parenthesized target", `(c.exactTasksOnJobRuntime) = true`, 1},
		{"a pointer to it", `p := &c.exactTasksOnJobRuntime; *p = true`, 1},
		{"a pointer through parentheses", `*(&(c.exactTasksOnJobRuntime)) = true`, 1},
		{"a struct literal", `c := Container{exactTasksOnJobRuntime: true}`, 1},
		{"a range target", `for _, c.exactTasksOnJobRuntime = range []bool{true} {}`, 1},
		{"a range key target", `for c.exactTasksOnJobRuntime = range map[bool]int{true: 1} {}`, 1},
		{"an unkeyed Container literal", `c := Container{"handle", nil}`, 1},
		{"an unkeyed Worker literal", `w := &Worker{nil, true}`, 1},
		{"another package's Container literal", `c := corev1.Container{Name: "main"}`, 0},
		{"a whole Container copied over another", `*c = *other`, 1},
		{"a struct copied through a pointer", `*(c) = *(other)`, 1},
		{"a whole Container assigned from a literal", `*c = Container{}`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := "package jetbridge\nfunc f() {\n" + tc.body + "\n}\n"
			writes, err := exactGateWrites("synthetic.go", []byte(src))
			if err != nil {
				t.Fatal(err)
			}
			if len(writes) != tc.found {
				t.Errorf("found %d writes (%v), want %d", len(writes), writes, tc.found)
			}
		})
	}
}

// exactGateWrites is every place in src that may set exactTasksOnJobRuntime:
//   - an assignment to it, tuples and parentheses included, or a range
//     statement's key or value naming it;
//   - its address taken;
//   - a composite literal's key naming it;
//   - an unkeyed literal of a type of this package that holds it (Container,
//     Worker), which sets every field by position;
//   - a whole struct stored through a pointer, *x = *y or *x = Container{...},
//     which overwrites every field at once; this package stores no other
//     struct that way. A store of another value through a pointer (*p = 30)
//     is not one: its type is not known without type-checking, and a
//     function returning a whole Container stored so would be missed.
//
// The one write allowed is the copy of a worker's field into a container's,
// x.exactTasksOnJobRuntime = y.exactTasksOnJobRuntime.
//
// Out of reach of a syntax search, and why that is enough: a value copy
// (c := *other, a struct passed or returned by value) makes a new value
// holding a gate already set, never a gate set where none was; a write
// through reflect or unsafe needs those packages, which no production file
// of this package imports for it (the search would have to read types to say
// more); and another package cannot name the unexported field at all.
func exactGateWrites(filename string, src []byte) ([]string, error) {
	const field = "exactTasksOnJobRuntime"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	isField := func(expr ast.Expr) bool {
		for {
			paren, ok := expr.(*ast.ParenExpr)
			if !ok {
				break
			}
			expr = paren.X
		}
		selector, ok := expr.(*ast.SelectorExpr)
		return ok && selector.Sel.Name == field
	}
	var writes []string
	report := func(node ast.Node) { writes = append(writes, fset.Position(node.Pos()).String()) }
	unparen := func(expr ast.Expr) ast.Expr {
		for {
			paren, ok := expr.(*ast.ParenExpr)
			if !ok {
				return expr
			}
			expr = paren.X
		}
	}
	holders := map[string]bool{"Container": true, "Worker": true}
	// wholeValue is a right-hand side that is a whole struct: another
	// pointer's value, or a literal of a holder type.
	wholeValue := func(expr ast.Expr) bool {
		switch expr := expr.(type) {
		case *ast.StarExpr:
			return true
		case *ast.CompositeLit:
			name, ok := expr.Type.(*ast.Ident)
			return ok && holders[name.Name]
		}
		return false
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.RangeStmt:
			if node.Tok == token.ASSIGN && (isField(node.Key) || isField(node.Value)) {
				report(node)
			}
		case *ast.CompositeLit:
			if name, ok := node.Type.(*ast.Ident); ok && holders[name.Name] {
				for _, element := range node.Elts {
					if _, keyed := element.(*ast.KeyValueExpr); !keyed {
						report(node)
						break
					}
				}
			}
		case *ast.AssignStmt:
			for i, target := range node.Lhs {
				if _, through := unparen(target).(*ast.StarExpr); through && len(node.Rhs) == len(node.Lhs) && wholeValue(unparen(node.Rhs[i])) {
					report(target)
					continue
				}
				if !isField(target) {
					continue
				}
				copied := node.Tok == token.ASSIGN && len(node.Lhs) == 1 && len(node.Rhs) == 1 && isField(node.Rhs[0])
				if !copied {
					report(target)
				}
			}
		case *ast.UnaryExpr:
			if node.Op == token.AND && isField(node.X) {
				report(node)
			}
		case *ast.KeyValueExpr:
			if key, ok := node.Key.(*ast.Ident); ok && key.Name == field {
				report(node)
			}
		}
		return true
	})
	return writes, nil
}

// routeExactTasksToJobRuntime is the gate on one container.
func (c *Container) routeExactTasksToJobRuntime() { c.exactTasksOnJobRuntime = true }
