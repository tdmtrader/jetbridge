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
)

var _ = Describe("active-users", func() {
	Context("when there are active users", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", "/api/v1/users"),
					ghttp.RespondWithJSONEncoded(http.StatusOK, []atc.User{
						{Username: "admin", Connector: "local", LastLogin: 1711900800},
						{Username: "dev", Connector: "github", LastLogin: 1711814400},
					}),
				),
			)
		})

		It("lists the active users", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "active-users")

			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())

			<-sess.Exited
			Expect(sess.ExitCode()).To(Equal(0))

			Expect(sess.Out).To(gbytes.Say(`admin`))
			Expect(sess.Out).To(gbytes.Say(`dev`))
		})
	})

	Context("when --since is invalid", func() {
		It("returns an error about date format", func() {
			flyCmd := exec.Command(flyPath, "-t", targetName, "active-users", "--since", "not-a-date")

			sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
			Expect(err).NotTo(HaveOccurred())

			<-sess.Exited
			Expect(sess.ExitCode()).NotTo(Equal(0))
			Expect(sess.Err).To(gbytes.Say(`since time should be in the format`))
		})
	})
})
