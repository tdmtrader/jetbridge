package integration_test

import (
	"io"
	"strconv"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/event"
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

	Context("when the operator has enabled run creation", func() {
		BeforeEach(func() {
			cmd.EnablePipelineRunCreation = true
		})

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

	// The gate is off by default, which is the shape a server ships in. This
	// is the clause the HTTP gate test cannot make: the route is not involved
	// here at all, and before the port learned about the hold this step
	// admitted runs on a server that was refusing them to everyone else.
	It("fails the step with the hold on stderr when run creation is disabled", func() {
		build := triggerCallingJob(owner)
		Expect(build.Status).To(Equal(atc.StatusFailed), buildOutput(owner, build))

		_, stderr := buildStreams(owner, build)
		Expect(stderr).To(ContainSubstring(atc.ErrPipelineRunCreationDisabled.Error()))

		Expect(countRows("SELECT count(*) FROM pipeline_runs")).To(Equal(0))
		Expect(countRows("SELECT count(*) FROM pipelines WHERE pipeline_run_id IS NOT NULL")).To(Equal(0))
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

	var settled atc.Build
	Eventually(func() string {
		build, found, err := client.Build(strconv.Itoa(triggered.ID))
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
