# WS3 — Containment: Kill Switch, Budget Watchdog, Wall-Clock Bounds

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the platform three containment mechanisms it does not have today: a cluster-wide switch that suppresses every external side effect without a redeploy, a runner-side watchdog that actually terminates a run that breaches its budget/turn/wall-clock caps, and a default wall-clock bound on agent steps — plus tests that make the existing `--max-turns`/`--max-budget-usd` plumbing impossible to delete silently.

**Architecture:** The switch clones the dispatcher-mode pattern end to end: one nullable column on the singleton `agent_settings` row, hot-read factory methods, a fail-safe resolver in Go, a GET/PUT API pair behind the same wrappa tiers, and a `fly` leaf command. Enforcement lives at the single choke point every external effect flows through — the two publisher services (`agent/publisher/git.go`, `workitem.go`) — checked after the durable intent is acquired and before any provider interaction, so a suppressed operation stays `pending` and retries exactly once after resume. The runner watchdog rides the existing claude stdout stream as an extra `io.Writer`, folds cumulative cost/turns out of the stream-json lines, and on breach fires the already-tested `cmd.Cancel` process-group kill, stamping `terminated_reason` into `flight/results.json`. The web side gains `--agent-step-default-timeout`, and derives the runner's `AGENT_MAX_WALL_CLOCK` from the step's effective timeout so the pod always self-terminates *inside* the web-side deadline.

**Tech Stack:** Go, PostgreSQL migrations/factories, counterfeiter fakes, ATC exec/engine/atccmd wiring, rata routes + wrappa auth tiers, `fly` go-flags commands, Ginkgo/Gomega in `atc/`, plain `testing` in `agent/` and `fly/commands`.

## Global Constraints

- Follow the mechanism decisions in [the design](../../specs/2026-07-25-platform-test-hardening-design.md) §WS3. Fail-safe directions, enforcement points, and flag names are binding.
- Suppression bounds **external effects only**. Do not gate dispatch, agent execution, sealing, or experiments — that property is what makes the switch a shadow-mode enabler.
- `agent/schema` must never import the main module. This plan does not touch it (`results.json` carries the termination reason in the existing `Metadata` map).
- Test conventions hold: plain `testing` in `agent/`, `fly/commands`, and `atc/atccmd`; Ginkgo in `atc/exec`, `atc/db`, `atc/db/migration`, `atc/wrappa`, and `fly/integration`.
- No new third-party dependencies.
- PostgreSQL must be running for the `atc/db` and `atc/db/migration` steps (`pg_isready`).
- Every task ends green and is independently landable. Tasks 1–3 make the switch work end to end; 4–5 are the API/CLI surface; 6–8 are independent of 1–5.

---

### Task 1: Persist the cluster-wide action-suppression switch

**Files:**
- Create: `atc/db/migration/migrations/1773106128_add_agent_actions_mode.up.sql`
- Create: `atc/db/migration/migrations/1773106128_add_agent_actions_mode.down.sql`
- Create: `atc/db/migration/agent_actions_mode_test.go`
- Modify: `atc/db/agent_settings.go`
- Modify: `atc/db/agent_settings_test.go`
- Modify: `atc/db/dbfakes/fake_agent_settings_factory.go` (regenerated)
- Modify: `atc/db/migration/legacy_upgrade_test.go`

- [ ] **Re-verify the migration number is still free.** Run `ls atc/db/migration/migrations/ | sort | tail -3` and `grep -n jetbridgeHeadMigration atc/db/migration/legacy_upgrade_test.go`. Expected: the last migration is `1773106127_capture_snapshot_exposure_lineage.*` and the constant is `1773106127`. If anything above `1773106127` exists, renumber this task's migration to `<highest>+1` and use that number everywhere below.

- [ ] Write the failing DB spec first. Append to `atc/db/agent_settings_test.go`:

```go
	It("defaults actions to active and reports the switch as unset", func() {
		mode, found, err := settings.GetActionsMode()
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
		Expect(mode).To(BeEmpty())
	})

	It("engages and releases the switch, recording its own provenance", func() {
		Expect(settings.SetActionsMode(db.ActionsModeSuppressed, "tdm")).To(Succeed())

		mode, found, err := settings.GetActionsMode()
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(mode).To(Equal(db.ActionsModeSuppressed))

		gotMode, updatedAt, updatedBy, found, err := settings.GetActionsSetting()
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(gotMode).To(Equal(db.ActionsModeSuppressed))
		Expect(updatedBy).To(Equal("tdm"))
		Expect(updatedAt).ToNot(BeZero())

		Expect(settings.SetActionsMode(db.ActionsModeActive, "ada")).To(Succeed())
		mode, _, err = settings.GetActionsMode()
		Expect(err).ToNot(HaveOccurred())
		Expect(mode).To(Equal(db.ActionsModeActive))

		var count int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_settings`).Scan(&count)).To(Succeed())
		Expect(count).To(Equal(1))
	})

	It("rejects an invalid actions mode before touching the DB", func() {
		err := settings.SetActionsMode("halt", "tdm")
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, db.ErrInvalidActionsMode)).To(BeTrue())

		_, found, err := settings.GetActionsMode()
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	// The two settings share one row but must not overwrite each other's
	// meaning. Engaging the switch must NOT invent a dispatcher mode: any
	// value would make the dispatcher's "no row -> boot flag" fallback stop
	// applying and silently change dispatch behavior on a live cluster.
	It("keeps the dispatcher setting unset when only the switch is engaged", func() {
		Expect(settings.SetActionsMode(db.ActionsModeSuppressed, "tdm")).To(Succeed())

		_, found, err := settings.GetDispatcherMode()
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	It("keeps the switch unset when only the dispatcher mode is set", func() {
		Expect(settings.SetDispatcherMode(db.DispatcherModeActive, "tdm")).To(Succeed())

		mode, found, err := settings.GetActionsMode()
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
		Expect(mode).To(BeEmpty())

		dispatcherMode, found, err := settings.GetDispatcherMode()
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(dispatcherMode).To(Equal(db.DispatcherModeActive))
	})
```

- [ ] Run `ginkgo --focus='agent settings' ./atc/db/` and confirm it fails to compile with `settings.GetActionsMode undefined (type db.AgentSettingsFactory has no field or method GetActionsMode)`.

- [ ] Write `atc/db/migration/migrations/1773106128_add_agent_actions_mode.up.sql`:

```sql
-- The cluster-wide action-suppression switch: an emergency brake that stops
-- every EXTERNAL side effect (publisher writes) without a redeploy. It does
-- NOT gate dispatch, agent execution, or sealing — suppression bounds effects,
-- not compute, which is what makes a shadow-mode rollout phase possible.
--
-- ABSENCE of an explicit setting means 'active': a brake nobody has touched is
-- not engaged. A FAILED read is treated as 'suppressed' by the readers; that
-- fail-safe policy lives in Go (publisher.EffectiveActionsMode), not here.
--
-- actions_updated_at/by are the switch's OWN provenance. The pre-existing
-- updated_at/updated_by belong to dispatcher_mode; sharing them would make
-- "who paused the dispatcher" read as whoever last touched the switch.
ALTER TABLE agent_settings
    ADD COLUMN actions_mode TEXT NOT NULL DEFAULT 'active'
        CHECK (actions_mode IN ('active','suppressed')),
    ADD COLUMN actions_updated_at TIMESTAMPTZ,
    ADD COLUMN actions_updated_by TEXT;

-- agent_settings is now a MULTI-SETTING singleton. Engaging the switch must be
-- able to CREATE the row without inventing a dispatcher mode, so dispatcher_mode
-- becomes nullable and NULL means "never set" — exactly the same effective mode
-- as no row at all (db.GetDispatcherSetting maps NULL to found=false, and
-- dispatch.ResolveEffectiveMode then honors the --agent-dispatcher-enabled boot
-- flag). The CHECK still constrains every non-NULL value.
ALTER TABLE agent_settings ALTER COLUMN dispatcher_mode DROP NOT NULL;
```

- [ ] Write `atc/db/migration/migrations/1773106128_add_agent_actions_mode.down.sql`:

```sql
-- Restoring NOT NULL needs a value for any row the switch created without a
-- dispatcher mode. 'off' is the effective mode such a row already reports when
-- --agent-dispatcher-enabled is unset; on a cluster booted with the flag set
-- this down migration therefore pins the dispatcher off until an admin sets a
-- mode again. That is deliberate: fail dormant, not fail dispatching.
UPDATE agent_settings SET dispatcher_mode = 'off' WHERE dispatcher_mode IS NULL;
ALTER TABLE agent_settings ALTER COLUMN dispatcher_mode SET NOT NULL;

ALTER TABLE agent_settings
    DROP COLUMN actions_updated_by,
    DROP COLUMN actions_updated_at,
    DROP COLUMN actions_mode;
```

- [ ] In `atc/db/agent_settings.go`, add the mode constants and error immediately below `ErrInvalidDispatcherMode`:

```go
// Valid action-suppression modes, mirrored by the agent_settings.actions_mode
// CHECK constraint (migration 1773106128) and by agent/publisher. Bare strings
// for the same reason as the dispatcher modes above: atc/db must not depend on
// agent/publisher.
const (
	ActionsModeActive     = "active"
	ActionsModeSuppressed = "suppressed"
)

// ErrInvalidActionsMode is returned by SetActionsMode for a mode outside
// {active,suppressed} — a guard in front of the CHECK constraint.
var ErrInvalidActionsMode = errors.New("actions_mode must be one of active|suppressed")
```

- [ ] Extend the `AgentSettingsFactory` interface (inside the same `//counterfeiter:generate` block) with:

```go
	// GetActionsMode is the publisher's HOT read of the cluster-wide
	// action-suppression switch. found=false means no admin has ever engaged
	// it, which callers must treat as active.
	GetActionsMode() (mode string, found bool, err error)
	// GetActionsSetting backs the GET status API: the stored mode plus its own
	// provenance. found=false means the switch has never been set.
	GetActionsSetting() (mode string, updatedAt time.Time, updatedBy string, found bool, err error)
	// SetActionsMode UPSERTs the singleton row (id=1) without touching
	// dispatcher_mode or its provenance. Validates mode before touching the DB.
	SetActionsMode(mode, updatedBy string) error
```

- [ ] Make `GetDispatcherSetting` NULL-aware — scan `dispatcher_mode` into a `sql.NullString` and return `found=false` when it is NULL:

```go
func (f *agentSettingsFactory) GetDispatcherSetting() (string, time.Time, string, bool, error) {
	var (
		mode      sql.NullString
		updatedAt time.Time
		updatedBy sql.NullString
	)
	err := psql.Select("dispatcher_mode", "updated_at", "updated_by").
		From("agent_settings").
		Where(sq.Eq{"id": 1}).
		RunWith(f.conn).
		QueryRow().
		Scan(&mode, &updatedAt, &updatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, "", false, nil
	}
	if err != nil {
		return "", time.Time{}, "", false, err
	}
	if !mode.Valid {
		// The row was created by SetActionsMode, which must not invent a
		// dispatcher mode. NULL is exactly "never set" — same effective mode
		// as no row, so the boot-flag fallback keeps applying.
		return "", time.Time{}, "", false, nil
	}
	return mode.String, updatedAt, updatedBy.String, true, nil
}
```

- [ ] Add the three actions methods below it:

```go
func validActionsMode(mode string) bool {
	switch mode {
	case ActionsModeActive, ActionsModeSuppressed:
		return true
	default:
		return false
	}
}

func (f *agentSettingsFactory) GetActionsMode() (string, bool, error) {
	mode, _, _, found, err := f.GetActionsSetting()
	return mode, found, err
}

func (f *agentSettingsFactory) GetActionsSetting() (string, time.Time, string, bool, error) {
	var (
		mode      string
		updatedAt sql.NullTime
		updatedBy sql.NullString
	)
	err := psql.Select("actions_mode", "actions_updated_at", "actions_updated_by").
		From("agent_settings").
		Where(sq.Eq{"id": 1}).
		RunWith(f.conn).
		QueryRow().
		Scan(&mode, &updatedAt, &updatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, "", false, nil
	}
	if err != nil {
		return "", time.Time{}, "", false, err
	}
	if !updatedAt.Valid {
		// The row exists but nobody has touched the switch (SetDispatcherMode
		// created it and the column default filled in 'active'). Absence is
		// meaningful, so report it as unset rather than as an admin decision.
		return "", time.Time{}, "", false, nil
	}
	return mode, updatedAt.Time, updatedBy.String, true, nil
}

func (f *agentSettingsFactory) SetActionsMode(mode, updatedBy string) error {
	if !validActionsMode(mode) {
		return fmt.Errorf("%w: got %q", ErrInvalidActionsMode, mode)
	}
	_, err := psql.Insert("agent_settings").
		Columns("id", "actions_mode", "actions_updated_at", "actions_updated_by").
		Values(1, mode, sq.Expr("now()"), updatedBy).
		Suffix("ON CONFLICT (id) DO UPDATE SET actions_mode = EXCLUDED.actions_mode, " +
			"actions_updated_at = now(), actions_updated_by = EXCLUDED.actions_updated_by").
		RunWith(f.conn).
		Exec()
	return err
}
```

- [ ] Regenerate the counterfeiter fake: `go run github.com/maxbrunsfeld/counterfeiter/v6 -o atc/db/dbfakes/fake_agent_settings_factory.go atc/db AgentSettingsFactory`. Expected output: `Writing `FakeAgentSettingsFactory` to `dbfakes/fake_agent_settings_factory.go`... Done`. Confirm with `grep -c 'func (fake \*FakeAgentSettingsFactory) GetActionsMode' atc/db/dbfakes/fake_agent_settings_factory.go` → `1`.

- [ ] Bump `jetbridgeHeadMigration` in `atc/db/migration/legacy_upgrade_test.go` from `1773106127` to `1773106128`.

- [ ] Write `atc/db/migration/agent_actions_mode_test.go`, cloning the shape of `atc/db/migration/snapshot_exposure_lineage_test.go` (same imports, same `BeforeEach`/`AfterEach` connection setup):

```go
var _ = Describe("agent actions mode migration", func() {
	const beforeVersion, targetVersion = 1773106127, 1773106128
	// ... identical database/lockDB/migrator BeforeEach + AfterEach as
	// snapshot_exposure_lineage_test.go, migrating to beforeVersion ...

	It("defaults existing rows to active and lets the switch create a row without a dispatcher mode", func() {
		_, err := database.Exec(`
			INSERT INTO agent_settings (id, dispatcher_mode, updated_by)
			VALUES (1, 'active', 'tdm')
		`)
		Expect(err).NotTo(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		var mode string
		var actionsUpdatedAt sql.NullTime
		Expect(database.QueryRow(`
			SELECT actions_mode, actions_updated_at FROM agent_settings WHERE id = 1
		`).Scan(&mode, &actionsUpdatedAt)).To(Succeed())
		Expect(mode).To(Equal("active"))
		Expect(actionsUpdatedAt.Valid).To(BeFalse())

		_, err = database.Exec(`DELETE FROM agent_settings`)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`
			INSERT INTO agent_settings (id, actions_mode, actions_updated_at, actions_updated_by)
			VALUES (1, 'suppressed', now(), 'tdm')
		`)
		Expect(err).NotTo(HaveOccurred())

		var dispatcherMode sql.NullString
		Expect(database.QueryRow(`
			SELECT dispatcher_mode FROM agent_settings WHERE id = 1
		`).Scan(&dispatcherMode)).To(Succeed())
		Expect(dispatcherMode.Valid).To(BeFalse())
	})

	It("refuses an unrecognized actions mode", func() {
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		_, err := database.Exec(`
			INSERT INTO agent_settings (id, dispatcher_mode, actions_mode) VALUES (1, 'off', 'halt')
		`)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("actions_mode"))
	})

	It("rolls back by pinning switch-created rows to a dormant dispatcher", func() {
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		_, err := database.Exec(`
			INSERT INTO agent_settings (id, actions_mode, actions_updated_at, actions_updated_by)
			VALUES (1, 'suppressed', now(), 'tdm')
		`)
		Expect(err).NotTo(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())

		var dispatcherMode string
		Expect(database.QueryRow(`
			SELECT dispatcher_mode FROM agent_settings WHERE id = 1
		`).Scan(&dispatcherMode)).To(Succeed())
		Expect(dispatcherMode).To(Equal("off"))

		var columns int
		Expect(database.QueryRow(`
			SELECT count(*) FROM information_schema.columns
			WHERE table_name = 'agent_settings' AND column_name = 'actions_mode'
		`).Scan(&columns)).To(Succeed())
		Expect(columns).To(Equal(0))
	})
})
```

- [ ] Run `ginkgo --focus='agent settings|agent actions mode' ./atc/db/ ./atc/db/migration/` and confirm all specs pass.
- [ ] Run `ginkgo --focus='Legacy Database Upgrade' ./atc/db/migration/` and confirm the head-migration assertions pass at `1773106128`.
- [ ] Run `go build ./...` and confirm no caller of `AgentSettingsFactory` broke.
- [ ] Commit `feat(db): persist the cluster-wide action-suppression switch`.

---

### Task 2: Refuse external side effects while actions are suppressed

**Files:**
- Create: `agent/publisher/actions.go`
- Create: `agent/publisher/actions_test.go`
- Modify: `agent/publisher/git.go`
- Modify: `agent/publisher/workitem.go`

- [ ] Write `agent/publisher/actions_test.go` first:

```go
package publisher_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/publisher"
)

type actionsReaderStub struct {
	mode  string
	found bool
	err   error
	reads int
}

func (stub *actionsReaderStub) GetActionsMode() (string, bool, error) {
	stub.reads++
	return stub.mode, stub.found, stub.err
}

func TestEffectiveActionsModeFailsSafe(t *testing.T) {
	for _, test := range []struct {
		name  string
		mode  string
		found bool
		err   error
		want  string
	}{
		{name: "read error suppresses", err: errors.New("connection refused"), want: publisher.ActionsModeSuppressed},
		{name: "read error beats a stored active", mode: "active", found: true, err: errors.New("boom"), want: publisher.ActionsModeSuppressed},
		{name: "unset is active", want: publisher.ActionsModeActive},
		{name: "stored active is active", mode: "active", found: true, want: publisher.ActionsModeActive},
		{name: "stored suppressed is suppressed", mode: "suppressed", found: true, want: publisher.ActionsModeSuppressed},
		{name: "unrecognized value fails closed", mode: "halt", found: true, want: publisher.ActionsModeSuppressed},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := publisher.EffectiveActionsMode(test.mode, test.found, test.err); got != test.want {
				t.Fatalf("EffectiveActionsMode(%q, %t, %v) = %q, want %q", test.mode, test.found, test.err, got, test.want)
			}
		})
	}
}

func TestGitServiceMakesNoExternalCallWhileActionsAreSuppressed(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	store := publisher.NewMemoryStore(func() time.Time { return now })
	backend := &gitBackendStub{base: "base-sha", result: publisher.GitResult{ExternalID: "pr-1", HeadSHA: "head-sha"}}
	actions := &actionsReaderStub{mode: publisher.ActionsModeSuppressed, found: true}
	service, err := publisher.NewGitService(
		store,
		&credentialsStub{credential: publisher.Credential{Reference: "secret/git"}},
		changeInspectorStub{change: publisher.RepositoryChange{
			BaseSHA: "base-sha", ResultSHA: "head-sha", MaterializedRoot: "/change",
		}},
		backend, time.Minute, time.Minute,
		publisher.WithActionsGate(actions),
	)
	if err != nil {
		t.Fatal(err)
	}

	request := branchRequest()
	if _, err := service.Execute(context.Background(), request); !errors.Is(err, publisher.ErrActionsSuppressed) {
		t.Fatalf("Execute error = %v, want ErrActionsSuppressed", err)
	}
	if backend.lookups != 0 || len(backend.operations) != 0 || backend.baseReads != 0 {
		t.Fatalf("suppressed publish touched the provider: lookups=%d bases=%d operations=%d",
			backend.lookups, backend.baseReads, len(backend.operations))
	}

	key, err := request.OperationKey()
	if err != nil {
		t.Fatal(err)
	}
	publication, found, err := store.Get(context.Background(), key)
	if err != nil || !found {
		t.Fatalf("Get = (%+v, %t, %v)", publication, found, err)
	}
	if publication.Status != publisher.StatusPending {
		t.Fatalf("operation status = %q, want pending so the run can be retried after resume", publication.Status)
	}
}

func TestGitServiceSuppressesWhenTheSwitchCannotBeRead(t *testing.T) {
	store := publisher.NewMemoryStore(time.Now)
	backend := &gitBackendStub{base: "base-sha", result: publisher.GitResult{ExternalID: "pr-1", HeadSHA: "head-sha"}}
	service, err := publisher.NewGitService(
		store,
		&credentialsStub{credential: publisher.Credential{Reference: "secret/git"}},
		changeInspectorStub{change: publisher.RepositoryChange{
			BaseSHA: "base-sha", ResultSHA: "head-sha", MaterializedRoot: "/change",
		}},
		backend, time.Minute, time.Minute,
		publisher.WithActionsGate(&actionsReaderStub{err: errors.New("connection refused")}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Execute(context.Background(), branchRequest()); !errors.Is(err, publisher.ErrActionsSuppressed) {
		t.Fatalf("Execute error = %v, want ErrActionsSuppressed", err)
	}
	if len(backend.operations) != 0 {
		t.Fatalf("unreadable switch permitted %d provider writes", len(backend.operations))
	}
}

// The WS3 acceptance property: suppression must not corrupt idempotency.
// One semantic operation, refused once, then executed exactly once on retry.
func TestGitServicePublishesExactlyOnceAfterActionsResume(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	store := publisher.NewMemoryStore(func() time.Time { return now })
	backend := &gitBackendStub{base: "base-sha", result: publisher.GitResult{
		ExternalID: "refs/heads/agent/upgrade", URL: "https://github.example/pr/7", HeadSHA: "head-sha",
	}}
	actions := &actionsReaderStub{mode: publisher.ActionsModeSuppressed, found: true}
	service, err := publisher.NewGitService(
		store,
		&credentialsStub{credential: publisher.Credential{Reference: "secret/git"}},
		changeInspectorStub{change: publisher.RepositoryChange{
			BaseSHA: "base-sha", ResultSHA: "head-sha", MaterializedRoot: "/change",
		}},
		backend, time.Minute, time.Minute,
		publisher.WithActionsGate(actions),
	)
	if err != nil {
		t.Fatal(err)
	}

	request := branchRequest()
	if _, err := service.Execute(context.Background(), request); !errors.Is(err, publisher.ErrActionsSuppressed) {
		t.Fatalf("suppressed Execute error = %v, want ErrActionsSuppressed", err)
	}

	// Resume, and let the refused attempt's lease expire so the retry reclaims
	// execution instead of waiting behind it.
	actions.mode = publisher.ActionsModeActive
	now = now.Add(2 * time.Minute)

	publication, err := service.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("resumed Execute: %v", err)
	}
	if publication.Status != publisher.StatusSucceeded {
		t.Fatalf("resumed status = %q, want succeeded", publication.Status)
	}
	if len(backend.operations) != 1 {
		t.Fatalf("provider writes = %d, want exactly 1 across suppression and retry", len(backend.operations))
	}
	key, err := request.OperationKey()
	if err != nil {
		t.Fatal(err)
	}
	if publication.OperationKey != key {
		t.Fatalf("operation key = %q, want the unchanged semantic identity %q", publication.OperationKey, key)
	}
	if publication.Attempt != 2 {
		t.Fatalf("attempt = %d, want 2 (the refusal consumed attempt 1)", publication.Attempt)
	}
}

func TestWorkItemServiceMakesNoExternalCallWhileActionsAreSuppressed(t *testing.T) {
	store := publisher.NewMemoryStore(time.Now)
	backend := &workItemBackendStub{result: publisher.WorkItemResult{ExternalID: "comment-9"}}
	service, err := publisher.NewWorkItemService(
		store,
		&credentialsStub{credential: publisher.Credential{Reference: "secret/jira"}},
		validSnapshotValueInspector(),
		backend, time.Minute, time.Minute,
		publisher.WithActionsGate(&actionsReaderStub{mode: publisher.ActionsModeSuppressed, found: true}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Execute(context.Background(), commentRequest()); !errors.Is(err, publisher.ErrActionsSuppressed) {
		t.Fatalf("Execute error = %v, want ErrActionsSuppressed", err)
	}
	if backend.lookups != 0 || len(backend.requests) != 0 {
		t.Fatalf("suppressed work-item publish touched the provider: lookups=%d writes=%d", backend.lookups, len(backend.requests))
	}
}
```

- [ ] `commentRequest()` may not exist yet. Run `grep -rn 'func commentRequest' agent/publisher/`; if it is absent, copy the request literal used by `TestWorkItemServicePublishesExplicitCommentIdempotently` in `agent/publisher/workitem_test.go` into a `commentRequest()` helper in that file and have the existing test call it, so both tests share one fixture.

- [ ] Run `go test ./agent/publisher/ -run 'Actions' -count=1` and confirm it fails to build with `undefined: publisher.ActionsModeSuppressed`.

- [ ] Write `agent/publisher/actions.go`:

```go
package publisher

import (
	"errors"
	"fmt"
)

// Cluster-wide action-suppression modes. These are the exact strings stored in
// agent_settings.actions_mode (migration 1773106128) and carried on the
// GET/PUT /api/v1/agent/actions wire.
//
//	active     — external side effects execute normally.
//	suppressed — every external side effect refuses BEFORE any provider call.
//
// Suppression bounds EXTERNAL EFFECTS ONLY: dispatch, agent execution, and
// sealing are deliberately NOT gated, which is what makes the switch a
// shadow-mode enabler rather than a stop-the-world button.
const (
	ActionsModeActive     = "active"
	ActionsModeSuppressed = "suppressed"
)

// ErrActionsSuppressed is the typed refusal every publisher service returns
// while the switch is engaged. It is RETRYABLE: the durable operation row is
// left pending, so the identical semantic operation — same OperationKey, same
// Idempotency-Key — executes exactly once after an admin resumes actions.
var ErrActionsSuppressed = errors.New(
	`publisher: external actions are suppressed by the platform action switch; ` +
		`resume with "fly agent actions resume" (PUT /api/v1/agent/actions {"mode":"active"}) and retry the build`)

// ActionsModeReader is the hot read of the cluster-wide switch.
// db.AgentSettingsFactory satisfies it. found=false means no admin has ever
// engaged the brake.
type ActionsModeReader interface {
	GetActionsMode() (mode string, found bool, err error)
}

// ValidActionsMode reports whether s is a recognized mode. The API layer uses
// it to reject a bad PUT before touching the database.
func ValidActionsMode(s string) bool {
	return s == ActionsModeActive || s == ActionsModeSuppressed
}

// EffectiveActionsMode applies the fail-safe policy to one settings read.
//
//   - readErr != nil ⇒ suppressed. A DB fault must never be the reason an
//     external side effect escapes an engaged brake, and a node that cannot
//     read agent_settings could not have completed the durable publish
//     protocol anyway. Same direction as dispatch.EffectiveModeFromRead, which
//     pauses on read error.
//   - found == false ⇒ active. Absence is meaningful: the switch is an
//     emergency brake and an untouched brake is not engaged.
//   - any stored value other than exactly "active" ⇒ suppressed. The CHECK
//     constraint makes an unknown value unreachable through the API; if one
//     ever appears, fail closed rather than guess.
func EffectiveActionsMode(mode string, found bool, readErr error) string {
	if readErr != nil {
		return ActionsModeSuppressed
	}
	if !found {
		return ActionsModeActive
	}
	if mode == ActionsModeActive {
		return ActionsModeActive
	}
	return ActionsModeSuppressed
}

// ServiceOption configures optional publisher-service dependencies without
// changing the existing positional constructors.
type ServiceOption func(*serviceOptions)

type serviceOptions struct {
	actions ActionsModeReader
}

// WithActionsGate makes the service consult the cluster-wide
// action-suppression switch before every external interaction.
func WithActionsGate(reader ActionsModeReader) ServiceOption {
	return func(options *serviceOptions) { options.actions = reader }
}

func buildServiceOptions(options []ServiceOption) serviceOptions {
	var resolved serviceOptions
	for _, option := range options {
		option(&resolved)
	}
	return resolved
}

// checkActionsAdmitted is the choke point every external side effect calls
// before touching a provider — including the recovery Lookup, so a suppressed
// publisher makes no network call at all.
//
// A nil reader means the deployment composed a service without the switch.
// NewGatewayExecutor refuses that at startup, so nil here can only come from a
// direct in-test construction and is admitted.
func checkActionsAdmitted(reader ActionsModeReader) error {
	if nilInterface(reader) {
		return nil
	}
	mode, found, err := reader.GetActionsMode()
	if EffectiveActionsMode(mode, found, err) == ActionsModeSuppressed {
		if err != nil {
			return fmt.Errorf("%w (the switch could not be read: %v)", ErrActionsSuppressed, err)
		}
		return ErrActionsSuppressed
	}
	return nil
}
```

- [ ] In `agent/publisher/git.go`, add `actions ActionsModeReader` to `GitService`, widen the constructor to `func NewGitService(store Store, credentials CredentialProvider, changes ChangeInspector, backend GitBackend, timeout time.Duration, lease time.Duration, options ...ServiceOption) (*GitService, error)`, and set `actions: buildServiceOptions(options).actions` in the returned struct literal.

- [ ] In `GitService.Execute`, insert the gate immediately after the `publication.Request.ValidatePersisted()` block and before `authorizedRequest := publication.Request.Clone()`:

```go
	// The action switch is checked AFTER the durable intent is acquired and
	// BEFORE any external interaction (including the recovery Lookup): the
	// operation row stays pending, so this exact semantic operation is retried
	// unchanged — and executed exactly once — after an admin resumes actions.
	if err := checkActionsAdmitted(service.actions); err != nil {
		return Publication{}, err
	}
```

- [ ] Apply the identical three edits to `agent/publisher/workitem.go`: the `actions` field on `WorkItemService`, `options ...ServiceOption` on `NewWorkItemService`, and the same gate block after `ValidatePersisted` / before `externalContext, cancel := context.WithTimeout(...)`.

- [ ] Run `go test ./agent/publisher/ -count=1` and confirm every spec passes, including the pre-existing ones (the variadic option is additive, so no existing call site changes).
- [ ] Run `gofmt -l agent/publisher` and confirm empty output.
- [ ] Commit `feat(publisher): refuse external effects while actions are suppressed`.

---

### Task 3: Wire the switch into the gateway executor and web composition

**Files:**
- Modify: `agent/publisher/gateway.go`
- Modify: `agent/publisher/gateway_test.go`
- Modify: `atc/atccmd/agent_publisher_gateway.go`
- Modify: `atc/atccmd/command.go`
- Modify: `atc/atccmd/agent_snapshot_composition_internal_test.go`

- [ ] Add the failing composition test to `agent/publisher/gateway_test.go`:

```go
func TestNewGatewayExecutorRequiresAnActionsModeReader(t *testing.T) {
	// A deployment that can perform external side effects must be able to stop
	// them without a redeploy. Composing a publisher with no switch is a
	// startup error, not a silently unstoppable publisher.
	config := validGatewayConfigForTest(t)
	config.ActionsMode = nil

	_, err := publisher.NewGatewayExecutor(
		publisher.NewMemoryStore(time.Now),
		newGatewayMetadataStoreForTest(t),
		newGatewayContentStoreForTest(t),
		config,
	)
	if err == nil || !strings.Contains(err.Error(), "actions-mode reader") {
		t.Fatalf("NewGatewayExecutor error = %v, want an actions-mode reader requirement", err)
	}
}
```

Run `grep -n 'NewGatewayExecutor(' agent/publisher/gateway_test.go` first and reuse whatever config/store helpers that file already has; rename `validGatewayConfigForTest`/`newGateway*StoreForTest` above to the existing helper names rather than adding new ones.

- [ ] Run `go test ./agent/publisher/ -run 'GatewayExecutorRequires' -count=1` and confirm it fails with `config.ActionsMode undefined`.

- [ ] In `agent/publisher/gateway.go`, add to `GatewayConfig`:

```go
	// ActionsMode is the cluster-wide action-suppression switch. It is
	// REQUIRED by NewGatewayExecutor (but deliberately NOT by
	// ValidateGatewayConfig, which runs at flag-validation time before a
	// database connection exists).
	ActionsMode ActionsModeReader
```

- [ ] In `NewGatewayExecutor`, after the existing `nilInterface(store) || ...` guard, add:

```go
	if nilInterface(config.ActionsMode) {
		return nil, fmt.Errorf("publisher gateway: an actions-mode reader is required; without it the cluster-wide action switch cannot suppress this publisher")
	}
```

and pass `WithActionsGate(config.ActionsMode)` as the trailing argument to both `NewGitService(...)` and `NewWorkItemService(...)`.

- [ ] In `atc/atccmd/agent_publisher_gateway.go`, widen the builder:

```go
func (cmd *RunCommand) buildAgentPublisherGateway(
	store publisher.Store,
	metadata snapshot.MetadataStore,
	content snapshot.ContentStore,
	actions publisher.ActionsModeReader,
) (publisher.Executor, error) {
	if !cmd.AgentPublisherGateway.Enabled {
		return nil, fmt.Errorf("publisher gateway is disabled")
	}
	config := cmd.agentPublisherGatewayConfig()
	config.ActionsMode = actions
	executor, err := publisher.NewGatewayExecutor(store, metadata, content, config)
	if err != nil {
		return nil, fmt.Errorf("build publisher gateway: %w", err)
	}
	return executor, nil
}
```

- [ ] In `atc/atccmd/command.go`, widen the composition seam (around line 172):

```go
type snapshotPublisherComposer func(
	publisher.Store,
	snapshot.MetadataStore,
	snapshot.ContentStore,
	// The cluster-wide action switch. Threaded explicitly rather than read off
	// the command so a deployment-supplied composer cannot forget it.
	publisher.ActionsModeReader,
) (publisher.Executor, error)
```

- [ ] In `composeAgentSnapshots` (around line 1838), pass the settings factory as the fourth argument:

```go
		snapshotPublisher, err = publisherComposer(
			db.NewAgentPublicationsFactory(connection),
			metadataStore,
			contentStore,
			db.NewAgentSettingsFactory(connection),
		)
```

- [ ] Update the composer stub in `atc/atccmd/agent_snapshot_composition_internal_test.go` (around line 148) to the new signature and assert the reader arrives:

```go
	var gotActionsReader publisher.ActionsModeReader
	command.agentSnapshotPublisherComposer = func(
		store publisher.Store,
		metadata snapshot.MetadataStore,
		content snapshot.ContentStore,
		actions publisher.ActionsModeReader,
	) (publisher.Executor, error) {
		gotPublicationStore = store
		publisherMetadata = metadata
		publisherContent = content
		gotActionsReader = actions
		return wantPublisher, nil
	}
```

and, in the same test's assertion block, add:

```go
	if gotActionsReader == nil {
		t.Fatal("publisher composition was not given the cluster-wide action switch")
	}
```

- [ ] Run `go build ./...` and confirm it compiles.
- [ ] Run `go test ./agent/publisher/ ./atc/atccmd/ -count=1` and confirm both pass.
- [ ] Commit `feat(atc): compose publishers with the cluster-wide action switch`.

---

### Task 4: Serve the action switch over the API

**Files:**
- Create: `agent/api/actions/handler.go`
- Create: `agent/api/actions/memory_store.go`
- Create: `agent/api/actions/handler_test.go`
- Create: `agent/api/actions/route_registration_test.go`
- Modify: `atc/routes.go`
- Modify: `atc/api/handler.go`
- Modify: `atc/api/api_suite_test.go`
- Modify: `atc/wrappa/api_auth_wrappa.go`
- Modify: `atc/wrappa/api_auth_wrappa_test.go`
- Modify: `atc/wrappa/reject_archived_wrappa.go`
- Modify: `atc/auditor/auditor.go`

- [ ] Write `agent/api/actions/route_registration_test.go`, cloning `agent/api/feedback/route_registration_test.go` exactly, with:

```go
	requiredRoutes := []struct {
		name   string
		method string
		path   string
	}{
		{atc.GetAgentActions, "GET", "/api/v1/agent/actions"},
		{atc.SetAgentActions, "PUT", "/api/v1/agent/actions"},
	}
```

- [ ] Write `agent/api/actions/handler_test.go`, mirroring `agent/api/dispatcher/handler_test.go`:

```go
package actions_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	actionsapi "github.com/concourse/concourse/agent/api/actions"
)

func newHandler() (*actionsapi.Handler, *actionsapi.MemoryStore) {
	store := actionsapi.NewMemoryStore()
	return actionsapi.NewHandler(store, func(r *http.Request) string {
		return r.Header.Get("X-Test-User")
	}), store
}

func getStatus(t *testing.T, h *actionsapi.Handler) actionsapi.Response {
	t.Helper()
	rec := httptest.NewRecorder()
	h.Get(rec, httptest.NewRequest("GET", "/api/v1/agent/actions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body %s", rec.Code, rec.Body.String())
	}
	var resp actionsapi.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

func TestGetReportsActiveUntilTheSwitchIsEngaged(t *testing.T) {
	h, _ := newHandler()
	resp := getStatus(t, h)
	if resp.Mode != "active" || resp.Source != "default" {
		t.Fatalf("got %+v, want mode=active source=default", resp)
	}
	if resp.UpdatedAt != nil || resp.UpdatedBy != nil {
		t.Errorf("expected null provenance before the switch is set, got %+v", resp)
	}
}

func TestPutSuppressesAndRecordsTheActor(t *testing.T) {
	h, _ := newHandler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/agent/actions", strings.NewReader(`{"mode":"suppressed"}`))
	req.Header.Set("X-Test-User", "tdm")
	h.Set(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body %s", rec.Code, rec.Body.String())
	}

	resp := getStatus(t, h)
	if resp.Mode != "suppressed" || resp.Source != "setting" {
		t.Fatalf("got %+v, want mode=suppressed source=setting", resp)
	}
	if resp.UpdatedBy == nil || *resp.UpdatedBy != "tdm" || resp.UpdatedAt == nil {
		t.Fatalf("provenance = %+v, want updated_by=tdm and a timestamp", resp)
	}
}

func TestPutRejectsAnUnknownMode(t *testing.T) {
	h, _ := newHandler()
	rec := httptest.NewRecorder()
	h.Set(rec, httptest.NewRequest("PUT", "/api/v1/agent/actions", strings.NewReader(`{"mode":"halt"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT status = %d, want 400", rec.Code)
	}
	if resp := getStatus(t, h); resp.Mode != "active" {
		t.Fatalf("rejected PUT changed the mode to %q", resp.Mode)
	}
}

func TestPutRejectsInvalidJSON(t *testing.T) {
	h, _ := newHandler()
	rec := httptest.NewRecorder()
	h.Set(rec, httptest.NewRequest("PUT", "/api/v1/agent/actions", strings.NewReader(`{`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT status = %d, want 400", rec.Code)
	}
}

func TestPutWithoutAnIdentityRecordsAnHonestSentinel(t *testing.T) {
	h, _ := newHandler()
	rec := httptest.NewRecorder()
	h.Set(rec, httptest.NewRequest("PUT", "/api/v1/agent/actions", strings.NewReader(`{"mode":"suppressed"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d", rec.Code)
	}
	resp := getStatus(t, h)
	if resp.UpdatedBy == nil || *resp.UpdatedBy != "unknown" {
		t.Fatalf("updated_by = %+v, want the \"unknown\" sentinel", resp.UpdatedBy)
	}
}

type failingStore struct{}

func (failingStore) GetActionsSetting() (string, time.Time, string, bool, error) {
	return "", time.Time{}, "", false, errors.New("connection refused")
}
func (failingStore) SetActionsMode(string, string) error { return errors.New("connection refused") }

// A read fault must be an ERROR on the wire, never a cheerful "active": an
// operator checking whether the brake is on must not be told it is off because
// the database is down.
func TestGetSurfacesAReadFaultInsteadOfGuessing(t *testing.T) {
	h := actionsapi.NewHandler(failingStore{}, func(*http.Request) string { return "tdm" })
	rec := httptest.NewRecorder()
	h.Get(rec, httptest.NewRequest("GET", "/api/v1/agent/actions", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("GET status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "active") {
		t.Fatalf("read fault reported a mode: %s", rec.Body.String())
	}
}
```

- [ ] Run `go test ./agent/api/actions/... -count=1` and confirm `no Go files in .../agent/api/actions`.

- [ ] Write `agent/api/actions/handler.go`:

```go
// Package actions serves the cluster-wide action-suppression API:
// GET  /api/v1/agent/actions (GetAgentActions — any authenticated user)
// PUT  /api/v1/agent/actions (SetAgentActions — admin only).
//
// The switch stops every EXTERNAL side effect (publisher writes) without a
// redeploy. It does not gate dispatch, agent execution, or sealing.
// Authentication and admin authorization are enforced by the wrappa layer
// (atc/wrappa/api_auth_wrappa.go); the handler trusts the request.
package actions

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/concourse/concourse/agent/publisher"
)

// Store is the persistence seam. db.AgentSettingsFactory satisfies it; the
// tests use a memory store. found=false means no admin has engaged the switch.
type Store interface {
	GetActionsSetting() (mode string, updatedAt time.Time, updatedBy string, found bool, err error)
	SetActionsMode(mode, updatedBy string) error
}

// UserNameFunc derives the requesting user's username (injected from the
// accessor by atc/api; this package never imports atc/api).
type UserNameFunc func(*http.Request) string

type Handler struct {
	store    Store
	userName UserNameFunc
}

func NewHandler(store Store, userName UserNameFunc) *Handler {
	return &Handler{store: store, userName: userName}
}

// Response is the GET/PUT body. mode is the EFFECTIVE mode every publisher
// honors now; source is "setting" when an admin set it and "default" when the
// switch has never been engaged. Note there is no boot flag: unlike the
// dispatcher, the switch has exactly one unset meaning — active.
type Response struct {
	Mode      string  `json:"mode"`
	Source    string  `json:"source"`
	UpdatedAt *string `json:"updated_at"`
	UpdatedBy *string `json:"updated_by"`
}

func (h *Handler) currentState() (Response, error) {
	mode, updatedAt, updatedBy, found, err := h.store.GetActionsSetting()
	if err != nil {
		// Deliberately NOT publisher.EffectiveActionsMode(…, err): the
		// fail-safe "suppressed" answer is for ENFORCEMENT. A reader asking
		// what the switch says must get an error, not a guess in either
		// direction.
		return Response{}, err
	}
	resp := Response{
		Mode:   publisher.EffectiveActionsMode(mode, found, nil),
		Source: "default",
	}
	if found {
		resp.Source = "setting"
		at := updatedAt.UTC().Format(time.RFC3339)
		resp.UpdatedAt = &at
		by := updatedBy
		resp.UpdatedBy = &by
	}
	return resp, nil
}

// Get handles GET /api/v1/agent/actions.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	resp, err := h.currentState()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// Set handles PUT /api/v1/agent/actions.
func (h *Handler) Set(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if !publisher.ValidActionsMode(body.Mode) {
		http.Error(w, "mode must be one of active|suppressed", http.StatusBadRequest)
		return
	}

	identity := h.userName(r)
	if identity == "" {
		// Record an honest sentinel rather than fabricating an actor for a
		// security-relevant control.
		identity = "unknown"
	}
	if err := h.store.SetActionsMode(body.Mode, identity); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp, err := h.currentState()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
```

- [ ] Write `agent/api/actions/memory_store.go`:

```go
package actions

import (
	"errors"
	"sync"
	"time"

	"github.com/concourse/concourse/agent/publisher"
)

var errInvalidMode = errors.New("actions mode must be one of active|suppressed")

// MemoryStore is an in-memory Store for tests and the atc/api suite. It mirrors
// the db factory's contract: never set -> found=false; SetActionsMode validates.
type MemoryStore struct {
	mu        sync.Mutex
	set       bool
	mode      string
	updatedAt time.Time
	updatedBy string
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{} }

func (m *MemoryStore) GetActionsSetting() (string, time.Time, string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.set {
		return "", time.Time{}, "", false, nil
	}
	return m.mode, m.updatedAt, m.updatedBy, true, nil
}

func (m *MemoryStore) SetActionsMode(mode, updatedBy string) error {
	if !publisher.ValidActionsMode(mode) {
		return errInvalidMode
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.set = true
	m.mode = mode
	m.updatedBy = updatedBy
	m.updatedAt = time.Now()
	return nil
}
```

- [ ] In `atc/routes.go`, add the constants next to the dispatcher pair:

```go
	GetAgentActions = "GetAgentActions"
	SetAgentActions = "SetAgentActions"
```

and the route entries next to the dispatcher routes:

```go
	{Path: "/api/v1/agent/actions", Method: "GET", Name: GetAgentActions},
	{Path: "/api/v1/agent/actions", Method: "PUT", Name: SetAgentActions},
```

- [ ] In `atc/api/handler.go`: import `actionsapi "github.com/concourse/concourse/agent/api/actions"`; add a parameter `agentActionsStore actionsapi.Store` immediately after `agentDispatcherBootDefault bool` with the comment `// agentActionsStore backs the cluster-wide action-suppression routes (Get/SetAgentActions).`; build the server immediately below the existing `dispatcherServer := dispatcherapi.NewHandler(…)` block (~line 222), reusing its identity closure verbatim:

```go
	actionsServer := actionsapi.NewHandler(
		agentActionsStore,
		func(r *http.Request) string {
			return accessor.GetAccessor(r).Claims().UserName
		},
	)
```

and register, beside the dispatcher entries (~line 451):

```go
		atc.GetAgentActions: http.HandlerFunc(actionsServer.Get),
		atc.SetAgentActions: http.HandlerFunc(actionsServer.Set),
```

- [ ] In `atc/atccmd/command.go` (around line 3529), pass `db.NewAgentSettingsFactory(dbConn),` immediately after the existing `cmd.AgentDispatcherEnabled,` argument.

- [ ] In `atc/api/api_suite_test.go` (around line 352), pass `actionsapi.NewMemoryStore(),` immediately after the `false, // agent dispatcher boot default (flag off)` argument, importing `actionsapi "github.com/concourse/concourse/agent/api/actions"`.

- [ ] In `atc/wrappa/api_auth_wrappa.go`, add `atc.GetAgentActions,` to the authenticated block right beside `atc.GetAgentDispatcher` and `atc.SetAgentActions,` to the `CheckAdminHandler` block right beside `atc.SetAgentDispatcher`, with the comment:

```go
			// SetAgentActions engages/releases the cluster-wide external-effect
			// brake — same admin tier as SetAgentDispatcher. Reads are merely
			// authenticated (block above) so on-call can see the brake state.
```

- [ ] In `atc/wrappa/reject_archived_wrappa.go`, add `atc.GetAgentActions,` and `atc.SetAgentActions,` beside the dispatcher entries.
- [ ] In `atc/auditor/auditor.go`, add `atc.GetAgentActions,` and `atc.SetAgentActions,` to the `EnableSystemAuditLog` case beside the dispatcher entries.

- [ ] In `atc/wrappa/api_auth_wrappa_test.go`, extend the `Describe("dispatcher route tiers", …)` block: add `atc.GetAgentActions: delegate,` and `atc.SetAgentActions: delegate,` to the wrapped handler map, and add two sibling `Describe` blocks that are literal copies of the `GetAgentDispatcher`/`SetAgentDispatcher` ones with the route names swapped — proving RBAC parity (401 unauthenticated; GET admits any authenticated user; PUT 403s a non-admin and admits an admin).

- [ ] Run `go test ./agent/api/actions/... -count=1` — expect all pass.
- [ ] Run `ginkgo --focus='dispatcher route tiers' ./atc/wrappa/` — expect all pass.
- [ ] Run `go build ./... && ginkgo ./atc/api/` — expect the api suite green (rata panics on a route with no handler, so a missed registration fails loudly here).
- [ ] Commit `feat(atc): expose the action-suppression switch over the API`.

---

### Task 5: Add `fly agent actions suppress|resume|status`

**Files:**
- Create: `fly/commands/agent_actions.go`
- Create: `fly/commands/agent_actions_test.go`
- Create: `fly/integration/agent_actions_test.go`
- Modify: `fly/commands/agent.go`

There is fly-integration precedent for agent commands (`fly/integration/agent_dispatcher_test.go` drives the real binary against the ghttp mock ATC), so this task carries both a unit test and an integration spec.

- [ ] Write `fly/commands/agent_actions_test.go`:

```go
package commands

import "testing"

func TestActionsActionToMode(t *testing.T) {
	for _, test := range []struct {
		action string
		mode   string
		ok     bool
	}{
		{action: "suppress", mode: "suppressed", ok: true},
		{action: "resume", mode: "active", ok: true},
		{action: "active", mode: "active", ok: true},
		{action: "off", ok: false},
		{action: "pause", ok: false},
		{action: "", ok: false},
	} {
		mode, ok := actionsActionToMode(test.action)
		if ok != test.ok || mode != test.mode {
			t.Errorf("actionsActionToMode(%q) = (%q, %t), want (%q, %t)", test.action, mode, ok, test.mode, test.ok)
		}
	}
}
```

- [ ] Write `fly/integration/agent_actions_test.go`, cloning `fly/integration/agent_dispatcher_test.go` with four `Describe` blocks:
  - `status (no subcommand)` — GET `/api/v1/agent/actions` returning `{"mode":"active","source":"default","updated_at":null,"updated_by":null}`; asserts exit 0, `sess.Out` says `actions: active` then `source:\s+default`.
  - `suppress` — PUT with `VerifyJSONRepresenting(map[string]any{"mode": "suppressed"})`, responding `{"mode":"suppressed","source":"setting","updated_at":"2026-07-25T12:00:00Z","updated_by":"tdm"}`; asserts `sess.Out` says `actions: suppressed`.
  - `resume` — PUT `{"mode":"active"}`; asserts `actions: active`.
  - `server error` — PUT responding `403 admin only`; asserts non-zero exit and `sess.Err` says `admin only`.

- [ ] Run `ginkgo --focus='fly agent actions' ./fly/integration/` and confirm it fails with `Unknown command 'actions'`.

- [ ] Write `fly/commands/agent_actions.go`:

```go
package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/concourse/concourse/fly/commands/internal/displayhelpers"
)

// AgentActionsCommand is a leaf command (not a subcommand group, so the
// no-argument invocation runs Execute and prints status — go-flags would
// otherwise demand a subcommand), mirroring `fly agent dispatcher`. An
// optional positional ACTION flips the cluster-wide external-effect brake:
//
//	fly agent actions             → show current mode
//	fly agent actions status      → same, explicit
//	fly agent actions suppress    → mode=suppressed (no external side effects)
//	fly agent actions resume      → mode=active
//
// Suppression stops publisher writes ONLY. Dispatch, agent execution, and
// sealing keep running — that is what makes it a shadow mode.
type AgentActionsCommand struct {
	Args struct {
		Action string `positional-arg-name:"ACTION" description:"suppress | resume | status (omit to show status)"`
	} `positional-args:"yes"`
	Json bool `long:"json" description:"Print status as JSON"`
}

// actionsStatus mirrors agent/api/actions.Response.
type actionsStatus struct {
	Mode      string  `json:"mode"`
	Source    string  `json:"source"`
	UpdatedAt *string `json:"updated_at"`
	UpdatedBy *string `json:"updated_by"`
}

// actionsActionToMode maps the CLI verb to the wire mode. ok=false for an
// unknown verb; "status" is handled by the caller as a read, not a write.
func actionsActionToMode(action string) (string, bool) {
	switch action {
	case "suppress":
		return "suppressed", true
	case "resume", "active":
		return "active", true
	default:
		return "", false
	}
}

func (command *AgentActionsCommand) Execute([]string) error {
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}

	var status actionsStatus
	if command.Args.Action == "" || command.Args.Action == "status" {
		resp, err := agentAPIRequest(target, "GET", "/api/v1/agent/actions", nil)
		if err != nil {
			return err
		}
		if err := decodeOrError(resp, &status); err != nil {
			return err
		}
		return printActionsStatus(status, command.Json)
	}

	mode, ok := actionsActionToMode(command.Args.Action)
	if !ok {
		return fmt.Errorf("unknown action %q: want one of suppress, resume, status", command.Args.Action)
	}
	payload, err := json.Marshal(map[string]string{"mode": mode})
	if err != nil {
		return err
	}
	resp, err := agentAPIRequestWithType(target, "PUT", "/api/v1/agent/actions",
		"application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if err := decodeOrError(resp, &status); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "external actions set to %s\n", status.Mode)
	return printActionsStatus(status, command.Json)
}

func printActionsStatus(status actionsStatus, asJSON bool) error {
	if asJSON {
		return displayhelpers.JsonPrint(status)
	}
	updated := "never (never engaged)"
	if status.UpdatedAt != nil {
		by := ""
		if status.UpdatedBy != nil {
			by = " by " + *status.UpdatedBy
		}
		updated = *status.UpdatedAt + by
	}
	fmt.Printf("actions: %s\n", status.Mode)
	fmt.Printf("source:  %s\n", status.Source)
	fmt.Printf("last updated: %s\n", updated)
	if status.Mode == "suppressed" {
		fmt.Println("external side effects (publisher writes) are REFUSED; runs still execute and seal")
	}
	return nil
}
```

- [ ] In `fly/commands/agent.go`, add to `AgentCommand` immediately below the `Dispatcher` field:

```go
	Actions     AgentActionsCommand     `command:"actions" description:"Show or set the cluster-wide external-action switch (active|suppressed)"`
```

- [ ] Run `go test ./fly/commands/ -run TestActionsActionToMode -count=1` — expect pass.
- [ ] Run `ginkgo --focus='fly agent actions' ./fly/integration/` — expect all four specs green.
- [ ] Commit `feat(fly): add agent actions suppress|resume|status`.

---

### Task 6: Add the runner-side budget, turn, and wall-clock watchdog

**Files:**
- Create: `agent/runner/watchdog.go`
- Create: `agent/runner/watchdog_test.go`
- Modify: `agent/runner/runner.go`
- Modify: `agent/runner/export_test.go`

- [ ] Write `agent/runner/watchdog_test.go` first:

```go
package runner_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/runner"
	schema "github.com/concourse/concourse/agent/schema"
)

// writeRunawayClaude writes a stub CLI that leaks a background descendant,
// records its pid, emits the given stream-json lines, and then NEVER exits —
// the shape of a claude that ignores its own --max-budget-usd/--max-turns.
func writeRunawayClaude(t *testing.T, dir string, lines ...string) (claudePath, grandchildPidPath string) {
	t.Helper()
	claudePath = filepath.Join(dir, "claude")
	grandchildPidPath = filepath.Join(dir, "grandchild-pid")
	script := "#!/bin/sh\nsleep 60 >/dev/null 2>&1 &\necho $! > '" + grandchildPidPath + "'\n"
	for _, line := range lines {
		script += "echo '" + line + "'\n"
	}
	script += "sleep 60\n"
	if err := os.WriteFile(claudePath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return claudePath, grandchildPidPath
}

func readResults(t *testing.T, flightDir string) schema.Results {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(flightDir, "results.json"))
	if err != nil {
		t.Fatalf("read results.json: %v", err)
	}
	var results schema.Results
	if err := json.Unmarshal(raw, &results); err != nil {
		t.Fatalf("decode results.json (%s): %v", raw, err)
	}
	return results
}

// expectDeadWithin reuses the descendant-kill test's polling technique: poll
// kill(pid, 0) until it fails, proving the whole process GROUP went down.
func expectDeadWithin(t *testing.T, pidFile string, within time.Duration) {
	t.Helper()
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("stub never recorded its descendant pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parsing descendant pid %q: %v", raw, err)
	}
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	syscall.Kill(pid, syscall.SIGKILL) // don't leak it past the test
	t.Fatalf("claude's leaked descendant (pid %d) survived the watchdog kill", pid)
}

func runWatchdogCase(t *testing.T, cfg runner.Config, claude, flight string) (int, time.Duration, schema.Results) {
	t.Helper()
	cfg.ClaudePath = claude
	cfg.FlightDir = flight
	cfg.Stdout = new(bytes.Buffer)
	cfg.Stderr = new(bytes.Buffer)

	start := time.Now()
	exit, err := runner.Run(context.Background(), cfg)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return exit, elapsed, readResults(t, flight)
}

func TestWatchdogKillsARunawayThatBreachesItsBudget(t *testing.T) {
	restore := runner.SetClaudeWaitDelay(500 * time.Millisecond)
	defer restore()

	dir := t.TempDir()
	flight := filepath.Join(dir, "flight")
	if err := os.MkdirAll(flight, 0o755); err != nil {
		t.Fatal(err)
	}
	claude, grandchild := writeRunawayClaude(t, dir,
		`{"type":"assistant"}`,
		`{"type":"result","subtype":"progress","total_cost_usd":2.5}`)

	exit, elapsed, results := runWatchdogCase(t, runner.Config{
		Prompt: "do it", WorkDir: dir, StepName: "runaway", BudgetSliceUSD: 1.0,
	}, claude, flight)

	if exit != 2 {
		t.Fatalf("exit = %d, want 2 (platform error)", exit)
	}
	if elapsed > 30*time.Second {
		t.Fatalf("Run took %s — the never-exiting CLI was not killed", elapsed)
	}
	if results.Status != schema.StatusError {
		t.Fatalf("status = %q, want error", results.Status)
	}
	if got := results.Metadata["terminated_reason"]; got != "budget" {
		t.Fatalf("terminated_reason = %v, want \"budget\"", got)
	}
	if !strings.Contains(results.Summary, "budget cap") {
		t.Fatalf("summary = %q, want it to name the breached cap", results.Summary)
	}
	expectDeadWithin(t, grandchild, 5*time.Second)
}

func TestWatchdogKillsARunawayThatBreachesItsTurnCap(t *testing.T) {
	restore := runner.SetClaudeWaitDelay(500 * time.Millisecond)
	defer restore()

	dir := t.TempDir()
	flight := filepath.Join(dir, "flight")
	if err := os.MkdirAll(flight, 0o755); err != nil {
		t.Fatal(err)
	}
	claude, grandchild := writeRunawayClaude(t, dir,
		`{"type":"assistant"}`, `{"type":"assistant"}`, `{"type":"assistant"}`)

	exit, elapsed, results := runWatchdogCase(t, runner.Config{
		Prompt: "do it", WorkDir: dir, StepName: "runaway", MaxTurns: 2,
	}, claude, flight)

	if exit != 2 || elapsed > 30*time.Second {
		t.Fatalf("exit = %d after %s, want 2 promptly", exit, elapsed)
	}
	if got := results.Metadata["terminated_reason"]; got != "turns" {
		t.Fatalf("terminated_reason = %v, want \"turns\"", got)
	}
	expectDeadWithin(t, grandchild, 5*time.Second)
}

func TestWatchdogKillsARunawayThatOutlivesItsWallClock(t *testing.T) {
	restore := runner.SetClaudeWaitDelay(500 * time.Millisecond)
	defer restore()

	dir := t.TempDir()
	flight := filepath.Join(dir, "flight")
	if err := os.MkdirAll(flight, 0o755); err != nil {
		t.Fatal(err)
	}
	claude, grandchild := writeRunawayClaude(t, dir)

	exit, elapsed, results := runWatchdogCase(t, runner.Config{
		Prompt: "do it", WorkDir: dir, StepName: "runaway", MaxWallClock: 300 * time.Millisecond,
	}, claude, flight)

	if exit != 2 || elapsed > 30*time.Second {
		t.Fatalf("exit = %d after %s, want 2 promptly", exit, elapsed)
	}
	if got := results.Metadata["terminated_reason"]; got != "wall_clock" {
		t.Fatalf("terminated_reason = %v, want \"wall_clock\"", got)
	}
	expectDeadWithin(t, grandchild, 5*time.Second)
}

func TestWatchdogLeavesAnOrdinaryRunAlone(t *testing.T) {
	dir := t.TempDir()
	flight := filepath.Join(dir, "flight")
	if err := os.MkdirAll(flight, 0o755); err != nil {
		t.Fatal(err)
	}
	claude := writeStubClaude(t, dir, okEnvelope)

	exit, _, results := runWatchdogCase(t, runner.Config{
		Prompt: "do it", WorkDir: dir, StepName: "ordinary",
		MaxTurns: 50, BudgetSliceUSD: 100, MaxWallClock: time.Minute,
	}, claude, flight)

	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	if results.Status != schema.StatusPass {
		t.Fatalf("status = %q, want pass", results.Status)
	}
	if _, found := results.Metadata["terminated_reason"]; found {
		t.Fatalf("an unbreached run recorded a termination reason: %+v", results.Metadata)
	}
}

// Reported cost and turn counts are CUMULATIVE-to-date totals. Summing them
// would trip the budget arm at a fraction of the real spend, so the fold must
// be max(), and this test is what says so.
func TestWatchdogFoldsCumulativeTotalsInsteadOfSummingThem(t *testing.T) {
	reason, cost, turns, killed := runner.ObserveStreamLinesForTest(
		runner.Config{BudgetSliceUSD: 1.0},
		`{"type":"result","subtype":"progress","total_cost_usd":0.4}`,
		`{"type":"result","subtype":"progress","total_cost_usd":0.9}`,
	)
	if reason != "" || killed {
		t.Fatalf("cumulative 0.9 against a 1.0 cap tripped the watchdog (reason=%q, killed=%t)", reason, killed)
	}
	if cost != 0.9 {
		t.Fatalf("cost = %v, want 0.9", cost)
	}

	reason, cost, _, killed = runner.ObserveStreamLinesForTest(
		runner.Config{BudgetSliceUSD: 1.0},
		`{"type":"result","subtype":"progress","total_cost_usd":0.9}`,
		`{"type":"result","subtype":"progress","total_cost_usd":1.5}`,
	)
	if reason != "budget" || !killed {
		t.Fatalf("cumulative 1.5 against a 1.0 cap did not trip (reason=%q, killed=%t)", reason, killed)
	}
	if cost != 1.5 {
		t.Fatalf("cost = %v, want 1.5", cost)
	}

	reason, _, turns, _ = runner.ObserveStreamLinesForTest(
		runner.Config{MaxTurns: 2},
		`{"type":"assistant"}`, `{"type":"assistant"}`, `{"type":"assistant"}`,
	)
	if reason != "turns" || turns != 3 {
		t.Fatalf("turn fold = (%q, %d), want (\"turns\", 3)", reason, turns)
	}

	// Malformed and non-JSON lines must never panic or corrupt the totals.
	if reason, _, _, _ = runner.ObserveStreamLinesForTest(
		runner.Config{BudgetSliceUSD: 1.0}, `not json`, ``, `{"type":`,
	); reason != "" {
		t.Fatalf("garbage stream tripped the watchdog: %q", reason)
	}
}

func TestFromEnvReadsMaxWallClock(t *testing.T) {
	t.Setenv("AGENT_MAX_WALL_CLOCK", "90m")
	if got := runner.FromEnv().MaxWallClock; got != 90*time.Minute {
		t.Errorf("MaxWallClock = %s, want 90m", got)
	}
	t.Setenv("AGENT_MAX_WALL_CLOCK", "not-a-duration")
	if got := runner.FromEnv().MaxWallClock; got != 0 {
		t.Errorf("malformed MaxWallClock = %s, want 0 (absent)", got)
	}
}
```

- [ ] Run `go test ./agent/runner/ -run Watchdog -count=1` and confirm it fails to build with `undefined: runner.ObserveStreamLinesForTest` and `unknown field MaxWallClock in struct literal`.

- [ ] Write `agent/runner/watchdog.go`:

```go
package runner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Watchdog termination reasons, stamped into flight/results.json as
// metadata.terminated_reason so ingestion (and a human reading the row) can
// tell a platform-enforced cutoff from an agent failure.
const (
	TerminatedReasonBudget    = "budget"
	TerminatedReasonTurns     = "turns"
	TerminatedReasonWallClock = "wall_clock"
)

// maxStreamLineBytes bounds the partial-line buffer held while waiting for a
// newline. The CLI emits one JSON object per line; a stream that never
// produces a newline is malformed, so drop the partial rather than grow
// without bound.
const maxStreamLineBytes = 1 << 20

// watchdog is the PLATFORM-SIDE backstop for the caps handed to the claude CLI
// as --max-budget-usd / --max-turns, plus a wall-clock bound the CLI has no
// flag for. The CLI's own enforcement stays the first line; this exists
// because a CLI that ignores, mis-parses, or outlives its own flags must not
// be able to spend unbounded money or time.
//
// On the FIRST breach it kills claude through the same cancel path the step
// timeout and build abort already use (cmd.Cancel -> group SIGKILL), so leaked
// tool subprocesses die with it. The runner's own context is untouched, so the
// flight recorder is still written after a kill.
type watchdog struct {
	maxCostUSD float64
	maxTurns   int
	maxWall    time.Duration
	kill       func()

	mu        sync.Mutex
	cost      float64
	turns     int
	assistant int
	reason    string
	detail    string
}

func newWatchdog(cfg Config, kill func()) *watchdog {
	return &watchdog{
		maxCostUSD: cfg.BudgetSliceUSD,
		maxTurns:   cfg.MaxTurns,
		maxWall:    cfg.MaxWallClock,
		kill:       kill,
	}
}

// armed reports whether any cap is configured. An unarmed watchdog is never
// attached to the stream, so an unconfigured step pays nothing.
func (w *watchdog) armed() bool {
	return w.maxCostUSD > 0 || w.maxTurns > 0 || w.maxWall > 0
}

// streamEvent is the minimal projection of a stream-json line the watchdog
// needs. Unknown fields are ignored on purpose: this must keep working when
// the CLI adds fields.
type streamEvent struct {
	Type         string  `json:"type"`
	CostUSD      float64 `json:"cost_usd"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	NumTurns     int     `json:"num_turns"`
}

// observe folds one stream-json line into the running totals and trips the
// watchdog on a breach.
//
// cost_usd / total_cost_usd / num_turns reported by the CLI are
// CUMULATIVE-to-date totals, so they are folded with max(), never summed —
// summing a progress stream would trip the budget arm at a fraction of the
// real spend. Turns are additionally floored by the number of assistant
// messages seen, which is the platform's own view of a turn and does not wait
// for the CLI to report num_turns in the final envelope.
func (w *watchdog) observe(line []byte) {
	var event streamEvent
	if json.Unmarshal(line, &event) != nil {
		return
	}

	w.mu.Lock()
	if event.TotalCostUSD > w.cost {
		w.cost = event.TotalCostUSD
	}
	if event.CostUSD > w.cost {
		w.cost = event.CostUSD
	}
	if event.NumTurns > w.turns {
		w.turns = event.NumTurns
	}
	if event.Type == "assistant" {
		w.assistant++
	}
	cost := w.cost
	turns := max(w.turns, w.assistant)
	w.mu.Unlock()

	if w.maxCostUSD > 0 && cost > w.maxCostUSD {
		w.trip(TerminatedReasonBudget, fmt.Sprintf(
			"cumulative cost $%.4f exceeded the $%.4f budget cap", cost, w.maxCostUSD))
		return
	}
	if w.maxTurns > 0 && turns > w.maxTurns {
		w.trip(TerminatedReasonTurns, fmt.Sprintf(
			"%d turns exceeded the %d-turn cap", turns, w.maxTurns))
	}
}

// watchWallClock trips the wall-clock arm when the run outlives maxWall. It
// returns as soon as done is closed (claude exited) so it never outlives the
// step.
func (w *watchdog) watchWallClock(start time.Time, done <-chan struct{}) {
	if w.maxWall <= 0 {
		return
	}
	timer := time.NewTimer(w.maxWall - time.Since(start))
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		w.trip(TerminatedReasonWallClock, fmt.Sprintf(
			"wall clock exceeded the %s bound", w.maxWall))
	}
}

// trip records the FIRST breach and kills claude's process group. Later
// breaches are ignored so results.json names the reason that actually ended
// the run.
func (w *watchdog) trip(reason, detail string) {
	w.mu.Lock()
	if w.reason != "" {
		w.mu.Unlock()
		return
	}
	w.reason, w.detail = reason, detail
	w.mu.Unlock()
	w.kill()
}

// terminated returns the winning breach, or ("", "") when the watchdog never
// fired.
func (w *watchdog) terminated() (reason string, detail string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.reason, w.detail
}

// observed returns the cumulative cost and turn count the watchdog saw. A
// killed run has no final envelope, so these are the only cost/turn numbers
// the flight recorder can report — without them a step stopped at $50 ingests
// as free.
func (w *watchdog) observed() (cost float64, turns int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cost, max(w.turns, w.assistant)
}

// streamLineWriter splits the claude stdout byte stream into complete NDJSON
// lines and hands each to observe. It rides the existing stdout MultiWriter,
// so the same bytes still reach the transcript buffer and the step log.
type streamLineWriter struct {
	buf     bytes.Buffer
	observe func([]byte)
}

func (writer *streamLineWriter) Write(p []byte) (int, error) {
	writer.buf.Write(p)
	for {
		pending := writer.buf.Bytes()
		index := bytes.IndexByte(pending, '\n')
		if index < 0 {
			break
		}
		line := make([]byte, index)
		copy(line, pending[:index])
		writer.buf.Next(index + 1)
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			writer.observe(trimmed)
		}
	}
	if writer.buf.Len() > maxStreamLineBytes {
		writer.buf.Reset()
	}
	return len(p), nil
}
```

- [ ] In `agent/runner/runner.go`, add the config field beneath `MaxTurns int`:

```go
	// MaxWallClock is the in-pod watchdog's wall-clock bound (env
	// AGENT_MAX_WALL_CLOCK, a Go duration string). The web side derives it
	// from the agent step's effective timeout so the runner self-terminates
	// and writes its flight recorder BEFORE the step deadline kills the pod.
	// Zero means no wall-clock bound.
	MaxWallClock time.Duration
```

- [ ] In `FromEnv`, immediately after the `AGENT_MAX_TURNS` block, add:

```go
	if v := os.Getenv("AGENT_MAX_WALL_CLOCK"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.MaxWallClock = d
		}
	}
```

- [ ] In `Run`, replace the command construction and stdout wiring. After `args = append(args, "--mcp-config", mcpConfigPath, "--strict-mcp-config")` and before `var buf bytes.Buffer`, insert:

```go
	// Platform-side containment backstop: the same caps handed to the CLI
	// above, plus AGENT_MAX_WALL_CLOCK, enforced HERE by killing claude's
	// process group. runCtx derives from ctx, so an ordinary step timeout or
	// build abort still tears the group down; cancelling runCtx does NOT
	// cancel the runner, so the flight recorder is still written after a kill.
	runCtx, killClaude := context.WithCancel(ctx)
	defer killClaude()
	dog := newWatchdog(cfg, killClaude)
```

Change `cmd := exec.CommandContext(ctx, claudePath, args...)` to `cmd := exec.CommandContext(runCtx, claudePath, args...)`, and replace `cmd.Stdout = io.MultiWriter(&buf, stdout)` with:

```go
	stdoutWriters := []io.Writer{&buf, stdout}
	if dog.armed() {
		stdoutWriters = append(stdoutWriters, &streamLineWriter{observe: dog.observe})
	}
	cmd.Stdout = io.MultiWriter(stdoutWriters...)
```

- [ ] Wrap the run itself so the wall-clock goroutine cannot outlive it. Replace `runErr := cmd.Run()` with:

```go
	claudeDone := make(chan struct{})
	if dog.armed() {
		go dog.watchWallClock(time.Now(), claudeDone)
	}
	runErr := cmd.Run()
	close(claudeDone)
```

- [ ] Immediately after `env, parseErr := parseEnvelope(buf.Bytes())`, insert the termination fold and change the cost record to use it:

```go
	terminatedReason, terminatedDetail := dog.terminated()
	costUSD, turns := env.ResolvedCostUSD(), env.NumTurns
	if terminatedReason != "" {
		// A killed run has no final envelope; report what the watchdog
		// actually saw so a runaway that was stopped is not ingested as free.
		observedCost, observedTurns := dog.observed()
		if observedCost > costUSD {
			costUSD = observedCost
		}
		if observedTurns > turns {
			turns = observedTurns
		}
		writeEvent(events, schema.EventError, map[string]string{
			"message": "agent-runner watchdog terminated the step (" + terminatedReason + "): " + terminatedDetail,
		})
	}

	writeEvent(events, schema.EventCostRecord, schema.CostRecordData{
		Source:              "agent_step",
		Provider:            "anthropic",
		Model:               env.Model,
		InputTokens:         env.Usage.InputTokens,
		OutputTokens:        env.Usage.OutputTokens,
		CacheReadTokens:     env.Usage.CacheReadInputTokens,
		CacheCreationTokens: env.Usage.CacheCreationInputTokens,
		Turns:               turns,
		CostUSD:             costUSD,
	})
```

(deleting the original `writeEvent(events, schema.EventCostRecord, …)` block it replaces).

- [ ] Add `|| terminatedReason != ""` to the status condition:

```go
	status := schema.StatusPass
	if runErr != nil || parseErr != nil || env.IsError || terminatedReason != "" {
		status = schema.StatusError
	}
```

- [ ] Override the summary and stamp the reason. Replace the `summary = truncate(summary, maxSummaryChars)` line with:

```go
	if terminatedReason != "" {
		// The kill is the authoritative outcome; a partial CLI result (or the
		// bare "signal: killed" from cmd.Run) must not hide WHY the step ended.
		summary = fmt.Sprintf("agent-runner terminated the step (%s): %s", terminatedReason, terminatedDetail)
	}
	summary = truncate(summary, maxSummaryChars)
```

and, immediately after the `results := schema.Results{…}` literal, add:

```go
	if terminatedReason != "" {
		results.Metadata = map[string]any{"terminated_reason": terminatedReason}
	}
```

- [ ] Change the final `writeEvent(events, schema.EventStepEnd, …)` to use `CostUSD: costUSD` and `Turns: turns` instead of `env.ResolvedCostUSD()` / `env.NumTurns`.

- [ ] Add the test seam to `agent/runner/export_test.go`:

```go
// ObserveStreamLinesForTest folds the given stream-json lines through a
// watchdog armed by cfg and reports the winning breach reason (empty when
// none), the cumulative totals it saw, and whether it fired the kill.
func ObserveStreamLinesForTest(cfg Config, lines ...string) (reason string, cost float64, turns int, killed bool) {
	dog := newWatchdog(cfg, func() { killed = true })
	writer := &streamLineWriter{observe: dog.observe}
	for _, line := range lines {
		_, _ = writer.Write([]byte(line + "\n"))
	}
	reason, _ = dog.terminated()
	cost, turns = dog.observed()
	return reason, cost, turns, killed
}
```

- [ ] Run `go test ./agent/runner/ -count=1` and confirm every test passes, including the pre-existing `TestCancellationKillsClaudesDescendants` (`runCtx` derives from `ctx`, so caller cancellation still reaches the group).
- [ ] Run `go test -race -count=1 ./agent/runner/` and confirm clean — the stream writer runs on the `os/exec` copy goroutine while the wall-clock arm runs on its own.
- [ ] Run `gofmt -l agent/runner` and confirm empty output.
- [ ] Commit `feat(agent): terminate runs that breach budget, turn, or wall-clock caps`.

---

### Task 7: Bound agent steps with a default wall-clock timeout

**Files:**
- Modify: `atc/exec/maybe_timeout.go`
- Modify: `atc/exec/agent_step.go`
- Modify: `atc/exec/agent_step_test.go`
- Modify: `atc/engine/step_factory.go`
- Modify: `atc/atccmd/command.go`
- Create: `atc/atccmd/agent_step_timeout_command_test.go`

- [ ] Write the failing flag test `atc/atccmd/agent_step_timeout_command_test.go`, mirroring `TestAgentPublisherGatewayFlagDefaults`:

```go
package atccmd_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/atc/atccmd"
	"github.com/jessevdk/go-flags"
)

func TestAgentStepDefaultTimeoutFlagDefault(t *testing.T) {
	command := &atccmd.ATCCommand{}
	parser := flags.NewParser(command, flags.Default)
	parser.NamespaceDelimiter = "-"
	run := parser.Find("run")

	option := run.FindOptionByLongName("agent-step-default-timeout")
	if option == nil {
		t.Fatal("--agent-step-default-timeout is missing")
	}
	if got := strings.Join(option.Default, ","); got != "2h" {
		t.Fatalf("--agent-step-default-timeout default = %q, want %q", got, "2h")
	}
}
```

- [ ] Add the failing exec specs to `atc/exec/agent_step_test.go`. Add `"time"` to the import block (the file does not import it yet). In the top `var (…)` block add `agentDefaultTimeout time.Duration`; in the `BeforeEach` that builds `agentPlan`, add `agentDefaultTimeout = 0`; and in the `JustBeforeEach` replace the literal `0,` argument to `exec.NewAgentStep` with `agentDefaultTimeout,` (this is the only `0,` between `fakeDelegateFactory,` and `agentImage,` — the other `exec.NewAgentStep` call sites in the file keep their literal `0`). Then add:

```go
	Context("when a platform default timeout is configured and the step declares none", func() {
		BeforeEach(func() {
			agentDefaultTimeout = time.Millisecond
			chosenContainer.ProcessDefs[0].Stub.Do = func(ctx context.Context, _ *runtimetest.Process) error {
				select {
				case <-ctx.Done():
					return fmt.Errorf("wrapped: %w", ctx.Err())
				case <-time.After(100 * time.Millisecond):
					return nil
				}
			}
		})

		It("fails without error and surfaces the timeout to the operator", func() {
			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeFalse())

			Expect(fakeDelegate.ErroredCallCount()).To(Equal(1))
			_, status := fakeDelegate.ErroredArgsForCall(0)
			Expect(status).To(Equal(exec.TimeoutLogMessage))
		})
	})

	Context("when an explicit per-step timeout is set", func() {
		BeforeEach(func() {
			agentDefaultTimeout = 100 * time.Minute
			agentPlan.Timeout = "20m"
		})

		// The pod-side watchdog bound is DERIVED from the step's effective
		// timeout (90% of it), so the runner always self-terminates and writes
		// its flight recorder inside the web-side deadline. An authored
		// timeout: still wins over the platform default.
		It("derives AGENT_MAX_WALL_CLOCK from the authored timeout", func() {
			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())

			_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
			Expect(spec.Env).To(ContainElement("AGENT_MAX_WALL_CLOCK=18m0s"))
		})
	})

	Context("when neither a per-step nor a platform timeout is set", func() {
		It("exports no AGENT_MAX_WALL_CLOCK row", func() {
			ok, err := step.Run(ctx, state)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())

			_, _, spec, _ := fakePool.FindOrSelectWorkerArgsForCall(0)
			Expect(spec.Env).ToNot(ContainElement(HavePrefix("AGENT_MAX_WALL_CLOCK=")))
		})
	})
```

- [ ] Run `go test ./atc/atccmd/ -run AgentStepDefaultTimeout -count=1` and confirm `--agent-step-default-timeout is missing`. Run `ginkgo --focus='AgentStep' ./atc/exec/` and confirm the three new specs fail.

- [ ] In `atc/exec/maybe_timeout.go`, extract the parser without changing `MaybeTimeout`'s observable behavior:

```go
// ResolveTimeout returns the effective timeout for a step: an authored
// `timeout:` always wins; otherwise the platform default applies.
func ResolveTimeout(timeoutStr string, defaultTimeout time.Duration) (time.Duration, error) {
	if timeoutStr == "" {
		return defaultTimeout, nil
	}
	timeout, err := time.ParseDuration(timeoutStr)
	if err != nil {
		return 0, fmt.Errorf("parse timeout: %w", err)
	}
	return timeout, nil
}

func MaybeTimeout(ctx context.Context, timeoutStr string, defaultTimeout time.Duration) (context.Context, func(), error) {
	if timeoutStr == "" && defaultTimeout == 0 {
		return ctx, func() {}, nil
	}

	timeout, err := ResolveTimeout(timeoutStr, defaultTimeout)
	if err != nil {
		return nil, nil, err
	}

	processCtx, cancel := context.WithTimeout(ctx, timeout)
	return processCtx, cancel, nil
}
```

- [ ] In `atc/exec/agent_step.go`, immediately after the `AGENT_MAX_TURNS` env block, add:

```go
	// AGENT_MAX_WALL_CLOCK is the in-pod watchdog bound. It is DERIVED from
	// the step's effective timeout (90% of it) rather than configured
	// separately, so the runner always self-terminates and writes its flight
	// recorder BEFORE the web-side deadline kills the pod — the difference
	// between an operator-readable wall_clock row and the zero-cost,
	// no-step.end error row a hard kill produces. A parse error is ignored
	// here; MaybeTimeout reports it authoritatively below.
	if effective, err := ResolveTimeout(step.plan.Timeout, step.defaultTimeout); err == nil && effective > 0 {
		env = append(env, "AGENT_MAX_WALL_CLOCK="+(effective-effective/10).String())
	}
```

- [ ] In `atc/engine/step_factory.go`, add the field `agentStepDefaultTimeout time.Duration` to `coreStepFactory` (next to `defaultTaskTimeout`), add the option:

```go
// WithAgentStepDefaultTimeout sets the platform wall-clock default applied to
// agent: steps that declare no timeout:. Zero leaves agent steps unbounded.
// Agent steps deliberately do NOT inherit --default-task-timeout: an agent's
// runaway profile is unrelated to a task's, and one knob must own it.
func WithAgentStepDefaultTimeout(timeout time.Duration) CoreStepFactoryOption {
	return func(f *coreStepFactory) { f.agentStepDefaultTimeout = timeout }
}
```

and change the `factory.defaultTaskTimeout,` argument inside the `exec.NewAgentStep(` call (the AgentStep one, ~line 329 — *not* the `exec.NewTaskStep(` call ~line 371) to `factory.agentStepDefaultTimeout,`.

- [ ] In `atc/atccmd/command.go`, add the flag immediately below `AgentStepImage`:

```go
	AgentStepDefaultTimeout time.Duration `long:"agent-step-default-timeout" default:"2h" description:"Default wall-clock bound for agent: steps that declare no timeout:. An explicit per-step timeout: always wins; 0 disables the default and leaves agent steps unbounded. Agent steps do not inherit --default-task-timeout."`
```

and append `engine.WithAgentStepDefaultTimeout(cmd.AgentStepDefaultTimeout),` to the `coreStepFactoryOptions` slice (beside `engine.WithAgentBudgetChecker(...)`).

- [ ] Run `go test ./atc/atccmd/ -run AgentStepDefaultTimeout -count=1` — expect pass.
- [ ] Run `ginkgo --focus='AgentStep' ./atc/exec/` — expect all specs pass, including the pre-existing ones.
- [ ] Run `ginkgo ./atc/engine/ && go build ./...` — expect green.
- [ ] Commit `feat(atc): bound agent steps with a default wall-clock timeout`.

---

### Task 8: Pin the CLI cap plumbing and give every seed an explicit turn cap

**Files:**
- Modify: `agent/runner/runner_test.go`
- Modify: `agent/workflow/seed_test.go`
- Modify: `agent/workflow/seeds/anonymization-audit-v3/workflow.yml`
- Modify: `agent/workflow/seeds/code-review-v3/workflow.yml`
- Modify: `agent/workflow/seeds/log-diagnosis-v3/workflow.yml`
- Modify: `agent/workflow/seeds/small-fix-v3/workflow.yml`
- Modify: `agent/workflow/seeds/version-upgrade-v3/workflow.yml`

- [ ] Add the argv-pinning test to `agent/runner/runner_test.go` (next to `TestRunHardCapsClaudeAtTheAuthoredBudgetSlice`). `--max-budget-usd` already has a guard; `--max-turns` has none, so deleting `runner.go`'s `--max-turns` lines currently keeps the suite green:

```go
// Both caps must appear LITERALLY in the constructed argv. Without this,
// deleting the --max-turns append from runner.go leaves the whole suite green
// while every agent step silently loses its turn cap.
func TestRunPassesBothAuthoredCapsToTheCLI(t *testing.T) {
	dir := t.TempDir()
	flight := filepath.Join(dir, "flight")
	if err := os.MkdirAll(flight, 0o755); err != nil {
		t.Fatal(err)
	}
	claude, argsPath := writeRecordingStubClaude(t, dir, okEnvelope)

	exit, err := runner.Run(context.Background(), runner.Config{
		Prompt: "do it", FlightDir: flight, WorkDir: dir, StepName: "capped",
		ClaudePath: claude, MaxTurns: 12, BudgetSliceUSD: 3.5,
		Stdout: new(bytes.Buffer), Stderr: new(bytes.Buffer),
	})
	if err != nil || exit != 0 {
		t.Fatalf("Run() exit = %d, err = %v", exit, err)
	}

	raw, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")

	for _, want := range [][2]string{{"--max-turns", "12"}, {"--max-budget-usd", "3.5"}} {
		found := false
		for index := 0; index+1 < len(args); index++ {
			if args[index] == want[0] && args[index+1] == want[1] {
				found = true
			}
		}
		if !found {
			t.Fatalf("claude args = %q, want %s %s", args, want[0], want[1])
		}
	}
}
```

- [ ] Run `go test ./agent/runner/ -run TestRunPassesBothAuthoredCaps -count=1` and confirm it passes. Then temporarily delete the `if cfg.MaxTurns > 0 { args = append(args, "--max-turns", …) }` block in `runner.go`, re-run, and confirm the expected failure `claude args = […], want --max-turns 12`. Restore the block and re-run green. This is the whole point of the test — verify it actually fails.

- [ ] Add the seed-level guard to `agent/workflow/seed_test.go`. In `TestVersionThreeEngineeringSeedsCompileAndRender`, replace the recursor's `OnAgent` line with:

```go
				// Every shipped agent step carries an explicit turn cap. The
				// CLI's default is not a platform bound, and an uncapped agent
				// is the runaway-overnight case WS3 exists to close.
				OnAgent: func(step *atc.AgentStep) error {
					if step.MaxTurns <= 0 {
						return fmt.Errorf("agent step %q ships without a max_turns cap", step.Name)
					}
					return nil
				},
```

- [ ] Run `go test ./agent/workflow/ -run TestVersionThreeEngineeringSeedsCompileAndRender -count=1` and confirm failures naming the uncapped steps, e.g. `inspect rendered plan: agent step "review" ships without a max_turns cap`.

- [ ] Add `max_turns:` to every agent step in the five seeds that have them (`merge-delivery-v3` has none — its plan is two `task:` steps, one `await_snapshot:`, and one `publish_snapshot:`). Insert the line immediately **after** each `budget_slice_usd:` line, at the same indentation (four spaces).

  Sizing rule: **20 turns per USD of the step's authored budget slice**, which is a generous ceiling no healthy run of these workflows approaches — today these steps run with no `--max-turns` at all, so the cap must be a backstop, not a behavior change. It stays proportional to the work each step was funded for, and the runner's `--max-budget-usd` remains the tighter, primary bound.

  | Seed | Agent step | `budget_slice_usd` | `max_turns` |
  |---|---|---|---|
  | `anonymization-audit-v3` | `audit` | 5 | 100 |
  | `code-review-v3` | `review` | 5 | 100 |
  | `log-diagnosis-v3` | `diagnose` | 5 | 100 |
  | `small-fix-v3` | `implement-and-test` | 10 | 200 |
  | `small-fix-v3` | `validate` | 2 | 40 |
  | `small-fix-v3` | `review` | 3 | 60 |
  | `small-fix-v3` | `enforce-approval` | 1 | 20 |
  | `version-upgrade-v3` | `upgrade` | 10 | 200 |
  | `version-upgrade-v3` | `review` | 3 | 60 |
  | `version-upgrade-v3` | `validate-and-ask` | 2 | 40 |
  | `version-upgrade-v3` | `enforce-approval` | 1 | 20 |

  For example, in `agent/workflow/seeds/code-review-v3/workflow.yml`:

```yaml
  - agent: review
    budget_slice_usd: 5
    max_turns: 100
    function_id: review
```

- [ ] Run `go test ./agent/workflow/ -count=1` and confirm every seed spec passes (`max_turns` is an existing `atc.AgentStep` field, so no parser change is needed; `atc/step_validator.go` already rejects a negative value).
- [ ] Run `go test ./agent/... -count=1` and `go build ./...` — expect green.
- [ ] Commit `test(agent): pin the CLI cap plumbing and cap every shipped seed`.

---

## Self-review against the WS3 acceptance criteria

| Design acceptance bullet | Where it lands |
|---|---|
| “`fly agent actions suppress` makes a publish step fail with the switch named and leaves the operation row pending” | Task 1 (durable mode), Task 2 (`ErrActionsSuppressed` names the switch and the resume command; `TestGitServiceMakesNoExternalCallWhileActionsAreSuppressed` asserts `StatusPending` and zero provider calls; work-item twin), Task 3 (the production executor is composed with the reader, and refuses to compose without one), Task 5 (the `fly` verb) |
| “resume + retry publishes exactly once (idempotency preserved across suppression)” | Task 2 — `TestGitServicePublishesExactlyOnceAfterActionsResume`: same `OperationKey`, `attempt == 2`, `len(backend.operations) == 1` |
| “A never-exiting fake CLI is killed at the configured wall-clock/budget with the reason recorded” | Task 6 — `TestWatchdogKillsARunawayThatBreachesItsBudget` / `…TurnCap` / `…OutlivesItsWallClock`, each asserting `metadata.terminated_reason` and proving the group kill by polling the leaked descendant’s pid |
| “Removing `--max-turns` from the runner makes a test fail” | Task 8 — `TestRunPassesBothAuthoredCapsToTheCLI`, with an explicit delete-the-line-and-watch-it-fail verification step |
| Web-side default wall-clock bound with `TimeoutLogMessage` surfaced (design item 3) | Task 7 — `--agent-step-default-timeout` (default `2h`, `0` disables), applied at the `MaybeTimeout` call site, with a spec asserting `fakeDelegate.Errored` receives `exec.TimeoutLogMessage`; per-step `timeout:` still wins |
| `AGENT_MAX_WALL_CLOCK` plumbed from the web side with a platform default (design item 2) | Task 6 (runner reads it) + Task 7 (web derives it as 90% of the step’s effective timeout and exports it) |
| Suppression must not gate dispatch/execution/sealing | Enforcement exists only in `agent/publisher/{git,workitem}.go` (Task 2). No dispatch, exec, or sealer path is touched; `agent/dispatch` is untouched by this plan |

**Deviations from the design, and why:**

1. **`AGENT_MAX_WALL_CLOCK` is derived, not a separate flag.** The design says “a new `AGENT_MAX_WALL_CLOCK` with a platform default” without naming a knob. A second independent duration flag would let an operator configure a runner bound *longer* than the step deadline, which reintroduces exactly the hard-kill/no-`step.end` failure the flight recorder already fights. Deriving it as 90% of the step’s effective timeout keeps one knob and guarantees the runner writes `results.json` before the pod dies.
2. **Stream events are not “already parsed”.** The runner buffers all of claude’s stdout and parses only the last line (`parseEnvelope`); there is no incremental event parse to consume. Task 6 therefore adds a minimal `streamLineWriter` on the existing stdout `MultiWriter` — no change to the transcript or envelope paths.
3. **`dispatcher_mode` becomes nullable (Task 1).** `agent_settings.dispatcher_mode` is `NOT NULL` with no default, so an INSERT that sets only `actions_mode` would have to invent a dispatcher mode — and *any* value makes `GetDispatcherSetting` report `found=true`, silently disabling the `--agent-dispatcher-enabled` boot fallback on a live cluster. Dropping `NOT NULL` and mapping NULL to `found=false` keeps the dispatcher’s semantics byte-identical while letting the switch own its own row creation. The switch also gets its own `actions_updated_at/by` provenance for the same reason.
4. **WS3-closing review correction (Task 8): "20 turns per USD is a generous ceiling" was contradicted by observed production spend, so every seed's `max_turns` was re-based to ~40/USD.** This plan's sizing rule assumed 20 turns/USD sat comfortably above real usage. Two independent, real (non-seed) data points said otherwise: `deploy/concourse-pipeline.yml`'s hand-authored review task (a separate, standalone CI pipeline for this repo itself, not one of the five workflow seeds and not budget-metered — it only carries `max_turns` plus a 30m step timeout) needed 141 turns for a successful run after a prior 50-turn guess had already hit `error_max_turns`; its own `max_turns` was independently bumped to 200 after that incident, which is corroborating evidence that turn counts in this range are real, not a per-USD data point. The per-USD number comes from `ci/dogfood/FINDINGS.md`, which records a 100-turn seed-driven ticket cap tripping at $5.98 (≈16.7 turns/USD) and, after that cap was raised to 250, a "successful" run finishing at 48 turns/$2.15 (≈22.3 turns/USD). Both are within a couple of turns/USD of the plan's original 20/USD line, not safely under it. A cap sitting ON the observed spend rate isn't a backstop; it's a second limit that binds at roughly the same time as the budget, which is exactly the ambiguity WS3 was supposed to remove. Every `max_turns` across the five capped seeds (`agent/workflow/seeds/{anonymization-audit,code-review,log-diagnosis,small-fix,version-upgrade}-v3/workflow.yml`) was doubled to ~40 turns per authored `budget_slice_usd` (2x the observed 17-22/USD), so `--max-budget-usd` stays the tighter, primary bound and `--max-turns` is purely a runaway guard. Deliberately unchanged: what a cap-stop *means* for step/ticket status. `error_max_turns` can still surface as a clean or `needs_review` outcome today (see the empty-branch incident in `ci/dogfood/FINDINGS.md`) — that is a separate product decision, not a test-hardening fix, and is tracked as its own follow-up ("Decide what a budget/turn cap-stop means for step status").
