package atccmd

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/worker/jetbridge"
)

type compositionContentStore struct{}

func (*compositionContentStore) Put(context.Context, snapshot.Digest, io.Reader) ([]snapshot.Location, error) {
	return nil, nil
}
func (*compositionContentStore) Open(context.Context, snapshot.Snapshot) (io.ReadCloser, error) {
	return nil, nil
}
func (*compositionContentStore) Exists(context.Context, snapshot.Location) (bool, error) {
	return false, nil
}
func (*compositionContentStore) DeleteLocation(context.Context, snapshot.Location) error { return nil }
func (*compositionContentStore) DeleteAll(context.Context, snapshot.Digest) error        { return nil }

func TestAgentSnapshotComponentsAreComposedOnceWithExplicitConnection(t *testing.T) {
	command := &RunCommand{}
	command.AgentSnapshots.Enabled = true
	command.AgentSnapshots.MaxBytes = 17
	command.AgentSnapshots.MaxFiles = 3
	wantDaemon := &jetbridge.DaemonClient{}
	wantStore := &compositionContentStore{}
	var calls int
	var gotConnection db.DbConn
	command.agentSnapshotComposer = func(connection db.DbConn) (*jetbridge.DaemonClient, snapshot.ContentStore, error) {
		calls++
		gotConnection = connection
		return wantDaemon, wantStore, nil
	}

	var explicitConnection db.DbConn = &dbfakes.FakeDbConn{}
	if err := command.composeAgentSnapshots(explicitConnection); err != nil {
		t.Fatal(err)
	}
	if err := command.composeAgentSnapshots(nil); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || gotConnection != explicitConnection {
		t.Fatalf("composer calls/connection = %d/%#v, want once with explicit connection", calls, gotConnection)
	}
	if command.agentSnapshotDaemonClient != wantDaemon || command.agentSnapshotContentStore != wantStore {
		t.Fatal("composition did not retain exact daemon/store identity")
	}
	if command.agentSnapshotArchiveLimits != (snapshot.ArchiveLimits{MaxContentBytes: 17, MaxEntries: 3}) {
		t.Fatalf("retained archive limits = %#v", command.agentSnapshotArchiveLimits)
	}
}

func TestAgentSnapshotCompositionDisabledRemainsNil(t *testing.T) {
	command := &RunCommand{}
	command.agentSnapshotComposer = func(db.DbConn) (*jetbridge.DaemonClient, snapshot.ContentStore, error) {
		t.Fatal("disabled composition invoked constructor")
		return nil, nil, nil
	}
	if err := command.composeAgentSnapshots(nil); err != nil {
		t.Fatal(err)
	}
	if command.agentSnapshotDaemonClient != nil || command.agentSnapshotContentStore != nil {
		t.Fatal("disabled snapshot composition must remain nil")
	}
}

func TestAgentSnapshotCompositionFailureIsFailClosed(t *testing.T) {
	command := &RunCommand{}
	command.AgentSnapshots.Enabled = true
	command.agentSnapshotComposer = func(db.DbConn) (*jetbridge.DaemonClient, snapshot.ContentStore, error) {
		return nil, nil, errors.New("invalid mTLS")
	}
	if err := command.composeAgentSnapshots(nil); err == nil {
		t.Fatal("expected composition failure")
	}
	if command.agentSnapshotDaemonClient != nil || command.agentSnapshotContentStore != nil {
		t.Fatal("failed composition published partial components")
	}
	if command.agentSnapshotArchiveLimits != (snapshot.ArchiveLimits{}) {
		t.Fatal("failed composition published snapshot admission limits")
	}
}
