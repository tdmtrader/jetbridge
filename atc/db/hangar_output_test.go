package db_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/hangar"
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
	// The fixture is package-level (hangar_output_fixture_test.go) because a
	// second file's specs need the same capture; these bindings keep every call
	// site below reading the way it did when it was a closure.
	activate := func() { hangarActivateEpoch(ctx, repository) }
	// publish, with its publication grace already elapsed on the database
	// clock. Every spec in this file that admits a reclamation needs that --
	// elapsed grace is one of Req 46's seven preconditions -- and a spec that
	// did not arrange it would be asserting the grace refusal under the name of
	// whatever else it was about.
	publish := func(digest hangar.Digest, generation int64) (output.CaptureKey, hangar.TreeRef) {
		GinkgoHelper()
		capture, ref := hangarPublish(ctx, repository, digest, generation)
		hangarAgePublication(ref, hangarGraceElapsed)

		return capture, ref
	}

	readLeaseRequest := hangarReadLeaseRequest

	acquire := func(tx db.Tx, id output.ClaimID, ref hangar.TreeRef, binding string) error {
		return repository.AcquireClaim(ctx, tx, output.ClaimAcquisition{
			ProtocolVersion:   output.ProtocolVersion,
			ClaimID:           id,
			Ref:               ref,
			ConsumerBindingID: output.OpaqueID(binding),
			RequestedAt:       output.NewTimestamp(time.Now()),
		})
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

			err = repository.AdmitReclaim(ctx, reclaimer, ref, uuid.NewString(), 1, output.MinLeaseTerm,
				output.DefaultPublicationGrace)
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
				output.MinLeaseTerm,
				output.DefaultPublicationGrace)).To(Succeed())
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
					output.MinLeaseTerm,
					output.DefaultPublicationGrace)
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
			err = repository.AdmitReclaim(ctx, reclaimer, ref, uuid.NewString(), 1, output.MinLeaseTerm,
				output.DefaultPublicationGrace)
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
				output.MinLeaseTerm,
				output.DefaultPublicationGrace)).To(Succeed())
			Expect(reclaimer.Commit()).To(Succeed())
		})

		It("refuses a warrant for a ref that is already reclaiming", func() {
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
				output.MinLeaseTerm,
				output.DefaultPublicationGrace)).To(Succeed())
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
	// Requirement 35 names four things that must hold together before a warrant
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

			// And what the warrant will bind is stored, so a re-mint is the same
			// bytes rather than a second lease.
			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			record, err := repository.LoadReadLease(ctx, tx, id)
			Expect(err).NotTo(HaveOccurred())
			Expect(record.WarrantNonce).To(Equal(request.WarrantNonce))
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
			Entry("no nonce for the warrant", output.ErrIncomplete, "read warrant nonce",
				func(request *output.ReadLeaseRequest) { request.WarrantNonce = "" }),
			// The term's CEILING, refused where the policy is read rather than
			// by the column's CHECK. Both refusals are typed ErrIncomplete, so
			// what tells them apart is which sentence comes back: the schema's
			// is the constraint's text, and a caller cannot act on it.
			Entry("a timeout whose term would outrun the bound", output.ErrIncomplete,
				"the bound is",
				func(request *output.ReadLeaseRequest) {
					request.MaterializationTimeout = 24 * time.Hour
				}),
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

		It("refuses a read for a ref no publication registered", func() {
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
		// one would be a warrant for content the plane has said is not there.
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

		// "There is no claim" and "I could not ask" are different answers.
		//
		// The claim lookup wrapped every failure as ErrNotFound, so a database
		// the transaction could not reach came back as "no claim protects this
		// ref" -- and a consumer reads that as "my binding is gone" and stops.
		// The failure is induced by renaming the table INSIDE the transaction
		// that then asks, which is a real undefined-table failure from the real
		// driver, and it is rolled back with the transaction.
		It("tells a claim that is absent from a claim lookup that failed", func() {
			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)

			// The COLUMN and not the table: the lock suffix takes
			// `SELECT 1 FROM hangar_claims ... FOR UPDATE` first, so renaming
			// the table would fail the lock and this spec would be asserting
			// the helper's error instead of the lookup's.
			_, err = tx.Exec(`ALTER TABLE hangar_claims DROP COLUMN released_at CASCADE`)
			Expect(err).NotTo(HaveOccurred())

			_, err = repository.AcquireReadLease(ctx, tx,
				readLeaseRequest(output.ReadLeaseID(uuid.NewString()), claimID, ref))
			Expect(err).To(HaveOccurred())
			Expect(err).To(MatchError(output.ErrInfrastructure))
			Expect(err).NotTo(MatchError(output.ErrNotFound),
				"a lookup that could not run was reported as an absent claim")
			Expect(tx.Rollback()).To(Succeed())

			// The control, on the same fixture: with the table where it
			// belongs, an absent claim really is a typed not-found.
			absent, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(absent)
			request := readLeaseRequest(output.ReadLeaseID(uuid.NewString()),
				output.ClaimID(uuid.NewString()), ref)
			_, err = repository.AcquireReadLease(ctx, absent, request)
			Expect(err).To(MatchError(output.ErrNotFound))
			Expect(absent.Rollback()).To(Succeed())
		})

		It("refuses a read while the epoch's lifetime policy is at risk", func() {
			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			Expect(repository.RecordRuntimeAtRisk(ctx, tx, output.PolicyFinding{Violation: output.ViolationOutOfBandAbsence, Subject: "missing-generation", Detail: "unexpected object loss"})).To(Succeed())
			Expect(tx.Commit()).To(Succeed())

			// The refusal is DEFERRED: it fires at the commit, not at the
			// insert, so what makes it a refusal rather than a lost answer is
			// the transaction adapter's mapping of the schema's class. A
			// substring on the message could not tell the two apart.
			_, err = admit(readLeaseRequest(output.ReadLeaseID(uuid.NewString()), claimID, ref))
			Expect(err).To(MatchError(output.ErrAtRisk))
			Expect(err.Error()).To(ContainSubstring("storage integrity"))
		})

		// The daemon's independent question, asked of the committed row rather
		// than of the token.
		Describe("validating the lease a warrant names", func() {
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
					ClaimID:           claimID,
					Ref:               ref,
					Destination:       request.Destination,
					ActivationEpoch:   1,
					WarrantNonce:      request.WarrantNonce,
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
				Expect(record.WarrantNonce).To(Equal(request.WarrantNonce))
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

			DescribeTable("refuses a warrant that does not describe the committed lease",
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
						nonce, err := output.NewReadWarrantNonce(rand.Reader)
						Expect(err).NotTo(HaveOccurred())
						question.WarrantNonce = nonce
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
					output.MinLeaseTerm,
					output.DefaultPublicationGrace)
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
					output.MinLeaseTerm,
					output.DefaultPublicationGrace)).To(Succeed())
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
			It("warrants exactly one term from now, however often it is renewed", func() {
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
			// on the LeaseControlStore port for any caller, and a method
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

			// RECOVERY CLOSES THE ABANDONED ONE AND NOTHING ELSE.
			//
			// The spec above has a single lease and it is already expired, so
			// `closed == 1` cannot tell "closed what the database says has run
			// out" from "closed everything unreleased" -- and the second is a
			// recovery pass that ends every in-flight read on the node. So this
			// one runs the pass against two leases at once and asserts the
			// survivor by asking the production validator, not by reading a
			// column: a lease that still validates is a lease a daemon may
			// still stage under.
			It("closes the abandoned lease and leaves a live one alone", func() {
				live := output.ReadLeaseID(uuid.NewString())
				liveRequest := readLeaseRequest(live, claimID, ref)
				liveLease, err := admit(liveRequest)
				Expect(err).NotTo(HaveOccurred())

				_, err = dbConn.Exec(`
					UPDATE hangar_read_leases
					SET granted_at = granted_at - interval '1 hour',
					    renewed_at = renewed_at - interval '1 hour',
					    expires_at = expires_at - interval '1 hour'
					WHERE read_lease_id = $1`, string(id))
				Expect(err).NotTo(HaveOccurred())

				closing, err := dbConn.Begin()
				Expect(err).NotTo(HaveOccurred())
				defer db.Rollback(closing)
				closed, err := repository.CloseAbandonedReadLeases(ctx, closing, 100)
				Expect(err).NotTo(HaveOccurred())
				Expect(closed).To(Equal(1),
					"recovery closed more than the one lease the database says has expired")
				Expect(closing.Commit()).To(Succeed())

				var released []string
				rows, err := dbConn.Query(`
					SELECT read_lease_id FROM hangar_read_leases WHERE released_at IS NOT NULL`)
				Expect(err).NotTo(HaveOccurred())
				defer rows.Close()
				for rows.Next() {
					var closedID string
					Expect(rows.Scan(&closedID)).To(Succeed())
					released = append(released, closedID)
				}
				Expect(rows.Err()).NotTo(HaveOccurred())
				Expect(released).To(ConsistOf(string(id)))

				// The live one still authorizes work, through the method a
				// daemon's question really goes through.
				validating, err := dbConn.Begin()
				Expect(err).NotTo(HaveOccurred())
				defer db.Rollback(validating)
				_, err = repository.ValidateReadLease(ctx, validating, output.ReadLeaseValidation{
					ReadLeaseID:       live,
					ClaimID:           claimID,
					Ref:               ref,
					Destination:       liveRequest.Destination,
					ActivationEpoch:   1,
					WarrantNonce:      liveRequest.WarrantNonce,
					RequiredRemaining: time.Minute,
				})
				Expect(err).NotTo(HaveOccurred(),
					"recovery closed a live reader's protection out from under it")
				Expect(liveLease.ReadLeaseID).To(Equal(live))

				// And the generation is still pinned: a reclaimer arriving now
				// meets the survivor.
				reclaiming, err := dbConn.Begin()
				Expect(err).NotTo(HaveOccurred())
				defer db.Rollback(reclaiming)
				err = repository.AdmitReclaim(ctx, reclaiming, ref, uuid.NewString(), 1,
					output.MinLeaseTerm,
					output.DefaultPublicationGrace)
				Expect(err).To(MatchError(output.ErrConflict))
				Expect(err.Error()).To(ContainSubstring("1 read lease(s)"))
				Expect(reclaiming.Rollback()).To(Succeed())
			})

			// RECOVERY VERSUS A RENEWAL, IN BOTH ARRIVAL ORDERS.
			//
			// The two run against each other for real here, on two
			// connections, because the interleaving is not reachable from one:
			// each transaction needs to be open while the other decides.
			//
			// What makes the first order possible at all is that `now()` is
			// `transaction_timestamp()`. A renewal whose transaction OPENED
			// while the lease was live still sees it live at its own now(),
			// while a recovery transaction that starts later reads the same
			// committed row as expired -- so a recovery pass really can pick up
			// a lease that a renewal is about to extend. The candidate SELECT
			// runs unlocked, by necessity: there is no identity to lock until
			// something has been selected. It is the predicate REPEATED under
			// the lock that saves the live reader, and nothing asserted that
			// repetition: deleting it leaves this suite green.
			//
			// The recovery pass is blocked on the renewal's row lock at the
			// moment the renewal commits, which is asserted rather than assumed
			// -- a spec that let the renewal commit first would be watching two
			// transactions that never met.
			It("leaves alone a lease that was renewed while recovery waited for its row", func() {
				renewing := postgresRunner.OpenConn()
				DeferCleanup(func() { Expect(renewing.Close()).To(Succeed()) })

				renewal, err := renewing.Begin()
				Expect(err).NotTo(HaveOccurred())
				defer db.Rollback(renewal)

				// The renewal's own now(), fixed by its first statement.
				var opened time.Time
				Expect(renewal.QueryRow(`SELECT transaction_timestamp()`).Scan(&opened)).
					To(Succeed())

				// Live at that instant, expired at any later transaction's.
				// All three instants move together, so the row keeps the
				// fifteen-minute term it was admitted under -- the schema
				// refuses a shorter one, and shortening a lease to make a race
				// reachable would be a different spec.
				deadline := opened.Add(200 * time.Millisecond)
				_, err = dbConn.Exec(`
					UPDATE hangar_read_leases
					SET granted_at = $2::timestamptz - interval '15 minutes',
					    renewed_at = $2::timestamptz - interval '15 minutes',
					    expires_at = $2
					WHERE read_lease_id = $1`, string(id), deadline)
				Expect(err).NotTo(HaveOccurred())

				Eventually(func() bool {
					var past bool
					Expect(dbConn.QueryRow(`SELECT now() > $1`, deadline).Scan(&past)).
						To(Succeed())

					return past
				}, 10*time.Second, 20*time.Millisecond).Should(BeTrue(),
					"the database clock never passed the expiry this spec set")

				// The renewal: admitted on its own clock, holding the row,
				// uncommitted.
				renewed, err := repository.RenewReadLease(ctx, renewal, lease)
				Expect(err).NotTo(HaveOccurred(),
					"the renewal was refused, so this is no longer the race it is named for")
				Expect(renewed.ExpiresAt.After(deadline)).To(BeTrue())

				closed := make(chan int, 1)
				done := make(chan error, 1)
				go func() {
					defer GinkgoRecover()
					tx, err := dbConn.Begin()
					if err != nil {
						done <- err

						return
					}
					defer db.Rollback(tx)
					count, err := repository.CloseAbandonedReadLeases(ctx, tx, 100)
					if err != nil {
						done <- err

						return
					}
					if err := tx.Commit(); err != nil {
						done <- err

						return
					}
					closed <- count
					done <- nil
				}()

				// It has read its candidates -- the committed row is expired --
				// and it is now waiting on the lock the renewal holds.
				Consistently(done, 500*time.Millisecond, 50*time.Millisecond).ShouldNot(Receive(),
					"recovery finished without ever meeting the renewal's row lock")

				Expect(renewal.Commit()).To(Succeed())

				var failure error
				Eventually(done, 10*time.Second).Should(Receive(&failure))
				Expect(failure).NotTo(HaveOccurred())
				Expect(closed).To(Receive(Equal(0)),
					"recovery closed a lease that was renewed while it waited; the decision came "+
						"from the candidate read taken before the row was held")

				// And the survivor is a lease a daemon may still stage under.
				validating, err := dbConn.Begin()
				Expect(err).NotTo(HaveOccurred())
				defer db.Rollback(validating)
				_, err = repository.ValidateReadLease(ctx, validating, validation())
				Expect(err).NotTo(HaveOccurred(),
					"the renewed lease no longer authorizes the read it protects")
			})

			// The other arrival order: recovery holds the row first, and the
			// renewal waits for it. A renewal that came back admitted here
			// would resurrect a lease recovery has already closed -- the
			// generation would be re-pinned by a reader the plane has decided
			// is gone, and the tombstone recovery wrote would be the only
			// record that it ever happened.
			//
			// The refusal CLASS is pinned by the expired-lease specs beside
			// this one; what this adds is the interleaving. The renewal is
			// still blocked on recovery's row at the moment recovery commits,
			// which is asserted, so its answer is taken from the row as
			// recovery left it and not from the reading it had before it
			// waited.
			It("refuses a renewal that waited for the recovery pass that closed its lease", func() {
				_, err := dbConn.Exec(`
					UPDATE hangar_read_leases
					SET granted_at = granted_at - interval '1 hour',
					    renewed_at = renewed_at - interval '1 hour',
					    expires_at = expires_at - interval '1 hour'
					WHERE read_lease_id = $1`, string(id))
				Expect(err).NotTo(HaveOccurred())

				recovery, err := dbConn.Begin()
				Expect(err).NotTo(HaveOccurred())
				defer db.Rollback(recovery)
				count, err := repository.CloseAbandonedReadLeases(ctx, recovery, 100)
				Expect(err).NotTo(HaveOccurred())
				Expect(count).To(Equal(1))

				renewing := postgresRunner.OpenConn()
				DeferCleanup(func() { Expect(renewing.Close()).To(Succeed()) })
				renewal, err := renewing.Begin()
				Expect(err).NotTo(HaveOccurred())
				defer db.Rollback(renewal)

				done := make(chan error, 1)
				go func() {
					defer GinkgoRecover()
					_, err := repository.RenewReadLease(ctx, renewal, lease)
					done <- err
				}()

				Consistently(done, 500*time.Millisecond, 50*time.Millisecond).ShouldNot(Receive(),
					"the renewal answered without waiting for the row recovery was holding, so "+
						"it decided from a read taken outside the suffix")

				Expect(recovery.Commit()).To(Succeed())

				var failure error
				Eventually(done, 10*time.Second).Should(Receive(&failure))
				Expect(failure).To(MatchError(output.ErrConflict),
					"the renewal resurrected a lease recovery had closed")
				Expect(renewal.Rollback()).To(Succeed())

				var released bool
				Expect(dbConn.QueryRow(`
					SELECT released_at IS NOT NULL FROM hangar_read_leases
					WHERE read_lease_id = $1`, string(id)).Scan(&released)).To(Succeed())
				Expect(released).To(BeTrue())
			})

			It("refuses a released lease even though its warrant is still signed", func() {
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
				Expect(other.WarrantNonce).NotTo(Equal(request.WarrantNonce))
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

	Describe("publication versus claim acquire", func() {
		It("refuses a claim on a ref no publication has registered, and admits it once one has", func() {
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

	Describe("a correlation an unresolved capture still protects", func() {
		It("shows no consumer result in the gap before a generation is published", func() {
			activate()

			// A capture that has written its digest but not yet published a
			// generation.
			hangarReserve(ctx, repository, hangarDigest(4), output.DefaultCaptureDeadline)

			orphan := hangar.TreeRef{
				Scope:      "team-a",
				Digest:     hangarDigest(4),
				Generation: 1725830823000004,
			}

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
				output.MinLeaseTerm,
				output.DefaultPublicationGrace)).To(Succeed())
			Expect(reclaimer.Commit()).To(Succeed())

			Expect(lifecycleState(first)).To(Equal("registered"))
			Expect(lifecycleState(second)).To(Equal("reclaiming"))
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
				SELECT 1 FROM hangar_captures
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
			// Two captures of one tree that published two generations.
			a := hangarPublishAt(ctx, repository, digest, 9, output.DefaultCaptureDeadline).Ref
			second := hangarPublishAt(ctx, repository, digest, 10, output.DefaultCaptureDeadline).Ref

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
			Expect(locks.Exact).To(HaveLen(2), "the duplicated tree ref was locked twice")
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
				output.MinLeaseTerm,
				output.DefaultPublicationGrace)).To(Succeed())
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
			err = repository.AdmitReclaim(ctx, late, second, uuid.NewString(), 1, output.MinLeaseTerm,
				output.DefaultPublicationGrace)
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
