package atccmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/worker/jetbridge"
)

func TestAgentSnapshotCompositionBuildsConfiguredPublisherGateway(t *testing.T) {
	directory := t.TempDir()
	policyPath := filepath.Join(directory, "policy.json")
	tokenPath := filepath.Join(directory, "token")
	if err := os.WriteFile(policyPath, []byte(`{
		"schema_version":1,
		"rules":[{"team":"engineering","publisher":"git-publisher/v1","modes":["pull-request"],"approval_policy_versions":["engineering/v1"],"target_branches":["main"],"destinations":["git.example/acme/widget"]}]
	}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte("mounted-token"), 0600); err != nil {
		t.Fatal(err)
	}

	command := &RunCommand{}
	command.AgentSnapshots.Enabled = true
	command.AgentSnapshots.MaxBytes = 1 << 20
	command.AgentSnapshots.MaxFiles = 100
	command.AgentSnapshots.TempDir = t.TempDir()
	command.AgentSnapshots.OrphanGracePeriod = time.Hour
	command.AgentSnapshots.BindingRetention = 24 * time.Hour
	command.AgentPublisherGateway.Enabled = true
	command.AgentPublisherGateway.Endpoint = "https://gateway.example"
	command.AgentPublisherGateway.PolicyFile = policyPath
	command.AgentPublisherGateway.TokenFile = tokenPath
	command.AgentPublisherGateway.RequestTimeout = 30 * time.Second
	command.AgentPublisherGateway.LeaseDuration = 5 * time.Minute
	command.AgentPublisherGateway.MaxResponseBytes = 1 << 20
	command.agentSnapshotComposer = func(db.DbConn, db.AgentSnapshotsFactory, snapshot.ArchiveLimits) (*jetbridge.DaemonClient, snapshot.ContentStore, error) {
		return &jetbridge.DaemonClient{}, &compositionContentStore{}, nil
	}
	command.agentSnapshotSealerComposer = func(snapshot.Canonicalizer, snapshot.ValidatorRegistry, snapshot.MetadataStore, snapshot.ContentStore, snapshot.DigestLockManager, time.Duration, time.Duration) (snapshot.SnapshotCreator, error) {
		return &compositionOutputSealer{}, nil
	}
	command.agentSnapshotLifecycleComposer = func(snapshot.MetadataStore, snapshot.ContentStore, snapshot.ReplicaRepairer, snapshot.DigestLockManager) (snapshotLifecycle, error) {
		return &compositionLifecycle{}, nil
	}

	if err := command.composeAgentSnapshots(&dbfakes.FakeDbConn{}, lagertest.NewTestLogger("publisher-gateway-composition")); err != nil {
		t.Fatal(err)
	}
	if command.agentSnapshotPublisher == nil {
		t.Fatal("enabled publisher gateway was not composed")
	}
	if _, ok := command.agentSnapshotPublisher.(*publisher.Router); !ok {
		t.Fatalf("publisher = %T, want provider-neutral router", command.agentSnapshotPublisher)
	}
	options, ok := command.agentSnapshotCoreStepFactoryOptions()
	if !ok || len(options) != 4 {
		t.Fatalf("gateway composition engine options = %d/%t, want sealer, canonicalizer, loader, publisher", len(options), ok)
	}
}

func TestAgentPublisherGatewayUsesDedicatedSnapshotScratch(t *testing.T) {
	command := &RunCommand{}
	command.AgentSnapshots.MaxBytes = 1 << 20
	command.AgentSnapshots.MaxFiles = 100
	command.AgentSnapshots.TempDir = t.TempDir()

	canonicalizer := command.agentPublisherGatewayConfig().SnapshotCanonicalizer
	if canonicalizer.TempDir != command.AgentSnapshots.TempDir {
		t.Fatalf("publisher canonicalizer temp dir = %q, want %q", canonicalizer.TempDir, command.AgentSnapshots.TempDir)
	}
}
