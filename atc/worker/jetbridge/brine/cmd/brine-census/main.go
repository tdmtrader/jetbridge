// brine-census reports the fixed consolidation scope without running scenarios.
// Run from the brine module with: go run ./cmd/brine-census
package main

import (
	"encoding/json"
	"fmt"
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/worker/jetbridge/brine/steps"
)

var features = []string{
	"task-command", "container-run", "pod-lifecycle", "step-closing",
	"step-integration", "volume-streaming", "container-pod",
}

// Freeze these existing files before editing. New Go helpers anywhere in the
// module must be added; moving code outside the module is not a reduction.
var support = []string{
	"task_command", "container_extra", "process", "process_gaps", "closing",
	"integration", "volume_streaming", "exec_target", "tty", "registry",
	"domain", "fixture", "assert", "assert_test", "resources", "tarhelp",
	"container_pod", "container_gaps", "container_lifecycle", "observability",
}

var legacy = []string{
	"behavioral_permutations_restored", "behavioral_runtime_spec_restored",
	"container_restored", "integration_restored", "process_restored",
	"volume_daemonset_restored", "volume_restored",
}

type counts struct {
	Lines     int `json:"lines"`
	CodeLines int `json:"non_comment_nonblank_lines"`
}

func main() {
	if err := report(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func report() error {
	defs := steps.Definitions()
	registry := brine.NewStepRegistry(defs)
	used := map[string]bool{}
	files := map[string]counts{}
	cases := 0
	for _, name := range features {
		path := "features/" + name + ".feature"
		f, err := brine.ParseFeatureFile(path)
		if err != nil {
			return err
		}
		for _, feature := range f.Features {
			for _, scenario := range feature.Scenarios {
				cases++
				for _, step := range scenario.Steps {
					def, _, ok := registry.Lookup(step.Text)
					if !ok {
						return fmt.Errorf("undefined step: %s", step.Text)
					}
					used[def.Pattern()] = true
				}
			}
		}
		files[path] = counts{}
	}
	for _, name := range support {
		files["steps/"+name+".go"] = counts{}
	}
	for _, name := range legacy {
		files["../"+name+"_test.go"] = counts{}
	}
	// New shared executor and its contract tests count even before they exist.
	for _, name := range []string{"local_executor", "local_executor_test", "artifact_handoff", "action", "action_test"} {
		path := "steps/" + name + ".go"
		if _, err := os.Stat(path); err == nil {
			files[path] = counts{}
		}
	}
	total := counts{}
	for path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		c := counts{Lines: strings.Count(string(data), "\n")}
		if filepath.Ext(path) == ".go" {
			fs := token.NewFileSet()
			file := fs.AddFile(path, -1, len(data))
			var scan scanner.Scanner
			scan.Init(file, data, nil, 0)
			lines := map[int]bool{}
			for {
				pos, tok, literal := scan.Scan()
				if tok == token.EOF {
					break
				}
				if tok == token.SEMICOLON && literal == "\n" {
					continue
				}
				start := fs.Position(pos).Line
				for n := 0; n <= strings.Count(literal, "\n"); n++ {
					lines[start+n] = true
				}
			}
			c.CodeLines = len(lines)
		} else {
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if line != "" && !strings.HasPrefix(line, "#") {
					c.CodeLines++
				}
			}
		}
		files[path] = c
		total.Lines += c.Lines
		total.CodeLines += c.CodeLines
	}
	if cases == 0 || len(used) == 0 {
		return fmt.Errorf("empty scenario census")
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{
		"expanded_cases": cases, "used_step_definitions": len(used),
		"all_registered_definitions": len(defs), "files": files, "total": total,
	})
}
