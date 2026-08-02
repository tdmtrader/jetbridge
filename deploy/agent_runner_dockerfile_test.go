package deploy

import (
	"os"
	"os/exec"
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
	sandbox := strings.Index(string(dockerfile), "ENV IS_SANDBOX=1")
	if sandbox < 0 || sandbox >= smoke {
		t.Fatal("agent runner image must enable IS_SANDBOX before the root Claude smoke")
	}
	for _, marker := range []string{
		"COPY --from=build /out/agent-runner /usr/local/bin/agent-runner",
		"COPY --from=build /out/function-runner /usr/local/bin/function-runner",
		"COPY --from=build --chmod=0555 /out/agent-output /usr/local/bin/agent-output",
		"COPY --from=claude --chmod=0555 /out/claude /usr/local/bin/claude",
		"COPY --from=build /usr/local/go /usr/local/go",
		"COPY --chmod=0555 deploy/agent-runner/smoke.sh /usr/local/bin/agent-runner-image-smoke",
	} {
		if installed := strings.LastIndex(string(dockerfile), marker); installed < 0 || installed > sandbox {
			t.Fatalf("IS_SANDBOX is enabled before %q is installed", marker)
		} else if installed > smoke {
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

func TestAgentRunnerImageSmokeProbesMaxTurnsParserWhenTopLevelHelpOmitsIt(t *testing.T) {
	binDir := t.TempDir()
	writeExecutable := func(name, contents string) {
		t.Helper()
		if err := os.WriteFile(binDir+"/"+name, []byte(contents), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	writeExecutable("claude", `#!/bin/sh
case "$1" in
  --version)
    printf '%s\n' '2.1.212 (Claude Code)'
    ;;
  --help)
    printf '%s\n' '--max-budget-usd --mcp-config --strict-mcp-config --append-system-prompt --output-format --verbose --dangerously-skip-permissions'
    ;;
  --print)
    case "$2" in
      --max-turns)
        test "$#" = 2 || exit 64
        printf '%s\n' "error: option '--max-turns <turns>' argument missing" >&2
        exit 1
        ;;
      --definitely-unknown-jetbridge-probe)
        printf '%s\n' "error: unknown option '--definitely-unknown-jetbridge-probe'" >&2
        exit 1
        ;;
      *)
        exit 64
        ;;
    esac
    ;;
  *)
    exit 64
    ;;
esac
`)
	for _, binary := range []string{"agent-runner", "function-runner", "agent-output"} {
		writeExecutable(binary, "#!/bin/sh\nexit 0\n")
	}

	maxTurnsProbe := exec.Command(binDir+"/claude", "--print", "--max-turns")
	maxTurnsOutput, maxTurnsErr := maxTurnsProbe.CombinedOutput()
	if maxTurnsErr == nil || !strings.Contains(string(maxTurnsOutput), "option '--max-turns <turns>' argument missing") {
		t.Fatalf("fake must model a registered max-turns option with a missing argument: %v\n%s", maxTurnsErr, maxTurnsOutput)
	}
	unknownProbe := exec.Command(binDir+"/claude", "--print", "--definitely-unknown-jetbridge-probe")
	unknownOutput, unknownErr := unknownProbe.CombinedOutput()
	if unknownErr == nil || !strings.Contains(string(unknownOutput), "unknown option") {
		t.Fatalf("fake must reject an unknown option: %v\n%s", unknownErr, unknownOutput)
	}

	cmd := exec.Command("sh", "agent-runner/smoke.sh")
	cmd.Env = append(os.Environ(), "PATH="+binDir+":"+os.Getenv("PATH"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("smoke must accept a registered max-turns parser diagnostic when top-level help omits --max-turns: %v\n%s", err, output)
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
		// The deployed reference names the in-cluster registry, but the digest
		// is verified through GHCR. Mirroring the same commit tag and requiring
		// both registries to return the same digest is the only thing that
		// makes the pullable reference provably the image just verified.
		`LOCAL_IMAGE="registry.home/agent-runner:${SHORT_SHA}"`,
		`LOCAL_PUSH_OUTPUT=$(kubectl exec -n cicd "${BUILDER_POD}" -- docker push "${LOCAL_IMAGE}")`,
		`if test "${LOCAL_DIGEST}" != "${DIGEST}"; then`,
		`DEPLOYABLE_IMAGE="registry.home/agent-runner@${DIGEST}"`,
		`printf 'CONCOURSE_AGENT_STEP_IMAGE=%s\nSOURCE_COMMIT=%s\nRUNNER_VERSION=%s\n' \`,
		`> ../runner-image-metadata/verified-image.env`,
		// The promotion runbook has an operator copy this printed line and then
		// asserts it against ^registry.home/… — it must be the deployable
		// reference, not the GHCR one the digest was verified through.
		`CONCOURSE_AGENT_STEP_IMAGE=${DEPLOYABLE_IMAGE}`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("agent-runner pipeline lacks immutable-image contract %q", required)
		}
	}
	if strings.Contains(script, "RepoDigests") {
		t.Fatal("agent-runner pipeline trusts local RepoDigests instead of the registry push response")
	}
	for _, forbidden := range []string{"git clone", "git fetch", "git push", "GIT_ASKPASS"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("privileged runner builder must not handle GitOps operation %q", forbidden)
		}
	}
	for _, input := range task.Config.Inputs {
		if input.Name == "home-infra" {
			t.Fatal("privileged builder must not receive the GitOps checkout")
		}
	}
	if task.Config.Params["GITHUB_TOKEN"] != "((github-token))" {
		t.Fatalf("builder GHCR login parameter = %q", task.Config.Params["GITHUB_TOKEN"])
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
		`docker push ${GHCR}:v${NEXT_VERSION}`,
		`if test "${LOCAL_DIGEST}" != "${DIGEST}"; then`,
		`verified-image.env`,
		`CONCOURSE_AGENT_STEP_IMAGE=${DEPLOYABLE_IMAGE}`,
	)
	if !strings.Contains(script, "done\nkubectl exec -n cicd \"${BUILDER_POD}\" -- docker info >/dev/null") {
		t.Fatal("agent-runner pipeline does not fail closed after its Docker readiness loop")
	}
}

func TestAgentRunnerWritebackRunbookOrdersArgoActivation(t *testing.T) {
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
		"verified-image.env",
		"put: home-infra",
		"ArgoCD",
		"self-upgrade",
		"Verify the running configuration",
		"Resume agent dispatch",
	)
	for _, required := range []string{
		"rebase: true",
		"force push",
		"^registry.home/agent-runner@sha256:[a-f0-9]{64}$",
		"argocd app get concourse --refresh --hard -n argocd -o json",
		"kubectl -n cicd rollout status deploy/concourse-web --timeout=10m",
	} {
		if !strings.Contains(section, required) {
			t.Errorf("writeback runbook lacks %q", required)
		}
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
