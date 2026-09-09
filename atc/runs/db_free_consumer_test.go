package runs_test

import (
	"context"
	"errors"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/runs"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// This file is A10: a consumer of the port that cannot see atc/db.
//
// Its import block is exactly context, errors, atc, atc/runs, ginkgo and
// gomega. If anything below required an atc/db type -- opening the
// transaction, committing it, rolling it back, admitting, hooking
// before-commit, reading the run's identity back, or telling one refusal from
// another -- the port would not have re-expressed the seam and D8 would have
// failed. composition_boundary_test.go asserts this file's import block from
// the outside, so the property survives an inattentive edit.
//
// It does NOT construct the port: NewAdmitter names three atc/db types by
// necessity (a constructor over CreateRunInTx cannot avoid naming the
// collaborator that owns it), so the suite file constructs it and this file
// uses the already-constructed value. That is the boundary, not a hole in it:
// the composition root builds the port, consumers only use it.
var _ = Describe("a consumer that cannot see atc/db", func() {
	var ctx context.Context

	// Any non-empty value will do. The port checks presence and nothing else
	// (requirement 8): it does not verify the key was recorded anywhere,
	// because the call row lives in a consumer table core must never read.
	const contractKey = "a10-consumer/some-call"

	BeforeEach(func() {
		ctx = context.Background()
	})

	admit := func(tx runs.Tx, adm runs.Admission) (runs.Run, error) {
		if adm.ContractKey == "" {
			adm.ContractKey = contractKey
		}

		return admitter.AdmitRun(ctx, tx, adm)
	}

	Describe("the transaction lifecycle, through the port alone", func() {
		It("opens a transaction and commits it", func() {
			tx, err := admitter.Begin(ctx)
			Expect(err).NotTo(HaveOccurred())

			_, err = tx.ExecContext(ctx, "INSERT INTO "+scratchTable+" (note) VALUES ($1)", "committed")
			Expect(err).NotTo(HaveOccurred())
			Expect(tx.Commit()).To(Succeed())

			Expect(countScratch("committed")).To(Equal(1))
		})

		It("opens a transaction and rolls it back", func() {
			tx, err := admitter.Begin(ctx)
			Expect(err).NotTo(HaveOccurred())

			_, err = tx.ExecContext(ctx, "INSERT INTO "+scratchTable+" (note) VALUES ($1)", "rolled-back")
			Expect(err).NotTo(HaveOccurred())
			Expect(tx.Rollback()).To(Succeed())

			Expect(countScratch("rolled-back")).To(Equal(0))
		})
	})

	Describe("admitting a run", func() {
		It("admits, runs the before-commit hook in the same transaction, and reports the run's identity", func() {
			tx, err := admitter.Begin(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer tx.Rollback()

			var hookRun runs.Run
			run, err := admit(tx, runs.Admission{
				Template:  templateRef,
				Principal: memberPrincipal,
				BeforeCommit: func(hookTx runs.Tx, created runs.Run) error {
					hookRun = created
					_, err := hookTx.ExecContext(ctx,
						"INSERT INTO "+scratchTable+" (note) VALUES ($1)", "hooked")

					return err
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// The identity the port reports back, with no atc/db model in sight.
			Expect(run.ID).To(BeNumerically(">", 0))
			Expect(run.Number).To(Equal(1))
			Expect(run.TemplatePipelineID).To(BeNumerically(">", 0))
			Expect(run.PayloadPipelineID).To(BeNumerically(">", 0))
			Expect(run.PayloadPipelineID).NotTo(Equal(run.TemplatePipelineID))
			Expect(run.CreatedBy).To(Equal("member-id"))

			// The hook saw the same run, before the caller committed.
			Expect(hookRun).To(Equal(run))

			Expect(tx.Commit()).To(Succeed())
			Expect(countScratch("hooked")).To(Equal(1))
			Expect(countRuns(run.ID)).To(Equal(1))
		})

		It("aborts creation when the before-commit hook fails", func() {
			tx, err := admitter.Begin(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer tx.Rollback()

			refused := errors.New("the consumer said no")
			_, err = admit(tx, runs.Admission{
				Template:     templateRef,
				Principal:    memberPrincipal,
				BeforeCommit: func(runs.Tx, runs.Run) error { return refused },
			})
			Expect(err).To(MatchError(refused))
		})

		// P2.9: the transaction really is the caller's. A port that quietly
		// opened one of its own would leave the run behind after this rollback.
		It("leaves no run behind when the caller rolls back", func() {
			tx, err := admitter.Begin(ctx)
			Expect(err).NotTo(HaveOccurred())

			run, err := admit(tx, runs.Admission{Template: templateRef, Principal: memberPrincipal})
			Expect(err).NotTo(HaveOccurred())
			Expect(tx.Rollback()).To(Succeed())

			Expect(countRuns(run.ID)).To(Equal(0))
		})

		// The port's other form of identity, named from a consumer that cannot
		// see atc/db. A build principal is exactly the case that has no
		// counterpart in the request accessor's world -- there is no token,
		// no claims map and no role -- so if any part of expressing one
		// required a core type, the consumer this file stands in for could not
		// build it either. It does not: a team name, three names for the
		// record, and an id.
		It("admits as a build acting for itself", func() {
			tx, err := admitter.Begin(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer tx.Rollback()

			run, err := admit(tx, runs.Admission{
				Template: templateRef,
				Principal: runs.Principal{Build: &runs.BuildPrincipal{
					TeamName:     "runs-team",
					PipelineName: "caller",
					JobName:      "release",
					BuildName:    "42",
					BuildID:      1,
				}},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(run.CreatedBy).To(Equal("build:runs-team/caller/release#42"))

			Expect(tx.Commit()).To(Succeed())
			Expect(countRuns(run.ID)).To(Equal(1))
		})

		It("carries params through to the admitted run", func() {
			tx, err := admitter.Begin(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer tx.Rollback()

			run, err := admit(tx, runs.Admission{
				Template:  paramsRef,
				Principal: memberPrincipal,
				Params:    atc.RunParams{"target": "staging"},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(run.ID).To(BeNumerically(">", 0))
		})
	})

	// The read half of the port, from the same consumer. This is the operation
	// that keeps a consumer out of pipeline_runs: it recorded a run id in a
	// table of its own, it comes back holding that id and nothing else, and
	// what it needs -- the run's number -- is on a core table. Without this it
	// would write the SELECT itself, and the coupling would be invisible to
	// any import graph because SQL names no packages.
	Describe("reading a run back through the port", func() {
		It("returns the identity admission returned, from an id alone", func() {
			tx, err := admitter.Begin(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer tx.Rollback()

			admitted, err := admit(tx, runs.Admission{Template: templateRef, Principal: memberPrincipal})
			Expect(err).NotTo(HaveOccurred())

			looked, err := admitter.LookupRun(ctx, tx, admitted.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(looked).To(Equal(admitted))
			Expect(looked.Number).To(Equal(1))
		})

		It("refuses an id that names no run", func() {
			tx, err := admitter.Begin(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer tx.Rollback()

			_, err = admitter.LookupRun(ctx, tx, 999999)
			Expect(err).To(BeIdenticalTo(runs.ErrRunNotFound))
		})
	})

	Describe("telling the refusals apart", func() {
		refusalFor := func(adm runs.Admission) error {
			GinkgoHelper()
			tx, err := admitter.Begin(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer tx.Rollback()

			_, err = admit(tx, adm)
			Expect(err).To(HaveOccurred())

			return err
		}

		// BeIdenticalTo rather than MatchError wherever the port's sentinel
		// shares its message with the atc/db value it re-expresses (here,
		// ErrNotATemplate and db.ErrPipelineRunNotTemplate are both "pipeline
		// is not a template"). MatchError compares the error values and so
		// succeeds for two errors.New sentinels of equal text, which makes it
		// blind to a raw atc/db error leaking through untranslated -- the one
		// failure these specs exist to catch. Pointer identity is not blind to
		// it. This file cannot name the atc/db value to assert the difference
		// directly, which is exactly why it asserts the port's own by identity.
		It("distinguishes an unknown template from one that is not a template", func() {
			Expect(refusalFor(runs.Admission{Template: unknownRef, Principal: memberPrincipal})).
				To(BeIdenticalTo(runs.ErrTemplateNotFound))
			Expect(refusalFor(runs.Admission{Template: ordinaryRef, Principal: memberPrincipal})).
				To(BeIdenticalTo(runs.ErrNotATemplate))
		})

		// D3 rules that a waiting parent whose re-admission hits a paused
		// template keeps waiting rather than erroring. That decision is only
		// expressible if paused arrives as its own value, distinct from
		// archived and from gone.
		It("distinguishes paused from archived, and neither from gone", func() {
			paused := refusalFor(runs.Admission{Template: pausedRef, Principal: memberPrincipal})
			archived := refusalFor(runs.Admission{Template: archivedRef, Principal: memberPrincipal})

			Expect(paused).To(BeIdenticalTo(runs.ErrTemplatePaused))
			Expect(paused).NotTo(MatchError(runs.ErrTemplateArchived))
			Expect(paused).NotTo(MatchError(runs.ErrTemplateNotFound))

			Expect(archived).To(BeIdenticalTo(runs.ErrTemplateArchived))
			Expect(archived).NotTo(MatchError(runs.ErrTemplatePaused))
			Expect(archived).NotTo(MatchError(runs.ErrTemplateNotFound))
		})

		It("reports invalid run params as their own refusal, with the reason", func() {
			err := refusalFor(runs.Admission{
				Template:  paramsRef,
				Principal: memberPrincipal,
				Params:    atc.RunParams{"not-declared": "x"},
			})

			var invalid runs.InvalidParamsError
			Expect(errors.As(err, &invalid)).To(BeTrue())
			Expect(invalid.Error()).To(ContainSubstring("not-declared"))
		})

		// A malformed principal is its own refusal, distinguishable from an
		// unauthorized one without any core type in sight. A consumer needs
		// the distinction: unauthorized is a fact about the caller's standing
		// that it may report and stop on, ambiguous is a bug in the consumer
		// that assembled the principal.
		It("distinguishes a principal that is not one identity from one that is not authorized", func() {
			both := refusalFor(runs.Admission{
				Template: templateRef,
				Principal: runs.Principal{
					Claims: claimsFor("member-user", "member-id"),
					Build:  &runs.BuildPrincipal{TeamName: "runs-team", BuildName: "42", BuildID: 1},
				},
			})
			Expect(both).To(BeIdenticalTo(runs.ErrPrincipalAmbiguous))
			Expect(both).NotTo(MatchError(runs.ErrUnauthorized))

			neither := refusalFor(runs.Admission{Template: templateRef, Principal: runs.Principal{}})
			Expect(neither).To(BeIdenticalTo(runs.ErrPrincipalAmbiguous))
		})

		It("refuses an unauthorized principal without saying whether the template exists", func() {
			// Same team, real template.
			Expect(refusalFor(runs.Admission{Template: otherTeamRef, Principal: memberPrincipal})).
				To(MatchError(runs.ErrUnauthorized))

			// A team that does not exist at all answers identically, so
			// admission is not an existence oracle for another team's names.
			Expect(refusalFor(runs.Admission{
				Template:  runs.TemplateRef{Team: "no-such-team", Pipeline: atc.PipelineRef{Name: "whatever"}},
				Principal: memberPrincipal,
			})).To(MatchError(runs.ErrUnauthorized))
		})
	})

	Describe("the contract key", func() {
		It("refuses an empty key before touching a row", func() {
			tx, err := admitter.Begin(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer tx.Rollback()

			_, err = admitter.AdmitRun(ctx, tx, runs.Admission{
				Template:  templateRef,
				Principal: memberPrincipal,
			})
			Expect(err).To(MatchError(runs.ErrMissingContractKey))
		})
	})
})

// countScratch and countRuns read through the port's own transaction handle:
// the consumer has no atc/db connection and does not want one.
func countScratch(note string) int {
	GinkgoHelper()

	return countThrough("SELECT count(*) FROM "+scratchTable+" WHERE note = $1", note)
}

func countRuns(runID int) int {
	GinkgoHelper()

	return countThrough("SELECT count(*) FROM pipeline_runs WHERE id = $1", runID)
}

func countThrough(query string, arg any) int {
	GinkgoHelper()
	ctx := context.Background()

	tx, err := admitter.Begin(ctx)
	Expect(err).NotTo(HaveOccurred())
	defer tx.Rollback()

	var count int
	Expect(tx.QueryRowContext(ctx, query, arg).Scan(&count)).To(Succeed())

	return count
}
