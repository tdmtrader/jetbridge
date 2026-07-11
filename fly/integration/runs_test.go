package integration_test

import (
	"net/http"
	"os/exec"

	"github.com/concourse/concourse/atc"
	"github.com/tedsuo/rata"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("runs", func() {
	It("lists runs with number, status, params and duration", func() {
		path, err := atc.Routes.CreatePathForRoute(atc.ListPipelineRuns,
			rata.Params{"team_name": "main", "pipeline_name": "regression-suite"})
		Expect(err).NotTo(HaveOccurred())

		atcServer.AppendHandlers(
			ghttp.CombineHandlers(
				ghttp.VerifyRequest("GET", path),
				ghttp.RespondWithJSONEncoded(http.StatusOK, []atc.PipelineRun{
					{Number: 2, Status: "running", Params: map[string]any{"ref": "def"}, CreatedAt: 1751500000},
					{Number: 1, Status: "succeeded", Params: map[string]any{"ref": "abc"}, CreatedAt: 1751400000, CompletedAt: 1751400300},
				}),
			),
		)

		flyCmd := exec.Command(flyPath, "-t", targetName, "runs", "-p", "regression-suite")
		sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())
		<-sess.Exited
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say(`2\s+running\s+ref=def`))
		Expect(sess.Out).To(gbytes.Say(`1\s+succeeded\s+ref=abc\s+5m0s`))
	})
})
