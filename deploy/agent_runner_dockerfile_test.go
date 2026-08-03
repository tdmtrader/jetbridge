package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
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
  mcp)
    printf '%s\n' 'output-builder: Connected'
    ;;
  *)
    exit 64
    ;;
esac
`)
	for _, binary := range []string{"agent-runner", "function-runner", "agent-output"} {
		writeExecutable(binary, "#!/bin/sh\nexit 0\n")
	}
	writeExecutable("curl", "#!/bin/sh\nexit 0\n")
	writeExecutable("install", "#!/bin/sh\ncat >/dev/null\n")

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

// This catches smoke cleanup that leaves its exact writable authority file
// behind or removes it before the managed sidecar has terminated.
func TestAgentRunnerImageSmokeCleansManagedMCPProbe(t *testing.T) {
	binDir := t.TempDir()
	writeExecutable := func(name, contents string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(contents), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeExecutable("claude", `#!/bin/sh
case "$1" in
  --version) printf '%s\n' '2.1.212 (Claude Code)' ;;
  --help) printf '%s\n' '--max-budget-usd --mcp-config --strict-mcp-config --append-system-prompt --output-format --verbose --dangerously-skip-permissions' ;;
  --print) printf '%s\n' "error: option '--max-turns <turns>' argument missing" >&2; exit 1 ;;
  mcp)
    test "$2" = "list" && test "$3" = "--mcp-config" && test "$5" = "--strict-mcp-config" || exit 64
    cat "$4" > "$MCP_CONFIG_LOG"
    printf '%s\n' "$MCP_STATUS"
    exit 0
    ;;
  *) exit 64 ;;
esac
`)
	writeExecutable("agent-output", `#!/bin/sh
printf '%s\n' "$$" > "$SIDECAR_PID_LOG"
while :; do sleep 1; done
`)
	for _, binary := range []string{"agent-runner", "function-runner"} {
		writeExecutable(binary, "#!/bin/sh\nexit 0\n")
	}
	writeExecutable("install", `#!/bin/sh
test "$1" = "-D" && test "$2" = "-m" && test "$3" = "0444" && test "$4" = "/dev/stdin" && test "$5" = "/run/concourse/output-builder/authority.json" || exit 64
tee "$INSTALL_LOG" > "$AUTHORITY_STATE"
`)
	writeExecutable("rm", `#!/bin/sh
if test "$#" = 3 && test "$1" = "-f" && test "$2" = "--" && test "$3" = "/run/concourse/output-builder/authority.json"; then
  test -s "$SIDECAR_PID_LOG" || exit 64
  if kill -0 "$(cat "$SIDECAR_PID_LOG")" 2>/dev/null; then
    exit 65
  fi
  exec /bin/rm -f -- "$AUTHORITY_STATE"
fi
if test "$#" = 3 && test "$1" = "-rf" && test "$2" = "--"; then
  case "$3" in
    "$TMPDIR"/agent-output-smoke.*) exec /bin/rm "$@" ;;
    *) exit 66 ;;
  esac
fi
exit 64
`)
	writeExecutable("curl", "#!/bin/sh\ntest -s \"$SIDECAR_PID_LOG\"\n")

	for _, tc := range []struct {
		name        string
		status      string
		wantSuccess bool
	}{
		{name: "connected", status: "output-builder: Connected", wantSuccess: true},
		{name: "disconnected", status: "output-builder: disconnected"},
		{name: "negative multiword", status: "output-builder: Not Connected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			installLog := filepath.Join(t.TempDir(), "authority.json")
			authorityState := filepath.Join(t.TempDir(), "authority.json")
			mcpConfigLog := filepath.Join(t.TempDir(), "mcp.json")
			sidecarPIDLog := filepath.Join(t.TempDir(), "sidecar.pid")
			cmd := exec.Command("sh", "agent-runner/smoke.sh")
			cmd.Env = append(os.Environ(),
				"PATH="+binDir+":"+os.Getenv("PATH"),
				"TMPDIR="+tmpDir,
				"INSTALL_LOG="+installLog,
				"AUTHORITY_STATE="+authorityState,
				"MCP_CONFIG_LOG="+mcpConfigLog,
				"MCP_STATUS="+tc.status,
				"SIDECAR_PID_LOG="+sidecarPIDLog,
			)
			output, err := cmd.CombinedOutput()
			if tc.wantSuccess {
				if err != nil {
					t.Fatalf("smoke success = %v\n%s", err, output)
				}
			} else if err == nil || !strings.Contains(string(output), "managed output builder MCP is not connected") {
				t.Fatalf("smoke failure = %v\n%s", err, output)
			}
			if _, err := os.Stat(installLog); err != nil {
				t.Fatalf("smoke did not install authority: %v", err)
			}
			authority, err := os.ReadFile(installLog)
			if err != nil || !strings.Contains(string(authority), `"inputs":{}`) || !strings.Contains(string(authority), `"type":"review/v1"`) || !strings.Contains(string(authority), `"mount_root":"`+tmpDir+`/agent-output-smoke.`) {
				t.Fatalf("authority = %q, %v", authority, err)
			}
			config, err := os.ReadFile(mcpConfigLog)
			if err != nil || string(config) != `{"mcpServers":{"output-builder":{"type":"http","url":"http://127.0.0.1:7783/mcp"}}}`+"\n" {
				t.Fatalf("MCP config = %q, %v", config, err)
			}
			if entries, err := os.ReadDir(tmpDir); err != nil || len(entries) != 0 {
				t.Fatalf("smoke temp cleanup = %#v, %v", entries, err)
			}
			if _, err := os.Stat(authorityState); !os.IsNotExist(err) {
				t.Fatalf("authority residue remains after sidecar cleanup: %v", err)
			}
		})
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
		`SOURCE_COMMIT=$(git rev-parse HEAD)`,
		`LOCAL_IMAGE="registry.home/agent-runner:${SOURCE_COMMIT}"`,
		`docker build --platform linux/amd64`,
		`--tag "${LOCAL_IMAGE}"`,
		`docker run --rm --platform linux/amd64`,
		`--entrypoint /usr/local/bin/agent-runner-image-smoke "${LOCAL_IMAGE}"`,
		`PUSH_OUTPUT=$(kubectl exec -n cicd "${BUILDER_POD}" -- docker push "${LOCAL_IMAGE}")`,
		`sed -n 's/.*digest: \(sha256:[a-f0-9]\{64\}\).*/\1/p'`,
		`grep -Eq '^sha256:[a-f0-9]{64}$'`,
		`IMMUTABLE_IMAGE="registry.home/agent-runner@${DIGEST}"`,
		`docker pull --platform linux/amd64 "${IMMUTABLE_IMAGE}"`,
		`--entrypoint /usr/local/bin/agent-runner-image-smoke "${IMMUTABLE_IMAGE}"`,
		`docker image inspect --format '{{.Os}}/{{.Architecture}}' "${IMMUTABLE_IMAGE}"`,
		`printf 'CONCOURSE_AGENT_STEP_IMAGE=%s\nSOURCE_COMMIT=%s\nRUNNER_VERSION=%s\n' \`,
		`> ../runner-image-metadata/verified-image.env`,
		`CONCOURSE_AGENT_STEP_IMAGE=${IMMUTABLE_IMAGE}`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("agent-runner pipeline lacks immutable-image contract %q", required)
		}
	}
	if strings.Contains(script, "RepoDigests") {
		t.Fatal("agent-runner pipeline trusts local RepoDigests instead of the registry push response")
	}
	for _, forbidden := range []string{"git clone", "git push", "GIT_ASKPASS"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("privileged runner builder must not handle GitOps operation %q", forbidden)
		}
	}
	if got := strings.Count(script, "git fetch --tags --force origin"); got != 1 {
		t.Fatalf("privileged runner builder public tag refresh count = %d, want exactly 1", got)
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
		`PUSH_OUTPUT=$(kubectl exec -n cicd "${BUILDER_POD}" -- docker push "${LOCAL_IMAGE}")`,
		`DIGEST=$(printf`,
		`docker pull --platform linux/amd64 "${IMMUTABLE_IMAGE}"`,
		`docker image inspect --format '{{.Os}}/{{.Architecture}}' "${IMMUTABLE_IMAGE}"`,
		`verified-image.env`,
		`docker push registry.home/agent-runner:v${NEXT_VERSION}`,
		`set +x`,
		`docker login ghcr.io`,
		`set -x`,
		`CONCOURSE_AGENT_STEP_IMAGE=${IMMUTABLE_IMAGE}`,
	)
	if !strings.Contains(script, "done\nkubectl exec -n cicd \"${BUILDER_POD}\" -- docker info >/dev/null") {
		t.Fatal("agent-runner pipeline does not fail closed after its Docker readiness loop")
	}
	requireTextOrder(t, script,
		`> ../runner-image-metadata/verified-image.env`,
		`docker login ghcr.io`,
	)
	for _, required := range []string{
		`if printf '%s\n' "${GITHUB_TOKEN}" | kubectl exec`,
		`WARNING: GHCR runner mirror`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("runner GHCR mirror is not explicitly best-effort; missing %q", required)
		}
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
		"tag-rc",
		"build-agent-runner-image",
		"verified-image.env",
		"supervised, unprivileged task",
		"ArgoCD",
		"self-upgrade",
		"Verify the running configuration",
		"Resume agent dispatch",
	)
	for _, required := range []string{
		"fresh fetch/rebase",
		"non-force push",
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
