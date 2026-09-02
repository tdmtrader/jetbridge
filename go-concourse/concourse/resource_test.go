package concourse_test

import (
	"net/http"
	"strings"

	"github.com/concourse/concourse/atc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("ATC Handler Resource", func() {
	Describe("Resource", func() {
		var (
			found     bool
			clientErr error

			expectedURL   = "/api/v1/teams/some-team/pipelines/some-pipeline/resources/myresource"
			expectedQuery = "vars.branch=%22master%22"
			pipelineRef   = atc.PipelineRef{Name: "some-pipeline", InstanceVars: atc.InstanceVars{"branch": "master"}}
		)

		JustBeforeEach(func() {
			_, found, clientErr = team.Resource(pipelineRef, "myresource")
		})

		Context("when the server returns a 500", func() {
			BeforeEach(func() {
				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", expectedURL, expectedQuery),
						ghttp.RespondWith(http.StatusInternalServerError, ""),
					),
				)
			})

			It("returns false for found and an error", func() {
				Expect(clientErr).To(HaveOccurred())
				Expect(found).To(BeFalse())
			})
		})
	})

	Describe("ClearResourceCache", func() {
		var (
			expectedQueryParams []string
			expectedURL         = "/api/v1/teams/some-team/pipelines/some-pipeline/resources/some-resource/cache"
			version             = atc.Version{}
			pipelineRef         = atc.PipelineRef{Name: "some-pipeline"}
		)

		Context("when the API call errors", func() {
			BeforeEach(func() {
				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("DELETE", expectedURL, strings.Join(expectedQueryParams, "&")),
						ghttp.RespondWithJSONEncoded(http.StatusInternalServerError, nil),
					),
				)
			})
			It("returns error", func() {
				_, err := team.ClearResourceCache(pipelineRef, "some-resource", version)
				Expect(err).To(HaveOccurred())
			})
		})
	})

	Describe("ListSharedForResource", func() {
		var (
			clientErr error
			found     bool

			expectedURL   = "/api/v1/teams/some-team/pipelines/some-pipeline/resources/myresource/shared"
			expectedQuery = "vars.branch=%22master%22"
			pipelineRef   = atc.PipelineRef{Name: "some-pipeline", InstanceVars: atc.InstanceVars{"branch": "master"}}
		)

		JustBeforeEach(func() {
			_, found, clientErr = team.ListSharedForResource(pipelineRef, "myresource")
		})

		Context("when the server returns a 500", func() {
			BeforeEach(func() {
				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", expectedURL, expectedQuery),
						ghttp.RespondWith(http.StatusInternalServerError, ""),
					),
				)
			})

			It("returns an error and not found", func() {
				Expect(clientErr).To(HaveOccurred())
				Expect(found).To(BeFalse())
			})
		})
	})
})
