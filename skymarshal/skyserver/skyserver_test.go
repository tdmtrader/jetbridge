package skyserver_test

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/onsi/gomega/ghttp"
)

// stateToken mirrors the struct in skyserver.go for test signing
type stateToken struct {
	RedirectURI string `json:"redirect_uri"`
	Entropy     string `json:"entropy"`
	Timestamp   int64  `json:"ts"`
	Signature   string `json:"sig,omitempty"`
}

// signStateToken creates a signed state token for testing
func signStateToken(redirectURI string, ts int64) string {
	st := stateToken{
		RedirectURI: redirectURI,
		Entropy:     "test-entropy",
		Timestamp:   ts,
	}

	payload, _ := json.Marshal(st)

	mac := hmac.New(sha256.New, stateSigningKey)
	mac.Write(payload)
	st.Signature = base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	signed, _ := json.Marshal(st)
	return base64.RawURLEncoding.EncodeToString(signed)
}

// signTestJWT creates a valid JWT for testing the Login flow
func signTestJWT(expiry time.Time) string {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	signer, _ := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	raw, _ := jwt.Signed(signer).Claims(jwt.Claims{
		Subject:  "test-user",
		Audience: jwt.Audience{"some-aud"},
		Expiry:   jwt.NewNumericDate(expiry),
		IssuedAt: jwt.NewNumericDate(time.Now()),
	}).Serialize()
	return raw
}

var _ = Describe("Sky Server API", func() {

	ExpectServerBehaviour := func() {

		Describe("GET /sky/login", func() {
			var (
				err      error
				request  *http.Request
				response *http.Response
			)

			BeforeEach(func() {
				request, err = http.NewRequest("GET", skyServer.URL+"/sky/login", nil)
				Expect(err).NotTo(HaveOccurred())
			})

			JustBeforeEach(func() {
				skyServer.Client().CheckRedirect = func(req *http.Request, via []*http.Request) error {
					return http.ErrUseLastResponse
				}

				response, err = skyServer.Client().Do(request)
				Expect(err).NotTo(HaveOccurred())
			})

			ExpectNewLogin := func() {

				It("redirects the initial request to the oauthConfig.AuthURL", func() {
					redirectURL, err := response.Location()
					Expect(err).NotTo(HaveOccurred())
					Expect(redirectURL.Path).To(Equal("/auth"))

					redirectValues := redirectURL.Query()
					Expect(redirectValues.Get("access_type")).To(Equal("offline"))
					Expect(redirectValues.Get("response_type")).To(Equal("code"))
					Expect(redirectValues.Get("state")).NotTo(BeEmpty())
					Expect(redirectValues.Get("scope")).To(Equal("some-scope"))
				})

				Context("when redirect_uri is provided", func() {
					BeforeEach(func() {
						request.URL.RawQuery = "redirect_uri=/redirect"
					})

					It("stores redirect_uri in the signed state token", func() {
						redirectURL, err := response.Location()
						Expect(err).NotTo(HaveOccurred())

						stateParam := redirectURL.Query().Get("state")
						data, err := base64.RawURLEncoding.DecodeString(stateParam)
						Expect(err).NotTo(HaveOccurred())

						var state map[string]interface{}
						json.Unmarshal(data, &state)
						Expect(state["redirect_uri"]).To(Equal("/redirect"))
						Expect(state["sig"]).NotTo(BeEmpty())
						Expect(state["ts"]).NotTo(BeZero())
					})
				})

				Context("when redirect_uri is NOT provided", func() {
					BeforeEach(func() {
						request.URL.RawQuery = ""
					})

					It("stores / as the default redirect_uri in the signed state token", func() {
						redirectURL, err := response.Location()
						Expect(err).NotTo(HaveOccurred())

						stateParam := redirectURL.Query().Get("state")
						data, err := base64.RawURLEncoding.DecodeString(stateParam)
						Expect(err).NotTo(HaveOccurred())

						var state map[string]interface{}
						json.Unmarshal(data, &state)
						Expect(state["redirect_uri"]).To(Equal("/"))
					})
				})
			}

			Context("without an existing token", func() {
				BeforeEach(func() {
					fakeTokenMiddleware.GetAuthTokenReturns("")
				})
				ExpectNewLogin()
			})

			Context("when the token has no type", func() {
				BeforeEach(func() {
					fakeTokenMiddleware.GetAuthTokenReturns("some-token")
				})
				ExpectNewLogin()
			})

			Context("when the token is not a valid bearer token", func() {
				BeforeEach(func() {
					fakeTokenMiddleware.GetAuthTokenReturns("not-bearer some-token")
				})
				ExpectNewLogin()
			})

			Context("when the bearer token is not a valid JWT", func() {
				BeforeEach(func() {
					fakeTokenMiddleware.GetAuthTokenReturns("bearer not-a-jwt")
				})
				ExpectNewLogin()
			})

			Context("when the token is expired", func() {
				BeforeEach(func() {
					expiredJWT := signTestJWT(time.Now().Add(-time.Hour))
					fakeTokenMiddleware.GetAuthTokenReturns("bearer " + expiredJWT)
				})
				ExpectNewLogin()
			})

			Context("when the token is valid", func() {
				BeforeEach(func() {
					validJWT := signTestJWT(time.Now().Add(time.Hour))
					fakeTokenMiddleware.GetAuthTokenReturns("bearer " + validJWT)
				})

				It("redirects to the default redirect URI", func() {
					Expect(response.StatusCode).To(Equal(http.StatusTemporaryRedirect))
					redirectURL, err := response.Location()
					Expect(err).NotTo(HaveOccurred())
					Expect(redirectURL.Path).To(Equal("/"))
				})
			})
		})

		Describe("GET /sky/logout", func() {
			var (
				err      error
				request  *http.Request
				response *http.Response
			)

			BeforeEach(func() {
				request, err = http.NewRequest("GET", skyServer.URL+"/sky/logout", nil)
				Expect(err).NotTo(HaveOccurred())
			})

			JustBeforeEach(func() {
				response, err = skyServer.Client().Do(request)
				Expect(err).NotTo(HaveOccurred())
			})

			It("returns 200 OK", func() {
				Expect(response.StatusCode).To(Equal(http.StatusOK))
			})

			It("unsets all cookies", func() {
				Expect(fakeTokenMiddleware.UnsetAuthTokenCallCount()).To(Equal(1))
				Expect(fakeTokenMiddleware.UnsetCSRFTokenCallCount()).To(Equal(1))
				Expect(fakeTokenMiddleware.UnsetRefreshTokenCallCount()).To(Equal(1))
			})
		})

		Describe("GET /sky/callback", func() {
			var (
				err      error
				request  *http.Request
				response *http.Response
				body     []byte
			)

			BeforeEach(func() {
				request, err = http.NewRequest("GET", skyServer.URL+"/sky/callback", nil)
				Expect(err).NotTo(HaveOccurred())
			})

			JustBeforeEach(func() {
				response, err = skyServer.Client().Do(request)
				Expect(err).NotTo(HaveOccurred())

				body, err = io.ReadAll(response.Body)
				Expect(err).NotTo(HaveOccurred())
			})

			Context("when there's an error param", func() {
				BeforeEach(func() {
					request.URL.RawQuery = "error=some-error"
				})

				It("errors", func() {
					Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
				})

				It("shows the error message", func() {
					Expect(string(body)).To(Equal("some-error\n"))
				})
			})

			Context("when the state token is missing", func() {
				BeforeEach(func() {
					request.URL.RawQuery = ""
				})

				It("errors", func() {
					Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
				})

				It("shows invalid state message", func() {
					Expect(string(body)).To(Equal("invalid state token\n"))
				})
			})

			Context("when the state token has invalid signature", func() {
				BeforeEach(func() {
					st := stateToken{
						RedirectURI: "/redirect",
						Entropy:     "test",
						Timestamp:   time.Now().Unix(),
						Signature:   "invalid-signature",
					}
					data, _ := json.Marshal(st)
					invalidState := base64.RawURLEncoding.EncodeToString(data)
					request.URL.RawQuery = "state=" + invalidState
				})

				It("errors", func() {
					Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
				})

				It("shows invalid state message", func() {
					Expect(string(body)).To(Equal("invalid state token\n"))
				})
			})

			Context("when the state token is expired", func() {
				BeforeEach(func() {
					expiredState := signStateToken("/redirect", time.Now().Add(-2*time.Hour).Unix())
					request.URL.RawQuery = "state=" + expiredState
				})

				It("errors", func() {
					Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
				})

				It("shows invalid state message", func() {
					Expect(string(body)).To(Equal("invalid state token\n"))
				})
			})

			Context("when the state token is valid", func() {
				var validState string

				BeforeEach(func() {
					validState = signStateToken("/some-redirect", time.Now().Unix())
					request.URL.RawQuery = "state=" + validState
				})

				Context("when there is an authorization code", func() {
					BeforeEach(func() {
						request.URL.RawQuery = "code=some-code&state=" + validState
					})

					Context("when requesting a token fails", func() {
						BeforeEach(func() {
							dexServer.AppendHandlers(
								ghttp.CombineHandlers(
									ghttp.VerifyRequest("POST", "/token"),
									ghttp.VerifyHeaderKV("Authorization", "Basic ZGV4LWNsaWVudC1pZDpkZXgtY2xpZW50LXNlY3JldA=="),
									ghttp.VerifyFormKV("grant_type", "authorization_code"),
									ghttp.VerifyFormKV("code", "some-code"),
									ghttp.RespondWith(http.StatusInternalServerError, "some-token-error"),
								),
							)
						})

						It("errors", func() {
							Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
						})

						It("shows the oauth2 retrieve error response", func() {
							Expect(string(body)).To(Equal("some-token-error\n"))
						})
					})

					Context("when requesting a token from dex fails with oauth error", func() {
						BeforeEach(func() {
							dexServer.AppendHandlers(
								ghttp.CombineHandlers(
									ghttp.VerifyRequest("POST", "/token"),
									ghttp.RespondWithJSONEncoded(http.StatusOK, map[string]string{
										"token_type": "some-type",
										"id_token":   "some-id-token",
									}),
								),
							)
						})

						It("errors", func() {
							Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
						})

						It("shows oauth error", func() {
							Expect(string(body)).To(Equal("oauth2: server response missing access_token\n"))
						})
					})

					Context("when the server returns a token with id_token", func() {
						BeforeEach(func() {
							dexServer.AppendHandlers(
								ghttp.CombineHandlers(
									ghttp.VerifyRequest("POST", "/token"),
									ghttp.VerifyHeaderKV("Authorization", "Basic ZGV4LWNsaWVudC1pZDpkZXgtY2xpZW50LXNlY3JldA=="),
									ghttp.VerifyFormKV("grant_type", "authorization_code"),
									ghttp.VerifyFormKV("code", "some-code"),
									ghttp.RespondWithJSONEncoded(http.StatusOK, map[string]string{
										"token_type":    "bearer",
										"access_token":  "some-access-token",
										"id_token":      "some-id-token",
										"refresh_token": "some-refresh-token",
									}),
								),
							)
						})

						Context("when redirect URI is http://example.com", func() {
							BeforeEach(func() {
								stateToken := signStateToken("http://example.com", time.Now().Unix())
								request.URL.RawQuery = "code=some-code&state=" + stateToken
							})

							It("returns 404", func() {
								Expect(response.StatusCode).To(Equal(http.StatusNotFound))
							})
						})

						Context("when redirect URI is //example.com", func() {
							BeforeEach(func() {
								stateToken := signStateToken("//example.com", time.Now().Unix())
								request.URL.RawQuery = "code=some-code&state=" + stateToken
							})

							It("returns 404", func() {
								Expect(response.StatusCode).To(Equal(http.StatusNotFound))
							})
						})

						Context("when redirecting to the ATC", func() {
							BeforeEach(func() {
								stateToken := signStateToken("/valid-redirect", time.Now().Unix())
								request.URL.RawQuery = "code=some-code&state=" + stateToken
							})

							Context("when setting the auth token fails", func() {
								BeforeEach(func() {
									fakeTokenMiddleware.SetAuthTokenReturns(errors.New("nope"))
								})
								It("errors", func() {
									Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
								})
							})

							Context("when setting the auth token succeeds", func() {
								BeforeEach(func() {
									fakeTokenMiddleware.SetAuthTokenReturns(nil)
								})

								Context("when setting the csrf token fails", func() {
									BeforeEach(func() {
										fakeTokenMiddleware.SetCSRFTokenReturns(errors.New("nope"))
									})
									It("errors", func() {
										Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
									})
								})

								Context("when setting the csrf token succeeds", func() {
									BeforeEach(func() {
										fakeTokenMiddleware.SetCSRFTokenReturns(nil)
									})

									It("saves the ID token as the auth token", func() {
										Expect(fakeTokenMiddleware.SetAuthTokenCallCount()).To(Equal(1))
										_, tokenString, _ := fakeTokenMiddleware.SetAuthTokenArgsForCall(0)
										Expect(tokenString).To(Equal("bearer some-id-token"))
									})

									It("stores the refresh token in a separate cookie", func() {
										Expect(fakeTokenMiddleware.SetRefreshTokenCallCount()).To(Equal(1))
										_, tokenString, _ := fakeTokenMiddleware.SetRefreshTokenArgsForCall(0)
										Expect(tokenString).To(Equal("some-refresh-token"))
									})

									It("sets a new csrf token", func() {
										Expect(fakeTokenMiddleware.SetCSRFTokenCallCount()).To(Equal(1))
										_, tokenString, _ := fakeTokenMiddleware.SetCSRFTokenArgsForCall(0)
										Expect(tokenString).NotTo(BeEmpty())
									})

									It("redirects to redirect_uri from state token with the csrf_token", func() {
										_, tokenArg, _ := fakeTokenMiddleware.SetCSRFTokenArgsForCall(0)

										redirectResponse := response.Request.Response
										Expect(redirectResponse).NotTo(BeNil())
										Expect(redirectResponse.StatusCode).To(Equal(http.StatusTemporaryRedirect))

										skyServerURL, err := url.Parse(skyServer.URL)
										Expect(err).NotTo(HaveOccurred())

										locationURL, err := redirectResponse.Location()
										Expect(err).NotTo(HaveOccurred())
										Expect(locationURL.Host).To(Equal(skyServerURL.Host))
										Expect(locationURL.Path).To(Equal("/valid-redirect"))
										Expect(locationURL.Query().Get("csrf_token")).To(Equal(tokenArg))
									})
								})
							})
						})
					})
				})
			})
		})
		Describe("POST /sky/token/refresh", func() {
			var (
				err      error
				request  *http.Request
				response *http.Response
				body     []byte
			)

			BeforeEach(func() {
				request, err = http.NewRequest("POST", skyServer.URL+"/sky/token/refresh", nil)
				Expect(err).NotTo(HaveOccurred())
			})

			JustBeforeEach(func() {
				response, err = skyServer.Client().Do(request)
				Expect(err).NotTo(HaveOccurred())

				body, err = io.ReadAll(response.Body)
				Expect(err).NotTo(HaveOccurred())
			})

			Context("when using GET method", func() {
				BeforeEach(func() {
					request.Method = "GET"
				})

				It("returns method not allowed", func() {
					Expect(response.StatusCode).To(Equal(http.StatusMethodNotAllowed))
				})
			})

			Context("when no refresh token is present", func() {
				BeforeEach(func() {
					fakeTokenMiddleware.GetRefreshTokenReturns("")
				})

				It("returns unauthorized", func() {
					Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
				})
			})

			Context("when a refresh token is present", func() {
				BeforeEach(func() {
					fakeTokenMiddleware.GetRefreshTokenReturns("old-refresh-token")
				})

				Context("when Dex returns a new token", func() {
					BeforeEach(func() {
						dexServer.AppendHandlers(
							ghttp.CombineHandlers(
								ghttp.VerifyRequest("POST", "/token"),
								ghttp.VerifyFormKV("grant_type", "refresh_token"),
								ghttp.VerifyFormKV("refresh_token", "old-refresh-token"),
								ghttp.RespondWithJSONEncoded(http.StatusOK, map[string]string{
									"token_type":    "bearer",
									"access_token":  "new-access-token",
									"id_token":      "new-id-token",
									"refresh_token": "new-refresh-token",
								}),
							),
						)
					})

					It("returns 200", func() {
						Expect(response.StatusCode).To(Equal(http.StatusOK))
					})

					It("sets the new ID token as the auth token", func() {
						Expect(fakeTokenMiddleware.SetAuthTokenCallCount()).To(Equal(1))
						_, tokenString, _ := fakeTokenMiddleware.SetAuthTokenArgsForCall(0)
						Expect(tokenString).To(Equal("bearer new-id-token"))
					})

					It("rotates the refresh token", func() {
						Expect(fakeTokenMiddleware.SetRefreshTokenCallCount()).To(Equal(1))
						_, tokenString, _ := fakeTokenMiddleware.SetRefreshTokenArgsForCall(0)
						Expect(tokenString).To(Equal("new-refresh-token"))
					})

					It("returns a new CSRF token", func() {
						Expect(fakeTokenMiddleware.SetCSRFTokenCallCount()).To(Equal(1))

						var respBody map[string]string
						Expect(json.Unmarshal(body, &respBody)).To(Succeed())
						Expect(respBody["csrf_token"]).NotTo(BeEmpty())
					})
				})

				Context("when Dex returns an error", func() {
					BeforeEach(func() {
						dexServer.AppendHandlers(
							ghttp.CombineHandlers(
								ghttp.VerifyRequest("POST", "/token"),
								ghttp.RespondWith(http.StatusUnauthorized, `{"error":"invalid_grant"}`),
							),
						)
					})

					It("returns unauthorized", func() {
						Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
					})
				})
			})
		})
	}

	Describe("With TLS Server", func() {
		BeforeEach(func() {
			skyServer.StartTLS()
		})

		ExpectServerBehaviour()
	})

	Describe("Without TLS Server", func() {
		BeforeEach(func() {
			skyServer.Start()
		})

		ExpectServerBehaviour()
	})
})
