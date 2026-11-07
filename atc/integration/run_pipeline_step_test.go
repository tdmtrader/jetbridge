package integration_test

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/runs"
	concourse "github.com/concourse/concourse/go-concourse/concourse"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The run_pipeline step, end to end, against a real booted ATC on real
// Postgres.
//
// Everything below this file's assertions is production wiring: the scheduler
// picks up the manually triggered build, the engine builds the step over the
// admitter atc/atccmd/child_run_admitter.go constructed, exec assembles the
// build principal out of the step metadata the engine filled in, the port
// authorizes it against the builds row, and the run factory writes the run and
// its payload. Not one of those seams is exercised by the unit suites either
// side of it -- each of them stops at the boundary -- and the adapter in the
// composition root is exercised by nothing else at all.
//
// It is also the only place the three outcomes can be told apart the way a
// person tells them apart: by the colour of the step and what is written on
// the build's stderr.
//
// No worker is involved. run_pipeline runs on the ATC itself, like
// set_pipeline, so the caller's job needs nothing this suite does not already
// boot.

// callerPipelineRef is the pipeline whose job carries the step.
var callerPipelineRef = atc.PipelineRef{Name: "caller"}

// callerPipelineConfig is one job, one step, and params the template's
// declared schema accepts. The params are here rather than omitted because
// the step interpolates and digests them before admitting, and a call with
// none would leave that whole path untravelled.
var callerPipelineConfig = []byte(`
---
jobs:
- name: call
  plan:
  - run_pipeline: template
    params:
      environment: staging
`)

var _ = Describe("the run_pipeline step", func() {
	var owner concourse.Client

	JustBeforeEach(func() {
		givenARunnableTemplate()

		owner = login(atcURL, "test", "test")

		_, _, _, err := owner.Team("run-team").CreateOrUpdatePipelineConfig(
			callerPipelineRef, "0", callerPipelineConfig, false)
		Expect(err).NotTo(HaveOccurred())

		// Saved paused, like every pipeline; a paused pipeline's job would
		// never be scheduled and every spec here would time out waiting.
		_, err = owner.Team("run-team").UnpausePipeline(callerPipelineRef)
		Expect(err).NotTo(HaveOccurred())
	})

	Context("with a runnable template", func() {
		It("admits a run of the template and names it in the build's log", func() {
			build := triggerCallingJob(owner)
			Expect(build.Status).To(Equal(atc.StatusSucceeded), buildOutput(owner, build))

			// The run exists, and its creator is the build that asked -- the
			// column, not the model, because the claim is about what was
			// persisted under a build's name. The form is the one a person
			// reading pipeline_runs.created_by can follow back to the build.
			Expect(countRows("SELECT count(*) FROM pipeline_runs")).To(Equal(1))
			Expect(queryString("SELECT created_by FROM pipeline_runs")).
				To(Equal("build:run-team/caller/call#1"))

			// And the payload pipeline the run materialized, addressed the way
			// the run factory addresses it: the template's own name, under the
			// run's ordinal.
			Expect(countRows(
				`SELECT count(*) FROM pipelines
				 WHERE name = 'template' AND instance_vars = '{"run":1}'::jsonb`,
			)).To(Equal(1))

			// The line the person who triggered the build reads. Asserted on
			// stdout specifically: a refusal goes to stderr, and a spec that
			// looked at the whole stream could not tell the two apart.
			stdout, _ := buildStreams(owner, build)
			Expect(stdout).To(ContainSubstring("admitted run #1 of run-team/template"))
		})

		It("fails the step with the refusal on stderr when the template is paused", func() {
			// Paused rather than archived: archived would also be a refusal,
			// but paused is the state a person puts a template into on purpose
			// and the one whose message they will be reading.
			_, err := owner.Team("run-team").PausePipeline(runTemplateRef)
			Expect(err).NotTo(HaveOccurred())

			build := triggerCallingJob(owner)

			// Failed, not errored: a refusal is a fact about what was asked
			// for, and the author of the caller's config is the one who can
			// act on it.
			Expect(build.Status).To(Equal(atc.StatusFailed), buildOutput(owner, build))

			stdout, stderr := buildStreams(owner, build)
			Expect(stderr).To(ContainSubstring(runs.ErrTemplatePaused.Error()))
			Expect(stdout).NotTo(ContainSubstring("admitted run"))

			// No row, no number, no payload. The port refuses before the run
			// factory is reached, and the step's transaction rolls back.
			Expect(countRows("SELECT count(*) FROM pipeline_runs")).To(Equal(0))
			Expect(countRows("SELECT count(*) FROM pipelines WHERE pipeline_run_id IS NOT NULL")).To(Equal(0))
		})
	})

	// The suite boots every other spec with the output plane; this context
	// boots the web without one, so the real wiring (cmd.outputPlane() into
	// both admitters) is what refuses a Run of a template with no results or
	// inputs.
	Context("on a web with no output plane", func() {
		BeforeEach(func() {
			cmd.Kubernetes.OutputPlaneEnabled = false
			cmd.Kubernetes.OutputCaptureEnabled = false
			cmd.Kubernetes.HangarKey = ""
			cmd.Kubernetes.ArtifactDaemonTLSCert = ""
			cmd.Kubernetes.ArtifactDaemonTLSKey = ""
			cmd.Kubernetes.ArtifactDaemonTLSCACert = ""
		})

		It("refuses the v2 route's Run with a 409 in the refusal's own words", func() {
			member := authedHTTPClient(atcURL, "m-user", "m-user")
			refused := postCreateRun(member, "run-team", runTemplateRef.Name, runVars)
			Expect(refused.Status).To(Equal(http.StatusConflict))
			Expect(errorsFrom(refused)).To(ConsistOf(atc.ErrRunOutputPlaneNotInService.Error()))

			_, err := login(atcURL, "m-user", "m-user").Team("run-team").CreatePipelineRun(runTemplateRef.Name, v2Request(runVars))
			Expect(err).To(Equal(concourse.InvalidPipelineRunError{Errors: []string{atc.ErrRunOutputPlaneNotInService.Error()}}))

			Expect(countRows("SELECT count(*) FROM pipeline_runs")).To(Equal(0))
		})

		It("fails the step with the refusal on stderr", func() {
			build := triggerCallingJob(owner)
			Expect(build.Status).To(Equal(atc.StatusFailed), buildOutput(owner, build))

			stdout, stderr := buildStreams(owner, build)
			Expect(stderr).To(ContainSubstring(atc.ErrRunOutputPlaneNotInService.Error()))
			Expect(stdout).NotTo(ContainSubstring("admitted run"))
			Expect(countRows("SELECT count(*) FROM pipeline_runs")).To(Equal(0))
		})
	})
})

// A caller whose step waits for its child Run. The child is the task-less
// `entry` template, whose Run reaches succeeded on its own: its one build
// finishes with nothing to run and the Run results component publishes the
// Run's terminal status. No worker is involved at either end.
var waitingCallerConfig = []byte(`
---
jobs:
- name: call
  plan:
  - run_pipeline: template
    wait: true
    params:
      environment: staging
`)

// Two waited calls, the second binding a result of the first. The template
// declares no inputs and publishes no results, so the second admission is
// refused: the step must fail, not error, and the build read failed.
var forwardingCallerConfig = []byte(`
---
jobs:
- name: call
  plan:
  - run_pipeline: template
    wait: true
    params:
      environment: staging
  - run_pipeline: template
    wait: true
    params:
      environment: production
    inputs:
      x: {from: template, result: r}
`)

var _ = Describe("a run_pipeline step that waits", func() {
	var (
		owner           concourse.Client
		callerConfig    []byte
		observeInterval time.Duration
	)

	BeforeEach(func() {
		callerConfig = waitingCallerConfig

		// The ATC runs in this process, so the step's poll interval is this
		// package variable. Short, so a spec waits on the child, not the
		// ticker.
		observeInterval = exec.RunPipelineObserveInterval
		exec.RunPipelineObserveInterval = 500 * time.Millisecond
	})

	AfterEach(func() {
		exec.RunPipelineObserveInterval = observeInterval
	})

	JustBeforeEach(func() {
		givenARunnableTemplate()

		owner = login(atcURL, "test", "test")

		_, _, _, err := owner.Team("run-team").CreateOrUpdatePipelineConfig(
			callerPipelineRef, "0", callerConfig, false)
		Expect(err).NotTo(HaveOccurred())

		_, err = owner.Team("run-team").UnpausePipeline(callerPipelineRef)
		Expect(err).NotTo(HaveOccurred())
	})

	It("succeeds once the child Run has succeeded", func() {
		build := triggerCallingJob(owner)
		Expect(build.Status).To(Equal(atc.StatusSucceeded), buildOutput(owner, build))

		Expect(queryString(fmt.Sprintf("SELECT status FROM builds WHERE id = %d", build.ID))).
			To(Equal(string(atc.StatusSucceeded)))
		Expect(countRows("SELECT count(*) FROM pipeline_runs")).To(Equal(1))
		Expect(queryString("SELECT status FROM pipeline_runs")).To(Equal(string(atc.RunStatusSucceeded)))

		stdout, _ := buildStreams(owner, build)
		Expect(stdout).To(ContainSubstring("admitted run #1 of run-team/template"))
		Expect(stdout).To(ContainSubstring("run #1 succeeded"))
	})

	// The replay is the one a restarted web makes: the first ATC is stopped
	// while the step waits, which releases the build and leaves it started,
	// and the second ATC's build tracker picks it up and runs the plan again.
	// The first boot's step is parked on a long poll interval, so the only
	// step that can finish the build is the replayed one. In this process
	// that goroutine outlives its ATC, as it would not in production; parked
	// between polls it holds no connection and writes nothing.
	It("re-attaches to the same child Run when the build is tracked again", func() {
		exec.RunPipelineObserveInterval = time.Hour

		triggered, err := owner.Team("run-team").CreateJobBuild(callerPipelineRef, "call")
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() int {
			return countRows("SELECT count(*) FROM pipeline_runs")
		}, 90*time.Second, 250*time.Millisecond).Should(Equal(1), "the step never admitted its child")

		// Waiting: the child is admitted and the build stays started.
		Consistently(func() string {
			return queryString(fmt.Sprintf("SELECT status FROM builds WHERE id = %d", triggered.ID))
		}, 2*time.Second, 250*time.Millisecond).Should(Equal(string(atc.StatusStarted)))

		exec.RunPipelineObserveInterval = 500 * time.Millisecond
		restartATC()
		owner = login(atcURL, "test", "test")

		build := awaitSettled(owner, triggered.ID)
		Expect(build.Status).To(Equal(atc.StatusSucceeded), buildOutput(owner, build))
		Expect(countRows("SELECT count(*) FROM pipeline_runs")).To(Equal(1))

		stdout, _ := buildStreams(owner, build)
		Expect(stdout).To(ContainSubstring("admitted run #1 of run-team/template"))
		Expect(stdout).To(ContainSubstring("re-attached to run #1 of run-team/template"))
		Expect(stdout).To(ContainSubstring("run #1 succeeded"))
	})

	It("fails with the refusal on stderr when the template is paused", func() {
		_, err := owner.Team("run-team").PausePipeline(runTemplateRef)
		Expect(err).NotTo(HaveOccurred())

		build := triggerCallingJob(owner)
		Expect(build.Status).To(Equal(atc.StatusFailed), buildOutput(owner, build))

		_, stderr := buildStreams(owner, build)
		Expect(stderr).To(ContainSubstring(runs.ErrTemplatePaused.Error()))
		Expect(countRows("SELECT count(*) FROM pipeline_runs")).To(Equal(0))
	})

	Context("when a later call binds a result the first child never published", func() {
		BeforeEach(func() {
			callerConfig = forwardingCallerConfig
		})

		It("fails the second step with the refusal on stderr, and the build fails", func() {
			build := triggerCallingJob(owner)
			Expect(build.Status).To(Equal(atc.StatusFailed), buildOutput(owner, build))

			stdout, stderr := buildStreams(owner, build)
			Expect(stdout).To(ContainSubstring("run #1 succeeded"))
			// The template declares no inputs, so the binding is refused as
			// one it does not match before any source is looked up.
			Expect(stderr).To(ContainSubstring(atc.ErrInvalidRunInputs.Error()))

			// The first call's child, and no second one.
			Expect(countRows("SELECT count(*) FROM pipeline_runs")).To(Equal(1))
		})
	})
})

// triggerCallingJob triggers the caller's job and returns the build once it
// has reached a terminal state.
//
// Manually triggered, because a job with no inputs has nothing to trigger it
// and this suite boots no worker to produce any.
func triggerCallingJob(client concourse.Client) atc.Build {
	GinkgoHelper()

	triggered, err := client.Team("run-team").CreateJobBuild(callerPipelineRef, "call")
	Expect(err).NotTo(HaveOccurred())

	return awaitSettled(client, triggered.ID)
}

// awaitSettled returns the build once it has reached a terminal state.
func awaitSettled(client concourse.Client, buildID int) atc.Build {
	GinkgoHelper()

	var settled atc.Build
	Eventually(func() string {
		build, found, err := client.Build(strconv.Itoa(buildID))
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())

		settled = build

		return string(build.Status)
	}, 90*time.Second, time.Second).ShouldNot(BeElementOf(
		string(atc.StatusPending), string(atc.StatusStarted),
	), "the triggered build never settled")

	return settled
}

// buildStreams reads the build's event stream and returns what the step wrote
// to stdout and to stderr, kept apart.
//
// The split is the assertion, not a convenience: exec.RunPipelineStep writes a
// refusal to stderr and the admitted run's line to stdout, and that is exactly
// the difference between "your config is wrong" and "here is your run". A
// helper that concatenated them would pass on a step that mixed them up.
func buildStreams(client concourse.Client, build atc.Build) (string, string) {
	GinkgoHelper()

	events, err := client.BuildEvents(strconv.Itoa(build.ID))
	Expect(err).NotTo(HaveOccurred())
	defer events.Close()

	var stdout, stderr string
	for {
		next, err := events.NextEvent()
		if err == io.EOF {
			break
		}
		Expect(err).NotTo(HaveOccurred())

		logged, isLog := next.(event.Log)
		if !isLog {
			continue
		}

		if logged.Origin.Source == event.OriginSourceStderr {
			stderr += logged.Payload
		} else {
			stdout += logged.Payload
		}
	}

	return stdout, stderr
}

// buildOutput is what a status assertion prints when it fails: both streams,
// so a build that went red for some reason this file did not anticipate says
// why instead of only saying "failed".
func buildOutput(client concourse.Client, build atc.Build) string {
	stdout, stderr := buildStreams(client, build)

	return "build " + strconv.Itoa(build.ID) + " stdout:\n" + stdout + "\nstderr:\n" + stderr
}

// queryString reads one text column, alongside the suite's countRows.
func queryString(query string) string {
	GinkgoHelper()

	conn := postgresRunner.OpenSingleton()
	defer conn.Close()

	var value string
	Expect(conn.QueryRow(query).Scan(&value)).To(Succeed())

	return value
}
