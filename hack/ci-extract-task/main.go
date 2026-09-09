// Command ci-extract-task lifts a single job's inline task config out of a
// Concourse pipeline file so `fly execute` can run it verbatim.
//
// It exists because the pipeline in deploy/concourse-pipeline.yml is the only
// place the CI tasks are written down: the runner image, the shell script and
// the input names all live inside `config:` blocks that nothing else can read.
// hack/ci-check.sh runs those exact blocks against a ref before it is offered
// for merge, and this is how it gets at them. Re-typing the script into a
// standalone task file would defeat the point -- the copy would drift, and a
// pre-merge check that runs a stale approximation of CI is worse than none.
//
// Usage:
//
//	ci-extract-task <pipeline.yml> <job-name>            # print the task config as YAML
//	ci-extract-task -inputs <pipeline.yml> <job-name>    # print the task's input names
//	ci-extract-task -jobs <pipeline.yml>                 # print every job that has a task
//
// A job with more than one task is rejected rather than guessed at.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"sigs.k8s.io/yaml"
)

type pipelineDoc struct {
	Jobs []struct {
		Name string `json:"name"`
		Plan []step `json:"plan"`
	} `json:"jobs"`
}

type step struct {
	Task   string          `json:"task"`
	Config json.RawMessage `json:"config"`

	// Nesting. These pipelines are nearly flat, but a task hidden inside an
	// in_parallel or a do is still a task.
	InParallel *inParallel `json:"in_parallel"`
	Do         []step      `json:"do"`
	Try        *step       `json:"try"`
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

func flatten(plan []step) []step {
	var out []step

	for _, s := range plan {
		switch {
		case s.InParallel != nil:
			out = append(out, flatten(s.InParallel.Steps)...)
		case len(s.Do) > 0:
			out = append(out, flatten(s.Do)...)
		case s.Try != nil:
			out = append(out, flatten([]step{*s.Try})...)
		default:
			out = append(out, s)
		}
	}

	return out
}

func main() {
	var wantInputs, wantJobs bool

	args := os.Args[1:]
	for len(args) > 0 && len(args[0]) > 1 && args[0][0] == '-' {
		switch args[0] {
		case "-inputs", "--inputs":
			wantInputs = true
		case "-jobs", "--jobs":
			wantJobs = true
		default:
			fatalf("unknown flag %q", args[0])
		}
		args = args[1:]
	}

	if wantJobs && len(args) != 1 {
		fatalf("usage: ci-extract-task -jobs <pipeline.yml>")
	}
	if !wantJobs && len(args) != 2 {
		fatalf("usage: ci-extract-task [-inputs] <pipeline.yml> <job-name>")
	}

	raw, err := os.ReadFile(args[0])
	if err != nil {
		fatalf("%v", err)
	}

	var doc pipelineDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		fatalf("parsing %s: %v", args[0], err)
	}

	if wantJobs {
		for _, job := range doc.Jobs {
			for _, s := range flatten(job.Plan) {
				if s.Task != "" {
					fmt.Println(job.Name)
					break
				}
			}
		}
		return
	}

	jobName := args[1]

	var known []string
	for _, job := range doc.Jobs {
		known = append(known, job.Name)

		if job.Name != jobName {
			continue
		}

		var tasks []step
		for _, s := range flatten(job.Plan) {
			if s.Task != "" {
				tasks = append(tasks, s)
			}
		}

		switch len(tasks) {
		case 0:
			fatalf("job %q has no task step", jobName)
		case 1:
		default:
			fatalf("job %q has %d task steps; this tool extracts one", jobName, len(tasks))
		}

		task := tasks[0]
		if len(task.Config) == 0 {
			fatalf("job %q task %q has no inline config (a `file:` task cannot be extracted)", jobName, task.Task)
		}

		if wantInputs {
			var cfg struct {
				Inputs []struct {
					Name string `json:"name"`
				} `json:"inputs"`
			}
			if err := yaml.Unmarshal(task.Config, &cfg); err != nil {
				fatalf("reading inputs of %q: %v", jobName, err)
			}
			for _, in := range cfg.Inputs {
				fmt.Println(in.Name)
			}
			return
		}

		out, err := yaml.JSONToYAML(task.Config)
		if err != nil {
			fatalf("re-encoding config of %q: %v", jobName, err)
		}
		os.Stdout.Write(out)

		return
	}

	fatalf("no job named %q in %s (jobs: %v)", jobName, args[0], known)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ci-extract-task: "+format+"\n", args...)
	os.Exit(1)
}
