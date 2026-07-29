package db_test

import (
	"context"
	"errors"
	"time"

	"github.com/concourse/concourse/agent/checkpoint"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentRunAttemptsFactory", func() {
	const (
		firstToken  = "11111111-1111-1111-1111-111111111111"
		secondToken = "22222222-2222-2222-2222-222222222222"
		staleToken  = "33333333-3333-3333-3333-333333333333"
	)

	var (
		ctx      context.Context
		factory  db.AgentRunAttemptsFactory
		identity checkpoint.Identity
	)

	BeforeEach(func() {
		ctx = context.Background()
		factory = db.NewAgentRunAttemptsFactory(dbConn)
		identity = checkpoint.Identity{
			BuildID: 8142, PlanID: "attempt-plan", FunctionID: "implement",
		}
	})

	allocate := func(maxTotal int) checkpoint.Attempt {
		attempt, err := factory.AllocateInitial(ctx, checkpoint.AllocateInitialAttemptRequest{
			Identity: identity, MaxTotalAttempts: maxTotal, MaterializationID: "materialization-1",
		})
		Expect(err).NotTo(HaveOccurred())
		return attempt
	}

	acquire := func(attempt int, token string) checkpoint.AttemptFence {
		fence, err := factory.AcquireFence(ctx, checkpoint.AcquireAttemptFenceRequest{
			Identity: identity, ExecutionAttempt: attempt, Token: token, TTL: time.Minute,
		})
		Expect(err).NotTo(HaveOccurred())
		return fence
	}

	transition := func(attempt int, token string, from, to checkpoint.AttemptState) checkpoint.Attempt {
		updated, err := factory.Transition(ctx, checkpoint.TransitionAttemptRequest{
			Identity: identity, ExecutionAttempt: attempt, ExpectedState: from, State: to,
			Fence: checkpoint.FenceClaim{ExecutionAttempt: attempt, Token: token},
		})
		Expect(err).NotTo(HaveOccurred())
		return updated
	}

	It("allocates attempt one idempotently with the durable default maximum", func() {
		request := checkpoint.AllocateInitialAttemptRequest{
			Identity: identity, MaterializationID: "materialization-1",
		}
		first, err := factory.AllocateInitial(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(first.ExecutionAttempt).To(Equal(1))
		Expect(first.MaxTotalAttempts).To(Equal(checkpoint.DefaultMaxTotalAttempts))
		Expect(first.State).To(Equal(checkpoint.AttemptScheduling))
		Expect(first.Current).To(BeTrue())

		retried, err := factory.AllocateInitial(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(retried).To(Equal(first))

		current, found, err := factory.Current(ctx, identity)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(current).To(Equal(first))

		changed := request
		changed.MaterializationID = "another-materialization"
		_, err = factory.AllocateInitial(ctx, changed)
		Expect(errors.Is(err, checkpoint.ErrConflict)).To(BeTrue())

		var rows int
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM agent_run_attempts
			WHERE attempt_number = 1 AND materialization_id = 'materialization-1'
		`).Scan(&rows)).To(Succeed())
		Expect(rows).To(Equal(1))
	})

	It("uses PostgreSQL time for an exact, nonrenewable current-attempt fence", func() {
		allocate(0)
		var before time.Time
		Expect(dbConn.QueryRow(`SELECT clock_timestamp()`).Scan(&before)).To(Succeed())

		first, err := factory.AcquireFence(ctx, checkpoint.AcquireAttemptFenceRequest{
			Identity: identity, ExecutionAttempt: 1, Token: firstToken, TTL: time.Minute,
		})
		Expect(err).NotTo(HaveOccurred())
		var after time.Time
		Expect(dbConn.QueryRow(`SELECT clock_timestamp()`).Scan(&after)).To(Succeed())
		Expect(first.ExecutionAttempt).To(Equal(1))
		Expect(first.Token).To(Equal(firstToken))
		Expect(first.ExpiresAt).To(BeTemporally(">=", before.Add(time.Minute)))
		Expect(first.ExpiresAt).To(BeTemporally("<=", after.Add(time.Minute)))

		retried, err := factory.AcquireFence(ctx, checkpoint.AcquireAttemptFenceRequest{
			Identity: identity, ExecutionAttempt: 1, Token: firstToken, TTL: 30 * time.Minute,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(retried).To(Equal(first), "an exact retry must not extend the durable expiry")

		_, err = factory.AcquireFence(ctx, checkpoint.AcquireAttemptFenceRequest{
			Identity: identity, ExecutionAttempt: 1, Token: secondToken, TTL: time.Minute,
		})
		Expect(errors.Is(err, checkpoint.ErrConflict)).To(BeTrue())

		release := checkpoint.ReleaseAttemptFenceRequest{
			Identity: identity, ExecutionAttempt: 1, Token: firstToken,
		}
		Expect(factory.ReleaseFence(ctx, release)).To(Succeed())
		Expect(factory.ReleaseFence(ctx, release)).To(Succeed(), "an exact release retry must be idempotent")

		_, err = factory.AcquireFence(ctx, checkpoint.AcquireAttemptFenceRequest{
			Identity: identity, ExecutionAttempt: 1, Token: firstToken, TTL: time.Minute,
		})
		Expect(errors.Is(err, checkpoint.ErrStaleFence)).To(BeTrue(),
			"a released token must never reacquire authority")

		second := acquire(1, secondToken)
		Expect(second.Token).To(Equal(secondToken))
		Expect(errors.Is(factory.ReleaseFence(ctx, release), checkpoint.ErrStaleFence)).To(BeTrue())
		Expect(factory.ReleaseFence(ctx, checkpoint.ReleaseAttemptFenceRequest{
			Identity: identity, ExecutionAttempt: 1, Token: secondToken,
		})).To(Succeed())
		_, err = factory.AcquireFence(ctx, checkpoint.AcquireAttemptFenceRequest{
			Identity: identity, ExecutionAttempt: 1, Token: firstToken, TTL: time.Minute,
		})
		Expect(errors.Is(err, checkpoint.ErrStaleFence)).To(BeTrue(),
			"a token must remain retired after later lease generations")
	})

	It("allows a fresh token after expiry but never renews the expired token", func() {
		allocate(0)
		expired, err := factory.AcquireFence(ctx, checkpoint.AcquireAttemptFenceRequest{
			Identity: identity, ExecutionAttempt: 1, Token: firstToken, TTL: time.Millisecond,
		})
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() bool {
			var now time.Time
			Expect(dbConn.QueryRow(`SELECT clock_timestamp()`).Scan(&now)).To(Succeed())
			return !now.Before(expired.ExpiresAt)
		}, time.Second, time.Millisecond).Should(BeTrue())

		_, err = factory.AcquireFence(ctx, checkpoint.AcquireAttemptFenceRequest{
			Identity: identity, ExecutionAttempt: 1, Token: firstToken, TTL: time.Minute,
		})
		Expect(errors.Is(err, checkpoint.ErrStaleFence)).To(BeTrue())
		replacement, err := factory.AcquireFence(ctx, checkpoint.AcquireAttemptFenceRequest{
			Identity: identity, ExecutionAttempt: 1, Token: secondToken, TTL: time.Minute,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(replacement.Token).To(Equal(secondToken))
	})

	It("enforces fenced transition CAS and invalidates the lease atomically on interruption", func() {
		allocate(0)
		acquire(1, firstToken)

		request := checkpoint.TransitionAttemptRequest{
			Identity: identity, ExecutionAttempt: 1,
			ExpectedState: checkpoint.AttemptScheduling, State: checkpoint.AttemptMaterializing,
			Fence: checkpoint.FenceClaim{ExecutionAttempt: 1, Token: firstToken},
		}
		materializing, err := factory.Transition(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		retried, err := factory.Transition(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(retried).To(Equal(materializing))

		staleExpected := request
		staleExpected.State = checkpoint.AttemptRunning
		_, err = factory.Transition(ctx, staleExpected)
		Expect(errors.Is(err, checkpoint.ErrConflict)).To(BeTrue())

		running := transition(1, firstToken, checkpoint.AttemptMaterializing, checkpoint.AttemptRunning)
		Expect(running.State).To(Equal(checkpoint.AttemptRunning))
		interrupted, err := factory.MarkInterrupted(ctx, checkpoint.MarkAttemptInterruptedRequest{
			Identity: identity, ExecutionAttempt: 1, Reason: checkpoint.InterruptionNodeLost,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(interrupted.State).To(Equal(checkpoint.AttemptInterrupted))
		Expect(interrupted.InterruptionReason).To(Equal(checkpoint.InterruptionNodeLost))
		Expect(interrupted.Fence).NotTo(BeNil())
		Expect(interrupted.Fence.Token).To(Equal(firstToken))
		Expect(interrupted.Fence.ExpiresAt.IsZero()).To(BeTrue())

		retriedInterruption, err := factory.MarkInterrupted(ctx, checkpoint.MarkAttemptInterruptedRequest{
			Identity: identity, ExecutionAttempt: 1, Reason: checkpoint.InterruptionNodeLost,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(retriedInterruption).To(Equal(interrupted))
		_, err = factory.MarkInterrupted(ctx, checkpoint.MarkAttemptInterruptedRequest{
			Identity: identity, ExecutionAttempt: 1, Reason: checkpoint.InterruptionEvicted,
		})
		Expect(errors.Is(err, checkpoint.ErrConflict)).To(BeTrue())

		_, err = factory.Transition(ctx, checkpoint.TransitionAttemptRequest{
			Identity: identity, ExecutionAttempt: 1,
			ExpectedState: checkpoint.AttemptRunning, State: checkpoint.AttemptFinalizing,
			Fence: checkpoint.FenceClaim{ExecutionAttempt: 1, Token: firstToken},
		})
		Expect(errors.Is(err, checkpoint.ErrStaleFence)).To(BeTrue(),
			"the interrupted owner must lose mutation authority immediately")
	})

	It("allocates exactly one replacement per typed interruption without consuming retries", func() {
		allocate(3)
		_, err := factory.BeginRecovery(ctx, checkpoint.BeginRecoveryRequest{
			Identity: identity, SourceExecutionAttempt: 1, Mode: checkpoint.FallbackCheckpointZero,
			Reason: checkpoint.InterruptionPreempted, MaterializationID: "materialization-2",
		})
		Expect(errors.Is(err, checkpoint.ErrConflict)).To(BeTrue(),
			"a replacement requires the source to be durably interrupted first")

		_, err = factory.MarkInterrupted(ctx, checkpoint.MarkAttemptInterruptedRequest{
			Identity: identity, ExecutionAttempt: 1, Reason: checkpoint.InterruptionPreempted,
		})
		Expect(err).NotTo(HaveOccurred())
		checkpointID := int64(91)
		firstRecovery := checkpoint.BeginRecoveryRequest{
			Identity: identity, SourceExecutionAttempt: 1,
			SourceCheckpointID: &checkpointID, SourceCheckpointGeneration: 7,
			Mode: checkpoint.FallbackWorkspaceOnly, Reason: checkpoint.InterruptionPreempted,
			MaterializationID: "materialization-2",
		}
		second, err := factory.BeginRecovery(ctx, firstRecovery)
		Expect(err).NotTo(HaveOccurred())
		Expect(second.ExecutionAttempt).To(Equal(2))
		Expect(second.SourceExecutionAttempt).To(Equal(1))
		Expect(second.SourceCheckpointID).To(Equal(&checkpointID))
		Expect(second.SourceCheckpointGeneration).To(Equal(7))
		Expect(second.Mode).To(Equal(checkpoint.FallbackWorkspaceOnly))
		Expect(second.SourceInterruptionReason).To(Equal(checkpoint.InterruptionPreempted))

		retried, err := factory.BeginRecovery(ctx, firstRecovery)
		Expect(err).NotTo(HaveOccurred())
		Expect(retried).To(Equal(second))
		changed := firstRecovery
		changed.MaterializationID = "ambiguous-materialization"
		_, err = factory.BeginRecovery(ctx, changed)
		Expect(errors.Is(err, checkpoint.ErrConflict)).To(BeTrue())

		_, err = factory.MarkInterrupted(ctx, checkpoint.MarkAttemptInterruptedRequest{
			Identity: identity, ExecutionAttempt: 2, Reason: checkpoint.InterruptionEvicted,
		})
		Expect(err).NotTo(HaveOccurred())
		thirdRequest := checkpoint.BeginRecoveryRequest{
			Identity: identity, SourceExecutionAttempt: 2, Mode: checkpoint.FallbackCheckpointZero,
			Reason: checkpoint.InterruptionEvicted, MaterializationID: "materialization-3",
		}
		third, err := factory.BeginRecovery(ctx, thirdRequest)
		Expect(err).NotTo(HaveOccurred())
		Expect(third.ExecutionAttempt).To(Equal(3))

		_, err = factory.MarkInterrupted(ctx, checkpoint.MarkAttemptInterruptedRequest{
			Identity: identity, ExecutionAttempt: 3, Reason: checkpoint.InterruptionPodDeleted,
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = factory.BeginRecovery(ctx, checkpoint.BeginRecoveryRequest{
			Identity: identity, SourceExecutionAttempt: 3, Mode: checkpoint.FallbackCheckpointZero,
			Reason: checkpoint.InterruptionPodDeleted, MaterializationID: "materialization-4",
		})
		Expect(errors.Is(err, checkpoint.ErrAttemptLimit)).To(BeTrue())

		current, found, err := factory.Current(ctx, identity)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(current.ExecutionAttempt).To(Equal(3))
	})

	It("keeps manual-review terminal authority queryable after interruption", func() {
		allocate(1)
		_, err := factory.MarkInterrupted(ctx, checkpoint.MarkAttemptInterruptedRequest{
			Identity: identity, ExecutionAttempt: 1, Reason: checkpoint.InterruptionPodDeleted,
		})
		Expect(err).NotTo(HaveOccurred())

		request := checkpoint.MarkAttemptManualReviewRequest{
			Identity: identity, ExecutionAttempt: 1, ExpectedState: checkpoint.AttemptInterrupted,
		}
		manual, err := factory.MarkManualReview(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(manual.State).To(Equal(checkpoint.AttemptManualReview))
		Expect(manual.TerminalAt).NotTo(BeNil())
		retried, err := factory.MarkManualReview(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(retried).To(Equal(manual))

		current, found, err := factory.Current(ctx, identity)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(current).To(Equal(manual))
		_, err = factory.BeginRecovery(ctx, checkpoint.BeginRecoveryRequest{
			Identity: identity, SourceExecutionAttempt: 1, Mode: checkpoint.FallbackCheckpointZero,
			Reason: checkpoint.InterruptionPodDeleted, MaterializationID: "materialization-2",
		})
		Expect(errors.Is(err, checkpoint.ErrConflict)).To(BeTrue())
	})

	It("does not terminalize an interrupted attempt under an already-terminal head", func() {
		allocate(1)
		_, err := factory.MarkInterrupted(ctx, checkpoint.MarkAttemptInterruptedRequest{
			Identity: identity, ExecutionAttempt: 1, Reason: checkpoint.InterruptionPodDeleted,
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`
			UPDATE agent_run_checkpoint_heads
			SET active = FALSE, terminal_at = clock_timestamp()
			WHERE build_id = $1 AND plan_id = $2 AND function_id = $3
		`, identity.BuildID, identity.PlanID, identity.FunctionID)
		Expect(err).NotTo(HaveOccurred())

		_, err = factory.MarkManualReview(ctx, checkpoint.MarkAttemptManualReviewRequest{
			Identity: identity, ExecutionAttempt: 1, ExpectedState: checkpoint.AttemptInterrupted,
		})
		Expect(errors.Is(err, checkpoint.ErrConflict)).To(BeTrue())
		current, found, err := factory.Current(ctx, identity)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(current.State).To(Equal(checkpoint.AttemptInterrupted),
			"a failed terminal transition must not partially mutate the attempt")
	})

	It("does not return a current attempt for an unknown identity", func() {
		current, found, err := factory.Current(ctx, identity)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
		Expect(current).To(Equal(checkpoint.Attempt{}))

		_ = staleToken
	})
})
