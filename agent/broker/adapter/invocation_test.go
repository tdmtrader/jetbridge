package adapter_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/agent/broker/adapter"
)

func TestNativeAdaptersBuildControlledInvocations(t *testing.T) {
	schema := filepath.Join(t.TempDir(), "result.schema.json")
	if err := os.WriteFile(schema, []byte(`{"type":"object","required":["answer"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	paths := adapter.Paths{
		WorkDir: "/work/child", ScratchDir: "/scratch/session",
		OutputSchema: schema,
	}
	tests := []struct {
		name       string
		adapter    adapter.Builder
		profile    broker.Profile
		wantBinary string
		wantArgs   []string
		forbidArgs []string
		credential string
	}{
		{
			name: "codex", adapter: adapter.Codex{}, profile: profile(broker.AdapterCodex),
			wantBinary: "codex",
			wantArgs: []string{"exec", "--strict-config", "--ephemeral", "--ignore-user-config", "--ignore-rules",
				"--sandbox", "read-only", "--model", "exact-model",
				"-c", `approval_policy="never"`,
				"-c", `model_reasoning_effort="high"`,
				"-c", "project_doc_max_bytes=0",
				"-c", "project_doc_fallback_filenames=[]",
				"--json", "--output-schema", paths.OutputSchema,
				"--output-last-message", filepath.Join(paths.ScratchDir, "result.json"), "-"},
			forbidArgs: []string{"--ask-for-approval"},
			credential: "CODEX_API_KEY",
		},
		{
			name: "claude", adapter: adapter.Claude{}, profile: profile(broker.AdapterClaude),
			wantBinary: "claude",
			wantArgs: []string{"-p", "--bare", "--output-format", "stream-json", "--verbose",
				"--model", "exact-model", "--permission-mode", "dontAsk",
				"--allowedTools", "Read,Glob,Grep", "--strict-mcp-config"},
			credential: "ANTHROPIC_API_KEY",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			invocation, err := tc.adapter.Build(tc.profile, paths, "secret-value")
			if err != nil {
				t.Fatalf("Build(): %v", err)
			}
			if invocation.Binary != tc.wantBinary || invocation.WorkDir != paths.WorkDir {
				t.Fatalf("invocation identity = %#v", invocation)
			}
			if tc.name == "codex" && !slices.Equal(invocation.Args, tc.wantArgs) {
				t.Errorf("Codex args = %q, want exact %q", invocation.Args, tc.wantArgs)
			} else if tc.name != "codex" {
				for _, argument := range tc.wantArgs {
					if !contains(invocation.Args, argument) {
						t.Errorf("args %q do not contain %q", invocation.Args, argument)
					}
				}
			}
			for _, argument := range tc.forbidArgs {
				if contains(invocation.Args, argument) {
					t.Errorf("args unexpectedly contain %q", argument)
				}
			}
			if invocation.Env[tc.credential] != "secret-value" {
				t.Fatalf("credential environment is missing")
			}
			if tc.name == "claude" {
				if !contains(invocation.Args, `{"type":"object","required":["answer"]}`) {
					t.Fatalf("Claude --json-schema value is not inline JSON: %q", invocation.Args)
				}
				if contains(invocation.Args, paths.OutputSchema) {
					t.Fatalf("Claude received schema path instead of inline JSON: %q", invocation.Args)
				}
				if invocation.Env["DISABLE_UPDATES"] != "1" || invocation.Env["DISABLE_AUTOUPDATER"] != "1" {
					t.Fatalf("Claude update controls = %#v", invocation.Env)
				}
			}
			if strings.Contains(invocation.Provenance(), "secret-value") {
				t.Fatal("provenance disclosed the credential")
			}
		})
	}
}

func TestCursorBuildFailsClosedWhileCleanContextCannotBeVerified(t *testing.T) {
	_, err := (adapter.Cursor{}).Build(profile(broker.AdapterCursor), adapter.Paths{
		WorkDir: "/work", ScratchDir: "/scratch", OutputSchema: "/schema",
	}, "secret")
	if err == nil || !strings.Contains(err.Error(), "unsupported adapter") {
		t.Fatalf("Cursor Build() error = %v, want unsupported adapter", err)
	}
}

func TestClaudeBuildRejectsUnsafeOrOversizedSchema(t *testing.T) {
	root := t.TempDir()
	realSchema := filepath.Join(root, "schema.json")
	if err := os.WriteFile(realSchema, []byte(`{"type":"object"}`), 0600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(root, "schema-link.json")
	if err := os.Symlink(realSchema, symlink); err != nil {
		t.Fatal(err)
	}
	basePaths := adapter.Paths{WorkDir: "/work", ScratchDir: "/scratch", OutputSchema: symlink}
	if _, err := (adapter.Claude{}).Build(profile(broker.AdapterClaude), basePaths, "secret"); err == nil ||
		!strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlink schema error = %v", err)
	}

	oversized := filepath.Join(root, "oversized.json")
	if err := os.WriteFile(oversized, []byte(`{"value":"`+strings.Repeat("x", (1<<20)+1)+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	basePaths.OutputSchema = oversized
	if _, err := (adapter.Claude{}).Build(profile(broker.AdapterClaude), basePaths, "secret"); err == nil ||
		!strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized schema error = %v", err)
	}
}

func TestBuilderRejectsAdapterProfileMismatch(t *testing.T) {
	_, err := (adapter.Cursor{}).Build(profile(broker.AdapterCodex), adapter.Paths{
		WorkDir: "/work", ScratchDir: "/scratch", OutputSchema: "/schema",
	}, "secret")
	if err == nil || !strings.Contains(err.Error(), "adapter") {
		t.Fatalf("mismatch error = %v", err)
	}
}

func profile(name broker.AdapterName) broker.Profile {
	nativeSchema := name != broker.AdapterCursor
	version := map[broker.AdapterName]string{
		broker.AdapterCodex:  "0.146.0",
		broker.AdapterClaude: "2.1.212",
		broker.AdapterCursor: "2026.07.23-e383d2b",
	}[name]
	return broker.Profile{
		ID: "test", Revision: 1,
		Selector:     broker.Selector{Tier: broker.TierBalanced, Effort: broker.EffortHigh},
		Tools:        []broker.Tool{broker.ToolConsultAgent},
		WorkerImage:  "registry/broker@sha256:" + strings.Repeat("a", 64),
		Adapter:      broker.AdapterSpec{Name: name, Version: version},
		Provider:     broker.ProviderSpec{Name: "provider", Model: "exact-model"},
		NativeEffort: "high", InstructionsDigest: "sha256:" + strings.Repeat("b", 64),
		CredentialSlot: "shared",
		Limits: broker.Limits{
			Timeout: time.Minute, MaxInputBytes: 1024, MaxOutputBytes: 1024,
		},
		Controls: broker.Controls{
			ReadOnlyWorkspace: true, NoBrokerRecursion: true, TestsUnavailable: true,
			NativeOutputSchema: nativeSchema, IgnoresUserConfig: name == broker.AdapterCodex,
		},
	}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
