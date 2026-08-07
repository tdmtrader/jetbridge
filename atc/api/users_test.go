package api_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"

	"github.com/concourse/concourse/atc"

	"github.com/concourse/concourse/atc/db/dbfakes"
	. "github.com/concourse/concourse/atc/testhelpers"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Users API", func() {

	var (
		response *http.Response
		query    url.Values

		realdb *realDB
		server *httptest.Server
	)

	BeforeEach(func() {
		realdb = useRealDB()
		server = realdb.Serve()
	})

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
						// Narrowly scoped: only the user factory fails, so the 500
						// is attributable to this handler's read. Everything else
						// in the handler stays real, which is what the deps struct
						// makes cheap.
						failing := new(dbfakes.FakeUserFactory)
						failing.GetAllUsersReturns(nil, errors.New("no db connection"))

						deps := realdb.Deps
						deps.userFactory = failing
						server = newAPIServer(deps)
						DeferCleanup(server.Close)
					})

					It("fails", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})

				Context("having only the seeded platform user", func() {
					It("returns just that user", func() {
						body, err := io.ReadAll(response.Body)
						Expect(err).NotTo(HaveOccurred())

						// Migration 1773106022 seeds a 'platform' user with
						// last_login at the epoch. The old spec asserted `[]` here,
						// which no real deployment ever returns.
						var listed []struct {
							Username  string `json:"username"`
							Connector string `json:"connector"`
							LastLogin int64  `json:"last_login"`
						}
						Expect(json.Unmarshal(body, &listed)).To(Succeed())
						Expect(listed).To(HaveLen(1))
						Expect(listed[0].Username).To(Equal("platform"))
						Expect(listed[0].Connector).To(Equal("local"))
						Expect(listed[0].LastLogin).To(BeZero())
					})
				})

				Context("having users", func() {
					BeforeEach(func() {
						Expect(realdb.Deps.userFactory.CreateOrUpdateUser("bob", "github", "sub")).To(Succeed())
					})

					It("returns all users logged in since table creation", func() {
						body, err := io.ReadAll(response.Body)
						Expect(err).NotTo(HaveOccurred())

						var listed []struct {
							Username  string `json:"username"`
							Connector string `json:"connector"`
						}
						Expect(json.Unmarshal(body, &listed)).To(Succeed())

						var names []string
						for _, u := range listed {
							names = append(names, u.Username+"/"+u.Connector)
						}
						Expect(names).To(ConsistOf("platform/local", "bob/github"))
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
		var date string
		BeforeEach(func() {
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
			BeforeEach(func() {
				// The seeded platform user logged in at the epoch, so a cutoff
				// after 1970 excludes it and leaves only the user created here --
				// which is the filter the endpoint exists to apply. The previous
				// date, 1969-12-30, is before the epoch and excludes nothing.
				date = "2000-01-01"
				Expect(realdb.Deps.userFactory.CreateOrUpdateUser("bob", "github", "sub")).To(Succeed())
			})
			It("returns users", func() {
				body, err := io.ReadAll(response.Body)
				Expect(err).NotTo(HaveOccurred())

				var listed []struct {
					Username string `json:"username"`
				}
				Expect(json.Unmarshal(body, &listed)).To(Succeed())

				var names []string
				for _, u := range listed {
					names = append(names, u.Username)
				}
				Expect(names).To(ConsistOf("bob"))
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

		Context("without a since parameter", func() {
			BeforeEach(func() {
				date = ""
			})
			It("lists every user", func() {
				body, err := io.ReadAll(response.Body)
				Expect(err).NotTo(HaveOccurred())

				// get_since.go:49-50 falls through to GetAllUsers when since is
				// absent, so this returns everyone -- here, the seeded platform
				// user. This Context was previously called "no users logged in
				// since the given date" and asserted `[]`, which was true only
				// because an unstubbed fake returns nil.
				var listed []struct {
					Username string `json:"username"`
				}
				Expect(json.Unmarshal(body, &listed)).To(Succeed())

				var names []string
				for _, u := range listed {
					names = append(names, u.Username)
				}
				Expect(names).To(ConsistOf("platform"))
			})
		})
	})
})
