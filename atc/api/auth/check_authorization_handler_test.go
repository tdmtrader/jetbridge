package auth_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/auditor/auditorfakes"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CheckAuthorizationHandler", func() {
	var (
		team db.Team

		server *httptest.Server
		client *http.Client

		// set by a Context to give the request a token; "" leaves it anonymous
		authorization string
	)

	simpleHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buffer := bytes.NewBufferString("simple ")

		io.Copy(w, buffer)
		io.Copy(w, r.Body)
	})

	BeforeEach(func() {
		authorization = ""
		team = createTeam("some-team")

		innerHandler := auth.CheckAuthorizationHandler(
			simpleHandler,
			auth.UnauthorizedRejector{},
		)

		// A real accessor resolves the role from the action, and every role
		// fails the blank one, so the action has to be a route that has one.
		server = httptest.NewServer(accessor.NewHandler(
			logger,
			atc.ListPipelines,
			innerHandler,
			realAccessFactory(),
			new(auditorfakes.FakeAuditor),
			map[string]string{},
		))

		client = &http.Client{
			Transport: &http.Transport{},
		}
	})

	Context("when a request is made", func() {
		var request *http.Request
		var response *http.Response

		BeforeEach(func() {
			var err error
			request, err = http.NewRequest("GET", server.URL+"/teams/some-team/pipelines", bytes.NewBufferString("hello"))
			Expect(err).NotTo(HaveOccurred())
			urlValues := url.Values{":team_name": []string{"some-team"}}
			request.URL.RawQuery = urlValues.Encode()
		})

		JustBeforeEach(func() {
			var err error

			if authorization != "" {
				request.Header.Set("Authorization", authorization)
			}

			response, err = client.Do(request)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when the request is authenticated", func() {
			BeforeEach(func() {
				authorization = validAccessToken()
			})

			Context("when the bearer token's team matches the request's team", func() {
				BeforeEach(func() {
					grantRole(team, accessor.ViewerRole)
				})

				It("returns 200", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})

				It("proxies to the handler", func() {
					responseBody, err := io.ReadAll(response.Body)
					Expect(err).NotTo(HaveOccurred())
					Expect(string(responseBody)).To(Equal("simple hello"))
				})
			})

			Context("when the bearer token's team is set to something other than the request's team", func() {
				BeforeEach(func() {
					grantRole(createTeam("some-other-team"), accessor.ViewerRole)
				})

				It("returns 403", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
					responseBody, err := io.ReadAll(response.Body)
					Expect(err).NotTo(HaveOccurred())
					Expect(string(responseBody)).To(Equal("forbidden"))
				})
			})
		})

		Context("when the request is not authenticated", func() {
			BeforeEach(func() {
				grantRole(team, accessor.ViewerRole)
			})

			It("returns 401", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
				responseBody, err := io.ReadAll(response.Body)
				Expect(err).NotTo(HaveOccurred())
				Expect(string(responseBody)).To(Equal("not authorized"))
			})
		})
	})
})
