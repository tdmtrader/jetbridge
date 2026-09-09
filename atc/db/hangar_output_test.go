package db_test

import (
	"context"
	"fmt"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// These are the races the lock suffix exists for, run against real PostgreSQL
// with real concurrent transactions and real arrival-order inversion.
//
// A test that only proves "no deadlock" would pass against a schema that let
// both sides win, so every case here also names the typed loser and asserts
// that nothing the loser was going to hand a consumer -- a claim, a read lease,
// a binding -- is visible afterwards.
var _ = Describe("the Hangar output lock suffix", func() {
	var (
		ctx        context.Context
		repository *db.HangarOutputRepository
		consumer   db.HangarConsumerPrefix
	)

	BeforeEach(func() {
		ctx = context.Background()

		// The suite runs on one pooled connection so that code needing a second
		// one deadlocks visibly. These specs need three on purpose: two racing
		// transactions and a third to read pg_locks while both are open. It
		// goes back to one afterwards.
		dbConn.SetMaxOpenConns(3)
		DeferCleanup(func() { dbConn.SetMaxOpenConns(1) })

		var err error
		consumer, err = db.HangarConsumerPrefixHeld("test-consumer")
		Expect(err).NotTo(HaveOccurred())
		repository = db.NewHangarOutputRepository(consumer)
	})

	// One activation epoch and one fresh, safe policy attestation: without
	// both, nothing in the plane admits anything, which is the held state the
	// migration leaves behind.
	activate := func() {
		GinkgoHelper()
		_, err := dbConn.Exec(`
			INSERT INTO hangar_output_activation_epochs
				(epoch_id, base_state, output_state, base_attestation, output_attestation,
				 receipt_public_key_id, receipt_key_valid_from, receipt_key_valid_until,
				 materialization_key_id, bucket_fingerprint, derived_namespace)
			VALUES (1, 'enabled', 'enabled', '{}', '{}', 'receipt-key-1',
				now() - interval '1 day', now() + interval '30 days',
				'materialize-key-1', 'gs://output-bucket', 'deployment/ns')`)
		Expect(err).NotTo(HaveOccurred())

		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(tx)
		Expect(repository.RecordPolicySnapshot(ctx, tx, output.PolicySnapshot{
			ProtocolVersion:      output.ProtocolVersion,
			ActivationEpoch:      1,
			BucketFingerprint:    "gs://output-bucket",
			Metageneration:       3,
			PolicyHash:           "policy-hash-1",
			LifecycleDeleteRules: 0,
			State:                output.PolicySafe,
			ObservedAt:           output.NewTimestamp(time.Now()),
		})).To(Succeed())
		Expect(tx.Commit()).To(Succeed())
	}

	identity := func() executioncontrol.Identity {
		return executioncontrol.Identity{
			ExecutionID: executioncontrol.ExecutionID(uuid.NewString()),
			Fence:       1,
		}
	}

	holdFor := func(handoff output.HandoffID, lease output.SourceLeaseID, execution executioncontrol.Identity, name output.OutputName) output.CaptureAcknowledgement {
		return output.CaptureAcknowledgement{
			ProtocolVersion: output.ProtocolVersion,
			Kind:            output.CaptureHoldAcknowledged,
			Execution:       execution,
			ActivationEpoch: 1,
			LedgerSequence:  1,
			NodeUID:         "node-uid",
			HandoffID:       handoff,
			SourceLeaseID:   lease,
			Incarnation: output.SourceIncarnation{
				ExecutionID:      execution.ExecutionID,
				NodeUID:          "node-uid",
				HandleGeneration: 1,
				Output:           name,
			},
			ObservedAt: output.NewTimestamp(time.Now()),
			Signature:  "signature",
		}
	}

	finishFor := func(execution executioncontrol.Identity) executioncontrol.Acknowledgement {
		return executioncontrol.Acknowledgement{
			ProtocolVersion: executioncontrol.ProtocolVersion,
			Kind:            executioncontrol.AcknowledgementFinish,
			Identity:        execution,
			ActivationEpoch: 1,
			LedgerSequence:  2,
			NodeUID:         "node-uid",
			ProcessIdentity: "process-1",
			ObservedAt:      output.NewTimestamp(time.Now()),
			Outcome:         &executioncontrol.ExitOutcome{ExitCode: 0},
			Signature:       "signature",
		}
	}

	// The one-use stat challenge the daemon would have been issued, for one
	// exact ref. Written as SQL because minting it is not the repository's job.
	issueChallenge := func(handoff output.HandoffID, reservation output.ReservationID, ref hangar.TreeRef) string {
		GinkgoHelper()

		nonce := "nonce-" + uuid.NewString()
		_, err := dbConn.Exec(`
			INSERT INTO hangar_receipt_stat_challenges
				(nonce, handoff_id, reservation_id, activation_epoch, receipt_public_key_id,
				 scope, digest, generation, capture_fence, not_after)
			VALUES ($1, $2, $3, 1, 'receipt-key-1', $4, $5, $6, 1, now() + interval '5 minutes')`,
			nonce, string(handoff), string(reservation), string(ref.Scope), string(ref.Digest),
			ref.Generation)
		Expect(err).NotTo(HaveOccurred())

		return nonce
	}

	// One receipt admission, in one place, because a second spelling of it is
	// a second set of facts and the guards under test are exactly about facts
	// agreeing.
	admissionFor := func(handoff output.HandoffID, execution executioncontrol.Identity, reservation output.ReservationID, ref hangar.TreeRef, nonce string) output.ReceiptAdmission {
		name := output.OutputName("result")

		return output.ReceiptAdmission{
			ProtocolVersion: output.ProtocolVersion,
			Receipt: output.Receipt{
				Claims: output.ReceiptClaims{
					ProtocolVersion:      output.ProtocolVersion,
					ReceiptVersion:       output.ReceiptDomain,
					Execution:            execution,
					ActivationEpoch:      1,
					HandoffID:            handoff,
					ProducerCheckpointID: output.OpaqueID("checkpoint-" + string(handoff)),
					ReservationID:        reservation,
					Incarnation: output.SourceIncarnation{
						ExecutionID:      execution.ExecutionID,
						NodeUID:          "node-uid",
						HandleGeneration: 1,
						Output:           name,
					},
					Output:        name,
					CaptureFence:  1,
					WriterFence:   1,
					Ref:           ref,
					Attributes:    output.AttributesFromFoundation(hangar.TreeAttributes{Ref: ref, StoredBytes: 2048, LogicalBytes: 4096, CreatedAt: time.Now()}),
					MarkerVersion: output.MarkerVersion,
					SignedAt:      output.NewTimestamp(time.Now()),
				},
				KeyID:     "receipt-key-1",
				Algorithm: output.ReceiptAlgorithm,
				Signature: "signature",
			},
			ChallengeNonce: nonce,
			Metageneration: 1,
			AdmittedAt:     output.NewTimestamp(time.Now()),
		}
	}

	// publish drives one whole capture, from predeclaration to a registered
	// receipt, through the repository rather than around it: what is under test
	// is the seam, and a fixture that wrote the rows itself would be testing
	// the schema twice and the repository not at all.
	publish := func(digest hangar.Digest, generation int64) (output.ReservationID, hangar.TreeRef) {
		GinkgoHelper()

		handoff := output.HandoffID(uuid.NewString())
		lease := output.SourceLeaseID(uuid.NewString())
		execution := identity()
		name := output.OutputName("result")
		deadline := output.NewTimestamp(time.Now().Add(24 * time.Hour))

		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(tx)

		Expect(repository.PredeclareHandoff(ctx, tx, output.CaptureAdmission{
			ProtocolVersion: output.ProtocolVersion,
			Execution:       execution,
			ActivationEpoch: 1,
			HandoffID:       handoff,
			SourceLeaseID:   lease,
			Output:          name,
			CaptureDeadline: deadline,
		})).To(Succeed())
		Expect(repository.AcknowledgeSourceHold(ctx, tx, holdFor(handoff, lease, execution, name))).
			To(Succeed())

		reservation, err := repository.CommitCaptureReservation(ctx, tx,
			output.SuccessfulFinishDisposition{
				ProtocolVersion:       output.ProtocolVersion,
				Disposition:           output.DispositionCapture,
				Execution:             execution,
				ActivationEpoch:       1,
				HandoffID:             handoff,
				SourceLeaseID:         lease,
				ProducerCheckpointID:  output.OpaqueID("checkpoint-" + string(handoff)),
				Output:                name,
				CaptureFence:          1,
				CaptureDeadline:       deadline,
				FinishAcknowledgement: finishFor(execution),
			})
		Expect(err).NotTo(HaveOccurred())

		_, err = repository.AcquireCaptureLease(ctx, tx, reservation, uuid.NewString(),
			output.MinLeaseTerm)
		Expect(err).NotTo(HaveOccurred())

		Expect(repository.ResolveLogicalReservation(ctx, tx, output.LogicalResolution{
			ProtocolVersion: output.ProtocolVersion,
			Execution:       execution,
			ActivationEpoch: 1,
			HandoffID:       handoff,
			ReservationID:   reservation,
			CaptureFence:    1,
			Scope:           "team-a",
			Digest:          digest,
			LogicalBytes:    4096,
			ResolvedAt:      output.NewTimestamp(time.Now()),
		})).To(Succeed())
		Expect(repository.RecordFirstObjectCreate(ctx, tx, reservation)).To(Succeed())
		Expect(tx.Commit()).To(Succeed())

		ref := hangar.TreeRef{Scope: "team-a", Digest: digest, Generation: generation}
		nonce := issueChallenge(handoff, reservation, ref)

		tx, err = dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(tx)
		Expect(repository.RegisterReceipt(ctx, tx,
			admissionFor(handoff, execution, reservation, ref, nonce))).To(Succeed())
		Expect(tx.Commit()).To(Succeed())

		return reservation, ref
	}

	acquire := func(tx db.Tx, id output.ClaimID, ref hangar.TreeRef, binding string) error {
		return repository.AcquireClaim(ctx, tx, output.ClaimAcquisition{
			ProtocolVersion:   output.ProtocolVersion,
			ClaimID:           id,
			Ref:               ref,
			ConsumerBindingID: output.OpaqueID(binding),
			RequestedAt:       output.NewTimestamp(time.Now()),
		})
	}

	// A second generation of one correlation, recorded the way inventory
	// records a marked orphan it found in the deployment's own bucket.
	adopt := func(ref hangar.TreeRef) error {
		GinkgoHelper()

		tx, err := dbConn.Begin()
		if err != nil {
			return err
		}
		defer db.Rollback(tx)
		if err := repository.AdoptManagedOrphan(ctx, tx, ref, 1, 1); err != nil {
			return err
		}

		return tx.Commit()
	}

	countActiveClaims := func(ref hangar.TreeRef) int {
		GinkgoHelper()
		var count int
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM hangar_claims c
			JOIN hangar_exact_lifecycles l ON l.id = c.lifecycle_id
			WHERE l.scope = $1 AND l.digest = $2 AND l.generation = $3 AND c.released_at IS NULL`,
			string(ref.Scope), string(ref.Digest), ref.Generation).Scan(&count)).To(Succeed())

		return count
	}

	lifecycleState := func(ref hangar.TreeRef) string {
		GinkgoHelper()
		var state string
		Expect(dbConn.QueryRow(`
			SELECT state FROM hangar_exact_lifecycles
			WHERE scope = $1 AND digest = $2 AND generation = $3`,
			string(ref.Scope), string(ref.Digest), ref.Generation).Scan(&state)).To(Succeed())

		return state
	}

	Describe("claimant versus reclaimer", func() {
		var ref hangar.TreeRef

		BeforeEach(func() {
			activate()
			_, ref = publish(hangarDigest(1), 1725830823000001)
		})

		// Claimant first: the reclaimer must recheck under the lock and skip.
		It("lets a claimant that arrives first make the reclaimer skip", func() {
			claimant, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(claimant)

			claimID := output.ClaimID(uuid.NewString())
			Expect(acquire(claimant, claimID, ref, "binding-1")).To(Succeed())
			Expect(claimant.Commit()).To(Succeed())

			reclaimer, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(reclaimer)

			err = repository.AdmitReclaim(ctx, reclaimer, ref, uuid.NewString(), 1, output.MinLeaseTerm)
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(err.Error()).To(ContainSubstring("1 claim(s)"))
			Expect(reclaimer.Rollback()).To(Succeed())

			Expect(countActiveClaims(ref)).To(Equal(1))
			Expect(lifecycleState(ref)).To(Equal("registered"))
		})

		// Reclaimer first: the claimant gets a typed lifecycle conflict and its
		// whole transaction rolls back, so no binding and no claim survive.
		It("makes a claimant that arrives second roll back with no claim", func() {
			reclaimer, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(reclaimer)

			Expect(repository.AdmitReclaim(ctx, reclaimer, ref, uuid.NewString(), 1,
				output.MinLeaseTerm)).To(Succeed())
			Expect(reclaimer.Commit()).To(Succeed())

			claimant, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(claimant)

			claimID := output.ClaimID(uuid.NewString())
			err = acquire(claimant, claimID, ref, "binding-1")
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(err.Error()).To(ContainSubstring("reclaiming"))
			Expect(claimant.Rollback()).To(Succeed())

			Expect(countActiveClaims(ref)).To(BeZero())
			Expect(lifecycleState(ref)).To(Equal("reclaiming"))
		})

		// The genuinely concurrent case: both transactions open, both past
		// their consumer prefix, and the second one blocks on the exact
		// lifecycle row until the first commits. Exactly one wins, and which
		// one is decided by arrival rather than by luck.
		It("serializes two open transactions on the exact lifecycle row", func() {
			claimant, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(claimant)

			claimID := output.ClaimID(uuid.NewString())
			Expect(acquire(claimant, claimID, ref, "binding-1")).To(Succeed())

			reclaimed := make(chan error, 1)
			go func() {
				defer GinkgoRecover()
				reclaimer, err := dbConn.Begin()
				if err != nil {
					reclaimed <- err

					return
				}
				defer db.Rollback(reclaimer)
				err = repository.AdmitReclaim(ctx, reclaimer, ref, uuid.NewString(), 1,
					output.MinLeaseTerm)
				if err == nil {
					err = reclaimer.Commit()
				}
				reclaimed <- err
			}()

			// The reclaimer is blocked on the lifecycle row this transaction
			// holds. Nothing it could do would let it through, which is the
			// property: this is a lock, not a retry window.
			Consistently(reclaimed, 500*time.Millisecond).ShouldNot(Receive())

			Expect(claimant.Commit()).To(Succeed())

			var reclaimErr error
			Eventually(reclaimed, 10*time.Second).Should(Receive(&reclaimErr))
			Expect(reclaimErr).To(MatchError(output.ErrConflict))

			Expect(countActiveClaims(ref)).To(Equal(1))
			Expect(lifecycleState(ref)).To(Equal("registered"))
		})
	})

	Describe("reader versus reclaimer", func() {
		var ref hangar.TreeRef

		BeforeEach(func() {
			activate()
			_, ref = publish(hangarDigest(2), 1725830823000002)
		})

		// Reclaim admission is refused while any read lease is active, even
		// after the domain releases its last claim: releasing the last claim
		// during a transfer must not delete the bytes out from under a reader.
		It("refuses reclaim while a read lease outlives the last claim", func() {
			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)

			claimID := output.ClaimID(uuid.NewString())
			Expect(acquire(tx, claimID, ref, "binding-1")).To(Succeed())
			lease, err := repository.AcquireReadLease(ctx, tx, output.ReadLeaseRequest{
				ReadLeaseID:            output.ReadLeaseID(uuid.NewString()),
				ClaimID:                claimID,
				Ref:                    ref,
				ActivationEpoch:        1,
				RequestedAt:            output.NewTimestamp(time.Now()),
				MaterializationTimeout: 10 * time.Minute,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(repository.ReleaseClaim(ctx, tx, output.ClaimRelease{
				ProtocolVersion: output.ProtocolVersion,
				ClaimID:         claimID,
				Ref:             ref,
				RequestedAt:     output.NewTimestamp(time.Now()),
			})).To(Succeed())
			Expect(tx.Commit()).To(Succeed())

			Expect(countActiveClaims(ref)).To(BeZero())

			reclaimer, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(reclaimer)
			err = repository.AdmitReclaim(ctx, reclaimer, ref, uuid.NewString(), 1, output.MinLeaseTerm)
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(err.Error()).To(ContainSubstring("1 read lease(s)"))
			Expect(reclaimer.Rollback()).To(Succeed())

			// And once the reader closes, the same admission succeeds -- so the
			// refusal above was the lease and not something incidental.
			closing, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(closing)
			Expect(repository.ReleaseReadLease(ctx, closing, lease)).To(Succeed())
			Expect(closing.Commit()).To(Succeed())

			reclaimer, err = dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(reclaimer)
			Expect(repository.AdmitReclaim(ctx, reclaimer, ref, uuid.NewString(), 1,
				output.MinLeaseTerm)).To(Succeed())
			Expect(reclaimer.Commit()).To(Succeed())
		})

		It("refuses a grant for a ref that is already reclaiming", func() {
			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			claimID := output.ClaimID(uuid.NewString())
			Expect(acquire(tx, claimID, ref, "binding-1")).To(Succeed())
			Expect(repository.ReleaseClaim(ctx, tx, output.ClaimRelease{
				ProtocolVersion: output.ProtocolVersion,
				ClaimID:         claimID,
				Ref:             ref,
				RequestedAt:     output.NewTimestamp(time.Now()),
			})).To(Succeed())
			Expect(tx.Commit()).To(Succeed())

			reclaimer, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(reclaimer)
			Expect(repository.AdmitReclaim(ctx, reclaimer, ref, uuid.NewString(), 1,
				output.MinLeaseTerm)).To(Succeed())
			Expect(reclaimer.Commit()).To(Succeed())

			reader, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(reader)
			_, err = repository.AcquireReadLease(ctx, reader, output.ReadLeaseRequest{
				ReadLeaseID:            output.ReadLeaseID(uuid.NewString()),
				ClaimID:                claimID,
				Ref:                    ref,
				ActivationEpoch:        1,
				RequestedAt:            output.NewTimestamp(time.Now()),
				MaterializationTimeout: 10 * time.Minute,
			})
			Expect(err).To(HaveOccurred())
			Expect(reader.Rollback()).To(Succeed())
		})
	})

	Describe("receipt registration versus claim acquire", func() {
		It("refuses a claim on a ref no receipt has registered, and admits it once one has", func() {
			activate()
			ref := hangar.TreeRef{
				Scope:      "team-a",
				Digest:     hangarDigest(3),
				Generation: 1725830823000003,
			}

			early, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(early)
			err = acquire(early, output.ClaimID(uuid.NewString()), ref, "binding-1")
			Expect(err).To(MatchError(output.ErrNotFound))
			Expect(early.Rollback()).To(Succeed())

			_, published := publish(hangarDigest(3), 1725830823000003)
			Expect(published).To(Equal(ref))

			late, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(late)
			Expect(acquire(late, output.ClaimID(uuid.NewString()), ref, "binding-1")).To(Succeed())
			Expect(late.Commit()).To(Succeed())
			Expect(countActiveClaims(ref)).To(Equal(1))
		})
	})

	Describe("adoption versus claimant", func() {
		It("refuses to adopt a correlation an unresolved reservation still protects", func() {
			activate()

			// A capture that has resolved its logical reservation but not yet
			// registered a generation: exactly the gap inventory must not
			// adopt into.
			handoff := output.HandoffID(uuid.NewString())
			lease := output.SourceLeaseID(uuid.NewString())
			execution := identity()
			deadline := output.NewTimestamp(time.Now().Add(24 * time.Hour))

			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			Expect(repository.PredeclareHandoff(ctx, tx, output.CaptureAdmission{
				ProtocolVersion: output.ProtocolVersion,
				Execution:       execution,
				ActivationEpoch: 1,
				HandoffID:       handoff,
				SourceLeaseID:   lease,
				Output:          "result",
				CaptureDeadline: deadline,
			})).To(Succeed())
			Expect(repository.AcknowledgeSourceHold(ctx, tx,
				holdFor(handoff, lease, execution, "result"))).To(Succeed())
			reservation, err := repository.CommitCaptureReservation(ctx, tx,
				output.SuccessfulFinishDisposition{
					ProtocolVersion:       output.ProtocolVersion,
					Disposition:           output.DispositionCapture,
					Execution:             execution,
					ActivationEpoch:       1,
					HandoffID:             handoff,
					SourceLeaseID:         lease,
					ProducerCheckpointID:  "checkpoint-orphan",
					Output:                "result",
					CaptureFence:          1,
					CaptureDeadline:       deadline,
					FinishAcknowledgement: finishFor(execution),
				})
			Expect(err).NotTo(HaveOccurred())
			_, err = repository.AcquireCaptureLease(ctx, tx, reservation, uuid.NewString(),
				output.MinLeaseTerm)
			Expect(err).NotTo(HaveOccurred())
			Expect(repository.ResolveLogicalReservation(ctx, tx, output.LogicalResolution{
				ProtocolVersion: output.ProtocolVersion,
				Execution:       execution,
				ActivationEpoch: 1,
				HandoffID:       handoff,
				ReservationID:   reservation,
				CaptureFence:    1,
				Scope:           "team-a",
				Digest:          hangarDigest(4),
				LogicalBytes:    4096,
				ResolvedAt:      output.NewTimestamp(time.Now()),
			})).To(Succeed())
			Expect(tx.Commit()).To(Succeed())

			orphan := hangar.TreeRef{
				Scope:      "team-a",
				Digest:     hangarDigest(4),
				Generation: 1725830823000004,
			}

			inventory, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(inventory)
			err = repository.AdoptManagedOrphan(ctx, inventory, orphan, 1, 1)
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(err.Error()).To(ContainSubstring("unresolved reservation"))
			Expect(inventory.Rollback()).To(Succeed())

			// A claimant cannot see it either: no consumer result appears in
			// the gap.
			claimant, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(claimant)
			Expect(acquire(claimant, output.ClaimID(uuid.NewString()), orphan, "binding-1")).
				To(MatchError(output.ErrNotFound))
			Expect(claimant.Rollback()).To(Succeed())
		})
	})

	Describe("exact-generation replacement", func() {
		// Two captures of identical content deduplicate to one generation or
		// produce two; either way a claim names one exact generation and never
		// floats to the other.
		It("keeps a claim on the generation it named when another is published", func() {
			activate()
			_, first := publish(hangarDigest(5), 1725830823000005)
			_, second := publish(hangarDigest(5), 1725830823000006)
			Expect(first).NotTo(Equal(second))

			claimID := output.ClaimID(uuid.NewString())
			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			Expect(acquire(tx, claimID, first, "binding-1")).To(Succeed())
			Expect(tx.Commit()).To(Succeed())

			// The same identity on the replacement generation is a conflict,
			// not a move.
			moving, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(moving)
			Expect(acquire(moving, claimID, second, "binding-1")).To(MatchError(output.ErrConflict))
			Expect(moving.Rollback()).To(Succeed())

			Expect(countActiveClaims(first)).To(Equal(1))
			Expect(countActiveClaims(second)).To(BeZero())

			// And the replacement can be reclaimed while the first is claimed,
			// which is the point of keying protection on the exact generation.
			reclaimer, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(reclaimer)
			Expect(repository.AdmitReclaim(ctx, reclaimer, second, uuid.NewString(), 1,
				output.MinLeaseTerm)).To(Succeed())
			Expect(reclaimer.Commit()).To(Succeed())

			Expect(lifecycleState(first)).To(Equal("registered"))
			Expect(lifecycleState(second)).To(Equal("reclaiming"))
		})
	})

	Describe("unknown-ref derivation", func() {
		// A caller that does not yet know the digest reads the facts without
		// row locks, takes the suffix, and then revalidates. If the facts moved
		// in between, that is a typed retry -- nothing is wrong, another actor
		// legitimately advanced the row while this one was choosing what to
		// lock.
		It("returns a typed retry when a derived fact changes under the locks", func() {
			activate()
			reservation, _ := publish(hangarDigest(7), 1725830823000007)

			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)

			derived, err := db.DeriveHangarRefUnlocked(ctx, tx, reservation)
			Expect(err).NotTo(HaveOccurred())
			Expect(derived.Resolved).To(BeTrue())
			Expect(derived.Logical.Digest).To(Equal(hangarDigest(7)))

			locks, err := db.LockHangarSuffix(ctx, tx, consumer, db.HangarLockRequest{
				Logical:  []db.HangarLogicalKey{derived.Logical},
				Captures: []output.ReservationID{reservation},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(locks.RevalidateDerivation(ctx, tx, derived)).To(Succeed())

			// The fact the derivation rested on, moved.
			stale := derived
			stale.CaptureFence = derived.CaptureFence + 1
			err = locks.RevalidateDerivation(ctx, tx, stale)
			Expect(err).To(MatchError(db.ErrHangarLockRetry))
			Expect(err.Error()).To(ContainSubstring("superseded"))
		})

		It("refuses to register a receipt whose reservation resolved elsewhere", func() {
			activate()
			reservation, _ := publish(hangarDigest(8), 1725830823000008)

			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			derived, err := db.DeriveHangarRefUnlocked(ctx, tx, reservation)
			Expect(err).NotTo(HaveOccurred())
			Expect(derived.Logical.Digest).To(Equal(hangarDigest(8)))
			Expect(tx.Rollback()).To(Succeed())
		})
	})

	Describe("a second receipt for one reservation", func() {
		// An ambiguous create response converges only through verified
		// per-capture retry (AC 9), and reuse for different facts conflicts
		// (Req 6). A second receipt naming another generation is that reuse:
		// accepting it silently leaves two lifecycle rows for one capture, one
		// of them `registered` with no receipt naming it, correlated to
		// nothing and reclaim-eligible once its grace passes.
		lifecyclesFor := func(digest hangar.Digest) int {
			GinkgoHelper()
			var count int
			Expect(dbConn.QueryRow(
				`SELECT count(*) FROM hangar_exact_lifecycles WHERE digest = $1`,
				string(digest)).Scan(&count)).To(Succeed())

			return count
		}
		unnamedRegistrations := func() int {
			GinkgoHelper()
			var count int
			Expect(dbConn.QueryRow(`
				SELECT count(*) FROM hangar_exact_lifecycles l
				WHERE l.state = 'registered'
				  AND NOT EXISTS (
					SELECT 1 FROM hangar_output_receipts r WHERE r.lifecycle_id = l.id)`).
				Scan(&count)).To(Succeed())

			return count
		}

		It("refuses another generation and leaves no lifecycle no receipt names", func() {
			activate()
			reservation, first := publish(hangarDigest(12), 1725830823000012)

			var handoffID, executionID string
			Expect(dbConn.QueryRow(`
				SELECT handoff_id, execution_id FROM hangar_capture_reservations
				WHERE reservation_id = $1`, string(reservation)).
				Scan(&handoffID, &executionID)).To(Succeed())

			handoff := output.HandoffID(handoffID)
			execution := executioncontrol.Identity{
				ExecutionID: executioncontrol.ExecutionID(executionID),
				Fence:       1,
			}
			second := first
			second.Generation = first.Generation + 1

			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			registerErr := repository.RegisterReceipt(ctx, tx, admissionFor(handoff, execution,
				reservation, second, issueChallenge(handoff, reservation, second)))
			if registerErr == nil {
				// Committing is what a caller told "no error" would do, and it
				// is what makes the damage countable below.
				Expect(tx.Commit()).To(Succeed())
			} else {
				Expect(tx.Rollback()).To(Succeed())
			}

			Expect(registerErr).To(MatchError(output.ErrConflict))
			Expect(registerErr.Error()).To(ContainSubstring("already registered"))
			Expect(lifecyclesFor(first.Digest)).To(Equal(1),
				"a second generation was recorded for one reservation")
			Expect(unnamedRegistrations()).To(BeZero(),
				"a registered lifecycle exists that no receipt names")
		})

		It("is idempotent for the same reservation and the same ref", func() {
			activate()
			reservation, ref := publish(hangarDigest(13), 1725830823000013)

			var handoffID, executionID string
			Expect(dbConn.QueryRow(`
				SELECT handoff_id, execution_id FROM hangar_capture_reservations
				WHERE reservation_id = $1`, string(reservation)).
				Scan(&handoffID, &executionID)).To(Succeed())

			handoff := output.HandoffID(handoffID)
			execution := executioncontrol.Identity{
				ExecutionID: executioncontrol.ExecutionID(executionID),
				Fence:       1,
			}

			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			Expect(repository.RegisterReceipt(ctx, tx, admissionFor(handoff, execution,
				reservation, ref, issueChallenge(handoff, reservation, ref)))).To(Succeed())
			Expect(tx.Commit()).To(Succeed())

			Expect(lifecyclesFor(ref.Digest)).To(Equal(1))
			Expect(unnamedRegistrations()).To(BeZero())
		})
	})

	// AC 11's two clauses that inverting which actor arrives first does not
	// cover.
	Describe("AC 11", func() {
		// The property is the *input* order, and it is proved by making the
		// helper block: a holder takes the key that sorts second, the helper is
		// handed the batch reversed, and a third connection asks with NOWAIT
		// whether the key that sorts first is held. A helper that locked in the
		// order it was given would have blocked on the second key immediately
		// and never reached the first, so the NOWAIT probe would succeed.
		//
		// The earlier version of this ran two concurrent transactions and
		// asserted neither deadlocked. It could not fail: two goroutines do not
		// interleave at statement granularity often enough to make an unsorted
		// helper deadlock, so the spec passed with sorting removed.
		blockedProbe := func(query string, args ...any) func() error {
			return func() error {
				probe, err := dbConn.Begin()
				if err != nil {
					return err
				}
				defer db.Rollback(probe)
				_, err = probe.Exec(query, args...)

				return err
			}
		}

		It("locks logical rows in sorted order when the batch arrives reversed", func() {
			activate()
			dbConn.SetMaxOpenConns(4)

			_, a := publish(hangarDigest(9), 1725830823000009)
			_, b := publish(hangarDigest(10), 1725830823000010)
			low, high := a, b
			if string(b.Digest) < string(a.Digest) {
				low, high = b, a
			}

			const lockLogical = `
				SELECT 1 FROM hangar_logical_reservations
				WHERE scope = $1 AND digest = $2
				FOR NO KEY UPDATE`
			probeFirst := blockedProbe(lockLogical+" NOWAIT", string(low.Scope), string(low.Digest))

			// Nobody holds the key that sorts first, yet. Without this the probe
			// below could be failing for a reason that has nothing to do with
			// the helper.
			Expect(probeFirst()).To(Succeed())

			holder, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(holder)
			_, err = holder.Exec(lockLogical, string(high.Scope), string(high.Digest))
			Expect(err).NotTo(HaveOccurred())

			done := make(chan error, 1)
			locked := make(chan db.HangarLocks, 1)
			go func() {
				defer GinkgoRecover()
				tx, err := dbConn.Begin()
				if err != nil {
					done <- err

					return
				}
				defer db.Rollback(tx)
				locks, err := db.LockHangarSuffix(ctx, tx, consumer, db.HangarLockRequest{
					Logical: []db.HangarLogicalKey{
						{Scope: high.Scope, Digest: high.Digest},
						// The duplicate is deliberate: a batch that names one
						// correlation twice must lock it once.
						{Scope: high.Scope, Digest: high.Digest},
						{Scope: low.Scope, Digest: low.Digest},
					},
				})
				if err != nil {
					done <- err

					return
				}
				locked <- locks
				done <- nil
			}()

			Eventually(probeFirst, 10*time.Second, 50*time.Millisecond).Should(
				MatchError(ContainSubstring("55P03")),
				"the key that sorts first was never locked while the helper blocked on the key "+
					"that sorts second, so the helper took the batch in the order it was handed")

			Expect(holder.Rollback()).To(Succeed())

			var completed error
			Eventually(done, 10*time.Second).Should(Receive(&completed))
			Expect(completed).NotTo(HaveOccurred())

			var locks db.HangarLocks
			Expect(locked).To(Receive(&locks))
			Expect(locks.Logical).To(HaveLen(2), "the duplicated correlation was locked twice")
		})

		It("locks exact rows in sorted order when the batch arrives reversed", func() {
			activate()
			dbConn.SetMaxOpenConns(4)

			// One correlation, two generations, so the class-2 order is decided
			// by generation and compared numerically: "9" sorts after "10" as
			// bytes, and a lock order that depends on how a number was spelled is
			// not an order.
			digest := hangarDigest(14)
			_, a := publish(digest, 9)
			second := hangar.TreeRef{Scope: a.Scope, Digest: digest, Generation: 10}
			Expect(adopt(second)).To(Succeed())

			low, high := a, second

			const lockExact = `
				SELECT id FROM hangar_exact_lifecycles
				WHERE scope = $1 AND digest = $2 AND generation = $3
				FOR NO KEY UPDATE`
			probeFirst := blockedProbe(lockExact+" NOWAIT",
				string(low.Scope), string(low.Digest), low.Generation)

			Expect(probeFirst()).To(Succeed())

			holder, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(holder)
			_, err = holder.Exec(lockExact,
				string(high.Scope), string(high.Digest), high.Generation)
			Expect(err).NotTo(HaveOccurred())

			done := make(chan error, 1)
			locked := make(chan db.HangarLocks, 1)
			go func() {
				defer GinkgoRecover()
				tx, err := dbConn.Begin()
				if err != nil {
					done <- err

					return
				}
				defer db.Rollback(tx)
				locks, err := db.LockHangarSuffix(ctx, tx, consumer, db.HangarLockRequest{
					Exact: []hangar.TreeRef{high, low, high},
				})
				if err != nil {
					done <- err

					return
				}
				locked <- locks
				done <- nil
			}()

			Eventually(probeFirst, 10*time.Second, 50*time.Millisecond).Should(
				MatchError(ContainSubstring("55P03")),
				"generation 9 was never locked while the helper blocked on generation 10, so the "+
					"helper took the batch in the order it was handed")

			Expect(holder.Rollback()).To(Succeed())

			var completed error
			Eventually(done, 10*time.Second).Should(Receive(&completed))
			Expect(completed).NotTo(HaveOccurred())

			var locks db.HangarLocks
			Expect(locked).To(Receive(&locks))
			Expect(locks.Exact).To(HaveLen(2), "the duplicated exact ref was locked twice")
			Expect(locks.Lifecycles).To(HaveLen(2))
		})

		It("acquires no lock on a consumer's own tables and inverts none it holds", func() {
			activate()
			_, ref := publish(hangarDigest(11), 1725830823000011)

			_, err := dbConn.Exec(`
				CREATE TABLE opaque_consumer_bindings (
					binding_id text PRIMARY KEY,
					visibility text NOT NULL,
					claim_id   uuid
				)`)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				_, err := dbConn.Exec(`DROP TABLE IF EXISTS opaque_consumer_bindings`)
				Expect(err).NotTo(HaveOccurred())
			})
			_, err = dbConn.Exec(`
				INSERT INTO opaque_consumer_bindings (binding_id, visibility)
				VALUES ('binding-1', 'hidden')`)
			Expect(err).NotTo(HaveOccurred())

			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)

			// The consumer's own prefix, taken by the consumer, in its own
			// tables, before it enters the suffix.
			_, err = tx.Exec(`SELECT 1 FROM opaque_consumer_bindings WHERE binding_id = $1 FOR UPDATE`,
				"binding-1")
			Expect(err).NotTo(HaveOccurred())

			before := hangarLockedRelations(tx)
			Expect(before).To(ContainElement("opaque_consumer_bindings"))

			Expect(acquire(tx, output.ClaimID(uuid.NewString()), ref, "binding-1")).To(Succeed())

			after := hangarLockedRelations(tx)

			// Hangar took locks -- on Hangar tables.
			Expect(after).To(ContainElement("hangar_exact_lifecycles"))

			// And exactly one consumer relation is locked, the one the consumer
			// locked itself: Hangar acquired none and inverted none.
			consumerLocks := 0
			for _, relation := range after {
				if relation == "opaque_consumer_bindings" {
					consumerLocks++
				}
			}
			Expect(consumerLocks).To(Equal(1),
				"Hangar acquired a lock on a consumer table; it never acquires a consumer-domain row")

			Expect(tx.Rollback()).To(Succeed())
		})
	})

	Describe("waking a worker", func() {
		// NOTIFY accelerates work; it is never how work is found. A failed one
		// is therefore a warning and nothing else: the transaction it announces
		// has already committed, there is nothing left to undo, and the work is
		// still picked up by the worker's periodic database-clock pass. So the
		// call returns nothing a caller could be tempted to roll back on.
		It("reports a failed notification without offering the caller an error", func() {
			logger := lagertest.NewTestLogger("hangar-output")

			Expect(func() {
				db.HangarOutputNotify(logger, dbConn, "1 this is not a channel name")
			}).NotTo(Panic())

			Expect(logger.Logs()).NotTo(BeEmpty())
			Expect(logger.LogMessages()).To(ContainElement(
				ContainSubstring("failed-to-notify-hangar-output-worker")))

			// And the connection is still usable, because nothing was rolled
			// back and nothing was left open.
			var one int
			Expect(dbConn.QueryRow(`SELECT 1`).Scan(&one)).To(Succeed())
			Expect(one).To(Equal(1))
		})

		It("wakes each worker channel it is given", func() {
			logger := lagertest.NewTestLogger("hangar-output")

			db.HangarOutputNotify(logger, dbConn,
				db.HangarOutputCaptureRecoveryChannel,
				db.HangarOutputReleaseChannel,
				db.HangarOutputInventoryChannel,
				db.HangarOutputReclaimChannel)

			Expect(logger.Logs()).To(BeEmpty(), "a valid channel was reported as failing")
		})
	})

	// Green: the product-neutral consumer that proves the seam composes.
	Describe("an opaque consumer", func() {
		var ref hangar.TreeRef

		BeforeEach(func() {
			activate()
			_, ref = publish(hangarDigest(12), 1725830823000012)

			_, err := dbConn.Exec(`
				CREATE TABLE opaque_consumer_bindings (
					binding_id text PRIMARY KEY,
					visibility text NOT NULL,
					claim_id   uuid
				)`)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				_, err := dbConn.Exec(`DROP TABLE IF EXISTS opaque_consumer_bindings`)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		binding := func(id string) (string, string) {
			GinkgoHelper()
			var visibility, claim string
			Expect(dbConn.QueryRow(`
				SELECT visibility, coalesce(claim_id::text, '')
				FROM opaque_consumer_bindings WHERE binding_id = $1`, id).
				Scan(&visibility, &claim)).To(Succeed())

			return visibility, claim
		}

		It("binds and acquires in one transaction, republishes without reacquiring, and unbinds beside the release", func() {
			claimID := output.ClaimID(uuid.NewString())

			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			_, err = tx.Exec(`
				INSERT INTO opaque_consumer_bindings (binding_id, visibility, claim_id)
				VALUES ('binding-1', 'hidden', $1)`, string(claimID))
			Expect(err).NotTo(HaveOccurred())
			Expect(acquire(tx, claimID, ref, "binding-1")).To(Succeed())
			Expect(tx.Commit()).To(Succeed())

			visibility, claim := binding("binding-1")
			Expect(visibility).To(Equal("hidden"))
			Expect(claim).To(Equal(string(claimID)))
			Expect(countActiveClaims(ref)).To(Equal(1))

			// Hidden to published, with no second acquire: the consumer keeps
			// the id, and Hangar neither replaces nor reacquires it.
			tx, err = dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			_, err = tx.Exec(`
				UPDATE opaque_consumer_bindings SET visibility = 'published' WHERE binding_id = 'binding-1'`)
			Expect(err).NotTo(HaveOccurred())
			Expect(tx.Commit()).To(Succeed())

			visibility, claim = binding("binding-1")
			Expect(visibility).To(Equal("published"))
			Expect(claim).To(Equal(string(claimID)))
			Expect(countActiveClaims(ref)).To(Equal(1))

			// Unbind and release together: neither a dangling visible binding
			// nor an indefinitely leaked claim is a valid crash outcome.
			tx, err = dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			_, err = tx.Exec(`
				UPDATE opaque_consumer_bindings SET visibility = 'unusable' WHERE binding_id = 'binding-1'`)
			Expect(err).NotTo(HaveOccurred())
			Expect(repository.ReleaseClaim(ctx, tx, output.ClaimRelease{
				ProtocolVersion: output.ProtocolVersion,
				ClaimID:         claimID,
				Ref:             ref,
				RequestedAt:     output.NewTimestamp(time.Now()),
			})).To(Succeed())
			Expect(tx.Commit()).To(Succeed())

			visibility, _ = binding("binding-1")
			Expect(visibility).To(Equal("unusable"))
			Expect(countActiveClaims(ref)).To(BeZero())
		})

		It("leaves neither half visible when the transaction is forced to roll back", func() {
			claimID := output.ClaimID(uuid.NewString())

			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			_, err = tx.Exec(`
				INSERT INTO opaque_consumer_bindings (binding_id, visibility, claim_id)
				VALUES ('binding-2', 'hidden', $1)`, string(claimID))
			Expect(err).NotTo(HaveOccurred())
			Expect(acquire(tx, claimID, ref, "binding-2")).To(Succeed())

			// The injected failure: the consumer's own write fails after both
			// halves are in the transaction.
			_, err = tx.Exec(`
				INSERT INTO opaque_consumer_bindings (binding_id, visibility) VALUES ('binding-2', 'hidden')`)
			Expect(err).To(HaveOccurred())
			Expect(tx.Rollback()).To(Succeed())

			var bindings int
			Expect(dbConn.QueryRow(`SELECT count(*) FROM opaque_consumer_bindings`).
				Scan(&bindings)).To(Succeed())
			Expect(bindings).To(BeZero(), "the consumer's binding survived a rolled-back transaction")
			Expect(countActiveClaims(ref)).To(BeZero(),
				"the Hangar claim survived a rolled-back transaction")

			var tombstones int
			Expect(dbConn.QueryRow(`SELECT count(*) FROM hangar_claims WHERE claim_id = $1`,
				string(claimID)).Scan(&tombstones)).To(Succeed())
			Expect(tombstones).To(BeZero())
		})

		It("refuses the consumer's own prefix being skipped", func() {
			_, err := db.HangarConsumerPrefixHeld("   ")
			Expect(err).To(MatchError(output.ErrIncomplete))

			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			_, err = db.LockHangarSuffix(ctx, tx, db.HangarConsumerPrefix{}, db.HangarLockRequest{
				Exact: []hangar.TreeRef{ref},
			})
			Expect(err).To(MatchError(output.ErrIncomplete))
			Expect(err.Error()).To(ContainSubstring("no consumer prefix token"))
		})
	})
})

// hangarDigest is a distinct valid sha256 digest per test.
func hangarDigest(n int) hangar.Digest {
	return hangar.Digest(fmt.Sprintf("sha256:%064d", n))
}

// hangarLockedRelations names every relation this transaction currently holds a
// row-level or table-level lock on.
func hangarLockedRelations(tx db.Tx) []string {
	GinkgoHelper()

	rows, err := tx.Query(`
		SELECT c.relname
		FROM pg_locks l
		JOIN pg_class c ON c.oid = l.relation
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE l.pid = pg_backend_pid()
		  AND n.nspname = 'public'
		  AND l.granted`)
	Expect(err).NotTo(HaveOccurred())
	defer rows.Close()

	var relations []string
	for rows.Next() {
		var name string
		Expect(rows.Scan(&name)).To(Succeed())
		relations = append(relations, name)
	}
	Expect(rows.Err()).NotTo(HaveOccurred())

	return relations
}
