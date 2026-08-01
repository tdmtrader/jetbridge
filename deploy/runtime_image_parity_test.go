package deploy_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The build-image job ships registry.home/jetbridge from a Dockerfile inlined
// in the pipeline, while Dockerfile.build declares the runtime image for
// everything else. They have drifted before: the inline copy omitted git,
// which silently made every repository/v1 seal and the ATC direct-Git
// publisher impossible on the deployed cluster while every test stayed green
// on hosts that happen to have git. This pins the runtime package set in both
// places to the same list.
var runtimePackages = []string{
	"ca-certificates",
	"dumb-init",
	"git=1:2.34.1-1ubuntu1.17",
}

func TestPipelineInlineRuntimeImageInstallsTheDeclaredPackages(t *testing.T) {
	pipeline := read(t, "concourse-pipeline.yml")
	inline := inlineDockerfile(t, pipeline)
	for _, pkg := range runtimePackages {
		if !strings.Contains(inline, pkg) {
			t.Errorf("pipeline inline runtime Dockerfile does not install %q:\n%s", pkg, inline)
		}
	}
}

func TestDockerfileBuildRuntimeStageInstallsTheDeclaredPackages(t *testing.T) {
	dockerfile := read(t, "../Dockerfile.build")
	stages := strings.Split(dockerfile, "\nFROM ")
	runtime := stages[len(stages)-1]
	for _, pkg := range runtimePackages {
		if !strings.Contains(runtime, pkg) {
			t.Errorf("Dockerfile.build runtime stage does not install %q:\n%s", pkg, runtime)
		}
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// inlineDockerfile extracts the heredoc the build-image job writes to
// /tmp/Dockerfile.
func inlineDockerfile(t *testing.T, pipeline string) string {
	t.Helper()
	block := regexp.MustCompile(`(?s)cat <<'DOCKERFILE' > /tmp/Dockerfile\n(.*?)\n\s*DOCKERFILE\n`)
	match := block.FindStringSubmatch(pipeline)
	if match == nil {
		t.Fatal("could not find the inline Dockerfile heredoc in concourse-pipeline.yml")
	}
	return match[1]
}
