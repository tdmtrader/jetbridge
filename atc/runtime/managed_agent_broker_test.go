package runtime

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

func TestValidateManagedAgentBrokerAcceptsOnlyFixedCompanion(t *testing.T) {
	spec := canonicalManagedAgentBrokerSpec(t)
	if err := ValidateManagedAgentBroker(spec); err != nil {
		t.Fatalf("canonical broker rejected: %v", err)
	}

	mutations := map[string]func(*ContainerSpec){
		"non agent":    func(s *ContainerSpec) { s.Type = db.ContainerTypeTask },
		"non hermetic": func(s *ContainerSpec) { s.Hermetic = false },
		"missing workflow": func(s *ContainerSpec) {
			s.ManagedAgentBroker.Authority.Files[ManagedAgentBrokerAuthorityFile] = []byte(`{"profiles":[]}`)
		},
		"wrong image":           func(s *ContainerSpec) { s.Sidecars[0].Image = "busybox:latest" },
		"wrong command":         func(s *ContainerSpec) { s.Sidecars[0].Command = []string{"sh"} },
		"wrong port":            func(s *ContainerSpec) { s.Sidecars[0].Ports[0].ContainerPort = 7785 },
		"authored environment":  func(s *ContainerSpec) { s.Sidecars[0].Env = []atc.SidecarEnvVar{{Name: "TOKEN", Value: "literal"}} },
		"missing marker":        func(s *ContainerSpec) { s.Env = nil },
		"duplicate companion":   func(s *ContainerSpec) { s.Sidecars = append(s.Sidecars, s.Sidecars[0]) },
		"unbounded scratch":     func(s *ContainerSpec) { s.ManagedAgentBroker.ScratchSizeBytes = 0 },
		"wrong workspace input": func(s *ContainerSpec) { s.ManagedAgentBroker.WorkspaceInputPath = "/work/other" },
		"literal credential":    func(s *ContainerSpec) { s.ManagedAgentBroker.Credentials[0].SecretName = "" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := canonicalManagedAgentBrokerSpec(t)
			mutate(&candidate)
			if err := ValidateManagedAgentBroker(candidate); err == nil {
				t.Fatal("invalid broker accepted")
			}
		})
	}
}

func TestValidateManagedAgentBrokerReservesNamePortAndMarkerWhenAbsent(t *testing.T) {
	for name, mutate := range map[string]func(*ContainerSpec){
		"name": func(s *ContainerSpec) {
			s.Sidecars = []atc.SidecarConfig{{Name: ManagedAgentBrokerName, Image: "busybox"}}
		},
		"port": func(s *ContainerSpec) {
			s.Sidecars = []atc.SidecarConfig{{Name: "authored", Image: "busybox", Ports: []atc.SidecarPort{{ContainerPort: ManagedAgentBrokerPort}}}}
		},
		"marker": func(s *ContainerSpec) { s.Env = []string{ManagedAgentBrokerMarkerEnv + "=1"} },
		"url":    func(s *ContainerSpec) { s.Env = []string{"X=" + ManagedAgentBrokerMCPURL} },
	} {
		t.Run(name, func(t *testing.T) {
			spec := ContainerSpec{Type: db.ContainerTypeAgent}
			mutate(&spec)
			if err := ValidateManagedAgentBroker(spec); err == nil {
				t.Fatal("reserved broker surface accepted")
			}
		})
	}
}

func canonicalManagedAgentBrokerSpec(t *testing.T) ContainerSpec {
	t.Helper()
	profile := broker.Profile{
		ID: "review-balanced", Revision: 1,
		Selector:    broker.Selector{Tier: broker.TierBalanced, Effort: broker.EffortHigh},
		Tools:       []broker.Tool{broker.ToolRequestReview},
		WorkerImage: "registry.example/broker@sha256:" + strings.Repeat("a", 64),
		Adapter:     broker.AdapterSpec{Name: broker.AdapterCodex, Version: "1.2.3"},
		Provider:    broker.ProviderSpec{Name: "provider", Model: "model"}, NativeEffort: "high",
		InstructionsDigest: "sha256:" + strings.Repeat("b", 64),
		CredentialSlot:     "shared",
		Limits:             broker.Limits{Timeout: time.Minute, MaxInputBytes: 1024, MaxOutputBytes: 1024},
		Controls:           broker.Controls{ReadOnlyWorkspace: true, NoBrokerRecursion: true, TestsUnavailable: true, NativeOutputSchema: true, IgnoresUserConfig: true},
	}
	catalog, err := broker.NewCatalog([]broker.Profile{profile})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := catalog.Resolve(broker.ToolRequestReview, profile.Selector)
	if err != nil {
		t.Fatal(err)
	}
	configuredProfile := resolved
	configuredProfile.Digest = ""
	profiles, _ := json.Marshal([]broker.Profile{configuredProfile})
	authority := map[string]any{
		"authority_endpoint":        "http://concourse-web/api/v1/internal",
		"bootstrap_capability_file": ManagedAgentBrokerAuthorityMountRoot + "/" + ManagedAgentBrokerBootstrapFile,
		"workspace_root":            ManagedAgentBrokerWorkspaceMountPath,
		"scratch_root":              ManagedAgentBrokerScratchMountPath,
		"adapter_binaries":          map[string]string{"codex": "/opt/bin/codex", "claude": "/opt/bin/claude", "cursor-agent": "/opt/bin/cursor-agent"},
		"output_schemas":            map[string]string{"request_review": "/opt/schemas/review.json", "consult_agent": "/opt/schemas/consultation.json"},
		"credential_slots":          map[string]string{"shared": ManagedAgentBrokerCredentialMountRoot + "/shared"},
		"instructions": map[string]any{
			"request_review": map[string]string{"path": "/opt/instructions/review.md", "digest": profile.InstructionsDigest},
			"consult_agent":  map[string]string{"path": "/opt/instructions/consult.md", "digest": profile.InstructionsDigest},
		},
		"attachments":     map[string]any{},
		"profiles":        json.RawMessage(profiles),
		"profile_digests": map[string]string{resolved.ID: resolved.Digest},
		"capture_limits":  map[string]any{"MaxPatchBytes": 1024, "MaxEntries": 100, "StabilityAttempts": 2},
	}
	raw, _ := json.Marshal(authority)
	image := resolved.WorkerImage
	return ContainerSpec{
		Type: db.ContainerTypeAgent, Hermetic: true, Dir: "/work",
		Env:    []string{ManagedAgentBrokerMarkerEnv + "=1"},
		Inputs: []Input{{DestinationPath: "/work/workspace"}},
		Sidecars: []atc.SidecarConfig{{
			Name: ManagedAgentBrokerName, Image: image,
			Command:    []string{"/usr/local/bin/agent-broker"},
			Ports:      []atc.SidecarPort{{ContainerPort: ManagedAgentBrokerPort, Protocol: "TCP"}},
			WorkingDir: "/",
		}},
		ManagedAgentBroker: &ManagedAgentBroker{
			Authority: PrivateFileMount{MountPath: ManagedAgentBrokerAuthorityMountRoot, Files: map[string][]byte{
				ManagedAgentBrokerAuthorityFile: raw, ManagedAgentBrokerBootstrapFile: []byte("capability"),
			}},
			WorkspaceInputPath: "/work/workspace", ScratchSizeBytes: 1 << 30,
			Credentials: []SecretKeyMount{{Slot: "shared", SecretName: "broker-provider", Key: "token", MountPath: ManagedAgentBrokerCredentialMountRoot + "/shared"}},
			Resources:   atc.SidecarResources{Requests: atc.SidecarResourceList{CPU: "100m", Memory: "128Mi"}, Limits: atc.SidecarResourceList{CPU: "1", Memory: "1Gi"}},
		},
	}
}
