package concourse_test

import (
	"net/http"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/go-concourse/concourse"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("CheckPrototype", func() {
	var (
		expectedURL   = "/api/v1/teams/some-team/pipelines/mypipeline/prototypes/myprototype/check"
		expectedQuery = "vars.branch=%22master%22"
		pipelineRef   = atc.PipelineRef{Name: "mypipeline", InstanceVars: atc.InstanceVars{"branch": "master"}}
	)

	Context("when ATC request succeeds", func() {
		var expectedCheck atc.Build

		BeforeEach(func() {
			expectedCheck = atc.Build{
				ID:        123,
				Status:    "started",
				StartTime: 100000000000,
				EndTime:   100000000000,
			}

			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", expectedURL, expectedQuery),
					ghttp.VerifyJSON(`{"from":{"ref":"fake-ref"},"shallow":true}`),
					ghttp.RespondWithJSONEncoded(http.StatusOK, expectedCheck),
				),
			)
		})

		It("sends check resource request to ATC", func() {
			check, found, err := team.CheckPrototype(pipelineRef, "myprototype", atc.Version{"ref": "fake-ref"}, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(check).To(Equal(expectedCheck))

			Expect(atcServer.ReceivedRequests()).To(HaveLen(1))
		})
	})

	Context("when the template guard refuses the check", func() {
		BeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", expectedURL, expectedQuery),
					ghttp.RespondWithJSONEncoded(http.StatusConflict, atc.SaveConfigResponse{
						Errors: []string{"pipeline templates cannot be checked directly"},
					}),
				),
			)
		})

		It("returns the sentence the server phrased rather than a raw body", func() {
			// This fails if the 409 envelope reaches fly wrapped in
			// "Unexpected Response", which prints the JSON at the user.
			_, _, err := team.CheckPrototype(pipelineRef, "myprototype", atc.Version{"ref": "fake-ref"}, true)
			Expect(err).To(Equal(concourse.APIRefusalError{
				Errors: []string{"pipeline templates cannot be checked directly"},
			}))
			Expect(err.Error()).To(Equal("pipeline templates cannot be checked directly"))
		})
	})
})
