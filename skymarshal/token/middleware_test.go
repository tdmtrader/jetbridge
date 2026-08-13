package token_test

import (
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/concourse/concourse/skymarshal/token"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Token Middleware", func() {

	var (
		err           error
		expiry        time.Time
		r             *http.Request
		w             *httptest.ResponseRecorder
		secureCookies bool
		middleware    token.Middleware
	)

	BeforeEach(func() {
		expiry = time.Now().Add(time.Minute)

		r, err = http.NewRequest("GET", "http://example.come", nil)
		Expect(err).NotTo(HaveOccurred())

		w = httptest.NewRecorder()

		secureCookies = false
	})

	JustBeforeEach(func() {
		middleware = token.NewMiddleware(secureCookies)
	})

	Describe("Auth Tokens", func() {
		Describe("GetAuthToken", func() {
			var result string

			BeforeEach(func() {
				r.AddCookie(&http.Cookie{Name: "skymarshal_auth", Value: "blah"})
			})

			JustBeforeEach(func() {
				result = middleware.GetAuthToken(r)
			})

			It("gets the token from the request", func() {
				Expect(result).To(Equal("blah"))
			})
		})

		Describe("SetAuthToken", func() {
			JustBeforeEach(func() {
				err = middleware.SetAuthToken(w, "blah", expiry)
			})

			It("writes the token to a cookie", func() {
				Expect(err).NotTo(HaveOccurred())

				cookies := w.Result().Cookies()
				Expect(cookies).To(HaveLen(1))

				Expect(cookies[0].Name).To(Equal("skymarshal_auth"))
				Expect(cookies[0].Expires.Unix()).To(Equal(expiry.Unix()))
				Expect(cookies[0].Value).To(Equal("blah"))
				Expect(cookies[0].Path).To(Equal("/"))
				Expect(cookies[0].HttpOnly).To(BeTrue())
				Expect(cookies[0].SameSite).To(Equal(http.SameSiteLaxMode))
			})
		})

		Describe("UnsetAuthToken", func() {
			JustBeforeEach(func() {
				middleware.UnsetAuthToken(w)
			})

			It("clears the token from the cookie", func() {
				cookies := w.Result().Cookies()
				Expect(cookies).To(HaveLen(1))
				Expect(cookies[0].Name).To(Equal("skymarshal_auth"))
				Expect(cookies[0].Value).To(Equal(""))
				Expect(cookies[0].MaxAge).To(Equal(-1))
				Expect(cookies[0].Path).To(Equal("/"))
				Expect(cookies[0].HttpOnly).To(BeTrue())
				Expect(cookies[0].SameSite).To(Equal(http.SameSiteLaxMode))
			})
		})
	})

	Describe("CSRF Tokens", func() {

		Describe("GetCSRFToken", func() {
			var result string

			BeforeEach(func() {
				r.AddCookie(&http.Cookie{Name: "skymarshal_csrf", Value: "blah"})
			})

			JustBeforeEach(func() {
				result = middleware.GetCSRFToken(r)
			})

			It("gets the token from the request", func() {
				Expect(result).To(Equal("blah"))
			})
		})

		Describe("SetCSRFToken", func() {
			JustBeforeEach(func() {
				err = middleware.SetCSRFToken(w, "blah", expiry)
			})

			It("writes the token to a cookie", func() {
				Expect(err).NotTo(HaveOccurred())

				cookies := w.Result().Cookies()
				Expect(cookies).To(HaveLen(1))
				Expect(cookies[0].Name).To(Equal("skymarshal_csrf"))
				Expect(cookies[0].Expires.Unix()).To(Equal(expiry.Unix()))
				Expect(cookies[0].Value).To(Equal("blah"))
				Expect(cookies[0].Path).To(Equal("/"))
				Expect(cookies[0].HttpOnly).To(BeTrue())
				Expect(cookies[0].SameSite).To(Equal(http.SameSiteLaxMode))
			})
		})

		Describe("UnsetCSRFToken", func() {
			JustBeforeEach(func() {
				middleware.UnsetCSRFToken(w)
			})

			It("clears the token from the cookie", func() {
				cookies := w.Result().Cookies()
				Expect(cookies).To(HaveLen(1))
				Expect(cookies[0].Name).To(Equal("skymarshal_csrf"))
				Expect(cookies[0].Value).To(Equal(""))
				Expect(cookies[0].MaxAge).To(Equal(-1))
				Expect(cookies[0].Path).To(Equal("/"))
				Expect(cookies[0].HttpOnly).To(BeTrue())
				Expect(cookies[0].SameSite).To(Equal(http.SameSiteLaxMode))
			})
		})
	})

	Describe("Refresh Tokens", func() {

		Describe("GetRefreshToken", func() {
			var result string

			BeforeEach(func() {
				r.AddCookie(&http.Cookie{Name: "skymarshal_refresh", Value: "blah"})
			})

			JustBeforeEach(func() {
				result = middleware.GetRefreshToken(r)
			})

			It("gets the token from the request", func() {
				Expect(result).To(Equal("blah"))
			})
		})

		Describe("SetRefreshToken", func() {
			JustBeforeEach(func() {
				err = middleware.SetRefreshToken(w, "blah", expiry)
			})

			It("writes the token to a cookie scoped to /sky/", func() {
				Expect(err).NotTo(HaveOccurred())

				cookies := w.Result().Cookies()
				Expect(cookies).To(HaveLen(1))
				Expect(cookies[0].Name).To(Equal("skymarshal_refresh"))
				Expect(cookies[0].Expires.Unix()).To(Equal(expiry.Unix()))
				Expect(cookies[0].Value).To(Equal("blah"))
				Expect(cookies[0].Path).To(Equal("/sky/"))
				Expect(cookies[0].HttpOnly).To(BeTrue())
				Expect(cookies[0].SameSite).To(Equal(http.SameSiteLaxMode))
			})
		})

		Describe("UnsetRefreshToken", func() {
			JustBeforeEach(func() {
				middleware.UnsetRefreshToken(w)
			})

			It("clears the token from the cookie scoped to /sky/", func() {
				cookies := w.Result().Cookies()
				Expect(cookies).To(HaveLen(1))
				Expect(cookies[0].Name).To(Equal("skymarshal_refresh"))
				Expect(cookies[0].Value).To(Equal(""))
				Expect(cookies[0].MaxAge).To(Equal(-1))
				Expect(cookies[0].Path).To(Equal("/sky/"))
				Expect(cookies[0].HttpOnly).To(BeTrue())
				Expect(cookies[0].SameSite).To(Equal(http.SameSiteLaxMode))
			})
		})
	})

	Describe("Secure Cookies", func() {
		var cookies []*http.Cookie

		JustBeforeEach(func() {
			Expect(middleware.SetAuthToken(w, "blah", expiry)).To(Succeed())
			Expect(middleware.SetCSRFToken(w, "blah", expiry)).To(Succeed())
			Expect(middleware.SetRefreshToken(w, "blah", expiry)).To(Succeed())
			middleware.UnsetAuthToken(w)
			middleware.UnsetCSRFToken(w)
			middleware.UnsetRefreshToken(w)

			cookies = w.Result().Cookies()
			Expect(cookies).To(HaveLen(6))
		})

		Context("when secure cookies are disabled", func() {
			It("marks no cookie secure", func() {
				for _, cookie := range cookies {
					Expect(cookie.Secure).To(BeFalse(), cookie.Name)
				}
			})
		})

		Context("when secure cookies are enabled", func() {
			BeforeEach(func() {
				secureCookies = true
			})

			It("marks every cookie secure", func() {
				for _, cookie := range cookies {
					Expect(cookie.Secure).To(BeTrue(), cookie.Name)
				}
			})
		})
	})
})
