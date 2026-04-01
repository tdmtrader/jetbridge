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

var _ = Describe("rerun-build", func() {
	var (
		rerunPath string
		err       error
	)

	BeforeEach(func() {
		rerunPath, err = atc.Routes.CreatePathForRoute(atc.RerunJobBuild, rata.Params{
			"pipeline_name": "some-pipeline",
			"job_name":      "some-job",
			"build_name":    "42",
			"team_name":     "main",
		})
		Expect(err).NotTo(HaveOccurred())
	})

	Context("when the build exists", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", rerunPath),
					ghttp.RespondWithJSONEncoded(http.StatusOK, atc.Build{ID: 99, Name: "43"}),
				),
			)
		})

		It("reruns the build and prints the new build number", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "rerun-build", "-j", "some-pipeline/some-job", "-b", "42")

			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())

			Eventually(sess).Should(gbytes.Say(`started some-pipeline/some-job #43`))

			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))
		})
	})

	Context("when the build does not exist", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", rerunPath),
					ghttp.RespondWith(http.StatusNotFound, ""),
				),
			)
		})

		It("exits with an error", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "rerun-build", "-j", "some-pipeline/some-job", "-b", "42")

			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())

			<-sess.Exited
			Expect(sess.ExitCode()).NotTo(Equal(0))
		})
	})
})
