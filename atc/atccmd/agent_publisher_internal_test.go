package atccmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/publisher/publishertest"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/worker/jetbridge"
)

func TestAgentSnapshotCompositionBuildsConfiguredInProcessPublisher(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(directory, "policy.json")
	credentialPath := filepath.Join(directory, "widget-git")
	if err := os.WriteFile(policyPath, []byte(`{
		"schema_version":1,
		"rules":[{"team":"engineering","publisher":"git-publisher/v1","mode":"branch","approval_policy_version":"engineering/v1","target_branch":"main","destination":"git.example/acme/widget","adapter":"direct-git","credential_reference":"widget-git","remote_url":"https://git.example/acme/widget.git"}]
	}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credentialPath, []byte("mounted-token"), 0600); err != nil {
		t.Fatal(err)
	}

	command := &RunCommand{}
	command.AgentSnapshots.Enabled = true
	command.AgentSnapshots.MaxBytes = 1 << 20
	command.AgentSnapshots.MaxFiles = 100
	command.AgentSnapshots.TempDir = t.TempDir()
	command.AgentSnapshots.OrphanGracePeriod = time.Hour
	command.AgentSnapshots.BindingRetention = 24 * time.Hour
	command.AgentPublisher.Enabled = true
	command.AgentPublisher.CredentialRoot = directory
	command.AgentPublisher.PolicyFile = policyPath
	command.AgentPublisher.CredentialFiles = map[string]string{"widget-git": credentialPath}
	command.AgentPublisher.DirectGitEnabled = true
	command.AgentPublisher.RequestTimeout = 30 * time.Second
	command.AgentPublisher.LeaseDuration = 5 * time.Minute
	command.agentSnapshotComposer = func(db.DbConn, db.AgentSnapshotsFactory, snapshot.ArchiveLimits) (*jetbridge.DaemonClient, snapshot.ContentStore, error) {
		return &jetbridge.DaemonClient{}, &compositionContentStore{}, nil
	}
	command.agentSnapshotSealerComposer = func(snapshot.Canonicalizer, snapshot.ValidatorRegistry, snapshot.MetadataStore, snapshot.ContentStore, snapshot.DigestLockManager, time.Duration, time.Duration) (snapshot.SnapshotCreator, error) {
		return &compositionOutputSealer{}, nil
	}
	command.agentSnapshotLifecycleComposer = func(snapshot.MetadataStore, snapshot.ContentStore, snapshot.ReplicaRepairer, snapshot.DigestLockManager) (snapshotLifecycle, error) {
		return &compositionLifecycle{}, nil
	}

	if err := command.composeAgentSnapshots(&dbfakes.FakeDbConn{}, lagertest.NewTestLogger("publisher-composition")); err != nil {
		t.Fatal(err)
	}
	if command.agentSnapshotPublisher == nil {
		t.Fatal("enabled in-process publisher was not composed")
	}
	if _, ok := command.agentSnapshotPublisher.(*publisher.Router); !ok {
		t.Fatalf("publisher = %T, want provider-neutral router", command.agentSnapshotPublisher)
	}
	options, ok := command.agentSnapshotCoreStepFactoryOptions()
	if !ok || len(options) != 4 {
		t.Fatalf("publisher composition engine options = %d/%t, want sealer, canonicalizer, loader, publisher", len(options), ok)
	}
}

func TestAgentPublisherUsesDedicatedSnapshotScratch(t *testing.T) {
	command := &RunCommand{}
	command.AgentSnapshots.MaxBytes = 1 << 20
	command.AgentSnapshots.MaxFiles = 100
	command.AgentSnapshots.TempDir = t.TempDir()

	canonicalizer := command.agentPublisherCanonicalizer()
	if canonicalizer.TempDir != command.AgentSnapshots.TempDir {
		t.Fatalf("publisher canonicalizer temp dir = %q, want %q", canonicalizer.TempDir, command.AgentSnapshots.TempDir)
	}
}

func TestAgentPublisherCompositionRejectsUnsupportedConfiguredModesAndAdapters(t *testing.T) {
	tests := map[string]string{
		"pull request": `{
			"schema_version":1,
			"rules":[{"team":"engineering","publisher":"git-publisher/v1","mode":"pull-request","approval_policy_version":"engineering/v1","target_branch":"main","destination":"git.example/acme/widget","adapter":"direct-git","credential_reference":"widget-git","remote_url":"https://git.example/acme/widget.git"}]
		}`,
		"work item": `{
			"schema_version":1,
			"rules":[{"team":"engineering","publisher":"work-item-publisher/v1","mode":"comment","approval_policy_version":"engineering/v1","destination":"tracker.example/acme/widget/17","adapter":"direct-git","credential_reference":"widget-git","remote_url":"https://tracker.example/acme/widget.git"}]
		}`,
		"gateway adapter": `{
			"schema_version":1,
			"rules":[{"team":"engineering","publisher":"git-publisher/v1","mode":"branch","approval_policy_version":"engineering/v1","target_branch":"main","destination":"git.example/acme/widget","adapter":"gateway","credential_reference":"widget-git","remote_url":"https://git.example/acme/widget.git"}]
		}`,
	}
	for name, policyDocument := range tests {
		t.Run(name, func(t *testing.T) {
			command := configuredAgentPublisherCommand(t, policyDocument)
			_, err := command.buildAgentPublisher(
				publishertest.NewMemoryStore(time.Now),
				&dbfakes.FakeAgentSnapshotsFactory{},
				&compositionContentStore{},
			)
			if err == nil || !strings.Contains(err.Error(), "unsupported") {
				t.Fatalf("composition error = %v, want unsupported configuration", err)
			}
		})
	}
}

func TestAgentPublisherPolicyValidationSelectsOnlyEnabledAdapterLanes(t *testing.T) {
	direct := publisher.PolicyRule{
		Team: "engineering", Publisher: publisher.GitPublisher,
		Mode: publisher.ModeBranch, ApprovalPolicyVersion: "engineering/v1",
		TargetBranch: "main", Destination: "git.example/acme/widget",
		Adapter: publisher.AdapterDirectGit, CredentialReference: "widget-git",
		RemoteURL: "https://git.example/acme/widget.git",
	}
	// Provider-native pull requests were removed. A policy that still names the
	// mode must be refused as an unsupported mode rather than quietly admitted.
	github := publisher.PolicyRule{
		Team: "engineering", Publisher: publisher.GitPublisher,
		Mode: publisher.ModePullRequest, ApprovalPolicyVersion: "engineering/v1",
		TargetBranch: "refs/heads/main", Destination: "github.com/acme/widget",
		Adapter: publisher.AdapterGitHub, Provider: publisher.PRProviderGitHub,
		Repository: "acme/widget", APIBaseURL: "https://api.github.com",
		RepositoryURL:           "https://github.com/acme/widget.git",
		ReadCredentialReference: "widget-read", WriteCredentialReference: "widget-write",
	}
	for name, test := range map[string]struct {
		directEnabled bool
		rules         []publisher.PolicyRule
		wantError     string
	}{
		"direct rule while enabled": {
			directEnabled: true, rules: []publisher.PolicyRule{direct},
		},
		"pull request rule is unsupported": {
			directEnabled: true, rules: []publisher.PolicyRule{github},
			wantError: "unsupported mode",
		},
		"direct rule while disabled": {
			rules:     []publisher.PolicyRule{direct},
			wantError: "direct Git adapter is disabled",
		},
	} {
		t.Run(name, func(t *testing.T) {
			policy := publisher.Policy{SchemaVersion: 1, Rules: test.rules}
			if err := policy.Validate(); err != nil {
				t.Fatalf("test policy is invalid: %v", err)
			}
			command := &RunCommand{}
			command.AgentPublisher.DirectGitEnabled = test.directEnabled
			err := command.validateAgentPublisherPolicy(policy)
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("validateAgentPublisherPolicy() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("validateAgentPublisherPolicy() error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestAgentPublisherCompositionUsesMountedCredentialProvider(t *testing.T) {
	command := configuredAgentPublisherCommand(t, `{
		"schema_version":1,
		"rules":[{"team":"engineering","publisher":"git-publisher/v1","mode":"merge","approval_policy_version":"engineering/v1","target_branch":"main","destination":"git.example/acme/widget","adapter":"direct-git","credential_reference":"widget-git","remote_url":"https://git.example/acme/widget.git"}]
	}`)
	credentialPath := command.AgentPublisher.CredentialFiles["widget-git"]
	if err := os.Chmod(credentialPath, 0644); err != nil {
		t.Fatal(err)
	}

	_, err := command.buildAgentPublisher(
		publishertest.NewMemoryStore(time.Now),
		&dbfakes.FakeAgentSnapshotsFactory{},
		&compositionContentStore{},
	)
	if err == nil || !strings.Contains(err.Error(), "accessible by other users") {
		t.Fatalf("composition error = %v, want private mounted credential rejection", err)
	}
}

func TestAgentPublisherBuilderFailsClosedWhenDisabled(t *testing.T) {
	command := &RunCommand{}
	_, err := command.buildAgentPublisher(
		publishertest.NewMemoryStore(time.Now),
		&dbfakes.FakeAgentSnapshotsFactory{},
		&compositionContentStore{},
	)
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled builder error = %v", err)
	}
}

func configuredAgentPublisherCommand(t *testing.T, policyDocument string) *RunCommand {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(directory, "policy.json")
	credentialPath := filepath.Join(directory, "widget-git")
	if err := os.WriteFile(policyPath, []byte(policyDocument), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credentialPath, []byte("mounted-token"), 0600); err != nil {
		t.Fatal(err)
	}
	command := &RunCommand{}
	command.AgentSnapshots.Enabled = true
	command.AgentSnapshots.MaxBytes = 1 << 20
	command.AgentSnapshots.MaxFiles = 100
	command.AgentSnapshots.TempDir = t.TempDir()
	command.AgentPublisher.Enabled = true
	command.AgentPublisher.CredentialRoot = directory
	command.AgentPublisher.PolicyFile = policyPath
	command.AgentPublisher.CredentialFiles = map[string]string{"widget-git": credentialPath}
	command.AgentPublisher.DirectGitEnabled = true
	command.AgentPublisher.RequestTimeout = 30 * time.Second
	command.AgentPublisher.LeaseDuration = 5 * time.Minute
	return command
}
