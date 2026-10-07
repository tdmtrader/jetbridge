package concourse_test

import (
	"net/http"

	"github.com/concourse/concourse/atc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("Hangar status functions", func() {
	It("reads the status", func() {
		atcServer.AppendHandlers(
			ghttp.CombineHandlers(
				ghttp.VerifyRequest("GET", "/api/v1/hangar/status"),
				ghttp.RespondWithJSONEncoded(http.StatusOK, atc.HangarStatus{
					Enabled:  false,
					Drained:  false,
					Residue:  atc.HangarResidue{PendingCaptures: 1, Total: 1},
					Findings: []atc.HangarFinding{{ID: 7, Violation: "out_of_band_absence"}},
				}),
			),
		)

		status, err := client.HangarStatus()
		Expect(err).NotTo(HaveOccurred())
		Expect(status.Residue.PendingCaptures).To(Equal(1))
		Expect(status.Findings).To(HaveLen(1))
		Expect(status.Findings[0].ID).To(BeEquivalentTo(7))
	})

	It("resolves a finding by id", func() {
		atcServer.AppendHandlers(
			ghttp.CombineHandlers(
				ghttp.VerifyRequest("PUT", "/api/v1/hangar/findings/7/resolve"),
				ghttp.RespondWith(http.StatusNoContent, nil),
			),
		)

		Expect(client.ResolveHangarFinding(7)).To(Succeed())
	})
})
