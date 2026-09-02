package concourse_test

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/go-concourse/concourse"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("ATC Handler Resource Versions", func() {
	Describe("ResourceVersions", func() {
		var (
			expectedURL      = "/api/v1/teams/some-team/pipelines/mypipeline/resources/myresource/versions"
			expectedQuery    []string
			expectedStatus   = http.StatusOK
			expectedVersions []atc.ResourceVersion
			pipelineRef      = atc.PipelineRef{Name: "mypipeline", InstanceVars: atc.InstanceVars{"branch": "master"}}
		)

		var page concourse.Page
		var filter atc.Version

		var versions []atc.ResourceVersion
		var found bool
		var clientErr error

		BeforeEach(func() {
			page = concourse.Page{}
			filter = atc.Version{}
			expectedQuery = []string{"vars.branch=%22master%22"}

			expectedVersions = []atc.ResourceVersion{
				{
					Version: atc.Version{"version": "v1"},
				},
				{
					Version: atc.Version{"version": "v2"},
				},
			}

		})

		JustBeforeEach(func() {

			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("GET", expectedURL, strings.Join(expectedQuery, "&")),
					ghttp.RespondWithJSONEncoded(expectedStatus, expectedVersions),
				),
			)

			versions, _, found, clientErr = team.ResourceVersions(pipelineRef, "myresource", page, filter)
		})

		Context("when from, to, and limit are 0", func() {
			It("calls to get all versions", func() {
				Expect(clientErr).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(versions).To(Equal(expectedVersions))
			})
		})

		Context("when the server returns an error", func() {
			BeforeEach(func() {
				expectedStatus = http.StatusInternalServerError
			})

			It("returns false and an error", func() {
				Expect(clientErr).To(HaveOccurred())
				Expect(found).To(BeFalse())
			})
		})

	})

	Describe("DisableResourceVersion", func() {
		var (
			expectedStatus    int
			pipelineName      = "banana"
			resourceName      = "myresource"
			resourceVersionID = 42
			expectedURL       = fmt.Sprintf("/api/v1/teams/some-team/pipelines/%s/resources/%s/versions/%s/disable", pipelineName, resourceName, strconv.Itoa(resourceVersionID))
			expectedQuery     = "vars.branch=%22master%22"
			pipelineRef       = atc.PipelineRef{Name: pipelineName, InstanceVars: atc.InstanceVars{"branch": "master"}}
		)

		JustBeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", expectedURL, expectedQuery),
					ghttp.RespondWith(expectedStatus, nil),
				),
			)
		})

		Context("when the disable resource call fails", func() {
			BeforeEach(func() {
				expectedStatus = http.StatusInternalServerError
			})

			It("calls the disable resource and returns an error", func() {
				Expect(func() {
					disabled, err := team.DisableResourceVersion(pipelineRef, resourceName, resourceVersionID)
					Expect(err).To(HaveOccurred())
					Expect(disabled).To(BeFalse())
				}).To(Change(func() int {
					return len(atcServer.ReceivedRequests())
				}).By(1))
			})
		})

	})

	Describe("EnableResourceVersion", func() {
		var (
			expectedStatus    int
			pipelineName      = "banana"
			resourceName      = "myresource"
			resourceVersionID = 42
			expectedURL       = fmt.Sprintf("/api/v1/teams/some-team/pipelines/%s/resources/%s/versions/%s/enable", pipelineName, resourceName, strconv.Itoa(resourceVersionID))
			expectedQuery     = "vars.branch=%22master%22"
			pipelineRef       = atc.PipelineRef{Name: pipelineName, InstanceVars: atc.InstanceVars{"branch": "master"}}
		)

		JustBeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", expectedURL, expectedQuery),
					ghttp.RespondWith(expectedStatus, nil),
				),
			)
		})

		Context("when the enable resource call fails", func() {
			BeforeEach(func() {
				expectedStatus = http.StatusInternalServerError
			})

			It("calls the enable resource and returns an error", func() {
				Expect(func() {
					enabled, err := team.EnableResourceVersion(pipelineRef, resourceName, resourceVersionID)
					Expect(err).To(HaveOccurred())
					Expect(enabled).To(BeFalse())
				}).To(Change(func() int {
					return len(atcServer.ReceivedRequests())
				}).By(1))
			})
		})

	})

	Describe("UnpinResource", func() {
		var (
			expectedStatus int
			pipelineName   = "banana"
			resourceName   = "myresource"
			expectedURL    = fmt.Sprintf("/api/v1/teams/some-team/pipelines/%s/resources/%s/unpin", pipelineName, resourceName)
			expectedQuery  = "vars.branch=%22master%22"
			pipelineRef    = atc.PipelineRef{Name: pipelineName, InstanceVars: atc.InstanceVars{"branch": "master"}}
		)

		JustBeforeEach(func() {
			atcServer.AppendHandlers(
				ghttp.CombineHandlers(
					ghttp.VerifyRequest("PUT", expectedURL, expectedQuery),
					ghttp.RespondWith(expectedStatus, nil),
				),
			)
		})

		Context("when the unpin resource call fails", func() {
			BeforeEach(func() {
				expectedStatus = http.StatusInternalServerError
			})

			It("calls the unpin resource and returns an error", func() {
				Expect(func() {
					pinned, err := team.UnpinResource(pipelineRef, resourceName)
					Expect(err).To(HaveOccurred())
					Expect(pinned).To(BeFalse())
				}).To(Change(func() int {
					return len(atcServer.ReceivedRequests())
				}).By(1))
			})
		})
	})
})
