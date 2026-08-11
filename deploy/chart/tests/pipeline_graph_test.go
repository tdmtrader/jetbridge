package tests

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// A Concourse task declares the directories it wants as `inputs`, and the
// scheduler refuses to run it unless every one of them exists in the build
// plan. Outputs do NOT cross job boundaries -- a job sees only its own steps
// and whatever it `get`s -- so a task input satisfied three jobs earlier is not
// satisfied at all.
//
// 473829956f shipped exactly that: it added a `web-public` output to
// build-image's build-frontend task and pasted the consuming block into
// tag-rc's create-rc-tag as well, a job that runs *earlier* and produces
// nothing of the sort. The task would have failed on its missing input,
// breaking the chain one job before the one the commit set out to fix. Nothing
// caught it -- the YAML is well-formed, and `fly validate-pipeline` checks
// schema, not reachability.
//
// So walk the graph: for every task, every declared input must be produced by
// an earlier step in the same job.
func TestPipelineTaskInputsAreSatisfied(t *testing.T) {
	for _, path := range pipelineFiles(t) {
		pipeline := loadPipeline(t, path)
		name := filepath.Base(path)

		for _, job := range pipeline.Jobs {
			// Artifact names visible at the current point in the plan.
			available := map[string]bool{}

			for _, step := range flattenPlan(job.Plan) {
				if step.Get != "" {
					// `get: x` produces a directory named x, unless renamed.
					produced := step.Get
					if step.Resource != "" {
						produced = step.Get
					}
					available[produced] = true
				}

				if step.Put != "" {
					available[step.Put] = true
				}

				if step.Task == "" {
					continue
				}

				for _, in := range step.Config.Inputs {
					if in.Optional {
						continue
					}
					if !available[in.Name] {
						t.Errorf(
							"%s: job %q, task %q declares input %q, "+
								"which no earlier step in that job produces "+
								"(available: %s).\n"+
								"Task outputs do not cross jobs -- if it is built "+
								"elsewhere, that job must build it too, or pass it "+
								"through a resource.",
							name, job.Name, step.Task, in.Name,
							strings.Join(sortedKeys(available), ", "),
						)
					}
				}

				for _, out := range step.Config.Outputs {
					available[out.Name] = true
				}
			}
		}
	}
}

// A task that declares an output nothing writes hands the next task an empty
// directory. That is how a stale frontend ships: the consumer's guard checks
// the file is non-empty, but a guard nobody reaches proves nothing. Require
// that a declared output is at least mentioned by the script that claims it.
func TestPipelineDeclaredOutputsAreWritten(t *testing.T) {
	for _, path := range pipelineFiles(t) {
		pipeline := loadPipeline(t, path)
		name := filepath.Base(path)

		for _, job := range pipeline.Jobs {
			for _, step := range flattenPlan(job.Plan) {
				if step.Task == "" {
					continue
				}

				script := strings.Join(step.Config.Run.Args, "\n")

				for _, out := range step.Config.Outputs {
					if !strings.Contains(script, out.Name) {
						t.Errorf(
							"%s: job %q, task %q declares output %q but its "+
								"script never mentions it, so the next task "+
								"receives an empty directory.",
							name, job.Name, step.Task, out.Name,
						)
					}
				}
			}
		}
	}
}

// `attempts: N` reruns the whole task, which is right for a flaky download and
// wrong for an operation that destroys history.
//
// The bar here is deliberately narrow. Most mutations in this pipeline are
// idempotent -- `docker push` of identical content, `kubectl apply`, a rollout
// restart -- and re-running them costs time, not correctness. What is not
// recoverable is a force-push or a remote-ref delete: the second attempt cannot
// tell whether the first one already moved the ref, and whatever was there
// before is gone either way.
//
// Both used to be here. The RC tag was force-pushed under `attempts: 2` -- a
// retried force-push, which is not a record of anything -- and the release task
// force-pushed HEAD to main, silently discarding any commit on main that was
// not an ancestor. Both are gone; this keeps them gone.
func TestPipelineDoesNotRetryDestructiveGit(t *testing.T) {
	destructive := []string{
		"git push --force",
		"git push -f",
		"git push --delete",
		"git push origin :",
	}

	for _, path := range pipelineFiles(t) {
		pipeline := loadPipeline(t, path)
		name := filepath.Base(path)

		for _, job := range pipeline.Jobs {
			for _, step := range flattenPlan(job.Plan) {
				if step.Task == "" {
					continue
				}

				// Comments in these scripts quote the very commands they
				// explain the removal of -- the release task documents the
				// `git push --force origin HEAD:refs/heads/main` it no longer
				// runs. Matching prose as if it were code makes this test fire
				// on its own changelog.
				script := stripShellComments(strings.Join(step.Config.Run.Args, "\n"))
				for _, d := range destructive {
					if !strings.Contains(script, d) {
						continue
					}

					// A force-push is worth flagging on its own; under
					// `attempts:` it is worse. Report either way, and say which.
					if step.Attempts > 1 {
						t.Errorf(
							"%s: job %q, task %q runs %q under attempts: %d. "+
								"A retried force-push cannot tell whether the "+
								"first attempt already moved the ref.",
							name, job.Name, step.Task, d, step.Attempts,
						)
					} else {
						t.Errorf(
							"%s: job %q, task %q runs %q. CI does not rewrite "+
								"remote history; whatever it overwrites is "+
								"unrecoverable and unlogged.",
							name, job.Name, step.Task, d,
						)
					}
				}
			}
		}
	}
}

type pipelineDoc struct {
	Jobs []struct {
		Name string `json:"name"`
		Plan []step `json:"plan"`
	} `json:"jobs"`
}

type step struct {
	Get      string `json:"get"`
	Put      string `json:"put"`
	Task     string `json:"task"`
	Resource string `json:"resource"`
	Attempts int    `json:"attempts"`

	// Nesting. These pipelines are nearly flat, but test-pipeline.yml uses
	// in_parallel, and a step hidden inside one is still a step.
	InParallel *inParallel `json:"in_parallel"`
	Do         []step      `json:"do"`
	Try        *step       `json:"try"`

	Config struct {
		Inputs []struct {
			Name     string `json:"name"`
			Optional bool   `json:"optional"`
		} `json:"inputs"`
		Outputs []struct {
			Name string `json:"name"`
		} `json:"outputs"`
		Run struct {
			Path string   `json:"path"`
			Args []string `json:"args"`
		} `json:"run"`
	} `json:"config"`
}

// in_parallel accepts either a bare list of steps or a mapping with a `steps`
// key; both appear in the wild.
type inParallel struct {
	Steps []step `json:"steps"`
}

func (p *inParallel) UnmarshalJSON(b []byte) error {
	var asList []step
	if err := yaml.Unmarshal(b, &asList); err == nil {
		p.Steps = asList
		return nil
	}

	var asMap struct {
		Steps []step `json:"steps"`
	}
	if err := yaml.Unmarshal(b, &asMap); err != nil {
		return err
	}
	p.Steps = asMap.Steps

	return nil
}

// flattenPlan returns the steps in execution order. Steps inside in_parallel
// run concurrently, so treating them as sequential is a deliberate
// simplification: it can only make this check more permissive about ordering,
// never less, and every real finding here is a missing artifact rather than a
// racy one.
func flattenPlan(plan []step) []step {
	var out []step

	for _, s := range plan {
		switch {
		case s.InParallel != nil:
			out = append(out, flattenPlan(s.InParallel.Steps)...)
		case len(s.Do) > 0:
			out = append(out, flattenPlan(s.Do)...)
		case s.Try != nil:
			out = append(out, flattenPlan([]step{*s.Try})...)
		default:
			out = append(out, s)
		}
	}

	return out
}

// stripShellComments removes whole-line and trailing `#` comments so a matcher
// sees only what the shell would run. It is deliberately simple: a `#` inside a
// quoted string is treated as a comment too. That direction of error only makes
// the matcher blinder, never noisier, which is the right way for it to be
// wrong.
func stripShellComments(script string) string {
	var kept []string

	for _, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if i := strings.Index(line, " #"); i >= 0 {
			line = line[:i]
		}
		kept = append(kept, line)
	}

	return strings.Join(kept, "\n")
}

func pipelineFiles(t *testing.T) []string {
	t.Helper()

	root := repoRoot(t)
	matches, err := filepath.Glob(filepath.Join(root, "deploy", "*.yml"))
	if err != nil {
		t.Fatalf("globbing deploy/*.yml: %v", err)
	}

	var pipelines []string
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			t.Fatalf("reading %s: %v", m, err)
		}
		// Chart values and similar live here too; a pipeline is the thing with
		// a jobs: list.
		var doc pipelineDoc
		if err := yaml.Unmarshal(b, &doc); err != nil {
			continue
		}
		if len(doc.Jobs) > 0 {
			pipelines = append(pipelines, m)
		}
	}

	if len(pipelines) == 0 {
		t.Fatal("found no pipeline files under deploy/ -- this test would pass vacuously")
	}

	sort.Strings(pipelines)

	return pipelines
}

func loadPipeline(t *testing.T, path string) pipelineDoc {
	t.Helper()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	var doc pipelineDoc
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}

	return doc
}

func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	t.Fatal(fmt.Sprint("could not find repo root (no go.mod above ", dir, ")"))

	return ""
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	return keys
}
