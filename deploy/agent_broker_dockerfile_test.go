package deploy_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestAgentBrokerImageIsImmutableAndReadOnlyRootCompatible(t *testing.T) {
	raw, err := os.ReadFile("agent-broker/Dockerfile")
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	dockerfile := string(raw)
	for _, want := range []string{
		"golang:1.25.12-bookworm@sha256:ea341baa9bd5ba6784f6d7161ace70544349a6242d54d34a0fbfd2c4d51c9d58",
		"debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818",
		"https://github.com/openai/codex/releases/download/rust-v0.146.0/codex-x86_64-unknown-linux-musl.tar.gz",
		"5ba3b9405543953081f661d0854d266f76e2abbe51d41349355a36de7673776a",
		"https://storage.googleapis.com/claude-code-dist-86c565f3-f756-42ad-8dfa-d59b1c096819/claude-code-releases/2.1.212/linux-x64/claude",
		"044a88cf3a5180776617fd3da1238dcbf9141ddec449a39cf7d2af1ac78e684e",
		"https://downloads.cursor.com/lab/2026.07.23-e383d2b/linux/x64/agent-cli-package.tar.gz",
		"702ad595213bee5df0268be9f80a19f29fcceaa2a42fc55e39f2b5199051f0c4",
		"apt-get install -y --no-install-recommends bash ca-certificates git ripgrep",
		"COPY --from=build --chmod=0555 /out/agent-broker /usr/local/bin/agent-broker",
		"COPY --from=harnesses --chown=65532:65534",
		"COPY --chown=65532:65534 --chmod=0444 deploy/agent-broker/assets/",
		"USER 65532:65534",
		`ENTRYPOINT ["/usr/local/bin/agent-broker"]`,
	} {
		if !strings.Contains(dockerfile, want) {
			t.Errorf("Dockerfile does not contain immutable contract %q", want)
		}
	}
	for _, forbidden := range []string{
		"curl | sh", "curl|sh", "npm install", "/latest/", "releases/latest",
		"apt-get install -y bash ca-certificates git ripgrep curl",
	} {
		if strings.Contains(dockerfile, forbidden) {
			t.Errorf("Dockerfile contains forbidden mutable installer %q", forbidden)
		}
	}
}

func TestAgentBrokerImageAssetsAreFixedAndSchemaValid(t *testing.T) {
	for path, wantDigest := range map[string]string{
		"agent-broker/assets/instructions/request-review.txt":   "9982a935820d5177131cf16e285ab137b0774a0d4701181aaf180358a3a6f669",
		"agent-broker/assets/instructions/consult-agent.txt":    "e79f4aa92d601d27222d53fb07fbba5e306856006c260c0a8019220153e23dbe",
		"agent-broker/assets/schemas/review-body.v1.json":       "",
		"agent-broker/assets/schemas/consultation-body.v1.json": "",
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("required image asset %s: %v", path, err)
			continue
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			t.Errorf("required image asset %s is not a nonempty regular file", path)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read image asset %s: %v", path, err)
			continue
		}
		if strings.HasSuffix(path, ".json") && !json.Valid(raw) {
			t.Errorf("image schema %s is not valid JSON", path)
		}
		if strings.HasSuffix(path, ".json") {
			var schema map[string]any
			if err := json.Unmarshal(raw, &schema); err != nil {
				t.Errorf("decode image schema %s: %v", path, err)
				continue
			}
			if got := schema["$schema"]; got != "http://json-schema.org/draft-07/schema#" {
				t.Errorf("image schema %s dialect = %v, want draft-07", path, got)
			}
			if _, found := schema["$defs"]; found {
				t.Errorf("image schema %s uses unsupported newer-dialect $defs", path)
			}
			definitions, definitionsFound := schema["definitions"].(map[string]any)
			if _, found := definitions["anchor"]; !definitionsFound || !found {
				t.Errorf("image schema %s has no draft-07 anchor definition", path)
			}
			if strings.Contains(string(raw), "#/$defs/") || !strings.Contains(string(raw), "#/definitions/anchor") {
				t.Errorf("image schema %s does not use draft-07 definition references", path)
			}
		}
		if wantDigest != "" {
			sum := sha256.Sum256(raw)
			if got := hex.EncodeToString(sum[:]); got != wantDigest {
				t.Errorf("instruction asset %s digest = %s, want %s", path, got, wantDigest)
			}
		}
	}
}

func TestAgentBrokerPackagingTargetsUseTheDedicatedDockerfile(t *testing.T) {
	makefile, err := os.ReadFile("../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"build-agent-broker-image:",
		"test-agent-broker-smoke:",
		`test "$$CONCOURSE_AGENT_BROKER_SMOKE" = "1"`,
		"deploy/agent-broker/Dockerfile",
		"--platform linux/amd64",
	} {
		if !strings.Contains(string(makefile), want) {
			t.Errorf("Makefile does not contain broker packaging contract %q", want)
		}
	}
	pipeline, err := os.ReadFile("concourse-pipeline.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"- name: build-agent-broker-image",
		"trigger: false",
		"-f /tmp/src/deploy/agent-broker/Dockerfile",
		`sed -n 's/.*digest: \(sha256:[a-f0-9]\{64\}\).*/\1/p'`,
		`grep -Eq '^sha256:[a-f0-9]{64}$'`,
		"AGENT_BROKER_IMAGE=${IMAGE_REPOSITORY}@${DIGEST}",
	} {
		if !strings.Contains(string(pipeline), want) {
			t.Errorf("pipeline does not contain broker packaging contract %q", want)
		}
	}
	if strings.Contains(string(pipeline), "RepoDigests") {
		t.Fatal("pipeline relies on local RepoDigests instead of the registry push digest")
	}
}
