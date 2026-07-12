package wrappa_test

import (
	"net/http"
	"net/http/httptest"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/agent/api/principals"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/accessor/accessorfakes"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/auditor/auditorfakes"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/wrappa"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/tedsuo/rata"
)

var _ = Describe("APIAuthWrappa", func() {
	var (
		fakeCheckPipelineAccessHandlerFactory   auth.CheckPipelineAccessHandlerFactory
		fakeCheckBuildReadAccessHandlerFactory  auth.CheckBuildReadAccessHandlerFactory
		fakeCheckBuildWriteAccessHandlerFactory auth.CheckBuildWriteAccessHandlerFactory
		fakeCheckWorkerTeamAccessHandlerFactory auth.CheckWorkerTeamAccessHandlerFactory
		fakeBuildFactory                        *dbfakes.FakeBuildFactory
	)

	BeforeEach(func() {
		fakeTeamFactory := new(dbfakes.FakeTeamFactory)
		workerFactory := new(dbfakes.FakeWorkerFactory)
		fakeBuildFactory = new(dbfakes.FakeBuildFactory)
		fakeCheckPipelineAccessHandlerFactory = auth.NewCheckPipelineAccessHandlerFactory(
			fakeTeamFactory,
		)

		fakeCheckBuildReadAccessHandlerFactory = auth.NewCheckBuildReadAccessHandlerFactory(fakeBuildFactory)
		fakeCheckBuildWriteAccessHandlerFactory = auth.NewCheckBuildWriteAccessHandlerFactory(fakeBuildFactory)
		fakeCheckWorkerTeamAccessHandlerFactory = auth.NewCheckWorkerTeamAccessHandlerFactory(workerFactory)
	})

	Describe("Wrap", func() {
		It("handles each route", func() {
			inputHandlers := rata.Handlers{}

			for _, route := range atc.Routes {
				inputHandlers[route.Name] = &stupidHandler{}
			}
			Expect(func() {
				wrappa.NewAPIAuthWrappa(
					fakeCheckPipelineAccessHandlerFactory,
					fakeCheckBuildReadAccessHandlerFactory,
					fakeCheckBuildWriteAccessHandlerFactory,
					fakeCheckWorkerTeamAccessHandlerFactory,
					auth.NewCheckAgentPrincipalHandlerFactory(principals.NewVerifier(principals.NewMemoryStore())),
				).Wrap(inputHandlers)
			}).NotTo(Panic())
		})

		// 00-shared-contracts.md §4.1/§4.2: SubmitAgentRunMetrics is the
		// strict principal(metrics:write) tier (no legacy bypass);
		// ListAgentRunMetrics is authorized-viewer against the main team.
		// These specs pin the tier each route is wrapped with — the
		// no-panic loop above only proves the switch is exhaustive.
		Describe("agent run metrics route tiers", func() {
			var (
				store        *principals.MemoryStore
				wrapped      rata.Handlers
				delegateHit  bool
				fakeAccessor *accessorfakes.FakeAccessFactory
				fakeaccess   *accessorfakes.FakeAccess
			)

			BeforeEach(func() {
				delegateHit = false
				fakeAccessor = new(accessorfakes.FakeAccessFactory)
				fakeaccess = new(accessorfakes.FakeAccess)
				fakeAccessor.CreateReturns(fakeaccess, nil)

				store = principals.NewMemoryStore()

				delegate := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					delegateHit = true
					w.WriteHeader(http.StatusOK)
				})

				wrapped = wrappa.NewAPIAuthWrappa(
					fakeCheckPipelineAccessHandlerFactory,
					fakeCheckBuildReadAccessHandlerFactory,
					fakeCheckBuildWriteAccessHandlerFactory,
					fakeCheckWorkerTeamAccessHandlerFactory,
					auth.NewCheckAgentPrincipalHandlerFactory(principals.NewVerifier(store)),
				).Wrap(rata.Handlers{
					atc.SubmitAgentRunMetrics: delegate,
					atc.ListAgentRunMetrics:   delegate,
				})
			})

			serve := func(routeName string, authorization string) *http.Response {
				server := httptest.NewServer(accessor.NewHandler(
					lagertest.NewTestLogger("api-auth-wrappa"),
					"some-action",
					wrapped[routeName],
					fakeAccessor,
					new(auditorfakes.FakeAuditor),
					map[string]string{},
				))
				defer server.Close()

				req, err := http.NewRequest("GET", server.URL, nil)
				Expect(err).NotTo(HaveOccurred())
				if authorization != "" {
					req.Header.Set("Authorization", authorization)
				}
				resp, err := http.DefaultClient.Do(req)
				Expect(err).NotTo(HaveOccurred())
				return resp
			}

			Describe("SubmitAgentRunMetrics", func() {
				It("admits a principal token carrying metrics:write", func() {
					_, token, err := store.Create(principals.CreateSpec{
						Name: "metrics-writer", Scopes: []string{principals.ScopeMetricsWrite},
					})
					Expect(err).NotTo(HaveOccurred())

					resp := serve(atc.SubmitAgentRunMetrics, "Bearer "+token)
					Expect(resp.StatusCode).To(Equal(http.StatusOK))
					Expect(delegateHit).To(BeTrue())
				})

				It("401s a principal token lacking metrics:write", func() {
					_, token, err := store.Create(principals.CreateSpec{
						Name: "cost-writer", Scopes: []string{principals.ScopeCostsWrite},
					})
					Expect(err).NotTo(HaveOccurred())

					resp := serve(atc.SubmitAgentRunMetrics, "Bearer "+token)
					Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
					Expect(delegateHit).To(BeFalse())
				})

				It("401s tokenless unauthenticated requests (no legacy bypass)", func() {
					fakeaccess.IsAuthenticatedReturns(false)

					resp := serve(atc.SubmitAgentRunMetrics, "")
					Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
					Expect(delegateHit).To(BeFalse())
				})

				It("403s authenticated non-admin users without a principal token", func() {
					fakeaccess.IsAuthenticatedReturns(true)
					fakeaccess.IsAdminReturns(false)

					resp := serve(atc.SubmitAgentRunMetrics, "")
					Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
					Expect(delegateHit).To(BeFalse())
				})
			})

			Describe("ListAgentRunMetrics", func() {
				It("401s unauthenticated requests", func() {
					fakeaccess.IsAuthenticatedReturns(false)

					resp := serve(atc.ListAgentRunMetrics, "")
					Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
					Expect(delegateHit).To(BeFalse())
				})

				It("403s users not authorized on the main team", func() {
					fakeaccess.IsAuthenticatedReturns(true)
					fakeaccess.IsAuthorizedReturns(false)

					resp := serve(atc.ListAgentRunMetrics, "")
					Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
					Expect(delegateHit).To(BeFalse())
				})

				It("admits main-team-authorized users", func() {
					fakeaccess.IsAuthenticatedReturns(true)
					fakeaccess.IsAuthorizedReturns(true)

					resp := serve(atc.ListAgentRunMetrics, "")
					Expect(resp.StatusCode).To(Equal(http.StatusOK))
					Expect(delegateHit).To(BeTrue())
					Expect(fakeaccess.IsAuthorizedCallCount()).To(Equal(1))
					Expect(fakeaccess.IsAuthorizedArgsForCall(0)).To(Equal(atc.DefaultTeamName))
				})
			})
		})
	})
})
