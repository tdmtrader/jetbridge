package builds_test

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/atc"
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

type lifecycleBehavior atc.PlanID

const (
	lifecycleStarted  lifecycleBehavior = "started"
	lifecycleReleased lifecycleBehavior = "released"
	lifecyclePanic    lifecycleBehavior = "panic"
	lifecycleFinish   lifecycleBehavior = "finish"
)

// lifecycleEngine gives every build the same small domain lifecycle. The
// build's persisted plan chooses the behavior; tests can only observe the
// lifecycle gates and the resulting database state.
type lifecycleEngine struct {
	mu      sync.Mutex
	running map[string]struct{}

	started  chan struct{}
	release  chan struct{}
	released chan struct{}
	draining chan struct{}

	releaseOnce sync.Once
	drainOnce   sync.Once
}

func newLifecycleEngine() *lifecycleEngine {
	return &lifecycleEngine{
		running:  map[string]struct{}{},
		started:  make(chan struct{}, 1),
		release:  make(chan struct{}),
		released: make(chan struct{}, 1),
		draining: make(chan struct{}),
	}
}

func (engine *lifecycleEngine) NewBuild(build db.Build) builds.Runnable {
	return lifecycleBuild{engine: engine, build: build}
}

func (engine *lifecycleEngine) Drain(context.Context) {
	engine.drainOnce.Do(func() { close(engine.draining) })
}

func (engine *lifecycleEngine) Release() {
	engine.releaseOnce.Do(func() { close(engine.release) })
}

func (engine *lifecycleEngine) claim(build db.Build) (string, bool) {
	key := lifecycleKey(build)

	engine.mu.Lock()
	defer engine.mu.Unlock()

	if _, found := engine.running[key]; found {
		return key, false
	}
	engine.running[key] = struct{}{}
	return key, true
}

func (engine *lifecycleEngine) unclaim(key string) {
	engine.mu.Lock()
	delete(engine.running, key)
	engine.mu.Unlock()
}

func lifecycleKey(build db.Build) string {
	if build.ID() != 0 {
		return fmt.Sprintf("build-%d", build.ID())
	}
	return fmt.Sprintf("resource-%d", build.ResourceID())
}

type lifecycleBuild struct {
	engine *lifecycleEngine
	build  db.Build
}

func (running lifecycleBuild) Run(context.Context) {
	behavior := lifecycleBehavior(running.build.PrivatePlan().ID)
	if behavior == lifecycleStarted {
		key, claimed := running.engine.claim(running.build)
		if !claimed {
			if err := running.build.Finish(db.BuildStatusErrored); err != nil {
				panic(err)
			}
			return
		}
		defer running.engine.unclaim(key)
	}

	if running.build.Name() == db.CheckBuildName {
		if err := running.build.OnCheckBuildStart(); err != nil {
			panic(err)
		}
	}

	switch behavior {
	case lifecycleStarted:
		running.engine.started <- struct{}{}
		<-running.engine.release
		running.engine.released <- struct{}{}
	case lifecycleReleased:
		running.engine.released <- struct{}{}
	case lifecyclePanic:
		panic("lifecycle build panicked")
	case lifecycleFinish:
		if err := running.build.Finish(db.BuildStatusSucceeded); err != nil {
			panic(err)
		}
	default:
		panic(fmt.Sprintf("unknown lifecycle behavior %q", running.build.PrivatePlan().ID))
	}
}

func startedLifecycleCheckBuild(behavior lifecycleBehavior) db.Build {
	GinkgoHelper()
	build, created, err := resource.CreateBuild(
		context.Background(),
		true,
		atc.Plan{ID: atc.PlanID(behavior)},
	)
	Expect(err).NotTo(HaveOccurred())
	Expect(created).To(BeTrue())
	return build
}

func inMemoryLifecycleCheckBuild(behavior lifecycleBehavior) db.Build {
	GinkgoHelper()
	build, err := resource.CreateInMemoryBuild(
		context.Background(),
		atc.Plan{ID: atc.PlanID(behavior)},
		util.NewSequenceGenerator(1),
	)
	Expect(err).NotTo(HaveOccurred())
	return build
}

var _ = Describe("Tracker", func() {
	var (
		engine *lifecycleEngine

		tracker   *builds.Tracker
		buildChan chan db.Build
	)

	BeforeEach(func() {
		engine = newLifecycleEngine()
		DeferCleanup(engine.Release)

		buildChan = make(chan db.Build, 10)
		DeferCleanup(func() { close(buildChan) })
		tracker = builds.NewTracker(
			lagertest.NewTestLogger("test"),
			buildFactory,
			engine,
			buildChan,
		)
	})

	Describe("Run", func() {
		It("runs every started build", func() {
			first := startedJobBuild(string(lifecycleFinish))
			second := startedJobBuild(string(lifecycleFinish))
			third := startedJobBuild(string(lifecycleFinish))

			Expect(tracker.Run(context.TODO())).To(Succeed())

			Eventually(func() []db.BuildStatus {
				return []db.BuildStatus{statusOf(first.ID()), statusOf(second.ID()), statusOf(third.ID())}
			}).Should(ConsistOf(
				db.BuildStatusSucceeded,
				db.BuildStatusSucceeded,
				db.BuildStatusSucceeded,
			))
		})

		It("runs in-memory check builds pushed onto the channel", func() {
			buildChan <- inMemoryLifecycleCheckBuild(lifecycleFinish)

			Eventually(func() string { return inMemoryStatusOf(resource.ID()) }).
				Should(Equal(string(db.BuildStatusSucceeded)))
		})

		It("does not track a build it is already running", func() {
			build := startedJobBuild(string(lifecycleStarted))

			Expect(tracker.Run(context.TODO())).To(Succeed())
			Eventually(engine.started).Should(Receive())

			Expect(tracker.Run(context.TODO())).To(Succeed())
			Consistently(func() db.BuildStatus { return statusOf(build.ID()) }, 100*time.Millisecond).
				Should(Equal(db.BuildStatusStarted))

			engine.Release()
			Eventually(engine.released).Should(Receive())
			Expect(statusOf(build.ID())).To(Equal(db.BuildStatusStarted))
		})

		It("does not track a second in-memory check for a resource already running", func() {
			buildChan <- inMemoryLifecycleCheckBuild(lifecycleStarted)
			Eventually(engine.started).Should(Receive())
			Expect(inMemoryStatusOf(resource.ID())).To(Equal(string(db.BuildStatusStarted)))

			buildChan <- inMemoryLifecycleCheckBuild(lifecycleStarted)
			Consistently(func() string { return inMemoryStatusOf(resource.ID()) }, 100*time.Millisecond).
				Should(Equal(string(db.BuildStatusStarted)))

			engine.Release()
			Eventually(engine.released).Should(Receive())
			Eventually(func() string { return inMemoryStatusOf(resource.ID()) }).
				Should(Equal(string(db.BuildStatusErrored)))
		})

		It("errors a build whose run panics without stopping the others", func() {
			crashing := startedJobBuild(string(lifecyclePanic))
			healthy := startedJobBuild(string(lifecycleFinish))

			Expect(tracker.Run(context.TODO())).To(Succeed())

			Eventually(func() db.BuildStatus { return statusOf(crashing.ID()) }).
				Should(Equal(db.BuildStatusErrored))
			Eventually(func() db.BuildStatus { return statusOf(healthy.ID()) }).
				Should(Equal(db.BuildStatusSucceeded))
		})
	})

	Describe("finalizing builds whose run returned early", func() {
		// Run() returning while the build is still started is a legitimate
		// resume path for a job build (engine drain, tracking lock held by
		// another web, retryable step error): a later tracker cycle picks it up
		// again. A check build has no such cycle, so leaving it started would
		// leak the checkFactory's in-flight tracking forever.
		It("leaves a released job build started", func() {
			build := startedJobBuild(string(lifecycleReleased))

			Expect(tracker.Run(context.TODO())).To(Succeed())
			Eventually(engine.released).Should(Receive())

			Consistently(func() db.BuildStatus { return statusOf(build.ID()) }, 100*time.Millisecond).
				Should(Equal(db.BuildStatusStarted))
		})

		It("errors an orphaned check build", func() {
			build := startedLifecycleCheckBuild(lifecycleReleased)

			Expect(tracker.Run(context.TODO())).To(Succeed())
			Eventually(engine.released).Should(Receive())

			Eventually(func() db.BuildStatus { return statusOf(build.ID()) }).
				Should(Equal(db.BuildStatusErrored))
		})

		It("errors an orphaned in-memory check so its in-flight tracking is cleared", func() {
			buildChan <- inMemoryLifecycleCheckBuild(lifecycleReleased)
			Eventually(engine.released).Should(Receive())

			Eventually(func() string { return inMemoryStatusOf(resource.ID()) }).
				Should(Equal(string(db.BuildStatusErrored)))
		})

		It("does not re-finish a build that completed on its own", func() {
			build := startedJobBuild(string(lifecycleFinish))

			Expect(tracker.Run(context.TODO())).To(Succeed())

			Eventually(func() db.BuildStatus { return statusOf(build.ID()) }).
				Should(Equal(db.BuildStatusSucceeded))
			Consistently(func() db.BuildStatus { return statusOf(build.ID()) }, 100*time.Millisecond).
				Should(Equal(db.BuildStatusSucceeded))
		})
	})

	Describe("metrics", func() {
		It("counts a job build as running while it runs", func() {
			metric.Metrics.BuildsRunning.Max()
			build := startedJobBuild(string(lifecycleStarted))

			Expect(tracker.Run(context.TODO())).To(Succeed())
			Eventually(engine.started).Should(Receive())

			Expect(metric.Metrics.BuildsRunning.Max()).To(BeNumerically(">=", 1))
			Expect(statusOf(build.ID())).To(Equal(db.BuildStatusStarted))

			engine.Release()
			Eventually(engine.released).Should(Receive())
		})

		It("counts a check build as a running check while it runs", func() {
			metric.Metrics.CheckBuildsRunning.Max()
			build := startedLifecycleCheckBuild(lifecycleStarted)

			Expect(tracker.Run(context.TODO())).To(Succeed())
			Eventually(engine.started).Should(Receive())

			Expect(metric.Metrics.CheckBuildsRunning.Max()).To(BeNumerically(">=", 1))
			Expect(statusOf(build.ID())).To(Equal(db.BuildStatusStarted))

			engine.Release()
			Eventually(engine.released).Should(Receive())
			Eventually(func() db.BuildStatus { return statusOf(build.ID()) }).
				Should(Equal(db.BuildStatusErrored))
		})
	})

	Describe("Drain", func() {
		It("drains the engine", func() {
			var _ component.Drainable = tracker

			tracker.Drain(context.TODO())

			Eventually(engine.draining).Should(BeClosed())
		})
	})
})
