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
// Five admissions stop and three things continue, and
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
			Expect(repository.RecordRuntimeAtRisk(ctx, tx, output.IntegrityFindingRecord{
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
			output.DefaultCaptureDeadline)
		hangarReleaseCaptureClaim(ctx, repository, capture)
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

		It("refuses a new capture", func() {
			err := commitOf(func(tx db.HangarOutputTx) {
				_, err := repository.InsertPending(ctx, tx, output.PendingCapture{
					Execution: hangarIdentity(), Output: "result",
					Node: "node-a", NodeUID: "node-uid", Term: output.DefaultCaptureDeadline,
				})
				Expect(err).NotTo(HaveOccurred())
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
				output.DefaultCaptureDeadline)
			recordFailure()

			// Releases remain possible. A plane that stopped releasing while
			// at risk would pin every producer's incarnation on its node for as
			// long as the monitor stayed unhappy.
			hangarReleaseSource(ctx, repository, capture)

			var released bool
			Expect(dbConn.QueryRow(`
				SELECT released_at IS NOT NULL FROM hangar_captures
				WHERE execution_id = $1 AND output_name = $2`,
				string(capture.Key.ExecutionID), string(capture.Key.Output)).Scan(&released)).
				To(Succeed())
			Expect(released).To(BeTrue())
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
				// The capture's own claim is a tombstone beside it; the
				// consumer's is the one still active.
				var active []output.ClaimID
				for _, claim := range claims {
					if claim.Active() {
						active = append(active, claim.ClaimID)
					}
				}
				Expect(active).To(ConsistOf(claimID))
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

	It("reopens admission only after an explicit resolution by id, retaining the finding", func() {
		recordFailure()
		recordFailure()
		in(func(tx db.HangarOutputTx) {
			findings, err := repository.OpenIntegrityFindings(ctx, tx)
			Expect(err).NotTo(HaveOccurred())
			Expect(findings).To(HaveLen(1))
			Expect(findings[0].Violation).To(Equal(output.ViolationOutOfBandAbsence))
			Expect(findings[0].Subject).To(Equal("missing-generation"))
			Expect(findings[0].BlocksAdmission).To(BeTrue())
			Expect(repository.ResolveIntegrityFinding(ctx, tx, findings[0].ID)).To(Succeed())
			Expect(repository.ResolveIntegrityFinding(ctx, tx, findings[0].ID)).
				To(Succeed(), "resolving a resolved finding again is idempotent")
			Expect(repository.ResolveIntegrityFinding(ctx, tx, findings[0].ID+1000000)).
				To(MatchError(output.ErrNotFound), "no such finding")
		})
		in(func(tx db.HangarOutputTx) {
			findings, err := repository.OpenIntegrityFindings(ctx, tx)
			Expect(err).NotTo(HaveOccurred())
			Expect(findings).To(BeEmpty())
		})
		capture := settled(hangarDigest(85), 1725830823000085)
		Expect(capture.Ref.Generation).NotTo(BeZero())
		var count int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM hangar_integrity_findings WHERE resolved_at IS NOT NULL`).Scan(&count)).To(Succeed())
		Expect(count).To(Equal(1))
		_, err := dbConn.Exec(`UPDATE hangar_integrity_findings SET resolved_at = NULL`)
		Expect(err).To(HaveOccurred())
	})

	It("admits work without policy evidence", func() {
		Expect(settled(hangarDigest(86), 1725830823000086).Ref.Generation).NotTo(BeZero())
		Expect(settled(hangarDigest(87), 1725830823000087).Ref.Generation).NotTo(BeZero())
	})
})
