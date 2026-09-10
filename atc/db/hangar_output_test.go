package db_test

import (
	"context"
	"crypto/rand"
	"database/sql"
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

	// A hold binds to an incarnation the daemon issued FIRST, and the schema
	// now says so: the reservation exists before the producing Pod does, and a
	// hold over no reservation is the Phase 4 seam -- a producer writing into a
	// directory nothing protects. Every hold below reserves first, because
	// every hold in production does.
	reserveFor := func(handoff output.HandoffID, lease output.SourceLeaseID, execution executioncontrol.Identity, name output.OutputName) output.ReservedIncarnation {
		return output.ReservedIncarnation{
			ProtocolVersion: output.ProtocolVersion,
			Execution:       execution,
			ActivationEpoch: 1,
			HandoffID:       handoff,
			SourceLeaseID:   lease,
			NodeUID:         "node-uid",
			Incarnation: output.SourceIncarnation{
				ExecutionID:      execution.ExecutionID,
				NodeUID:          "node-uid",
				HandleGeneration: 1,
				Output:           name,
			},
			Directory:      string(execution.ExecutionID) + ".1/" + string(name),
			LedgerSequence: 1,
			ObservedAt:     output.NewTimestamp(time.Now()),
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
	issueChallenge := func(handoff output.HandoffID, reservation output.ReservationID, ref hangar.TreeRef) (string, time.Time) {
		GinkgoHelper()

		nonce := "nonce-" + uuid.NewString()
		var issuedAt time.Time
		err := dbConn.QueryRow(`
			INSERT INTO hangar_receipt_stat_challenges
				(nonce, handoff_id, reservation_id, activation_epoch, receipt_public_key_id,
				 scope, digest, generation, capture_fence, not_after)
			VALUES ($1, $2, $3, 1, 'receipt-key-1', $4, $5, $6, 1, now() + interval '5 minutes')
			RETURNING issued_at`,
			nonce, string(handoff), string(reservation), string(ref.Scope), string(ref.Digest),
			ref.Generation).Scan(&issuedAt)
		Expect(err).NotTo(HaveOccurred())

		return nonce, issuedAt
	}

	// One receipt admission, in one place, because a second spelling of it is
	// a second set of facts and the guards under test are exactly about facts
	// agreeing.
	admissionFor := func(handoff output.HandoffID, execution executioncontrol.Identity, reservation output.ReservationID, ref hangar.TreeRef, nonce string, issuedAt time.Time) output.ReceiptAdmission {
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
					ChallengeNonce:       nonce,
					ChallengeIssuedAt:    output.NewTimestamp(issuedAt),
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
		Expect(repository.RecordSourceReservation(ctx, tx,
			reserveFor(handoff, lease, execution, name), "node-a")).To(Succeed())
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
		Expect(repository.RecordFirstObjectCreate(ctx, tx, reservation, 1)).To(Succeed())
		Expect(tx.Commit()).To(Succeed())

		ref := hangar.TreeRef{Scope: "team-a", Digest: digest, Generation: generation}
		nonce, issuedAt := issueChallenge(handoff, reservation, ref)

		tx, err = dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(tx)
		Expect(repository.RegisterReceipt(ctx, tx,
			admissionFor(handoff, execution, reservation, ref, nonce, issuedAt))).To(Succeed())
		Expect(tx.Commit()).To(Succeed())

		return reservation, ref
	}

	// readLeaseRequest is a well-formed managed-read admission: an exact stat
	// taken a moment ago, a destination that is a handle and a volume, and a
	// nonce minted once for this lease. Every refusal spec below starts from
	// this and changes exactly one thing, so a red row names the check rather
	// than "a lease was refused".
	readLeaseRequest := func(id output.ReadLeaseID, claimID output.ClaimID, ref hangar.TreeRef) output.ReadLeaseRequest {
		GinkgoHelper()
		nonce, err := output.NewReadGrantNonce(rand.Reader)
		Expect(err).NotTo(HaveOccurred())

		// The marker on a real stat carries the reservation that published the
		// object. The fixture reads it back rather than inventing one, so a
		// well-formed stat proof here is the shape production actually observes.
		var reservation string
		Expect(dbConn.QueryRow(`
			SELECT reservation_id FROM hangar_logical_reservations WHERE scope = $1 AND digest = $2`,
			string(ref.Scope), string(ref.Digest)).Scan(&reservation)).To(Succeed())

		return output.ReadLeaseRequest{
			ReadLeaseID:            id,
			ClaimID:                claimID,
			Ref:                    ref,
			ActivationEpoch:        1,
			RequestedAt:            output.NewTimestamp(time.Now()),
			MaterializationTimeout: 10 * time.Minute,
			Destination:            output.ReadDestination{Handle: "task-handle", Volume: "input-0"},
			GrantNonce:             nonce,
			StatProof: output.PublishedObject{
				Attributes: hangar.TreeAttributes{
					Ref: ref, StoredBytes: 1024, LogicalBytes: 4096,
					CreatedAt: time.Now().Add(-time.Minute),
				},
				Metageneration: 1,
				Marker: output.ObjectMarker{
					Version:         output.MarkerVersion,
					Scope:           ref.Scope,
					Digest:          ref.Digest,
					ReservationID:   output.ReservationID(reservation),
					ActivationEpoch: 1,
					CreatedAt:       output.NewTimestamp(time.Now().Add(-time.Minute)),
				},
			},
			StatObservedAt: output.NewTimestamp(time.Now()),
		}
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
			lease, err := repository.AcquireReadLease(ctx, tx,
				readLeaseRequest(output.ReadLeaseID(uuid.NewString()), claimID, ref))
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
			_, err = repository.AcquireReadLease(ctx, reader,
				readLeaseRequest(output.ReadLeaseID(uuid.NewString()), claimID, ref))
			Expect(err).To(HaveOccurred())
			Expect(reader.Rollback()).To(Succeed())
		})
	})

	// A MANAGED READ is admitted by a transaction and validated by another.
	//
	// Requirement 35 names four things that must hold together before a grant
	// exists: an exact stat proving the registered marked generation is
	// PRESENT, a readable lifecycle state, at least one active claim, and a
	// current activation epoch. Requirement 36 adds the lease term. The control
	// row is first, so a repository that refused every read would fail the
	// table rather than pass it.
	Describe("a managed read", func() {
		var ref hangar.TreeRef
		var claimID output.ClaimID

		BeforeEach(func() {
			activate()
			_, ref = publish(hangarDigest(30), 1725830823000030)

			claimID = output.ClaimID(uuid.NewString())
			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			Expect(acquire(tx, claimID, ref, "binding-read")).To(Succeed())
			Expect(tx.Commit()).To(Succeed())
		})

		admit := func(request output.ReadLeaseRequest) (output.ReadLease, error) {
			GinkgoHelper()
			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)

			lease, err := repository.AcquireReadLease(ctx, tx, request)
			if err != nil {
				return output.ReadLease{}, err
			}

			// Committed through the production adapter, because the refusals
			// this plane makes at COMMIT are only typed on the other side of it.
			return lease, db.HangarOutputTx{Tx: tx}.Commit()
		}

		It("admits a read against a claimed, registered, marked, freshly stat-ed generation", func() {
			id := output.ReadLeaseID(uuid.NewString())
			request := readLeaseRequest(id, claimID, ref)

			lease, err := admit(request)
			Expect(err).NotTo(HaveOccurred())
			Expect(lease.ReadLeaseID).To(Equal(id))
			Expect(lease.Ref).To(Equal(ref))
			Expect(lease.LeaseFence).To(Equal(output.LeaseFence(1)))

			// The term is the requirement's own arithmetic, read off the row
			// the database wrote rather than off the value Go passed in.
			term := lease.ExpiresAt.Sub(lease.GrantedAt.Time)
			Expect(term).To(BeNumerically(">=", output.MinLeaseTerm))
			Expect(term).To(BeNumerically(">=",
				request.MaterializationTimeout+output.LeaseTermMargin))
			Expect(output.MayStartWork(term, request.MaterializationTimeout)).To(BeTrue())

			// And what the grant will bind is stored, so a re-mint is the same
			// bytes rather than a second lease.
			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			record, err := repository.LoadReadLease(ctx, tx, id)
			Expect(err).NotTo(HaveOccurred())
			Expect(record.GrantNonce).To(Equal(request.GrantNonce))
			Expect(record.Destination).To(Equal(request.Destination))
			Expect(record.Lease.LeaseFence).To(Equal(lease.LeaseFence))
		})

		DescribeTable("refuses a read that is not admitted",
			func(sentinel error, substring string, spoil func(*output.ReadLeaseRequest)) {
				request := readLeaseRequest(output.ReadLeaseID(uuid.NewString()), claimID, ref)
				spoil(&request)

				_, err := admit(request)
				Expect(err).To(MatchError(sentinel))
				Expect(err.Error()).To(ContainSubstring(substring))

				var leases int
				Expect(dbConn.QueryRow(`SELECT count(*) FROM hangar_read_leases WHERE read_lease_id = $1`,
					string(request.ReadLeaseID)).Scan(&leases)).To(Succeed())
				Expect(leases).To(BeZero(), "a refused read still created a lease")
			},
			Entry("a claim nobody acquired", output.ErrNotFound, "active claim",
				func(request *output.ReadLeaseRequest) {
					request.ClaimID = output.ClaimID(uuid.NewString())
				}),
			Entry("a stat for another generation", output.ErrConflict, "the stat proves",
				func(request *output.ReadLeaseRequest) {
					request.StatProof.Attributes.Ref.Generation++
				}),
			Entry("a stat whose metageneration moved", output.ErrConflict, "metageneration",
				func(request *output.ReadLeaseRequest) { request.StatProof.Metageneration = 4 }),
			Entry("a stat carrying no accepted marker", output.ErrConflict, "marker version",
				func(request *output.ReadLeaseRequest) {
					request.StatProof.Marker.Version = "hangar-output-v0"
				}),
			Entry("a stat from ten minutes ago", output.ErrTimeout, "older than",
				func(request *output.ReadLeaseRequest) {
					request.StatObservedAt = output.NewTimestamp(time.Now().Add(-10 * time.Minute))
				}),
			Entry("an epoch that is not the one the ref was registered under",
				output.ErrConflict, "was registered under epoch",
				func(request *output.ReadLeaseRequest) { request.ActivationEpoch = 2 }),
			Entry("a destination that is a path", output.ErrInvalidIdentity, "canonical path segment",
				func(request *output.ReadLeaseRequest) {
					request.Destination.Volume = "../elsewhere"
				}),
			Entry("no nonce for the grant", output.ErrIncomplete, "read grant nonce",
				func(request *output.ReadLeaseRequest) { request.GrantNonce = "" }),
		)

		It("refuses a read whose claim was released", func() {
			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			Expect(repository.ReleaseClaim(ctx, tx, output.ClaimRelease{
				ProtocolVersion: output.ProtocolVersion,
				ClaimID:         claimID,
				Ref:             ref,
				RequestedAt:     output.NewTimestamp(time.Now()),
			})).To(Succeed())
			Expect(tx.Commit()).To(Succeed())

			_, err = admit(readLeaseRequest(output.ReadLeaseID(uuid.NewString()), claimID, ref))
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(err.Error()).To(ContainSubstring("already released"))
		})

		It("refuses a read for a ref no receipt registered", func() {
			unregistered := ref
			unregistered.Generation++

			request := readLeaseRequest(output.ReadLeaseID(uuid.NewString()), claimID, ref)
			request.Ref = unregistered
			request.StatProof.Attributes.Ref = unregistered

			_, err := admit(request)
			Expect(err).To(MatchError(output.ErrNotFound))
		})

		// The two states a claimed generation can still reach.
		//
		// Requirement 52 keeps existing claims RECORDED when a lifetime
		// violation is detected -- the consumer's binding does not evaporate --
		// so a claimed ref really can be sitting in `missing_out_of_band` or
		// `conflicted` when a read is asked for, and a read admitted against
		// one would be a grant for content the plane has said is not there.
		//
		// Phase 7 owns the code that writes those states; the fixture sets them
		// directly, which is what a spec for a state whose writer has not
		// landed yet can honestly do. What it does NOT do is assert through
		// SQL: the refusal below comes out of the repository.
		DescribeTable("refuses a read for a generation that is no longer readable",
			func(state string) {
				_, err := dbConn.Exec(`
					UPDATE hangar_exact_lifecycles SET state = $4
					WHERE scope = $1 AND digest = $2 AND generation = $3`,
					string(ref.Scope), string(ref.Digest), ref.Generation, state)
				Expect(err).NotTo(HaveOccurred())
				Expect(countActiveClaims(ref)).To(Equal(1),
					"the claim went away, so this is no longer the case it is named for")

				_, err = admit(readLeaseRequest(output.ReadLeaseID(uuid.NewString()), claimID, ref))
				Expect(err).To(MatchError(output.ErrConflict))
				Expect(err.Error()).To(ContainSubstring(state))
			},
			Entry("recorded missing out of band", "missing_out_of_band"),
			Entry("recorded conflicted", "conflicted"),
		)

		It("refuses a read while the epoch's lifetime policy is at risk", func() {
			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			Expect(repository.RecordPolicySnapshot(ctx, tx, output.PolicySnapshot{
				ProtocolVersion:      output.ProtocolVersion,
				ActivationEpoch:      1,
				BucketFingerprint:    "gs://output-bucket",
				Metageneration:       4,
				PolicyHash:           "policy-hash-2",
				LifecycleDeleteRules: 1,
				State:                output.PolicyAtRisk,
				ObservedAt:           output.NewTimestamp(time.Now()),
			})).To(Succeed())
			Expect(tx.Commit()).To(Succeed())

			// The refusal is DEFERRED: it fires at the commit, not at the
			// insert, so what makes it a refusal rather than a lost answer is
			// the transaction adapter's mapping of the schema's class. A
			// substring on the message could not tell the two apart.
			_, err = admit(readLeaseRequest(output.ReadLeaseID(uuid.NewString()), claimID, ref))
			Expect(err).To(MatchError(output.ErrAtRisk))
			Expect(err.Error()).To(ContainSubstring("lifetime policy"))
		})

		// The daemon's independent question, asked of the committed row rather
		// than of the token.
		Describe("validating the lease a grant names", func() {
			var id output.ReadLeaseID
			var request output.ReadLeaseRequest
			var lease output.ReadLease

			BeforeEach(func() {
				id = output.ReadLeaseID(uuid.NewString())
				request = readLeaseRequest(id, claimID, ref)

				var err error
				lease, err = admit(request)
				Expect(err).NotTo(HaveOccurred())
			})

			validation := func() output.ReadLeaseValidation {
				return output.ReadLeaseValidation{
					ReadLeaseID:       id,
					LeaseFence:        lease.LeaseFence,
					ClaimID:           claimID,
					Ref:               ref,
					Destination:       request.Destination,
					ActivationEpoch:   1,
					GrantNonce:        request.GrantNonce,
					RequiredRemaining: request.MaterializationTimeout + output.LeaseStartMargin,
				}
			}

			validate := func(question output.ReadLeaseValidation) (output.ReadLeaseRecord, error) {
				GinkgoHelper()
				tx, err := dbConn.Begin()
				Expect(err).NotTo(HaveOccurred())
				defer db.Rollback(tx)

				return repository.ValidateReadLease(ctx, tx, question)
			}

			It("admits the exact committed lease for work that fits inside it", func() {
				record, err := validate(validation())
				Expect(err).NotTo(HaveOccurred())
				Expect(record.Lease.ReadLeaseID).To(Equal(id))
				Expect(record.GrantNonce).To(Equal(request.GrantNonce))
			})

			It("still admits it after the consumer released its last claim", func() {
				tx, err := dbConn.Begin()
				Expect(err).NotTo(HaveOccurred())
				defer db.Rollback(tx)
				Expect(repository.ReleaseClaim(ctx, tx, output.ClaimRelease{
					ProtocolVersion: output.ProtocolVersion,
					ClaimID:         claimID,
					Ref:             ref,
					RequestedAt:     output.NewTimestamp(time.Now()),
				})).To(Succeed())
				Expect(tx.Commit()).To(Succeed())
				Expect(countActiveClaims(ref)).To(BeZero())

				_, err = validate(validation())
				Expect(err).NotTo(HaveOccurred(),
					"a transfer that released its last claim mid-read was refused; the lease is "+
						"what protects a read once it has one")
			})

			DescribeTable("refuses a grant that does not describe the committed lease",
				func(sentinel error, spoil func(*output.ReadLeaseValidation)) {
					question := validation()
					spoil(&question)

					_, err := validate(question)
					Expect(err).To(MatchError(sentinel))
				},
				Entry("a lease nobody committed", output.ErrNotFound,
					func(question *output.ReadLeaseValidation) {
						question.ReadLeaseID = output.ReadLeaseID(uuid.NewString())
					}),
				Entry("a superseded fence", executioncontrol.ErrStaleFence,
					func(question *output.ReadLeaseValidation) { question.LeaseFence++ }),
				Entry("another claim", output.ErrUnauthorized,
					func(question *output.ReadLeaseValidation) {
						question.ClaimID = output.ClaimID(uuid.NewString())
					}),
				Entry("another generation", output.ErrUnauthorized,
					func(question *output.ReadLeaseValidation) { question.Ref.Generation++ }),
				Entry("another destination", output.ErrUnauthorized,
					func(question *output.ReadLeaseValidation) {
						question.Destination.Volume = "input-9"
					}),
				Entry("another epoch", output.ErrUnauthorized,
					func(question *output.ReadLeaseValidation) { question.ActivationEpoch = 2 }),
				Entry("another nonce", output.ErrUnauthorized,
					func(question *output.ReadLeaseValidation) {
						nonce, err := output.NewReadGrantNonce(rand.Reader)
						Expect(err).NotTo(HaveOccurred())
						question.GrantNonce = nonce
					}),
				Entry("work that would outlive the lease", output.ErrTimeout,
					func(question *output.ReadLeaseValidation) {
						question.RequiredRemaining = 24 * time.Hour
					}),
			)

			// An abandoned reader does not pin a generation forever.
			//
			// A materializer that dies mid-staging leaves an unreleased lease.
			// Requirement 46 asks for "no active read lease" and AC 13 says a
			// reader's protection ends when the lease closes OR SAFELY EXPIRES,
			// and counting an expired one forever would let one crash pin a
			// generation for the life of the deployment.
			//
			// The lease is aged by moving both of its instants back together, so
			// its fifteen-minute term is preserved and what changes is only
			// whether it has run out. Nothing here shortens a lease.
			It("stops pinning a generation once an abandoned lease has expired", func() {
				tx, err := dbConn.Begin()
				Expect(err).NotTo(HaveOccurred())
				defer db.Rollback(tx)
				Expect(repository.ReleaseClaim(ctx, tx, output.ClaimRelease{
					ProtocolVersion: output.ProtocolVersion,
					ClaimID:         claimID,
					Ref:             ref,
					RequestedAt:     output.NewTimestamp(time.Now()),
				})).To(Succeed())
				Expect(tx.Commit()).To(Succeed())

				// The control: while the lease is live, reclaim is refused.
				blocked, err := dbConn.Begin()
				Expect(err).NotTo(HaveOccurred())
				defer db.Rollback(blocked)
				err = repository.AdmitReclaim(ctx, blocked, ref, uuid.NewString(), 1,
					output.MinLeaseTerm)
				Expect(err).To(MatchError(output.ErrConflict))
				Expect(err.Error()).To(ContainSubstring("1 read lease(s)"))
				Expect(blocked.Rollback()).To(Succeed())

				_, err = dbConn.Exec(`
					UPDATE hangar_read_leases
					SET granted_at = granted_at - interval '1 hour',
					    renewed_at = renewed_at - interval '1 hour',
					    expires_at = expires_at - interval '1 hour'
					WHERE read_lease_id = $1`, string(id))
				Expect(err).NotTo(HaveOccurred())

				admitted, err := dbConn.Begin()
				Expect(err).NotTo(HaveOccurred())
				defer db.Rollback(admitted)
				Expect(repository.AdmitReclaim(ctx, admitted, ref, uuid.NewString(), 1,
					output.MinLeaseTerm)).To(Succeed())
				Expect(admitted.Commit()).To(Succeed())

				// And recovery writes the release the daemon never got to write,
				// so the tombstone that prevents resurrection exists either way.
				closing, err := dbConn.Begin()
				Expect(err).NotTo(HaveOccurred())
				defer db.Rollback(closing)
				closed, err := repository.CloseAbandonedReadLeases(ctx, closing, 100)
				Expect(err).NotTo(HaveOccurred())
				Expect(closed).To(Equal(1))
				Expect(closing.Commit()).To(Succeed())

				again, err := dbConn.Begin()
				Expect(err).NotTo(HaveOccurred())
				defer db.Rollback(again)
				closed, err = repository.CloseAbandonedReadLeases(ctx, again, 100)
				Expect(err).NotTo(HaveOccurred())
				Expect(closed).To(BeZero(), "recovery closed a lease it had already closed")
				Expect(again.Rollback()).To(Succeed())

				var released int
				Expect(dbConn.QueryRow(`
					SELECT count(*) FROM hangar_read_leases
					WHERE read_lease_id = $1 AND released_at IS NOT NULL`,
					string(id)).Scan(&released)).To(Succeed())
				Expect(released).To(Equal(1))
			})

			// A RENEWAL GRANTS ONE TERM, and the same one every time.
			//
			// The interval must not be read off the row being renewed:
			// `expires_at` is what the previous renewal moved and `granted_at`
			// never moves, so a term derived from their difference grows by the
			// age of the lease on every pass. At the daemon's one-minute
			// cadence the k-th renewal would add k-1 minutes, and an abandoned
			// reader would pin its generation for term + N(N-1)/2 minutes --
			// which is exactly the pin CloseAbandonedReadLeases exists to
			// bound, made unboundable by the mechanism meant to keep a live
			// reader alive. Requirement 36 names ONE term.
			//
			// The row is aged so that renewing has something to do; ageing
			// moves both instants together, so what changes is how much is
			// left, never the term itself.
			It("grants exactly one term from now, however often it is renewed", func() {
				term := output.LeaseTermFor(request.MaterializationTimeout)

				_, err := dbConn.Exec(`
					UPDATE hangar_read_leases
					SET granted_at = granted_at - interval '10 minutes',
					    renewed_at = renewed_at - interval '10 minutes',
					    expires_at = expires_at - interval '10 minutes'
					WHERE read_lease_id = $1`, string(id))
				Expect(err).NotTo(HaveOccurred())

				current := lease
				for pass := 1; pass <= 3; pass++ {
					tx, err := dbConn.Begin()
					Expect(err).NotTo(HaveOccurred())
					record, err := repository.LoadReadLease(ctx, tx, id)
					Expect(err).NotTo(HaveOccurred())

					current, err = repository.RenewReadLease(ctx, tx, record.Lease)
					Expect(err).NotTo(HaveOccurred())
					Expect(tx.Commit()).To(Succeed())

					remaining := time.Until(current.ExpiresAt.Time)
					Expect(remaining).To(BeNumerically("<=", term),
						fmt.Sprintf("renewal %d left more than one term on the lease", pass))
					Expect(remaining).To(BeNumerically(">", term-time.Minute),
						fmt.Sprintf("renewal %d granted less than a term", pass))
				}
			})

			// The repository's own contract, not the handler's composition.
			//
			// LeaseControl runs ValidateReadLease first and would refuse both
			// of these before RenewReadLease saw them -- but RenewReadLease is
			// on the ReadLeaseRepository contract for any caller, and a method
			// whose error text says "released, expired or ..." should be the
			// method that decides it. A renewal that resurrected an expired
			// lease would re-pin a generation recovery had already released.
			It("refuses to renew a lease that has already expired", func() {
				_, err := dbConn.Exec(`
					UPDATE hangar_read_leases
					SET granted_at = granted_at - interval '1 hour',
					    renewed_at = renewed_at - interval '1 hour',
					    expires_at = expires_at - interval '1 hour'
					WHERE read_lease_id = $1`, string(id))
				Expect(err).NotTo(HaveOccurred())

				tx, err := dbConn.Begin()
				Expect(err).NotTo(HaveOccurred())
				defer db.Rollback(tx)
				_, err = repository.RenewReadLease(ctx, tx, lease)
				Expect(err).To(MatchError(output.ErrConflict))
				Expect(err.Error()).To(ContainSubstring("expired"))
			})

			DescribeTable("refuses to renew a lease whose generation is no longer readable",
				func(state string) {
					_, err := dbConn.Exec(`
						UPDATE hangar_exact_lifecycles SET state = $4
						WHERE scope = $1 AND digest = $2 AND generation = $3`,
						string(ref.Scope), string(ref.Digest), ref.Generation, state)
					Expect(err).NotTo(HaveOccurred())

					tx, err := dbConn.Begin()
					Expect(err).NotTo(HaveOccurred())
					defer db.Rollback(tx)
					_, err = repository.RenewReadLease(ctx, tx, lease)
					Expect(err).To(MatchError(output.ErrConflict))
				},
				Entry("recorded missing out of band", "missing_out_of_band"),
				Entry("recorded conflicted", "conflicted"),
			)

			It("refuses a released lease even though its grant is still signed", func() {
				tx, err := dbConn.Begin()
				Expect(err).NotTo(HaveOccurred())
				defer db.Rollback(tx)
				Expect(repository.ReleaseReadLease(ctx, tx, lease)).To(Succeed())
				Expect(tx.Commit()).To(Succeed())

				_, err = validate(validation())
				Expect(err).To(MatchError(output.ErrConflict))
				Expect(err.Error()).To(ContainSubstring("was released"))
			})

			// A repeat is a RETRY, not a renewal.
			//
			// The lease id and the nonce are the caller's, generated before the
			// attempt, so a caller whose commit answer was lost asks again with
			// the same ones. Advancing anything there would hand the retry a
			// different lease than the one that may already be committed, and
			// the byte-identical re-mint requirement 37 asks for would be
			// impossible to honour. Reuse of the identity for DIFFERENT facts is
			// the other half, and it is a conflict.
			It("is idempotent for the same identity and facts, and a conflict for others", func() {
				again, err := admit(request)
				Expect(err).NotTo(HaveOccurred())
				Expect(again.LeaseFence).To(Equal(lease.LeaseFence))
				Expect(again.GrantedAt.Time).To(BeTemporally("==", lease.GrantedAt.Time))
				Expect(again.ExpiresAt.Time).To(BeTemporally("==", lease.ExpiresAt.Time))

				var leases int
				Expect(dbConn.QueryRow(
					`SELECT count(*) FROM hangar_read_leases WHERE read_lease_id = $1`,
					string(id)).Scan(&leases)).To(Succeed())
				Expect(leases).To(Equal(1))

				// The same identity, a different nonce: another read wearing
				// this one's lease id.
				other := readLeaseRequest(id, claimID, ref)
				Expect(other.GrantNonce).NotTo(Equal(request.GrantNonce))
				_, err = admit(other)
				Expect(err).To(MatchError(output.ErrConflict))
				Expect(err.Error()).To(ContainSubstring("already protects another read"))
			})

			It("refuses to reactivate a released lease under its own identity", func() {
				tx, err := dbConn.Begin()
				Expect(err).NotTo(HaveOccurred())
				defer db.Rollback(tx)
				Expect(repository.ReleaseReadLease(ctx, tx, lease)).To(Succeed())
				Expect(tx.Commit()).To(Succeed())

				_, err = admit(request)
				Expect(err).To(MatchError(output.ErrConflict))
				Expect(err.Error()).To(ContainSubstring("stays tombstoned"))
			})
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
			Expect(repository.RecordSourceReservation(ctx, tx,
				reserveFor(handoff, lease, execution, "result"), "node-a")).To(Succeed())
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
		It("returns a typed retry when another owner takes over between the read and the lock", func() {
			activate()
			reservation, _ := publish(hangarDigest(7), 1725830823000007)

			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)

			derived, err := db.DeriveHangarRefUnlocked(ctx, tx, reservation)
			Expect(err).NotTo(HaveOccurred())
			Expect(derived.Resolved).To(BeTrue())
			Expect(derived.Logical.Digest).To(Equal(hangarDigest(7)))

			// The takeover, committed by somebody else, between the unlocked read
			// and the locks. Mutating the derived struct on the client would prove
			// only that the comparison compares; what has to be true is that the
			// revalidation reads the row again and sees what the other owner did.
			takeover, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(takeover)
			_, err = takeover.Exec(`
				UPDATE hangar_capture_attempt_leases
				SET owner_id = $2, capture_fence = capture_fence + 1, renewed_at = now(),
				    expires_at = now() + interval '15 minutes'
				WHERE reservation_id = $1`, string(reservation), uuid.NewString())
			Expect(err).NotTo(HaveOccurred())
			Expect(takeover.Commit()).To(Succeed())

			locks, err := db.LockHangarSuffix(ctx, tx, consumer, db.HangarLockRequest{
				Logical:  []db.HangarLogicalKey{derived.Logical},
				Captures: []output.ReservationID{reservation},
			})
			Expect(err).NotTo(HaveOccurred())

			err = locks.RevalidateDerivation(ctx, tx, derived)
			Expect(err).To(MatchError(db.ErrHangarLockRetry))
			Expect(err.Error()).To(ContainSubstring("superseded"))
			Expect(tx.Rollback()).To(Succeed())
		})

		It("revalidates cleanly when nothing moved", func() {
			activate()
			reservation, _ := publish(hangarDigest(16), 1725830823000016)

			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)

			derived, err := db.DeriveHangarRefUnlocked(ctx, tx, reservation)
			Expect(err).NotTo(HaveOccurred())
			locks, err := db.LockHangarSuffix(ctx, tx, consumer, db.HangarLockRequest{
				Logical:  []db.HangarLogicalKey{derived.Logical},
				Captures: []output.ReservationID{reservation},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(locks.RevalidateDerivation(ctx, tx, derived)).To(Succeed())
			Expect(tx.Rollback()).To(Succeed())
		})

		It("refuses to register a receipt whose reservation resolved elsewhere", func() {
			activate()
			reservation, ref := publish(hangarDigest(8), 1725830823000008)

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

			// A receipt for content this reservation never resolved to. The
			// logical identity of published bytes is what recovery and inventory
			// correlate on, so registering a ref against a reservation resolved
			// elsewhere is a conflict and not a second record.
			elsewhere := ref
			elsewhere.Digest = hangarDigest(17)

			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			elsewhereNonce, elsewhereIssuedAt := issueChallenge(handoff, reservation, elsewhere)
			err = repository.RegisterReceipt(ctx, tx, admissionFor(handoff, execution, reservation,
				elsewhere, elsewhereNonce, elsewhereIssuedAt))
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(err.Error()).To(ContainSubstring("against a reservation resolved to"))
			Expect(tx.Rollback()).To(Succeed())
		})
	})

	Describe("the irreversible publish point", func() {
		// Req 10: a stale owner may not seal, publish, sign/register a
		// receipt, finalize or release. This is the publish half, and it is
		// the one write nothing can walk back -- past it, cancellation cannot
		// unmake the object, and the capture is left to receipt or orphan
		// settlement.
		It("refuses a superseded owner", func() {
			activate()

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
			Expect(repository.RecordSourceReservation(ctx, tx,
				reserveFor(handoff, lease, execution, name), "node-a")).To(Succeed())
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
				Digest:          hangarDigest(18),
				LogicalBytes:    4096,
				ResolvedAt:      output.NewTimestamp(time.Now()),
			})).To(Succeed())
			Expect(tx.Commit()).To(Succeed())

			// Somebody else takes capture ownership over.
			_, err = dbConn.Exec(`
				UPDATE hangar_capture_attempt_leases
				SET owner_id = $2, capture_fence = capture_fence + 1, renewed_at = now(),
				    expires_at = now() + interval '15 minutes'
				WHERE reservation_id = $1`, string(reservation), uuid.NewString())
			Expect(err).NotTo(HaveOccurred())

			superseded, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(superseded)
			err = repository.RecordFirstObjectCreate(ctx, superseded, reservation, 1)
			Expect(err).To(MatchError(executioncontrol.ErrStaleFence))
			Expect(err.Error()).To(ContainSubstring("a stale owner may not publish"))
			Expect(superseded.Rollback()).To(Succeed())

			var past bool
			Expect(dbConn.QueryRow(`
				SELECT past_irreversible_publish_point FROM hangar_capture_reservations
				WHERE reservation_id = $1`, string(reservation)).Scan(&past)).To(Succeed())
			Expect(past).To(BeFalse(),
				"a superseded owner moved the capture past the point nothing walks back")

			// And the owner that actually holds the fence records it.
			current, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(current)
			Expect(repository.RecordFirstObjectCreate(ctx, current, reservation, 2)).To(Succeed())
			Expect(current.Commit()).To(Succeed())

			Expect(dbConn.QueryRow(`
				SELECT past_irreversible_publish_point FROM hangar_capture_reservations
				WHERE reservation_id = $1`, string(reservation)).Scan(&past)).To(Succeed())
			Expect(past).To(BeTrue())
		})
	})

	Describe("cancelling a capture before the publish point", func() {
		// Req 5 and Req 11: the source remains held until both halves are
		// authoritative, and a cancellation before sealing or object creation
		// terminally cancels *and fenced-releases the source*. A capture that
		// called itself settled the moment the row said `cancelled` was
		// reporting the second half done because the first half was decided --
		// while a node somewhere still holds the source open. The other two
		// branches already mean "the daemon acknowledged the release" by
		// `Settled`, and one word meaning two things across three branches is
		// what makes a drain predicate unwritable.
		It("is not settled until the source release is acknowledged", func() {
			activate()

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
			Expect(repository.RecordSourceReservation(ctx, tx,
				reserveFor(handoff, lease, execution, name), "node-a")).To(Succeed())
			Expect(repository.AcknowledgeSourceHold(ctx, tx, holdFor(handoff, lease, execution, name))).
				To(Succeed())
			_, err = repository.CommitCaptureReservation(ctx, tx,
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
			Expect(tx.Commit()).To(Succeed())

			cancelling, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(cancelling)
			status, err := repository.CancelOrSettle(ctx, cancelling, handoff)
			Expect(err).NotTo(HaveOccurred())
			Expect(status.PastIrreversiblePublishPoint).To(BeFalse())
			Expect(status.Settled).To(BeFalse(),
				"the capture called itself settled while the source is still held")
			Expect(cancelling.Commit()).To(Succeed())

			var state string
			var unsettled bool
			Expect(dbConn.QueryRow(`
				SELECT state, settled_at IS NULL FROM hangar_capture_reservations
				WHERE handoff_id = $1`, string(handoff)).Scan(&state, &unsettled)).To(Succeed())
			Expect(state).To(Equal("cancelled"), "the cancellation was not recorded")
			Expect(unsettled).To(BeTrue(), "a settlement time was stamped with nothing to earn it")

			// The cancellation recorded an INTENT, and it is the database that
			// minted it: the generic CancelOrSettle seam takes a handoff and
			// nothing else, because T7 calls it and must not learn what a
			// release intent is.
			var intent string
			Expect(dbConn.QueryRow(`
				SELECT release_intent_id::text FROM hangar_capture_reservations
				WHERE handoff_id = $1`, string(handoff)).Scan(&intent)).To(Succeed())
			Expect(intent).NotTo(BeEmpty(),
				"a cancelled capture recorded no release intent, so nothing on any node has "+
					"anything to acknowledge")

			// The daemon's fenced release, arriving through the repository
			// rather than through raw SQL -- which is what makes this a test of
			// the pair rather than of the column.
			release := output.ReleaseAcknowledgement{
				ProtocolVersion: output.ProtocolVersion,
				Disposition:     output.DispositionCapture,
				Execution:       execution,
				ActivationEpoch: 1,
				HandoffID:       handoff,
				SourceLeaseID:   lease,
				ReleaseIntentID: output.ReleaseIntentID(intent),
				Incarnation: output.SourceIncarnation{
					ExecutionID:      execution.ExecutionID,
					NodeUID:          "node-uid",
					HandleGeneration: 1,
					Output:           name,
				},
				LedgerSequence: 9,
				ObservedAt:     output.NewTimestamp(time.Now().UTC()),
				Signature:      "c2lnbmF0dXJlLXJlbGVhc2U",
			}

			// A release naming an intent this branch never recorded is refused,
			// and it is asserted BEFORE the one that succeeds: "the release was
			// admitted" proves nothing on a repository that admits every
			// release.
			foreign := release
			foreign.ReleaseIntentID = output.ReleaseIntentID(uuid.NewString())
			refusing, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(refusing)
			Expect(repository.AcknowledgeCaptureRelease(ctx, refusing, foreign)).NotTo(Succeed())
			Expect(refusing.Rollback()).To(Succeed())

			// And one offered to the wrong branch.
			wrongBranch := release
			wrongBranch.Disposition = output.DispositionNoCapture
			misdirecting, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(misdirecting)
			Expect(repository.AcknowledgeCaptureRelease(ctx, misdirecting, wrongBranch)).NotTo(Succeed())
			Expect(misdirecting.Rollback()).To(Succeed())

			releasing, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(releasing)
			Expect(repository.AcknowledgeCaptureRelease(ctx, releasing, release)).To(Succeed())
			Expect(releasing.Commit()).To(Succeed())

			// Idempotent for the same intent: recovery repeats it until
			// committed-versus-not is known.
			repeating, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(repeating)
			Expect(repository.AcknowledgeCaptureRelease(ctx, repeating, release)).To(Succeed())
			Expect(repeating.Commit()).To(Succeed())

			reading, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(reading)
			status, err = repository.ClassifyHandoff(ctx, reading, handoff)
			Expect(err).NotTo(HaveOccurred())
			Expect(status.Settled).To(BeTrue(),
				"the release is acknowledged and the capture still reports something owed")
			Expect(reading.Rollback()).To(Succeed())

			var settled bool
			Expect(dbConn.QueryRow(`
				SELECT settled_at IS NOT NULL FROM hangar_capture_reservations
				WHERE handoff_id = $1`, string(handoff)).Scan(&settled)).To(Succeed())
			Expect(settled).To(BeTrue(), "the acknowledged release did not settle the capture")
		})

		// Phase 3 completion pass. The capture branch's release guard is
		// `state = 'cancelled' AND NOT past_irreversible_publish_point`, and
		// the second half had no vector: through the repository the two facts
		// look mutually exclusive, because RecordFirstObjectCreate refuses a
		// cancelled row and CancelOrSettle returns early past the publish
		// point. So the state the guard defends against looked unreachable,
		// and a guard against an unreachable state is one somebody deletes.
		//
		// It is reachable, by the interleaving the guard is FOR. A publish that
		// is already in flight passes the point while the row is still live;
		// the canceller classifies the handoff a moment earlier, sees a capture
		// it may cancel, and then blocks on the publisher's row lock. When it
		// wakes the row is past the point and its own UPDATE -- which re-checks
		// only `settled_at IS NULL` -- goes through. State: cancelled, and past
		// the point nothing walks back.
		//
		// The release offered there is a node working from stale state. The
		// object may exist; the capture settles a registered receipt or a
		// terminal orphan, and admitting the release would settle it while an
		// object nobody has correlated is sitting in the bucket.
		It("refuses a release offered after the irreversible publish point, and admits one before it", func() {
			activate()

			setUp := func() (output.HandoffID, output.ReservationID, output.ReleaseAcknowledgement) {
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
				Expect(repository.RecordSourceReservation(ctx, tx,
					reserveFor(handoff, lease, execution, name), "node-a")).To(Succeed())
				Expect(repository.AcknowledgeSourceHold(ctx, tx,
					holdFor(handoff, lease, execution, name))).To(Succeed())
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
				Expect(tx.Commit()).To(Succeed())

				return handoff, reservation, output.ReleaseAcknowledgement{
					ProtocolVersion: output.ProtocolVersion,
					Disposition:     output.DispositionCapture,
					Execution:       execution,
					ActivationEpoch: 1,
					HandoffID:       handoff,
					SourceLeaseID:   lease,
					Incarnation: output.SourceIncarnation{
						ExecutionID:      execution.ExecutionID,
						NodeUID:          "node-uid",
						HandleGeneration: 1,
						Output:           name,
					},
					LedgerSequence: 11,
					ObservedAt:     output.NewTimestamp(time.Now().UTC()),
					Signature:      "c2lnbmF0dXJlLXJlbGVhc2U",
				}
			}

			intentFor := func(handoff output.HandoffID) output.ReleaseIntentID {
				var intent string
				Expect(dbConn.QueryRow(`
					SELECT release_intent_id::text FROM hangar_capture_reservations
					WHERE handoff_id = $1`, string(handoff)).Scan(&intent)).To(Succeed())
				Expect(intent).NotTo(BeEmpty())

				return output.ReleaseIntentID(intent)
			}

			// THE CONTROL, first. Cancelled and short of the publish point: the
			// release is admitted. Without this the refusal below would also
			// pass on a branch that refuses every capture release.
			handoff, _, release := setUp()
			cancelling, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(cancelling)
			_, err = repository.CancelOrSettle(ctx, cancelling, handoff)
			Expect(err).NotTo(HaveOccurred())
			Expect(cancelling.Commit()).To(Succeed())

			release.ReleaseIntentID = intentFor(handoff)
			admitting, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(admitting)
			Expect(repository.AcknowledgeCaptureRelease(ctx, admitting, release)).To(Succeed())
			Expect(admitting.Commit()).To(Succeed())

			// AND THE VECTOR. A second capture, raced past the point.
			racedHandoff, racedReservation, racedRelease := setUp()

			// The publisher passes the point and HOLDS ITS TRANSACTION OPEN.
			publishing, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(publishing)
			Expect(repository.ResolveLogicalReservation(ctx, publishing, output.LogicalResolution{
				ProtocolVersion: output.ProtocolVersion,
				Execution:       racedRelease.Execution,
				ActivationEpoch: 1,
				HandoffID:       racedHandoff,
				ReservationID:   racedReservation,
				CaptureFence:    1,
				Scope:           "team-a",
				Digest:          hangarDigest(37),
				LogicalBytes:    4096,
				ResolvedAt:      output.NewTimestamp(time.Now()),
			})).To(Succeed())
			Expect(repository.RecordFirstObjectCreate(ctx, publishing, racedReservation, 1)).
				To(Succeed())

			// The canceller starts while the row still reads live, and blocks
			// on the publisher's lock at its UPDATE.
			cancelled := make(chan error, 1)
			go func() {
				racing, err := dbConn.Begin()
				if err != nil {
					cancelled <- err

					return
				}
				if _, err := repository.CancelOrSettle(ctx, racing, racedHandoff); err != nil {
					_ = racing.Rollback()
					cancelled <- err

					return
				}
				cancelled <- racing.Commit()
			}()

			// Give the canceller time to reach the lock. If it has not, the
			// interleaving is simply a different order and the assertions below
			// are about the state, not about the timing.
			time.Sleep(250 * time.Millisecond)
			Expect(publishing.Commit()).To(Succeed())
			Eventually(cancelled, 10*time.Second).Should(Receive(BeNil()))

			var past bool
			var state string
			Expect(dbConn.QueryRow(`
				SELECT past_irreversible_publish_point, state FROM hangar_capture_reservations
				WHERE reservation_id = $1`, string(racedReservation)).Scan(&past, &state)).To(Succeed())
			Expect(state).To(Equal("cancelled"))
			Expect(past).To(BeTrue(),
				"the race did not reach the state this vector is about; the guard is untested")

			racedRelease.ReleaseIntentID = intentFor(racedHandoff)
			refusing, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(refusing)
			err = repository.AcknowledgeCaptureRelease(ctx, refusing, racedRelease)
			Expect(err).To(MatchError(output.ErrConflict),
				"a release past the irreversible publish point was not refused as a conflict")
			Expect(err.Error()).To(ContainSubstring("irreversible publish point"))
			Expect(refusing.Rollback()).To(Succeed())

			// And it really was refused: nothing was written.
			var acknowledged, settled bool
			Expect(dbConn.QueryRow(`
				SELECT release_acknowledged_at IS NOT NULL, settled_at IS NOT NULL
				FROM hangar_capture_reservations WHERE reservation_id = $1`,
				string(racedReservation)).Scan(&acknowledged, &settled)).To(Succeed())
			Expect(acknowledged).To(BeFalse(),
				"the release past the publish point was recorded anyway")
			Expect(settled).To(BeFalse(),
				"a capture with an uncorrelated object in the bucket was settled")
		})

		// Review finding R2-3. Cancellation is terminal by Req 11's own word,
		// but it does not take the capture lease away -- so the owner that was
		// running when the control plane gave up is still the owner at the
		// current fence, and every write on the publish path was fenced and
		// nothing else. The control comes first: the same call, by the same
		// owner, at the same fence, succeeds while the capture is live.
		It("refuses the live owner's publish writes after a terminal cancellation", func() {
			activate()

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
			Expect(repository.RecordSourceReservation(ctx, tx,
				reserveFor(handoff, lease, execution, name), "node-a")).To(Succeed())
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
			Expect(tx.Commit()).To(Succeed())

			resolution := output.LogicalResolution{
				ProtocolVersion: output.ProtocolVersion,
				Execution:       execution,
				ActivationEpoch: 1,
				HandoffID:       handoff,
				ReservationID:   reservation,
				CaptureFence:    1,
				Scope:           "team-a",
				Digest:          hangarDigest(31),
				LogicalBytes:    4096,
				ResolvedAt:      output.NewTimestamp(time.Now()),
			}

			// The control, thrown away: while the capture is live this owner
			// resolves and passes the publish point. Without it, the refusals
			// below would also pass on a repository that had stopped writing.
			live, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(live)
			Expect(repository.ResolveLogicalReservation(ctx, live, resolution)).To(Succeed())
			Expect(repository.RecordFirstObjectCreate(ctx, live, reservation, 1)).To(Succeed())
			Expect(live.Rollback()).To(Succeed())

			cancelling, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(cancelling)
			status, err := repository.CancelOrSettle(ctx, cancelling, handoff)
			Expect(err).NotTo(HaveOccurred())
			Expect(status.PastIrreversiblePublishPoint).To(BeFalse())
			Expect(cancelling.Commit()).To(Succeed())

			after, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(after)
			err = repository.ResolveLogicalReservation(ctx, after, resolution)
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(err.Error()).To(ContainSubstring("terminally cancelled"))
			Expect(after.Rollback()).To(Succeed())

			publishing, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(publishing)
			err = repository.RecordFirstObjectCreate(ctx, publishing, reservation, 1)
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(err.Error()).To(ContainSubstring("terminally cancelled"))
			Expect(publishing.Rollback()).To(Succeed())

			var past bool
			var state string
			Expect(dbConn.QueryRow(`
				SELECT past_irreversible_publish_point, state FROM hangar_capture_reservations
				WHERE reservation_id = $1`, string(reservation)).Scan(&past, &state)).To(Succeed())
			Expect(state).To(Equal("cancelled"))
			Expect(past).To(BeFalse(),
				"a cancelled capture was moved past the point nothing walks back")

			var resolved bool
			Expect(dbConn.QueryRow(`
				SELECT EXISTS(SELECT 1 FROM hangar_logical_reservations WHERE reservation_id = $1)`,
				string(reservation)).Scan(&resolved)).To(Succeed())
			Expect(resolved).To(BeFalse(),
				"a cancelled capture resolved a logical identity nothing will ever publish")
		})

		// A registered receipt is a decision about the OBJECT. The source is on
		// some node until that node says otherwise, and a held source is exempt
		// from payload cleanup, sweep and reuse -- so calling a registered
		// capture settled is how a successful capture came to pin its
		// incarnation on a node forever. It settles the way the other two
		// branches settle: when the fenced release is acknowledged.
		It("reports a registered capture settled only once its source is released", func() {
			activate()
			reservation, _ := publish(hangarDigest(15), 1725830823000015)

			var handoffID, intent, executionID, leaseID string
			Expect(dbConn.QueryRow(`
				SELECT handoff_id, release_intent_id, execution_id, source_lease_id
				FROM hangar_capture_reservations WHERE reservation_id = $1`, string(reservation)).
				Scan(&handoffID, &intent, &executionID, &leaseID)).To(Succeed())
			handoff := output.HandoffID(handoffID)
			Expect(intent).ToNot(BeEmpty(),
				"a registered capture recorded no release intent, so nothing addresses the "+
					"node still holding the source it sealed")

			settled := func() bool {
				GinkgoHelper()

				tx, err := dbConn.Begin()
				Expect(err).NotTo(HaveOccurred())
				defer db.Rollback(tx)
				status, err := repository.ClassifyHandoff(ctx, tx, handoff)
				Expect(err).NotTo(HaveOccurred())

				return status.Settled
			}

			Expect(settled()).To(BeFalse(),
				"a registered capture reported itself settled while its source was still held")

			incomplete, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(incomplete)
			owed, err := repository.IncompleteHandoffs(ctx, incomplete, 100)
			Expect(err).NotTo(HaveOccurred())
			Expect(owed).To(ContainElement(handoff),
				"a registered capture that still owes a release is not in the debt query, so "+
					"nothing will ever come back for it")
			Expect(incomplete.Rollback()).To(Succeed())

			// And the release settles it. Past the irreversible publish point,
			// which is where a registered capture always is: `registered` is
			// the one state that reached it legitimately.
			release, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(release)
			execution := executioncontrol.Identity{
				ExecutionID: executioncontrol.ExecutionID(executionID),
				Fence:       1,
			}
			Expect(repository.AcknowledgeCaptureRelease(ctx, release, output.ReleaseAcknowledgement{
				ProtocolVersion: output.ProtocolVersion,
				Disposition:     output.DispositionCapture,
				Execution:       execution,
				ActivationEpoch: 1,
				HandoffID:       handoff,
				SourceLeaseID:   output.SourceLeaseID(leaseID),
				ReleaseIntentID: output.ReleaseIntentID(intent),
				Incarnation: output.SourceIncarnation{
					ExecutionID:      execution.ExecutionID,
					NodeUID:          "node-uid",
					HandleGeneration: 1,
					Output:           "result",
				},
				LedgerSequence: 11,
				ObservedAt:     output.NewTimestamp(time.Now().UTC()),
				Signature:      "c2lnbmF0dXJlLXJlbGVhc2U",
			})).To(Succeed())
			Expect(release.Commit()).To(Succeed())

			Expect(settled()).To(BeTrue())
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
			secondNonce, secondIssuedAt := issueChallenge(handoff, reservation, second)
			registerErr := repository.RegisterReceipt(ctx, tx, admissionFor(handoff, execution,
				reservation, second, secondNonce, secondIssuedAt))
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
			lateNonce, lateIssuedAt := issueChallenge(handoff, reservation, ref)
			Expect(repository.RegisterReceipt(ctx, tx, admissionFor(handoff, execution,
				reservation, ref, lateNonce, lateIssuedAt))).To(Succeed())
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
			// Two rows: the one the consumer locks, and one it does not. The
			// second is what makes this spec able to fail -- pg_locks holds
			// one row per (relation, mode, pid) and row locks live in tuple
			// headers, so Hangar taking the same mode on the same row the
			// consumer already holds is invisible from outside. Reaching any
			// row the consumer left alone is not.
			_, err = dbConn.Exec(`
				INSERT INTO opaque_consumer_bindings (binding_id, visibility)
				VALUES ('binding-1', 'hidden'), ('binding-2', 'hidden')`)
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

			// And the consumer row nobody locked is still free while this
			// transaction holds every lock the claim needed.
			probe, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(probe)
			_, err = probe.Exec(
				`SELECT 1 FROM opaque_consumer_bindings WHERE binding_id = $1 FOR UPDATE NOWAIT`,
				"binding-2")
			Expect(err).NotTo(HaveOccurred(),
				"Hangar reached a consumer row the consumer never locked")
			Expect(probe.Rollback()).To(Succeed())

			Expect(tx.Rollback()).To(Succeed())
		})
	})

	// What a coordinator reads, and the two facts it writes.
	//
	// A capture is recovered by a process that was not the one that started it,
	// so it cannot ask "what was I doing" -- it can only read what is durably
	// true. These are the reads and writes that makes that possible, and the
	// point of testing them here rather than in the coordinator's own package
	// is that the interesting half is what PostgreSQL actually stored.
	Describe("what a coordinator reads back", func() {
		var (
			handoff   output.HandoffID
			lease     output.SourceLeaseID
			execution executioncontrol.Identity
			name      output.OutputName
			deadline  output.Timestamp
		)

		BeforeEach(func() {
			activate()

			handoff = output.HandoffID(uuid.NewString())
			lease = output.SourceLeaseID(uuid.NewString())
			execution = identity()
			name = output.OutputName("result")
			deadline = output.NewTimestamp(time.Now().Add(24 * time.Hour))

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
			Expect(tx.Commit()).To(Succeed())
		})

		read := func() output.HandoffRecord {
			GinkgoHelper()

			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			record, err := repository.LoadHandoffRecord(ctx, tx, handoff)
			Expect(err).NotTo(HaveOccurred())

			return record
		}

		// The reservation is recorded because a RELEASE has to be addressed to
		// one node, and after a crash there is nowhere else to learn which. It
		// is the fact Phase 4 left only in the memory of the process that
		// asked for it.
		It("remembers where the source is, once, and refuses a second location", func() {
			// The control: before anything is reserved, the record says so and
			// the coordinator has nothing to release.
			Expect(read().Source.Reserved()).To(BeFalse())

			reserved := reserveFor(handoff, lease, execution, name)

			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			Expect(repository.RecordSourceReservation(ctx, tx, reserved, "node-a")).To(Succeed())
			// The same answer again is the same answer: a daemon that repeats
			// a reservation is not a second capture.
			Expect(repository.RecordSourceReservation(ctx, tx, reserved, "node-a")).To(Succeed())

			elsewhere := reserved
			elsewhere.Incarnation.HandleGeneration = 9
			elsewhere.Directory = string(execution.ExecutionID) + ".9/result"
			Expect(repository.RecordSourceReservation(ctx, tx, elsewhere, "node-b")).
				To(MatchError(output.ErrConflict))
			Expect(tx.Commit()).To(Succeed())

			record := read()
			Expect(record.Source.Reserved()).To(BeTrue())
			Expect(record.Source.Locator).To(Equal("node-a"))
			Expect(record.Source.Incarnation).To(Equal(reserved.Incarnation))
			Expect(record.Source.Directory).To(Equal(reserved.Directory))
		})

		// The Phase 4 seam, in the schema. A hold binds to an incarnation the
		// daemon issued FIRST; a hold over no reservation is a producer
		// writing into a directory nothing protects.
		It("refuses a hold over a source nobody reserved, and admits one over a reserved source", func() {
			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			Expect(repository.AcknowledgeSourceHold(ctx, tx,
				holdFor(handoff, lease, execution, name))).ToNot(Succeed())
			db.Rollback(tx)

			second, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(second)
			Expect(repository.RecordSourceReservation(ctx, second,
				reserveFor(handoff, lease, execution, name), "node-a")).To(Succeed())
			Expect(repository.AcknowledgeSourceHold(ctx, second,
				holdFor(handoff, lease, execution, name))).To(Succeed())
			Expect(second.Commit()).To(Succeed())

			Expect(read().HoldAcknowledged).To(BeTrue())
		})

		// Cancellation before Stage 2 forks on the RESERVATION, not the hold.
		// The unreserved form is the control and it is asserted first: it
		// really does close with no daemon call, which is what makes the
		// reserved form's owed release meaningful.
		It("owes a release for a cancellation over a reserved source and none for one before it", func() {
			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			status, err := repository.CancelOrSettle(ctx, tx, handoff)
			Expect(err).NotTo(HaveOccurred())
			Expect(*status.Disposition).To(Equal(output.DispositionPreReservationCancel))
			Expect(status.Settled).To(BeTrue(),
				"a cancellation with nothing on any node is closed on the spot")
			db.Rollback(tx)

			// And the same cancellation over a RESERVED source is not settled:
			// there is a directory on a node, and only that node can say it is
			// gone.
			reservedHandoff := output.HandoffID(uuid.NewString())
			reservedLease := output.SourceLeaseID(uuid.NewString())
			reservedExecution := identity()

			second, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(second)
			Expect(repository.PredeclareHandoff(ctx, second, output.CaptureAdmission{
				ProtocolVersion: output.ProtocolVersion,
				Execution:       reservedExecution,
				ActivationEpoch: 1,
				HandoffID:       reservedHandoff,
				SourceLeaseID:   reservedLease,
				Output:          name,
				CaptureDeadline: output.NewTimestamp(time.Now().Add(24 * time.Hour)),
			})).To(Succeed())
			Expect(repository.RecordSourceReservation(ctx, second,
				reserveFor(reservedHandoff, reservedLease, reservedExecution, name),
				"node-a")).To(Succeed())
			reservedStatus, err := repository.CancelOrSettle(ctx, second, reservedHandoff)
			Expect(err).NotTo(HaveOccurred())
			Expect(*reservedStatus.Disposition).To(Equal(output.DispositionPreReservationCancel))
			Expect(reservedStatus.Settled).To(BeFalse(),
				"a cancellation over a reserved incarnation was closed without releasing it; "+
					"the directory the ATC may already have mounted is still on the node")
			Expect(second.Commit()).To(Succeed())

			record := func() output.HandoffRecord {
				tx, err := dbConn.Begin()
				Expect(err).NotTo(HaveOccurred())
				defer db.Rollback(tx)
				loaded, err := repository.LoadHandoffRecord(ctx, tx, reservedHandoff)
				Expect(err).NotTo(HaveOccurred())

				return loaded
			}()
			Expect(record.ReleaseIntentID).ToNot(BeEmpty(),
				"the cancellation recorded no release intent, so nothing addresses the node")
			Expect(record.ReleaseAcknowledged).To(BeFalse())
		})

		// A terminal failure is a distinct fact from a cancellation: one is
		// something a caller asked for and the other is something that could
		// not be done. Both are terminal, neither creates a receipt.
		It("records a typed terminal failure and refuses one from a stale owner", func() {
			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			Expect(repository.RecordSourceReservation(ctx, tx,
				reserveFor(handoff, lease, execution, name), "node-a")).To(Succeed())
			Expect(repository.AcknowledgeSourceHold(ctx, tx,
				holdFor(handoff, lease, execution, name))).To(Succeed())
			reservation, err := repository.CommitCaptureReservation(ctx, tx,
				output.SuccessfulFinishDisposition{
					ProtocolVersion:       output.ProtocolVersion,
					Disposition:           output.DispositionCapture,
					Execution:             execution,
					ActivationEpoch:       1,
					HandoffID:             handoff,
					SourceLeaseID:         lease,
					ProducerCheckpointID:  "checkpoint",
					Output:                name,
					CaptureFence:          1,
					CaptureDeadline:       deadline,
					FinishAcknowledgement: finishFor(execution),
				})
			Expect(err).NotTo(HaveOccurred())
			Expect(tx.Commit()).To(Succeed())

			stale, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(stale)
			Expect(repository.RecordTerminalCaptureFailure(ctx, stale, reservation, 99,
				"seal_unconfirmed")).To(MatchError(output.ErrUnauthorized))
			db.Rollback(stale)

			owner, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(owner)
			Expect(repository.RecordTerminalCaptureFailure(ctx, owner, reservation, 1,
				"seal_unconfirmed")).To(Succeed())
			// The same failure again is the same failure.
			Expect(repository.RecordTerminalCaptureFailure(ctx, owner, reservation, 1,
				"seal_unconfirmed")).To(Succeed())
			Expect(owner.Commit()).To(Succeed())

			record := read()
			Expect(record.State).To(Equal(output.CaptureStateFailed))
			Expect(record.TerminalFailure).To(Equal("seal_unconfirmed"))
			Expect(record.Receipt).To(BeNil(),
				"a terminally failed capture produced a receipt")
			Expect(record.ReleaseIntentID).ToNot(BeEmpty(),
				"a capture that failed before the publish point owes a fenced release and "+
					"recorded no intent for it")
		})

		// Requirement 17's seal deadline, on the database clock. It is stamped
		// once and read in SQL, because the process that begins a seal is not
		// necessarily the process that has to say whether the boundary was
		// proved in time -- a capture crosses an ATC restart, and a deadline
		// held in a dead process's memory never expires.
		It("stamps the seal deadline once and answers whether it has passed", func() {
			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			Expect(repository.RecordSourceReservation(ctx, tx,
				reserveFor(handoff, lease, execution, name), "node-a")).To(Succeed())
			Expect(repository.AcknowledgeSourceHold(ctx, tx,
				holdFor(handoff, lease, execution, name))).To(Succeed())
			reservation, err := repository.CommitCaptureReservation(ctx, tx,
				output.SuccessfulFinishDisposition{
					ProtocolVersion:       output.ProtocolVersion,
					Disposition:           output.DispositionCapture,
					Execution:             execution,
					ActivationEpoch:       1,
					HandoffID:             handoff,
					SourceLeaseID:         lease,
					ProducerCheckpointID:  "checkpoint",
					Output:                name,
					CaptureFence:          1,
					CaptureDeadline:       deadline,
					FinishAcknowledgement: finishFor(execution),
				})
			Expect(err).NotTo(HaveOccurred())
			Expect(tx.Commit()).To(Succeed())

			// The control: before any seal begins there is no deadline, and
			// "has it passed" is false rather than vacuously true. Without
			// this, a predicate that answered true for everything would look
			// like an enforced deadline.
			unstamped, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(unstamped)
			Expect(repository.SealDeadlinePassed(ctx, unstamped, reservation)).To(BeFalse())

			// A stale owner does not get to set one.
			_, err = repository.RecordSealDeadline(ctx, unstamped, reservation, 99, time.Minute)
			Expect(err).To(MatchError(output.ErrUnauthorized))
			db.Rollback(unstamped)

			owner, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(owner)
			first, err := repository.RecordSealDeadline(ctx, owner, reservation, 1, time.Hour)
			Expect(err).NotTo(HaveOccurred())
			// A begin_seal repeated after a lost answer is the SAME seal, so
			// it inherits the deadline rather than granting itself a fresh
			// one -- which would be an unbounded seal wearing a bound.
			repeated, err := repository.RecordSealDeadline(ctx, owner, reservation, 1, time.Hour)
			Expect(err).NotTo(HaveOccurred())
			Expect(repeated.Time).To(Equal(first.Time))
			Expect(repository.SealDeadlinePassed(ctx, owner, reservation)).To(BeFalse())
			Expect(owner.Commit()).To(Succeed())

			// And a deadline in the past has passed, measured by `now()` in
			// SQL rather than by anything this process computed.
			elapsed, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(elapsed)
			_, err = elapsed.Exec(`
				UPDATE hangar_capture_reservations
				SET seal_deadline_at = now() - interval '1 minute'
				WHERE reservation_id = $1`, string(reservation))
			Expect(err).NotTo(HaveOccurred())
			Expect(repository.SealDeadlinePassed(ctx, elapsed, reservation)).To(BeTrue())
			Expect(elapsed.Commit()).To(Succeed())
		})

		// One read, one moment. The facts live in six tables and a coordinator
		// that read them one at a time would be deciding on a mixture of two
		// moments, which is the class of bug the whole plane exists to refuse.
		It("assembles the whole capture in one read", func() {
			reservation, ref := publish(hangarDigest(77), 771)

			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			var published string
			Expect(tx.QueryRow(`
				SELECT handoff_id FROM hangar_capture_reservations WHERE reservation_id = $1`,
				string(reservation)).Scan(&published)).To(Succeed())
			record, err := repository.LoadHandoffRecord(ctx, tx,
				output.HandoffID(published))
			Expect(err).NotTo(HaveOccurred())

			Expect(record.Source.Reserved()).To(BeTrue())
			Expect(record.HoldAcknowledged).To(BeTrue())
			Expect(record.Disposition).ToNot(BeNil())
			Expect(*record.Disposition).To(Equal(output.DispositionCapture))
			Expect(record.ReservationID).To(Equal(reservation))
			Expect(record.State).To(Equal(output.CaptureStateRegistered))
			Expect(record.LogicalResolved).To(BeTrue())
			Expect(record.Scope).To(Equal(ref.Scope))
			Expect(record.Digest).To(Equal(ref.Digest))
			Expect(record.Ref).To(Equal(ref))
			Expect(record.Receipt).ToNot(BeNil())
			// Not settled: the receipt is registered and the fenced release of
			// the source it sealed is still owed. `settled` means the same
			// thing on all three branches.
			Expect(record.Settled).To(BeFalse())
			Expect(record.ReleaseIntentID).ToNot(BeEmpty(),
				"a registered capture recorded no release intent for the source it sealed")
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

		// ROLLBACK AT EVERY STATEMENT, not at the one an author happened to
		// think of.
		//
		// The spec above injects its fault after both halves are already in the
		// transaction, which is the easy half: a composition that only ever
		// failed there could still leave a claim behind when it broke in the
		// middle of the acquire. So this counts the statements the WHOLE
		// composition runs -- the consumer's own writes and every statement
		// inside AcquireClaim -- and then runs it once per statement, aborting
		// AT that statement. Neither half may be visible afterwards, at any
		// index.
		//
		// The count is discovered rather than written down: a number in the
		// spec would go stale the first time the repository grew a statement,
		// and the spec would keep passing over the shorter prefix it knew.
		It("leaves neither half visible when it is rolled back at any statement", func() {
			claimID := output.ClaimID(uuid.NewString())

			compose := func(counter *countingTx, id output.ClaimID, binding string) error {
				if _, err := counter.ExecContext(ctx, `
					INSERT INTO opaque_consumer_bindings (binding_id, visibility, claim_id)
					VALUES ($1, 'hidden', $2)`, binding, string(id)); err != nil {
					return err
				}
				if err := repository.AcquireClaim(ctx, counter, output.ClaimAcquisition{
					ProtocolVersion:   output.ProtocolVersion,
					ClaimID:           id,
					Ref:               ref,
					ConsumerBindingID: output.OpaqueID(binding),
					RequestedAt:       output.NewTimestamp(time.Now()),
				}); err != nil {
					return err
				}
				_, err := counter.ExecContext(ctx, `
					UPDATE opaque_consumer_bindings SET visibility = 'published' WHERE binding_id = $1`,
					binding)

				return err
			}

			// One clean run, to learn how many statements there are.
			var total int
			func() {
				tx, err := dbConn.Begin()
				Expect(err).NotTo(HaveOccurred())
				defer db.Rollback(tx)

				counter := &countingTx{inner: tx}
				Expect(compose(counter, claimID, "binding-count")).To(Succeed())
				total = counter.count
			}()
			Expect(total).To(BeNumerically(">=", 4),
				"the composition runs too few statements for this spec to be saying anything")

			for at := 1; at <= total; at++ {
				id := output.ClaimID(uuid.NewString())
				binding := fmt.Sprintf("binding-at-%d", at)

				// The transaction is closed by a defer inside its own scope.
				// A failed assertion aborts the spec, and an aborted spec that
				// left this transaction open would hold a lock on the
				// consumer's own table until the cleanup DROP blocked on it --
				// a spec that reported a hang rather than a failure.
				func() {
					tx, err := dbConn.Begin()
					Expect(err).NotTo(HaveOccurred())
					defer db.Rollback(tx)

					counter := &countingTx{inner: tx, failAt: at}
					err = compose(counter, id, binding)
					Expect(err).To(HaveOccurred(),
						"statement %d was injected with a fault and the composition still succeeded", at)
					Expect(err.Error()).To(ContainSubstring("injected"),
						"statement %d failed for a reason this spec did not cause: %v", at, err)
				}()

				var bindings int
				Expect(dbConn.QueryRow(
					`SELECT count(*) FROM opaque_consumer_bindings WHERE binding_id = $1`, binding).
					Scan(&bindings)).To(Succeed())
				Expect(bindings).To(BeZero(),
					"the consumer's binding survived a rollback at statement %d", at)

				var claims int
				Expect(dbConn.QueryRow(`SELECT count(*) FROM hangar_claims WHERE claim_id = $1`,
					string(id)).Scan(&claims)).To(Succeed())
				Expect(claims).To(BeZero(),
					"a Hangar claim -- active or tombstoned -- survived a rollback at statement %d", at)
			}

			Expect(countActiveClaims(ref)).To(BeZero())
		})

		// The arrival inversion, with a consumer's own binding in it.
		//
		// The claimant-versus-reclaimer specs above prove which side wins. This
		// proves the thing AC 10 actually asks for and they cannot say: that
		// the loser leaves no DANGLING BINDING. A consumer whose Hangar half
		// failed and whose own half committed would have published a reference
		// to content nothing protects, which is exactly the outcome the shared
		// transaction exists to make impossible.
		It("leaves no dangling binding whichever of the claimant and the reclaimer arrives first", func() {
			// Reclaimer first: the consumer loses, and takes its own binding
			// down with it.
			reclaimer, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(reclaimer)
			Expect(repository.AdmitReclaim(ctx, reclaimer, ref, uuid.NewString(), 1,
				output.MinLeaseTerm)).To(Succeed())
			Expect(reclaimer.Commit()).To(Succeed())

			loser, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(loser)
			lateID := output.ClaimID(uuid.NewString())
			_, err = loser.Exec(`
				INSERT INTO opaque_consumer_bindings (binding_id, visibility, claim_id)
				VALUES ('binding-late', 'hidden', $1)`, string(lateID))
			Expect(err).NotTo(HaveOccurred())
			err = acquire(loser, lateID, ref, "binding-late")
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(err.Error()).To(ContainSubstring("reclaiming"))
			Expect(loser.Rollback()).To(Succeed())

			var dangling int
			Expect(dbConn.QueryRow(
				`SELECT count(*) FROM opaque_consumer_bindings WHERE binding_id = 'binding-late'`).
				Scan(&dangling)).To(Succeed())
			Expect(dangling).To(BeZero(),
				"the consumer's binding survived a claim the reclaimer had already won")
			Expect(lifecycleState(ref)).To(Equal("reclaiming"))

			// Claimant first, on a second generation: the consumer wins, its
			// binding is there, and the reclaimer rechecks under the lock and
			// skips.
			_, second := publish(hangarDigest(21), 1725830823000021)

			winner, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(winner)
			earlyID := output.ClaimID(uuid.NewString())
			_, err = winner.Exec(`
				INSERT INTO opaque_consumer_bindings (binding_id, visibility, claim_id)
				VALUES ('binding-early', 'hidden', $1)`, string(earlyID))
			Expect(err).NotTo(HaveOccurred())
			Expect(acquire(winner, earlyID, second, "binding-early")).To(Succeed())
			Expect(winner.Commit()).To(Succeed())

			late, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(late)
			err = repository.AdmitReclaim(ctx, late, second, uuid.NewString(), 1, output.MinLeaseTerm)
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(err.Error()).To(ContainSubstring("1 claim(s)"))
			Expect(late.Rollback()).To(Succeed())

			visibility, claim := binding("binding-early")
			Expect(visibility).To(Equal("hidden"))
			Expect(claim).To(Equal(string(earlyID)))
			Expect(countActiveClaims(second)).To(Equal(1))
			Expect(lifecycleState(second)).To(Equal("registered"))
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

// countingTx counts the statements a composition runs, and can fail at any one
// of them.
//
// It is the only honest way to say "rollback at EVERY statement": the
// composition's statements are not all the spec's -- most of them are inside
// AcquireClaim -- so a spec that injected its fault between the calls it can
// see would be asserting about three boundaries out of a dozen. Counting first
// and injecting by index makes the assertion cover whatever the repository
// currently does, and grow with it.
//
// The error is a plain one on purpose: what the spec checks is that the
// composition fails and leaves nothing, not that a particular sentinel came
// back out.
type countingTx struct {
	inner  db.Tx
	count  int
	failAt int
}

func (tx *countingTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	tx.count++
	if tx.failAt == tx.count {
		return nil, fmt.Errorf("injected fault at statement %d", tx.count)
	}

	return tx.inner.ExecContext(ctx, query, args...)
}

func (tx *countingTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	tx.count++
	if tx.failAt == tx.count {
		return nil, fmt.Errorf("injected fault at statement %d", tx.count)
	}

	return tx.inner.QueryContext(ctx, query, args...)
}

var _ output.Tx = (*countingTx)(nil)
