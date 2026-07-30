package deploy

import (
	"os"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestForgePRResourceDockerfile(t *testing.T) {
	raw, err := os.ReadFile("forge-pr-resource.Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	dockerfile := string(raw)
	for _, required := range []string{"go build", "./cmd/forge-pr-resource", "git ca-certificates", "/opt/resource/check", "/opt/resource/in", "/opt/resource/out", "--chmod=0555"} {
		if !strings.Contains(dockerfile, required) {
			t.Fatalf("Dockerfile missing %q", required)
		}
	}
	if strings.Contains(strings.ToLower(dockerfile), "arg token") || strings.Contains(strings.ToLower(dockerfile), "env token") || strings.Contains(dockerfile, "ENTRYPOINT") {
		t.Fatal("Dockerfile must not embed credentials or rely on an entrypoint")
	}
}

func TestForgePRResourceReleaseJobPublishesAnExactDigestContract(t *testing.T) {
	raw, err := os.ReadFile("concourse-pipeline.yml")
	if err != nil {
		t.Fatal(err)
	}
	var pipeline struct {
		Jobs []struct {
			Name string `yaml:"name"`
			Plan []struct {
				Get        string   `yaml:"get"`
				Task       string   `yaml:"task"`
				Trigger    bool     `yaml:"trigger"`
				Passed     []string `yaml:"passed"`
				Privileged bool     `yaml:"privileged"`
				Config     struct {
					Inputs []struct {
						Name string `yaml:"name"`
					} `yaml:"inputs"`
					Params map[string]string `yaml:"params"`
					Run    struct {
						Path string   `yaml:"path"`
						Args []string `yaml:"args"`
					} `yaml:"run"`
				} `yaml:"config"`
			} `yaml:"plan"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &pipeline); err != nil {
		t.Fatalf("parse release pipeline: %v", err)
	}

	var job *struct {
		Name string `yaml:"name"`
		Plan []struct {
			Get        string   `yaml:"get"`
			Task       string   `yaml:"task"`
			Trigger    bool     `yaml:"trigger"`
			Passed     []string `yaml:"passed"`
			Privileged bool     `yaml:"privileged"`
			Config     struct {
				Inputs []struct {
					Name string `yaml:"name"`
				} `yaml:"inputs"`
				Params map[string]string `yaml:"params"`
				Run    struct {
					Path string   `yaml:"path"`
					Args []string `yaml:"args"`
				} `yaml:"run"`
			} `yaml:"config"`
		} `yaml:"plan"`
	}
	for index := range pipeline.Jobs {
		if pipeline.Jobs[index].Name == "build-forge-pr-resource-image" {
			job = &pipeline.Jobs[index]
			break
		}
	}
	if job == nil {
		t.Fatal("release pipeline lacks build-forge-pr-resource-image job")
	}
	if len(job.Plan) != 2 {
		t.Fatalf("forge-pr resource job plan has %d steps, want 2", len(job.Plan))
	}
	if get := job.Plan[0]; get.Get != "repo" || get.Trigger || !slices.Equal(get.Passed, []string{"unit-tests"}) {
		t.Fatalf("forge-pr resource source gate = %#v, want manual repo gated by unit-tests", get)
	}
	task := job.Plan[1]
	if task.Task != "build-and-push-forge-pr-resource" || !task.Privileged {
		t.Fatalf("forge-pr resource build step = %#v, want privileged build-and-push task", task)
	}
	if len(task.Config.Inputs) != 1 || task.Config.Inputs[0].Name != "repo" {
		t.Fatalf("forge-pr resource build inputs = %#v, want repo only", task.Config.Inputs)
	}
	if task.Config.Params["GITHUB_TOKEN"] != "((github-token))" {
		t.Fatalf("forge-pr resource build token source = %q, want pipeline credential interpolation", task.Config.Params["GITHUB_TOKEN"])
	}
	if task.Config.Run.Path != "sh" || len(task.Config.Run.Args) == 0 {
		t.Fatalf("forge-pr resource build run = %#v, want shell script", task.Config.Run)
	}
	script := task.Config.Run.Args[len(task.Config.Run.Args)-1]
	for _, required := range []string{
		"deploy/forge-pr-resource.Dockerfile",
		`GHCR="ghcr.io/tdmtrader/forge-pr-resource"`,
		`docker push ${GHCR}:${SHORT_SHA}`,
		`FORGE_PR_RESOURCE_IMAGE=${GHCR}@${RESOURCE_DIGEST}`,
		`^sha256:[a-f0-9]{64}$`,
		`FORGE_PR_RESOURCE_IMAGE=${FORGE_PR_RESOURCE_IMAGE}`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("forge-pr resource release script lacks %q", required)
		}
	}
}
