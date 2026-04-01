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

var _ = Describe("paused-jobs", func() {
	var (
		listPath string
		err      error
	)

	BeforeEach(func() {
		listPath, err = atc.Routes.CreatePathForRoute(atc.ListJobs, rata.Params{
			"pipeline_name": "some-pipeline",
			"team_name":     "main",
		})
		Expect(err).NotTo(HaveOccurred())
	})

	Context("when there are paused and unpaused jobs", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", listPath),
					ghttp.RespondWithJSONEncoded(http.StatusOK, []atc.Job{
						{ID: 1, Name: "active-job", Paused: false},
						{ID: 2, Name: "paused-job", Paused: true, PausedBy: "admin"},
						{ID: 3, Name: "another-paused-job", Paused: true},
					}),
				),
			)
		})

		It("lists only paused jobs", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "paused-jobs", "-p", "some-pipeline")

			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())

			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))

			Expect(sess.Out).To(gbytes.Say(`paused-job`))
			Expect(sess.Out).To(gbytes.Say(`another-paused-job`))
		})

		It("does not list unpaused jobs", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "paused-jobs", "-p", "some-pipeline")

			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())

			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))

			Expect(sess.Out).NotTo(gbytes.Say(`active-job`))
		})
	})
})
