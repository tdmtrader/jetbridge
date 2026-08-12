package auth_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/auditor/auditorfakes"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AuthenticationHandler", func() {

	var (
		server *httptest.Server
		client *http.Client

		// set by a Context to give the request a token; "" leaves it anonymous
		authorization string

		err      error
		request  *http.Request
		response *http.Response
	)

	simpleHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buffer := bytes.NewBufferString("simple hello")

		_, err := io.Copy(w, buffer)
		Expect(err).ToNot(HaveOccurred())
	})

	BeforeEach(func() {
		authorization = ""
		client = http.DefaultClient
	})

	JustBeforeEach(func() {
		if authorization != "" {
			request.Header.Set("Authorization", authorization)
		}

		response, err = client.Do(request)
		Expect(err).NotTo(HaveOccurred())
	})

	Describe("CheckAuthenticationHandler", func() {

		BeforeEach(func() {
			innerHandler := auth.CheckAuthenticationHandler(
				simpleHandler,
				auth.UnauthorizedRejector{},
			)

			server = httptest.NewServer(accessor.NewHandler(
				logger,
				"some-action",
				innerHandler,
				realAccessFactory(),
				new(auditorfakes.FakeAuditor),
				map[string]string{},
			))
		})

		Context("when a request is made", func() {
			BeforeEach(func() {
				request, err = http.NewRequest("GET", server.URL, nil)
				Expect(err).NotTo(HaveOccurred())
			})

			Context("when the user is authenticated ", func() {
				BeforeEach(func() {
					authorization = validAccessToken()
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

			Context("when the user is not authenticated", func() {
				It("returns 401", func() {
					Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
				})

				It("rejects the request", func() {
					responseBody, err := io.ReadAll(response.Body)
					Expect(err).NotTo(HaveOccurred())
					Expect(string(responseBody)).To(Equal("not authorized"))
				})
			})
		})
	})

	Describe("CheckAuthenticationIfProvidedHandler", func() {

		BeforeEach(func() {
			innerHandler := auth.CheckAuthenticationIfProvidedHandler(
				simpleHandler,
				auth.UnauthorizedRejector{},
			)

			server = httptest.NewServer(accessor.NewHandler(
				logger,
				"some-action",
				innerHandler,
				realAccessFactory(),
				new(auditorfakes.FakeAuditor),
				map[string]string{},
			))
		})

		Context("when a request is made", func() {
			BeforeEach(func() {
				request, err = http.NewRequest("GET", server.URL, nil)
				Expect(err).NotTo(HaveOccurred())
			})

			Context("when a token is provided", func() {
				Context("when the user is not authenticated", func() {
					BeforeEach(func() {
						authorization = expiredAccessToken()
					})

					It("returns 401", func() {
						Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
					})

					It("rejects the request", func() {
						responseBody, err := io.ReadAll(response.Body)
						Expect(err).NotTo(HaveOccurred())
						Expect(string(responseBody)).To(Equal("not authorized"))
					})
				})

				Context("when the user is authenticated ", func() {
					BeforeEach(func() {
						authorization = validAccessToken()
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
			})

			Context("when a token is NOT provided", func() {
				It("returns 200", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})

				It("proxies to the handler", func() {
					responseBody, err := io.ReadAll(response.Body)
					Expect(err).NotTo(HaveOccurred())
					Expect(string(responseBody)).To(Equal("simple hello"))
				})
			})
		})
	})
})
