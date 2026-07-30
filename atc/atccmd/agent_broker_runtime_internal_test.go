package atccmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/agent/broker/workspace"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/agentchildexecutions"
	"github.com/concourse/concourse/atc/exec"
	atcruntime "github.com/concourse/concourse/atc/runtime"
)

func TestAgentBrokerAuthorityFactoryReturnsScopedFilesWithoutRootKey(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))
	signer, _ := agentchildexecutions.NewCapabilitySigner("key-1", key)
	profile := agentBrokerRuntimeProfile(t)
	ref := snapshot.SnapshotRef{ID: 7, Type: "repository/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("c", 64))}
	factory := agentBrokerAuthorityFactory{
		signer: signer, runtime: validAgentBrokerRuntimeConfig(profile.InstructionsDigest),
		leaseDuration: time.Minute, capabilityTTL: time.Hour,
		now: func() time.Time { return time.Unix(1000, 0) },
	}
	managed, err := factory.BuildAgentBroker(exec.AgentBrokerAuthorityRequest{
		TeamID: 1, TeamName: "main", BuildID: 2, SnapshotCreatedBy: "concourse",
		WorkflowDefinitionID: 3, WorkflowRunID: 4, NodePlanID: "node", ParentAttempt: 1,
		BrokerInstance: "broker-instance", Profiles: []broker.Profile{profile},
		ScopeInputs: map[string]snapshot.SnapshotRef{"workspace": ref},
		InputPaths:  map[string]string{"workspace": "/work/workspace"}, WorkspaceInputPath: "/work/workspace",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := managed.Authority.Files[atcruntime.ManagedAgentBrokerAuthorityFile]
	if strings.Contains(string(raw), strings.Repeat("k", 32)) || strings.Contains(string(raw), "capability_key") {
		t.Fatal("root signing key leaked into broker authority")
	}
	if len(managed.Authority.Files[atcruntime.ManagedAgentBrokerBootstrapFile]) == 0 ||
		len(managed.Credentials) != 1 || managed.Credentials[0].SecretName != "provider-secret" {
		t.Fatalf("managed broker = %#v", managed)
	}
}

func TestLoadAgentBrokerRuntimeRejectsPartialAndUnknownConfig(t *testing.T) {
	for name, value := range map[string]any{
		"partial": map[string]any{"authority_endpoint": "https://atc.example"},
		"unknown": map[string]any{
			"authority_endpoint": "https://atc.example", "unknown": true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(value)
			path := filepath.Join(t.TempDir(), "runtime.json")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadAgentBrokerRuntime(path); err == nil {
				t.Fatal("partial/unknown runtime config accepted")
			}
		})
	}
}

func validAgentBrokerRuntimeConfig(instructionDigest string) agentBrokerRuntimeConfig {
	return agentBrokerRuntimeConfig{
		AuthorityEndpoint: "https://atc.example",
		AdapterBinaries:   map[string]string{"codex": "/opt/bin/codex", "claude": "/opt/bin/claude", "cursor-agent": "/opt/bin/cursor-agent"},
		OutputSchemas:     map[string]string{"request_review": "/opt/schemas/review.json", "consult_agent": "/opt/schemas/consultation.json"},
		Instructions: map[string]agentBrokerRuntimeInstruction{
			"request_review": {Path: "/opt/instructions/review.md", Digest: instructionDigest},
			"consult_agent":  {Path: "/opt/instructions/consult.md", Digest: instructionDigest},
		},
		CredentialSlots:  map[string]agentBrokerRuntimeCredential{"shared": {SecretName: "provider-secret", Key: "token"}},
		CaptureLimits:    workspace.Limits{MaxPatchBytes: 1024, MaxEntries: 100, StabilityAttempts: 2},
		ScratchSizeBytes: 1 << 30,
		Resources:        atc.SidecarResources{Requests: atc.SidecarResourceList{CPU: "100m", Memory: "128Mi"}},
	}
}

func agentBrokerRuntimeProfile(t *testing.T) broker.Profile {
	t.Helper()
	configured := broker.Profile{
		ID: "profile", Revision: 1, Selector: broker.Selector{Tier: broker.TierBalanced, Effort: broker.EffortHigh},
		Tools: []broker.Tool{broker.ToolRequestReview}, WorkerImage: "registry.example/broker@sha256:" + strings.Repeat("a", 64),
		Adapter:  broker.AdapterSpec{Name: broker.AdapterCodex, Version: "1.2.3"},
		Provider: broker.ProviderSpec{Name: "provider", Model: "model"}, NativeEffort: "high",
		InstructionsDigest: "sha256:" + strings.Repeat("b", 64), CredentialSlot: "shared",
		Limits:   broker.Limits{Timeout: time.Minute, MaxInputBytes: 1024, MaxOutputBytes: 1024},
		Controls: broker.Controls{ReadOnlyWorkspace: true, NoBrokerRecursion: true, TestsUnavailable: true, NativeOutputSchema: true, IgnoresUserConfig: true},
	}
	catalog, err := broker.NewCatalog([]broker.Profile{configured})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := catalog.Resolve(broker.ToolRequestReview, configured.Selector)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
