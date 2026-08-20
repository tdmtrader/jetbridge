package concourse_test

import (
	"net/http"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/go-concourse/concourse"
	"github.com/concourse/concourse/go-concourse/concourse/internal"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("Pipeline runs client", func() {
	const collectionPath = "/api/v1/teams/some-team/pipelines/template/runs"

	Describe("CreatePipelineRun", func() {
		It("posts typed variables to the base pipeline and returns the committed child reference", func() {
			expected := pipelineRun(3)
			expected.InstanceRef = &atc.PipelineIdentifier{
				TeamName:     "child-team",
				PipelineName: "template",
				InstanceVars: atc.InstanceVars{"branch": "main"},
			}

			atcServer.AppendHandlers(ghttp.CombineHandlers(
				ghttp.VerifyRequest("POST", collectionPath),
				ghttp.VerifyHeaderKV("Content-Type", "application/json"),
				ghttp.VerifyJSONRepresenting(map[string]any{"vars": map[string]any{
					"enabled": true,
					"retries": float64(2),
					"branch":  "main",
				}}),
				ghttp.RespondWithJSONEncoded(http.StatusCreated, expected),
			))

			run, err := team.CreatePipelineRun("template", map[string]any{
				"enabled": true,
				"retries": 2.0,
				"branch":  "main",
			})

			Expect(err).NotTo(HaveOccurred())
			Expect(run).To(Equal(expected))
			Expect((*run.Params)["enabled"]).To(BeTrue())
			Expect((*run.Params)["retries"]).To(Equal(2.0))
			Expect((*run.Params)["branch"]).To(Equal("main"))
		})

		DescribeTable("preserves actionable failure responses",
			func(status int, body string) {
				atcServer.AppendHandlers(ghttp.CombineHandlers(
					ghttp.VerifyRequest("POST", collectionPath),
					ghttp.RespondWith(status, body),
				))

				_, err := team.CreatePipelineRun("template", map[string]any{})

				Expect(err).To(MatchError(ContainSubstring(body)))
				Expect(err).To(Equal(internal.UnexpectedResponseError{
					StatusCode: status,
					Status:     statusText(status),
					Body:       body,
				}))
			},
			Entry("bad parameters", http.StatusBadRequest, "parameter branch must be a string"),
			Entry("template conflict", http.StatusConflict, "pipeline is paused"),
		)
	})

	Describe("PipelineRuns", func() {
		It("lists the default page and decodes pagination links", func() {
			atcServer.AppendHandlers(ghttp.CombineHandlers(
				ghttp.VerifyRequest("GET", collectionPath),
				ghttp.RespondWithJSONEncoded(http.StatusOK, []atc.PipelineRun{pipelineRun(2)}, http.Header{
					"Link": {"<http://example.test/runs?from=2&limit=50>; rel=\"next\", <http://example.test/runs?to=5&limit=50>; rel=\"previous\""},
				}),
			))

			runs, pagination, err := team.PipelineRuns("template", concourse.Page{})

			Expect(err).NotTo(HaveOccurred())
			Expect(runs).To(HaveLen(1))
			Expect(pagination.Next).NotTo(BeNil())
			Expect(*pagination.Next).To(Equal(concourse.Page{From: 2, Limit: 50}))
			Expect(pagination.Previous).NotTo(BeNil())
			Expect(*pagination.Previous).To(Equal(concourse.Page{To: 5, Limit: 50}))
		})

		It("forwards explicit keyset page values", func() {
			atcServer.AppendHandlers(ghttp.CombineHandlers(
				ghttp.VerifyRequest("GET", collectionPath, "from=10&limit=20&to=30"),
				ghttp.RespondWithJSONEncoded(http.StatusOK, []atc.PipelineRun{}),
			))

			runs, pagination, err := team.PipelineRuns("template", concourse.Page{From: 10, To: 30, Limit: 20})

			Expect(err).NotTo(HaveOccurred())
			Expect(runs).To(BeEmpty())
			Expect(pagination).To(Equal(concourse.Pagination{}))
		})
	})

	Describe("PipelineRun", func() {
		It("gets a numbered run", func() {
			expected := pipelineRun(7)
			atcServer.AppendHandlers(ghttp.CombineHandlers(
				ghttp.VerifyRequest("GET", collectionPath+"/7"),
				ghttp.RespondWithJSONEncoded(http.StatusOK, expected),
			))

			run, found, err := team.PipelineRun("template", 7)

			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(run).To(Equal(expected))
		})

		It("returns found false only for a missing run", func() {
			atcServer.AppendHandlers(ghttp.CombineHandlers(
				ghttp.VerifyRequest("GET", collectionPath+"/9"),
				ghttp.RespondWith(http.StatusNotFound, ""),
			))

			_, found, err := team.PipelineRun("template", 9)

			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
		})

		It("returns non-404 errors with their response body", func() {
			atcServer.AppendHandlers(ghttp.CombineHandlers(
				ghttp.VerifyRequest("GET", collectionPath+"/9"),
				ghttp.RespondWith(http.StatusConflict, "payload is being reclaimed"),
			))

			_, found, err := team.PipelineRun("template", 9)

			Expect(found).To(BeFalse())
			Expect(err).To(Equal(internal.UnexpectedResponseError{
				StatusCode: http.StatusConflict,
				Status:     statusText(http.StatusConflict),
				Body:       "payload is being reclaimed",
			}))
		})
	})
})

func pipelineRun(number int) atc.PipelineRun {
	params := atc.Params{"enabled": true, "retries": 2.0, "branch": "main"}
	return atc.PipelineRun{
		ID:                 number,
		TemplatePipelineID: 1,
		Number:             number,
		Params:             &params,
		Status:             atc.RunStatusRunning,
		CreatedBy:          "some-user",
		CreatedAt:          time.Date(2026, time.August, 19, 12, 0, 0, 0, time.UTC),
	}
}

func statusText(status int) string {
	return string(rune(status/100+'0')) + string(rune(status/10%10+'0')) + string(rune(status%10+'0')) + " " + http.StatusText(status)
}
