package auth_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/auth"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CheckAdminHandler", func() {
	var (
		server *httptest.Server
		client *http.Client

		// set by a Context to give the request a token; "" leaves it anonymous
		authorization string
	)

	simpleHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buffer := bytes.NewBufferString("simple ")

		_, err := io.Copy(w, buffer)
		Expect(err).ToNot(HaveOccurred())
		_, err = io.Copy(w, r.Body)
		Expect(err).ToNot(HaveOccurred())
	})

	BeforeEach(func() {
		authorization = ""

		innerHandler := auth.CheckAdminHandler(
			simpleHandler,
			auth.UnauthorizedRejector{},
		)

		// ListActiveUsersSince is a route this handler guards, and like every
		// admin route it carries no default role, so it resolves to the blank
		// role the real handler hands admin routes.
		server = httptest.NewServer(accessor.NewHandler(
			logger,
			atc.ListActiveUsersSince,
			innerHandler,
			realAccessFactory(),
			realAuditor(),
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

			request, err = http.NewRequest("GET", server.URL, bytes.NewBufferString("hello"))
			Expect(err).NotTo(HaveOccurred())
		})

		JustBeforeEach(func() {
			var err error

			if authorization != "" {
				request.Header.Set("Authorization", authorization)
			}

			response, err = client.Do(request)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when the validator returns true", func() {
			BeforeEach(func() {
				authorization = validAccessToken()
			})

			Context("when is admin", func() {
				BeforeEach(func() {
					makeAdmin(createTeam("some-team"))
				})

				It("returns 200 OK", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})

				It("proxies to the handler", func() {
					responseBody, err := io.ReadAll(response.Body)
					Expect(err).NotTo(HaveOccurred())
					Expect(string(responseBody)).To(Equal("simple hello"))
				})
			})

			Context("when is not admin", func() {
				BeforeEach(func() {
					// Owning a team is not enough: the team itself must be an
					// administrator team.
					grantRole(createTeam("some-team"), accessor.OwnerRole)
				})

				It("returns 403 Forbidden", func() {
					Expect(response.StatusCode).To(Equal(http.StatusForbidden))
					responseBody, err := io.ReadAll(response.Body)
					Expect(err).NotTo(HaveOccurred())
					Expect(string(responseBody)).To(Equal("forbidden"))
				})
			})
		})

		Context("when the validator returns false", func() {
			It("rejects the request", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
				responseBody, err := io.ReadAll(response.Body)
				Expect(err).NotTo(HaveOccurred())
				Expect(string(responseBody)).To(Equal("not authorized"))
			})
		})
	})
})
