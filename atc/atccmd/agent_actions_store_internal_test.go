package atccmd

import (
	"reflect"
	"testing"

	actionsapi "github.com/concourse/concourse/agent/api/actions"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
)

// TestAgentActionsStoreIsDatabaseBackedNotPerNodeMemory pins the wiring at
// command.go's agentActionsStore (the value constructAPIHandler feeds
// api.NewHandler's agentActionsStore parameter) to the DB-backed factory.
//
// actionsapi.NewMemoryStore exists only for agent/api/actions's own unit
// tests (see handler_test.go). It is per-process: if it were ever wired into
// production instead of db.NewAgentSettingsFactory, each ATC web node would
// keep its own independent copy of the cluster-wide action-suppression
// switch, so "fly agent actions suppress" would engage on only the one node
// that handled the PUT while every other node kept publishing — silently
// defeating the whole point of the brake. That failure mode is severe enough
// to pin directly, rather than trust review alone to catch a regression.
func TestAgentActionsStoreIsDatabaseBackedNotPerNodeMemory(t *testing.T) {
	command := &RunCommand{}
	conn := &dbfakes.FakeDbConn{}

	store := command.agentActionsStore(conn)

	if store == nil {
		t.Fatal("agentActionsStore returned nil")
	}
	if _, isMemory := store.(*actionsapi.MemoryStore); isMemory {
		t.Fatal("actions API handler was wired to actionsapi.NewMemoryStore, a per-node in-memory brake; " +
			"it must be db.NewAgentSettingsFactory so the cluster-wide switch is actually cluster-wide")
	}

	want := reflect.TypeOf(db.NewAgentSettingsFactory(conn))
	if got := reflect.TypeOf(store); got != want {
		t.Fatalf("agentActionsStore() type = %s, want %s (db.NewAgentSettingsFactory)", got, want)
	}
}
