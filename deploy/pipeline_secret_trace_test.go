package deploy

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type deployPipeline struct {
	Resources []deployPipelineResource `yaml:"resources"`
	Jobs      []deployPipelineJob      `yaml:"jobs"`
}

type deployPipelineResource struct {
	Name   string         `yaml:"name"`
	Type   string         `yaml:"type"`
	Source map[string]any `yaml:"source"`
}

type deployPipelineJob struct {
	Name string               `yaml:"name"`
	Plan []deployPipelineStep `yaml:"plan"`
}

type deployPipelineStep struct {
	Get        string         `yaml:"get"`
	Put        string         `yaml:"put"`
	Task       string         `yaml:"task"`
	Privileged bool           `yaml:"privileged"`
	Timeout    string         `yaml:"timeout"`
	Attempts   int            `yaml:"attempts"`
	Params     map[string]any `yaml:"params"`
	Passed     []string       `yaml:"passed"`
	Config     struct {
		Inputs []struct {
			Name string `yaml:"name"`
		} `yaml:"inputs"`
		Outputs []struct {
			Name string `yaml:"name"`
		} `yaml:"outputs"`
		Params map[string]string `yaml:"params"`
		Run    struct {
			Path string   `yaml:"path"`
			Args []string `yaml:"args"`
		} `yaml:"run"`
	} `yaml:"config"`
}

func readDeployPipeline(t *testing.T, path string) deployPipeline {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var pipeline deployPipeline
	if err := yaml.Unmarshal(raw, &pipeline); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return pipeline
}

func findDeployPipelineTask(t *testing.T, pipeline deployPipeline, jobName, taskName string) deployPipelineStep {
	t.Helper()
	for _, job := range pipeline.Jobs {
		if job.Name != jobName {
			continue
		}
		for _, step := range job.Plan {
			if step.Task == taskName {
				return step
			}
		}
		t.Fatalf("job %q has no task %q", jobName, taskName)
	}
	t.Fatalf("pipeline has no job %q", jobName)
	return deployPipelineStep{}
}

func deployPipelineTaskScript(t *testing.T, step deployPipelineStep) string {
	t.Helper()
	if step.Config.Run.Path != "sh" || len(step.Config.Run.Args) == 0 {
		t.Fatalf("task %q run = %#v, want shell script", step.Task, step.Config.Run)
	}
	return step.Config.Run.Args[len(step.Config.Run.Args)-1]
}

var (
	scriptEnablesXtrace     = regexp.MustCompile(`(?m)^[[:space:]]*set[[:space:]]+(-[[:alpha:]]*x[[:alpha:]]*|-o[[:space:]]+xtrace)([[:space:]]|$)`)
	scriptDisablesXtrace    = regexp.MustCompile(`^[[:space:]]*set[[:space:]]+\+x([[:space:]]|$)`)
	singleDollarParameterRE = regexp.MustCompile(`\$%s([^[:alnum:]_]|$)`)
	bracedParameterRE       = regexp.MustCompile(`\$\{%s(?:[^[:alnum:]_]|$)`)
)

func TestPipelineTasksDoNotTraceServiceAccountTokens(t *testing.T) {
	const projectedServiceAccountDirectory = "/var/run/secrets/kubernetes.io/serviceaccount"
	tokenTaskCount := 0

	for _, path := range []string{"concourse-pipeline.yml", "borg-pipeline.yml"} {
		pipeline := readDeployPipeline(t, path)
		for _, job := range pipeline.Jobs {
			for _, step := range job.Plan {
				if step.Task == "" || len(step.Config.Run.Args) == 0 {
					continue
				}
				script := step.Config.Run.Args[len(step.Config.Run.Args)-1]
				readsProjectedToken := strings.Contains(script, projectedServiceAccountDirectory+"/token") ||
					(strings.Contains(script, projectedServiceAccountDirectory) && strings.Contains(script, `cat "$SA_DIR/token"`))
				if !readsProjectedToken {
					continue
				}

				tokenTaskCount++
				if shellArgumentsEnableXtrace(step.Config.Run.Args[:len(step.Config.Run.Args)-1]) || scriptEnablesXtrace.MatchString(script) {
					t.Errorf("%s job %q task %q traces projected service-account token consumption", path, job.Name, step.Task)
				}
			}
		}
	}

	if tokenTaskCount != 3 {
		t.Fatalf("projected service-account token tasks = %d, want exactly 3", tokenTaskCount)
	}
}

func TestPipelineTasksDoNotTraceParameterizedSecrets(t *testing.T) {
	for _, path := range []string{"concourse-pipeline.yml", "borg-pipeline.yml"} {
		pipeline := readDeployPipeline(t, path)
		for _, job := range pipeline.Jobs {
			for _, step := range job.Plan {
				secretParams := deployPipelineSecretParams(step.Config.Params)
				if step.Task == "" || len(secretParams) == 0 {
					continue
				}

				script := deployPipelineTaskScript(t, step)
				xtraceEnabled := shellArgumentsEnableXtrace(step.Config.Run.Args[:len(step.Config.Run.Args)-1])
				for _, line := range strings.Split(script, "\n") {
					if shellLineDisablesXtrace(line) {
						xtraceEnabled = false
					}
					if xtraceEnabled {
						for _, name := range secretParams {
							if shellLineExpandsParameter(line, name) {
								t.Errorf("%s job %q task %q expands parameter %q while xtrace is active", path, job.Name, step.Task, name)
							}
						}
					}
					if shellLineEnablesXtrace(line) {
						xtraceEnabled = true
					}
				}
			}
		}
	}
}

func deployPipelineSecretParams(params map[string]string) []string {
	var names []string
	for name := range params {
		if strings.Contains(name, "TOKEN") || strings.Contains(name, "PASSWORD") || strings.Contains(name, "SECRET") {
			names = append(names, name)
		}
	}
	return names
}

func shellLineExpandsParameter(line, name string) bool {
	return regexp.MustCompile(fmt.Sprintf(singleDollarParameterRE.String(), regexp.QuoteMeta(name))).MatchString(line) ||
		regexp.MustCompile(fmt.Sprintf(bracedParameterRE.String(), regexp.QuoteMeta(name))).MatchString(line)
}

func shellLineDisablesXtrace(line string) bool {
	return scriptDisablesXtrace.MatchString(line)
}

func shellLineEnablesXtrace(line string) bool {
	return regexp.MustCompile(`^[[:space:]]*set[[:space:]]+-x([[:space:]]|$)`).MatchString(line)
}

func shellArgumentsEnableXtrace(arguments []string) bool {
	for index, argument := range arguments {
		if argument == "-o" && index+1 < len(arguments) && arguments[index+1] == "xtrace" {
			return true
		}
		if strings.HasPrefix(argument, "-") && !strings.HasPrefix(argument, "--") && strings.Contains(strings.TrimPrefix(argument, "-"), "x") {
			return true
		}
	}
	return false
}
