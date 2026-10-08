package db_test

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Accept/fence and every boundary it closes serialize on the Run lock. A
// sequential test only proves each side reads the other's committed state;
// these hold one side's transaction open on its own connection while the other
// waits for it in PostgreSQL, so the lock -- not statement order -- chooses
// the winner.
//
// A Run payload admits no one-off build at all (ErrPipelineRunOneOffBuild,
// whatever the fence), so there is no one-off admission to race.
var _ = Describe("Run cancellation races on independent connections", func() {
	var f *cancellationRace

	BeforeEach(func() {
		f = newCancellationRace()
	})

	run := func(b cancellationBoundary) func() error {
		return func() error {
			if b.standalone != nil {
				return b.standalone(f, f.workConn)
			}
			tx := f.begin(f.workConn)
			if err := b.inTx(f, tx.tx); err != nil {
				return err
			}
			return tx.tx.Commit()
		}
	}

	DescribeTable("the fence commits first",
		func(b cancellationBoundary) {
			if b.prepare != nil {
				b.prepare(f)
			}
			fence := f.begin(f.cancelConn)
			Expect(f.accept(fence.tx)).To(Equal(atc.RunCancelAccepted))

			work := async(run(b))
			f.waitBlockedBy(fence.pid)
			Expect(fence.tx.Commit()).To(Succeed())

			var err error
			Eventually(work, raceTimeout).Should(Receive(&err))
			b.refused(err)
			b.absent(f)
			f.expectCancellation(true)
		},
		cancellationBoundaries(),
	)

	DescribeTable("the boundary commits first",
		func(b cancellationBoundary) {
			if b.prepare != nil {
				b.prepare(f)
			}
			var workDone chan error
			var release func()
			var holder int
			if b.standalone != nil {
				query, id := b.blocker(f)
				block := f.begin(f.blockConn)
				var locked int
				Expect(block.tx.QueryRow(query, id).Scan(&locked)).To(Succeed())
				workDone = async(run(b))
				f.waitBlockedBy(block.pid)
				release = func() { Expect(block.tx.Rollback()).To(Succeed()) }
			} else {
				work := f.begin(f.workConn)
				Expect(b.inTx(f, work.tx)).To(Succeed())
				holder = work.pid
				release = func() { Expect(work.tx.Commit()).To(Succeed()) }
			}

			// The boundary now holds the Run lock, so the fence can only wait.
			fence := f.begin(f.cancelConn)
			cancelled := asyncOutcome(func() (atc.RunCancelOutcome, error) {
				outcome, err := f.accept(fence.tx)
				if err != nil {
					return outcome, err
				}
				return outcome, fence.tx.Commit()
			})
			f.waitBlocked(fence.pid, holder)
			release()

			if workDone != nil {
				var err error
				Eventually(workDone, raceTimeout).Should(Receive(&err))
				Expect(err).NotTo(HaveOccurred())
			}
			var result outcomeResult
			Eventually(cancelled, raceTimeout).Should(Receive(&result))
			Expect(result.err).NotTo(HaveOccurred())
			Expect(result.outcome).To(Equal(b.outcome))
			b.admitted(f)
			f.expectCancellation(b.outcome == atc.RunCancelAccepted)
		},
		cancellationBoundaries(),
	)

	DescribeTable("the fence rolls back",
		func(b cancellationBoundary) {
			if b.prepare != nil {
				b.prepare(f)
			}
			fence := f.begin(f.cancelConn)
			Expect(f.accept(fence.tx)).To(Equal(atc.RunCancelAccepted))

			work := async(run(b))
			f.waitBlockedBy(fence.pid)
			Expect(fence.tx.Rollback()).To(Succeed())

			var err error
			Eventually(work, raceTimeout).Should(Receive(&err))
			Expect(err).NotTo(HaveOccurred(), "a rolled-back fence still refused the boundary")
			b.admitted(f)
			f.expectCancellation(false)
		},
		cancellationBoundaries(),
	)
})

// cancellationBoundary is one path the fence closes.
type cancellationBoundary struct {
	// prepare brings the Run to the state the boundary acts on, before either
	// racing transaction begins.
	prepare func(*cancellationRace)
	// inTx runs a boundary whose caller owns the transaction.
	inTx func(*cancellationRace, db.Tx) error
	// standalone runs a boundary that owns its transaction. blocker names a
	// row it locks after the Run, so holding that row from a third connection
	// holds the boundary open with the Run lock taken.
	standalone func(*cancellationRace, db.DbConn) error
	blocker    func(*cancellationRace) (string, int)
	// refused is how the boundary answers once the fence has committed.
	refused func(error)
	// admitted and absent read the boundary's durable effect.
	admitted func(*cancellationRace)
	absent   func(*cancellationRace)
	// outcome is what a fence behind the committed boundary returns.
	outcome atc.RunCancelOutcome
}

var errRunNotPublished = errors.New("the Run was not published")

func cancellationBoundaries() []TableEntry {
	refusedAs := func(target error) func(error) {
		return func(err error) {
			GinkgoHelper()
			Expect(err).To(MatchError(target))
		}
	}
	jobBuilds := func(f *cancellationRace) int {
		GinkgoHelper()
		var count int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM builds WHERE job_id=$1 AND created_by='race-admission'`, f.reviewJobID).Scan(&count)).To(Succeed())
		return count
	}
	pendingFetches := func(f *cancellationRace) int {
		GinkgoHelper()
		var count int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM builds WHERE job_id=$1 AND status='pending'`, f.fetch.JobID()).Scan(&count)).To(Succeed())
		return count
	}
	checks := func(f *cancellationRace) int {
		GinkgoHelper()
		var count int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM builds WHERE pipeline_id=$1 AND resource_id IS NOT NULL`, f.payloadID).Scan(&count)).To(Succeed())
		return count
	}
	debt := func(f *cancellationRace) bool {
		GinkgoHelper()
		var requested time.Time
		Expect(dbConn.QueryRow(`SELECT schedule_requested FROM jobs WHERE id=$1`, f.reviewJobID).Scan(&requested)).To(Succeed())
		return requested.After(f.scheduleRequested)
	}
	executions := func(f *cancellationRace) int {
		GinkgoHelper()
		var count int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM pipeline_run_executions WHERE build_id=$1 AND plan_id='race-task'`, f.review.ID()).Scan(&count)).To(Succeed())
		return count
	}
	captures := func(f *cancellationRace) (int, int) {
		GinkgoHelper()
		var links, rows int
		Expect(dbConn.QueryRow(`SELECT (SELECT count(*) FROM pipeline_run_captures WHERE run_id=$1),(SELECT count(*) FROM hangar_captures)`, f.runID).Scan(&links, &rows)).To(Succeed())
		return links, rows
	}
	published := func(f *cancellationRace) (string, bool) {
		GinkgoHelper()
		var status string
		var version sql.NullString
		Expect(dbConn.QueryRow(`SELECT status,terminal_observation_version FROM pipeline_runs WHERE id=$1`, f.runID).Scan(&status, &version)).To(Succeed())
		return status, version.Valid
	}

	return []TableEntry{
		// The scheduler's admission is the one with no second gate: a manual
		// build also asks for scheduling, which the fence refuses on its own.
		Entry("automatic job build admission", cancellationBoundary{
			prepare: func(f *cancellationRace) { Expect(f.fetch.Finish(db.BuildStatusSucceeded)).To(Succeed()) },
			standalone: func(f *cancellationRace, conn db.DbConn) error {
				return f.payloadJob(conn, "fetch").EnsurePendingBuildExists(f.ctx)
			},
			blocker: func(f *cancellationRace) (string, int) {
				return `SELECT id FROM jobs WHERE id=$1 FOR UPDATE`, f.fetch.JobID()
			},
			refused:  refusedAs(db.ErrPipelineRunCancelling),
			admitted: func(f *cancellationRace) { Expect(pendingFetches(f)).To(Equal(1)) },
			absent:   func(f *cancellationRace) { Expect(pendingFetches(f)).To(BeZero()) },
			outcome:  atc.RunCancelAccepted,
		}),
		Entry("manual job build admission", cancellationBoundary{
			standalone: func(f *cancellationRace, conn db.DbConn) error {
				_, err := f.payloadJob(conn, "review").CreateBuild("race-admission")
				return err
			},
			blocker: func(f *cancellationRace) (string, int) {
				return `SELECT id FROM jobs WHERE id=$1 FOR UPDATE`, f.reviewJobID
			},
			refused:  refusedAs(db.ErrPipelineRunCancelling),
			admitted: func(f *cancellationRace) { Expect(jobBuilds(f)).To(Equal(1)) },
			absent:   func(f *cancellationRace) { Expect(jobBuilds(f)).To(BeZero()) },
			outcome:  atc.RunCancelAccepted,
		}),
		Entry("check admission", cancellationBoundary{
			standalone: func(f *cancellationRace, conn db.DbConn) error {
				resource, found, err := f.payload(conn).Resource("source")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				_, _, err = resource.CreateBuild(f.ctx, true, atc.Plan{ID: "race-check", Check: &atc.CheckPlan{Type: "some-base-resource-type", Resource: "source", Source: atc.Source{"repository": "example"}}})
				return err
			},
			// The check build's foreign key takes KEY SHARE on its payload
			// row after the Run lock.
			blocker: func(f *cancellationRace) (string, int) {
				return `SELECT id FROM pipelines WHERE id=$1 FOR UPDATE`, f.payloadID
			},
			refused:  refusedAs(db.ErrPipelineRunCancelling),
			admitted: func(f *cancellationRace) { Expect(checks(f)).To(Equal(1)) },
			absent:   func(f *cancellationRace) { Expect(checks(f)).To(BeZero()) },
			outcome:  atc.RunCancelAccepted,
		}),
		Entry("scheduler-debt creation", cancellationBoundary{
			prepare: func(f *cancellationRace) { f.settleScheduling() },
			standalone: func(f *cancellationRace, conn db.DbConn) error {
				return f.payloadJob(conn, "review").RequestSchedule()
			},
			blocker: func(f *cancellationRace) (string, int) {
				return `SELECT id FROM jobs WHERE id=$1 FOR UPDATE`, f.reviewJobID
			},
			refused:  refusedAs(db.ErrPipelineRunCancelling),
			admitted: func(f *cancellationRace) { Expect(debt(f)).To(BeTrue()) },
			absent:   func(f *cancellationRace) { Expect(debt(f)).To(BeFalse()) },
			outcome:  atc.RunCancelAccepted,
		}),
		Entry("defensive execution start", cancellationBoundary{
			inTx: func(f *cancellationRace, tx db.Tx) error {
				_, _, err := f.factory.AdmitRunExecution(f.ctx, tx, db.RunExecutionRequest{
					BuildID: f.review.ID(), PlanID: "race-task", Kind: db.ContainerTypeTask,
					NodeName: "node", NodeUID: "node-uid",
				})
				return err
			},
			refused:  refusedAs(db.ErrPipelineRunCancelling),
			admitted: func(f *cancellationRace) { Expect(executions(f)).To(Equal(1)) },
			absent:   func(f *cancellationRace) { Expect(executions(f)).To(BeZero()) },
			outcome:  atc.RunCancelAccepted,
		}),
		Entry("a capture start", cancellationBoundary{
			inTx: func(f *cancellationRace, tx db.Tx) error {
				_, err := f.factory.StartRunCapture(f.ctx, tx, f.review.ID(), f.plan, time.Hour, "node", "node-uid")
				return err
			},
			refused: refusedAs(db.ErrPipelineRunCancelling),
			admitted: func(f *cancellationRace) {
				links, rows := captures(f)
				Expect([]int{links, rows}).To(Equal([]int{1, 1}))
			},
			absent: func(f *cancellationRace) {
				links, rows := captures(f)
				Expect([]int{links, rows}).To(Equal([]int{0, 0}))
			},
			outcome: atc.RunCancelAccepted,
		}),
		Entry("ordinary terminal publication", cancellationBoundary{
			prepare: func(f *cancellationRace) { f.failEveryJob() },
			inTx: func(f *cancellationRace, tx db.Tx) error {
				completed, err := f.factory.FinalizeOutputRun(f.ctx, tx, f.runID)
				if err == nil && !completed {
					return errRunNotPublished
				}
				return err
			},
			refused: refusedAs(errRunNotPublished),
			admitted: func(f *cancellationRace) {
				status, versioned := published(f)
				Expect(status).To(Equal("failed"))
				Expect(versioned).To(BeTrue())
			},
			absent: func(f *cancellationRace) {
				status, versioned := published(f)
				Expect(status).To(Equal("running"))
				Expect(versioned).To(BeFalse())
			},
			// Completion first keeps its observation; the fence writes nothing.
			outcome: atc.RunCancelAlreadyTerminal,
		}),
	}
}

const raceTimeout = 10 * time.Second

// cancellationRace is one running v2 Run with a selected-output producer and
// a checked resource, and three connections of its own: the fence's, the
// boundary's, and one that can hold a row the boundary needs.
type cancellationRace struct {
	ctx                             context.Context
	factory                         db.PipelineRunFactory
	cancelConn, workConn, blockConn db.DbConn
	creation                        db.RunCreation
	runID, payloadID, reviewJobID   int
	review, fetch                   db.Build
	plan                            atc.TaskPlan
	scheduleRequested               time.Time
}

func newCancellationRace() *cancellationRace {
	GinkgoHelper()
	f := &cancellationRace{ctx: context.Background()}
	consumer, err := db.HangarConsumerPrefixHeld("cancellation-race-test")
	Expect(err).NotTo(HaveOccurred())
	hangarActivateEpoch(f.ctx, db.NewHangarOutputRepository(consumer))
	_, err = dbConn.Exec(`UPDATE pipeline_run_activation SET epoch=1, admission_enabled=true WHERE singleton`)
	Expect(err).NotTo(HaveOccurred())

	producer := &atc.TaskStep{
		Name: "review", TaskID: uuid.NewString(), RunResult: &atc.RunResult{Name: "findings", Output: "result"},
		Config: &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "true"}, Outputs: []atc.TaskOutputConfig{{Name: "result"}}},
	}
	template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "cancellation-race"}, atc.Config{
		Template: true,
		Jobs: atc.JobConfigs{
			{Name: "review", PlanSequence: []atc.Step{{Config: producer}}},
			{Name: "fetch", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "source"}}}},
		},
		Resources: atc.ResourceConfigs{{Name: "source", Type: "some-base-resource-type", Source: atc.Source{"repository": "example"}}},
	}, 0, false)
	Expect(err).NotTo(HaveOccurred())
	f.factory = db.NewPipelineRunFactory(dbConn, lockFactory)
	tx, err := dbConn.Begin()
	Expect(err).NotTo(HaveOccurred())
	defer db.Rollback(tx)
	f.creation, err = f.factory.CreateRunInTx(f.ctx, tx, template, db.RunParams{}, "creator", db.RunCreationOpts{ActivationEpoch: 1, HangarOutput: true})
	Expect(err).NotTo(HaveOccurred())
	Expect(tx.Commit()).To(Succeed())
	f.runID = f.creation.Run.ID()
	for _, build := range f.creation.EntryBuilds {
		switch build.JobName() {
		case "review":
			f.review = build
		case "fetch":
			f.fetch = build
		}
	}
	Expect(f.review).NotTo(BeNil())
	Expect(f.fetch).NotTo(BeNil())
	f.reviewJobID = f.review.JobID()
	f.payloadID = f.review.PipelineID()
	materialized := f.creation.Config.Jobs[0].PlanSequence[0].Config.(*atc.TaskStep)
	f.plan = atc.TaskPlan{Name: materialized.Name, TaskID: materialized.TaskID, RunResult: materialized.RunResult, Config: materialized.Config}

	f.cancelConn = openRunLifecycleConn()
	f.workConn = openRunLifecycleConn()
	f.blockConn = openRunLifecycleConn()
	return f
}

type heldTx struct {
	tx  db.Tx
	pid int
}

// begin opens a transaction on conn and records its backend. A failed spec
// rolls it back before the connection closes, which releases any waiter.
func (f *cancellationRace) begin(conn db.DbConn) heldTx {
	GinkgoHelper()
	tx, err := conn.BeginTx(f.ctx, nil)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { _ = tx.Rollback() })
	var pid int
	Expect(tx.QueryRow(`SELECT pg_backend_pid()`).Scan(&pid)).To(Succeed())
	return heldTx{tx: tx, pid: pid}
}

func (f *cancellationRace) accept(tx db.Tx) (atc.RunCancelOutcome, error) {
	return db.NewPipelineRunFactory(f.cancelConn, lockFactory).AcceptRunCancellation(f.ctx, tx, f.runID, "race-requester", nil)
}

// waitBlockedBy waits until some backend is waiting on a lock holder holds.
func (f *cancellationRace) waitBlockedBy(holder int) {
	GinkgoHelper()
	Eventually(func() (bool, error) {
		var blocked bool
		err := dbConn.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))`, holder).Scan(&blocked)
		return blocked, err
	}, raceTimeout, 10*time.Millisecond).Should(BeTrue(), "nothing waited for backend %d", holder)
}

// waitBlocked waits until waiter is blocked by holder, or by anything when
// holder is zero.
func (f *cancellationRace) waitBlocked(waiter, holder int) {
	GinkgoHelper()
	Eventually(func() (bool, error) {
		var blocked bool
		err := dbConn.QueryRow(`SELECT CASE WHEN $2=0 THEN cardinality(pg_blocking_pids($1))>0 ELSE $2 = ANY(pg_blocking_pids($1)) END`, waiter, holder).Scan(&blocked)
		return blocked, err
	}, raceTimeout, 10*time.Millisecond).Should(BeTrue(), "backend %d never waited", waiter)
}

// expectCancellation reads the fence's durable facts, which are all present
// or all absent.
func (f *cancellationRace) expectCancellation(requested bool) {
	GinkgoHelper()
	var at sql.NullTime
	var by, reason sql.NullString
	Expect(dbConn.QueryRow(`SELECT cancel_requested_at,cancel_requested_by,cancel_reason FROM pipeline_runs WHERE id=$1`, f.runID).Scan(&at, &by, &reason)).To(Succeed())
	if requested {
		Expect(at.Valid).To(BeTrue())
		Expect(by.String).To(Equal("race-requester"))
	} else {
		Expect(at.Valid).To(BeFalse())
		Expect(by.Valid).To(BeFalse())
		Expect(reason.Valid).To(BeFalse())
	}
}

func (f *cancellationRace) payload(conn db.DbConn) db.Pipeline {
	GinkgoHelper()
	payload, found, err := db.NewPipelineRunFactory(conn, lockFactory).InstancePipeline(f.creation.Run)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	return payload
}

func (f *cancellationRace) payloadJob(conn db.DbConn, name string) db.Job {
	GinkgoHelper()
	job, found, err := f.payload(conn).Job(name)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	return job
}

// settleScheduling consumes the debt creation left, so new debt is visible.
func (f *cancellationRace) settleScheduling() {
	GinkgoHelper()
	for _, name := range []string{"review", "fetch"} {
		Expect(f.payloadJob(dbConn, name).ConsumeScheduleRequest(time.Now().UTC())).To(Succeed())
	}
	Expect(dbConn.QueryRow(`SELECT schedule_requested FROM jobs WHERE id=$1`, f.reviewJobID).Scan(&f.scheduleRequested)).To(Succeed())
}

// failEveryJob leaves the Run ready for ordinary publication as failed.
func (f *cancellationRace) failEveryJob() {
	GinkgoHelper()
	for _, build := range f.creation.EntryBuilds {
		Expect(build.Finish(db.BuildStatusFailed)).To(Succeed())
	}
	f.settleScheduling()
}

func async(work func() error) chan error {
	done := make(chan error, 1)
	go func() {
		defer GinkgoRecover()
		done <- work()
	}()
	return done
}

type outcomeResult struct {
	outcome atc.RunCancelOutcome
	err     error
}

func asyncOutcome(work func() (atc.RunCancelOutcome, error)) chan outcomeResult {
	done := make(chan outcomeResult, 1)
	go func() {
		defer GinkgoRecover()
		outcome, err := work()
		done <- outcomeResult{outcome, err}
	}()
	return done
}
