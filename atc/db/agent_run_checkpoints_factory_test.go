package db_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/concourse/concourse/agent/checkpoint"
	"github.com/concourse/concourse/agent/hangar"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/db"
	"github.com/google/uuid"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentRunCheckpointsFactory", func() {
	var (
		ctx      context.Context
		factory  db.AgentRunCheckpointsFactory
		attempts db.AgentRunAttemptsFactory
		identity checkpoint.Identity
	)

	BeforeEach(func() {
		ctx = context.Background()
		factory = db.NewAgentRunCheckpointsFactory(dbConn)
		attempts = db.NewAgentRunAttemptsFactory(dbConn)
		identity = checkpoint.Identity{BuildID: 7123, PlanID: "checkpoint-plan", FunctionID: "implement"}
	})

	authorize := func(subject checkpoint.Identity, target int) checkpoint.FenceClaim {
		current, found, err := attempts.Current(ctx, subject)
		Expect(err).NotTo(HaveOccurred())
		if !found {
			current, err = attempts.AllocateInitial(ctx, checkpoint.AllocateInitialAttemptRequest{
				Identity: subject, MaxTotalAttempts: 3, MaterializationID: "checkpoint-test-1",
			})
			Expect(err).NotTo(HaveOccurred())
		}
		for current.ExecutionAttempt < target {
			if current.State != checkpoint.AttemptInterrupted {
				current, err = attempts.MarkInterrupted(ctx, checkpoint.MarkAttemptInterruptedRequest{
					Identity: subject, ExecutionAttempt: current.ExecutionAttempt, Reason: checkpoint.InterruptionPreempted,
				})
				Expect(err).NotTo(HaveOccurred())
			}
			request := checkpoint.BeginRecoveryRequest{
				Identity: subject, SourceExecutionAttempt: current.ExecutionAttempt,
				Mode: checkpoint.FallbackCheckpointZero, Reason: checkpoint.InterruptionPreempted,
				MaterializationID: fmt.Sprintf("checkpoint-test-%d", current.ExecutionAttempt+1),
			}
			if latest, found, latestErr := factory.Latest(ctx, subject); latestErr != nil {
				Expect(latestErr).NotTo(HaveOccurred())
			} else if found {
				checkpointID := latest.CheckpointID
				request.SourceCheckpointID = &checkpointID
				request.SourceCheckpointGeneration = latest.Generation
				request.Mode = checkpoint.FallbackWorkspaceOnly
			}
			current, err = attempts.BeginRecovery(ctx, request)
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(current.ExecutionAttempt).To(Equal(target))
		if current.State == checkpoint.AttemptScheduling {
			fence, err := attempts.AcquireFence(ctx, checkpoint.AcquireAttemptFenceRequest{
				Identity: subject, ExecutionAttempt: target, Token: uuid.NewString(), TTL: time.Hour,
			})
			Expect(err).NotTo(HaveOccurred())
			current, err = attempts.Transition(ctx, checkpoint.TransitionAttemptRequest{
				Identity: subject, ExecutionAttempt: target, ExpectedState: checkpoint.AttemptScheduling,
				State: checkpoint.AttemptRunning, Fence: fence.FenceClaim,
			})
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(current.Fence).NotTo(BeNil())
		return current.Fence.FenceClaim
	}

	stage := func(attempt int) checkpoint.StagedCheckpoint {
		fence := authorize(identity, attempt)
		staged, err := factory.Begin(ctx, checkpoint.BeginRequest{
			Identity: identity, Fence: fence, ExecutionAttempt: attempt,
		})
		Expect(err).NotTo(HaveOccurred())
		return staged
	}

	prepareAndComplete := func(staged checkpoint.StagedCheckpoint, digit string) checkpoint.ObjectUploadTicket {
		digest := hangar.Digest("sha256:" + strings.Repeat(digit, 64))
		key, err := hangar.Key(hangar.KindCheckpoint, digest)
		Expect(err).NotTo(HaveOccurred())
		ticket, err := factory.PrepareObjectUpload(ctx, checkpoint.PrepareObjectUploadRequest{
			StagedCheckpointID: staged.ID,
			Digest:             digest,
			Key:                key,
			Fence:              staged.Fence,
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = factory.CompleteObjectUpload(ctx, checkpoint.CompleteObjectUploadRequest{
			Ticket: ticket,
			Object: hangar.ObjectRef{Kind: hangar.KindCheckpoint, Digest: digest, Key: key, Generation: 7},
			Fence:  staged.Fence,
		})
		Expect(err).NotTo(HaveOccurred())
		return ticket
	}

	manifestFor := func(staged checkpoint.StagedCheckpoint, ticket checkpoint.ObjectUploadTicket) checkpoint.Manifest {
		return checkpoint.Manifest{
			Version: 1, CheckpointID: staged.ID, Generation: staged.Generation, ExecutionAttempt: staged.ExecutionAttempt,
			BuildID: identity.BuildID, PlanID: identity.PlanID, FunctionID: identity.FunctionID,
			Provider: "claude", RuntimeImage: "runner", Model: "test", ConfigDigest: "sha256:" + strings.Repeat("b", 64),
			InputDigest: "sha256:" + strings.Repeat("c", 64), MCPDigest: "sha256:" + strings.Repeat("d", 64), SkillDigest: "sha256:" + strings.Repeat("e", 64),
			Archive: &hangar.ObjectRef{Kind: ticket.Kind, Digest: ticket.Digest, Key: ticket.Key, Generation: 7}, SafeAt: time.Now().UTC(),
		}
	}

	createWorkflowRun := func() snapshot.WorkflowRunID {
		suffix := time.Now().UnixNano()
		name := fmt.Sprintf("checkpoint-workflow-%d", suffix)
		contentHash := fmt.Sprintf("checkpoint-definition-%d", suffix)
		var definitionID int
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by, schema_version, signature_version)
			VALUES ($1, 1, $2, 'schema_version: 3', 'checkpoint-test', 3, 1)
			RETURNING id
		`, name, contentHash).Scan(&definitionID)).To(Succeed())
		var runID snapshot.WorkflowRunID
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_runs
				(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
				 schema_version, signature_version, definition_content_hash, idempotency_key,
				 parameterized_config, parameterized_config_hash, origin_kind, origin_reference,
				 created_by, status)
			VALUES ($1, $2, $3, $4, 1, 3, 1, $5, $6, '{}', $7,
				'test', '', 'checkpoint-test', 'running')
			RETURNING id
		`, defaultTeam.ID(), defaultTeam.Name(), definitionID, name, contentHash,
			fmt.Sprintf("checkpoint-run-%d", suffix), fmt.Sprintf("checkpoint-config-%d", suffix),
		).Scan(&runID)).To(Succeed())
		return runID
	}

	It("requires optional workflow-run identity to match exactly in both directions", func() {
		runID := createWorkflowRun()
		withoutRun := identity
		withRun := identity
		withRun.WorkflowRunID = &runID

		withoutRunFence := authorize(withoutRun, 1)
		staged, err := factory.Begin(ctx, checkpoint.BeginRequest{Identity: withoutRun, Fence: withoutRunFence, ExecutionAttempt: 1})
		Expect(err).NotTo(HaveOccurred())
		Expect(factory.Abort(ctx, checkpoint.AbortRequest{StagedCheckpointID: staged.ID, Fence: staged.Fence})).To(Succeed())

		_, err = factory.Begin(ctx, checkpoint.BeginRequest{Identity: withRun, Fence: checkpoint.FenceClaim{ExecutionAttempt: 2, Token: uuid.NewString()}, ExecutionAttempt: 2})
		Expect(errors.Is(err, checkpoint.ErrConflict)).To(BeTrue(),
			"a caller cannot attach workflow provenance to a head created without it")
		_, _, err = factory.Latest(ctx, withRun)
		Expect(errors.Is(err, checkpoint.ErrConflict)).To(BeTrue(),
			"read paths must enforce the same exact optional identity")

		withoutRun.BuildID++
		withRun.BuildID++
		withRunFence := authorize(withRun, 1)
		staged, err = factory.Begin(ctx, checkpoint.BeginRequest{Identity: withRun, Fence: withRunFence, ExecutionAttempt: 1})
		Expect(err).NotTo(HaveOccurred())
		Expect(factory.Abort(ctx, checkpoint.AbortRequest{StagedCheckpointID: staged.ID, Fence: staged.Fence})).To(Succeed())

		_, err = factory.Begin(ctx, checkpoint.BeginRequest{Identity: withoutRun, Fence: checkpoint.FenceClaim{ExecutionAttempt: 2, Token: uuid.NewString()}, ExecutionAttempt: 2})
		Expect(errors.Is(err, checkpoint.ErrConflict)).To(BeTrue(),
			"a caller cannot omit workflow provenance from a head created with it")
		Expect(errors.Is(factory.MarkTerminal(ctx, withoutRun, time.Now()), checkpoint.ErrConflict)).To(BeTrue(),
			"mutating paths must enforce the same exact optional identity")
	})

	It("assigns object upload expiry from database authority", func() {
		staged := stage(1)
		digest := hangar.Digest("sha256:" + strings.Repeat("0", 64))
		key, err := hangar.Key(hangar.KindCheckpoint, digest)
		Expect(err).NotTo(HaveOccurred())
		ticket, err := factory.PrepareObjectUpload(ctx, checkpoint.PrepareObjectUploadRequest{
			StagedCheckpointID: staged.ID, Digest: digest, Key: key, Fence: staged.Fence,
		})
		Expect(err).NotTo(HaveOccurred())

		var uploadTTLSeconds float64
		Expect(dbConn.QueryRow(`
			SELECT EXTRACT(EPOCH FROM (upload_expires_at - created_at))
			FROM agent_checkpoint_objects WHERE id = $1
		`, ticket.ObjectID).Scan(&uploadTTLSeconds)).To(Succeed())
		// The expiry is one hour from clock_timestamp(), which advances during
		// the transaction, while created_at defaults to now(), which is fixed at
		// transaction start. The difference is therefore an hour plus however
		// long this transaction had already been running — never less, and never
		// more than by that elapsed time. A caller-supplied window would miss by
		// hours, not by the milliseconds a loaded machine adds here.
		Expect(uploadTTLSeconds).To(BeNumerically(">=", time.Hour.Seconds()),
			"the upload window is measured from database time, not caller time")
		Expect(uploadTTLSeconds).To(BeNumerically("<", (time.Hour+time.Minute).Seconds()),
			"the caller cannot lengthen the upload authority window")
	})

	It("uses database time rather than a caller cutoff to expire staged checkpoints", func() {
		staged := stage(1)
		claims, err := factory.ClaimCheckpointExpirations(ctx, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(claims).To(BeEmpty())

		var status checkpoint.CheckpointStatus
		Expect(dbConn.QueryRow(`SELECT status FROM agent_run_checkpoints WHERE id = $1`, staged.ID).Scan(&status)).To(Succeed())
		Expect(status).To(Equal(checkpoint.CheckpointStaged),
			"a caller-forged future cutoff cannot accelerate the server-owned stage deadline")

		_, err = dbConn.Exec(`
			UPDATE agent_run_checkpoints SET stage_expires_at = now() - INTERVAL '1 second'
			WHERE id = $1
		`, staged.ID)
		Expect(err).NotTo(HaveOccurred())
		claims, err = factory.ClaimCheckpointExpirations(ctx, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(claims).To(BeEmpty(), "staged expiry aborts but never emits a recovery-object claim")
		Expect(dbConn.QueryRow(`SELECT status FROM agent_run_checkpoints WHERE id = $1`, staged.ID).Scan(&status)).To(Succeed())
		Expect(status).To(Equal(checkpoint.CheckpointAborted))
	})

	It("reserves generations once and commits only an available ticket through a CAS", func() {
		fence := authorize(identity, 1)
		var dbBefore time.Time
		Expect(dbConn.QueryRow(`SELECT clock_timestamp()`).Scan(&dbBefore)).To(Succeed())
		first, beginErr := factory.Begin(ctx, checkpoint.BeginRequest{
			Identity: identity, Fence: fence, ExecutionAttempt: 1,
		})
		Expect(beginErr).NotTo(HaveOccurred())
		Expect(first.Generation).To(Equal(1))
		Expect(first.ExpectedPreviousGeneration).To(Equal(0))
		var persistedStageExpiresAt, dbAfter time.Time
		Expect(dbConn.QueryRow(`
			SELECT stage_expires_at, clock_timestamp()
			FROM agent_run_checkpoints WHERE id = $1
		`, first.ID).Scan(&persistedStageExpiresAt, &dbAfter)).To(Succeed())
		Expect(first.StageExpiresAt).To(BeTemporally("==", persistedStageExpiresAt))
		Expect(persistedStageExpiresAt).To(BeTemporally(">=", dbBefore.Add(time.Hour)),
			"the database authority must assign at least the one-hour stage deadline")
		Expect(persistedStageExpiresAt).To(BeTemporally("<=", dbAfter.Add(time.Hour)),
			"the stage deadline must be based on database time during Begin")
		_, err := factory.Begin(ctx, checkpoint.BeginRequest{
			Identity: identity, Fence: checkpoint.FenceClaim{ExecutionAttempt: 2, Token: uuid.NewString()}, ExecutionAttempt: 2,
		})
		Expect(err).To(HaveOccurred(), "only one active staged generation may hold a head")

		By("rejecting a commit before Hangar has acknowledged the exact upload ticket")
		ticket, err := factory.PrepareObjectUpload(ctx, checkpoint.PrepareObjectUploadRequest{
			StagedCheckpointID: first.ID,
			Digest:             hangar.Digest("sha256:" + strings.Repeat("a", 64)),
			Key:                "hangar/v1/checkpoints/sha256/" + strings.Repeat("a", 64) + ".tar.zst",
			Fence:              first.Fence,
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = factory.Commit(ctx, checkpoint.CommitRequest{StagedCheckpointID: first.ID, ExpectedPreviousGeneration: 0, Manifest: manifestFor(first, ticket), Fence: first.Fence})
		Expect(err).To(HaveOccurred())

		_, err = factory.CompleteObjectUpload(ctx, checkpoint.CompleteObjectUploadRequest{
			Ticket: ticket,
			Object: hangar.ObjectRef{Kind: ticket.Kind, Digest: ticket.Digest, Key: ticket.Key, Generation: 7},
			Fence:  first.Fence,
		})
		Expect(err).NotTo(HaveOccurred())
		committed, err := factory.Commit(ctx, checkpoint.CommitRequest{StagedCheckpointID: first.ID, ExpectedPreviousGeneration: 0, Manifest: manifestFor(first, ticket), Fence: first.Fence})
		Expect(err).NotTo(HaveOccurred())
		Expect(committed.Generation).To(Equal(1))

		latest, found, err := factory.Latest(ctx, identity)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(latest.CheckpointID).To(Equal(first.ID))

		second := stage(2)
		Expect(second.Generation).To(Equal(2))
		Expect(second.ExpectedPreviousGeneration).To(Equal(1))
		Expect(factory.Abort(ctx, checkpoint.AbortRequest{StagedCheckpointID: second.ID, Fence: second.Fence})).To(Succeed())
		Expect(factory.Abort(ctx, checkpoint.AbortRequest{StagedCheckpointID: second.ID, Fence: second.Fence})).To(Succeed())

		third := stage(3)
		Expect(third.Generation).To(Equal(3), "aborting a generation must never make it reusable")
	})

	It("rejects stale and out-of-order staged candidates without moving the committed head backwards", func() {
		first := stage(1)
		firstTicket := prepareAndComplete(first, "a")
		_, err := factory.Commit(ctx, checkpoint.CommitRequest{StagedCheckpointID: first.ID, ExpectedPreviousGeneration: 0, Manifest: manifestFor(first, firstTicket), Fence: first.Fence})
		Expect(err).NotTo(HaveOccurred())

		candidate := stage(2)
		candidateTicket := prepareAndComplete(candidate, "b")
		_, err = factory.Commit(ctx, checkpoint.CommitRequest{StagedCheckpointID: candidate.ID, ExpectedPreviousGeneration: 0, Manifest: manifestFor(candidate, candidateTicket), Fence: candidate.Fence})
		Expect(err).To(HaveOccurred())

		latest, found, err := factory.Latest(ctx, identity)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(latest.Generation).To(Equal(1))
	})

	It("loads only an exact committed or retained superseded recovery source", func() {
		staged := stage(1)
		ticket := prepareAndComplete(staged, "a")
		committed, err := factory.Commit(ctx, checkpoint.CommitRequest{StagedCheckpointID: staged.ID, ExpectedPreviousGeneration: 0, Manifest: manifestFor(staged, ticket), Fence: staged.Fence})
		Expect(err).NotTo(HaveOccurred())
		loaded, err := factory.LoadRetainedRecoverySource(ctx, identity, committed.CheckpointID, committed.Generation)
		Expect(err).NotTo(HaveOccurred())
		Expect(loaded.CheckpointID).To(Equal(committed.CheckpointID))
		_, err = factory.LoadRetainedRecoverySource(ctx, identity, committed.CheckpointID+1, committed.Generation)
		Expect(errors.Is(err, checkpoint.ErrNotFound)).To(BeTrue())
		_, err = factory.LoadRetainedRecoverySource(ctx, identity, committed.CheckpointID, committed.Generation+1)
		Expect(errors.Is(err, checkpoint.ErrNotFound)).To(BeTrue())
		_, err = dbConn.Exec(`UPDATE agent_run_checkpoints SET status = 'superseded', superseded_at = clock_timestamp() WHERE id = $1`, committed.CheckpointID)
		Expect(err).NotTo(HaveOccurred())
		loaded, err = factory.LoadRetainedRecoverySource(ctx, identity, committed.CheckpointID, committed.Generation)
		Expect(err).NotTo(HaveOccurred())
		Expect(loaded.CheckpointID).To(Equal(committed.CheckpointID))
	})

	It("fences terminal heads from new reservations and delayed commits", func() {
		first := stage(1)
		firstTicket := prepareAndComplete(first, "a")
		_, err := factory.Commit(ctx, checkpoint.CommitRequest{
			StagedCheckpointID: first.ID, ExpectedPreviousGeneration: 0, Manifest: manifestFor(first, firstTicket), Fence: first.Fence,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(factory.MarkTerminal(ctx, identity, time.Now())).To(Succeed())

		_, err = factory.Begin(ctx, checkpoint.BeginRequest{Identity: identity, Fence: checkpoint.FenceClaim{ExecutionAttempt: 2, Token: uuid.NewString()}, ExecutionAttempt: 2})
		Expect(errors.Is(err, checkpoint.ErrConflict)).To(BeTrue(), "an inactive head cannot reserve another generation")

		delayedIdentity := checkpoint.Identity{
			BuildID: identity.BuildID + 1, PlanID: identity.PlanID + "-delayed", FunctionID: identity.FunctionID,
		}
		identity = delayedIdentity
		delayed := stage(1)
		delayedTicket := prepareAndComplete(delayed, "b")
		Expect(factory.MarkTerminal(ctx, delayedIdentity, time.Now())).To(Succeed())
		_, err = factory.Commit(ctx, checkpoint.CommitRequest{
			StagedCheckpointID: delayed.ID, ExpectedPreviousGeneration: 0, Manifest: manifestFor(delayed, delayedTicket), Fence: delayed.Fence,
		})
		Expect(errors.Is(err, checkpoint.ErrConflict)).To(BeTrue(), "a delayed commit cannot replace a terminal head")

		latest, found, err := factory.Latest(ctx, delayedIdentity)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
		Expect(latest).To(Equal(checkpoint.Manifest{}))
	})

	It("converges concurrent first uploads for one digest on the same durable object", func() {
		const contenders = 8
		dbConn.SetMaxOpenConns(contenders)
		stages := make([]checkpoint.StagedCheckpoint, 0, contenders)
		for index := range contenders {
			identity = checkpoint.Identity{
				BuildID:    int64(8000 + index),
				PlanID:     fmt.Sprintf("concurrent-upload-%d", index),
				FunctionID: "implement",
			}
			stages = append(stages, stage(1))
		}

		digest := hangar.Digest("sha256:" + strings.Repeat("9", 64))
		key, err := hangar.Key(hangar.KindCheckpoint, digest)
		Expect(err).NotTo(HaveOccurred())
		start := make(chan struct{})
		results := make(chan struct {
			ticket checkpoint.ObjectUploadTicket
			err    error
		}, contenders)
		var group sync.WaitGroup
		for _, staged := range stages {
			staged := staged
			group.Add(1)
			go func() {
				defer group.Done()
				<-start
				ticket, err := factory.PrepareObjectUpload(ctx, checkpoint.PrepareObjectUploadRequest{
					StagedCheckpointID: staged.ID,
					Digest:             digest,
					Key:                key,
					Fence:              staged.Fence,
				})
				results <- struct {
					ticket checkpoint.ObjectUploadTicket
					err    error
				}{ticket: ticket, err: err}
			}()
		}
		close(start)
		group.Wait()
		close(results)

		var objectID int64
		var uploadToken string
		for result := range results {
			Expect(result.err).NotTo(HaveOccurred())
			if objectID == 0 {
				objectID = result.ticket.ObjectID
				uploadToken = result.ticket.UploadToken
				continue
			}
			Expect(result.ticket.ObjectID).To(Equal(objectID))
			Expect(result.ticket.UploadToken).To(Equal(uploadToken))
		}
		Expect(objectID).To(BeNumerically(">", 0))
		Expect(uploadToken).NotTo(BeEmpty())
	})

	It("shares a normalized archive object and retains the terminal latest checkpoint only through its diagnostic TTL", func() {
		first := stage(1)
		ticket := prepareAndComplete(first, "a")
		_, err := factory.Commit(ctx, checkpoint.CommitRequest{StagedCheckpointID: first.ID, ExpectedPreviousGeneration: 0, Manifest: manifestFor(first, ticket), Fence: first.Fence})
		Expect(err).NotTo(HaveOccurred())

		second := stage(1)
		shared, err := factory.PrepareObjectUpload(ctx, checkpoint.PrepareObjectUploadRequest{
			StagedCheckpointID: second.ID, Digest: ticket.Digest, Key: ticket.Key, Fence: second.Fence,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(shared.AlreadyAvailable).To(BeTrue())
		_, err = factory.Commit(ctx, checkpoint.CommitRequest{StagedCheckpointID: second.ID, ExpectedPreviousGeneration: 1, Manifest: manifestFor(second, shared), Fence: second.Fence})
		Expect(err).NotTo(HaveOccurred())

		_, err = dbConn.Exec(`
			UPDATE agent_run_checkpoints SET superseded_at = now() - INTERVAL '2 hours'
			WHERE id = $1
		`, first.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(factory.MarkTerminal(ctx, identity, time.Now().Add(-25*time.Hour))).To(Succeed())
		claims, err := factory.ClaimCheckpointExpirations(ctx, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(claims).To(HaveLen(2), "the superseded and terminal latest rows are independently eligible")
		for _, claim := range claims {
			Expect(factory.FinalizeCheckpointExpiration(ctx, claim)).To(Succeed())
		}
		latest, found, err := factory.Latest(ctx, identity)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
		Expect(latest).To(Equal(checkpoint.Manifest{}))
	})

	It("journals effects monotonically and preserves their server-validated identity", func() {
		fence := authorize(identity, 1)
		begun, err := factory.BeginEffect(ctx, checkpoint.BeginEffectRequest{
			Identity: identity, Fence: fence, ExecutionAttempt: 1,
			Effect: checkpoint.Effect{ToolCallID: "call-1", ToolName: "write_file", Provider: "claude", AdapterVersion: "v1", IdempotencyKey: "key-1", IdempotencyContract: "contract-v1", State: checkpoint.EffectBegun},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(begun.State).To(Equal(checkpoint.EffectBegun))

		committed, err := factory.CommitEffect(ctx, checkpoint.CommitEffectRequest{Identity: identity, Fence: fence, ExecutionAttempt: 1, ToolCallID: "call-1"})
		Expect(err).NotTo(HaveOccurred())
		Expect(committed.State).To(Equal(checkpoint.EffectCommitted))
		_, err = factory.CommitEffect(ctx, checkpoint.CommitEffectRequest{Identity: identity, Fence: fence, ExecutionAttempt: 1, ToolCallID: "call-1"})
		Expect(err).NotTo(HaveOccurred(), "committing an acknowledged effect is idempotent")

		effects, err := factory.ListEffects(ctx, identity, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(effects).To(HaveLen(1))
		Expect(effects[0].IdempotencyContract).To(Equal("contract-v1"))
	})

	It("fences new effects after terminal while allowing an already-begun effect to close", func() {
		fence := authorize(identity, 1)
		begunRequest := checkpoint.BeginEffectRequest{
			Identity: identity, Fence: fence, ExecutionAttempt: 1,
			Effect: checkpoint.Effect{
				ToolCallID: "call-before-terminal", ToolName: "write_file", Provider: "claude",
				AdapterVersion: "v1", IdempotencyKey: "key-before-terminal",
				IdempotencyContract: "contract-v1", State: checkpoint.EffectBegun,
			},
		}
		begun, err := factory.BeginEffect(ctx, begunRequest)
		Expect(err).NotTo(HaveOccurred())
		Expect(begun.State).To(Equal(checkpoint.EffectBegun))
		Expect(factory.MarkTerminal(ctx, identity, time.Now())).To(Succeed())

		_, err = factory.BeginEffect(ctx, begunRequest)
		Expect(errors.Is(err, checkpoint.ErrConflict)).To(BeTrue(), "terminalization must reject every effect begin")

		_, err = factory.BeginEffect(ctx, checkpoint.BeginEffectRequest{
			Identity: identity, Fence: fence, ExecutionAttempt: 1,
			Effect: checkpoint.Effect{
				ToolCallID: "call-after-terminal", ToolName: "write_file", Provider: "claude",
				AdapterVersion: "v1", IdempotencyKey: "key-after-terminal",
				IdempotencyContract: "contract-v1", State: checkpoint.EffectBegun,
			},
		})
		Expect(errors.Is(err, checkpoint.ErrConflict)).To(BeTrue(),
			"a terminal head cannot authorize a new side effect")

		committed, err := factory.CommitEffect(ctx, checkpoint.CommitEffectRequest{
			Identity: identity, Fence: fence, ExecutionAttempt: 1, ToolCallID: begun.ToolCallID,
		})
		Expect(err).NotTo(HaveOccurred(),
			"an effect already authorized before terminal may durably close after its provider returns")
		Expect(committed.State).To(Equal(checkpoint.EffectCommitted))
		_, err = factory.CommitEffect(ctx, checkpoint.CommitEffectRequest{
			Identity: identity, Fence: fence, ExecutionAttempt: 1, ToolCallID: begun.ToolCallID,
		})
		Expect(err).NotTo(HaveOccurred(), "closing an acknowledged terminal-race effect remains idempotent")
	})

	It("rejects stale checkpoint mutations while allowing only the exact begun effect to close", func() {
		staged := stage(1)
		ticket := prepareAndComplete(staged, "c")
		oldFence := staged.Fence
		begun, err := factory.BeginEffect(ctx, checkpoint.BeginEffectRequest{
			Identity: identity, Fence: oldFence, ExecutionAttempt: 1,
			Effect: checkpoint.Effect{ToolCallID: "fenced-effect", ToolName: "write_file", Provider: "claude", AdapterVersion: "v1", State: checkpoint.EffectBegun},
		})
		Expect(err).NotTo(HaveOccurred())
		wrongToken := checkpoint.FenceClaim{ExecutionAttempt: 1, Token: uuid.NewString()}
		_, err = factory.BeginEffect(ctx, checkpoint.BeginEffectRequest{
			Identity: identity, Fence: wrongToken, ExecutionAttempt: 1,
			Effect: checkpoint.Effect{ToolCallID: "cross-token", ToolName: "write_file", Provider: "claude", AdapterVersion: "v1", State: checkpoint.EffectBegun},
		})
		Expect(errors.Is(err, checkpoint.ErrStaleFence)).To(BeTrue())

		_, err = attempts.MarkInterrupted(ctx, checkpoint.MarkAttemptInterruptedRequest{
			Identity: identity, ExecutionAttempt: 1, Reason: checkpoint.InterruptionPreempted,
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = attempts.BeginRecovery(ctx, checkpoint.BeginRecoveryRequest{
			Identity: identity, SourceExecutionAttempt: 1, Mode: checkpoint.FallbackCheckpointZero,
			Reason: checkpoint.InterruptionPreempted, MaterializationID: "replacement",
		})
		Expect(err).NotTo(HaveOccurred())

		mutations := []func() error{
			func() error {
				_, err := factory.Begin(ctx, checkpoint.BeginRequest{Identity: identity, Fence: oldFence, ExecutionAttempt: 1})
				return err
			},
			func() error {
				return factory.Abort(ctx, checkpoint.AbortRequest{StagedCheckpointID: staged.ID, Fence: oldFence})
			},
			func() error {
				_, err := factory.PrepareObjectUpload(ctx, checkpoint.PrepareObjectUploadRequest{StagedCheckpointID: staged.ID, Digest: ticket.Digest, Key: ticket.Key, Fence: oldFence})
				return err
			},
			func() error {
				_, err := factory.CompleteObjectUpload(ctx, checkpoint.CompleteObjectUploadRequest{Ticket: ticket, Object: hangar.ObjectRef{Kind: ticket.Kind, Digest: ticket.Digest, Key: ticket.Key, Generation: 7}, Fence: oldFence})
				return err
			},
			func() error {
				_, err := factory.Commit(ctx, checkpoint.CommitRequest{StagedCheckpointID: staged.ID, ExpectedPreviousGeneration: 0, Manifest: manifestFor(staged, ticket), Fence: oldFence})
				return err
			},
			func() error {
				_, err := factory.BeginEffect(ctx, checkpoint.BeginEffectRequest{Identity: identity, Fence: oldFence, ExecutionAttempt: 1, Effect: checkpoint.Effect{ToolCallID: "after-replacement", ToolName: "write_file", Provider: "claude", AdapterVersion: "v1", State: checkpoint.EffectBegun}})
				return err
			},
		}
		for _, mutate := range mutations {
			Expect(errors.Is(mutate(), checkpoint.ErrStaleFence)).To(BeTrue())
		}

		closed, err := factory.CommitEffect(ctx, checkpoint.CommitEffectRequest{Identity: identity, Fence: oldFence, ExecutionAttempt: 1, ToolCallID: begun.ToolCallID})
		Expect(err).NotTo(HaveOccurred())
		Expect(closed.State).To(Equal(checkpoint.EffectCommitted))
		_, err = factory.CommitEffect(ctx, checkpoint.CommitEffectRequest{Identity: identity, Fence: wrongToken, ExecutionAttempt: 1, ToolCallID: begun.ToolCallID})
		Expect(errors.Is(err, checkpoint.ErrStaleFence)).To(BeTrue())

		expiring := checkpoint.Identity{BuildID: identity.BuildID + 100, PlanID: identity.PlanID, FunctionID: identity.FunctionID}
		_, err = attempts.AllocateInitial(ctx, checkpoint.AllocateInitialAttemptRequest{Identity: expiring, MaterializationID: "expiring"})
		Expect(err).NotTo(HaveOccurred())
		expiredFence, err := attempts.AcquireFence(ctx, checkpoint.AcquireAttemptFenceRequest{
			Identity: expiring, ExecutionAttempt: 1, Token: uuid.NewString(), TTL: 100 * time.Millisecond,
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = attempts.Transition(ctx, checkpoint.TransitionAttemptRequest{
			Identity: expiring, ExecutionAttempt: 1, ExpectedState: checkpoint.AttemptScheduling,
			State: checkpoint.AttemptRunning, Fence: expiredFence.FenceClaim,
		})
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() bool {
			var now time.Time
			Expect(dbConn.QueryRow(`SELECT clock_timestamp()`).Scan(&now)).To(Succeed())
			return !now.Before(expiredFence.ExpiresAt)
		}, time.Second, time.Millisecond).Should(BeTrue())
		_, err = factory.Begin(ctx, checkpoint.BeginRequest{Identity: expiring, Fence: expiredFence.FenceClaim, ExecutionAttempt: 1})
		Expect(errors.Is(err, checkpoint.ErrStaleFence)).To(BeTrue())
	})

	It("rejects a checkpoint reservation whose live fence expires while waiting on the current attempt", func() {
		waitingIdentity := checkpoint.Identity{
			BuildID: identity.BuildID + 101, PlanID: identity.PlanID, FunctionID: identity.FunctionID,
		}
		_, err := attempts.AllocateInitial(ctx, checkpoint.AllocateInitialAttemptRequest{
			Identity: waitingIdentity, MaterializationID: "attempt-lock-expiry",
		})
		Expect(err).NotTo(HaveOccurred())
		fence, err := attempts.AcquireFence(ctx, checkpoint.AcquireAttemptFenceRequest{
			Identity: waitingIdentity, ExecutionAttempt: 1, Token: uuid.NewString(), TTL: 3 * time.Second,
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = attempts.Transition(ctx, checkpoint.TransitionAttemptRequest{
			Identity: waitingIdentity, ExecutionAttempt: 1, ExpectedState: checkpoint.AttemptScheduling,
			State: checkpoint.AttemptRunning, Fence: fence.FenceClaim,
		})
		Expect(err).NotTo(HaveOccurred())

		dbConn.SetMaxOpenConns(8)
		blocker, err := dbConn.BeginTx(ctx, nil)
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(blocker)
		var attemptID int64
		Expect(blocker.QueryRowContext(ctx, `
			SELECT a.id
			FROM agent_run_attempts AS a
			JOIN agent_run_checkpoint_heads AS h ON h.id = a.head_id
			WHERE h.build_id = $1 AND h.plan_id = $2 AND h.function_id = $3 AND a.is_current
			FOR UPDATE OF a
		`, waitingIdentity.BuildID, waitingIdentity.PlanID, waitingIdentity.FunctionID).Scan(&attemptID)).To(Succeed())

		backendPIDs := make(chan int, 1)
		waitingFactory := db.NewAgentRunCheckpointsFactory(checkpointMutationObservedConn{
			DbConn: dbConn, backendPIDs: backendPIDs,
		})
		mutationResult := make(chan error, 1)
		go func() {
			_, err := waitingFactory.Begin(ctx, checkpoint.BeginRequest{
				Identity: waitingIdentity, Fence: fence.FenceClaim, ExecutionAttempt: 1,
			})
			mutationResult <- err
		}()

		var mutationPID int
		Eventually(backendPIDs).WithTimeout(5 * time.Second).Should(Receive(&mutationPID))
		Eventually(func() (bool, error) {
			var waiting bool
			err := dbConn.QueryRow(`
				SELECT EXISTS (
					SELECT 1 FROM pg_locks
					WHERE pid = $1 AND locktype = 'transactionid' AND NOT granted
				)
			`, mutationPID).Scan(&waiting)
			return waiting, err
		}).WithTimeout(5*time.Second).Should(BeTrue(),
			"the checkpoint reservation must be waiting on the locked current attempt before its fence expires")
		var beganBeforeFenceExpiry bool
		Expect(dbConn.QueryRow(`
			SELECT xact_start < $2
			FROM pg_stat_activity
			WHERE pid = $1
		`, mutationPID, fence.ExpiresAt).Scan(&beganBeforeFenceExpiry)).To(Succeed())
		Expect(beganBeforeFenceExpiry).To(BeTrue(), "the mutation transaction began while the fence was live")

		Eventually(func() (bool, error) {
			var expired bool
			err := dbConn.QueryRow(`SELECT clock_timestamp() >= $1`, fence.ExpiresAt).Scan(&expired)
			return expired, err
		}).WithTimeout(5 * time.Second).Should(BeTrue())
		Expect(blocker.Commit()).To(Succeed())

		var mutationErr error
		Eventually(mutationResult).WithTimeout(5 * time.Second).Should(Receive(&mutationErr))
		Expect(errors.Is(mutationErr, checkpoint.ErrStaleFence)).To(BeTrue(),
			"a reservation released after wall-clock expiry must not use the transaction-start timestamp")
	})

	It("does not claim a referenced restore object, then deletes it only after expiration releases the final durable reference", func() {
		staged := stage(1)
		ticket := prepareAndComplete(staged, "a")
		_, err := factory.Commit(ctx, checkpoint.CommitRequest{
			StagedCheckpointID: staged.ID, ExpectedPreviousGeneration: 0, Manifest: manifestFor(staged, ticket), Fence: staged.Fence,
		})
		Expect(err).NotTo(HaveOccurred())

		claims, err := factory.ClaimUnreferencedObjects(ctx, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(claims).To(BeEmpty(), "the latest committed checkpoint keeps its archive restorable")

		Expect(factory.MarkTerminal(ctx, identity, time.Now().Add(-25*time.Hour))).To(Succeed())
		expirations, err := factory.ClaimCheckpointExpirations(ctx, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(expirations).To(HaveLen(1))
		Expect(factory.FinalizeCheckpointExpiration(ctx, expirations[0])).To(Succeed())

		claims, err = factory.ClaimUnreferencedObjects(ctx, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(claims).To(HaveLen(1))
		Expect(claims[0].NeedsUploadInspection).To(BeFalse())
		Expect(factory.FinalizeObjectDeletion(ctx, claims[0])).To(Succeed())
		claims, err = factory.ClaimUnreferencedObjects(ctx, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(claims).To(BeEmpty())
	})

	It("reclaims expired deletion leases after crashes before or after idempotent Hangar deletion", func() {
		for index, crashAfterHangarDelete := range []bool{false, true} {
			By(fmt.Sprintf("recovering crash scenario after_hangar_delete=%t", crashAfterHangarDelete))
			staged := stage(index + 1)
			ticket := prepareAndComplete(staged, fmt.Sprintf("%x", index+1))
			Expect(factory.Abort(ctx, checkpoint.AbortRequest{StagedCheckpointID: staged.ID, Fence: staged.Fence})).To(Succeed())

			claims, err := factory.ClaimUnreferencedObjects(ctx, 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(claims).To(HaveLen(1))
			first := claims[0]
			Expect(first.NeedsUploadInspection).To(BeFalse())

			claims, err = factory.ClaimUnreferencedObjects(ctx, 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(claims).To(BeEmpty(), "an unexpired delete lease must remain exclusive")

			if crashAfterHangarDelete {
				By("leaving metadata unchanged after the generation-matched Hangar delete succeeds")
			}
			_, err = dbConn.Exec(`
				UPDATE agent_checkpoint_objects
				SET delete_lease_expires_at = now() - INTERVAL '1 second'
				WHERE id = $1
			`, ticket.ObjectID)
			Expect(err).NotTo(HaveOccurred())

			restarted := db.NewAgentRunCheckpointsFactory(dbConn)
			claims, err = restarted.ClaimUnreferencedObjects(ctx, 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(claims).To(HaveLen(1))
			reclaimed := claims[0]
			Expect(reclaimed.ObjectID).To(Equal(first.ObjectID))
			Expect(reclaimed.Object).To(Equal(first.Object),
				"reissued generation identity keeps Hangar Delete idempotent when bytes are already absent")
			Expect(reclaimed.Token).NotTo(Equal(first.Token))
			Expect(errors.Is(restarted.FinalizeObjectDeletion(ctx, first), checkpoint.ErrConflict)).To(BeTrue())
			Expect(restarted.FinalizeObjectDeletion(ctx, reclaimed)).To(Succeed())
		}
	})

	It("reclaims an expired reconciliation lease after a crash before Hangar inspection", func() {
		staged := stage(1)
		digest := hangar.Digest("sha256:" + strings.Repeat("6", 64))
		key, err := hangar.Key(hangar.KindCheckpoint, digest)
		Expect(err).NotTo(HaveOccurred())
		ticket, err := factory.PrepareObjectUpload(ctx, checkpoint.PrepareObjectUploadRequest{
			StagedCheckpointID: staged.ID, Digest: digest, Key: key, Fence: staged.Fence,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(factory.Abort(ctx, checkpoint.AbortRequest{StagedCheckpointID: staged.ID, Fence: staged.Fence})).To(Succeed())
		_, err = dbConn.Exec(`
			UPDATE agent_checkpoint_objects
			SET upload_expires_at = now() - INTERVAL '6 minutes'
			WHERE id = $1
		`, ticket.ObjectID)
		Expect(err).NotTo(HaveOccurred())

		claims, err := factory.ClaimUnreferencedObjects(ctx, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(claims).To(HaveLen(1))
		first := claims[0]
		Expect(first.NeedsUploadInspection).To(BeTrue())
		claims, err = factory.ClaimUnreferencedObjects(ctx, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(claims).To(BeEmpty(), "an unexpired reconciliation lease must remain exclusive")

		_, err = dbConn.Exec(`
			UPDATE agent_checkpoint_objects
			SET reconciliation_lease_expires_at = now() - INTERVAL '1 second'
			WHERE id = $1
		`, ticket.ObjectID)
		Expect(err).NotTo(HaveOccurred())
		restarted := db.NewAgentRunCheckpointsFactory(dbConn)
		claims, err = restarted.ClaimUnreferencedObjects(ctx, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(claims).To(HaveLen(1))
		reclaimed := claims[0]
		Expect(reclaimed.ObjectID).To(Equal(first.ObjectID))
		Expect(reclaimed.Token).NotTo(Equal(first.Token))
		_, err = restarted.ReconcileUnreferencedUploadingObject(ctx, first, nil)
		Expect(errors.Is(err, checkpoint.ErrConflict)).To(BeTrue())
		removed, err := restarted.ReconcileUnreferencedUploadingObject(ctx, reclaimed, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(removed).To(BeFalse(), "the reclaimed first absence remains only the first observation")
	})

	It("reclaims inspected uploading metadata after a crash before durable adoption", func() {
		staged := stage(1)
		digest := hangar.Digest("sha256:" + strings.Repeat("7", 64))
		key, err := hangar.Key(hangar.KindCheckpoint, digest)
		Expect(err).NotTo(HaveOccurred())
		ticket, err := factory.PrepareObjectUpload(ctx, checkpoint.PrepareObjectUploadRequest{
			StagedCheckpointID: staged.ID, Digest: digest, Key: key, Fence: staged.Fence,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(factory.Abort(ctx, checkpoint.AbortRequest{StagedCheckpointID: staged.ID, Fence: staged.Fence})).To(Succeed())
		_, err = dbConn.Exec(`
			UPDATE agent_checkpoint_objects
			SET upload_expires_at = now() - INTERVAL '6 minutes'
			WHERE id = $1
		`, ticket.ObjectID)
		Expect(err).NotTo(HaveOccurred())

		claims, err := factory.ClaimUnreferencedObjects(ctx, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(claims).To(HaveLen(1))
		first := claims[0]
		inspected := hangar.ObjectRef{
			Kind: hangar.KindCheckpoint, Digest: digest, Key: key, Generation: 17,
		}
		By("simulating a crash after Inspect returns the immutable generation but before DB adoption")
		_, err = dbConn.Exec(`
			UPDATE agent_checkpoint_objects
			SET reconciliation_lease_expires_at = now() - INTERVAL '1 second'
			WHERE id = $1
		`, ticket.ObjectID)
		Expect(err).NotTo(HaveOccurred())

		restarted := db.NewAgentRunCheckpointsFactory(dbConn)
		claims, err = restarted.ClaimUnreferencedObjects(ctx, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(claims).To(HaveLen(1))
		reclaimed := claims[0]
		Expect(reclaimed.UploadTicket.Digest).To(Equal(first.UploadTicket.Digest))
		Expect(reclaimed.UploadTicket.Key).To(Equal(first.UploadTicket.Key))
		Expect(reclaimed.Token).NotTo(Equal(first.Token))
		adopted, err := restarted.ReconcileUnreferencedUploadingObject(ctx, reclaimed, &inspected)
		Expect(err).NotTo(HaveOccurred())
		Expect(adopted).To(BeTrue())

		var status string
		var generation int64
		Expect(dbConn.QueryRow(`
			SELECT status, generation FROM agent_checkpoint_objects WHERE id = $1
		`, ticket.ObjectID).Scan(&status, &generation)).To(Succeed())
		Expect(status).To(Equal("available"))
		Expect(generation).To(Equal(int64(17)))
	})

	It("reconciles an abandoned upload through two missing-object observations before deleting metadata", func() {
		staged := stage(1)
		digest := hangar.Digest("sha256:" + strings.Repeat("f", 64))
		key, err := hangar.Key(hangar.KindCheckpoint, digest)
		Expect(err).NotTo(HaveOccurred())
		ticket, err := factory.PrepareObjectUpload(ctx, checkpoint.PrepareObjectUploadRequest{
			StagedCheckpointID: staged.ID, Digest: digest, Key: key, Fence: staged.Fence,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(factory.Abort(ctx, checkpoint.AbortRequest{StagedCheckpointID: staged.ID, Fence: staged.Fence})).To(Succeed())
		_, err = dbConn.Exec(`
			UPDATE agent_checkpoint_objects
			SET upload_expires_at = now() - INTERVAL '6 minutes'
			WHERE id = $1
		`, ticket.ObjectID)
		Expect(err).NotTo(HaveOccurred())

		claims, err := factory.ClaimUnreferencedObjects(ctx, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(claims).To(HaveLen(1))
		Expect(claims[0].NeedsUploadInspection).To(BeTrue())
		removed, err := factory.ReconcileUnreferencedUploadingObject(ctx, claims[0], nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(removed).To(BeFalse(), "one absent Inspect result is not enough after an upload interruption")
		_, err = dbConn.Exec(`
			UPDATE agent_checkpoint_objects
			SET missing_observed_at = now() - INTERVAL '6 minutes'
			WHERE id = $1
		`, ticket.ObjectID)
		Expect(err).NotTo(HaveOccurred())

		claims, err = factory.ClaimUnreferencedObjects(ctx, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(claims).To(HaveLen(1))
		removed, err = factory.ReconcileUnreferencedUploadingObject(ctx, claims[0], nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(removed).To(BeTrue())
	})

	It("does not reconcile uploading objects before the server-owned grace period", func() {
		staged := stage(1)
		digest := hangar.Digest("sha256:" + strings.Repeat("8", 64))
		key, err := hangar.Key(hangar.KindCheckpoint, digest)
		Expect(err).NotTo(HaveOccurred())
		_, err = factory.PrepareObjectUpload(ctx, checkpoint.PrepareObjectUploadRequest{
			StagedCheckpointID: staged.ID, Digest: digest, Key: key, Fence: staged.Fence,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(factory.Abort(ctx, checkpoint.AbortRequest{StagedCheckpointID: staged.ID, Fence: staged.Fence})).To(Succeed())

		claims, err := factory.ClaimUnreferencedObjects(ctx, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(claims).To(BeEmpty())
	})

	It("cleans recovered attempt metadata only after checkpoint expiry and object deletion make restore impossible", func() {
		staged := stage(1)
		ticket := prepareAndComplete(staged, "a")
		_, err := factory.Commit(ctx, checkpoint.CommitRequest{
			StagedCheckpointID: staged.ID, ExpectedPreviousGeneration: 0, Manifest: manifestFor(staged, ticket), Fence: staged.Fence,
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = factory.BeginEffect(ctx, checkpoint.BeginEffectRequest{
			Identity: identity, Fence: staged.Fence, ExecutionAttempt: 1,
			Effect: checkpoint.Effect{ToolCallID: "read", ToolName: "read_file", Provider: "claude", AdapterVersion: "v1", ReadOnly: true, State: checkpoint.EffectBegun},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(db.NewAgentRunEventsFactory(dbConn).Record(ctx, checkpoint.RunEvent{
			Identity: identity, ExecutionAttempt: 1, Type: checkpoint.EventSessionCompleted,
		})).To(Succeed())
		_, err = attempts.MarkInterrupted(ctx, checkpoint.MarkAttemptInterruptedRequest{
			Identity: identity, ExecutionAttempt: 1, Reason: checkpoint.InterruptionPreempted,
		})
		Expect(err).NotTo(HaveOccurred())
		recovery, err := attempts.BeginRecovery(ctx, checkpoint.BeginRecoveryRequest{
			Identity: identity, SourceExecutionAttempt: 1,
			SourceCheckpointID: &staged.ID, SourceCheckpointGeneration: staged.Generation,
			Mode: checkpoint.FallbackWorkspaceOnly, Reason: checkpoint.InterruptionPreempted,
			MaterializationID: "metadata-cleanup-recovery",
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = attempts.MarkManualReview(ctx, checkpoint.MarkAttemptManualReviewRequest{
			Identity: identity, ExecutionAttempt: recovery.ExecutionAttempt,
			ExpectedState: checkpoint.AttemptScheduling, MaterializationID: recovery.MaterializationID,
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`
			INSERT INTO agent_run_attempt_metrics
				(attempt_id, build_id, plan_id, execution_attempt, function_id,
				 step_name, source, provider, model, status)
			VALUES ($1, $2, $3, $4, $5, $5, 'agent_step', 'anthropic', 'test', 'error')
		`, recovery.ID, identity.BuildID, identity.PlanID,
			recovery.ExecutionAttempt, identity.FunctionID)
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`
			INSERT INTO agent_run_attempt_transcripts
				(attempt_id, build_id, plan_id, execution_attempt, function_id,
				 step_name, ndjson, byte_len)
			VALUES ($1, $2, $3, $4, $5, $5, 'x', 1)
		`, recovery.ID, identity.BuildID, identity.PlanID,
			recovery.ExecutionAttempt, identity.FunctionID)
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`
			UPDATE agent_run_checkpoint_heads
			SET terminal_at = clock_timestamp() - INTERVAL '31 days'
			WHERE build_id = $1 AND plan_id = $2 AND function_id = $3
		`, identity.BuildID, identity.PlanID, identity.FunctionID)
		Expect(err).NotTo(HaveOccurred())
		expirations, err := factory.ClaimCheckpointExpirations(ctx, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(expirations).To(HaveLen(1))
		Expect(factory.FinalizeCheckpointExpiration(ctx, expirations[0])).To(Succeed())
		deletions, err := factory.ClaimUnreferencedObjects(ctx, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(deletions).To(HaveLen(1))
		Expect(factory.FinalizeObjectDeletion(ctx, deletions[0])).To(Succeed())

		cleaned, err := factory.CleanupTerminalMetadata(ctx, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(cleaned).To(Equal(1))
		latest, found, err := factory.Latest(ctx, identity)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
		Expect(latest).To(Equal(checkpoint.Manifest{}))
		_, found, err = attempts.Current(ctx, identity)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse(), "attempt rows must be removed before their RESTRICT-referenced source checkpoints")
		var retainedAttemptRecords int
		Expect(dbConn.QueryRow(`
			SELECT
				(SELECT count(*) FROM agent_run_attempt_metrics WHERE attempt_id = $1) +
				(SELECT count(*) FROM agent_run_attempt_transcripts WHERE attempt_id = $1)
		`, recovery.ID).Scan(&retainedAttemptRecords)).To(Succeed())
		Expect(retainedAttemptRecords).To(BeZero())
	})
})

type checkpointMutationObservedConn struct {
	db.DbConn
	backendPIDs chan<- int
}

func (conn checkpointMutationObservedConn) BeginTx(ctx context.Context, opts *sql.TxOptions) (db.Tx, error) {
	tx, err := conn.DbConn.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	var pid int
	if err := tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	conn.backendPIDs <- pid
	return tx, nil
}
