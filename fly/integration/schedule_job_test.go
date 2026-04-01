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

var _ = Describe("schedule-job", func() {
	var (
		schedulePath string
		err          error
	)

	BeforeEach(func() {
		schedulePath, err = atc.Routes.CreatePathForRoute(atc.ScheduleJob, rata.Params{
			"pipeline_name": "some-pipeline",
			"job_name":      "some-job",
			"team_name":     "main",
		})
		Expect(err).NotTo(HaveOccurred())
	})

	Context("when the job exists", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", schedulePath),
					ghttp.RespondWith(http.StatusOK, ""),
				),
			)
		})

		It("schedules the job and prints success", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "schedule-job", "-j", "some-pipeline/some-job")

			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())

			Eventually(sess).Should(gbytes.Say(`scheduled 'some-job'`))

			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
		})
	})

	Context("when the job is not found", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", schedulePath),
					ghttp.RespondWith(http.StatusNotFound, ""),
				),
			)
		})

		It("prints an error and exits non-zero", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "schedule-job", "-j", "some-pipeline/some-job")

			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())

			<-sess.Exited
			Expect(sess.ExitCode()).NotTo(Equal(0))
			Expect(sess.Err).To(gbytes.Say(`some-pipeline/some-job not found`))
		})
	})
})
