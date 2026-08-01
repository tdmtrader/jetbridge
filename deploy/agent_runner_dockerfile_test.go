package deploy

import (
	"os"
	"strings"
	"testing"
)

func TestAgentRunnerDockerfile(t *testing.T) {
	const (
		claudeURL    = "https://storage.googleapis.com/claude-code-dist-86c565f3-f756-42ad-8dfa-d59b1c096819/claude-code-releases/2.1.212/linux-x64/claude"
		claudeSHA256 = "044a88cf3a5180776617fd3da1238dcbf9141ddec449a39cf7d2af1ac78e684e"
		debianImage  = "debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818"
	)
	dockerfile, err := os.ReadFile("agent-runner/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	broker, err := os.ReadFile("agent-broker/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(dockerfile), "harvest-runner") {
		t.Fatal("agent runner image still packages harvest-runner")
	}
	if strings.Contains(string(dockerfile), "npm install") || !strings.Contains(string(dockerfile), "2.1.212") || !strings.Contains(string(dockerfile), claudeSHA256) {
		t.Fatal("agent runner must use the checksum-pinned native Claude 2.1.212 release")
	}
	for _, want := range []string{
		debianImage,
		claudeURL + " \\",
		"--output /tmp/claude",
		claudeSHA256 + "  /tmp/claude",
		"install -D -m 0555 /tmp/claude /out/claude",
		"COPY --from=claude --chmod=0555 /out/claude /usr/local/bin/claude",
		"COPY --chmod=0555 deploy/agent-runner/smoke.sh /usr/local/bin/agent-runner-image-smoke",
		"RUN /usr/local/bin/agent-runner-image-smoke",
	} {
		if !strings.Contains(string(dockerfile), want) {
			t.Fatalf("runner Dockerfile missing %q", want)
		}
	}
	for _, pin := range []string{debianImage, claudeURL, claudeSHA256} {
		if !strings.Contains(string(broker), pin) || !strings.Contains(string(dockerfile), pin) {
			t.Fatalf("runner and broker Claude pins differ at %q", pin)
		}
	}
	if strings.Contains(string(dockerfile), "2.0.1") {
		t.Fatal("runner Dockerfile retains Claude 2.0.1")
	}
	smoke := strings.Index(string(dockerfile), "RUN /usr/local/bin/agent-runner-image-smoke")
	for _, marker := range []string{
		"COPY --from=build /out/agent-runner /usr/local/bin/agent-runner",
		"COPY --from=build /out/function-runner /usr/local/bin/function-runner",
		"COPY --from=build --chmod=0555 /out/agent-output /usr/local/bin/agent-output",
		"COPY --from=claude --chmod=0555 /out/claude /usr/local/bin/claude",
		"COPY --from=build /usr/local/go /usr/local/go",
		"COPY --chmod=0555 deploy/agent-runner/smoke.sh /usr/local/bin/agent-runner-image-smoke",
	} {
		if installed := strings.LastIndex(string(dockerfile), marker); installed < 0 || installed > smoke {
			t.Fatalf("smoke runs before %q is installed", marker)
		}
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
	if !strings.Contains(string(dockerfile), "COPY --from=build --chmod=0555 /out/agent-output /usr/local/bin/agent-output") {
		t.Fatal("agent runner image must ship a non-writable managed agent-output tool")
	}
}

func TestAgentRunnerMakeTargets(t *testing.T) {
	makefile, err := os.ReadFile("../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"AGENT_RUNNER_IMAGE ?= concourse-agent-runner:dev",
		".PHONY: build-agent-runner-image test-agent-runner-smoke",
		"docker build --platform linux/amd64",
		`--file deploy/agent-runner/Dockerfile`,
		`--tag "$(AGENT_RUNNER_IMAGE)"`,
		`test "$$CONCOURSE_AGENT_RUNNER_SMOKE" = "1"`,
		"ERROR: set CONCOURSE_AGENT_RUNNER_SMOKE=1 to run the runner smoke gate",
		`docker run --rm --platform linux/amd64 --entrypoint /usr/local/bin/agent-runner-image-smoke "$(AGENT_RUNNER_IMAGE)"`,
	} {
		if !strings.Contains(string(makefile), want) {
			t.Fatalf("Makefile missing %q", want)
		}
	}
}

func TestAgentRunnerPipelinePublishesVerifiedImmutableImage(t *testing.T) {
	pipeline := readDeployPipeline(t, "concourse-pipeline.yml")
	task := findDeployPipelineTask(t, pipeline, "build-agent-runner-image", "build-and-push-agent-runner")
	script := deployPipelineTaskScript(t, task)

	for _, required := range []string{
		`SHORT_SHA=$(git rev-parse --short=12 HEAD)`,
		`IMAGE="${GHCR}:${SHORT_SHA}"`,
		`docker build --platform linux/amd64`,
		`--tag "${IMAGE}"`,
		`docker run --rm --platform linux/amd64`,
		`--entrypoint /usr/local/bin/agent-runner-image-smoke "${IMAGE}"`,
		`PUSH_OUTPUT=$(kubectl exec -n cicd "${BUILDER_POD}" -- docker push "${IMAGE}")`,
		`sed -n 's/.*digest: \(sha256:[a-f0-9]\{64\}\).*/\1/p'`,
		`grep -Eq '^sha256:[a-f0-9]{64}$'`,
		`IMMUTABLE_IMAGE="${IMAGE_REPOSITORY}@${DIGEST}"`,
		`docker pull --platform linux/amd64 "${IMMUTABLE_IMAGE}"`,
		`docker image inspect --format '{{.Os}}/{{.Architecture}}' "${IMMUTABLE_IMAGE}"`,
		`CONCOURSE_AGENT_STEP_IMAGE=${IMMUTABLE_IMAGE}`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("agent-runner pipeline lacks immutable-image contract %q", required)
		}
	}
	if strings.Contains(script, "RepoDigests") {
		t.Fatal("agent-runner pipeline trusts local RepoDigests instead of the registry push response")
	}
	requireTextOrder(t, script,
		`docker build --platform linux/amd64`,
		`docker run --rm --platform linux/amd64`,
		`set +x`,
		`docker login ghcr.io`,
		`set -x`,
		`PUSH_OUTPUT=$(kubectl exec -n cicd "${BUILDER_POD}" -- docker push "${IMAGE}")`,
		`DIGEST=$(printf`,
		`docker pull --platform linux/amd64 "${IMMUTABLE_IMAGE}"`,
		`docker image inspect --format '{{.Os}}/{{.Architecture}}' "${IMMUTABLE_IMAGE}"`,
		`CONCOURSE_AGENT_STEP_IMAGE=${IMMUTABLE_IMAGE}`,
	)
	if !strings.Contains(script, "done\nkubectl exec -n cicd \"${BUILDER_POD}\" -- docker info >/dev/null") {
		t.Fatal("agent-runner pipeline does not fail closed after its Docker readiness loop")
	}
}

func TestAgentRunnerDeploymentRunbookOrdersCompatibilityWindow(t *testing.T) {
	raw, err := os.ReadFile("../docs/agentic/V3_CUTOVER_DEPLOY.md")
	if err != nil {
		t.Fatal(err)
	}
	runbook := string(raw)
	sectionStart := strings.Index(runbook, "## Post-upgrade sequence")
	sectionEnd := strings.Index(runbook, "### Permanent coupling")
	if sectionStart < 0 || sectionEnd <= sectionStart {
		t.Fatal("deployment runbook lacks the recurring deployment sequence")
	}
	section := runbook[sectionStart:sectionEnd]
	requireTextOrder(t, section,
		"Pause new agent dispatch",
		"build-agent-runner-image",
		"agent-runner-image-smoke",
		"CONCOURSE_AGENT_STEP_IMAGE",
		"Deploy the matching web artifact",
		"Verify the running configuration",
		"Re-import",
		"Resume agent dispatch",
	)
	normalizedSection := strings.Join(strings.Fields(section), " ")
	for _, required := range []string{"positive budget slice", "unsupported", "--max-budget-usd"} {
		if !strings.Contains(normalizedSection, required) {
			t.Errorf("deployment runbook lacks budget-smoke warning %q", required)
		}
	}
}
