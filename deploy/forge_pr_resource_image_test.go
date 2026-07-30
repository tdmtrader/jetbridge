package deploy

import (
	"os"
	"strings"
	"testing"
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
