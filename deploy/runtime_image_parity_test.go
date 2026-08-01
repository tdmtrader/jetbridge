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
	pipeline := readDeployFile(t, "concourse-pipeline.yml")
	inline := inlineDockerfile(t, pipeline)
	assertInstallsDeclaredPackages(t, inline)
}

func TestDockerfileBuildRuntimeImageStageInstallsTheDeclaredPackages(t *testing.T) {
	dockerfile := readDeployFile(t, "../Dockerfile.build")
	stages := strings.Split(dockerfile, "\nFROM ")
	runtime := stages[len(stages)-1]
	assertInstallsDeclaredPackages(t, runtime)
}

// assertInstallsDeclaredPackages checks runtimePackages against only the
// apt-get install argument list within region, not the whole region. The
// region also contains an ENTRYPOINT line naming "dumb-init" as the exec
// target, so asserting against the whole region is satisfied by that line
// regardless of what apt-get actually installs — it would pass even if
// dumb-init were dropped from the install list entirely.
func assertInstallsDeclaredPackages(t *testing.T, region string) {
	t.Helper()
	start := strings.Index(region, "apt-get install")
	if start < 0 {
		t.Fatalf("region has no apt-get install invocation:\n%s", region)
	}
	end := strings.Index(region, "rm -rf /var/lib/apt/lists/*")
	if end < 0 {
		t.Fatalf("region has no apt cleanup (rm -rf /var/lib/apt/lists/*):\n%s", region)
	}
	if end < start {
		t.Fatalf("apt cleanup appears before apt-get install in region:\n%s", region)
	}
	installList := region[start:end]
	for _, pkg := range runtimePackages {
		if !strings.Contains(installList, pkg) {
			t.Errorf("apt-get install list does not include %q:\n%s", pkg, installList)
		}
	}
}

func readDeployFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// inlineDockerfile extracts the heredoc the build-image job writes to
// /tmp/Dockerfile. It fails loudly if the marker is missing or appears more
// than once — deploy/test-pipeline.yml already has a byte-identical marker
// for an unrelated job, so a second occurrence in concourse-pipeline.yml
// itself must not be silently resolved by taking the first match.
func inlineDockerfile(t *testing.T, pipeline string) string {
	t.Helper()
	block := regexp.MustCompile(`(?s)cat <<'DOCKERFILE' > /tmp/Dockerfile\n(.*?)\n\s*DOCKERFILE\n`)
	matches := block.FindAllStringSubmatch(pipeline, -1)
	if len(matches) != 1 {
		t.Fatalf("expected exactly one inline Dockerfile heredoc in concourse-pipeline.yml, found %d", len(matches))
	}
	return matches[0][1]
}
