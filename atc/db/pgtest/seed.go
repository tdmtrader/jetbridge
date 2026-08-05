package pgtest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
)

// SeedTeam creates a real team.
func SeedTeam(t *testing.T, conn db.DbConn, name string) db.Team {
	t.Helper()
	team, err := db.NewTeamFactory(conn, lockFactoryFor(t, conn)).CreateTeam(atc.Team{Name: name})
	if err != nil {
		t.Fatalf("pgtest: create team %q: %v", name, err)
	}
	return team
}

// SeedSnapshot seals one snapshot owned by team, and returns it.
//
// It commits an upload occurrence rather than a build occurrence, which is
// what keeps this cheap: an upload occurrence declares no lineage inputs and
// needs no build, job or pipeline row, so a caller gets a team-owned snapshot
// without reproducing atc/db's fixture scaffolding. Only metadata is written —
// no bytes are stored — which is enough for the authorization reads.
func SeedSnapshot(t *testing.T, conn db.DbConn, team db.Team, hexDigit string) snapshot.SnapshotRef {
	t.Helper()

	if len(hexDigit) != 1 {
		t.Fatalf("pgtest: hexDigit must be one character, got %q", hexDigit)
	}
	ctx := context.Background()
	value := snapshot.Digest("sha256:" + strings.Repeat(hexDigit, 64))

	factory := db.NewAgentSnapshotsFactory(conn)
	lease, err := db.NewAgentSnapshotDigestLocker(conn).AcquireMany(ctx, []snapshot.Digest{value})
	if lease != nil {
		defer lease.Close()
	}
	if err != nil {
		t.Fatalf("pgtest: acquire digest lease: %v", err)
	}

	key := fmt.Sprintf("seed-%s-%d", hexDigit, team.ID())
	staged, err := factory.StageUpload(ctx, lease, snapshot.StageUploadRequest{
		Digest:         value,
		TeamID:         team.ID(),
		Attempt:        "upload:" + key,
		LeaseExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("pgtest: stage upload: %v", err)
	}

	output := snapshot.SealCommitOutput{
		ClientKey:         "seed",
		Port:              snapshot.Port{Name: "snapshot", Type: snapshot.TypeRef("opaque/v1")},
		Digest:            value,
		ByteSize:          128,
		FileCount:         1,
		Representation:    "application/vnd.jetbridge.snapshot.tar.v1",
		IntrinsicMetadata: json.RawMessage(`{"tree":"seed"}`),
		StagedUploadID:    staged.ID,
		Locations: []snapshot.Location{{
			Digest: value,
			Driver: "jetbridge-daemon-v1",
			Key:    "snapshots/" + value.String(),
			Node:   "seed-node",
		}},
		// An upload occurrence has no build to inherit retention from, so it
		// must state its own or sealing refuses it.
		Retention: []snapshot.RetentionSpec{{
			Class:  snapshot.RetentionClassPin,
			Actor:  "pgtest",
			Reason: "seeded fixture",
		}},
	}

	sealed, err := factory.CommitSealBatch(ctx, lease, snapshot.SealCommit{
		Context: snapshot.SealCommitContext{
			TeamID:          team.ID(),
			TeamName:        team.Name(),
			CreatedBy:       "pgtest",
			Upload:          &snapshot.UploadOccurrence{IdempotencyKey: key},
			Inputs:          map[string]snapshot.SnapshotRef{},
			InputOrder:      []string{},
			ExpectedOutputs: []snapshot.Port{output.Port},
		},
		Outputs: []snapshot.SealCommitOutput{output},
	})
	if err != nil {
		t.Fatalf("pgtest: commit seal batch: %v", err)
	}
	return sealed["seed"].Snapshot
}

// lockFactoryFor opens the raw connections a LockFactory needs against the same
// database the conn is bound to. CreateTeam does not itself take a lock, but
// the team it returns carries the factory, so handing it a real one keeps the
// seeded team usable for anything a test does later.
func lockFactoryFor(t *testing.T, conn db.DbConn) lock.LockFactory {
	t.Helper()
	value, ok := dsnByConn.Load(conn)
	if !ok {
		t.Fatal("pgtest: connection was not handed out by OpenTestDB")
	}
	var conns [lock.FactoryCount]*sql.DB
	for i := range conns {
		raw, err := sql.Open("pgx", value.(string))
		if err != nil {
			t.Fatalf("pgtest: open lock connection: %v", err)
		}
		raw.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = raw.Close() })
		conns[i] = raw
	}
	noop := func(lager.Logger, lock.LockID) {}
	return lock.NewLockFactory(conns, noop, noop)
}
