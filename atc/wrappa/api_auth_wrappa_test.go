package wrappa_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/accessor/accessorfakes"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/auditor/auditorfakes"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/wrappa"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/tedsuo/rata"
)

var _ = Describe("APIAuthWrappa", func() {
	var (
		checkPipelineAccessHandlerFactory   auth.CheckPipelineAccessHandlerFactory
		checkBuildReadAccessHandlerFactory  auth.CheckBuildReadAccessHandlerFactory
		checkBuildWriteAccessHandlerFactory auth.CheckBuildWriteAccessHandlerFactory
		checkWorkerTeamAccessHandlerFactory auth.CheckWorkerTeamAccessHandlerFactory
	)

	BeforeEach(func() {
		checkPipelineAccessHandlerFactory = auth.NewCheckPipelineAccessHandlerFactory(
			teamFactory,
		)

		checkBuildReadAccessHandlerFactory = auth.NewCheckBuildReadAccessHandlerFactory(buildFactory)
		checkBuildWriteAccessHandlerFactory = auth.NewCheckBuildWriteAccessHandlerFactory(buildFactory)
		checkWorkerTeamAccessHandlerFactory = auth.NewCheckWorkerTeamAccessHandlerFactory(workerFactory)
	})

	Describe("Wrap", func() {
		It("handles each route", func() {
			inputHandlers := rata.Handlers{}

			for _, route := range atc.Routes {
				inputHandlers[route.Name] = &stupidHandler{}
			}
			Expect(func() {
				wrappa.NewAPIAuthWrappa(
					checkPipelineAccessHandlerFactory,
					checkBuildReadAccessHandlerFactory,
					checkBuildWriteAccessHandlerFactory,
					checkWorkerTeamAccessHandlerFactory,
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
					checkPipelineAccessHandlerFactory,
					checkBuildReadAccessHandlerFactory,
					checkBuildWriteAccessHandlerFactory,
					checkWorkerTeamAccessHandlerFactory,
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
				BeforeEach(func() {
					delegate := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						delegateHit = true
						w.WriteHeader(http.StatusOK)
					})

					wrapped = wrappa.NewAPIAuthWrappa(
						checkPipelineAccessHandlerFactory,
						checkBuildReadAccessHandlerFactory,
						checkBuildWriteAccessHandlerFactory,
						checkWorkerTeamAccessHandlerFactory,
					).Wrap(rata.Handlers{
						atc.GetBuildAgentReviews:     delegate,
						atc.ListBuildAgentRunMetrics: delegate,
					})
				})

				createBuild := func(jobPublic bool) db.Build {
					team, err := teamFactory.CreateTeam(atc.Team{Name: "some-team"})
					Expect(err).NotTo(HaveOccurred())

					pipeline, _, err := team.SavePipeline(
						atc.PipelineRef{Name: "some-pipeline"},
						atc.Config{Jobs: atc.JobConfigs{{Name: "some-job", Public: jobPublic}}},
						db.ConfigVersion(0),
						false,
					)
					Expect(err).NotTo(HaveOccurred())
					Expect(pipeline.Expose()).To(Succeed())

					job, found, err := pipeline.Job("some-job")
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())

					build, err := job.CreateBuild("some-user")
					Expect(err).NotTo(HaveOccurred())
					return build
				}

				serveBuild := func(routeName string, buildID int) *http.Response {
					server := httptest.NewServer(accessor.NewHandler(
						lagertest.NewTestLogger("api-auth-wrappa"),
						"some-action",
						wrapped[routeName],
						fakeAccessor,
						new(auditorfakes.FakeAuditor),
						map[string]string{},
					))
					defer server.Close()

					req, err := http.NewRequest("GET", fmt.Sprintf("%s?:build_id=%d", server.URL, buildID), nil)
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
							build := createBuild(false)

							resp := serveBuild(routeName, build.ID())
							Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
							Expect(delegateHit).To(BeFalse())
						})

						It("admits anonymous requests when the pipeline and job are both public", func() {
							fakeaccess.IsAuthenticatedReturns(false)
							build := createBuild(true)

							resp := serveBuild(routeName, build.ID())
							Expect(resp.StatusCode).To(Equal(http.StatusOK))
							Expect(delegateHit).To(BeTrue())
						})

						It("admits team members regardless of job visibility", func() {
							fakeaccess.IsAuthenticatedReturns(true)
							fakeaccess.IsAuthorizedReturns(true)
							build := createBuild(false)

							resp := serveBuild(routeName, build.ID())
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
						checkPipelineAccessHandlerFactory,
						checkBuildReadAccessHandlerFactory,
						checkBuildWriteAccessHandlerFactory,
						checkWorkerTeamAccessHandlerFactory,
					).Wrap(rata.Handlers{
						atc.GetAgentWorkflowStats: delegate,
						atc.UpdateAgentWorkflow:   delegate,
					})
				})

				Describe("UpdateAgentWorkflow", func() {
					It("rejects a syntactically plausible retired bearer token", func() {
						fakeaccess.IsAuthenticatedReturns(false)

						resp := serve(atc.UpdateAgentWorkflow, "Bearer cap1.7.s3cret")
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

		Describe("human agent routes", func() {
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
					checkPipelineAccessHandlerFactory,
					checkBuildReadAccessHandlerFactory,
					checkBuildWriteAccessHandlerFactory,
					checkWorkerTeamAccessHandlerFactory,
				).Wrap(rata.Handlers{
					atc.CreateAgentTicket:     delegate,
					atc.TransitionAgentTicket: delegate,
					atc.GetAgentTicket:        delegate,
					atc.SubmitAgentFeedback:   delegate,
				})
			})

			serve := func(routeName, authorization string) *http.Response {
				server := httptest.NewServer(accessor.NewHandler(
					lagertest.NewTestLogger("api-auth-wrappa"),
					routeName,
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

			for _, route := range []string{atc.CreateAgentTicket, atc.TransitionAgentTicket, atc.GetAgentTicket} {
				route := route
				Describe(route, func() {
					It("401s anonymous requests", func() {
						fakeaccess.IsAuthenticatedReturns(false)

						resp := serve(route, "")
						Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
						Expect(delegateHit).To(BeFalse())
					})

					It("does not grant authority to a cap1-looking bearer", func() {
						fakeaccess.IsAuthenticatedReturns(false)

						resp := serve(route, "Bearer cap1.7.s3cret")
						Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
						Expect(delegateHit).To(BeFalse())
					})

					It("403s an authenticated user outside main", func() {
						fakeaccess.IsAuthenticatedReturns(true)
						fakeaccess.IsAuthorizedReturns(false)

						resp := serve(route, "")
						Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
						Expect(delegateHit).To(BeFalse())
					})

					It("admits an authorized main-team human", func() {
						fakeaccess.IsAuthenticatedReturns(true)
						fakeaccess.IsAuthorizedReturns(true)

						resp := serve(route, "")
						Expect(resp.StatusCode).To(Equal(http.StatusOK))
						Expect(delegateHit).To(BeTrue())
					})
				})
			}

			Describe("SubmitAgentFeedback", func() {
				serveFeedback := func() *httptest.ResponseRecorder {
					handler := accessor.NewHandler(
						lagertest.NewTestLogger("api-auth-wrappa"),
						atc.SubmitAgentFeedback,
						wrapped[atc.SubmitAgentFeedback],
						fakeAccessor,
						new(auditorfakes.FakeAuditor),
						map[string]string{},
					)

					req := httptest.NewRequest(http.MethodPost, "/?:team_name=research", nil)
					resp := httptest.NewRecorder()
					handler.ServeHTTP(resp, req)
					return resp
				}

				It("401s anonymous requests", func() {
					fakeaccess.IsAuthenticatedReturns(false)
					Expect(serveFeedback().Code).To(Equal(http.StatusUnauthorized))
					Expect(delegateHit).To(BeFalse())
				})

				It("403s a user outside the requested team", func() {
					fakeaccess.IsAuthenticatedReturns(true)
					fakeaccess.IsAuthorizedReturns(false)
					Expect(serveFeedback().Code).To(Equal(http.StatusForbidden))
					Expect(delegateHit).To(BeFalse())
					Expect(fakeaccess.IsAuthorizedArgsForCall(0)).To(Equal("research"))
				})

				It("admits a member of the requested team with the member role", func() {
					fakeaccess.IsAuthenticatedReturns(true)
					fakeaccess.IsAuthorizedReturns(true)
					Expect(serveFeedback().Code).To(Equal(http.StatusOK))
					Expect(delegateHit).To(BeTrue())
					Expect(fakeaccess.IsAuthorizedArgsForCall(0)).To(Equal("research"))
					_, role := fakeAccessor.CreateArgsForCall(0)
					Expect(role).To(Equal(accessor.MemberRole))
				})
			})
		})

		// Dispatcher runtime-control tier pinning (dispatcher-runtime-control
		// wire contract): GetAgentDispatcher is merely authenticated (ANY
		// signed-in user may READ status); SetAgentDispatcher is admin-only
		// The no-panic loop only proves the switch is exhaustive — these specs
		// pin the tier, including the REJECTS-non-admin-PUT case.
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
					checkPipelineAccessHandlerFactory,
					checkBuildReadAccessHandlerFactory,
					checkBuildWriteAccessHandlerFactory,
					checkWorkerTeamAccessHandlerFactory,
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
