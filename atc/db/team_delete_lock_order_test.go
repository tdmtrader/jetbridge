package db_test

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// These specs interleave a team purge or a reclamation with the transactions
// it can meet, each on its own connection, in the one order that deadlocked:
// the other transaction takes its first lock, the purge runs until it waits,
// and the other transaction then takes its second lock. Postgres resolves a
// deadlock by failing one side with 40P01 after deadlock_timeout; neither
// side may be that victim.

const lockOrderWait = 10 * time.Second

func expectNoDeadlock(err error, who string) {
	GinkgoHelper()
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		Expect(pgErr.Code).NotTo(Equal(pgerrcode.DeadlockDetected), "%s was chosen as a deadlock victim", who)
	}
	Expect(err).NotTo(HaveOccurred(), who)
}

// blockedOnLock runs work on its own connection and returns once that
// connection is waiting on a lock.
func blockedOnLock(work func(conn db.DbConn) error) <-chan error {
	GinkgoHelper()
	conn := openRunLifecycleConn()
	// The connection allows one session, so this pid is the one work uses.
	var pid int
	Expect(conn.QueryRow(`SELECT pg_backend_pid()`).Scan(&pid)).To(Succeed())
	done := make(chan error, 1)
	go func() {
		defer GinkgoRecover()
		done <- work(conn)
	}()
	Eventually(func() string {
		var waiting sql.NullString
		Expect(dbConn.QueryRow(`SELECT wait_event_type FROM pg_stat_activity WHERE pid=$1`, pid).Scan(&waiting)).To(Succeed())
		return waiting.String
	}).WithTimeout(lockOrderWait).Should(Equal("Lock"), "the concurrent transaction never reached its lock wait")
	return done
}

func blockedTeamDelete(name string) <-chan error {
	GinkgoHelper()
	return blockedOnLock(func(conn db.DbConn) error {
		team, found, err := db.NewTeamFactory(conn, lockFactory).FindTeam(name)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("team not found")
		}
		return team.Delete()
	})
}

// step runs one statement of the interleaved transaction with a bounded
// wait, so a wait that is not a deadlock fails the spec instead of hanging.
func step(tx db.Tx, who, statement string, args ...any) {
	GinkgoHelper()
	ctx, cancel := context.WithTimeout(context.Background(), lockOrderWait)
	defer cancel()
	_, err := tx.ExecContext(ctx, statement, args...)
	expectNoDeadlock(err, who)
}

func expectFinished(done <-chan error, who string) {
	GinkgoHelper()
	var err error
	Eventually(done).WithTimeout(lockOrderWait).Should(Receive(&err), "%s never finished", who)
	expectNoDeadlock(err, who)
}

func expectTeamGone(name string) {
	GinkgoHelper()
	_, found, err := teamFactory.FindTeam(name)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeFalse())
}

func lockOrderTemplate(team db.Team, retention *atc.RunRetentionConfig) db.Pipeline {
	GinkgoHelper()
	template, _, err := team.SavePipeline(atc.PipelineRef{Name: "lock-order"}, atc.Config{
		Template:     true,
		RunRetention: retention,
		Jobs:         atc.JobConfigs{{Name: "entry", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "source"}}}}},
		Resources:    atc.ResourceConfigs{{Name: "source", Type: "some-base-resource-type", Source: atc.Source{"repository": "example"}}},
	}, 0, false)
	Expect(err).NotTo(HaveOccurred())
	return template
}

var _ = Describe("Team purge lock order", func() {
	It("does not deadlock with a Run path that holds its team share lock", func() {
		ctx := context.Background()
		team, err := teamFactory.CreateTeam(atc.Team{Name: "purge-meets-run-path"})
		Expect(err).NotTo(HaveOccurred())
		creation, err := dbtest.CreateRun(dbConn, db.NewPipelineRunFactory(dbConn, lockFactory), ctx, lockOrderTemplate(team, nil), db.RunParams{}, "creator")
		Expect(err).NotTo(HaveOccurred())

		// Run paths (execution admission, result publication, input upload,
		// output start) take the team FOR SHARE and then the Run.
		runPath, err := openRunLifecycleConn().Begin()
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(runPath)
		step(runPath, "the Run path", `SELECT id FROM teams WHERE id=$1 FOR SHARE`, team.ID())
		purge := blockedTeamDelete(team.Name())
		step(runPath, "the Run path", `SELECT id FROM pipeline_runs WHERE id=$1 FOR NO KEY UPDATE`, creation.Run.ID())
		Expect(runPath.Commit()).To(Succeed())

		expectFinished(purge, "Team.Delete")
		expectTeamGone(team.Name())
	})

	It("does not deadlock with a check collection batch", func() {
		ctx := context.Background()
		evidence := admitRunEvidence(ctx, "purge-meets-check-collection")

		// A collection batch locks its checks with SKIP LOCKED and then
		// deletes their executions, builds and events.
		collector, err := openRunLifecycleConn().Begin()
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(collector)
		step(collector, "check collection", `SELECT set_config('concourse.pipeline_run_check_gc', 'on', true)`)
		step(collector, "check collection", `SELECT id FROM builds WHERE id=$1 FOR UPDATE SKIP LOCKED`, evidence.checkID)
		purge := blockedTeamDelete(evidence.team.Name())
		step(collector, "check collection", `DELETE FROM pipeline_run_executions WHERE build_id=$1`, evidence.checkID)
		step(collector, "check collection", `DELETE FROM builds WHERE id=$1`, evidence.checkID)
		step(collector, "check collection", `DELETE FROM check_build_events WHERE build_id=$1`, evidence.checkID)
		Expect(collector.Commit()).To(Succeed())

		expectFinished(purge, "Team.Delete")
		expectTeamGone(evidence.team.Name())
	})
})

var _ = Describe("Run reclamation lock order", func() {
	It("does not deadlock with a check collection batch", func() {
		keepLast := 1
		f := newRunCheckFixtureWithRetention("reclaim-meets-check-collection", &atc.RunRetentionConfig{KeepLast: &keepLast})
		check, _ := f.check(f.resource, executedClosed)
		started, err := f.entryBuild.Start(atc.Plan{})
		Expect(err).NotTo(HaveOccurred())
		Expect(started).To(BeTrue())
		Expect(f.entryBuild.Finish(db.BuildStatusSucceeded)).To(Succeed())
		payload, found, err := f.factory.InstancePipeline(f.run)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		entry, found, err := payload.Job("entry")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		consumeObservedSchedule(entry)
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(tx)
		completed, err := f.factory.FinalizeOutputRun(f.ctx, tx, f.run.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(completed).To(BeTrue())
		Expect(tx.Commit()).To(Succeed())
		tx, err = dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(tx)
		_, err = f.factory.CreateRunInTx(f.ctx, tx, f.template, db.RunParams{}, "creator", db.RunCreationOpts{ActivationEpoch: 1, HangarEpoch: 1})
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.Commit()).To(Succeed())

		collector, err := openRunLifecycleConn().Begin()
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(collector)
		step(collector, "check collection", `SELECT set_config('concourse.pipeline_run_check_gc', 'on', true)`)
		step(collector, "check collection", `SELECT id FROM builds WHERE id=$1 FOR UPDATE SKIP LOCKED`, check.ID())
		var destroyed bool
		reclaim := blockedOnLock(func(conn db.DbConn) error {
			var err error
			destroyed, err = db.NewPipelineRunReclaimLifecycle(conn).DestroyReclaimableRun(f.run.ID())
			return err
		})
		step(collector, "check collection", `DELETE FROM pipeline_run_executions WHERE build_id=$1`, check.ID())
		step(collector, "check collection", `DELETE FROM builds WHERE id=$1`, check.ID())
		step(collector, "check collection", `DELETE FROM check_build_events WHERE build_id=$1`, check.ID())
		Expect(collector.Commit()).To(Succeed())

		expectFinished(reclaim, "reclamation")
		Expect(destroyed).To(BeTrue())
		expectPipelineExists(payload.ID(), false)
	})
})
