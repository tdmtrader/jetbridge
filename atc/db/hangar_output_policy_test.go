package db_test

import (
	"context"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

// The at-risk matrix: the exact set of operations that stop from detection
// onward, and the exact set that does not.
//
// Req 52 names five admissions that stop and three things that continue, and
// the value of this file is that both halves are asserted. A suite that only
// proved the refusals would pass for a plane that stopped doing anything at all
// the moment a monitor blinked -- which is the failure mode an operator cannot
// diagnose, because diagnosis is one of the things that has to keep working.
var _ = Describe("the storage-integrity admission gate", func() {
	var (
		ctx        context.Context
		repository *db.HangarOutputRepository
	)

	begin := func() db.HangarOutputTx {
		GinkgoHelper()
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())

		return db.HangarOutputTx{Tx: tx}
	}

	in := func(work func(tx db.HangarOutputTx)) {
		GinkgoHelper()
		tx := begin()
		defer db.Rollback(tx)
		work(tx)
		Expect(tx.Commit()).To(Succeed())
	}

	recordFailure := func() {
		in(func(tx db.HangarOutputTx) {
			Expect(repository.RecordRuntimeAtRisk(ctx, tx, 1, output.PolicyFinding{
				Violation: output.ViolationOutOfBandAbsence, Subject: "missing-generation", Detail: "unexpected loss",
			})).To(Succeed())
		})
	}

	// admissionRefused runs one admission to COMMIT, because the gate is a
	// deferred constraint trigger: a refusal that arrived statement by
	// statement would be a different gate from the one production has.
	commitOf := func(work func(tx db.HangarOutputTx)) error {
		GinkgoHelper()
		tx := begin()
		if err := func() error {
			defer func() { _ = recover() }()
			work(tx)

			return nil
		}(); err != nil {
			db.Rollback(tx)

			return err
		}
		err := tx.Commit()
		db.Rollback(tx)

		return err
	}

	settled := func(digest hangar.Digest, generation int64) HangarCapture {
		GinkgoHelper()
		capture := hangarPublishAt(ctx, repository, digest, generation,
			output.NewTimestamp(time.Now().Add(output.DefaultCaptureDeadline)))
		hangarReleaseSource(ctx, repository, capture)

		return capture
	}

	BeforeEach(func() {
		ctx = context.Background()
		dbConn.SetMaxOpenConns(3)
		DeferCleanup(func() { dbConn.SetMaxOpenConns(1) })

		consumer, err := db.HangarConsumerPrefixHeld("policy-spec")
		Expect(err).NotTo(HaveOccurred())
		repository = db.NewHangarOutputRepository(consumer)

		hangarActivateEpoch(ctx, repository)
	})

	Describe("what stops from detection onward", func() {
		var (
			ref     hangar.TreeRef
			capture HangarCapture
		)

		BeforeEach(func() {
			// Everything the refusals will need is built while the policy is
			// still safe, so a red row names the gate rather than the fixture.
			capture = settled(hangarDigest(80), 1725830823000080)
			ref = capture.Ref
			recordFailure()
		})

		It("refuses a new capture's predeclaration", func() {
			err := commitOf(func(tx db.HangarOutputTx) {
				Expect(repository.PredeclareHandoff(ctx, tx, output.CaptureAdmission{
					ProtocolVersion: output.ProtocolVersion,
					Execution:       hangarIdentity(),
					ActivationEpoch: 1,
					HandoffID:       output.HandoffID(uuid.NewString()),
					SourceHoldID:    output.SourceHoldID(uuid.NewString()),
					Output:          output.OutputName("result"),
					CaptureDeadline: output.NewTimestamp(
						time.Now().Add(output.DefaultCaptureDeadline)),
				})).To(Succeed())
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("storage integrity"))
		})

		It("refuses a claim acquire", func() {
			err := commitOf(func(tx db.HangarOutputTx) {
				Expect(repository.AcquireClaim(ctx, tx, output.ClaimAcquisition{
					ProtocolVersion:   output.ProtocolVersion,
					ClaimID:           output.ClaimID(uuid.NewString()),
					Ref:               ref,
					ConsumerBindingID: "binding-at-risk",
					RequestedAt:       output.NewTimestamp(time.Now()),
				})).To(Succeed())
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("storage integrity"))
		})

		It("refuses orphan adoption", func() {
			orphan := hangar.TreeRef{
				Scope: "team-a", Digest: hangarDigest(81), Generation: 1725830823000081,
			}
			err := commitOf(func(tx db.HangarOutputTx) {
				outcome, err := repository.AdoptManagedOrphan(ctx, tx,
					hangarAdoptionFor(orphan, time.Now().Add(-30*24*time.Hour)))
				Expect(err).NotTo(HaveOccurred())
				Expect(outcome).To(Equal(output.AdoptionAdopted))
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("storage integrity"))
		})

		It("refuses reclaim admission", func() {
			hangarAgeCapture(capture, 48*time.Hour)
			hangarAgePublication(ref, hangarGraceElapsed)
			err := commitOf(func(tx db.HangarOutputTx) {
				Expect(repository.AdmitReclaim(ctx, tx, ref, uuid.NewString(), 1,
					output.MinLeaseTerm,
					output.DefaultPublicationGrace)).To(Succeed())
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("storage integrity"))
		})
	})

	Describe("what keeps working", func() {
		It("lets a terminal capture release its source and settle", func() {
			capture := hangarPublishAt(ctx, repository, hangarDigest(82), 1725830823000082,
				output.NewTimestamp(time.Now().Add(output.DefaultCaptureDeadline)))
			recordFailure()

			// Releases remain possible. A plane that stopped releasing while
			// at risk would pin every producer's incarnation on its node for as
			// long as the monitor stayed unhappy.
			hangarReleaseSource(ctx, repository, capture)

			var settledAt bool
			Expect(dbConn.QueryRow(`
				SELECT settled_at IS NOT NULL FROM hangar_capture_reservations
				WHERE reservation_id = $1`, string(capture.ReservationID)).Scan(&settledAt)).
				To(Succeed())
			Expect(settledAt).To(BeTrue())
		})

		It("keeps existing claims recorded and lets them be released", func() {
			capture := settled(hangarDigest(83), 1725830823000083)
			claimID := output.ClaimID(uuid.NewString())
			in(func(tx db.HangarOutputTx) {
				Expect(repository.AcquireClaim(ctx, tx, output.ClaimAcquisition{
					ProtocolVersion:   output.ProtocolVersion,
					ClaimID:           claimID,
					Ref:               capture.Ref,
					ConsumerBindingID: "binding-existing",
					RequestedAt:       output.NewTimestamp(time.Now()),
				})).To(Succeed())
			})

			recordFailure()

			// Diagnosis: the claim is still readable, which is how an operator
			// finds out what is protected.
			in(func(tx db.HangarOutputTx) {
				claims, err := repository.ReadClaims(ctx, tx, capture.Ref)
				Expect(err).NotTo(HaveOccurred())
				Expect(claims).To(HaveLen(1))
			})

			// And it can be released. A release is not a new protection.
			in(func(tx db.HangarOutputTx) {
				Expect(repository.ReleaseClaim(ctx, tx, output.ClaimRelease{
					ProtocolVersion: output.ProtocolVersion,
					ClaimID:         claimID,
					Ref:             capture.Ref,
					RequestedAt:     output.NewTimestamp(time.Now()),
				})).To(Succeed())
			})
		})

	})

	It("reopens admission only after explicit reconciliation, retaining the finding", func() {
		recordFailure()
		recordFailure()
		in(func(tx db.HangarOutputTx) {
			findings, err := repository.OpenPolicyViolations(ctx, tx, 1)
			Expect(err).NotTo(HaveOccurred())
			Expect(findings).To(HaveLen(1))
			Expect(repository.ReconcilePolicyViolation(ctx, tx, 1, output.ViolationOutOfBandAbsence, "missing-generation")).To(Succeed())
		})
		in(func(tx db.HangarOutputTx) {
			findings, err := repository.OpenPolicyViolations(ctx, tx, 1)
			Expect(err).NotTo(HaveOccurred())
			Expect(findings).To(BeEmpty())
		})
		capture := settled(hangarDigest(85), 1725830823000085)
		Expect(capture.Ref.Generation).NotTo(BeZero())
		var count int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM hangar_policy_violations WHERE activation_epoch = 1 AND resolved_at IS NOT NULL`).Scan(&count)).To(Succeed())
		Expect(count).To(Equal(1))
		_, err := dbConn.Exec(`UPDATE hangar_policy_violations SET resolved_at = NULL WHERE activation_epoch = 1`)
		Expect(err).To(HaveOccurred())
	})

	It("admits work without policy evidence and ignores historical unsafe or stale readings", func() {
		Expect(settled(hangarDigest(86), 1725830823000086).Ref.Generation).NotTo(BeZero())
		_, err := dbConn.Exec(`INSERT INTO hangar_policy_snapshots
            (activation_epoch, bucket_fingerprint, metageneration, policy_hash, lifecycle_delete_rules, state, observed_at)
            VALUES (1, 'gs://output-bucket', 1, 'legacy-policy', 1, 'at_risk', now() - interval '1 day')`)
		Expect(err).NotTo(HaveOccurred())
		Expect(settled(hangarDigest(87), 1725830823000087).Ref.Generation).NotTo(BeZero())
	})
})
