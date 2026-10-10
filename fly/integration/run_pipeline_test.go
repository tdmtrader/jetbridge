package integration_test

import (
	"net/http"
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/ghttp"
	"github.com/tedsuo/rata"
)

// The refusal text is db.ErrPipelineRunPaused's, in
// atc/db/pipeline_run_errors.go. It is written out here rather than imported so
// that this spec describes a server's answer rather than sharing the server's
// own constant; keep the two in step.
const pausedRunCreation = "template pipeline is paused"

var _ = Describe("run-pipeline against a paused template", func() {
	var createPath, runsPath string

	BeforeEach(func() {
		var err error
		createPath, err = atc.Routes.CreatePathForRoute(atc.CreatePipelineRunV2, rata.Params{
			"team_name":     teamName,
			"pipeline_name": "some-template",
		})
		Expect(err).NotTo(HaveOccurred())

		runsPath, err = atc.Routes.CreatePathForRoute(atc.ListPipelineRuns, rata.Params{
			"team_name":     teamName,
			"pipeline_name": "some-template",
		})
		Expect(err).NotTo(HaveOccurred())
	})

	Context("when the server refuses with the run API's conflict envelope", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", createPath),
					ghttp.RespondWithJSONEncoded(http.StatusConflict, atc.SaveConfigResponse{
						Errors: []string{pausedRunCreation},
					}),
				),
			)
		})

		It("prints the server's reason as its own message and fails", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "run-pipeline", "-p", "some-template")

			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())

			<-sess.Exited
			Expect(sess.ExitCode()).NotTo(Equal(0))

			output := string(sess.Out.Contents()) + string(sess.Err.Contents())
			Expect(output).To(ContainSubstring(pausedRunCreation))

			// The two shapes that mean the client did not recognise the
			// refusal: the generic wrapper, and the raw body inside it.
			Expect(output).NotTo(ContainSubstring("Unexpected Response"))
			Expect(output).NotTo(ContainSubstring(`{"errors"`))
		})
	})

	Context("when the same body arrives with a status the client does not recognise", func() {
		// This spec exists only to show the one above can fail. The refusal
		// path already existed before the gate did, so a green run of that
		// spec on its own separates nothing.
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", createPath),
					ghttp.RespondWithJSONEncoded(http.StatusNotImplemented, atc.SaveConfigResponse{
						Errors: []string{pausedRunCreation},
					}),
				),
			)
		})

		It("degrades to the generic wrapper, which is what 409 buys", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "run-pipeline", "-p", "some-template")

			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())

			<-sess.Exited
			Expect(sess.ExitCode()).NotTo(Equal(0))

			output := string(sess.Out.Contents()) + string(sess.Err.Contents())
			Expect(output).To(ContainSubstring("Unexpected Response"))
		})
	})

	Context("when the invocation replays a run that has since been reclaimed", func() {
		BeforeEach(func() {
			completed := time.Unix(1000060, 0)
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", createPath),
					ghttp.VerifyJSON(`{"invocation_key":"release.42"}`),
					ghttp.RespondWithJSONEncoded(http.StatusOK, atc.PipelineRun{
						ID:               3,
						Number:           7,
						Status:           atc.RunStatusSucceeded,
						CreatedBy:        "someone",
						CreatedAt:        time.Unix(1000000, 0),
						CompletedAt:      &completed,
						Reclaimed:        true,
						AdmissionOutcome: atc.RunAdmissionReplayed,
					}, http.Header{"Idempotency-Replayed": []string{"true"}}),
				),
			)
		})

		It("reports the replayed run by its number and detail URL, with no payload", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "run-pipeline", "-p", "some-template", "--invocation-key", "release.42")

			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())

			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0), string(sess.Err.Contents()))
			Expect(string(sess.Out.Contents())).To(Equal(
				"already started some-template run #7 (succeeded, payload reclaimed)\n" +
					atcServer.URL() + "/teams/" + teamName + "/pipelines/some-template/runs/7\n",
			))
		})
	})

	Context("when the template is paused but runs already exist", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", runsPath),
					ghttp.RespondWithJSONEncoded(http.StatusOK, []atc.PipelineRun{
						{
							ID:        1,
							Number:    7,
							Status:    atc.RunStatusSucceeded,
							CreatedBy: "someone",
							CreatedAt: time.Unix(1000000, 0),
						},
					}),
				),
			)
		})

		It("still lists retained history", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "runs", "-p", "some-template")

			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())

			Eventually(sess).Should(gbytes.Say(`7`))
			Eventually(sess).Should(gbytes.Say(`succeeded`))

			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
		})
	})
})
