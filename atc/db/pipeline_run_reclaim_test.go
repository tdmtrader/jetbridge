package db_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/event"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type reclaimFixture struct {
	template db.Pipeline
	run      db.PipelineRun
	payload  db.Pipeline
	buildID  int
	jobID    int
}

func newReclaimTemplate(name string, keepLast, ttlDays *int) db.Pipeline {
	GinkgoHelper()
	template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: name}, atc.Config{
		Template:     true,
		RunRetention: &atc.RunRetentionConfig{KeepLast: keepLast, TTLDays: ttlDays},
		Jobs:         atc.JobConfigs{{Name: "entry"}},
	}, 0, false)
	Expect(err).NotTo(HaveOccurred())
	return template
}

func newReclaimRun(template db.Pipeline, completedAt *time.Time) reclaimFixture {
	GinkgoHelper()
	creation, err := db.NewPipelineRunFactory(dbConn, lockFactory).CreateRun(context.Background(), template, db.RunParams{}, "creator")
	Expect(err).NotTo(HaveOccurred())
	payload, found, err := defaultTeam.Pipeline(atc.PipelineRef{
		Name: template.Name(), InstanceVars: atc.InstanceVars{"run": float64(creation.Run.Number())},
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	entry, found, err := payload.Job("entry")
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	build := pendingRunBuild(entry)
	if completedAt != nil {
		_, err = dbConn.Exec(`UPDATE builds SET status = 'succeeded', completed = true, end_time = $2 WHERE id = $1`, build.ID(), *completedAt)
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`UPDATE pipeline_runs SET status = 'succeeded', completed_at = $2 WHERE id = $1`, creation.Run.ID(), *completedAt)
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`UPDATE pipelines SET paused = true, paused_by = 'run-completed', paused_at = $2 WHERE id = $1`, payload.ID(), *completedAt)
		Expect(err).NotTo(HaveOccurred())
	}
	return reclaimFixture{template: template, run: creation.Run, payload: payload, buildID: build.ID(), jobID: entry.ID()}
}

func expectPipelineExists(id int, expected bool) {
	GinkgoHelper()
	var exists bool
	Expect(dbConn.QueryRow(`SELECT EXISTS (SELECT 1 FROM pipelines WHERE id = $1)`, id).Scan(&exists)).To(Succeed())
	Expect(exists).To(Equal(expected))
}

func reclaimRunPayloadForTest(template db.Pipeline, run db.PipelineRun) {
	GinkgoHelper()
	_, err := dbConn.Exec(`UPDATE pipelines SET run_retention_ttl_days = 1 WHERE id = $1`, template.ID())
	Expect(err).NotTo(HaveOccurred())
	_, err = dbConn.Exec(`
		UPDATE builds
		SET status = 'failed', end_time = COALESCE(end_time, now())
		WHERE pipeline_run_id = $1 AND status IN ('pending', 'started')
	`, run.ID())
	Expect(err).NotTo(HaveOccurred())
	_, err = dbConn.Exec(`
		UPDATE pipeline_runs
		SET status = 'failed', completed_at = now() - interval '2 days'
		WHERE id = $1
	`, run.ID())
	Expect(err).NotTo(HaveOccurred())
	destroyed, err := db.NewPipelineRunReclaimLifecycle(dbConn).DestroyReclaimableRun(run.ID())
	Expect(err).NotTo(HaveOccurred())
	Expect(destroyed).To(BeTrue())
}

var _ = Describe("Pipeline run reclamation", func() {
	var lifecycle db.PipelineRunReclaimLifecycle

	BeforeEach(func() {
		lifecycle = db.NewPipelineRunReclaimLifecycle(dbConn)
	})

	It("returns no candidates without a declared retention policy", func() {
		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "keep-all"}, atc.Config{Template: true, Jobs: atc.JobConfigs{{Name: "entry"}}}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		completed := time.Now().Add(-365 * 24 * time.Hour)
		newReclaimRun(template, &completed)

		ids, err := lifecycle.ReclaimCandidateRunIDs(20)
		Expect(err).NotTo(HaveOccurred())
		Expect(ids).To(BeEmpty())
	})

	It("uses the numeric run window rather than counting terminal rows", func() {
		keepLast := 2
		template := newReclaimTemplate("number-window", &keepLast, nil)
		completed := time.Now().Add(-time.Hour)
		first := newReclaimRun(template, &completed)
		second := newReclaimRun(template, nil)
		third := newReclaimRun(template, &completed)
		fourth := newReclaimRun(template, &completed)
		_, err := dbConn.Exec(`UPDATE pipelines SET last_run_number = 5 WHERE id = $1`, template.ID())
		Expect(err).NotTo(HaveOccurred())

		ids, err := lifecycle.ReclaimCandidateRunIDs(20)
		Expect(err).NotTo(HaveOccurred())
		Expect(ids).To(ConsistOf(first.run.ID(), third.run.ID()))
		Expect(ids).NotTo(ContainElement(second.run.ID()), "running rows are never candidates even inside the old numeric window")
		Expect(ids).NotTo(ContainElement(fourth.run.ID()), "the newest numeric window remains live")
	})

	It("selects age and number policies independently with OR semantics", func() {
		keepLast, ttlDays := 2, 3
		template := newReclaimTemplate("or-policy", &keepLast, &ttlDays)
		old := time.Now().Add(-4 * 24 * time.Hour)
		fresh := time.Now().Add(-time.Hour)
		numberOnly := newReclaimRun(template, &fresh)
		retained := newReclaimRun(template, &fresh)
		ageOnly := newReclaimRun(template, &old)

		ids, err := lifecycle.ReclaimCandidateRunIDs(20)
		Expect(err).NotTo(HaveOccurred())
		Expect(ids).To(ConsistOf(ageOnly.run.ID(), numberOnly.run.ID()))
		Expect(ids).NotTo(ContainElement(retained.run.ID()))
	})

	It("deduplicates the independent query arms and enforces the final batch cap", func() {
		keepLast, ttlDays := 2, 1
		template := newReclaimTemplate("bounded", &keepLast, &ttlDays)
		old := time.Now().Add(-48 * time.Hour)
		fresh := time.Now().Add(-time.Hour)
		shared := newReclaimRun(template, &old)
		newReclaimRun(template, &fresh)
		ageOnly := newReclaimRun(template, &old)

		ids, err := lifecycle.ReclaimCandidateRunIDs(2)
		Expect(err).NotTo(HaveOccurred())
		Expect(ids).To(ConsistOf(shared.run.ID(), ageOnly.run.ID()))
	})

	It("counts a run past both retention policies once", func() {
		// The backlog is a count of runs, not of matched predicates, and the
		// two arms overlap by construction: a run old enough to be past the
		// TTL is usually also outside the keep-last window. Summing them
		// instead of UNIONing them would double every such run and report a
		// backlog twice its true size against a batch size that is not.
		keepLast, ttlDays := 1, 1
		template := newReclaimTemplate("both-policies-backlog", &keepLast, &ttlDays)
		old := time.Now().Add(-48 * time.Hour)
		fresh := time.Now().Add(-time.Hour)
		newReclaimRun(template, &old)
		newReclaimRun(template, &old)
		newReclaimRun(template, &fresh)

		backlog, err := lifecycle.ReclaimBacklog()
		Expect(err).NotTo(HaveOccurred())
		Expect(backlog).To(Equal(2), "the two older runs are past both policies and are two runs, not four")
	})

	It("counts runs a template retains only by age", func() {
		// A cluster that expresses retention purely as a TTL declares no
		// keep-last at all, so the number arm matches nothing there. Without
		// the age arm the backlog would read zero on exactly the cluster whose
		// reclaimer it is meant to be watching.
		ttlDays := 1
		template := newReclaimTemplate("ttl-only-backlog", nil, &ttlDays)
		old := time.Now().Add(-48 * time.Hour)
		fresh := time.Now().Add(-time.Hour)
		newReclaimRun(template, &old)
		newReclaimRun(template, &old)
		newReclaimRun(template, &fresh)

		backlog, err := lifecycle.ReclaimBacklog()
		Expect(err).NotTo(HaveOccurred())
		Expect(backlog).To(Equal(2), "both elapsed runs are eligible on age alone")
	})

	It("skips an unelapsed retry deadline", func() {
		keepLast := 1
		template := newReclaimTemplate("retry-deadline", &keepLast, nil)
		completed := time.Now().Add(-time.Hour)
		deferred := newReclaimRun(template, &completed)
		eligible := newReclaimRun(template, &completed)
		newReclaimRun(template, &completed)
		Expect(lifecycle.DeferRunReclaim(deferred.run.ID(), time.Now().Add(time.Hour))).To(Succeed())

		ids, err := lifecycle.ReclaimCandidateRunIDs(20)
		Expect(err).NotTo(HaveOccurred())
		Expect(ids).NotTo(ContainElement(deferred.run.ID()))
		Expect(ids).To(ContainElement(eligible.run.ID()), "a deferred oldest run must not monopolize the bounded candidate pass")
	})

	It("notifies the reclaimer after a committed retention-policy change", func() {
		template := newReclaimTemplate("retention-notify", nil, nil)
		signal, err := dbConn.Bus().ListenSignal(atc.ComponentReclaimerPipelineRuns)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(dbConn.Bus().UnlistenSignal(atc.ComponentReclaimerPipelineRuns, signal)).To(Succeed()) })
		keepLast := 1
		_, _, err = defaultTeam.SavePipeline(template.PipelineRef(), atc.Config{
			Template: true, RunRetention: &atc.RunRetentionConfig{KeepLast: &keepLast}, Jobs: atc.JobConfigs{{Name: "entry"}},
		}, template.ConfigVersion(), false)
		Expect(err).NotTo(HaveOccurred())
		Eventually(signal.C()).WithTimeout(3 * time.Second).Should(Receive())
	})

	It("notifies the reclaimer only after a run build commits the terminal transition", func() {
		keepLast := 1
		template := newReclaimTemplate("completion-notify", &keepLast, nil)
		fixture := newReclaimRun(template, nil)
		payload := fixture.loadPayload(dbConn)
		entry, found, err := payload.Job("entry")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(entry.ConsumeScheduleRequest(entry.ScheduleRequestedTime())).To(Succeed())
		build, found, err := buildFactory.Build(fixture.buildID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		started, err := build.Start(atc.Plan{})
		Expect(err).NotTo(HaveOccurred())
		Expect(started).To(BeTrue())
		signal, err := dbConn.Bus().ListenSignal(atc.ComponentReclaimerPipelineRuns)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(dbConn.Bus().UnlistenSignal(atc.ComponentReclaimerPipelineRuns, signal)).To(Succeed()) })

		Expect(build.Finish(db.BuildStatusSucceeded)).To(Succeed())
		Eventually(signal.C()).WithTimeout(3 * time.Second).Should(Receive())
		stored, found, err := db.NewPipelineRunFactory(dbConn, lockFactory).GetRunByID(fixture.run.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(stored.Status()).To(Equal(atc.RunStatusSucceeded))
	})

	It("takes the template lock before the run lock and rechecks a withdrawn policy", func() {
		keepLast := 1
		template := newReclaimTemplate("withdraw-race", &keepLast, nil)
		completed := time.Now().Add(-time.Hour)
		victim := newReclaimRun(template, &completed)
		newReclaimRun(template, &completed)

		gateConn := openRunLifecycleConn()
		reclaimConn := openRunLifecycleConn()
		probeConn := openRunLifecycleConn()
		gate, err := gateConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = gate.Rollback() })
		var locked int
		Expect(gate.QueryRow(`SELECT id FROM pipelines WHERE id = $1 FOR UPDATE`, template.ID()).Scan(&locked)).To(Succeed())

		result := make(chan struct {
			destroyed bool
			err       error
		}, 1)
		go func() {
			destroyed, err := db.NewPipelineRunReclaimLifecycle(reclaimConn).DestroyReclaimableRun(victim.run.ID())
			result <- struct {
				destroyed bool
				err       error
			}{destroyed, err}
		}()
		Consistently(result, 150*time.Millisecond).ShouldNot(Receive())

		probe, err := probeConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		Expect(probe.QueryRow(`SELECT id FROM pipeline_runs WHERE id = $1 FOR UPDATE NOWAIT`, victim.run.ID()).Scan(&locked)).To(Succeed(), "reclaimer waiting on template must not already hold the run lock")
		Expect(probe.Rollback()).To(Succeed())
		_, err = gate.Exec(`UPDATE pipelines SET run_retention_keep_last = NULL WHERE id = $1`, template.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(gate.Commit()).To(Succeed())

		var got struct {
			destroyed bool
			err       error
		}
		Eventually(result).WithTimeout(3 * time.Second).Should(Receive(&got))
		Expect(got.err).NotTo(HaveOccurred())
		Expect(got.destroyed).To(BeFalse())
		expectPipelineExists(victim.payload.ID(), true)
	})

	It("rechecks a concurrent reopen after waiting for the template lock", func() {
		keepLast := 1
		template := newReclaimTemplate("reopen-race", &keepLast, nil)
		completed := time.Now().Add(-time.Hour)
		victim := newReclaimRun(template, &completed)
		newReclaimRun(template, &completed)

		gateConn := openRunLifecycleConn()
		reclaimConn := openRunLifecycleConn()
		reopenConn := openRunLifecycleConn()
		gate, err := gateConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = gate.Rollback() })
		var locked int
		Expect(gate.QueryRow(`SELECT id FROM pipelines WHERE id = $1 FOR UPDATE`, template.ID()).Scan(&locked)).To(Succeed())
		result := make(chan bool, 1)
		go func() {
			destroyed, _ := db.NewPipelineRunReclaimLifecycle(reclaimConn).DestroyReclaimableRun(victim.run.ID())
			result <- destroyed
		}()
		Consistently(result, 150*time.Millisecond).ShouldNot(Receive())

		reopen, err := reopenConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		_, err = reopen.Exec(`UPDATE pipeline_runs SET status = 'running', completed_at = NULL WHERE id = $1`, victim.run.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(reopen.Commit()).To(Succeed())
		Expect(gate.Rollback()).To(Succeed())
		Eventually(result).WithTimeout(3 * time.Second).Should(Receive(BeFalse()))
		expectPipelineExists(victim.payload.ID(), true)
	})

	It("atomically detaches retained job builds and deletes only disposable payload data", func() {
		keepLast := 1
		template := newReclaimTemplate("atomic", &keepLast, nil)
		completed := time.Now().Add(-time.Hour)
		victim := newReclaimRun(template, &completed)
		newReclaimRun(template, &completed)
		_, err := dbConn.Exec(`UPDATE builds SET drained = true WHERE id = $1`, victim.buildID)
		Expect(err).NotTo(HaveOccurred())
		var checkID int
		Expect(dbConn.QueryRow(`
			INSERT INTO builds(name, status, team_id, pipeline_id)
			VALUES ('check', 'started', $1, $2) RETURNING id
		`, defaultTeam.ID(), victim.payload.ID()).Scan(&checkID)).To(Succeed())

		destroyed, err := lifecycle.DestroyReclaimableRun(victim.run.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(BeTrue())
		expectPipelineExists(victim.payload.ID(), false)

		var jobID, pipelineID sql.NullInt64
		var runID int
		var runName, runKey string
		var teamID int
		var drained bool
		Expect(dbConn.QueryRow(`
			SELECT job_id, pipeline_id, pipeline_run_id, run_job_name, run_job_key, team_id, drained
			FROM builds WHERE id = $1
		`, victim.buildID).Scan(&jobID, &pipelineID, &runID, &runName, &runKey, &teamID, &drained)).To(Succeed())
		Expect(jobID.Valid).To(BeFalse())
		Expect(pipelineID.Valid).To(BeFalse())
		Expect(runID).To(Equal(victim.run.ID()))
		Expect(runName).To(Equal("entry"))
		Expect(runKey).To(Equal("entry"))
		Expect(teamID).To(Equal(defaultTeam.ID()))
		Expect(drained).To(BeTrue())
		var checkExists bool
		Expect(dbConn.QueryRow(`SELECT EXISTS (SELECT 1 FROM builds WHERE id = $1)`, checkID).Scan(&checkExists)).To(Succeed())
		Expect(checkExists).To(BeFalse(), "disposable non-stamped checks follow the payload cascade")
	})

	It("treats policy withdrawal, reopen, active stamped builds, and a missing child as normal misses", func() {
		keepLast := 1
		completed := time.Now().Add(-time.Hour)

		withdrawnTemplate := newReclaimTemplate("withdrawn", &keepLast, nil)
		withdrawn := newReclaimRun(withdrawnTemplate, &completed)
		newReclaimRun(withdrawnTemplate, &completed)
		_, err := dbConn.Exec(`UPDATE pipelines SET run_retention_keep_last = NULL WHERE id = $1`, withdrawnTemplate.ID())
		Expect(err).NotTo(HaveOccurred())
		destroyed, err := lifecycle.DestroyReclaimableRun(withdrawn.run.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(BeFalse())

		reopenedTemplate := newReclaimTemplate("reopened", &keepLast, nil)
		reopened := newReclaimRun(reopenedTemplate, &completed)
		newReclaimRun(reopenedTemplate, &completed)
		_, err = dbConn.Exec(`UPDATE pipeline_runs SET status = 'running', completed_at = NULL WHERE id = $1`, reopened.run.ID())
		Expect(err).NotTo(HaveOccurred())
		destroyed, err = lifecycle.DestroyReclaimableRun(reopened.run.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(BeFalse())

		blockedTemplate := newReclaimTemplate("blocked", &keepLast, nil)
		blocked := newReclaimRun(blockedTemplate, &completed)
		newReclaimRun(blockedTemplate, &completed)
		_, err = dbConn.Exec(`UPDATE builds SET status = 'started', completed = false WHERE id = $1`, blocked.buildID)
		Expect(err).NotTo(HaveOccurred())
		destroyed, err = lifecycle.DestroyReclaimableRun(blocked.run.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(BeFalse())

		missingTemplate := newReclaimTemplate("missing", &keepLast, nil)
		missing := newReclaimRun(missingTemplate, &completed)
		newReclaimRun(missingTemplate, &completed)
		_, err = dbConn.Exec(`UPDATE builds SET job_id = NULL, pipeline_id = NULL WHERE pipeline_run_id = $1`, missing.run.ID())
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`DELETE FROM pipelines WHERE id = $1`, missing.payload.ID())
		Expect(err).NotTo(HaveOccurred())
		destroyed, err = lifecycle.DestroyReclaimableRun(missing.run.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(BeFalse())
		candidateIDs, err := lifecycle.ReclaimCandidateRunIDs(20)
		Expect(err).NotTo(HaveOccurred())
		Expect(candidateIDs).NotTo(ContainElement(missing.run.ID()), "headers without a live child must not consume the bounded batch")

		for _, runID := range []int{withdrawn.run.ID(), reopened.run.ID(), blocked.run.ID(), missing.run.ID()} {
			var retry sql.NullTime
			Expect(dbConn.QueryRow(`SELECT reclaim_retry_after FROM pipeline_runs WHERE id = $1`, runID).Scan(&retry)).To(Succeed())
			Expect(retry.Valid).To(BeFalse(), "ordinary recheck misses must not accrue retry debt")
		}
	})

	It("rolls back build detachment when payload deletion fails", func() {
		keepLast := 1
		template := newReclaimTemplate("rollback", &keepLast, nil)
		completed := time.Now().Add(-time.Hour)
		victim := newReclaimRun(template, &completed)
		newReclaimRun(template, &completed)
		failing := db.NewPipelineRunReclaimLifecycle(reclaimDeleteFailingConn{DbConn: dbConn})

		destroyed, err := failing.DestroyReclaimableRun(victim.run.ID())
		Expect(err).To(MatchError("injected payload delete failure"))
		Expect(destroyed).To(BeFalse())
		expectPipelineExists(victim.payload.ID(), true)
		var jobID, pipelineID sql.NullInt64
		Expect(dbConn.QueryRow(`SELECT job_id, pipeline_id FROM builds WHERE id = $1`, victim.buildID).Scan(&jobID, &pipelineID)).To(Succeed())
		Expect(jobID.Valid).To(BeTrue())
		Expect(pipelineID.Valid).To(BeTrue())
	})

	It("propagates an eligibility recheck query failure", func() {
		keepLast := 1
		template := newReclaimTemplate("eligibility-query-failure", &keepLast, nil)
		completed := time.Now().Add(-time.Hour)
		victim := newReclaimRun(template, &completed)
		newReclaimRun(template, &completed)
		failing := db.NewPipelineRunReclaimLifecycle(reclaimEligibilityFailingConn{DbConn: dbConn})

		destroyed, err := failing.DestroyReclaimableRun(victim.run.ID())
		Expect(destroyed).To(BeFalse())
		Expect(err).To(MatchError("injected eligibility recheck failure"))
		expectPipelineExists(victim.payload.ID(), true)
	})

	It("serializes manual reopen before reclaim on the durable run lock", func() {
		keepLast := 1
		template := newReclaimTemplate("admission-first", &keepLast, nil)
		completed := time.Now().Add(-time.Hour)
		victim := newReclaimRun(template, &completed)
		newReclaimRun(template, &completed)

		gateConn := openRunLifecycleConn()
		admissionConn := openRunLifecycleConn()
		reclaimConn := openRunLifecycleConn()
		gate, err := gateConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = gate.Rollback() })
		var locked int
		Expect(gate.QueryRow(`SELECT id FROM pipeline_runs WHERE id = $1 FOR UPDATE`, victim.run.ID()).Scan(&locked)).To(Succeed())
		payload := victim.loadPayload(admissionConn)
		job, found, err := payload.Job("entry")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		observedRunID, found := job.PipelineRunID()
		Expect(found).To(BeTrue())
		Expect(observedRunID).To(Equal(victim.run.ID()))
		admitted := make(chan error, 1)
		go func() { _, err := job.CreateBuild("manual"); admitted <- err }()
		Consistently(admitted, 100*time.Millisecond).ShouldNot(Receive())
		reclaimed := make(chan bool, 1)
		go func() {
			destroyed, _ := db.NewPipelineRunReclaimLifecycle(reclaimConn).DestroyReclaimableRun(victim.run.ID())
			reclaimed <- destroyed
		}()
		Consistently(reclaimed, 100*time.Millisecond).ShouldNot(Receive())
		Expect(gate.Rollback()).To(Succeed())
		Eventually(admitted).WithTimeout(3 * time.Second).Should(Receive(BeNil()))
		Eventually(reclaimed).WithTimeout(3 * time.Second).Should(Receive(BeFalse()))
		expectPipelineExists(victim.payload.ID(), true)
	})

	It("serializes reclaim before already-hydrated admission on the durable run lock", func() {
		keepLast := 1
		template := newReclaimTemplate("reclaim-first", &keepLast, nil)
		completed := time.Now().Add(-time.Hour)
		victim := newReclaimRun(template, &completed)
		newReclaimRun(template, &completed)

		gateConn := openRunLifecycleConn()
		reclaimConn := openRunLifecycleConn()
		admissionConn := openRunLifecycleConn()
		gate, err := gateConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = gate.Rollback() })
		var locked int
		Expect(gate.QueryRow(`SELECT id FROM pipeline_runs WHERE id = $1 FOR UPDATE`, victim.run.ID()).Scan(&locked)).To(Succeed())

		reclaimed := make(chan error, 1)
		go func() {
			destroyed, err := db.NewPipelineRunReclaimLifecycle(reclaimConn).DestroyReclaimableRun(victim.run.ID())
			if err == nil && !destroyed {
				err = errors.New("reclaim unexpectedly missed")
			}
			reclaimed <- err
		}()
		Consistently(reclaimed, 100*time.Millisecond).ShouldNot(Receive())
		payload := victim.loadPayload(admissionConn)
		job, found, err := payload.Job("entry")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		observedRunID, found := job.PipelineRunID()
		Expect(found).To(BeTrue())
		Expect(observedRunID).To(Equal(victim.run.ID()))
		admitted := make(chan error, 1)
		go func() { _, err := job.CreateBuild("manual"); admitted <- err }()
		Consistently(admitted, 100*time.Millisecond).ShouldNot(Receive())
		Expect(gate.Rollback()).To(Succeed())
		Eventually(reclaimed).WithTimeout(3 * time.Second).Should(Receive(BeNil()))
		Eventually(admitted).WithTimeout(3 * time.Second).Should(Receive(MatchError(db.ErrPipelineRunPayloadGone)))
		expectPipelineExists(victim.payload.ID(), false)
	})
})

func (fixture reclaimFixture) loadPayload(conn db.DbConn) db.Pipeline {
	GinkgoHelper()
	team, found, err := db.NewTeamFactory(conn, lockFactory).FindTeam(fixture.payload.TeamName())
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	payload, found, err := team.Pipeline(fixture.payload.PipelineRef())
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	return payload
}

type reclaimDeleteFailingConn struct{ db.DbConn }

func (c reclaimDeleteFailingConn) Begin() (db.Tx, error) {
	tx, err := c.DbConn.Begin()
	if err != nil {
		return nil, err
	}
	return reclaimDeleteFailingTx{Tx: tx}, nil
}

type reclaimDeleteFailingTx struct{ db.Tx }

func (tx reclaimDeleteFailingTx) Exec(query string, args ...any) (sql.Result, error) {
	if strings.Contains(query, "DELETE FROM pipelines") {
		return nil, fmt.Errorf("injected payload delete failure")
	}
	return tx.Tx.Exec(query, args...)
}

type reclaimEligibilityFailingConn struct{ db.DbConn }

func (c reclaimEligibilityFailingConn) Begin() (db.Tx, error) {
	tx, err := c.DbConn.Begin()
	if err != nil {
		return nil, err
	}
	return reclaimEligibilityFailingTx{Tx: tx}, nil
}

type reclaimEligibilityFailingTx struct{ db.Tx }

func (tx reclaimEligibilityFailingTx) QueryRow(query string, args ...any) sq.RowScanner {
	if strings.Contains(query, "template.run_retention_keep_last") && strings.Contains(query, "run.completed_at < now()") {
		return reclaimEligibilityFailingRow{}
	}
	return tx.Tx.QueryRow(query, args...)
}

type reclaimEligibilityFailingRow struct{}

func (reclaimEligibilityFailingRow) Scan(...any) error {
	return errors.New("injected eligibility recheck failure")
}

var _ = Describe("run payload mutation guards", func() {
	It("rejects parent-link updates for a live payload", func() {
		template := newReclaimTemplate("payload-parent-link", nil, nil)
		fixture := newReclaimRun(template, nil)

		Expect(fixture.payload.SetParentIDs(fixture.jobID, fixture.buildID)).To(MatchError(db.ErrPipelineRunPayloadMutation))
		Expect(fixture.payload.Reload()).To(BeTrue())
		Expect(fixture.payload.ParentJobID()).To(BeZero())
		Expect(fixture.payload.ParentBuildID()).To(BeZero())
	})

	It("reports a hydrated payload or job reclaimed before its mutation lock as gone", func() {
		template := newReclaimTemplate("reclaimed-mutation", nil, nil)
		fixture := newReclaimRun(template, nil)
		job, found, err := fixture.payload.Job("entry")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		reclaimRunPayloadForTest(template, fixture.run)

		Expect(fixture.payload.Pause("alice")).To(MatchError(db.ErrPipelineRunPayloadGone))
		Expect(fixture.payload.Unpause()).To(MatchError(db.ErrPipelineRunPayloadGone))
		Expect(fixture.payload.Archive()).To(MatchError(db.ErrPipelineRunPayloadGone))
		Expect(fixture.payload.Destroy()).To(MatchError(db.ErrPipelineRunPayloadGone))
		Expect(fixture.payload.Expose()).To(MatchError(db.ErrPipelineRunPayloadGone))
		Expect(fixture.payload.Hide()).To(MatchError(db.ErrPipelineRunPayloadGone))
		Expect(job.Pause("alice")).To(MatchError(db.ErrPipelineRunPayloadGone))
		_, err = fixture.payload.CreateOneOffBuild()
		Expect(err).To(MatchError(db.ErrPipelineRunPayloadGone))
		_, err = fixture.payload.CreateStartedBuild(atc.Plan{})
		Expect(err).To(MatchError(db.ErrPipelineRunPayloadGone))
	})

	It("refuses generic set, rename, archive, and destroy while preserving active pause/unpause", func() {
		keepLast := 1
		template := newReclaimTemplate("mutation-base", &keepLast, nil)
		live := newReclaimRun(template, nil)

		_, _, err := defaultTeam.SavePipeline(live.payload.PipelineRef(), atc.Config{Jobs: atc.JobConfigs{{Name: "changed"}}}, live.payload.ConfigVersion(), false)
		Expect(err).To(MatchError(db.ErrPipelineRunPayloadMutation))
		Expect(live.payload.Archive()).To(MatchError(db.ErrPipelineRunPayloadMutation))
		Expect(live.payload.Destroy()).To(MatchError(db.ErrPipelineRunPayloadMutation))
		Expect(live.payload.Expose()).To(MatchError(db.ErrPipelineRunPayloadMutation))
		Expect(live.payload.Hide()).To(MatchError(db.ErrPipelineRunPayloadMutation))
		Expect(live.payload.Pause("alice")).To(Succeed())
		Expect(live.payload.Unpause()).To(Succeed())

		found, err := defaultTeam.RenamePipeline("mutation-base", "renamed-base")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(template.Reload()).To(BeTrue())
		Expect(template.Name()).To(Equal("renamed-base"))
		Expect(live.payload.Reload()).To(BeTrue())
		Expect(live.payload.Name()).To(Equal("mutation-base"), "base rename must not rewrite live child identity")

		found, err = defaultTeam.RenamePipeline("mutation-base", "payload-only")
		Expect(found).To(BeFalse())
		Expect(err).To(MatchError(db.ErrPipelineRunPayloadMutation))
	})

	It("allows base archive but gives base destroy a durable-history conflict", func() {
		template := newReclaimTemplate("base-lifecycle", nil, nil)
		newReclaimRun(template, nil)
		Expect(template.Archive()).To(Succeed())
		Expect(template.Destroy()).To(MatchError(db.ErrPipelineTemplateHasRunHistory))
	})
})

var _ = Describe("team purge with pipeline runs", func() {
	It("locks templates before runs and serializes concurrent run creation", func() {
		team, err := teamFactory.CreateTeam(atc.Team{Name: "purge-run-race"})
		Expect(err).NotTo(HaveOccurred())
		template, _, err := team.SavePipeline(atc.PipelineRef{Name: "purge-race-template"}, atc.Config{
			Template: true, Jobs: atc.JobConfigs{{Name: "entry"}},
		}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		factory := db.NewPipelineRunFactory(dbConn, lockFactory)
		existing, err := factory.CreateRun(context.Background(), template, db.RunParams{}, "creator")
		Expect(err).NotTo(HaveOccurred())

		gateConn := openRunLifecycleConn()
		deleteConn := openRunLifecycleConn()
		createConn := openRunLifecycleConn()
		gate, err := gateConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = gate.Rollback() })
		var locked int
		Expect(gate.QueryRow(`SELECT id FROM pipeline_runs WHERE id = $1 FOR UPDATE`, existing.Run.ID()).Scan(&locked)).To(Succeed())

		deleteTeam, found, err := db.NewTeamFactory(deleteConn, lockFactory).FindTeam(team.Name())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		deleted := make(chan error, 1)
		go func() { deleted <- deleteTeam.Delete() }()
		Consistently(deleted, 100*time.Millisecond).ShouldNot(Receive())
		Eventually(func() bool {
			var id int
			err := createConn.QueryRow(`SELECT id FROM pipelines WHERE id = $1 FOR UPDATE NOWAIT`, template.ID()).Scan(&id)
			return err != nil
		}).WithTimeout(3*time.Second).Should(BeTrue(), "team purge must acquire the template lock before waiting for the run")

		createFactory := db.NewPipelineRunFactory(createConn, lockFactory)
		created := make(chan error, 1)
		go func() {
			_, err := createFactory.CreateRun(context.Background(), template, db.RunParams{}, "racer")
			created <- err
		}()
		Consistently(created, 100*time.Millisecond).ShouldNot(Receive())

		Expect(gate.Rollback()).To(Succeed())
		Eventually(deleted).WithTimeout(3 * time.Second).Should(Receive(BeNil()))
		Eventually(created).WithTimeout(3 * time.Second).Should(Receive(MatchError(db.ErrPipelineRunNotTemplate)))
		var headers int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM pipeline_runs WHERE template_pipeline_id = $1`, template.ID()).Scan(&headers)).To(Succeed())
		Expect(headers).To(BeZero())
	})

	It("removes live and detached builds, run caches, payloads, headers, and the team event partition", func() {
		team, err := teamFactory.CreateTeam(atc.Team{Name: "purge-runs"})
		Expect(err).NotTo(HaveOccurred())
		keepLast := 1
		template, _, err := team.SavePipeline(atc.PipelineRef{Name: "purge-template"}, atc.Config{
			Template: true, RunRetention: &atc.RunRetentionConfig{KeepLast: &keepLast}, Jobs: atc.JobConfigs{{Name: "entry"}},
		}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		factory := db.NewPipelineRunFactory(dbConn, lockFactory)
		create := func(completed bool) (db.PipelineRun, db.Pipeline, db.Build) {
			GinkgoHelper()
			creation, err := factory.CreateRun(context.Background(), template, db.RunParams{}, "creator")
			Expect(err).NotTo(HaveOccurred())
			payload, found, err := team.Pipeline(atc.PipelineRef{Name: template.Name(), InstanceVars: atc.InstanceVars{"run": float64(creation.Run.Number())}})
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			job, found, err := payload.Job("entry")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			build := pendingRunBuild(job)
			if completed {
				_, err = dbConn.Exec(`UPDATE builds SET status = 'succeeded', completed = true, end_time = now() WHERE id = $1`, build.ID())
				Expect(err).NotTo(HaveOccurred())
				_, err = dbConn.Exec(`UPDATE pipeline_runs SET status = 'succeeded', completed_at = now() WHERE id = $1`, creation.Run.ID())
				Expect(err).NotTo(HaveOccurred())
			}
			return creation.Run, payload, build
		}
		terminal, terminalPayload, detachedBuild := create(true)
		live, livePayload, liveBuild := create(false)
		_, err = dbConn.Exec(`UPDATE pipelines SET last_run_number = 3 WHERE id = $1`, template.ID())
		Expect(err).NotTo(HaveOccurred())
		destroyed, err := db.NewPipelineRunReclaimLifecycle(dbConn).DestroyReclaimableRun(terminal.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(BeTrue())
		expectPipelineExists(terminalPayload.ID(), false)

		cache, err := taskCacheFactory.FindOrCreate(atc.TaskCacheIdentity{TeamID: team.ID(), TemplatePipelineID: template.ID(), RunJobName: "entry"}, "task", "cache")
		Expect(err).NotTo(HaveOccurred())
		Expect(detachedBuild.SaveEvent(event.StartTask{})).To(Succeed())
		Expect(liveBuild.SaveEvent(event.StartTask{})).To(Succeed())
		var eventCount int
		Expect(dbConn.QueryRow(fmt.Sprintf(`SELECT count(*) FROM team_build_events_%d`, team.ID())).Scan(&eventCount)).To(Succeed())
		Expect(eventCount).To(BeNumerically(">", 0))

		Expect(team.Delete()).To(Succeed())
		_, found, err := teamFactory.FindTeam("purge-runs")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
		expectPipelineExists(template.ID(), false)
		expectPipelineExists(livePayload.ID(), false)
		for _, runID := range []int{terminal.ID(), live.ID()} {
			var exists bool
			Expect(dbConn.QueryRow(`SELECT EXISTS (SELECT 1 FROM pipeline_runs WHERE id = $1)`, runID).Scan(&exists)).To(Succeed())
			Expect(exists).To(BeFalse())
		}
		for _, buildID := range []int{detachedBuild.ID(), liveBuild.ID()} {
			var exists bool
			Expect(dbConn.QueryRow(`SELECT EXISTS (SELECT 1 FROM builds WHERE id = $1)`, buildID).Scan(&exists)).To(Succeed())
			Expect(exists).To(BeFalse())
		}
		var cacheExists bool
		Expect(dbConn.QueryRow(`SELECT EXISTS (SELECT 1 FROM task_caches WHERE id = $1)`, cache.ID()).Scan(&cacheExists)).To(Succeed())
		Expect(cacheExists).To(BeFalse())
		var eventTableExists bool
		Expect(dbConn.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)`, fmt.Sprintf("team_build_events_%d", team.ID())).Scan(&eventTableExists)).To(Succeed())
		Expect(eventTableExists).To(BeFalse())
	})
})
