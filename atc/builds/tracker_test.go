package builds_test

import (
	"context"
	"io"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/atc/builds"
	"github.com/concourse/concourse/atc/component"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func init() {
	util.PanicSink = io.Discard
}

type runnableFunc func(context.Context)

func (f runnableFunc) Run(ctx context.Context) { f(ctx) }

// stubEngine stands in for the real engine, which recovers its own panics
// (engine.go), so a Runnable that crashes or hangs cannot be expressed through
// it, and whose Drain has no effect observable from outside it.
type stubEngine struct {
	newBuild func(db.Build) builds.Runnable
	drained  []context.Context
}

func (e *stubEngine) NewBuild(build db.Build) builds.Runnable { return e.newBuild(build) }

func (e *stubEngine) Drain(ctx context.Context) { e.drained = append(e.drained, ctx) }

var _ = Describe("Tracker", func() {
	var (
		engine *stubEngine

		tracker   *builds.Tracker
		buildChan chan db.Build
	)

	BeforeEach(func() {
		engine = &stubEngine{newBuild: func(db.Build) builds.Runnable {
			return runnableFunc(func(context.Context) {})
		}}
		buildChan = make(chan db.Build, 10)

		tracker = builds.NewTracker(
			lagertest.NewTestLogger("test"),
			buildFactory,
			engine,
			buildChan,
		)
	})

	// runsInto reports each build the engine is asked to run.
	runsInto := func(running chan<- db.Build) {
		engine.newBuild = func(build db.Build) builds.Runnable {
			return runnableFunc(func(context.Context) {
				running <- build
			})
		}
	}

	Describe("Run", func() {
		It("runs every started build", func() {
			first := startedJobBuild("first")
			second := startedJobBuild("second")
			third := startedJobBuild("third")

			running := make(chan db.Build, 3)
			runsInto(running)

			Expect(tracker.Run(context.TODO())).To(Succeed())

			Expect([]int{(<-running).ID(), (<-running).ID(), (<-running).ID()}).
				To(ConsistOf(first.ID(), second.ID(), third.ID()))
		})

		It("runs in-memory check builds pushed onto the channel", func() {
			running := make(chan db.Build, 1)
			runsInto(running)

			build := inMemoryCheckBuild()
			buildChan <- build

			Eventually(running).Should(Receive())
		})

		It("does not track a build it is already running", func() {
			startedJobBuild("already-running")

			wait := make(chan struct{})
			defer close(wait)

			running := make(chan db.Build, 3)
			engine.newBuild = func(build db.Build) builds.Runnable {
				return runnableFunc(func(context.Context) {
					running <- build
					<-wait
				})
			}

			Expect(tracker.Run(context.TODO())).To(Succeed())
			<-running

			Expect(tracker.Run(context.TODO())).To(Succeed())

			Consistently(running, 100*time.Millisecond).ShouldNot(Receive())
		})

		It("does not track a second in-memory check for a resource already running", func() {
			wait := make(chan struct{})
			defer close(wait)

			running := make(chan db.Build, 3)
			engine.newBuild = func(build db.Build) builds.Runnable {
				return runnableFunc(func(context.Context) {
					running <- build
					<-wait
				})
			}

			// Two dispatches for the same resource, as the check factory makes
			// them: distinct builds with no id yet, deduplicated by resource.
			buildChan <- inMemoryCheckBuild()
			<-running
			buildChan <- inMemoryCheckBuild()

			Consistently(running, 100*time.Millisecond).ShouldNot(Receive())
		})

		It("errors a build whose run panics without stopping the others", func() {
			crashing := startedJobBuild("crashing")
			healthy := startedJobBuild("healthy")

			running := make(chan db.Build, 2)
			engine.newBuild = func(build db.Build) builds.Runnable {
				id := build.ID()
				return runnableFunc(func(context.Context) {
					if id == crashing.ID() {
						panic("something went wrong")
					}
					running <- build
				})
			}

			Expect(tracker.Run(context.TODO())).To(Succeed())

			Expect((<-running).ID()).To(Equal(healthy.ID()))
			Eventually(func() db.BuildStatus { return statusOf(crashing.ID()) }).
				Should(Equal(db.BuildStatusErrored))
		})
	})

	Describe("finalizing builds whose run returned early", func() {
		// Run() returning while the build is still started is a legitimate
		// resume path for a job build (engine drain, tracking lock held by
		// another web, retryable step error): a later tracker cycle picks it up
		// again. A check build has no such cycle, so leaving it started would
		// leak the checkFactory's in-flight tracking forever.
		returnsWithoutFinishing := func(done chan<- struct{}) {
			engine.newBuild = func(db.Build) builds.Runnable {
				return runnableFunc(func(context.Context) { close(done) })
			}
		}

		It("leaves a released job build started", func() {
			build := startedJobBuild("released")

			done := make(chan struct{})
			returnsWithoutFinishing(done)

			Expect(tracker.Run(context.TODO())).To(Succeed())
			<-done

			Consistently(func() db.BuildStatus { return statusOf(build.ID()) }, 100*time.Millisecond).
				Should(Equal(db.BuildStatusStarted))
		})

		It("errors an orphaned check build", func() {
			build := startedCheckBuild()

			done := make(chan struct{})
			returnsWithoutFinishing(done)

			Expect(tracker.Run(context.TODO())).To(Succeed())
			<-done

			Eventually(func() db.BuildStatus { return statusOf(build.ID()) }).
				Should(Equal(db.BuildStatusErrored))
		})

		It("errors an orphaned in-memory check so its in-flight tracking is cleared", func() {
			done := make(chan struct{})
			returnsWithoutFinishing(done)

			buildChan <- inMemoryCheckBuild()
			<-done

			Eventually(func() string { return inMemoryStatusOf(resource.ID()) }).
				Should(Equal(string(db.BuildStatusErrored)))
		})

		It("does not re-finish a build that completed on its own", func() {
			build := startedJobBuild("completed")

			done := make(chan struct{})
			engine.newBuild = func(b db.Build) builds.Runnable {
				return runnableFunc(func(context.Context) {
					Expect(b.Finish(db.BuildStatusSucceeded)).To(Succeed())
					close(done)
				})
			}

			Expect(tracker.Run(context.TODO())).To(Succeed())
			<-done

			Consistently(func() db.BuildStatus { return statusOf(build.ID()) }, 100*time.Millisecond).
				Should(Equal(db.BuildStatusSucceeded))
		})
	})

	Describe("metrics", func() {
		It("counts a job build as running while it runs", func() {
			metric.Metrics.BuildsRunning.Max()
			startedJobBuild("metric-job")

			var seenDuringRun float64
			done := make(chan struct{})
			engine.newBuild = func(db.Build) builds.Runnable {
				return runnableFunc(func(context.Context) {
					seenDuringRun = metric.Metrics.BuildsRunning.Max()
					close(done)
				})
			}

			Expect(tracker.Run(context.TODO())).To(Succeed())
			<-done

			Expect(seenDuringRun).To(BeNumerically(">=", 1))
		})

		It("counts a check build as a running check while it runs", func() {
			metric.Metrics.CheckBuildsRunning.Max()
			startedCheckBuild()

			var seenDuringRun float64
			done := make(chan struct{})
			engine.newBuild = func(db.Build) builds.Runnable {
				return runnableFunc(func(context.Context) {
					seenDuringRun = metric.Metrics.CheckBuildsRunning.Max()
					close(done)
				})
			}

			Expect(tracker.Run(context.TODO())).To(Succeed())
			<-done

			Expect(seenDuringRun).To(BeNumerically(">=", 1))
		})
	})

	Describe("Drain", func() {
		It("drains the engine", func() {
			var _ component.Drainable = tracker

			ctx := context.TODO()
			tracker.Drain(ctx)

			Expect(engine.drained).To(Equal([]context.Context{ctx}))
		})
	})
})
