package deploy

import (
	"os"
	"strings"
	"testing"
)

func TestATCRuntimeImageContainsControlledGitWithoutPublisherCredentials(t *testing.T) {
	const jammyGitPackageVersion = "1:2.34.1-1ubuntu1.17"

	dockerfile, err := os.ReadFile("../Dockerfile.build")
	if err != nil {
		t.Fatal(err)
	}
	text := string(dockerfile)
	finalStage := strings.LastIndex(text, "FROM ubuntu:22.04")
	if finalStage < 0 {
		t.Fatal("Dockerfile.build has no final Ubuntu runtime stage")
	}
	runtime := text[finalStage:]
	if !strings.Contains(runtime, "\n      git="+jammyGitPackageVersion+" \\") {
		t.Fatalf("final ATC runtime stage does not install exact Jammy Git package %s", jammyGitPackageVersion)
	}
	if !strings.Contains(runtime, "\nUSER 65534:65534\n") {
		t.Fatal("final ATC runtime stage is not pinned to the chart's non-root identity")
	}
	for _, forbidden := range []string{
		"COPY publisher",
		"COPY --from=publisher",
		"CONCOURSE_AGENT_PUBLISHER_CREDENTIAL",
		"/run/concourse-publisher",
	} {
		if strings.Contains(runtime, forbidden) {
			t.Fatalf("final runtime image embeds publisher credential material or paths via %q", forbidden)
		}
	}
}
