package runs_test

import (
	"context"
	"database/sql"
	"strconv"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/runs"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// What a build may do to a child Run after admitting it: watch it, and ask for
// it to stop. Both are authorized against the team that owns the Run's
// template, and neither runs inside a caller's transaction.
var _ = Describe("observing and cancelling an admitted run", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	admit := func(ref runs.TemplateRef, principal runs.Principal, key string) runs.Run {
		GinkgoHelper()
		tx, err := admitter.Begin(ctx)
		Expect(err).NotTo(HaveOccurred())
		defer tx.Rollback()

		run, err := admitIn(ctx, admitter, tx, runs.Admission{Template: ref, Principal: principal, ContractKey: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.Commit()).To(Succeed())

		return run
	}

	// finish settles the Run's builds and publishes a terminal header by
	// fixture, with the deferred publication check suspended for that one
	// statement: the finalizer needs the output plane's claim bookkeeping,
	// which this suite does not have.
	finish := func(runID int, status atc.RunStatus) {
		GinkgoHelper()
		_, err := dbConn.Exec(`UPDATE builds SET status = 'succeeded', completed = true, end_time = now() WHERE pipeline_run_id = $1 AND NOT completed`, runID)
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`ALTER TABLE pipeline_runs DISABLE TRIGGER run_terminal_result_match`)
		Expect(err).NotTo(HaveOccurred())
		defer func() {
			_, err := dbConn.Exec(`ALTER TABLE pipeline_runs ENABLE TRIGGER run_terminal_result_match`)
			Expect(err).NotTo(HaveOccurred())
		}()
		_, err = dbConn.Exec(`UPDATE pipeline_runs SET status = $2, completed_at = now(), result_manifest = '{}' WHERE id = $1`, runID, string(status))
		Expect(err).NotTo(HaveOccurred())
	}

	type fence struct {
		requested bool
		by        sql.NullString
		reason    sql.NullString
	}
	fenceOf := func(runID int) fence {
		GinkgoHelper()
		var f fence
		Expect(dbConn.QueryRow(`SELECT cancel_requested_at IS NOT NULL, cancel_requested_by, cancel_reason FROM pipeline_runs WHERE id = $1`, runID).
			Scan(&f.requested, &f.by, &f.reason)).To(Succeed())
		return f
	}

	otherTeamRun := func() runs.Run {
		GinkgoHelper()
		stranger := runningBuildOn(otherTeamHandle, "other-caller")
		return admit(otherTeamRef, runs.Principal{Build: buildPrincipalFor(stranger)}, "observe-test.other")
	}

	Describe("ObserveRun", func() {
		It("reports a running run, then the status it finished with", func() {
			admitted := admit(templateRef, buildPrincipal, "observe-test.one")

			observed, err := admitter.ObserveRun(ctx, admitted.ID, buildPrincipal)
			Expect(err).NotTo(HaveOccurred())
			Expect(observed.Run).To(Equal(admitted))
			Expect(observed.Status).To(Equal(atc.RunStatusRunning))
			Expect(observed.Terminal()).To(BeFalse())
			Expect(observed.CancellationRequested).To(BeFalse())

			finish(admitted.ID, atc.RunStatusFailed)

			observed, err = admitter.ObserveRun(ctx, admitted.ID, buildPrincipal)
			Expect(err).NotTo(HaveOccurred())
			Expect(observed.Run).To(Equal(admitted))
			Expect(observed.Status).To(Equal(atc.RunStatusFailed))
			Expect(observed.Terminal()).To(BeTrue())
		})

		It("answers a viewer of the template's team", func() {
			admitted := admit(templateRef, memberPrincipal, "observe-test.viewer")

			observed, err := admitter.ObserveRun(ctx, admitted.ID, viewerPrincipal)
			Expect(err).NotTo(HaveOccurred())
			Expect(observed.Run.ID).To(Equal(admitted.ID))
		})

		// Another team's run and a run that does not exist get the same
		// answer, so the port tells a caller nothing about run ids it has no
		// standing on.
		It("refuses another team's run exactly as it refuses a missing one", func() {
			foreign := otherTeamRun()

			for _, principal := range []runs.Principal{memberPrincipal, buildPrincipal} {
				_, err := admitter.ObserveRun(ctx, foreign.ID, principal)
				Expect(err).To(BeIdenticalTo(runs.ErrUnauthorized))
				Expect(runs.IsRefusal(err)).To(BeTrue())

				_, err = admitter.ObserveRun(ctx, 999999, principal)
				Expect(err).To(BeIdenticalTo(runs.ErrUnauthorized))
			}
		})

		It("tells an admin about every team's runs, and that a missing one is missing", func() {
			foreign := otherTeamRun()

			observed, err := admitter.ObserveRun(ctx, foreign.ID, adminPrincipal)
			Expect(err).NotTo(HaveOccurred())
			Expect(observed.Run.ID).To(Equal(foreign.ID))

			_, err = admitter.ObserveRun(ctx, 999999, adminPrincipal)
			Expect(err).To(BeIdenticalTo(runs.ErrRunNotFound))
		})

		It("is honoured for a build that has been aborted, so the step reaches its cancel", func() {
			admitted := admit(templateRef, buildPrincipal, "observe-test.aborted")
			Expect(callerBuild.MarkAsAborted()).To(Succeed())

			status, err := admitter.ObserveRun(ctx, admitted.ID, buildPrincipal)
			Expect(err).NotTo(HaveOccurred())
			Expect(status.Run.ID).To(Equal(admitted.ID))
		})

		It("refuses a principal that is two identities before reading anything", func() {
			admitted := admit(templateRef, memberPrincipal, "observe-test.ambiguous")
			both := runs.Principal{Claims: memberPrincipal.Claims, Build: buildPrincipal.Build}

			_, err := admitter.ObserveRun(ctx, admitted.ID, both)
			Expect(err).To(BeIdenticalTo(runs.ErrPrincipalAmbiguous))
			_, err = admitter.ObserveRun(ctx, 999999, both)
			Expect(err).To(BeIdenticalTo(runs.ErrPrincipalAmbiguous))
		})
	})

	Describe("CancelRun", func() {
		It("records the fence on a running run under the build's reserved requester", func() {
			admitted := admit(templateRef, buildPrincipal, "cancel-test.one")

			Expect(admitter.CancelRun(ctx, admitted.ID, buildPrincipal, "parent build aborted")).To(Succeed())

			f := fenceOf(admitted.ID)
			Expect(f.requested).To(BeTrue())
			Expect(f.by.String).To(Equal("system:build/" + strconv.Itoa(callerBuild.ID())))
			Expect(f.reason.String).To(Equal("parent build aborted"))

			// The fence is what cancellation records; the status stays
			// running until the cancellation worker settles the run.
			observed, err := admitter.ObserveRun(ctx, admitted.ID, buildPrincipal)
			Expect(err).NotTo(HaveOccurred())
			Expect(observed.Status).To(Equal(atc.RunStatusRunning))
			Expect(observed.CancellationRequested).To(BeTrue())
		})

		// Abort marks the build before the step's context is cancelled, so
		// the step asks after the flag is set. Refusing it then would orphan
		// the child the abort meant to stop.
		It("is honoured for a build that has been aborted", func() {
			admitted := admit(templateRef, buildPrincipal, "cancel-test.aborted")
			Expect(callerBuild.MarkAsAborted()).To(Succeed())

			Expect(admitter.CancelRun(ctx, admitted.ID, buildPrincipal, "")).To(Succeed())

			f := fenceOf(admitted.ID)
			Expect(f.requested).To(BeTrue())
			Expect(f.reason.Valid).To(BeFalse())
		})

		It("is not an error a second time, and leaves the first request in place", func() {
			admitted := admit(templateRef, buildPrincipal, "cancel-test.twice")

			Expect(admitter.CancelRun(ctx, admitted.ID, buildPrincipal, "first")).To(Succeed())
			Expect(admitter.CancelRun(ctx, admitted.ID, memberPrincipal, "second")).To(Succeed())

			f := fenceOf(admitted.ID)
			Expect(f.by.String).To(Equal("system:build/" + strconv.Itoa(callerBuild.ID())))
			Expect(f.reason.String).To(Equal("first"))
		})

		It("is a no-op on a run that has already finished", func() {
			admitted := admit(templateRef, buildPrincipal, "cancel-test.terminal")
			finish(admitted.ID, atc.RunStatusSucceeded)

			Expect(admitter.CancelRun(ctx, admitted.ID, buildPrincipal, "too late")).To(Succeed())

			Expect(fenceOf(admitted.ID).requested).To(BeFalse())
			observed, err := admitter.ObserveRun(ctx, admitted.ID, buildPrincipal)
			Expect(err).NotTo(HaveOccurred())
			Expect(observed.Status).To(Equal(atc.RunStatusSucceeded))
		})

		It("records a person's verified subject, as the HTTP route does", func() {
			admitted := admit(templateRef, memberPrincipal, "cancel-test.person")

			Expect(admitter.CancelRun(ctx, admitted.ID, memberPrincipal, "")).To(Succeed())
			Expect(fenceOf(admitted.ID).by.String).To(Equal("local:member-user"))
		})

		It("refuses a subject that claims the reserved requester prefix", func() {
			admitted := admit(templateRef, memberPrincipal, "cancel-test.impostor")
			claims := claimsFor("member-user", "member-id")
			claims["sub"] = "system:build/1"

			err := admitter.CancelRun(ctx, admitted.ID, runs.Principal{Claims: claims}, "")
			Expect(err).To(BeIdenticalTo(runs.ErrUnauthorized))
			Expect(fenceOf(admitted.ID).requested).To(BeFalse())
		})

		It("refuses a viewer, who may watch a run but not stop it", func() {
			admitted := admit(templateRef, memberPrincipal, "cancel-test.viewer")

			err := admitter.CancelRun(ctx, admitted.ID, viewerPrincipal, "")
			Expect(err).To(BeIdenticalTo(runs.ErrUnauthorized))
			Expect(fenceOf(admitted.ID).requested).To(BeFalse())
		})

		It("refuses another team's run exactly as it refuses a missing one, and writes nothing", func() {
			foreign := otherTeamRun()

			Expect(admitter.CancelRun(ctx, foreign.ID, buildPrincipal, "")).To(BeIdenticalTo(runs.ErrUnauthorized))
			Expect(admitter.CancelRun(ctx, 999999, buildPrincipal, "")).To(BeIdenticalTo(runs.ErrUnauthorized))
			Expect(fenceOf(foreign.ID).requested).To(BeFalse())
		})
	})

	// A child whose bound input cannot be had is refused, not faulted: the
	// step fails with the reason instead of retrying a binding that will fail
	// the same way again.
	Describe("an admission whose bound inputs cannot be bound", func() {
		admitInputs := func(ref runs.TemplateRef, inputs map[string]atc.RunInputSource) error {
			GinkgoHelper()
			tx, err := admitter.Begin(ctx)
			Expect(err).NotTo(HaveOccurred())
			defer tx.Rollback()

			_, err = admitIn(ctx, admitter, tx, runs.Admission{
				Template: ref, Principal: buildPrincipal, Inputs: inputs, ContractKey: "inputs-test.one",
			})
			return err
		}

		It("is a refusal when the template declares no such input", func() {
			err := admitInputs(templateRef, map[string]atc.RunInputSource{"seed": {RunID: 1, Result: "out"}})
			Expect(err).To(BeIdenticalTo(atc.ErrInvalidRunInputs))
			Expect(runs.IsRefusal(err)).To(BeTrue())
		})

		It("is a refusal when the source run has no such result", func() {
			savePipeline(defaultTeam, "seeded", atc.Config{Template: true, Jobs: atc.JobConfigs{{
				Name: "entry",
				PlanSequence: []atc.Step{{Config: &atc.TaskStep{
					Name: "use", TaskID: uuid.NewString(),
					RunInputs: []atc.RunInput{{Name: "seed", Input: "seed"}},
					Config: &atc.TaskConfig{
						Platform: "linux", Run: atc.TaskRunConfig{Path: "true"},
						Inputs: []atc.TaskInputConfig{{Name: "seed"}},
					},
				}}},
			}}})
			seeded := runs.TemplateRef{Team: defaultTeam.Name(), Pipeline: atc.PipelineRef{Name: "seeded"}}

			// A real run of the team, still running, so it has published
			// nothing for the binding to take.
			source := admit(templateRef, buildPrincipal, "inputs-test.source")

			err := admitInputs(seeded, map[string]atc.RunInputSource{"seed": {RunID: source.ID, Result: "out"}})
			Expect(err).To(BeIdenticalTo(atc.ErrRunInputUnavailable))
			Expect(runs.IsRefusal(err)).To(BeTrue())
		})
	})
})
