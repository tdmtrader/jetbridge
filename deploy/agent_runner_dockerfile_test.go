package deploy

import (
	"os"
	"strings"
	"testing"
)

func TestAgentRunnerDockerfile(t *testing.T) {
	dockerfile, err := os.ReadFile("agent-runner/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(dockerfile), "harvest-runner") {
		t.Fatal("agent runner image still packages harvest-runner")
	}
	if !strings.Contains(string(dockerfile), "go build -o /out/agent-runner ./cmd/agent-runner") {
		t.Fatal("agent runner image no longer builds agent-runner")
	}
	if !strings.Contains(string(dockerfile), "go build -o /out/function-runner ./cmd/function-runner") {
		t.Fatal("agent runner image no longer builds function-runner")
	}
	if !strings.Contains(string(dockerfile), "COPY --from=build /out/function-runner /usr/local/bin/function-runner") {
		t.Fatal("agent runner image builds function-runner but does not ship it")
	}
	if !strings.Contains(string(dockerfile), "go build -o /out/agent-output ./cmd/agent-output") {
		t.Fatal("agent runner image no longer builds the managed agent-output tool")
	}
	if !strings.Contains(string(dockerfile), "COPY --from=build /out/agent-output /usr/local/bin/agent-output") {
		t.Fatal("agent runner image builds agent-output but does not ship it")
	}
}
