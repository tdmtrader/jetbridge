package devvalidate

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
)

func TestDecodeResultRejectsForgedLogPathAndUnknownFields(t *testing.T) {
	for name, raw := range map[string]string{
		"path escape":   `{"profile_identity":{"profile_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","protected_config_digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},"status":"passed","duration_seconds":0,"attempts":[{"check_id":"build","number":1,"status":"ok","duration_seconds":0,"full_log_path":"../forged.log","failures":[]}]}`,
		"unknown field": `{"profile_identity":{"profile_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","protected_config_digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},"status":"passed","duration_seconds":0,"attempts":[{"check_id":"build","number":1,"status":"ok","duration_seconds":0,"full_log_path":"attempt-0001.log","failures":[],"forged":true}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeResult([]byte(raw)); err == nil || !strings.Contains(err.Error(), "dev validation") {
				t.Fatalf("DecodeResult() error = %v, want strict rejection", err)
			}
		})
	}
}

type fakeCommand struct{}

func (fakeCommand) Run(_ context.Context, args []string, _ string, _ []string) (int, error) {
	value := func(flag string) string {
		for i := range args {
			if args[i] == flag {
				return args[i+1]
			}
		}
		return ""
	}
	profile, _ := os.ReadFile(value("--profile"))
	config, _ := os.ReadFile(value("--config"))
	if err := os.MkdirAll(value("--logs"), 0700); err != nil {
		return 0, err
	}
	if err := os.WriteFile(filepath.Join(value("--logs"), "attempt-0001.log"), []byte("complete log\n"), 0600); err != nil {
		return 0, err
	}
	result := map[string]any{"profile_identity": map[string]string{"profile_digest": digest(profile), "protected_config_digest": digest(config)}, "status": "passed", "duration_seconds": 0, "attempts": []any{map[string]any{"check_id": "build", "number": 1, "status": "ok", "summary": "ok", "duration_seconds": 0, "output_tail": "ok", "full_log_path": "attempt-0001.log", "failures": []any{}}}}
	raw, _ := json.Marshal(result)
	return 0, os.WriteFile(value("--result"), raw, 0600)
}

func TestRunnerWritesRecordOnlyFromPinnedResultAndRetainsFullLog(t *testing.T) {
	profileRaw := []byte("schema_version: 1\nname: check\nchecks:\n  - id: build\n    operation: build\n    scope: full\n    timeout: 1m\n    retries: 0\n")
	configRaw := []byte("schema_version: 1\ncomponents: []\n")
	profile := workflow.CompiledDevValidationProfile{Name: "check", Candidate: workflow.DevValidationContract{Name: "candidate", Type: "opaque/v1"}, CapabilityImage: "registry.example/dev-mcp@sha256:" + strings.Repeat("a", 64), CapabilityImageDigest: snapshot.Digest("sha256:" + strings.Repeat("a", 64)), Command: []string{workflow.DevValidationCLIPath, workflow.DevValidationCLIValidateCommand}, Profile: profileRaw, ProfileDigest: shaDigest(profileRaw), ProtectedConfig: configRaw, ProtectedConfigDigest: shaDigest(configRaw)}
	workspace, output := t.TempDir(), t.TempDir()
	candidate := snapshot.SnapshotRef{ID: 1, Type: "opaque/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("b", 64))}
	if _, err := NewRunner(fakeCommand{}).Run(context.Background(), Request{Candidate: candidate, CandidateInput: "candidate", WorkspaceRoot: workspace, OutputRoot: output, Profile: profile, WorkflowDefinitionID: 1, WorkflowVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(output, "record.json")); err != nil {
		t.Fatalf("record not written: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(output, "content", "logs", "attempt-0001.log")); err != nil || string(got) != "complete log\n" {
		t.Fatalf("full log = %q, %v", got, err)
	}
}

func shaDigest(raw []byte) snapshot.Digest {
	sum := sha256.Sum256(raw)
	return snapshot.Digest("sha256:" + fmt.Sprintf("%x", sum[:]))
}
func digest(raw []byte) string { return shaDigest(raw).String() }
