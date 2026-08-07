package api_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func jsonEncode(object any) *bytes.Buffer {
	reqPayload, err := json.Marshal(object)
	Expect(err).NotTo(HaveOccurred())
	return bytes.NewBuffer(reqPayload)
}

var _ = Describe("Teams API", func() {
	var fakeTeam *dbfakes.FakeTeam

	BeforeEach(func() {
		fakeTeam = new(dbfakes.FakeTeam)
	})

	Describe("GET /api/v1/teams", func() {
		var (
			realdb   *realDB
			response *http.Response
		)

		BeforeEach(func() {
			realdb = useRealDB()
			server = realdb.Serve()
			listTeamAuth := atc.TeamAuth{"owner": map[string][]string{"groups": {}, "users": {"local:username"}}}
			avengers, err := realdb.Deps.teamFactory.CreateTeam(atc.Team{Name: "avengers", Auth: listTeamAuth})
			Expect(err).NotTo(HaveOccurred())
			aliens, err := realdb.Deps.teamFactory.CreateTeam(atc.Team{Name: "aliens", Auth: listTeamAuth})
			Expect(err).NotTo(HaveOccurred())
			predators, err := realdb.Deps.teamFactory.CreateTeam(atc.Team{Name: "predators", Auth: listTeamAuth})
			Expect(err).NotTo(HaveOccurred())
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedCalls(func(name string) bool { return name == avengers.Name() || name == predators.Name() })
			_ = aliens
		})

		JustBeforeEach(func() {
			var err error
			response, err = client.Get(server.URL + "/api/v1/teams")
			Expect(err).NotTo(HaveOccurred())
		})

		It("returns only the teams the user is authorized for", func() {
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			var teams []atc.Team
			Expect(json.NewDecoder(response.Body).Decode(&teams)).To(Succeed())
			listTeamAuth := atc.TeamAuth{"owner": map[string][]string{"groups": {}, "users": {"local:username"}}}
			avengers, found, err := realdb.Deps.teamFactory.FindTeam("avengers")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			predators, found, err := realdb.Deps.teamFactory.FindTeam("predators")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(teams).To(Equal([]atc.Team{{ID: avengers.ID(), Name: avengers.Name(), Auth: listTeamAuth}, {ID: predators.ID(), Name: predators.Name(), Auth: listTeamAuth}}))
		})
		It("returns 200 for an authorized team list", func() { Expect(response.StatusCode).To(Equal(http.StatusOK)) })
		It("returns application/json for an authorized team list", func() { Expect(response.Header.Get("Content-Type")).To(Equal("application/json")) })

		Context("when listing teams fails", func() {
			BeforeEach(func() {
				doomed := postgresRunner.OpenConn()
				Expect(doomed.Close()).To(Succeed())
				deps := realdb.Deps
				deps.teamFactory = db.NewTeamFactory(doomed, realdb.LockFactory)
				server = newAPIServer(deps)
				DeferCleanup(server.Close)
			})

			It("returns 500", func() { Expect(response.StatusCode).To(Equal(http.StatusInternalServerError)) })
		})
	})

	Describe("GET /api/v1/teams/:team_name", func() {
		var (
			realdb   *realDB
			team     db.Team
			response *http.Response
		)

		BeforeEach(func() {
			realdb = useRealDB()
			server = realdb.Serve()
			var err error
			team, err = realdb.Deps.teamFactory.CreateTeam(atc.Team{Name: "a-team", Auth: atc.TeamAuth{"owner": map[string][]string{"groups": {}, "users": {"local:username"}}}})
			Expect(err).NotTo(HaveOccurred())
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAdminReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
		})

		JustBeforeEach(func() {
			var err error
			response, err = client.Get(server.URL + "/api/v1/teams/a-team")
			Expect(err).NotTo(HaveOccurred())
		})

		It("returns the persisted team", func() {
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			var returned atc.Team
			Expect(json.NewDecoder(response.Body).Decode(&returned)).To(Succeed())
			Expect(returned).To(Equal(atc.Team{ID: team.ID(), Name: team.Name(), Auth: team.Auth()}))
		})
		It("returns 200 ok", func() { Expect(response.StatusCode).To(Equal(http.StatusOK)) })
		It("returns application/json", func() { Expect(response.Header.Get("Content-Type")).To(Equal("application/json")) })
		It("returns a team JSON", func() {
			var returned atc.Team
			Expect(json.NewDecoder(response.Body).Decode(&returned)).To(Succeed())
			Expect(returned.Auth).To(Equal(team.Auth()))
		})

		Context("when not authenticated", func() {
			BeforeEach(func() { fakeAccess.IsAuthenticatedReturns(false) })
			It("returns 401", func() { Expect(response.StatusCode).To(Equal(http.StatusUnauthorized)) })
		})
		Context("when authenticated but not authorized", func() {
			BeforeEach(func() { fakeAccess.IsAdminReturns(false); fakeAccess.IsAuthorizedReturns(false) })
			It("returns 403", func() { Expect(response.StatusCode).To(Equal(http.StatusForbidden)) })
		})
	})

	Describe("PUT /api/v1/teams/:team_name", func() {
		var (
			realdb   *realDB
			response *http.Response
			atcTeam  atc.Team
			teamAuth atc.TeamAuth
		)

		BeforeEach(func() {
			realdb = useRealDB()
			server = realdb.Serve()
			teamAuth = atc.TeamAuth{"owner": map[string][]string{"groups": {}, "users": {"local:username"}}}
			atcTeam = atc.Team{Auth: teamAuth}
			fakeAccess.IsAuthenticatedReturns(true)
		})
		JustBeforeEach(func() {
			request, err := http.NewRequest("PUT", server.URL+"/api/v1/teams/some-team", jsonEncode(atcTeam))
			Expect(err).NotTo(HaveOccurred())
			response, err = client.Do(request)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when the requester is an admin and the team is not found", func() {
			BeforeEach(func() { fakeAccess.IsAuthorizedReturns(true); fakeAccess.IsAdminReturns(true) })
			It("persists team mutations through PostgreSQL", func() {
				Expect(response.StatusCode).To(Equal(http.StatusCreated))
				created, found, err := realdb.Deps.teamFactory.FindTeam("some-team")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(created.Name()).To(Equal("some-team"))
				Expect(created.Auth()).To(Equal(atc.TeamAuth{"owner": map[string][]string{"groups": {}, "users": {"local:username"}}}))
			})
			It("creates the team", func() { Expect(response.StatusCode).To(Equal(http.StatusCreated)) })
			It("persists the requested team name", func() {
				created, found, err := realdb.Deps.teamFactory.FindTeam("some-team")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(created.Name()).To(Equal("some-team"))
			})
			It("persists the requested team auth", func() {
				created, found, err := realdb.Deps.teamFactory.FindTeam("some-team")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(created.Auth()).To(Equal(teamAuth))
			})

			Context("when creating the team fails", func() {
				BeforeEach(func() {
					dbTeamFactory.FindTeamReturns(nil, false, nil)
					dbTeamFactory.CreateTeamReturns(nil, errors.New("it is never going to happen"))
					deps := realdb.Deps
					deps.teamFactory = dbTeamFactory
					server = newAPIServer(deps)
					DeferCleanup(server.Close)
				})
				It("returns 500", func() { Expect(response.StatusCode).To(Equal(http.StatusInternalServerError)) })
			})
		})

		Context("when the team already exists", func() {
			oldAuth := atc.TeamAuth{"owner": map[string][]string{"groups": {"old-group"}, "users": {}}}
			BeforeEach(func() {
				_, err := realdb.Deps.teamFactory.CreateTeam(atc.Team{Name: "some-team", Auth: oldAuth})
				Expect(err).NotTo(HaveOccurred())
				fakeAccess.IsAuthorizedReturns(true)
				fakeAccess.IsAdminReturns(true)
			})
			It("updates persisted provider auth", func() {
				Expect(response.StatusCode).To(Equal(http.StatusOK))
				updated, found, err := realdb.Deps.teamFactory.FindTeam("some-team")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(updated.Auth()).To(Equal(teamAuth))
				Expect(updated.Auth()).NotTo(Equal(oldAuth))
			})
			It("updates provider auth", func() { Expect(response.StatusCode).To(Equal(http.StatusOK)) })
			It("replaces old provider auth", func() {
				updated, found, err := realdb.Deps.teamFactory.FindTeam("some-team")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(updated.Auth()).NotTo(Equal(oldAuth))
			})

			Context("when updating provider auth fails", func() {
				BeforeEach(func() {
					// Retained fault seam: Team.UpdateProviderAuth must fail after FindTeam
					// succeeds; a closed TeamFactory fails the lookup before this method.
					fakeTeam.UpdateProviderAuthReturns(errors.New("stop trying to make fetch happen"))
					dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
					deps := realdb.Deps
					deps.teamFactory = dbTeamFactory
					server = newAPIServer(deps)
					DeferCleanup(server.Close)
				})
				It("returns 500", func() { Expect(response.StatusCode).To(Equal(http.StatusInternalServerError)) })
			})
		})
	})

	Describe("DELETE /api/v1/teams/:team_name", func() {
		var (
			realdb   *realDB
			response *http.Response
			teamName string
		)
		BeforeEach(func() {
			realdb = useRealDB()
			server = realdb.Serve()
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAdminReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			teamName = "team"
		})
		JustBeforeEach(func() {
			req, err := http.NewRequest("DELETE", server.URL+"/api/v1/teams/"+teamName, nil)
			Expect(err).NotTo(HaveOccurred())
			response, err = client.Do(req)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when the team exists", func() {
			BeforeEach(func() {
				_, err := realdb.Deps.teamFactory.CreateTeam(atc.Team{Name: "team", Auth: atc.TeamAuth{"owner": map[string][]string{"groups": {}, "users": {"local:admin"}}}})
				Expect(err).NotTo(HaveOccurred())
			})
			It("deletes the persisted team", func() {
				Expect(response.StatusCode).To(Equal(http.StatusNoContent))
				team, found, err := realdb.Deps.teamFactory.FindTeam("team")
				Expect(err).NotTo(HaveOccurred())
				Expect(team).To(BeNil())
				Expect(found).To(BeFalse())
			})
			It("returns 204 No Content", func() { Expect(response.StatusCode).To(Equal(http.StatusNoContent)) })
			It("receives the correct team name", func() {
				_, found, err := realdb.Deps.teamFactory.FindTeam("team")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeFalse())
			})
			It("deletes the team from the DB", func() {
				team, found, err := realdb.Deps.teamFactory.FindTeam("team")
				Expect(err).NotTo(HaveOccurred())
				Expect(team).To(BeNil())
				Expect(found).To(BeFalse())
			})
		})
		Context("when the team does not exist", func() {
			It("returns 404", func() { Expect(response.StatusCode).To(Equal(http.StatusNotFound)) })
			It("returns 404 Not Found", func() { Expect(response.StatusCode).To(Equal(http.StatusNotFound)) })
		})
		Context("when deleting the sole admin team", func() {
			BeforeEach(func() {
				teamName = atc.DefaultTeamName
				fakeTeam.NameReturns(atc.DefaultTeamName)
				fakeTeam.AdminReturns(true)
				dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
				dbTeamFactory.GetTeamsReturns([]db.Team{fakeTeam}, nil)
				deps := realdb.Deps
				deps.teamFactory = dbTeamFactory
				server = newAPIServer(deps)
				DeferCleanup(server.Close)
			})
			It("returns 403", func() { Expect(response.StatusCode).To(Equal(http.StatusForbidden)) })
			It("returns 403 Forbidden and backs off", func() { Expect(response.StatusCode).To(Equal(http.StatusForbidden)) })
		})
		Context("when deleting the team fails", func() {
			BeforeEach(func() {
				// Retained fault seam: Team.Delete must fail after FindTeam succeeds;
				// a closed TeamFactory fails the lookup before this method.
				fakeTeam.DeleteReturns(errors.New("disaster"))
				dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
				deps := realdb.Deps
				deps.teamFactory = dbTeamFactory
				server = newAPIServer(deps)
				DeferCleanup(server.Close)
			})
			It("returns 500", func() { Expect(response.StatusCode).To(Equal(http.StatusInternalServerError)) })
			It("returns 500 Internal Server Error", func() { Expect(response.StatusCode).To(Equal(http.StatusInternalServerError)) })
		})
	})

	Describe("PUT /api/v1/teams/:team_name/rename", func() {
		var (
			realdb      *realDB
			response    *http.Response
			requestBody string
		)
		BeforeEach(func() {
			realdb = useRealDB()
			server = realdb.Serve()
			requestBody = `{"name":"some-new-name"}`
			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
		})
		JustBeforeEach(func() {
			req, err := http.NewRequest("PUT", server.URL+"/api/v1/teams/a-team/rename", bytes.NewBufferString(requestBody))
			Expect(err).NotTo(HaveOccurred())
			response, err = client.Do(req)
			Expect(err).NotTo(HaveOccurred())
		})
		Context("when the team exists", func() {
			BeforeEach(func() {
				_, err := realdb.Deps.teamFactory.CreateTeam(atc.Team{Name: "a-team", Auth: atc.TeamAuth{"owner": map[string][]string{"groups": {}, "users": {"local:username"}}}})
				Expect(err).NotTo(HaveOccurred())
			})
			It("renames the persisted team", func() {
				Expect(response.StatusCode).To(Equal(http.StatusOK))
				old, found, err := realdb.Deps.teamFactory.FindTeam("a-team")
				Expect(err).NotTo(HaveOccurred())
				Expect(old).To(BeNil())
				Expect(found).To(BeFalse())
				renamed, found, err := realdb.Deps.teamFactory.FindTeam("some-new-name")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(renamed.Name()).To(Equal("some-new-name"))
			})
			It("renames the team to the name provided", func() {
				_, found, err := realdb.Deps.teamFactory.FindTeam("some-new-name")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
			})
			It("returns 200", func() { Expect(response.StatusCode).To(Equal(http.StatusOK)) })
		})
		Context("when the name is invalid", func() {
			BeforeEach(func() {
				requestBody = `{"name":"_some-new-name"}`
				_, err := realdb.Deps.teamFactory.CreateTeam(atc.Team{Name: "a-team"})
				Expect(err).NotTo(HaveOccurred())
			})
			It("returns a warning", func() {
				Expect(response.StatusCode).To(Equal(http.StatusOK))
				Expect(io.ReadAll(response.Body)).To(MatchJSON(`{"warnings":[{"type":"invalid_identifier","message":"team: '_some-new-name' is not a valid identifier: must start with a lowercase letter or a number"}]}`))
			})
			It("returns a warning in the response body", func() { Expect(io.ReadAll(response.Body)).To(ContainSubstring("invalid_identifier")) })
		})
		Context("when the name is empty", func() {
			BeforeEach(func() {
				requestBody = `{"name":""}`
				_, err := realdb.Deps.teamFactory.CreateTeam(atc.Team{Name: "a-team"})
				Expect(err).NotTo(HaveOccurred())
			})
			It("returns 400", func() { Expect(response.StatusCode).To(Equal(http.StatusBadRequest)) })
		})
		Context("when renaming fails after lookup", func() {
			BeforeEach(func() {
				// Retained fault seam: Team.Rename must fail after FindTeam succeeds;
				// a closed TeamFactory fails the lookup before this method.
				fakeTeam.RenameReturns(errors.New("disaster"))
				dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
				deps := realdb.Deps
				deps.teamFactory = dbTeamFactory
				server = newAPIServer(deps)
				DeferCleanup(server.Close)
			})
			It("returns 500", func() { Expect(response.StatusCode).To(Equal(http.StatusInternalServerError)) })
		})
	})

	Describe("GET /api/v1/teams/:team_name/builds", func() {
		var (
			realdb      *realDB
			response    *http.Response
			queryParams string
		)
		BeforeEach(func() {
			realdb = useRealDB()
			server = realdb.Serve()
			fakeAccess.IsAuthenticatedReturns(true)
		})
		JustBeforeEach(func() {
			var err error
			response, err = client.Get(server.URL + "/api/v1/teams/some-team/builds" + queryParams)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when builds exist", func() {
			var startedBuild, succeededBuild db.Build
			BeforeEach(func() {
				team, err := realdb.Deps.teamFactory.CreateTeam(atc.Team{Name: "some-team"})
				Expect(err).NotTo(HaveOccurred())
				pipeline := realdb.SavePipeline(team, "some-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}})
				job, found, err := pipeline.Job("some-job")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				startedBuild, err = job.CreateBuild("api-test")
				Expect(err).NotTo(HaveOccurred())
				started, err := startedBuild.Start(atc.Plan{})
				Expect(err).NotTo(HaveOccurred())
				Expect(started).To(BeTrue())
				succeededBuild, err = job.CreateBuild("api-test")
				Expect(err).NotTo(HaveOccurred())
				started, err = succeededBuild.Start(atc.Plan{})
				Expect(err).NotTo(HaveOccurred())
				Expect(started).To(BeTrue())
				Expect(succeededBuild.Finish(db.BuildStatusSucceeded)).To(Succeed())
				_, err = startedBuild.Reload()
				Expect(err).NotTo(HaveOccurred())
				_, err = succeededBuild.Reload()
				Expect(err).NotTo(HaveOccurred())
			})
			It("returns dynamic build fields from PostgreSQL", func() {
				Expect(response.StatusCode).To(Equal(http.StatusOK))
				var builds []atc.Build
				Expect(json.NewDecoder(response.Body).Decode(&builds)).To(Succeed())
				Expect(builds).To(HaveLen(2))
				byID := map[int]atc.Build{}
				for _, build := range builds {
					byID[build.ID] = build
				}
				for _, expected := range []db.Build{startedBuild, succeededBuild} {
					actual := byID[expected.ID()]
					Expect(actual.ID).To(Equal(expected.ID()))
					Expect(actual.Name).To(Equal(expected.Name()))
					Expect(actual.JobName).To(Equal(expected.JobName()))
					Expect(actual.PipelineName).To(Equal(expected.PipelineName()))
					Expect(actual.TeamName).To(Equal(expected.TeamName()))
					Expect(actual.Status).To(Equal(atc.BuildStatus(expected.Status())))
					Expect(actual.StartTime).To(Equal(expected.StartTime().Unix()))
					if expected.EndTime().IsZero() {
						Expect(actual.EndTime).To(BeZero())
					} else {
						Expect(actual.EndTime).To(Equal(expected.EndTime().Unix()))
					}
				}
				Expect(startedBuild.EndTime()).To(BeZero())
				Expect(succeededBuild.EndTime()).NotTo(BeZero())
			})
			It("returns 200 OK", func() { Expect(response.StatusCode).To(Equal(http.StatusOK)) })
			It("returns Content-Type application/json", func() { Expect(response.Header.Get("Content-Type")).To(Equal("application/json")) })
			It("returns a started build", func() {
				var builds []atc.Build
				Expect(json.NewDecoder(response.Body).Decode(&builds)).To(Succeed())
				Expect(builds).To(ContainElement(HaveField("Status", atc.StatusStarted)))
			})
			It("returns a succeeded build", func() {
				var builds []atc.Build
				Expect(json.NewDecoder(response.Body).Decode(&builds)).To(Succeed())
				Expect(builds).To(ContainElement(HaveField("Status", atc.StatusSucceeded)))
			})
		})
		Context("when observing page translation", func() {
			BeforeEach(func() {
				dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
				deps := realdb.Deps
				deps.teamFactory = dbTeamFactory
				server = newAPIServer(deps)
				DeferCleanup(server.Close)
				queryParams = "?from=2&to=3&limit=8"
			})
			It("passes page arguments through", func() {
				Expect(fakeTeam.BuildsCallCount()).To(Equal(1))
				Expect(fakeTeam.BuildsArgsForCall(0)).To(Equal(db.Page{From: db.NewIntPtr(2), To: db.NewIntPtr(3), Limit: 8}))
			})
			Context("with no page parameters", func() {
				BeforeEach(func() { queryParams = "" })
				It("uses the default limit", func() { Expect(fakeTeam.BuildsArgsForCall(0)).To(Equal(db.Page{Limit: 100})) })
			})
		})
		Context("when listing builds fails after lookup", func() {
			BeforeEach(func() {
				// Retained fault seam: Team.Builds must fail after FindTeam succeeds;
				// a closed TeamFactory fails the lookup before this method.
				fakeTeam.BuildsReturns(nil, db.Pagination{}, errors.New("oh no!"))
				dbTeamFactory.FindTeamReturns(fakeTeam, true, nil)
				deps := realdb.Deps
				deps.teamFactory = dbTeamFactory
				server = newAPIServer(deps)
				DeferCleanup(server.Close)
			})
			It("returns 404", func() { Expect(response.StatusCode).To(Equal(http.StatusNotFound)) })
		})
	})
})
