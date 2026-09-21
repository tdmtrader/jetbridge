package auditor_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/auditor"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The auditor runs before the handler has authenticated or authorized
// anything. For a route whose body is a credential, an input bearer or an
// upload, parsing the form there both copies the body into the audit log and
// consumes it, so the handler that owns it then sees nothing.
var _ = Describe("Audit of sensitive request bodies", func() {
	var logger *lagertest.TestLogger
	var aud auditor.Auditor

	BeforeEach(func() {
		logger = lagertest.NewTestLogger("audit")
		aud = auditor.NewAuditor(false, false, false, true, false, false, false, false, false, logger)
	})

	routed := func(target, body string) *http.Request {
		// The router prepends route parameters to the query before the
		// auditor sees the request; this is that shape.
		request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return request
	}

	It("keeps a form-encoded credential handoff out of the log and leaves the body to the handler", func() {
		const body = `refresh_token=SECRET-REFRESH&{"tokens":{"refresh_token":"SECRET-REFRESH"}}`
		request := routed("/api/v2/teams/t/pipelines/review/runs/3/credentials/findings?:team_name=t&:pipeline_name=review&:number=3&:result_name=findings", body)

		aud.Audit(atc.HandoffPipelineRunCredentials, "owner", request)

		logs := logger.Logs()
		Expect(logs).To(HaveLen(1))
		Expect(logs[0].Data["action"]).To(Equal(atc.HandoffPipelineRunCredentials))
		Expect(logs[0].Data["parameters"]).To(Equal(map[string]any{
			"team_name": "t", "pipeline_name": "review", "number": "3", "result_name": "findings",
		}))
		Expect(string(logger.Buffer().Contents())).NotTo(ContainSubstring("SECRET-REFRESH"))

		remaining, err := io.ReadAll(request.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(remaining)).To(Equal(body), "the handler must still receive the whole body")
		Expect(request.Form).To(BeNil(), "the auditor must not leave a parsed form for the handler to trust")
	})

	DescribeTable("logs only declared route parameters for every sensitive action",
		func(action, target string, want map[string]any) {
			const body = "bearer=SECRET-BEARER&:input_name=SECRET-OVERRIDE"
			request := routed(target+"&undeclared=SECRET-QUERY", body)

			aud.Audit(action, "member", request)

			logs := logger.Logs()
			Expect(logs).To(HaveLen(1))
			Expect(logs[0].Data["parameters"]).To(Equal(want))
			Expect(string(logger.Buffer().Contents())).NotTo(ContainSubstring("SECRET-"))
			remaining, err := io.ReadAll(request.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(remaining)).To(Equal(body))
		},
		Entry("versioned creation carries input bearers", atc.CreatePipelineRunV2,
			"/api/v2/teams/t/pipelines/review/runs?:team_name=t&:pipeline_name=review",
			map[string]any{"team_name": "t", "pipeline_name": "review"}),
		Entry("input upload carries the tree", atc.UploadPipelineRunInput,
			"/api/v1/teams/t/pipelines/review/run-inputs/change?:team_name=t&:pipeline_name=review&:input_name=change",
			map[string]any{"team_name": "t", "pipeline_name": "review", "input_name": "change"}),
		Entry("cancellation carries operational text", atc.CancelPipelineRun,
			"/api/v1/teams/t/pipelines/review/runs/3/cancel?:team_name=t&:pipeline_name=review&:number=3",
			map[string]any{"team_name": "t", "pipeline_name": "review", "number": "3"}),
	)

	It("still parses the form for an ordinary route", func() {
		request := routed("/api/v1/teams/t/pipelines/p/pause?:team_name=t&:pipeline_name=p", "")

		aud.Audit(atc.PausePipeline, "member", request)

		Expect(logger.Logs()).To(HaveLen(1))
		Expect(request.Form).NotTo(BeNil())
	})
})
