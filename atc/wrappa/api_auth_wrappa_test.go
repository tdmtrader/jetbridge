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

		// 00-shared-contracts.md §4.2: ListAgentWorkflowRunMetrics is
		// authorized-viewer against the main team. These specs pin the tier each
		// route is wrapped with — the no-panic loop above only proves the switch
		// is exhaustive. (The strict principal(metrics:write) submit tier is
		// gone with POST /api/v1/agent/metrics: metrics are written in-process.)
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
					atc.ListAgentWorkflowRunMetrics: delegate,
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

			// GetBuildAgentReviews and ListBuildAgentRunMetrics carry
			// content-bearing run output (review findings, run
			// Summary/Results), so they ride the same tier as
			// BuildEvents: pipeline AND job public, or team access —
			// not the pipeline-only AnyJobHandler tier.
			Describe("build-scoped agent routes", func() {
				var (
					fakeJob *dbfakes.FakeJob
				)

				BeforeEach(func() {
					build := new(dbfakes.FakeBuildForAPI)
					fakePipeline := new(dbfakes.FakePipeline)
					fakeJob = new(dbfakes.FakeJob)

					build.PipelineIDReturns(41)
					build.PipelineReturns(fakePipeline, true, nil)
					build.AllAssociatedTeamNamesReturns([]string{"some-team"})
					build.JobIDReturns(43)
					build.JobNameReturns("some-job")
					fakePipeline.PublicReturns(true)
					fakePipeline.JobReturns(fakeJob, true, nil)
					fakeBuildFactory.BuildForAPIReturns(build, true, nil)

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
						atc.GetBuildAgentReviews:     delegate,
						atc.ListBuildAgentRunMetrics: delegate,
					})
				})

				serveBuild := func(routeName string) *http.Response {
					server := httptest.NewServer(accessor.NewHandler(
						lagertest.NewTestLogger("api-auth-wrappa"),
						"some-action",
						wrapped[routeName],
						fakeAccessor,
						new(auditorfakes.FakeAuditor),
						map[string]string{},
					))
					defer server.Close()

					req, err := http.NewRequest("GET", server.URL+"?:build_id=55", nil)
					Expect(err).NotTo(HaveOccurred())
					resp, err := http.DefaultClient.Do(req)
					Expect(err).NotTo(HaveOccurred())
					return resp
				}

				for _, routeName := range []string{atc.GetBuildAgentReviews, atc.ListBuildAgentRunMetrics} {
					routeName := routeName

					Describe(routeName, func() {
						It("401s anonymous requests when the job is private, even on a public pipeline", func() {
							fakeaccess.IsAuthenticatedReturns(false)
							fakeJob.PublicReturns(false)

							resp := serveBuild(routeName)
							Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
							Expect(delegateHit).To(BeFalse())
						})

						It("admits anonymous requests when the pipeline and job are both public", func() {
							fakeaccess.IsAuthenticatedReturns(false)
							fakeJob.PublicReturns(true)

							resp := serveBuild(routeName)
							Expect(resp.StatusCode).To(Equal(http.StatusOK))
							Expect(delegateHit).To(BeTrue())
						})

						It("admits team members regardless of job visibility", func() {
							fakeaccess.IsAuthenticatedReturns(true)
							fakeaccess.IsAuthorizedReturns(true)
							fakeJob.PublicReturns(false)

							resp := serveBuild(routeName)
							Expect(resp.StatusCode).To(Equal(http.StatusOK))
							Expect(delegateHit).To(BeTrue())
						})
					})
				}
			})

			// S-6 workflow lifecycle: GetAgentWorkflowStats is a plain
			// authorized-viewer read; UpdateAgentWorkflow (annotate/deprecate)
			// is human-only with NO principal path — a bare tickets:read
			// principal token must be rejected, mirroring the deprecated verbs
			// on tickets.
			Describe("workflow lifecycle route tiers", func() {
				BeforeEach(func() {
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
						atc.GetAgentWorkflowStats: delegate,
						atc.UpdateAgentWorkflow:   delegate,
					})
				})

				Describe("UpdateAgentWorkflow", func() {
					It("REJECTS a bare tickets:read agent-principal token (human-only, no principal path)", func() {
						_, token, err := store.Create(principals.CreateSpec{
							Name: "ticket-reader", Scopes: []string{principals.ScopeTicketsRead},
						})
						Expect(err).NotTo(HaveOccurred())
						fakeaccess.IsAuthenticatedReturns(false)

						resp := serve(atc.UpdateAgentWorkflow, "Bearer "+token)
						Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
						Expect(delegateHit).To(BeFalse())
					})

					It("admits an authorized main-team member", func() {
						fakeaccess.IsAuthenticatedReturns(true)
						fakeaccess.IsAuthorizedReturns(true)

						resp := serve(atc.UpdateAgentWorkflow, "")
						Expect(resp.StatusCode).To(Equal(http.StatusOK))
						Expect(delegateHit).To(BeTrue())
						Expect(fakeaccess.IsAuthorizedArgsForCall(0)).To(Equal(atc.DefaultTeamName))
					})
				})

				Describe("GetAgentWorkflowStats", func() {
					It("401s unauthenticated requests", func() {
						fakeaccess.IsAuthenticatedReturns(false)

						resp := serve(atc.GetAgentWorkflowStats, "")
						Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
						Expect(delegateHit).To(BeFalse())
					})

					It("admits main-team-authorized users", func() {
						fakeaccess.IsAuthenticatedReturns(true)
						fakeaccess.IsAuthorizedReturns(true)

						resp := serve(atc.GetAgentWorkflowStats, "")
						Expect(resp.StatusCode).To(Equal(http.StatusOK))
						Expect(delegateHit).To(BeTrue())
					})
				})
			})

			Describe("ListAgentWorkflowRunMetrics", func() {
				It("401s unauthenticated requests", func() {
					fakeaccess.IsAuthenticatedReturns(false)

					resp := serve(atc.ListAgentWorkflowRunMetrics, "")
					Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
					Expect(delegateHit).To(BeFalse())
				})

				It("403s users not authorized on the main team", func() {
					fakeaccess.IsAuthenticatedReturns(true)
					fakeaccess.IsAuthorizedReturns(false)

					resp := serve(atc.ListAgentWorkflowRunMetrics, "")
					Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
					Expect(delegateHit).To(BeFalse())
				})

				It("admits main-team-authorized users", func() {
					fakeaccess.IsAuthenticatedReturns(true)
					fakeaccess.IsAuthorizedReturns(true)

					resp := serve(atc.ListAgentWorkflowRunMetrics, "")
					Expect(resp.StatusCode).To(Equal(http.StatusOK))
					Expect(delegateHit).To(BeTrue())
					Expect(fakeaccess.IsAuthorizedCallCount()).To(Equal(1))
					Expect(fakeaccess.IsAuthorizedArgsForCall(0)).To(Equal(atc.DefaultTeamName))
				})
			})
		})

		// The pure principal(<scope>) publishing tier is gone with POST
		// /api/v1/agent/{reviews,costs,metrics} — reviews, ledger rows and
		// metrics are written in-process. What remains of the principal tier is
		// the COMBINED ticket tier: an agent principal carrying the ticket scope
		// OR an authorized main-team human.
		Describe("agent ticket route tiers", func() {
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
					atc.CreateAgentTicket:     delegate,
					atc.TransitionAgentTicket: delegate,
					atc.GetAgentTicket:        delegate,
				})
			})

			serve := func(routeName, authorization string) *http.Response {
				server := httptest.NewServer(accessor.NewHandler(
					lagertest.NewTestLogger("api-auth-wrappa"),
					"some-action",
					wrapped[routeName],
					fakeAccessor,
					new(auditorfakes.FakeAuditor),
					map[string]string{},
				))
				defer server.Close()

				req, err := http.NewRequest(http.MethodPost, server.URL, nil)
				Expect(err).NotTo(HaveOccurred())
				if authorization != "" {
					req.Header.Set("Authorization", authorization)
				}
				resp, err := http.DefaultClient.Do(req)
				Expect(err).NotTo(HaveOccurred())
				return resp
			}

			for _, tc := range []struct {
				route      string
				scope      string
				otherScope string
			}{
				{atc.CreateAgentTicket, principals.ScopeTicketsWrite, principals.ScopeTicketsRead},
				{atc.TransitionAgentTicket, principals.ScopeTicketsWrite, principals.ScopeTicketsRead},
				{atc.GetAgentTicket, principals.ScopeTicketsRead, principals.ScopeTicketsWrite},
			} {
				tc := tc
				Describe(tc.route, func() {
					It("admits a principal carrying the required scope", func() {
						_, token, err := store.Create(principals.CreateSpec{Name: "writer", Scopes: []string{tc.scope}})
						Expect(err).NotTo(HaveOccurred())

						resp := serve(tc.route, "Bearer "+token)
						Expect(resp.StatusCode).To(Equal(http.StatusOK))
						Expect(delegateHit).To(BeTrue())
					})

					It("401s anonymous requests without a principal token", func() {
						fakeaccess.IsAuthenticatedReturns(false)

						resp := serve(tc.route, "")
						Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
						Expect(delegateHit).To(BeFalse())
					})

					It("rejects a principal carrying only the other ticket scope", func() {
						_, token, err := store.Create(principals.CreateSpec{Name: "other-writer", Scopes: []string{tc.otherScope}})
						Expect(err).NotTo(HaveOccurred())

						resp := serve(tc.route, "Bearer "+token)
						Expect(resp.StatusCode).ToNot(Equal(http.StatusOK))
						Expect(delegateHit).To(BeFalse())
					})

					It("admits an authorized main-team human with no principal token", func() {
						fakeaccess.IsAuthenticatedReturns(true)
						fakeaccess.IsAuthorizedReturns(true)

						resp := serve(tc.route, "")
						Expect(resp.StatusCode).To(Equal(http.StatusOK))
						Expect(delegateHit).To(BeTrue())
					})
				})
			}
		})

		// Dispatcher runtime-control tier pinning (dispatcher-runtime-control
		// wire contract): GetAgentDispatcher is merely authenticated (ANY
		// signed-in user may READ status); SetAgentDispatcher is admin-only
		// (same CheckAdminHandler tier as CreateAgentPrincipal). The no-panic
		// loop only proves the switch is exhaustive — these specs pin the tier,
		// including the REJECTS-non-admin-PUT case.
		Describe("dispatcher route tiers", func() {
			var (
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

				delegate := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					delegateHit = true
					w.WriteHeader(http.StatusOK)
				})

				wrapped = wrappa.NewAPIAuthWrappa(
					fakeCheckPipelineAccessHandlerFactory,
					fakeCheckBuildReadAccessHandlerFactory,
					fakeCheckBuildWriteAccessHandlerFactory,
					fakeCheckWorkerTeamAccessHandlerFactory,
					auth.NewCheckAgentPrincipalHandlerFactory(principals.NewVerifier(principals.NewMemoryStore())),
				).Wrap(rata.Handlers{
					atc.GetAgentDispatcher: delegate,
					atc.SetAgentDispatcher: delegate,
				})
			})

			serveDispatcher := func(routeName string) *http.Response {
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
				resp, err := http.DefaultClient.Do(req)
				Expect(err).NotTo(HaveOccurred())
				return resp
			}

			Describe("GetAgentDispatcher", func() {
				It("401s unauthenticated requests", func() {
					fakeaccess.IsAuthenticatedReturns(false)

					resp := serveDispatcher(atc.GetAgentDispatcher)
					Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
					Expect(delegateHit).To(BeFalse())
				})

				It("admits ANY authenticated user, admin or not", func() {
					fakeaccess.IsAuthenticatedReturns(true)
					fakeaccess.IsAdminReturns(false)

					resp := serveDispatcher(atc.GetAgentDispatcher)
					Expect(resp.StatusCode).To(Equal(http.StatusOK))
					Expect(delegateHit).To(BeTrue())
				})
			})

			Describe("SetAgentDispatcher", func() {
				It("401s unauthenticated requests", func() {
					fakeaccess.IsAuthenticatedReturns(false)

					resp := serveDispatcher(atc.SetAgentDispatcher)
					Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
					Expect(delegateHit).To(BeFalse())
				})

				It("REJECTS an authenticated non-admin (403)", func() {
					fakeaccess.IsAuthenticatedReturns(true)
					fakeaccess.IsAdminReturns(false)

					resp := serveDispatcher(atc.SetAgentDispatcher)
					Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
					Expect(delegateHit).To(BeFalse())
				})

				It("admits admins", func() {
					fakeaccess.IsAuthenticatedReturns(true)
					fakeaccess.IsAdminReturns(true)

					resp := serveDispatcher(atc.SetAgentDispatcher)
					Expect(resp.StatusCode).To(Equal(http.StatusOK))
					Expect(delegateHit).To(BeTrue())
				})
			})
		})
	})
})
