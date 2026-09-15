package auth_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/configserver"
	"github.com/concourse/concourse/atc/api/policychecker"
	"github.com/concourse/concourse/atc/auditor"
	"github.com/concourse/concourse/atc/policy"
	"github.com/concourse/concourse/atc/wrappa"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/tedsuo/rata"
)

type configPolicy struct {
	policy.NoopChecker
	inputs []policy.PolicyCheckInput
	denied bool
}

func (p *configPolicy) ShouldSkipAction(string) bool      { return false }
func (p *configPolicy) ShouldCheckHttpMethod(string) bool { return true }
func (p *configPolicy) Check(input policy.PolicyCheckInput) (policy.PolicyCheckResult, error) {
	p.inputs = append(p.inputs, input)
	return configPolicyResult{p.denied}, nil
}

type configPolicyResult struct{ denied bool }

func (p configPolicyResult) Allowed() bool      { return !p.denied }
func (p configPolicyResult) ShouldBlock() bool  { return true }
func (p configPolicyResult) Messages() []string { return []string{"fixture policy denial"} }

var _ = Describe("Conditional config API authority", func() {
	It("shares roles, policy payload and audit identity while returning committed receipts", func() {
		team := createTeam("strict-team")
		grantRole(team, accessor.MemberRole)
		log := lagertest.NewTestLogger("strict-api")
		checker := &configPolicy{}
		configs := configserver.NewServer(log, teamFactory, nil)
		wraps := wrappa.MultiWrappa{
			wrappa.NewPolicyCheckWrappa(log, policychecker.NewApiPolicyChecker(checker)),
			wrappa.NewAPIAuthWrappa(nil, nil, nil, nil),
			wrappa.NewAccessorWrappa(log, realAccessFactory(), auditor.NewAuditor(true, true, true, true, true, true, true, true, true, log), map[string]string{atc.SaveConfig: accessor.OwnerRole}),
		}
		handlers := wraps.Wrap(rata.Handlers{atc.SaveConfig: http.HandlerFunc(configs.SaveConfig), atc.SaveConfigConditional: http.HandlerFunc(configs.SaveConfigConditional)})
		routes := rata.Routes{}
		for _, route := range atc.Routes {
			if handlers[route.Name] != nil {
				routes = append(routes, route)
			}
		}
		router, err := rata.NewRouter(routes, handlers)
		Expect(err).NotTo(HaveOccurred())
		token := validAccessToken()
		call := func(version string) *httptest.ResponseRecorder {
			request := httptest.NewRequest("PUT", "/api/v1/teams/strict-team/pipelines/deploy/config/conditional", bytes.NewBufferString(`{"jobs":[{"name":"unit","plan":[]}]}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", token)
			request.Header.Set(atc.ConfigVersionHeader, version)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			return response
		}
		Expect(call("0").Code).To(Equal(http.StatusForbidden))
		Expect(checker.inputs).To(BeEmpty())
		grantRole(team, accessor.OwnerRole)
		created := call("0")
		Expect(created.Code).To(Equal(http.StatusCreated), created.Body.String())
		version := created.Header().Get(atc.ConfigVersionHeader)
		Expect(version).NotTo(BeEmpty())
		persisted, found, err := team.Pipeline(atc.PipelineRef{Name: "deploy"})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(version).To(Equal(fmt.Sprint(persisted.ConfigVersion())))
		Expect(checker.inputs).To(HaveLen(1))
		Expect(checker.inputs[0].Action).To(Equal(atc.SaveConfig))
		Expect(checker.inputs[0].Team).To(Equal("strict-team"))
		Expect(checker.inputs[0].Pipeline).To(Equal("deploy"))
		Expect(checker.inputs[0].HttpMethod).To(Equal("PUT"))
		Expect(checker.inputs[0].Data).NotTo(BeNil())
		Expect(string(log.Buffer().Contents())).To(ContainSubstring(`"action":"SaveConfig"`))
		Expect(string(log.Buffer().Contents())).NotTo(ContainSubstring(`"action":"SaveConfigConditional"`))
		conflict := call("0")
		Expect(conflict.Code).To(Equal(http.StatusConflict))
		var envelope atc.SaveConfigResponse
		Expect(json.Unmarshal(conflict.Body.Bytes(), &envelope)).To(Succeed())
		Expect(envelope.Code).To(Equal(atc.ConfigVersionConflictCode))
		checker.denied = true
		Expect(call(version).Code).To(Equal(http.StatusForbidden))
		Expect(persisted.Reload()).To(BeTrue())
		Expect(fmt.Sprint(persisted.ConfigVersion())).To(Equal(version))
	})
	It("rejects malformed preconditions before persistence", func() {
		s := configserver.NewServer(logger, teamFactory, nil)
		for _, value := range []string{"", " 1", "+1", "1x", "01", "-1", "2147483648"} {
			req := httptest.NewRequest("PUT", "/", bytes.NewBufferString(`{"jobs":[{"name":"unit","plan":[]}]}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(atc.ConfigVersionHeader, value)
			response := httptest.NewRecorder()
			s.SaveConfigConditional(response, req)
			Expect(response.Code).To(Equal(http.StatusBadRequest), value)
		}
	})
})
