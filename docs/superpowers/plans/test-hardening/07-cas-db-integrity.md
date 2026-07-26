# WS7 — CAS and Durable-State Integrity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every compare-and-swap and durable-state invariant on the agentic spine executable as a test: the lost-CAS branches of workflow-run admission, ticket `Transition` under real contention, the immutability of a dispatched run's bound inputs, the input side of the seal/GC race, the foreign-key topology that keeps snapshot deletion from cascading, the publication outcome index-repair path, and the unbounded growth of expired retention claims.

**Architecture:** Six of the seven items are tests over existing code. Two are small production changes: an input-digest lock inside `CommitSealBatch` (only if the race test proves it is needed — see Task 5) and a retention-claim reaper on the `snapshot.MetadataStore` contract driven from the `Lifecycle.Collect` sweep. Everything else is `agent/workflowrun` scripted-store tests (plain `testing`) and `atc/db` / `atc/db/migration` Ginkgo specs against real PostgreSQL.

**Tech Stack:** Go, plain `testing` in `agent/`, Ginkgo/Gomega in `atc/`, PostgreSQL advisory locks and `pg_constraint` catalog queries, counterfeiter fakes.

## Global Constraints

- **Cross-plan CI contract.** Every `atc/db` and `atc/db/migration` spec in this plan runs in the `db-tests` job introduced by [`01-ci-execution.md`](01-ci-execution.md). Until that job exists these suites run only locally. Locally, PostgreSQL must be up: `pg_isready` before any DB task (see `CLAUDE.md`).
- Do **not** run `atc/db` suites with `--race`; the repo-wide ban (ginkgo `-p` parallel compilation failures) applies. `agent/...` packages are plain `testing` and may be run under the WS1 race lane.
- Do not reduce any existing `Eventually` timeout. New barrier assertions use the 5s/10s timeouts the surrounding specs already use.
- Test conventions hold: plain `testing` with scripted stubs in `agent/workflowrun`, Ginkgo `Describe`/`It` with `dbConn`/`defaultTeam` from `atc/db/db_suite_test.go` in `atc/db`, `migrator.Migrate(nil, nil, version)` in `atc/db/migration`.
- No new third-party dependencies.
- Tasks are independently landable in any order, with one exception: **Task 9 requires Task 8** (it consumes the store method Task 8 adds).
- `agent/schema` is a separate module and is untouched by this plan.

---

### Task 1: Pin both lost-CAS admission branches in the workflow-run binder

`agent/workflowrun/binder.go:429-432` (advance lost the CAS) and `:459-468` (failure-marking lost the CAS) have no test. Report C: "binder.go:429-432,:459-468 lost-CAS branches NO test; 0 goroutines in 6677 lines." The correct behavior, read off the surrounding code:

- `advanceAdmission` with `transitioned == false` and the row still `admitting` with a complete execution means **another writer owns the CAS**: return `ErrPlatformFailure` (retryable), never `ErrCorruptPartialAdmission`, and never re-create the execution. The next call must resume the same durable identity.
- `advanceAdmission` with `transitioned == true` and the row still reading `admitting` is genuinely impossible: `ErrCorruptPartialAdmission`.
- `failAllocated` with `transitioned == false` and a durable winner found means our failure-marking lost to a concurrent writer: **defer to the winner** — return `BindResult{Run: winner, Created: created}` with a `nil` error, discarding our own cause.
- `failAllocated` with `transitioned == false` and no winner returns the original cause.

**Files:**
- Modify: `agent/workflowrun/binder_test.go`

- [ ] Add `TestBindAndCreateLostAdmissionCASStaysRetryableAndResumes` to `agent/workflowrun/binder_test.go`, driving `advanceAdmission` through `handleExisting` (admitting + complete execution) and returning `false` from the transition stub:

```go
func TestBindAndCreateLostAdmissionCASStaysRetryableAndResumes(t *testing.T) {
	definition := binderTestDefinition()
	rendered := binderTestRendered(t, definition)
	admitting := db.AgentWorkflowRun{
		ID: 41, TeamID: 7, TeamName: "research", WorkflowDefinitionID: definition.ID,
		WorkflowName: definition.Name, WorkflowVersion: definition.Version,
		SchemaVersion: 3, SignatureVersion: 1, DefinitionContentHash: definition.ContentHash,
		IdempotencyKey: "lost-advance-cas", ParameterizedConfig: mustCanonical(t, rendered.Config),
		ParameterizedConfigHash: rendered.TargetConfigHash, OriginKind: "manual", CreatedBy: "alice",
		Status: db.AgentWorkflowRunStatusAdmitting,
	}
	pipelineRunID, templateID, instanceID := 73, 2, 3
	plannedBuildID := int64(5)
	concreteHash := strings.Repeat("c", 64)
	admitting.PipelineRunID, admitting.TemplatePipelineID, admitting.InstancePipelineID =
		&pipelineRunID, &templateID, &instanceID
	admitting.ConcreteConfig, admitting.ConcreteConfigHash = mustCanonical(t, rendered.Config), &concreteHash
	admitting.PlannedBuildID = &plannedBuildID
	running := admitting
	running.Status = db.AgentWorkflowRunStatusRunning

	// The concurrent winner only becomes visible after it has advanced the row.
	winnerAdvanced := false
	transitions := 0
	store := &storeStub{
		find: func(context.Context, int, string) (db.AgentWorkflowRun, bool, error) {
			if winnerAdvanced {
				return running, true, nil
			}
			return admitting, true, nil
		},
		snapshots: func(context.Context, snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error) {
			return nil, nil
		},
		transition: func(_ context.Context, id snapshot.WorkflowRunID, from, to db.AgentWorkflowRunStatus, message string) (bool, error) {
			transitions++
			if id != admitting.ID || from != db.AgentWorkflowRunStatusAdmitting ||
				to != db.AgentWorkflowRunStatusRunning || message != "" {
				t.Fatalf("transition = (%s, %s, %s, %q)", id.String(), from, to, message)
			}
			return false, nil
		},
	}
	unwanted := errors.New("unexpected external side effect")
	binder, err := NewBinder(
		&resolverStub{live: func(context.Context, string) (workflow.Definition, bool, error) {
			return workflow.Definition{}, false, unwanted
		}},
		&rendererStub{},
		&authorizerStub{get: func(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
			return snapshot.Snapshot{}, false, unwanted
		}},
		store,
		&budgetStub{admit: func(context.Context, BudgetAdmission) error { return unwanted }},
		&saverStub{save: func(context.Context, AdmissionContext, ImmutableTemplateSpec) (WorkflowRunTemplateRef, error) {
			return WorkflowRunTemplateRef{}, unwanted
		}},
		&creatorStub{create: func(context.Context, snapshot.WorkflowRunID, WorkflowRunTemplateRef, map[string]any, string, BeforeWorkflowRunCommit) (WorkflowRunExecution, bool, error) {
			t.Fatal("a lost admission CAS must never start a second execution")
			return WorkflowRunExecution{}, false, nil
		}},
		&secretStub{prepare: func(context.Context, AdmissionContext, db.AgentWorkflowRun) (PreparedRunSecret, error) {
			return nil, unwanted
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	admission := AdmissionContext{TeamID: 7, TeamName: "research", CreatedBy: "alice", Origin: Origin{Kind: "manual"}}
	request := BindRequest{
		WorkflowName: definition.Name, Inputs: map[string]snapshot.SnapshotID{},
		IdempotencyKey: "lost-advance-cas",
	}

	_, err = binder.BindAndCreate(context.Background(), admission, request)
	if !errors.Is(err, ErrPlatformFailure) {
		t.Fatalf("error = %v, want platform failure", err)
	}
	if errors.Is(err, ErrCorruptPartialAdmission) {
		t.Fatalf("a lost CAS is not corruption: %v", err)
	}
	if !strings.Contains(err.Error(), "workflow admission CAS did not advance") {
		t.Fatalf("error = %q, want the lost-CAS explanation", err)
	}
	if transitions != 1 {
		t.Fatalf("transition attempts = %d, want 1", transitions)
	}

	// No wedge: once the winner has advanced the row, the same call succeeds
	// against the same durable identity without re-running admission.
	winnerAdvanced = true
	result, err := binder.BindAndCreate(context.Background(), admission, request)
	if err != nil {
		t.Fatalf("retry BindAndCreate: %v", err)
	}
	if result.Created || result.Run.ID != admitting.ID || result.Run.Status != db.AgentWorkflowRunStatusRunning {
		t.Fatalf("retry result = %+v", result)
	}
	if transitions != 1 {
		t.Fatalf("retry re-attempted the CAS: transitions = %d", transitions)
	}
}
```

- [ ] Add the sibling arm `TestBindAndCreateWonAdmissionCASWithUnadvancedRowIsCorrupt`: identical setup, but the transition stub returns `true, nil` and `find` always returns `admitting`; assert `errors.Is(err, ErrCorruptPartialAdmission)` and that the creator stub is never called.
- [ ] Add `TestBindAndCreateLostFailureCASDefersToDurableWinner`, entering `failAllocated` through `handleExisting` → `resume` → budget denial on an admitting run with an **empty** execution:

```go
func TestBindAndCreateLostFailureCASDefersToDurableWinner(t *testing.T) {
	definition := binderTestDefinition()
	rendered := binderTestRendered(t, definition)
	run := db.AgentWorkflowRun{
		ID: 41, TeamID: 7, TeamName: "research", WorkflowDefinitionID: definition.ID,
		WorkflowName: definition.Name, WorkflowVersion: definition.Version,
		SchemaVersion: 3, SignatureVersion: 1, DefinitionContentHash: definition.ContentHash,
		IdempotencyKey: "lost-failure-cas", ParameterizedConfig: mustCanonical(t, rendered.Config),
		ParameterizedConfigHash: rendered.TargetConfigHash, OriginKind: "manual", CreatedBy: "alice",
		Status: db.AgentWorkflowRunStatusAdmitting,
	}
	winner := run
	winner.Status = db.AgentWorkflowRunStatusRunning
	pipelineRunID, templateID, instanceID := 73, 2, 3
	plannedBuildID := int64(5)
	concreteHash := strings.Repeat("c", 64)
	winner.PipelineRunID, winner.TemplatePipelineID, winner.InstancePipelineID =
		&pipelineRunID, &templateID, &instanceID
	winner.ConcreteConfig, winner.ConcreteConfigHash = mustCanonical(t, rendered.Config), &concreteHash
	winner.PlannedBuildID = &plannedBuildID

	findCalls := 0
	store := &storeStub{
		find: func(context.Context, int, string) (db.AgentWorkflowRun, bool, error) {
			findCalls++
			if findCalls == 1 {
				return run, true, nil
			}
			return winner, true, nil
		},
		snapshots: func(context.Context, snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error) {
			return nil, nil
		},
		transition: func(_ context.Context, id snapshot.WorkflowRunID, from, to db.AgentWorkflowRunStatus, message string) (bool, error) {
			if id != run.ID || from != db.AgentWorkflowRunStatusAdmitting ||
				to != db.AgentWorkflowRunStatusErrored || message != ErrBudgetDenied.Error() {
				t.Fatalf("transition = (%s, %s, %s, %q)", id.String(), from, to, message)
			}
			return false, nil
		},
	}
	unwanted := errors.New("unexpected external side effect")
	binder, err := NewBinder(
		&resolverStub{live: func(context.Context, string) (workflow.Definition, bool, error) {
			return workflow.Definition{}, false, unwanted
		}},
		&rendererStub{},
		&authorizerStub{},
		store,
		&budgetStub{admit: func(context.Context, BudgetAdmission) error { return ErrBudgetDenied }},
		&saverStub{save: func(context.Context, AdmissionContext, ImmutableTemplateSpec) (WorkflowRunTemplateRef, error) {
			return WorkflowRunTemplateRef{}, unwanted
		}},
		&creatorStub{create: func(context.Context, snapshot.WorkflowRunID, WorkflowRunTemplateRef, map[string]any, string, BeforeWorkflowRunCommit) (WorkflowRunExecution, bool, error) {
			t.Fatal("a denied admission must never start an execution")
			return WorkflowRunExecution{}, false, nil
		}},
		&secretStub{prepare: func(context.Context, AdmissionContext, db.AgentWorkflowRun) (PreparedRunSecret, error) {
			t.Fatal("budget denial precedes secret preparation")
			return nil, nil
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := binder.BindAndCreate(context.Background(), AdmissionContext{
		TeamID: 7, TeamName: "research", CreatedBy: "alice", Origin: Origin{Kind: "manual"},
	}, BindRequest{
		WorkflowName: definition.Name, Inputs: map[string]snapshot.SnapshotID{},
		IdempotencyKey: "lost-failure-cas",
	})
	if err != nil {
		t.Fatalf("a lost failure CAS must defer to the durable winner, got %v", err)
	}
	if result.Created || result.Run.ID != run.ID || result.Run.Status != db.AgentWorkflowRunStatusRunning {
		t.Fatalf("result = %+v", result)
	}
}
```

- [ ] Add `TestBindAndCreateLostFailureCASWithoutWinnerReturnsCause`: same shape, but the second `find` returns `db.AgentWorkflowRun{}, false, nil`; assert `errors.Is(err, ErrBudgetDenied)` and that no external stub ran.
- [ ] Run `go test ./agent/workflowrun/ -run 'TestBindAndCreate(Lost|Won)' -count=1 -v` and confirm all four pass. These tests exercise previously-unreached lines; if one fails, the failure text names the branch (`workflow admission CAS did not advance`, `ErrCorruptPartialAdmission`, or "must never start a second execution").
- [ ] Confirm the new lines are reached: `go test ./agent/workflowrun/ -run 'TestBindAndCreate' -coverprofile=/tmp/binder.out -count=1 && go tool cover -func=/tmp/binder.out | grep -E 'advanceAdmission|failAllocated'` — both must report 100.0%.
- [ ] Run `go test ./agent/workflowrun/ -count=1` (whole package) and `gofmt -l agent/workflowrun`.
- [ ] Commit `test(workflowrun): pin both lost-CAS admission branches`.

### Task 2: Prove ticket `Transition` admits exactly one winner under contention

`atc/db/agent_tickets_factory.go:445-513` is a single autocommit `UPDATE … WHERE id = $1 AND state = $from`. That is atomic, but nothing proves it; the existing tests at `agent_tickets_factory_test.go:358-363` are sequential stale-precondition tests. Model the new spec on the 12-caller `ReserveDispatch` race at `agent_tickets_factory_test.go:171-201`.

**CI contract:** runs in the `db-tests` job from plan 01; locally requires PostgreSQL (`pg_isready`).

**Files:**
- Modify: `atc/db/agent_tickets_factory_test.go`

- [ ] Inside the existing `Describe("Transition (the single writer)")` block, add the contention spec:

```go
		It("admits exactly one winner when many callers race the same from-state", func() {
			// Each caller needs its own pooled connection; the suite default is
			// deliberately small so accidental pool sharing is visible.
			dbConn.SetMaxOpenConns(8)

			const callers = 16
			results := make(chan error, callers)
			var wait sync.WaitGroup
			for range callers {
				wait.Add(1)
				go func() {
					defer GinkgoRecover()
					defer wait.Done()
					results <- factory.Transition(id, tickets.StateDraft, tickets.StateQueued,
						tickets.TransitionMeta{})
				}()
			}
			wait.Wait()
			close(results)

			winners, stale := 0, 0
			for err := range results {
				if err == nil {
					winners++
					continue
				}
				Expect(err).To(MatchError(tickets.ErrStaleTransition))
				stale++
			}
			Expect(winners).To(Equal(1), "the from-state precondition must admit exactly one writer")
			Expect(stale).To(Equal(callers - 1))

			got, found, err := factory.Get(id)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(got.State).To(Equal(tickets.StateQueued))
			// Create writes revision 1; exactly one Transition may bump it.
			Expect(got.Revision).To(Equal(int64(2)))
			var queuedAt sql.NullTime
			Expect(dbConn.QueryRow(`SELECT queued_at FROM agent_tickets WHERE id = $1`, id).
				Scan(&queuedAt)).To(Succeed())
			Expect(queuedAt.Valid).To(BeTrue())
		})
```

- [ ] Add the divergent-target arm, which pins that the CAS is on the `from` state and not on the target (both `draft→queued` and `draft→abandoned` are legal edges per `agent/api/tickets/types.go:51`):

```go
		It("lets only one of two competing target states win from the same from-state", func() {
			dbConn.SetMaxOpenConns(8)

			const callersPerTarget = 8
			type outcome struct {
				target tickets.State
				err    error
			}
			results := make(chan outcome, callersPerTarget*2)
			var wait sync.WaitGroup
			for _, target := range []tickets.State{tickets.StateQueued, tickets.StateAbandoned} {
				for range callersPerTarget {
					wait.Add(1)
					go func() {
						defer GinkgoRecover()
						defer wait.Done()
						results <- outcome{target: target, err: factory.Transition(
							id, tickets.StateDraft, target, tickets.TransitionMeta{})}
					}()
				}
			}
			wait.Wait()
			close(results)

			winners := []tickets.State{}
			for result := range results {
				if result.err == nil {
					winners = append(winners, result.target)
					continue
				}
				Expect(result.err).To(MatchError(tickets.ErrStaleTransition))
			}
			Expect(winners).To(HaveLen(1))

			got, _, err := factory.Get(id)
			Expect(err).NotTo(HaveOccurred())
			Expect(got.State).To(Equal(winners[0]), "the durable state must be the winner's target")
			Expect(got.Revision).To(Equal(int64(2)))
		})
```

- [ ] Run `pg_isready` then `ginkgo --focus="AgentTicketsFactory Transition \(the single writer\)" ./atc/db/`. Expect all specs in that Describe green. A regression that widened `Transition` past its `state = $from` precondition fails with `the from-state precondition must admit exactly one writer` and a `winners` count above 1.
- [ ] Run `gofmt -l atc/db` and commit `test(db): prove ticket Transition is single-winner under contention`.

### Task 3: Name the ticket TOCTOU regressions honestly

`agent/api/tickets/task_race_test.go` contains zero goroutines (Report C, gap 9). Its decorator shim simulates a sequential read-then-write window; the real concurrency proof now lives in Task 2. Rename the file, rename the shim, and say so, so that nobody reads the current name as "ticket CAS is proven concurrent".

**Files:**
- Rename: `agent/api/tickets/task_race_test.go` → `agent/api/tickets/task_toctou_test.go`
- Modify: `agent/api/tickets/handler_test.go`

- [ ] `git mv agent/api/tickets/task_race_test.go agent/api/tickets/task_toctou_test.go`.
- [ ] Move `TestCreateAndUpdateRejectNegativeBudget` (the last function in the file, which is ordinary handler validation and uses `newTestHandler`/`withParams`) verbatim to the end of `agent/api/tickets/handler_test.go`, so the renamed file contains only TOCTOU material. Confirm `handler_test.go` imports `net/http`, `net/http/httptest`, `net/url`, and `strings`, adding any that are missing; `task_toctou_test.go`'s own import block still needs all four and is unchanged.
- [ ] Replace the header of `task_toctou_test.go` (everything above `func TestUpdateTaskAppliesToCurrentlyActivePlan`, after the `import` block) with:

```go
// Sequential TOCTOU regressions — there is not one goroutine in this file.
//
// sequentialPlanSwapStore does not simulate parallelism: it mutates the store
// from inside the read the handler performs, which is enough to pin the
// handler's read-then-write window (agent-review-native #7: read plan_version,
// plan replaced, write against the stale version, 200 OK, change invisible).
// The fix routes the handler through the atomic Store.UpdateActiveTask, which
// resolves the active version and writes in one store operation.
//
// The concurrent proof for ticket state is a different test in a different
// place: atc/db/agent_tickets_factory_test.go, "Transition (the single
// writer)" and the ReserveDispatch reservation race, where real goroutines
// contend on the real PostgreSQL compare-and-swap.

// sequentialPlanSwapStore lands a "concurrent" SubmitPlan between any
// handler-side read of the active plan and the subsequent write. The swap is
// performed inline by the reader, not by another goroutine.
type sequentialPlanSwapStore struct {
	*tickets.MemoryStore
	swapped bool
}

func (s *sequentialPlanSwapStore) ActivePlan(id int) ([]tickets.Task, error) {
	stale, err := s.MemoryStore.ActivePlan(id)
	if !s.swapped {
		s.swapped = true
		// the "concurrent" submit: a new plan becomes active AFTER the
		// snapshot above was taken but BEFORE the caller acts on it
		s.MemoryStore.SubmitPlan(id, []tickets.Task{{Title: "replacement"}})
	}
	return stale, err
}
```

- [ ] Update the single construction site in `TestUpdateTaskAppliesToCurrentlyActivePlan` from `&planSwapStore{MemoryStore: mem}` to `&sequentialPlanSwapStore{MemoryStore: mem}`.
- [ ] Run `go test ./agent/api/tickets/ -count=1` and `gofmt -l agent/api/tickets`; expect no behavior change (`ok github.com/concourse/concourse/agent/api/tickets`).
- [ ] Commit `test(tickets): name the sequential TOCTOU regressions honestly`.

### Task 4: Prove a dispatched run's bound inputs survive later ticket edits (CU-11)

CU-11 is one of the two `NONE` rows in the invariant audit: "later ticket edits must not mutate an already-dispatched run's bound input snapshot." The bound copy lives in exactly three places, all durable columns:

- `agent_workflow_run_snapshots (workflow_run_id, direction, port_name, snapshot_id)` — the bound input snapshot IDs;
- `agent_workflow_runs.parameterized_config` / `.parameterized_config_hash` — the rendered config the run executes;
- `agent_tickets.work_item_snapshot_id` / `.repository_snapshot_id` — the ticket's own record of what it dispatched.

The test drives the real factories through the same call sequence `agent/dispatch/dispatch.go:197-291` performs (`ReserveDispatch` → `CaptureRevision` → seal → `RecordDispatchWorkItem` → `CreateWithInputs` → `RecordDispatchRun` → `Transition`). It deliberately does **not** call `dispatch.DispatchOne`: that needs a compiled workflow definition, renderer, and binder, none of which own the invariant. The invariant is a durable-storage property, so it is asserted where the storage is.

**CI contract:** runs in the `db-tests` job from plan 01; locally requires PostgreSQL (`pg_isready`).

**Files:**
- Modify: `atc/db/agent_tickets_factory_test.go`

- [ ] Add `"crypto/sha256"` and `"encoding/hex"` to the file's import block (`context`, `strings`, `time`, `fmt`, `sql`, `snapshot`, `workitem`, `tickets`, `db` are already imported).
- [ ] Add the spec at the top level of the `AgentTicketsFactory` Describe:

```go
	It("never rebinds a dispatched run when the ticket is edited afterwards (CU-11)", func() {
		ctx := context.Background()
		runs := db.NewAgentWorkflowRunsFactory(dbConn)
		unique := time.Now().UnixNano()
		definitionName := fmt.Sprintf("cu11-workflow-%d", unique)
		contentHash := strings.Repeat("a", 64)

		var definitionID int
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by, schema_version, signature_version)
			VALUES ($1, 7, $2, 'schema_version: 3', 'alice', 3, 1)
			RETURNING id
		`, definitionName, contentHash).Scan(&definitionID)).To(Succeed())

		insertSnapshot := func(typeName string, digest string) snapshot.SnapshotID {
			var id snapshot.SnapshotID
			Expect(dbConn.QueryRow(`
				INSERT INTO agent_snapshots
					(type_name, type_version, digest, byte_size, file_count, representation)
				VALUES ($1, 1, $2, 32, 1, 'application/vnd.jetbridge.snapshot.tar.v1')
				RETURNING id
			`, typeName, digest).Scan(&id)).To(Succeed())
			_, err := dbConn.Exec(`
				INSERT INTO agent_snapshot_grants (snapshot_id, team_id, granted_by, reason)
				VALUES ($1, $2, 'alice', 'cu11 test')
			`, int64(id), defaultTeam.ID())
			Expect(err).NotTo(HaveOccurred())
			return id
		}
		documentDigest := func(document []byte) string {
			sum := sha256.Sum256(document)
			return "sha256:" + hex.EncodeToString(sum[:])
		}

		repositoryDigest := "sha256:" + strings.Repeat("b", 64)
		repositoryID := insertSnapshot("repository", repositoryDigest)

		ticketID, err := factory.Create(&tickets.Ticket{
			Title: "original title", Body: "original body", Origin: "fly", Repo: "example/repo",
			WorkflowName: definitionName, UserName: "tdm", CreatedBy: "tdm",
			RepositorySnapshotID: &repositoryID,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(factory.Transition(ticketID, tickets.StateDraft, tickets.StateQueued,
			tickets.TransitionMeta{})).To(Succeed())
		queued, found, err := factory.Get(ticketID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())

		reservation, err := factory.ReserveDispatch(ctx, ticketID, tickets.DispatchReservationRequest{
			ExpectedRevision: queued.Revision, WorkflowVersion: 7, WorkflowDefinitionID: definitionID,
		})
		Expect(err).NotTo(HaveOccurred())

		capture, found, err := factory.CaptureRevision(ctx, ticketID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(capture.Validate()).To(Succeed())
		boundDocument := append([]byte(nil), capture.Document...)
		boundDigest := documentDigest(boundDocument)
		workItemID := insertSnapshot("work-item", boundDigest)
		Expect(factory.RecordDispatchWorkItem(
			ctx, ticketID, reservation.Key, capture.Revision, workItemID)).To(Succeed())

		parameterizedConfig := []byte(`{"jobs":[{"name":"run"}]}`)
		run, created, err := runs.CreateWithInputs(ctx, db.AgentWorkflowRunCreateRequest{
			TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(),
			WorkflowDefinitionID: definitionID, WorkflowName: definitionName, WorkflowVersion: 7,
			SchemaVersion: 3, SignatureVersion: 1, DefinitionContentHash: contentHash,
			IdempotencyKey:          reservation.Key,
			ParameterizedConfig:     parameterizedConfig,
			ParameterizedConfigHash: strings.Repeat("c", 64),
			OriginKind:              "ticket", OriginReference: strconv.Itoa(ticketID),
			CreatedBy: "tdm", Status: db.AgentWorkflowRunStatusAdmitting,
			Inputs: map[string]snapshot.SnapshotRef{
				"work_item":  {ID: workItemID, Type: "work-item/v1", Digest: snapshot.Digest(boundDigest)},
				"repository": {ID: repositoryID, Type: "repository/v1", Digest: snapshot.Digest(repositoryDigest)},
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())

		var pipelineID, pipelineRunID int
		Expect(dbConn.QueryRow(`
			INSERT INTO pipelines (name, team_id, secondary_ordering)
			VALUES ($1, $2, 1) RETURNING id
		`, fmt.Sprintf("cu11-pipeline-%d", unique), defaultTeam.ID()).Scan(&pipelineID)).To(Succeed())
		Expect(dbConn.QueryRow(`
			INSERT INTO pipeline_runs (template_pipeline_id, instance_pipeline_id, number)
			VALUES ($1, $1, 1) RETURNING id
		`, pipelineID).Scan(&pipelineRunID)).To(Succeed())
		Expect(factory.RecordDispatchRun(ctx, ticketID, reservation.Key, run.ID, pipelineRunID)).To(Succeed())
		Expect(factory.Transition(ticketID, tickets.StateQueued, tickets.StateRunning,
			tickets.TransitionMeta{PipelineRunID: &pipelineRunID})).To(Succeed())

		type binding struct {
			direction string
			port      string
			snapshot  int64
			digest    string
		}
		readBindings := func() []binding {
			rows, err := dbConn.Query(`
				SELECT b.direction, b.port_name, b.snapshot_id, s.digest
				FROM agent_workflow_run_snapshots b
				JOIN agent_snapshots s ON s.id = b.snapshot_id
				WHERE b.workflow_run_id = $1
				ORDER BY b.direction, b.port_name
			`, int64(run.ID))
			Expect(err).NotTo(HaveOccurred())
			defer rows.Close()
			bindings := []binding{}
			for rows.Next() {
				var got binding
				Expect(rows.Scan(&got.direction, &got.port, &got.snapshot, &got.digest)).To(Succeed())
				bindings = append(bindings, got)
			}
			Expect(rows.Err()).NotTo(HaveOccurred())
			return bindings
		}
		readRenderedConfig := func() (string, string) {
			var config, hash string
			Expect(dbConn.QueryRow(`
				SELECT parameterized_config::text, parameterized_config_hash
				FROM agent_workflow_runs WHERE id = $1
			`, int64(run.ID)).Scan(&config, &hash)).To(Succeed())
			return config, hash
		}
		boundBindings := readBindings()
		boundConfig, boundConfigHash := readRenderedConfig()
		Expect(boundBindings).To(HaveLen(2))

		// Every mutable surface a human can touch after dispatch.
		newTitle, newBody := "edited title", "edited body"
		Expect(factory.Update(ticketID, tickets.Update{Title: &newTitle, Body: &newBody})).To(Succeed())
		_, err = factory.SubmitSpec(ticketID, tickets.Spec{
			Title: "spec after dispatch", Body: "written while the run is live", SubmittedBy: "tdm",
		})
		Expect(err).NotTo(HaveOccurred())
		rebound := insertSnapshot("repository", "sha256:"+strings.Repeat("d", 64))
		Expect(factory.Update(ticketID, tickets.Update{RepositorySnapshotID: &rebound})).
			To(MatchError(tickets.ErrDispatchConflict), "a dispatched ticket must refuse input rebinding")

		// The edits are real: a fresh capture of the same ticket differs.
		recapture, found, err := factory.CaptureRevision(ctx, ticketID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(recapture.Document).NotTo(Equal(boundDocument))
		Expect(documentDigest(recapture.Document)).NotTo(Equal(boundDigest))

		// The dispatched run is untouched by all of it.
		Expect(readBindings()).To(Equal(boundBindings))
		config, hash := readRenderedConfig()
		Expect(config).To(Equal(boundConfig))
		Expect(hash).To(Equal(boundConfigHash))

		edited, found, err := factory.Get(ticketID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(edited.Title).To(Equal(newTitle))
		Expect(*edited.WorkItemSnapshotID).To(Equal(workItemID))
		Expect(*edited.RepositorySnapshotID).To(Equal(repositoryID))
		var boundWorkItemDigest string
		Expect(dbConn.QueryRow(`SELECT digest FROM agent_snapshots WHERE id = $1`,
			int64(workItemID)).Scan(&boundWorkItemDigest)).To(Succeed())
		Expect(boundWorkItemDigest).To(Equal(boundDigest))
	})
```

- [ ] `strconv` is already imported by this file (used at `agent_tickets_factory_test.go:220`); confirm the import block still compiles with `go vet ./atc/db/`.
- [ ] Run `pg_isready` then `ginkgo --focus="never rebinds a dispatched run" ./atc/db/`. Expect `Ran 1 of N Specs … 1 Passed`. If a future change let `Update` rebind a dispatched input, the spec fails at `a dispatched ticket must refuse input rebinding`; if it let the bound copy drift, it fails on the `readBindings()`/`readRenderedConfig()` equality.
- [ ] Run `gofmt -l atc/db` and commit `test(db): prove ticket edits cannot rebind a dispatched run`.

### Task 5: Close the input side of the seal/GC race

`CommitSealBatch` (`atc/db/agent_snapshots_factory.go:151-171`) builds its digest-lease set from `commit.Outputs` only. `validateSealInputs` (`:395-441`) reads each input through `authorizedSnapshotByRef(..., available=true)` (`:910-928`) with no row lock and no advisory lock, under READ COMMITTED. `agent/snapshot/lifecycle.go:325-392` holds a session lease over the digest it is expiring, deletes locations and bytes, and only then marks the manifest expired — so between this transaction's availability read and its commit, a collector can delete the input's bytes and expire it. The sibling path already solves this: `agent_workflow_runs_factory.go:87` calls `lockWorkflowRunInputDigests` (`:279-299`) before validating inputs, and `agent_workflow_runs_factory_test.go:684` proves it with a real advisory-lock barrier.

The test is written first and drives whichever outcome is real.

**CI contract:** runs in the `db-tests` job from plan 01; locally requires PostgreSQL (`pg_isready`).

**Files:**
- Modify: `atc/db/agent_snapshots_factory_test.go`
- Modify (only if the investigation step says so): `atc/db/agent_snapshots_factory.go`

- [ ] Add the barrier spec inside the existing `Describe("real PostgreSQL digest-lock barriers")` block (its `BeforeEach` already closes the shared lease and raises the pool to 8), reusing `makeUnretained`, `stage`, `output`, `digest`, `newBuild`, `acquireObservedDigestLease` exactly as the neighbouring specs do:

```go
		It("serializes seal input availability with digest GC", func() {
			inputValue, outputValue := digest("9"), digest("a")
			inputRef, _ := makeUnretained(inputValue, "seal-input-source")

			manager := db.NewAgentSnapshotDigestLocker(dbConn)
			gcLease, err := manager.AcquireMany(ctx, []snapshot.Digest{inputValue})
			Expect(err).NotTo(HaveOccurred())
			defer gcLease.Close()

			sealerLease, err := manager.AcquireMany(ctx, []snapshot.Digest{outputValue})
			Expect(err).NotTo(HaveOccurred())
			defer sealerLease.Close()
			staged, err := factory.StageUpload(ctx, sealerLease, snapshot.StageUploadRequest{
				Digest: outputValue, TeamID: defaultTeam.ID(), Attempt: "seal-input-race",
				LeaseExpiresAt: time.Now().Add(time.Hour),
			})
			Expect(err).NotTo(HaveOccurred())
			candidate := output("seal-input-race", "result", "opaque/v1", outputValue, staged)
			buildID := newBuild(defaultTeam.ID())

			commitResult := make(chan error, 1)
			go func() {
				defer GinkgoRecover()
				_, commitErr := factory.CommitSealBatch(ctx, sealerLease, snapshot.SealCommit{
					Context: snapshot.SealCommitContext{
						TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
						Build: &snapshot.BuildOccurrence{
							BuildID: buildID, PlanID: "plan-1", Attempt: "seal-input-race",
							StepKind: "agent", StepName: "produce",
						},
						Inputs:          map[string]snapshot.SnapshotRef{"source": inputRef},
						InputOrder:      []string{"source"},
						ExpectedOutputs: []snapshot.Port{candidate.Port},
					},
					Outputs: []snapshot.SealCommitOutput{candidate},
				})
				commitResult <- commitErr
			}()

			// The commit must not be able to read this input as available while
			// the collector owns its digest: it has to wait for that lock.
			Eventually(func() (int, error) {
				select {
				case commitErr := <-commitResult:
					return 0, fmt.Errorf("seal committed before the input digest barrier: %v", commitErr)
				default:
				}
				var waiters int
				queryErr := dbConn.QueryRow(`
					SELECT count(*)
					FROM pg_stat_activity
					WHERE wait_event_type = 'Lock' AND wait_event = 'advisory'
				`).Scan(&waiters)
				return waiters, queryErr
			}).WithTimeout(5 * time.Second).Should(BeNumerically(">", 0))

			expired, err := factory.MarkDigestExpired(ctx, gcLease, inputValue, time.Now())
			Expect(err).NotTo(HaveOccurred())
			Expect(expired).To(BeTrue())
			Expect(gcLease.Close()).To(Succeed())

			var sealErr error
			Eventually(commitResult).WithTimeout(10 * time.Second).Should(Receive(&sealErr))
			Expect(sealErr).To(MatchError(ContainSubstring("unavailable")))

			var productions, lineage int
			Expect(dbConn.QueryRow(`
				SELECT count(*)
				FROM agent_snapshot_productions p
				JOIN agent_snapshots s ON s.id = p.snapshot_id
				WHERE s.digest = $1
			`, outputValue).Scan(&productions)).To(Succeed())
			Expect(dbConn.QueryRow(`
				SELECT count(*) FROM agent_snapshot_lineage WHERE input_snapshot_id = $1
			`, int64(inputRef.ID)).Scan(&lineage)).To(Succeed())
			Expect(productions).To(Equal(0), "no production may bind content expired at commit time")
			Expect(lineage).To(Equal(0), "no lineage row may reference expired-at-commit content")
		})
```

- [ ] **Investigation step — run the new spec and branch on the result.** Run `pg_isready` then `ginkgo --focus="serializes seal input availability with digest GC" ./atc/db/`.
  - **Outcome A (expected):** the spec fails inside the `Eventually` with `seal committed before the input digest barrier: <nil>` — the commit ran to completion while the collector held the input digest, so lineage was bound to content the collector was in the middle of deleting. **Next action:** implement the fix in the following steps, then re-run.
  - **Outcome B:** the spec passes unchanged — something already serializes the input side. **Next action:** do not change production code. Add a one-line comment above the spec naming the mechanism you found (`git grep -n "pg_advisory_xact_lock" atc/db/agent_snapshots_factory.go` and the `validateSealInputs` call site are the two places to look), and commit with the test-only message at the end of this task.
- [ ] (Outcome A only) Add the lock helper to `atc/db/agent_snapshots_factory.go` next to the other seal validators. `sort`, `fmt`, and `snapshotDigestLockKey` (`atc/db/agent_snapshot_digest_locker.go:69`) are already available in the package:

```go
// lockSealInputDigests serializes this commit against digest GC for every input
// it is about to bind. Output digests are already covered by the caller's
// session lease; inputs are not, and validateSealInputs' availability read is
// otherwise a plain READ COMMITTED snapshot that a collector can invalidate
// before this transaction commits — leaving a lineage row pointing at content
// whose bytes were already being deleted. Same mechanism, key space, and
// lexical acquisition order as lockWorkflowRunInputDigests.
//
// Re-entrancy: an input digest that is also an output digest is already held at
// session scope by this connection's lease. PostgreSQL advisory locks stack
// within a session, so the transaction-scoped acquisition below returns
// immediately rather than self-deadlocking.
func lockSealInputDigests(ctx context.Context, tx Tx, commit snapshot.SealCommitContext) error {
	unique := make(map[snapshot.Digest]struct{}, len(commit.Inputs))
	for _, ref := range commit.Inputs {
		unique[ref.Digest] = struct{}{}
	}
	ordered := make([]snapshot.Digest, 0, len(unique))
	for value := range unique {
		ordered = append(ordered, value)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	for _, value := range ordered {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, snapshotDigestLockKey(value)); err != nil {
			return fmt.Errorf("db: lock snapshot seal input digest %s: %w", value, err)
		}
	}
	return nil
}
```

- [ ] (Outcome A only) Call it inside `CommitSealBatch`, immediately before `validateSealInvocation`, so the lock is held for the whole remainder of the transaction:

```go
	if err := lockSealInputDigests(ctx, tx, commit.Context); err != nil {
		return nil, err
	}
	if err := validateSealInvocation(ctx, tx, commit.Context); err != nil {
		return nil, err
	}
```

- [ ] (Outcome A only) Add the self-overlap regression to the **main** `AgentSnapshotsFactory` Describe (whose `BeforeEach` lease covers all sixteen hex digests, so both the input and the output digest are already held at session scope). The property under test is that the call *returns* rather than blocking on its own session lock:

```go
	It("seals a pass-through output whose digest equals its input digest", func() {
		value := digest("b")
		sourceStage := stage(value, defaultTeam.ID(), "pass-through-source")
		source := seal(newBuild(defaultTeam.ID()), "pass-through-source", nil, nil,
			[]snapshot.SealCommitOutput{
				output("pass-through-source", "result", "opaque/v1", value, sourceStage),
			})["pass-through-source"].Snapshot

		forwardStage := stage(value, defaultTeam.ID(), "pass-through-forward")
		forward := output("pass-through-forward", "forwarded", "opaque/v1", value, forwardStage)
		buildID := newBuild(defaultTeam.ID())
		done := make(chan error, 1)
		go func() {
			defer GinkgoRecover()
			_, commitErr := factory.CommitSealBatch(ctx, lease, snapshot.SealCommit{
				Context: snapshot.SealCommitContext{
					TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), CreatedBy: "alice",
					Build: &snapshot.BuildOccurrence{
						BuildID: buildID, PlanID: "plan-forward", Attempt: "pass-through-forward",
						StepKind: "task", StepName: "forward",
					},
					Inputs:          map[string]snapshot.SnapshotRef{"source": source},
					InputOrder:      []string{"source"},
					ExpectedOutputs: []snapshot.Port{forward.Port},
				},
				Outputs: []snapshot.SealCommitOutput{forward},
			})
			done <- commitErr
		}()
		var commitErr error
		// A self-deadlock on the input lock would hang here instead of returning.
		Eventually(done).WithTimeout(20 * time.Second).Should(Receive(&commitErr))
		Expect(commitErr).NotTo(HaveOccurred())
	})
```

  If this commit is refused for a contract reason unrelated to locking (an error naming a manifest, production, or expected-output conflict rather than hanging), keep the `Eventually(done)` timeout assertion and relax the last line to `Expect(commitErr).To(HaveOccurred())` plus a comment quoting the refusal — the locking property is the returned-not-blocked behavior, not the commit's success.
- [ ] Re-run `ginkgo --focus="serializes seal input availability with digest GC|pass-through output whose digest" ./atc/db/`; both green.
- [ ] Run the full snapshot factory suite for regressions: `ginkgo --focus="AgentSnapshotsFactory" ./atc/db/`. Two-sealer, orphan-GC, and pin-vs-GC barrier specs must stay green (the new lock is taken by the same session that already owns the output lease, so it must not change their ordering).
- [ ] Run `gofmt -l atc/db` and commit — Outcome A: `fix(db): serialize seal input availability with digest GC`; Outcome B: `test(db): pin input-side seal and digest-GC serialization`.

### Task 6: Pin the `agent_snapshots` foreign-key topology

Deleting an `agent_snapshots` row is not a supported operation — the design deletes *bytes* and keeps rows forever — but three foreign keys are `ON DELETE CASCADE`, so a future delete path would silently destroy review, feedback, and projection rows. Nothing pins that today. Verified against the migrations at `410d9b59f8`: **24 foreign keys reference `agent_snapshots`, 21 `RESTRICT` (20 single-column plus the composite `agent_snapshot_exposures` key) and exactly 3 `CASCADE`.**

The assertion keys on `(child table, ordered child columns)` from the catalog — formatting-independent — and asserts `pg_get_constraintdef` for each entry names the expected delete action.

**CI contract:** runs in the `db-tests` job from plan 01; locally requires PostgreSQL (`pg_isready`).

**Files:**
- Create: `atc/db/migration/snapshot_fk_topology_test.go`

- [ ] Create the spec, modelled on `atc/db/migration/workflow_waits_test.go:15-40` for the migrator setup and on its `pg_get_constraintdef` assertion at `:198-205`. `jetbridgeHeadMigration` is declared in the same package at `atc/db/migration/legacy_upgrade_test.go:37`:

```go
package migration_test

import (
	"database/sql"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The snapshot design never deletes an agent_snapshots row: bytes expire, rows
// and lineage stay forever. Three foreign keys are nonetheless ON DELETE
// CASCADE, so any future delete path would silently destroy review, feedback,
// and projection rows instead of failing. This test makes both the RESTRICT set
// and the CASCADE trio a deliberate, reviewed choice.
var _ = Describe("agent_snapshots foreign-key topology", func() {
	var database *sql.DB
	var lockDB [lock.FactoryCount]*sql.DB
	var migrator migration.Migrator

	BeforeEach(func() {
		var err error
		database, err = sql.Open("pgx", postgresRunner.DataSourceName())
		Expect(err).NotTo(HaveOccurred())
		for index := range lock.FactoryCount {
			lockDB[index], err = sql.Open("pgx", postgresRunner.DataSourceName())
			Expect(err).NotTo(HaveOccurred())
		}
		noop := func(lager.Logger, lock.LockID) {}
		migrator = migration.NewMigrator(database, lock.NewLockFactory(lockDB, noop, noop))
		Expect(migrator.Migrate(nil, nil, jetbridgeHeadMigration)).To(Succeed())
	})

	AfterEach(func() {
		_ = database.Close()
		for _, connection := range lockDB {
			_ = connection.Close()
		}
	})

	It("keeps every snapshot reference RESTRICT except the three declared CASCADEs", func() {
		// key: "<child table>(<ordered child columns>)"; value: delete action.
		want := map[string]string{
			"agent_experiment_evaluations(measurement_snapshot_id)":              "RESTRICT",
			"agent_experiment_fixture_bindings(snapshot_id)":                     "RESTRICT",
			"agent_publication_occurrences(approval_answer_snapshot_id)":         "RESTRICT",
			"agent_publication_occurrences(approval_question_snapshot_id)":       "RESTRICT",
			"agent_publication_occurrences(input_snapshot_id)":                   "RESTRICT",
			"agent_publications(approval_answer_snapshot_id)":                    "RESTRICT",
			"agent_publications(approval_question_snapshot_id)":                  "RESTRICT",
			"agent_publications(input_snapshot_id)":                              "RESTRICT",
			"agent_snapshot_exposures(input_snapshot_id,tree_digest)":            "RESTRICT",
			"agent_snapshot_grants(snapshot_id)":                                 "RESTRICT",
			"agent_snapshot_lineage(input_snapshot_id)":                          "RESTRICT",
			"agent_snapshot_productions(snapshot_id)":                            "RESTRICT",
			"agent_snapshot_retention_claims(snapshot_id)":                       "RESTRICT",
			"agent_tickets(repository_snapshot_id)":                              "RESTRICT",
			"agent_tickets(work_item_snapshot_id)":                               "RESTRICT",
			"agent_workflow_outcomes(modification_snapshot_id)":                  "RESTRICT",
			"agent_workflow_outcomes(output_snapshot_id)":                        "RESTRICT",
			"agent_workflow_run_snapshots(snapshot_id)":                          "RESTRICT",
			"agent_workflow_waits(answer_snapshot_id)":                           "RESTRICT",
			"agent_workflow_waits(default_snapshot_id)":                          "RESTRICT",
			"agent_workflow_waits(question_snapshot_id)":                         "RESTRICT",

			// The only three cascades. Each is a derived projection of the
			// snapshot it points at, so it may not outlive that row. Adding a
			// fourth, or reaching one of these from a new delete path, is a
			// data-loss decision and must edit this list on purpose.
			"agent_feedback(review_snapshot_id)":                       "CASCADE",
			"agent_repository_change_projections(snapshot_id)":         "CASCADE",
			"agent_reviews(snapshot_id)":                               "CASCADE",
		}

		rows, err := database.Query(`
			SELECT c.conrelid::regclass::text AS child_table,
			       (
			           SELECT string_agg(a.attname, ',' ORDER BY k.ordinality)
			           FROM unnest(c.conkey) WITH ORDINALITY AS k(attnum, ordinality)
			           JOIN pg_attribute a
			             ON a.attrelid = c.conrelid AND a.attnum = k.attnum
			       ) AS child_columns,
			       pg_get_constraintdef(c.oid) AS definition
			FROM pg_constraint c
			WHERE c.contype = 'f' AND c.confrelid = 'agent_snapshots'::regclass
			ORDER BY 1, 2
		`)
		Expect(err).NotTo(HaveOccurred())
		defer rows.Close()

		got := map[string]string{}
		for rows.Next() {
			var table, columns, definition string
			Expect(rows.Scan(&table, &columns, &definition)).To(Succeed())
			key := table + "(" + columns + ")"
			Expect(definition).To(ContainSubstring("REFERENCES agent_snapshots"), key)
			action, found := want[key]
			Expect(found).To(BeTrue(),
				"undeclared foreign key into agent_snapshots: %s -> %s", key, definition)
			Expect(definition).To(ContainSubstring("ON DELETE "+action),
				"%s must be ON DELETE %s, got %q", key, action, definition)
			got[key] = action
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		Expect(got).To(Equal(want), "the set of foreign keys into agent_snapshots changed")

		cascades := 0
		for _, action := range got {
			if action == "CASCADE" {
				cascades++
			}
		}
		Expect(got).To(HaveLen(24))
		Expect(cascades).To(Equal(3))
	})
})
```

- [ ] Run `pg_isready` then `ginkgo --focus="agent_snapshots foreign-key topology" ./atc/db/migration/`. Expect `1 Passed`. A new snapshot-referencing FK fails with `undeclared foreign key into agent_snapshots: <table>(<columns>) -> ...`; a changed delete action fails with `... must be ON DELETE RESTRICT, got "..."`.
- [ ] If the run reports a count other than 24, do **not** relax the assertion: re-derive the list with `git grep -n "REFERENCES agent_snapshots" atc/db/migration/migrations/` and add the missing entry with the delete action its migration declares.
- [ ] Run `gofmt -l atc/db/migration` and commit `test(db): pin the agent_snapshots foreign-key topology`.

### Task 7: Assert the publication outcome index-repair path actually repairs

`linkSucceededPublicationOutcome` (`atc/db/agent_publications_factory.go:410-467`) re-runs on every terminal `Acquire` (`:172-179`), which is the reconcile that heals a missing `agent_workflow_outcomes` row. Report C: "index-REPAIR path executed but never asserted (no test deletes outcome row)." Delete the row and prove the re-acquire recreates it identically.

**CI contract:** runs in the `db-tests` job from plan 01; locally requires PostgreSQL (`pg_isready`).

**Files:**
- Modify: `atc/db/agent_publications_factory_test.go`

- [ ] Add the spec to the `AgentPublicationsFactory` Describe, reusing its `request()` helper and `input`/`workflowRunID` fixtures:

```go
	It("recreates a deleted outcome index row on terminal re-acquire", func() {
		ctx := context.Background()
		acquired, execute, err := factory.Acquire(ctx, request(), time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(execute).To(BeTrue())
		completed, err := factory.Complete(ctx, acquired.OperationKey, acquired.Attempt, publisher.Result{
			Status: publisher.StatusSucceeded, ExternalID: "pr-31", URL: "https://github.example/pr/31",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(completed.Status).To(Equal(publisher.StatusSucceeded))

		type outcomeRow struct {
			disposition       string
			publicationState  string
			publicationID     int64
			publicationStatus string
			humanModified     bool
			interventionCount int
			labels            string
			actor             string
			revision          int64
		}
		readOutcome := func() (outcomeRow, bool) {
			var row outcomeRow
			err := dbConn.QueryRow(`
				SELECT disposition, publication_state, publication_id, publication_status,
				       human_modified, intervention_count, labels::text, actor, revision
				FROM agent_workflow_outcomes
				WHERE team_id = $1 AND workflow_run_id = $2 AND output_snapshot_id = $3
			`, defaultTeam.ID(), int64(workflowRunID), int64(input.ID)).Scan(
				&row.disposition, &row.publicationState, &row.publicationID, &row.publicationStatus,
				&row.humanModified, &row.interventionCount, &row.labels, &row.actor, &row.revision,
			)
			if errors.Is(err, sql.ErrNoRows) {
				return outcomeRow{}, false
			}
			Expect(err).NotTo(HaveOccurred())
			return row, true
		}

		indexed, found := readOutcome()
		Expect(found).To(BeTrue())
		Expect(indexed.publicationID).To(Equal(int64(completed.ID)))

		// Lose the index row the way a bad migration or a manual repair would.
		_, err = dbConn.Exec(`
			DELETE FROM agent_workflow_outcomes
			WHERE team_id = $1 AND workflow_run_id = $2 AND output_snapshot_id = $3
		`, defaultTeam.ID(), int64(workflowRunID), int64(input.ID))
		Expect(err).NotTo(HaveOccurred())
		_, found = readOutcome()
		Expect(found).To(BeFalse())

		// A terminal re-acquire is the reconcile: it must rebuild the index.
		replayed, execute, err := db.NewAgentPublicationsFactory(dbConn).Acquire(ctx, request(), time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(execute).To(BeFalse(), "a terminal operation must never be re-executed")
		Expect(replayed.ID).To(Equal(completed.ID))

		repaired, found := readOutcome()
		Expect(found).To(BeTrue(), "terminal re-acquire must recreate the outcome index row")
		Expect(repaired).To(Equal(indexed), "the repaired row must carry identical content")
	})
```

- [ ] `errors` and `sql` are needed by `readOutcome`. `errors` is already imported at `agent_publications_factory_test.go:5`; add `"database/sql"` to the import block.
- [ ] Run `pg_isready` then `ginkgo --focus="recreates a deleted outcome index row" ./atc/db/`. Expect `1 Passed`. If the repair path were removed or moved behind the `inserted` branch, the spec fails with `terminal re-acquire must recreate the outcome index row`; if it repaired with different content it fails with `the repaired row must carry identical content` and prints both structs.
- [ ] Run `gofmt -l atc/db` and commit `test(db): assert publication outcome index repair`.

### Task 8: Delete long-expired retention claims in the metadata store

Expired retention-claim rows accumulate forever (Report E, gap 9): nothing ever deletes them, and every GC/repair discovery query scans them. A claim whose `expires_at` has passed already has no effect — `DigestState`'s retention predicate (`atc/db/agent_snapshots_factory.go:1392-1400`) and the discovery query (`:1665-1696`) both filter on `expires_at IS NULL OR expires_at > now()` — so deleting it after a grace period changes no decision. The grace period exists purely so an operator can still see recent expiries.

**CI contract:** runs in the `db-tests` job from plan 01; locally requires PostgreSQL (`pg_isready`).

**Files:**
- Modify: `agent/snapshot/store.go`
- Modify: `agent/snapshot/snapshotfakes/fake_metadata_store.go` (regenerated)
- Modify: `atc/db/agent_snapshots_factory.go`
- Modify: `atc/db/agent_snapshots_factory_test.go`

- [ ] Write the DB spec first, in the main `AgentSnapshotsFactory` Describe:

```go
	It("reaps only retention claims whose expiry is strictly older than the cutoff", func() {
		value := digest("c")
		staged := stage(value, defaultTeam.ID(), "claim-reap")
		ref := seal(newBuild(defaultTeam.ID()), "claim-reap", nil, nil, []snapshot.SealCommitOutput{
			output("claim-reap", "result", "opaque/v1", value, staged),
		})["claim-reap"].Snapshot

		cutoff := time.Now().Truncate(time.Microsecond)
		insertClaim := func(actor string, expiresAt any) {
			_, err := dbConn.Exec(`
				INSERT INTO agent_snapshot_retention_claims
					(snapshot_id, team_id, class, expires_at, actor, reason)
				VALUES ($1, $2, 'binding', $3, $4, 'claim reap test')
			`, int64(ref.ID), defaultTeam.ID(), expiresAt, actor)
			Expect(err).NotTo(HaveOccurred())
		}
		// The seal already created one 'binding' claim with actor "build"; give
		// every other row a distinct actor to satisfy the unique key.
		_, err := dbConn.Exec(`
			UPDATE agent_snapshot_retention_claims
			SET expires_at = $2 WHERE snapshot_id = $1 AND actor = 'build'
		`, int64(ref.ID), cutoff.Add(-time.Second))
		Expect(err).NotTo(HaveOccurred())
		insertClaim("exactly-at-cutoff", cutoff)
		insertClaim("still-retaining", cutoff.Add(time.Hour))
		_, err = dbConn.Exec(`
			INSERT INTO agent_snapshot_retention_claims
				(snapshot_id, team_id, class, expires_at, actor, reason)
			VALUES ($1, $2, 'pin', NULL, 'permanent-pin', 'claim reap test')
		`, int64(ref.ID), defaultTeam.ID())
		Expect(err).NotTo(HaveOccurred())

		reaped, err := factory.ReapExpiredRetentionClaims(ctx, cutoff)
		Expect(err).NotTo(HaveOccurred())
		Expect(reaped).To(Equal(1), "only the strictly-older claim may be deleted")

		rows, err := dbConn.Query(`
			SELECT actor FROM agent_snapshot_retention_claims
			WHERE snapshot_id = $1 ORDER BY actor
		`, int64(ref.ID))
		Expect(err).NotTo(HaveOccurred())
		defer rows.Close()
		actors := []string{}
		for rows.Next() {
			var actor string
			Expect(rows.Scan(&actor)).To(Succeed())
			actors = append(actors, actor)
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		Expect(actors).To(Equal([]string{"exactly-at-cutoff", "permanent-pin", "still-retaining"}))

		// Idempotent: a second sweep at the same cutoff removes nothing.
		reaped, err = factory.ReapExpiredRetentionClaims(ctx, cutoff)
		Expect(err).NotTo(HaveOccurred())
		Expect(reaped).To(BeZero())
	})
```

- [ ] Run `pg_isready` then `ginkgo --focus="reaps only retention claims" ./atc/db/` and confirm the compile failure: `factory.ReapExpiredRetentionClaims undefined (type db.AgentSnapshotsFactory has no field or method ReapExpiredRetentionClaims)`.
- [ ] Add the method to the `MetadataStore` contract in `agent/snapshot/store.go`, immediately after `MarkDigestExpired`:

```go
	// ReapExpiredRetentionClaims deletes retention claims whose expiry is
	// strictly older than expiredBefore and returns how many rows it removed.
	// A NULL expiry retains forever and an expiry at or after the cutoff still
	// retains, so neither is ever deleted: the sweep removes only rows that
	// already have no effect on retention, which is why it needs no digest
	// lease. Callers pass now minus a grace period, never a bare now, so
	// recently-expired claims stay visible to operators.
	ReapExpiredRetentionClaims(context.Context, time.Time) (int, error)
```

- [ ] Regenerate the counterfeiter fake: `(cd agent/snapshot && go generate ./...)`. Confirm `agent/snapshot/snapshotfakes/fake_metadata_store.go` gains `ReapExpiredRetentionClaimsStub` and that `var _ snapshot.MetadataStore = new(FakeMetadataStore)` at the file's end still compiles.
- [ ] Implement the method in `atc/db/agent_snapshots_factory.go`, next to `MarkDigestExpired`:

```go
func (factory *agentSnapshotsFactory) ReapExpiredRetentionClaims(
	ctx context.Context,
	expiredBefore time.Time,
) (int, error) {
	if expiredBefore.IsZero() {
		return 0, fmt.Errorf("db: retention claim reap cutoff is required")
	}
	// Strictly older than the cutoff, and never a NULL expiry: a claim that
	// still retains anything is not reapable at any grace period, including
	// zero.
	result, err := factory.conn.ExecContext(ctx, `
		DELETE FROM agent_snapshot_retention_claims
		WHERE expires_at IS NOT NULL AND expires_at < $1
	`, expiredBefore.UTC())
	if err != nil {
		return 0, err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(deleted), nil
}
```

- [ ] Add a second spec asserting the zero-value guard: `_, err := factory.ReapExpiredRetentionClaims(ctx, time.Time{})` must `MatchError(ContainSubstring("cutoff is required"))` and delete nothing.
- [ ] Re-run `ginkgo --focus="reaps only retention claims|cutoff is required" ./atc/db/` — both green. Then run `go build ./...` to catch any other `MetadataStore` implementor.
- [ ] Run `gofmt -l agent/snapshot atc/db` and commit `feat(db): delete long-expired retention claims`.

### Task 9: Reap expired retention claims from the collect sweep

**Requires Task 8.** Wire the store method into `Lifecycle.Collect`, report it, and log it exactly like every other lifecycle counter. `Repair` does not reap: one sweep owns the deletion.

**Files:**
- Modify: `agent/snapshot/lifecycle.go`
- Modify: `agent/snapshot/lifecycle_test.go`
- Modify: `atc/atccmd/command.go`

- [ ] Extend the test stub in `agent/snapshot/lifecycle_test.go` (`lifecycleMetadata`, which embeds `MetadataStore`, so nothing else needs touching):

```go
	reapCutoffs []time.Time
	reapCount   int
	reapErr     error
```

```go
func (m *lifecycleMetadata) ReapExpiredRetentionClaims(_ context.Context, expiredBefore time.Time) (int, error) {
	m.events = append(m.events, "reap-claims")
	m.reapCutoffs = append(m.reapCutoffs, expiredBefore)
	return m.reapCount, m.reapErr
}
```

- [ ] Add the failing tests:

```go
func TestLifecycleCollectReapsClaimsBehindTheGracePeriod(t *testing.T) {
	now := lifecycleNow()
	defer restoreRetentionClaimGrace(retentionClaimGracePeriod)
	retentionClaimGracePeriod = 30 * 24 * time.Hour

	metadata := &lifecycleMetadata{reapCount: 7}
	lifecycle := mustLifecycle(t, metadata, &lifecycleContent{events: &metadata.events},
		&lifecycleRepairer{}, &lifecycleLocks{}, now)
	report, err := lifecycle.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.ClaimsReaped != 7 {
		t.Fatalf("report = %#v", report)
	}
	if len(metadata.reapCutoffs) != 1 || !metadata.reapCutoffs[0].Equal(now.Add(-30*24*time.Hour)) {
		t.Fatalf("cutoffs = %v, want one at now-30d", metadata.reapCutoffs)
	}
}

func TestLifecycleCollectZeroGraceStillPassesTheUnshiftedClock(t *testing.T) {
	now := lifecycleNow()
	defer restoreRetentionClaimGrace(retentionClaimGracePeriod)
	retentionClaimGracePeriod = 0

	metadata := &lifecycleMetadata{}
	lifecycle := mustLifecycle(t, metadata, &lifecycleContent{events: &metadata.events},
		&lifecycleRepairer{}, &lifecycleLocks{}, now)
	if _, err := lifecycle.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The store deletes strictly before the cutoff, so a zero grace can still
	// never reach a claim that expires at or after this instant.
	if len(metadata.reapCutoffs) != 1 || !metadata.reapCutoffs[0].Equal(now) {
		t.Fatalf("cutoffs = %v, want exactly [%s]", metadata.reapCutoffs, now)
	}
}

func TestLifecycleCollectRefusesNegativeGraceAndReapsNothing(t *testing.T) {
	defer restoreRetentionClaimGrace(retentionClaimGracePeriod)
	retentionClaimGracePeriod = -time.Second

	metadata := &lifecycleMetadata{}
	lifecycle := mustLifecycle(t, metadata, &lifecycleContent{events: &metadata.events},
		&lifecycleRepairer{}, &lifecycleLocks{}, lifecycleNow())
	report, err := lifecycle.Collect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "grace period must not be negative") {
		t.Fatalf("error = %v", err)
	}
	if len(metadata.reapCutoffs) != 0 || report.ClaimsReaped != 0 {
		t.Fatalf("a negative grace must never reach the store: %v", metadata.reapCutoffs)
	}
}

func TestLifecycleCollectJoinsClaimReapFailureWithCandidateProgress(t *testing.T) {
	now := lifecycleNow()
	defer restoreRetentionClaimGrace(retentionClaimGracePeriod)
	retentionClaimGracePeriod = time.Hour

	digest := lifecycleDigest(t, '4')
	metadata := &lifecycleMetadata{
		page:      LifecycleCandidatePage{Candidates: []LifecycleCandidate{{Digest: digest, Kind: LifecycleCandidateRepair}}},
		states:    map[Digest]DigestState{},
		reapErr:   errors.New("claim sweep failed"),
		reapCount: 3,
	}
	lifecycle := mustLifecycle(t, metadata, &lifecycleContent{events: &metadata.events},
		&lifecycleRepairer{}, &lifecycleLocks{}, now)
	report, err := lifecycle.Collect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "claim sweep failed") {
		t.Fatalf("error = %v", err)
	}
	if report.Scanned != 1 || report.Deferred != 1 || report.ClaimsReaped != 0 {
		t.Fatalf("a failed sweep must not report reaped claims: %#v", report)
	}
}

func TestLifecycleRepairNeverReapsClaims(t *testing.T) {
	metadata := &lifecycleMetadata{}
	lifecycle := mustLifecycle(t, metadata, &lifecycleContent{events: &metadata.events},
		&lifecycleRepairer{}, &lifecycleLocks{}, lifecycleNow())
	if _, err := lifecycle.Repair(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(metadata.reapCutoffs) != 0 {
		t.Fatalf("Repair reaped claims: %v", metadata.reapCutoffs)
	}
}

func restoreRetentionClaimGrace(previous time.Duration) {
	retentionClaimGracePeriod = previous
}
```

- [ ] Also add a cancellation guard test: `TestLifecycleCollectSkipsClaimReapAfterCancellation` builds a lifecycle over a one-candidate page, cancels the context before `Collect`, and asserts `len(metadata.reapCutoffs) == 0`.
- [ ] Run `go test ./agent/snapshot/ -run 'TestLifecycle(Collect(Reaps|Zero|Refuses|Joins|Skips)|RepairNever)' -count=1` and confirm the compile failure: `undefined: retentionClaimGracePeriod` and `report.ClaimsReaped undefined`.
- [ ] Implement in `agent/snapshot/lifecycle.go`. Add the package-level knob next to `defaultLifecyclePageSize`:

```go
// retentionClaimGracePeriod is how far behind the collector's clock a claim's
// expiry must be before its row is deleted. Expired claims already retain
// nothing; the grace exists so an operator investigating a recent expiry can
// still see the row that caused it. Package-level so tests can shorten it.
var retentionClaimGracePeriod = 30 * 24 * time.Hour
```

  Add `ClaimsReaped int` to `LifecycleReport`, and reshape `Collect`:

```go
func (lifecycle *Lifecycle) Collect(ctx context.Context) (LifecycleReport, error) {
	if lifecycle == nil {
		return LifecycleReport{}, fmt.Errorf("snapshot: lifecycle is required")
	}
	lifecycle.collectMu.Lock()
	defer lifecycle.collectMu.Unlock()
	report, err := lifecycle.runPage(ctx, &lifecycle.collectCursor, func(candidate LifecycleCandidate) bool {
		return candidate.Kind == LifecycleCandidateOrphan || candidate.Kind == LifecycleCandidateExpiry
	}, func(
		ctx context.Context,
		candidate LifecycleCandidate,
		now time.Time,
		lease DigestLease,
		phase *LifecyclePhase,
		report *LifecycleReport,
	) (bool, error) {
		switch candidate.Kind {
		case LifecycleCandidateOrphan:
			return lifecycle.collectOrphan(ctx, candidate.Digest, now, lease, phase, report)
		case LifecycleCandidateExpiry:
			return lifecycle.collectExpiry(ctx, candidate.Digest, now, lease, phase, report)
		default:
			return false, nil
		}
	})
	if ctx.Err() != nil {
		return report, err
	}
	// Claim rows are not digest-scoped and an already-expired claim changes no
	// retention decision, so this sweep needs no lease and cannot race the
	// candidate work above.
	grace := retentionClaimGracePeriod
	if grace < 0 {
		return report, errors.Join(err, fmt.Errorf("snapshot: retention claim grace period must not be negative"))
	}
	reaped, reapErr := lifecycle.metadata.ReapExpiredRetentionClaims(ctx, lifecycle.now().Add(-grace))
	if reapErr != nil {
		return report, errors.Join(err, reapErr)
	}
	if reaped < 0 {
		return report, errors.Join(err, fmt.Errorf("snapshot: negative reaped retention claim count"))
	}
	report.ClaimsReaped = reaped
	return report, err
}
```

- [ ] Re-run the focused lifecycle tests, then `go test ./agent/snapshot/ -count=1` for the whole package.
- [ ] Add the counter to the component log in `atc/atccmd/command.go:2131-2139`, keeping the existing key style:

```go
		"stale_pruned":      report.StalePruned,
		"claims_reaped":     report.ClaimsReaped,
```

- [ ] Run `go build ./...` and `go test ./atc/atccmd/ -count=1`.
- [ ] Run `gofmt -l agent/snapshot atc/atccmd` and commit `feat(snapshot): reap expired retention claims in the collect sweep`.

---

## Acceptance check (spec WS7)

Re-read this list before declaring the workstream done. Each row cites the task that satisfies it.

| WS7 decision | Where it lands | Done when |
|---|---|---|
| 1. Lost-CAS branches (`binder.go:429-432`, `:459-468`) | Task 1 | Four tests; `advanceAdmission` and `failAllocated` both report 100% coverage; the retry proves no wedge and the creator stub proves no double-start |
| 2. `Transition` under contention + rename the decorator tests | Tasks 2, 3 | 16 racers, one winner, `revision` bumped exactly once; `task_toctou_test.go` states it has no goroutines and points at the DB test |
| 3. CU-11 | Task 4 | Bound input snapshot IDs and `parameterized_config`/hash byte-identical across title, body, spec, and refused rebinding; a fresh capture proves the edit was real |
| 4. Input-side seal race | Task 5 | Barrier spec is red first (`seal committed before the input digest barrier`), green after the input-digest xact lock; no production or lineage row binds expired-at-commit content |
| 5. FK topology pin | Task 6 | All 24 FKs enumerated: 21 RESTRICT (20 single-column + the composite exposure key) and exactly 3 CASCADE |
| 6. Index repair asserted | Task 7 | Outcome row deleted, terminal re-`Acquire` recreates it, full-row equality |
| 7. Retention-claim reaping | Tasks 8, 9 | Strictly-older-than-cutoff deletion, never a NULL or future expiry; zero-grace and negative-grace cases tested; `claims_reaped` in the report and the component log; `Repair` never reaps |
| "All new suites run under the WS1 `db-tests` CI job" | Global Constraints + per-task CI contract lines | Tasks 2, 4, 5, 6, 7, 8 name the contract; Task 6's suite is `atc/db/migration`, which `ginkgo -r ./atc/db` covers |

- [ ] Final sweep: `pg_isready`, then `ginkgo -r ./atc/db/` and `go test ./agent/snapshot/ ./agent/workflowrun/ ./agent/api/tickets/ -count=1`.
- [ ] Confirm the two production changes in this plan are exactly the two the spec authorizes (WS7 items 4 and 7): the `CommitSealBatch` input lock (only under Task 5 Outcome A) and retention-claim reaping. Anything else in the diff is out of scope.
