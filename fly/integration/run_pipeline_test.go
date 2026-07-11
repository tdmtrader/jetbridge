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

var _ = Describe("run-pipeline", func() {
	It("POSTs params and prints the created run", func() {
		path, err := atc.Routes.CreatePathForRoute(atc.CreatePipelineRun,
			rata.Params{"team_name": "main", "pipeline_name": "regression-suite"})
		Expect(err).NotTo(HaveOccurred())

		atcServer.AppendHandlers(
			ghttp.CombineHandlers(
				ghttp.VerifyRequest("POST", path),
				ghttp.VerifyJSON(`{"params":{"ref":"abc","procs":2}}`),
				ghttp.RespondWithJSONEncoded(http.StatusCreated,
					atc.PipelineRun{ID: 9, Number: 42, Status: "running"}),
			),
		)

		flyCmd := exec.Command(flyPath, "-t", targetName, "run-pipeline",
			"-p", "regression-suite", "-v", "ref=abc", "-y", "procs=2")
		sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())
		<-sess.Exited
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say(`created run regression-suite#42`))
	})

	It("fails politely when the server rejects params", func() {
		path, err := atc.Routes.CreatePathForRoute(atc.CreatePipelineRun,
			rata.Params{"team_name": "main", "pipeline_name": "regression-suite"})
		Expect(err).NotTo(HaveOccurred())

		atcServer.AppendHandlers(
			ghttp.CombineHandlers(
				ghttp.VerifyRequest("POST", path),
				ghttp.RespondWith(http.StatusBadRequest, `unknown param "bogus"`),
			),
		)

		flyCmd := exec.Command(flyPath, "-t", targetName, "run-pipeline",
			"-p", "regression-suite", "-v", "bogus=x")
		sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())
		<-sess.Exited
		Expect(sess.ExitCode()).NotTo(Equal(0))
	})
})
