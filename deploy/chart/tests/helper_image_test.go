package tests

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// The artifact helper's init containers carry warrants, so its default is
// pinned by digest, and the chart and the binary pin the same image. The K3s
// tiers preload that image so their clusters run the helper the chart
// defaults to without pulling it; a re-pin that misses one of them leaves a
// tier pulling from Docker Hub, or the binary running a helper the chart
// does not name.
func TestTheHelperImageDefaultIsOnePinnedImageEverywhere(t *testing.T) {
	root := repoRoot(t)
	read := func(path string) string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}

	var values struct {
		Kubernetes struct {
			ArtifactHelperImage string `json:"artifactHelperImage"`
		} `json:"kubernetes"`
	}
	if err := yaml.Unmarshal([]byte(read("deploy/chart/values.yaml")), &values); err != nil {
		t.Fatal(err)
	}
	image := values.Kubernetes.ArtifactHelperImage
	if !regexp.MustCompile(`^[a-z0-9./-]+:[A-Za-z0-9._-]+@sha256:[0-9a-f]{64}$`).MatchString(image) {
		t.Fatalf("kubernetes.artifactHelperImage %q is not pinned as name:tag@sha256:<digest>", image)
	}

	binary := regexp.MustCompile(`DefaultArtifactHelperImage = "([^"]+)"`).FindStringSubmatch(read("atc/worker/jetbridge/config.go"))
	if binary == nil {
		t.Fatal("atc/worker/jetbridge/config.go declares no DefaultArtifactHelperImage")
	}
	if binary[1] != image {
		t.Errorf("the binary's DefaultArtifactHelperImage is %q, the chart's default %q", binary[1], image)
	}

	for _, path := range []string{"deploy/k8s-e2e-pipeline.yml", "topgun/k8s_behavioral/cluster_lifecycle_test.go"} {
		if !strings.Contains(read(path), image) {
			t.Errorf("%s does not preload the chart's helper image %s", path, image)
		}
	}
}
