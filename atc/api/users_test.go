package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/db"
	. "github.com/concourse/concourse/atc/testhelpers"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Users API", func() {

	var (
		response *http.Response
		query    url.Values
	)

	Context("GET /api/v1/user", func() {

		JustBeforeEach(func() {
			req, err := http.NewRequest("GET", server.URL+"/api/v1/user", nil)
			Expect(err).NotTo(HaveOccurred())

			req.URL.RawQuery = query.Encode()

			response, err = client.Do(req)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authenticated", func() {

			BeforeEach(func() {
				rawClaims, err := json.Marshal(map[string]any{
					"sub":                "some-sub",
					"aud":                []string{"api-test"},
					"exp":                time.Now().Add(time.Hour).Unix(),
					"name":               "some-name",
					"preferred_username": "some-user-name",
					"email":              "some@email.com",
					"federated_claims": map[string]any{
						"connector_id": "some-connector",
						"user_id":      "some-user-id",
					},
				})
				Expect(err).NotTo(HaveOccurred())

				var claims db.Claims
				Expect(json.Unmarshal(rawClaims, &claims)).To(Succeed())
				Expect(apiDB.AccessTokenFactory.CreateAccessToken("some-user-token", claims)).To(Succeed())

				profile := requestProfile{
					authorization: "Bearer some-user-token",
					connector:     "some-connector",
					userID:        "some-user-id",
				}

				someTeam, err := apiDB.Deps.teamFactory.CreateTeam(atc.Team{Name: "some-team"})
				Expect(err).NotTo(HaveOccurred())
				otherTeam, err := apiDB.Deps.teamFactory.CreateTeam(atc.Team{Name: "some-other-team"})
				Expect(err).NotTo(HaveOccurred())
				grantProfile(someTeam, profile, accessor.OwnerRole)
				grantProfile(otherTeam, profile, accessor.ViewerRole)

				result, err := apiDB.Conn.Exec(`UPDATE teams SET admin = TRUE WHERE id = $1`, someTeam.ID())
				Expect(err).NotTo(HaveOccurred())
				rowsAffected, err := result.RowsAffected()
				Expect(err).NotTo(HaveOccurred())
				Expect(rowsAffected).To(Equal(int64(1)))

				someTeam, found, err := apiDB.Deps.teamFactory.FindTeam(someTeam.Name())
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(someTeam.Admin()).To(BeTrue())

				useProfile(profile)
			})

			It("succeeds", func() {
				Expect(response.StatusCode).To(Equal(http.StatusOK))
			})

			It("returns Content-Type 'application/json'", func() {
				Expect(response.Header.Get("Content-Type")).To(Equal("application/json"))
			})

			It("returns the current user", func() {
				body, err := io.ReadAll(response.Body)
				Expect(err).NotTo(HaveOccurred())

				Expect(body).To(MatchJSON(`{
							"sub": "some-sub",
							"name": "some-name",
							"user_id": "some-user-id",
							"user_name": "some-user-name",
							"email": "some@email.com",
							"is_admin": true,
							"is_system": false,
							"teams": {
							  "some-team": ["owner"],
							  "some-other-team": ["viewer"]
							},
							"connector": "some-connector",
							"display_user_id": "some-user-name"
						}`))
			})
		})

		Context("not authenticated", func() {
			BeforeEach(func() {
				useProfile(anonymousProfile)
			})

			It("returns 401", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})

	})

	Context("GET /api/v1/users", func() {
		var realdb *realDB

		BeforeEach(func() {
			realdb = useRealDB()
		})

		JustBeforeEach(func() {
			req, err := http.NewRequest("GET", server.URL+"/api/v1/users", nil)
			Expect(err).NotTo(HaveOccurred())

			req.URL.RawQuery = query.Encode()

			response, err = client.Do(req)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authenticated", func() {
			Context("not an admin", func() {
				BeforeEach(func() {
					useProfile(memberProfile)
				})

				It("returns 403", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				})

			})

			Context("being an admin", func() {

				BeforeEach(func() {
					useProfile(adminProfile)
				})

				It("succeeds", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})

				It("returns Content-Type 'application/json'", func() {
					expectedHeaderEntries := map[string]string{
						"Content-Type": "application/json",
					}
					Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
				})

				Context("having no users", func() {
					It("returns an empty array", func() {
						body, err := io.ReadAll(response.Body)
						Expect(err).NotTo(HaveOccurred())

						Expect(body).To(MatchJSON(`[]`))
					})
				})

				Context("having users", func() {
					var loggedInAt time.Time
					BeforeEach(func() {
						loggedInAt = time.Now()
						Expect(realdb.Deps.userFactory.CreateOrUpdateUser("bob", "github", "sub")).To(Succeed())
					})

					It("returns all users logged in since table creation", func() {
						var users []atc.User
						Expect(json.NewDecoder(response.Body).Decode(&users)).To(Succeed())

						stored, err := realdb.Deps.userFactory.GetAllUsers()
						Expect(err).NotTo(HaveOccurred())
						Expect(stored).To(HaveLen(1))

						Expect(users).To(HaveLen(1))
						Expect(users[0].ID).To(Equal(stored[0].ID()))
						Expect(users[0].Username).To(Equal("bob"))
						Expect(users[0].Connector).To(Equal("github"))
						Expect(time.Unix(users[0].LastLogin, 0)).To(BeTemporally("~", loggedInAt, time.Minute))
					})

				})

			})

		})

		Context("not authenticated", func() {

			BeforeEach(func() {
				useProfile(anonymousProfile)
			})

			It("returns 401", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})

		})

	})

	Context("GET /api/v1/users?since=", func() {
		var (
			realdb *realDB
			date   string
		)
		BeforeEach(func() {
			realdb = useRealDB()
			useProfile(adminProfile)
		})

		JustBeforeEach(func() {
			req, err := http.NewRequest("GET", server.URL+"/api/v1/users?since="+date, nil)
			Expect(err).NotTo(HaveOccurred())

			response, err = client.Do(req)
			Expect(err).NotTo(HaveOccurred())
		})
		Context("with correct date format", func() {
			var loggedInAt time.Time
			BeforeEach(func() {
				date = "1969-12-30"

				loggedInAt = time.Now()
				Expect(realdb.Deps.userFactory.CreateOrUpdateUser("bob", "github", "sub")).To(Succeed())
			})
			It("returns users", func() {
				var users []atc.User
				Expect(json.NewDecoder(response.Body).Decode(&users)).To(Succeed())

				stored, err := realdb.Deps.userFactory.GetAllUsers()
				Expect(err).NotTo(HaveOccurred())
				Expect(stored).To(HaveLen(1))

				Expect(users).To(HaveLen(1))
				Expect(users[0].ID).To(Equal(stored[0].ID()))
				Expect(users[0].Username).To(Equal("bob"))
				Expect(users[0].Connector).To(Equal("github"))
				Expect(time.Unix(users[0].LastLogin, 0)).To(BeTemporally("~", loggedInAt, time.Minute))
			})
		})

		Context("with a date later than any login", func() {
			BeforeEach(func() {
				// Two days, not one: the handler parses the date as UTC
				// midnight, which for a positive local offset can still land
				// before a login that happened moments ago.
				date = time.Now().UTC().AddDate(0, 0, 2).Format("2006-01-02")

				Expect(realdb.Deps.userFactory.CreateOrUpdateUser("bob", "github", "sub")).To(Succeed())
			})
			It("returns an empty array", func() {
				body, err := io.ReadAll(response.Body)
				Expect(err).NotTo(HaveOccurred())

				Expect(body).To(MatchJSON(`[]`))
			})
		})

		Context("with incorrect date format", func() {
			BeforeEach(func() {
				date = "1969-14-30"
			})
			It("returns an error message", func() {
				body, err := io.ReadAll(response.Body)
				Expect(err).NotTo(HaveOccurred())

				Expect(body).To(MatchJSON(`{"error": "wrong date format (yyyy-mm-dd)"}`))
			})

			It("returns a HTTP 400", func() {
				Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
			})
		})

		Context("no users logged in since the given date", func() {
			BeforeEach(func() {
				date = ""
			})
			It("returns an empty array", func() {
				body, err := io.ReadAll(response.Body)
				Expect(err).NotTo(HaveOccurred())

				Expect(body).To(MatchJSON(`[]`))
			})
		})
	})
})
