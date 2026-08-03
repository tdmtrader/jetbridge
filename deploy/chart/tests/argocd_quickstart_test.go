package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestDocumentedArgoCDQuickstartRendersDeterministically(t *testing.T) {
	app := readArgoQuickstartApplication(t)
	sets := make([]string, 0, len(app.Spec.Source.Helm.Parameters))
	for _, parameter := range app.Spec.Source.Helm.Parameters {
		value := parameter.Value
		if parameter.Name == "kubernetes.artifactHelperImage" {
			value = testArtifactHelperImage
		}
		sets = append(sets, parameter.Name+"="+value)
	}

	flags := []string{"--namespace", app.Spec.Destination.Namespace}
	first, err := runHelmChart(t, "template", flags, sets...)
	if err != nil {
		t.Fatalf("render documented ArgoCD quickstart: %v\n%s", err, first)
	}
	second, err := runHelmChart(t, "template", flags, sets...)
	if err != nil {
		t.Fatalf("render documented ArgoCD quickstart again: %v\n%s", err, second)
	}
	if first != second {
		t.Fatal("documented ArgoCD quickstart produces different desired manifests on consecutive clusterless renders")
	}

	for _, secretName := range []string{
		"concourse-web-signing-key",
		"concourse-artifact-daemon-resolve-capability",
	} {
		if !strings.Contains(first, "secretName: "+secretName) {
			t.Errorf("documented ArgoCD quickstart does not mount external Secret %q", secretName)
		}
	}
}

type argoQuickstartApplication struct {
	Spec struct {
		Source struct {
			Helm struct {
				Parameters []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"parameters"`
			} `json:"helm"`
		} `json:"source"`
		Destination struct {
			Namespace string `json:"namespace"`
		} `json:"destination"`
	} `json:"spec"`
}

func readArgoQuickstartApplication(t *testing.T) argoQuickstartApplication {
	t.Helper()
	readmePath, err := filepath.Abs("../README.md")
	if err != nil {
		t.Fatalf("resolve chart README: %v", err)
	}
	contents, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("read chart README: %v", err)
	}

	quickstart := string(contents)
	marker := "## Quickstart (ArgoCD)"
	markerAt := strings.Index(quickstart, marker)
	if markerAt < 0 {
		t.Fatalf("chart README is missing %q", marker)
	}
	quickstart = quickstart[markerAt+len(marker):]
	openFence := strings.Index(quickstart, "```yaml")
	if openFence < 0 {
		t.Fatal("ArgoCD quickstart is missing its Application YAML")
	}
	quickstart = quickstart[openFence+len("```yaml"):]
	closeFence := strings.Index(quickstart, "```")
	if closeFence < 0 {
		t.Fatal("ArgoCD quickstart Application YAML fence is not closed")
	}

	var app argoQuickstartApplication
	if err := yaml.Unmarshal([]byte(quickstart[:closeFence]), &app); err != nil {
		t.Fatalf("parse ArgoCD quickstart Application: %v", err)
	}
	if app.Spec.Destination.Namespace == "" {
		t.Fatal("ArgoCD quickstart Application has no destination namespace")
	}
	return app
}
