package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/concourse/concourse/atc"

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
				fakeAccess.IsAuthenticatedReturns(true)

				fakeAccess.UserInfoReturns(atc.UserInfo{
					Sub:      "some-sub",
					Name:     "some-name",
					UserId:   "some-user-id",
					UserName: "some-user-name",
					Email:    "some@email.com",
					IsAdmin:  true,
					IsSystem: false,
					Teams: map[string][]string{
						"some-team":       {"owner"},
						"some-other-team": {"viewer"},
					},
					Connector:     "some-connector",
					DisplayUserId: "some-user-id",
				})
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
							"display_user_id": "some-user-id"
						}`))
			})
		})

		Context("not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
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
			server = realdb.Serve()
		})

		JustBeforeEach(func() {
			req, err := http.NewRequest("GET", server.URL+"/api/v1/users", nil)
			Expect(err).NotTo(HaveOccurred())

			req.URL.RawQuery = query.Encode()

			response, err = client.Do(req)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authenticated", func() {

			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(true)
			})

			Context("not an admin", func() {

				It("returns 403", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
				})

			})

			Context("being an admin", func() {

				BeforeEach(func() {
					fakeAccess.IsAdminReturns(true)
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

				Context("failing to retrieve users", func() {
					BeforeEach(func() {
						doomed := postgresRunner.OpenConn()
						Expect(doomed.Close()).To(Succeed())

						deps := realdb.Deps
						deps.userFactory = db.NewUserFactory(doomed)
						server = newAPIServer(deps)
						DeferCleanup(server.Close)
					})

					It("fails", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
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
				fakeAccess.IsAuthenticatedReturns(false)
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
			server = realdb.Serve()

			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAdminReturns(true)
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
