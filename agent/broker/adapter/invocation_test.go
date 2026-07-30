package adapter_test

import (
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/agent/broker/adapter"
)

func TestNativeAdaptersBuildControlledInvocations(t *testing.T) {
	paths := adapter.Paths{
		WorkDir: "/work/child", ScratchDir: "/scratch/session",
		OutputSchema: "/run/broker/result.schema.json",
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
			wantArgs: []string{"exec", "--ephemeral", "--ignore-user-config", "--ignore-rules",
				"--sandbox", "read-only", "--ask-for-approval", "never", "--json",
				"--output-schema", paths.OutputSchema, "--model", "exact-model"},
			credential: "CODEX_API_KEY",
		},
		{
			name: "claude", adapter: adapter.Claude{}, profile: profile(broker.AdapterClaude),
			wantBinary: "claude",
			wantArgs: []string{"-p", "--output-format", "stream-json", "--verbose",
				"--model", "exact-model", "--permission-mode", "dontAsk",
				"--allowedTools", "Read,Glob,Grep", "--strict-mcp-config"},
			credential: "ANTHROPIC_API_KEY",
		},
		{
			name: "cursor", adapter: adapter.Cursor{}, profile: profile(broker.AdapterCursor),
			wantBinary: "cursor-agent",
			wantArgs:   []string{"--print", "--output-format", "stream-json", "--model", "exact-model"},
			forbidArgs: []string{"--force"},
			credential: "CURSOR_API_KEY",
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
			for _, argument := range tc.wantArgs {
				if !contains(invocation.Args, argument) {
					t.Errorf("args %q do not contain %q", invocation.Args, argument)
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
			if strings.Contains(invocation.Provenance(), "secret-value") {
				t.Fatal("provenance disclosed the credential")
			}
		})
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
	return broker.Profile{
		ID: "test", Revision: 1,
		Selector:     broker.Selector{Tier: broker.TierBalanced, Effort: broker.EffortHigh},
		Tools:        []broker.Tool{broker.ToolConsultAgent},
		WorkerImage:  "registry/broker@sha256:" + strings.Repeat("a", 64),
		Adapter:      broker.AdapterSpec{Name: name, Version: "1.2.3"},
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
