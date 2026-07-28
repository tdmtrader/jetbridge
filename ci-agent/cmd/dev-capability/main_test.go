package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/ci-agent/devmcp"
)

type commandOutput struct {
	ProfileIdentity devmcp.ProfileIdentity `json:"profile_identity"`
	Status          string                 `json:"status"`
	Error           string                 `json:"error,omitempty"`
	Attempts        []struct {
		Status      string `json:"status"`
		OutputTail  string `json:"output_tail"`
		FullLogPath string `json:"full_log_path"`
	} `json:"attempts"`
}

func TestValidateWritesMachineResultAndCompleteLogs(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		command    string
		wantExit   int
		wantStatus string
	}{
		{name: "passed", command: "printf 'stdout complete\\n'; printf 'stderr complete\\n' >&2", wantExit: 0, wantStatus: devmcp.ValidationStatusPassed},
		{name: "test failure", command: "printf 'failure complete\\n'; exit 1", wantExit: 1, wantStatus: devmcp.ValidationStatusFailed},
		{name: "infrastructure failure", command: "printf 'infra complete\\n'; exit 2", wantExit: 2, wantStatus: devmcp.ValidationStatusError},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newValidationFixture(t, testCase.command)
			var stderr bytes.Buffer
			exitCode := runCommand(fixture.args(), &stderr)
			if exitCode != testCase.wantExit {
				t.Fatalf("exit = %d, want %d; stderr=%s", exitCode, testCase.wantExit, stderr.String())
			}
			raw, err := os.ReadFile(fixture.result)
			if err != nil {
				t.Fatalf("read result: %v", err)
			}
			var output commandOutput
			if err := json.Unmarshal(raw, &output); err != nil {
				t.Fatalf("decode result: %v; raw=%s", err, raw)
			}
			if output.Status != testCase.wantStatus || output.ProfileIdentity.ProfileDigest == "" || output.ProfileIdentity.ProtectedConfigDigest == "" {
				t.Fatalf("result = %#v", output)
			}
			if len(output.Attempts) != 1 || output.Attempts[0].FullLogPath != "attempt-0001.log" {
				t.Fatalf("attempts = %#v", output.Attempts)
			}
			completeLog, err := os.ReadFile(filepath.Join(fixture.logs, output.Attempts[0].FullLogPath))
			if err != nil {
				t.Fatalf("read complete log: %v", err)
			}
			if !strings.Contains(string(completeLog), "complete") {
				t.Fatalf("complete log was not retained: %q", completeLog)
			}
		})
	}
}

func TestValidateRejectsCandidateOwnedAuthorityFiles(t *testing.T) {
	fixture := newValidationFixture(t, "true")
	candidateConfig := filepath.Join(fixture.workspace, "dev-mcp.yml")
	if err := os.WriteFile(candidateConfig, []byte(fixture.config), 0o644); err != nil {
		t.Fatal(err)
	}
	args := fixture.args()
	for index, argument := range args {
		if argument == "--config" {
			args[index+1] = candidateConfig
		}
	}
	var stderr bytes.Buffer
	if exitCode := runCommand(args, &stderr); exitCode != 2 {
		t.Fatalf("exit = %d, want infrastructure/config error; stderr=%s", exitCode, stderr.String())
	}
	if !strings.Contains(stderr.String(), "outside candidate workspace") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestValidateRejectsAuthoritySymlinkedIntoCandidateWorkspace(t *testing.T) {
	fixture := newValidationFixture(t, "true")
	candidateConfig := filepath.Join(fixture.workspace, "dev-mcp.yml")
	if err := os.WriteFile(candidateConfig, []byte(fixture.config), 0o644); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(fixture.root, "protected-config-link.yml")
	if err := os.Symlink(candidateConfig, symlink); err != nil {
		t.Fatal(err)
	}
	args := fixture.args()
	for index, argument := range args {
		if argument == "--config" {
			args[index+1] = symlink
		}
	}
	var stderr bytes.Buffer
	if exitCode := runCommand(args, &stderr); exitCode != 2 {
		t.Fatalf("exit = %d, want infrastructure/config error; stderr=%s", exitCode, stderr.String())
	}
	if !strings.Contains(stderr.String(), "outside candidate workspace") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

type validationFixture struct {
	root        string
	workspace   string
	config      string
	configPath  string
	profilePath string
	changedPath string
	result      string
	logs        string
}

func newValidationFixture(t *testing.T, command string) validationFixture {
	t.Helper()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	protected := filepath.Join(root, "protected")
	output := filepath.Join(root, "output")
	for _, path := range []string{workspace, protected, output} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	config := "schema_version: 1\ncomponents:\n  - id: app\n    description: app\n    paths: [\"app/\"]\n    kind: service\n    test: {cmd: [\"sh\", \"-c\", " + quoteYAML(command) + "]}\n"
	profile := "schema_version: 1\nname: test\nchecks:\n  - id: 01-test\n    operation: test\n    scope: required\n    components: [app]\n    timeout: 1m\n    retries: 0\n"
	configPath := filepath.Join(protected, "dev-mcp.yml")
	profilePath := filepath.Join(protected, "profile.yml")
	changedPath := filepath.Join(protected, "changed.json")
	for path, contents := range map[string]string{configPath: config, profilePath: profile, changedPath: "[]\n"} {
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return validationFixture{root: root, workspace: workspace, config: config, configPath: configPath, profilePath: profilePath, changedPath: changedPath, result: filepath.Join(output, "result.json"), logs: filepath.Join(output, "logs")}
}

func (fixture validationFixture) args() []string {
	return []string{"validate", "--config", fixture.configPath, "--profile", fixture.profilePath, "--workspace", fixture.workspace, "--changed-paths", fixture.changedPath, "--result", fixture.result, "--logs", fixture.logs}
}

func quoteYAML(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
