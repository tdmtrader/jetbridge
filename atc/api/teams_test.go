package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func jsonEncode(object any) *bytes.Buffer {
	reqPayload, err := json.Marshal(object)
	Expect(err).NotTo(HaveOccurred())
	return bytes.NewBuffer(reqPayload)
}

var _ = Describe("Teams API", func() {
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
			_, err = realdb.Deps.teamFactory.CreateTeam(atc.Team{Name: "aliens", Auth: listTeamAuth})
			Expect(err).NotTo(HaveOccurred())
			predators, err := realdb.Deps.teamFactory.CreateTeam(atc.Team{Name: "predators", Auth: listTeamAuth})
			Expect(err).NotTo(HaveOccurred())
			grantProfile(avengers, memberProfile, accessor.ViewerRole)
			grantProfile(predators, memberProfile, accessor.ViewerRole)
			useProfile(memberProfile)
		})

		JustBeforeEach(func() {
			var err error
			response, err = client.Get(server.URL + "/api/v1/teams")
			Expect(err).NotTo(HaveOccurred())
		})

		It("returns only the teams the user is authorized for", func() {
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(response.Header.Get("Content-Type")).To(Equal("application/json"))
			var teams []atc.Team
			Expect(json.NewDecoder(response.Body).Decode(&teams)).To(Succeed())
			avengers, found, err := realdb.Deps.teamFactory.FindTeam("avengers")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			predators, found, err := realdb.Deps.teamFactory.FindTeam("predators")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(teams).To(Equal([]atc.Team{
				{ID: avengers.ID(), Name: avengers.Name(), Auth: avengers.Auth()},
				{ID: predators.ID(), Name: predators.Name(), Auth: predators.Auth()},
			}))
			names := []string{teams[0].Name, teams[1].Name}
			Expect(names).To(Equal([]string{"avengers", "predators"}))
			Expect(names).NotTo(ContainElement("aliens"))
			Expect(names).NotTo(ContainElement(atc.DefaultTeamName))
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
			grantProfile(team, memberProfile, accessor.ViewerRole)
			useProfile(memberProfile)
		})

		JustBeforeEach(func() {
			var err error
			response, err = client.Get(server.URL + "/api/v1/teams/a-team")
			Expect(err).NotTo(HaveOccurred())
		})

		It("returns the persisted team", func() {
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(response.Header.Get("Content-Type")).To(Equal("application/json"))
			var returned atc.Team
			Expect(json.NewDecoder(response.Body).Decode(&returned)).To(Succeed())
			Expect(returned).To(Equal(atc.Team{ID: team.ID(), Name: team.Name(), Auth: team.Auth()}))
		})

		Context("when not authenticated", func() {
			BeforeEach(func() { useProfile(anonymousProfile) })
			It("returns 401", func() { Expect(response.StatusCode).To(Equal(http.StatusUnauthorized)) })
		})
		Context("when authenticated but not authorized", func() {
			BeforeEach(func() {
				ungrantedProfile := persistRequestProfile(
					"get-team-ungranted-token",
					"get-team-ungranted-subject",
					"get-team-ungranted-user",
					"Get Team Ungranted",
					"get-team-ungranted",
				)
				useProfile(ungrantedProfile)
			})
			It("returns 403", func() { Expect(response.StatusCode).To(Equal(http.StatusForbidden)) })
		})
	})

	Describe("PUT /api/v1/teams/:team_name", func() {
		var (
			realdb      *realDB
			response    *http.Response
			atcTeam     atc.Team
			teamAuth    atc.TeamAuth
			teamName    string
			cacheSignal *db.NotifySignal
		)

		BeforeEach(func() {
			realdb = useRealDB()
			server = realdb.Serve()
			teamAuth = atc.TeamAuth{"owner": map[string][]string{"groups": {}, "users": {"local:username"}}}
			atcTeam = atc.Team{Auth: teamAuth}
			teamName = "some-team"
			var err error
			cacheSignal, err = realdb.Conn.Bus().ListenSignal(atc.TeamCacheChannel)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				Expect(realdb.Conn.Bus().UnlistenSignal(atc.TeamCacheChannel, cacheSignal)).To(Succeed())
			})
		})
		JustBeforeEach(func() {
			request, err := http.NewRequest("PUT", server.URL+"/api/v1/teams/"+teamName, jsonEncode(atcTeam))
			Expect(err).NotTo(HaveOccurred())
			if cacheSignal != nil {
				for {
					select {
					case <-cacheSignal.C():
						continue
					default:
						goto cacheSignalDrained
					}
				}
			}
		cacheSignalDrained:
			response, err = client.Do(request)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when the requester is an admin and the team is not found", func() {
			BeforeEach(func() { useProfile(adminProfile) })
			It("persists team mutations through PostgreSQL", func() {
				Expect(response.StatusCode).To(Equal(http.StatusCreated))
				created, found, err := realdb.Deps.teamFactory.FindTeam("some-team")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(created.Name()).To(Equal("some-team"))
				Expect(created.Auth()).To(Equal(atc.TeamAuth{"owner": map[string][]string{"groups": {}, "users": {"local:username"}}}))
			})

			Context("when observing cache notification", func() {
				It("notifies the team cache after creation", func() {
					Expect(response.StatusCode).To(Equal(http.StatusCreated))
					Eventually(cacheSignal.C()).Should(Receive())
				})
			})

			Context("when the team name needs a compatibility warning", func() {
				BeforeEach(func() { teamName = "_some-team" })

				It("returns the warning and persisted team", func() {
					Expect(response.StatusCode).To(Equal(http.StatusCreated))
					var body struct {
						Warnings []atc.ConfigWarning `json:"warnings"`
						Team     atc.Team            `json:"team"`
					}
					Expect(json.NewDecoder(response.Body).Decode(&body)).To(Succeed())
					Expect(body.Warnings).To(Equal([]atc.ConfigWarning{{
						Type:    "invalid_identifier",
						Message: "team: '_some-team' is not a valid identifier: must start with a lowercase letter or a number",
					}}))
					Expect(body.Team.ID).To(BeNumerically(">", 0))
					Expect(body.Team.Name).To(Equal("_some-team"))
					Expect(body.Team.Auth).To(Equal(teamAuth))
					persisted, found, err := realdb.Deps.teamFactory.FindTeam("_some-team")
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())
					Expect(persisted.ID()).To(Equal(body.Team.ID))
				})
			})
		})

		Context("when an authorized non-admin updates an existing team", func() {
			var expectedAuth atc.TeamAuth
			oldAuth := atc.TeamAuth{"owner": map[string][]string{"groups": {"old-group"}, "users": {}}}
			BeforeEach(func() {
				team, err := realdb.Deps.teamFactory.CreateTeam(atc.Team{Name: "some-team", Auth: oldAuth})
				Expect(err).NotTo(HaveOccurred())
				grantProfile(team, memberProfile, accessor.OwnerRole)
				team, found, err := realdb.Deps.teamFactory.FindTeam("some-team")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				expectedAuth = team.Auth()
				useProfile(memberProfile)
			})
			It("updates persisted provider auth", func() {
				Expect(response.StatusCode).To(Equal(http.StatusOK))
				updated, found, err := realdb.Deps.teamFactory.FindTeam("some-team")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(updated.Auth()).To(Equal(teamAuth))
				Expect(updated.Auth()).NotTo(Equal(expectedAuth))
			})

			Context("when provider auth is empty", func() {
				BeforeEach(func() { atcTeam = atc.Team{} })

				It("rejects the request without mutating provider auth", func() {
					Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
					team, found, err := realdb.Deps.teamFactory.FindTeam("some-team")
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())
					Expect(team.Auth()).To(Equal(expectedAuth))
				})
			})

			Context("when provider auth is invalid", func() {
				BeforeEach(func() {
					atcTeam = atc.Team{Auth: atc.TeamAuth{"owner": map[string][]string{}}}
				})

				It("rejects the request without mutating provider auth", func() {
					Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
					team, found, err := realdb.Deps.teamFactory.FindTeam("some-team")
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())
					Expect(team.Auth()).To(Equal(expectedAuth))
				})
			})

		})

		Context("when an authorized non-admin targets a missing team", func() {
			BeforeEach(func() {
				useProfile(memberProfile)
			})

			It("refuses to create the team", func() {
				Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				team, found, err := realdb.Deps.teamFactory.FindTeam("some-team")
				Expect(err).NotTo(HaveOccurred())
				Expect(team).To(BeNil())
				Expect(found).To(BeFalse())
				Consistently(cacheSignal.C()).ShouldNot(Receive())
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
			useProfile(adminProfile)
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
		})
		Context("when the team does not exist", func() {
			It("returns 404 Not Found", func() { Expect(response.StatusCode).To(Equal(http.StatusNotFound)) })
		})
		Context("when an authorized non-admin requests deletion", func() {
			BeforeEach(func() {
				team, err := realdb.Deps.teamFactory.CreateTeam(atc.Team{Name: "team"})
				Expect(err).NotTo(HaveOccurred())
				grantProfile(team, memberProfile, accessor.OwnerRole)
				useProfile(memberProfile)
			})

			It("returns 403 without deleting the team", func() {
				Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				team, found, err := realdb.Deps.teamFactory.FindTeam("team")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(team.Name()).To(Equal("team"))
			})
		})
		Context("when deleting the sole admin team", func() {
			var adminTeamID int

			BeforeEach(func() {
				teamName = atc.DefaultTeamName
				mainTeam, found, err := realdb.Deps.teamFactory.FindTeam(atc.DefaultTeamName)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				adminTeamID = mainTeam.ID()

				result, err := realdb.Conn.Exec(`UPDATE teams SET admin = TRUE WHERE id = $1`, adminTeamID)
				Expect(err).NotTo(HaveOccurred())
				rowsAffected, err := result.RowsAffected()
				Expect(err).NotTo(HaveOccurred())
				Expect(rowsAffected).To(Equal(int64(1)))

				mainTeam, found, err = realdb.Deps.teamFactory.FindTeam(atc.DefaultTeamName)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(mainTeam.ID()).To(Equal(adminTeamID))
				Expect(mainTeam.Admin()).To(BeTrue())
				var adminTeamCount int
				Expect(realdb.Conn.QueryRow(`SELECT count(*) FROM teams WHERE admin = TRUE`).Scan(&adminTeamCount)).To(Succeed())
				Expect(adminTeamCount).To(Equal(1))
			})
			It("returns 403 Forbidden and does not delete", func() {
				Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				mainTeam, found, err := realdb.Deps.teamFactory.FindTeam(atc.DefaultTeamName)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(mainTeam.ID()).To(Equal(adminTeamID))
				Expect(mainTeam.Admin()).To(BeTrue())
				var adminTeamCount int
				Expect(realdb.Conn.QueryRow(`SELECT count(*) FROM teams WHERE admin = TRUE`).Scan(&adminTeamCount)).To(Succeed())
				Expect(adminTeamCount).To(Equal(1))
			})
		})
	})

	Describe("PUT /api/v1/teams/:team_name/rename", func() {
		var (
			realdb      *realDB
			response    *http.Response
			requestBody string
			team        db.Team
		)
		BeforeEach(func() {
			realdb = useRealDB()
			server = realdb.Serve()
			requestBody = `{"name":"some-new-name"}`
			var err error
			team, err = realdb.Deps.teamFactory.CreateTeam(atc.Team{Name: "a-team", Auth: atc.TeamAuth{"owner": map[string][]string{"groups": {}, "users": {"local:username"}}}})
			Expect(err).NotTo(HaveOccurred())
			grantProfile(team, memberProfile, accessor.OwnerRole)
			useProfile(memberProfile)
		})
		JustBeforeEach(func() {
			req, err := http.NewRequest("PUT", server.URL+"/api/v1/teams/a-team/rename", bytes.NewBufferString(requestBody))
			Expect(err).NotTo(HaveOccurred())
			response, err = client.Do(req)
			Expect(err).NotTo(HaveOccurred())
		})
		Context("when not authenticated", func() {
			BeforeEach(func() {
				useProfile(anonymousProfile)
			})

			It("returns 401 without renaming the team", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
				original, found, err := realdb.Deps.teamFactory.FindTeam("a-team")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(original.Name()).To(Equal("a-team"))
				renamed, found, err := realdb.Deps.teamFactory.FindTeam("some-new-name")
				Expect(err).NotTo(HaveOccurred())
				Expect(renamed).To(BeNil())
				Expect(found).To(BeFalse())
			})
		})
		Context("when authenticated but not authorized", func() {
			BeforeEach(func() {
				ungrantedProfile := persistRequestProfile(
					"rename-team-ungranted-token",
					"rename-team-ungranted-subject",
					"rename-team-ungranted-user",
					"Rename Team Ungranted",
					"rename-team-ungranted",
				)
				useProfile(ungrantedProfile)
			})

			It("returns 403 without renaming the team", func() {
				Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				original, found, err := realdb.Deps.teamFactory.FindTeam("a-team")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(original.Name()).To(Equal("a-team"))
				renamed, found, err := realdb.Deps.teamFactory.FindTeam("some-new-name")
				Expect(err).NotTo(HaveOccurred())
				Expect(renamed).To(BeNil())
				Expect(found).To(BeFalse())
			})
		})
		Context("when the team exists", func() {
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
		})
		Context("when the name is invalid", func() {
			BeforeEach(func() {
				requestBody = `{"name":"_some-new-name"}`
			})
			It("returns a warning", func() {
				Expect(response.StatusCode).To(Equal(http.StatusOK))
				Expect(io.ReadAll(response.Body)).To(MatchJSON(`{"warnings":[{"type":"invalid_identifier","message":"team: '_some-new-name' is not a valid identifier: must start with a lowercase letter or a number"}]}`))
			})
		})
		Context("when the name is empty", func() {
			BeforeEach(func() {
				requestBody = `{"name":""}`
			})
			It("returns the validation error as JSON", func() {
				Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
				Expect(io.ReadAll(response.Body)).To(MatchJSON(`{"errors":["team: identifier cannot be an empty string"]}`))
			})
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
			useProfile(memberProfile)
		})
		JustBeforeEach(func() {
			var err error
			response, err = client.Get(server.URL + "/api/v1/teams/some-team/builds" + queryParams)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when not authenticated", func() {
			var (
				team           db.Team
				protectedBuild db.Build
			)

			BeforeEach(func() {
				useProfile(anonymousProfile)
				var err error
				team, err = realdb.Deps.teamFactory.CreateTeam(atc.Team{Name: "some-team"})
				Expect(err).NotTo(HaveOccurred())
				protectedBuild, err = team.CreateOneOffBuild()
				Expect(err).NotTo(HaveOccurred())
			})

			It("returns 401 without changing the protected build", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
				builds, _, err := team.Builds(db.Page{Limit: 100})
				Expect(err).NotTo(HaveOccurred())
				Expect(builds).To(HaveLen(1))
				Expect(builds[0].ID()).To(Equal(protectedBuild.ID()))
			})
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
				Expect(response.Header.Get("Content-Type")).To(Equal("application/json"))
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
					Expect(actual.APIURL).To(Equal(fmt.Sprintf("/api/v1/builds/%d", expected.ID())))
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
		})
		Context("when observing page translation", func() {
			var (
				builds [4]db.Build
			)

			BeforeEach(func() {
				team, err := realdb.Deps.teamFactory.CreateTeam(atc.Team{Name: "some-team"})
				Expect(err).NotTo(HaveOccurred())
				for i := range builds {
					builds[i], err = team.CreateOneOffBuild()
					Expect(err).NotTo(HaveOccurred())
				}

				queryParams = fmt.Sprintf("?from=%d&to=%d&limit=8", builds[1].ID(), builds[2].ID())
			})
			It("returns the requested build range", func() {
				Expect(response.StatusCode).To(Equal(http.StatusOK))

				var returned []atc.Build
				Expect(json.NewDecoder(response.Body).Decode(&returned)).To(Succeed())
				Expect(returned).To(HaveLen(2))
				Expect([]int{returned[0].ID, returned[1].ID}).To(Equal([]int{builds[1].ID(), builds[2].ID()}))
			})
			Context("with no page parameters", func() {
				BeforeEach(func() { queryParams = "" })
				It("uses the default limit", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))

					var returned []atc.Build
					Expect(json.NewDecoder(response.Body).Decode(&returned)).To(Succeed())
					Expect(returned).To(HaveLen(4))
					Expect([]int{returned[0].ID, returned[1].ID, returned[2].ID, returned[3].ID}).To(Equal([]int{
						builds[3].ID(), builds[2].ID(), builds[1].ID(), builds[0].ID(),
					}))
				})
			})
			Context("when newer and older pages are available", func() {
				BeforeEach(func() {
					queryParams = fmt.Sprintf("?from=%d&limit=2", builds[1].ID())
				})

				It("returns Link headers per RFC 5988", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))

					var returned []atc.Build
					Expect(json.NewDecoder(response.Body).Decode(&returned)).To(Succeed())
					Expect(returned).To(HaveLen(2))
					Expect([]int{returned[0].ID, returned[1].ID}).To(Equal([]int{builds[2].ID(), builds[1].ID()}))
					Expect(response.Header.Values("Link")).To(ConsistOf(
						fmt.Sprintf(`<%s/api/v1/teams/some-team/builds?from=%d&limit=2>; rel="previous"`, externalURL, builds[3].ID()),
						fmt.Sprintf(`<%s/api/v1/teams/some-team/builds?to=%d&limit=2>; rel="next"`, externalURL, builds[0].ID()),
					))
				})
			})
		})
	})
})
