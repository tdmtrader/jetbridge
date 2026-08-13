package auth_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"

	"code.cloudfoundry.org/lager/v3"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/buildserver"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/skymarshal/token"
)

var _ = Describe("WebAuthHandler", func() {
	var (
		middleware token.Middleware
		nested     recordingHandler
	)

	var server *httptest.Server

	BeforeEach(func() {
		middleware = token.NewMiddleware(true)
		nested = recordingHandler{requests: make(chan *http.Request, 1)}

		server = httptest.NewServer(auth.WebAuthHandler{
			Handler:    nested,
			Middleware: middleware,
		})
	})

	AfterEach(func() {
		server.Close()
	})

	Describe("handling a request", func() {
		var request *http.Request
		var response *http.Response
		var authenticated bool

		BeforeEach(func() {
			authenticated = false

			var err error
			request, err = http.NewRequest("GET", server.URL, bytes.NewBufferString("hello"))
			Expect(err).NotTo(HaveOccurred())
		})

		// The cookie is attached here rather than in a BeforeEach so that a
		// context which replaces the request still sends it.
		JustBeforeEach(func() {
			if authenticated {
				request.AddCookie(&http.Cookie{Name: authCookieName, Value: "username:password"})
			}

			var err error
			response, err = http.DefaultClient.Do(request)
			Expect(err).NotTo(HaveOccurred())
			defer response.Body.Close()
		})

		Context("without the auth cookie", func() {
			It("does not set auth cookie in response", func() {
				Expect(response.Cookies()).To(HaveLen(0))
			})

			It("proxies to the handler without setting the Authorization header", func() {
				var proxied *http.Request
				Expect(nested.requests).To(Receive(&proxied))
				Expect(proxied.Header.Get("Authorization")).To(BeEmpty())
			})

			It("does not set CSRF required context in request", func() {
				var proxied *http.Request
				Expect(nested.requests).To(Receive(&proxied))
				csrfRequiredContext := proxied.Context().Value(auth.CSRFRequiredKey)
				Expect(csrfRequiredContext).To(BeNil())
			})

			Context("the nested handler returns unauthorized", func() {
				BeforeEach(func() {
					request = requestToNestedStatus(middleware, http.StatusUnauthorized)
				})

				It("does not unset any cookie", func() {
					Expect(response.Cookies()).To(BeEmpty())
				})
			})
		})

		Context("with the auth cookie", func() {
			BeforeEach(func() {
				authenticated = true
			})

			It("sets the Authorization header with the value from the cookie", func() {
				var proxied *http.Request
				Expect(nested.requests).To(Receive(&proxied))
				Expect(proxied.Header.Get("Authorization")).To(Equal("username:password"))
			})

			It("sets CSRF required context in request", func() {
				var proxied *http.Request
				Expect(nested.requests).To(Receive(&proxied))
				csrfRequiredContext := proxied.Context().Value(auth.CSRFRequiredKey)
				Expect(csrfRequiredContext).NotTo(BeNil())

				boolCsrf := csrfRequiredContext.(bool)
				Expect(boolCsrf).To(BeFalse())
			})

			Context("and the request also has an Authorization header", func() {
				BeforeEach(func() {
					request.Header.Set("Authorization", "foobar")
				})

				It("does not override the Authorization header", func() {
					var proxied *http.Request
					Expect(nested.requests).To(Receive(&proxied))
					Expect(proxied.Header.Get("Authorization")).To(Equal("foobar"))
				})
			})

			Context("the nested handler returns an event stream", func() {
				BeforeEach(func() {
					team := createTeam("event-stream-team")
					build := createJobBuild(team, "event-stream-pipeline", "event-stream-job")
					started, err := build.Start(atc.Plan{})
					Expect(err).NotTo(HaveOccurred())
					Expect(started).To(BeTrue())
					Expect(build.Finish(db.BuildStatusSucceeded)).To(Succeed())
					found, err := build.Reload()
					Expect(err).NotTo(HaveOccurred())
					Expect(found).To(BeTrue())
					Expect(build.IsCompleted()).To(BeTrue())

					eventServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						defer GinkgoRecover()
						auth.WebAuthHandler{
							Handler:    buildserver.NewEventHandler(lager.NewLogger("test"), build),
							Middleware: middleware,
						}.ServeHTTP(w, r)
					}))
					DeferCleanup(eventServer.Close)

					request, err = http.NewRequest("GET", eventServer.URL, bytes.NewBufferString("hello"))
					Expect(err).NotTo(HaveOccurred())
				})

				It("returns success", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})
			})

			Context("the nested handler returns unauthorized", func() {
				BeforeEach(func() {
					request = requestToNestedStatus(middleware, http.StatusUnauthorized)
				})

				It("unsets the auth cookie", func() {
					cookie := cookieNamed(response, authCookieName)
					Expect(cookie.Value).To(BeEmpty())
					Expect(cookie.MaxAge).To(Equal(-1))
					Expect(cookie.Path).To(Equal("/"))
					Expect(cookie.Secure).To(BeTrue())
					Expect(cookie.HttpOnly).To(BeTrue())
					Expect(cookie.SameSite).To(Equal(http.SameSiteLaxMode))
				})

				It("unsets the csrf cookie", func() {
					cookie := cookieNamed(response, csrfCookieName)
					Expect(cookie.Value).To(BeEmpty())
					Expect(cookie.MaxAge).To(Equal(-1))
					Expect(cookie.Path).To(Equal("/"))
					Expect(cookie.Secure).To(BeTrue())
					Expect(cookie.HttpOnly).To(BeTrue())
					Expect(cookie.SameSite).To(Equal(http.SameSiteLaxMode))
				})

				It("unsets the cookies before the status is written", func() {
					Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
					Expect(response.Cookies()).To(HaveLen(2))
				})
			})
		})
	})

	Describe("CSRF Required", func() {
		var request *http.Request
		var err error
		Context("when CSRF context is set", func() {
			BeforeEach(func() {
				request, err = http.NewRequest("GET", server.URL, bytes.NewBufferString("hello"))
				Expect(err).To(BeNil())

				ctx := context.WithValue(request.Context(), auth.CSRFRequiredKey, true)
				request = request.WithContext(ctx)

			})
			It("fetches the bool value", func() {
				Expect(auth.IsCSRFRequired(request)).To(BeTrue())
			})
		})

		Context("when CSRF context is not set", func() {
			BeforeEach(func() {
				request, err = http.NewRequest("GET", server.URL, bytes.NewBufferString("hello"))
				Expect(err).To(BeNil())
			})
			It("fetches the bool value", func() {
				Expect(auth.IsCSRFRequired(request)).To(BeFalse())
			})

		})
	})
})

// recordingHandler hands each request it is given to the spec over a buffered
// channel: the handler runs on the server's goroutine, so the request cannot be
// stashed in a variable the spec reads.
type recordingHandler struct {
	requests chan *http.Request
}

func (handler recordingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	handler.requests <- r
}

// requestToNestedStatus points a request at a WebAuthHandler whose nested
// handler answers with status, which is what drives the cookie unsetting.
func requestToNestedStatus(middleware token.Middleware, status int) *http.Request {
	server := httptest.NewServer(auth.WebAuthHandler{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}),
		Middleware: middleware,
	})
	DeferCleanup(server.Close)

	request, err := http.NewRequest("GET", server.URL, bytes.NewBufferString("hello"))
	Expect(err).NotTo(HaveOccurred())
	return request
}
