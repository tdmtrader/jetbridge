package integration_test

import (
	"net/http"
	"os/exec"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/ghttp"
	"github.com/tedsuo/rata"
)

var _ = Describe("paused-pipelines", func() {
	var (
		listPath string
		err      error
	)

	BeforeEach(func() {
		listPath, err = atc.Routes.CreatePathForRoute(atc.ListPipelines, rata.Params{
			"team_name": "main",
		})
		Expect(err).NotTo(HaveOccurred())
	})

	Context("when there are paused and unpaused pipelines", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", listPath),
					ghttp.RespondWithJSONEncoded(http.StatusOK, []atc.Pipeline{
						{ID: 1, Name: "active-pipeline", Paused: false, TeamName: "main"},
						{ID: 2, Name: "paused-pipeline", Paused: true, TeamName: "main", PausedBy: "admin"},
						{ID: 3, Name: "another-paused", Paused: true, TeamName: "main"},
					}),
				),
			)
		})

		It("lists only paused pipelines", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "paused-pipelines")

			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())

			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))

			Expect(sess.Out).To(gbytes.Say(`paused-pipeline`))
			Expect(sess.Out).To(gbytes.Say(`another-paused`))
		})

		It("does not list unpaused pipelines", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "paused-pipelines")

			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())

			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))

			Expect(sess.Out).NotTo(gbytes.Say(`active-pipeline`))
		})
	})
})
