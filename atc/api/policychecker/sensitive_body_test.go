package policychecker_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/policychecker"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/policy"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Sensitive request policy bodies", func() {
	DescribeTable("authorizes route metadata without reading or forwarding the body",
		func(action, contentType, bodyText string, allowed bool) {
			team := persistTeam("some-team", atc.TeamAuth{
				"member": {"users": {"test:some-user"}},
			})
			access := accessor.NewAccessor(accessor.Verification{
				HasToken: true, IsTokenValid: true,
				RawClaims: map[string]any{"name": "some-user", "federated_claims": map[string]any{"connector_id": "test"}},
			}, accessor.MemberRole, systemClaimKey, []string{systemClaimValue}, []db.Team{team}, nil)
			body := &observedPolicyBody{Reader: strings.NewReader(bodyText)}
			request := httptest.NewRequest(http.MethodPost, "/credentials?:team_name=some-team&:pipeline_name=some-pipeline", body)
			request.Header.Set("Content-Type", contentType)
			if !allowed {
				opaServer.Reset(`{"result":{"allowed":false,"block":true,"reasons":["handoff denied"]}}`)
			}

			result, err := policychecker.NewApiPolicyChecker(initializedChecker(policy.Filter{
				HttpMethods: []string{http.MethodPost},
			})).Check(action, access, request)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Allowed()).To(Equal(allowed))
			Expect(result.ShouldBlock()).To(Equal(!allowed))
			Expect(body.reads).To(BeZero(), "policy must leave sensitive body parsing to the authorized handler")
			Expect(opaServer.Requests()).To(HaveLen(1), "omitting the body must not bypass policy authorization")
			var received struct {
				Input policy.PolicyCheckInput `json:"input"`
			}
			Expect(json.Unmarshal(opaServer.Requests()[0], &received)).To(Succeed())
			Expect(received.Input).To(Equal(policy.PolicyCheckInput{
				Service: "concourse", ClusterName: "some-cluster", ClusterVersion: "some-version",
				HttpMethod: http.MethodPost, Action: action, User: "some-user",
				Team: "some-team", Roles: []string{"member"}, Pipeline: "some-pipeline",
			}))
			remaining, err := io.ReadAll(request.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(remaining)).To(Equal(bodyText))
		},
		Entry("credential JSON", atc.HandoffPipelineRunCredentials, "application/json",
			`{"tokens":{"access_token":"synthetic-access","refresh_token":"synthetic-refresh","id_token":"synthetic-id"}}`, true),
		Entry("credential form data cannot override the route", atc.HandoffPipelineRunCredentials, "application/x-www-form-urlencoded",
			`:pipeline_name=body-pipeline&refresh_token=synthetic-refresh`, true),
		Entry("denied credential handoff", atc.HandoffPipelineRunCredentials, "application/json",
			`{"tokens":{"refresh_token":"synthetic-denied-refresh"}}`, false),
		Entry("cancellation reason", atc.CancelPipelineRun, "application/json",
			`{"reason":"operational text"}`, true),
		Entry("versioned creation input bearer", atc.CreatePipelineRunV2, "application/json",
			`{"invocation_key":"k","inputs":{"change":{"source_id":"s","bearer":"synthetic-bearer"}}}`, true),
		Entry("input upload tree", atc.UploadPipelineRunInput, "application/json",
			`{"not":"a tar, but policy must not look"}`, true),
	)
})

type observedPolicyBody struct {
	io.Reader
	reads int
}

func (b *observedPolicyBody) Read(p []byte) (int, error) {
	b.reads++
	return b.Reader.Read(p)
}
