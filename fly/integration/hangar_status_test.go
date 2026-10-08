package integration_test

import (
	"os/exec"

	"github.com/concourse/concourse/atc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("hangar-status", func() {
	run := func(args ...string) *gexec.Session {
		GinkgoHelper()
		flyCmd := exec.Command(flyPath, append([]string{"-t", targetName, "hangar-status"}, args...)...)
		sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())
		<-sess.Exited
		return sess
	}

	It("prints the in-service flag, the residue and the open findings with their ids", func() {
		atcServer.AppendHandlers(
			ghttp.CombineHandlers(
				ghttp.VerifyRequest("GET", "/api/v1/hangar/status"),
				ghttp.RespondWithJSONEncoded(200, atc.HangarStatus{
					Enabled: false,
					AtRisk:  true,
					Residue: atc.HangarResidue{PendingCaptures: 2, OpenClaims: 3, Total: 5, UnacknowledgedReleases: 5},
					Findings: []atc.HangarFinding{{
						ID: 42, Violation: "out_of_band_absence", Subject: "scope/digest/9", BlocksAdmission: true,
					}},
				}),
			),
		)

		sess := run()
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("out of service, draining"))
		Expect(sess.Out).To(gbytes.Say(`pending captures\s+2`))
		Expect(sess.Out).To(gbytes.Say(`open claims\s+3`))
		Expect(sess.Out).To(gbytes.Say(`total\s+5`))
		Expect(sess.Out).To(gbytes.Say(`captures released without node acknowledgement\s+5`))
		Expect(sess.Out).To(gbytes.Say(`open integrity findings\s+1`))
		Expect(sess.Out).To(gbytes.Say(`42\s+out_of_band_absence\s+scope/digest/9`))
	})

	It("says drained when the plane is out of service with no residue", func() {
		atcServer.AppendHandlers(
			ghttp.CombineHandlers(
				ghttp.VerifyRequest("GET", "/api/v1/hangar/status"),
				ghttp.RespondWithJSONEncoded(200, atc.HangarStatus{Enabled: false, Drained: true}),
			),
		)

		sess := run()
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("out of service, drained"))
	})

	It("resolves a finding by id", func() {
		atcServer.AppendHandlers(
			ghttp.CombineHandlers(
				ghttp.VerifyRequest("PUT", "/api/v1/hangar/findings/42/resolve"),
				ghttp.RespondWith(204, ""),
			),
		)

		sess := run("--resolve-finding", "42")
		Expect(sess.ExitCode()).To(Equal(0))
		Expect(sess.Out).To(gbytes.Say("resolved integrity finding 42"))
	})

	It("fails when the finding is not open", func() {
		atcServer.AppendHandlers(
			ghttp.CombineHandlers(
				ghttp.VerifyRequest("PUT", "/api/v1/hangar/findings/42/resolve"),
				ghttp.RespondWith(404, "no open integrity finding with that id"),
			),
		)

		sess := run("--resolve-finding", "42")
		Expect(sess.ExitCode()).To(Equal(1))
		Expect(sess.Err).To(gbytes.Say("resolving finding 42"))
	})
})
