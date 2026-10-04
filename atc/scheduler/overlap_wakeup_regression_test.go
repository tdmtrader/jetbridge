package scheduler_test

import (
	"context"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/builds"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/scheduler"
	"github.com/concourse/concourse/atc/scheduler/algorithm"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	gocache "github.com/patrickmn/go-cache"
)

var _ = Describe("Scheduler overlapping wakeups", func() {
	DescribeTable("uses a real committed request and notification", func(ctx SpecContext, newer bool) {
		fixture := useSchedulerDB()
		fixture.Conn.SetMaxOpenConns(4)
		DeferCleanup(func() { fixture.Conn.SetMaxOpenConns(1) })
		_, pipeline := persistSchedulerPipeline(fixture, "wake-team", "wake-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "wake-job"}}})
		Expect(pipeline.Unpause()).To(Succeed())
		job := schedulerPipelineJob(pipeline, "wake-job")
		signal, err := fixture.Conn.Bus().ListenSignal(atc.ComponentScheduler)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(fixture.Conn.Bus().UnlistenSignal(atc.ComponentScheduler, signal)).To(Succeed()) })
		first := requestSchedulerJob(fixture, job)
		Eventually(ctx, signal.C()).Should(Receive())
		blocker, err := fixture.Conn.Begin()
		Expect(err).NotTo(HaveOccurred())
		var completions []*schedulerJobCompletion
		DeferCleanup(func() {
			_ = blocker.Rollback()
			for _, completion := range completions {
				Eventually(completion.done, 5*time.Second).Should(BeClosed())
			}
		})
		_, err = blocker.Exec("LOCK TABLE resource_pins IN ACCESS EXCLUSIVE MODE")
		Expect(err).NotTo(HaveOccurred())
		alg := algorithm.New(db.NewVersionsDB(fixture.Conn, 1000, gocache.New(time.Minute, time.Minute)))
		tracked := observeSchedulerJobFactory(fixture.JobFactory)
		runner := scheduler.NewRunner(lagertest.NewTestLogger("overlap"), tracked, &scheduler.Scheduler{
			Algorithm: alg, BuildStarter: scheduler.NewBuildStarter(builds.NewPlanner(atc.NewPlanFactory(0)), alg),
		}, 1, fixture.Conn.Bus())
		Expect(runner.Run(context.Background())).To(Succeed())
		firstDone := tracked.completion(job.ID())
		Expect(firstDone).NotTo(BeNil())
		completions = append(completions, firstDone)
		Eventually(ctx, func() (int, error) {
			var count int
			err := fixture.Conn.QueryRow("SELECT count(*) FROM pg_locks WHERE database=(SELECT oid FROM pg_database WHERE datname=current_database()) AND relation='resource_pins'::regclass AND mode='AccessShareLock' AND NOT granted").Scan(&count)
			return count, err
		}).Should(Equal(1))
		requested := first
		if newer {
			requested = requestSchedulerJob(fixture, job)
			Expect(requested.After(first)).To(BeTrue())
			Eventually(ctx, signal.C()).Should(Receive())
		}
		// Both scans use real persisted jobs. The second sees the first pass still
		// blocked in its real inputs query rather than replacing scheduling logic.
		Expect(runner.Run(context.Background())).To(Succeed())
		Expect(blocker.Rollback()).To(Succeed())
		Eventually(ctx, firstDone.done).Should(BeClosed())
		actual, consumed := schedulerJobTimestamps(fixture, job.ID())
		Expect(actual).To(Equal(requested))
		Expect(consumed).To(Equal(first))
		if !newer {
			Consistently(signal.C(), 2*time.Second).ShouldNot(Receive(), "the same request must not create a notification loop")
			return
		}
		Eventually(ctx, signal.C(), 2*time.Second).Should(Receive(), "a newer committed request must be woken after the old pass releases its job lock")
		Expect(runner.Run(context.Background())).To(Succeed())
		secondDone := tracked.completion(job.ID())
		Expect(secondDone).NotTo(BeNil())
		completions = append(completions, secondDone)
		Eventually(ctx, secondDone.done).Should(BeClosed())
		actual, consumed = schedulerJobTimestamps(fixture, job.ID())
		Expect(actual).To(Equal(requested))
		Expect(consumed).To(Equal(requested))
	},
		Entry("wakes a newer request after the active pass", true, SpecTimeout(30*time.Second)),
		Entry("does not wake again for the same request", false, SpecTimeout(30*time.Second)),
	)
})
