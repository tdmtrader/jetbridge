package accessor_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"time"

	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("JWKSVerifier", func() {
	var (
		privateKey *rsa.PrivateKey
		jwksServer *httptest.Server
		verifier   accessor.TokenVerifier
		req        *http.Request
		claims     map[string]any
		verifyErr  error
		keyID      string
	)

	BeforeEach(func() {
		var err error
		privateKey, err = rsa.GenerateKey(rand.Reader, 2048)
		Expect(err).NotTo(HaveOccurred())
		keyID = "test-key-1"

		jwksServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			jwks := jose.JSONWebKeySet{
				Keys: []jose.JSONWebKey{
					{
						Key:       &privateKey.PublicKey,
						KeyID:     keyID,
						Algorithm: string(jose.RS256),
						Use:       "sig",
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(jwks)
		}))

		verifier = accessor.NewJWKSVerifier(jwksServer.URL, []string{"some-aud"})

		req, _ = http.NewRequest("GET", "http://localhost:8080", nil)
	})

	AfterEach(func() {
		jwksServer.Close()
	})

	signToken := func(claims jwt.Claims, extraClaims map[string]any) string {
		GinkgoHelper()
		signer, err := jose.NewSigner(
			jose.SigningKey{Algorithm: jose.RS256, Key: privateKey},
			(&jose.SignerOptions{}).WithType("JWT").WithHeader(jose.HeaderKey("kid"), keyID),
		)
		Expect(err).NotTo(HaveOccurred())

		builder := jwt.Signed(signer).Claims(claims)
		if extraClaims != nil {
			builder = builder.Claims(extraClaims)
		}
		raw, err := builder.Serialize()
		Expect(err).NotTo(HaveOccurred())
		return raw
	}

	JustBeforeEach(func() {
		claims, verifyErr = verifier.Verify(req)
	})

	Context("with a valid token", func() {
		BeforeEach(func() {
			token := signToken(
				jwt.Claims{
					Subject:  "user-123",
					Audience: jwt.Audience{"some-aud"},
					Expiry:   jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
					IssuedAt: jwt.NewNumericDate(time.Now()),
				},
				map[string]any{
					"email":              "user@example.com",
					"name":               "Test User",
					"preferred_username": "testuser",
					"groups":             []string{"team-a", "team-b"},
					"federated_claims": map[string]any{
						"connector_id": "local",
						"user_id":      "ms-user-456",
					},
				},
			)
			req.Header.Set("Authorization", "bearer "+token)
		})

		It("succeeds and returns raw claims", func() {
			Expect(verifyErr).NotTo(HaveOccurred())
			Expect(claims).NotTo(BeNil())
			Expect(claims["sub"]).To(Equal("user-123"))
			Expect(claims["email"]).To(Equal("user@example.com"))
			Expect(claims["name"]).To(Equal("Test User"))
			Expect(claims["preferred_username"]).To(Equal("testuser"))

			federated, ok := claims["federated_claims"].(map[string]any)
			Expect(ok).To(BeTrue())
			Expect(federated["connector_id"]).To(Equal("local"))
			Expect(federated["user_id"]).To(Equal("ms-user-456"))
		})
	})

	Context("when request has no token", func() {
		It("fails with no token error", func() {
			Expect(verifyErr).To(Equal(accessor.ErrVerificationNoToken))
		})
	})

	Context("when request has invalid auth header", func() {
		BeforeEach(func() {
			req.Header.Set("Authorization", "invalid")
		})

		It("fails with invalid token", func() {
			Expect(verifyErr).To(Equal(accessor.ErrVerificationInvalidToken))
		})
	})

	Context("when request has wrong auth scheme", func() {
		BeforeEach(func() {
			req.Header.Set("Authorization", "basic abc123")
		})

		It("fails with invalid token", func() {
			Expect(verifyErr).To(Equal(accessor.ErrVerificationInvalidToken))
		})
	})

	Context("when token is not a valid JWT", func() {
		BeforeEach(func() {
			req.Header.Set("Authorization", "bearer not-a-jwt")
		})

		It("fails with invalid token", func() {
			Expect(verifyErr).To(Equal(accessor.ErrVerificationInvalidToken))
		})
	})

	Context("when token is signed with wrong key", func() {
		BeforeEach(func() {
			wrongKey, err := rsa.GenerateKey(rand.Reader, 2048)
			Expect(err).NotTo(HaveOccurred())

			signer, err := jose.NewSigner(
				jose.SigningKey{Algorithm: jose.RS256, Key: wrongKey},
				(&jose.SignerOptions{}).WithType("JWT").WithHeader(jose.HeaderKey("kid"), keyID),
			)
			Expect(err).NotTo(HaveOccurred())

			raw, err := jwt.Signed(signer).Claims(jwt.Claims{
				Subject:  "user-123",
				Audience: jwt.Audience{"some-aud"},
				Expiry:   jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
			}).Serialize()
			Expect(err).NotTo(HaveOccurred())

			req.Header.Set("Authorization", "bearer "+raw)
		})

		It("fails with invalid token", func() {
			Expect(verifyErr).To(Equal(accessor.ErrVerificationInvalidToken))
		})
	})

	Context("when token has expired", func() {
		BeforeEach(func() {
			token := signToken(
				jwt.Claims{
					Subject:  "user-123",
					Audience: jwt.Audience{"some-aud"},
					Expiry:   jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)),
				},
				nil,
			)
			req.Header.Set("Authorization", "bearer "+token)
		})

		It("fails with token expired", func() {
			Expect(verifyErr).To(Equal(accessor.ErrVerificationTokenExpired))
		})
	})

	Context("when token has wrong audience", func() {
		BeforeEach(func() {
			token := signToken(
				jwt.Claims{
					Subject:  "user-123",
					Audience: jwt.Audience{"wrong-aud"},
					Expiry:   jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
				},
				nil,
			)
			req.Header.Set("Authorization", "bearer "+token)
		})

		It("fails with invalid audience", func() {
			Expect(verifyErr).To(Equal(accessor.ErrVerificationInvalidAudience))
		})
	})

	Context("when JWKS endpoint is unreachable", func() {
		BeforeEach(func() {
			jwksServer.Close()

			token := signToken(
				jwt.Claims{
					Subject:  "user-123",
					Audience: jwt.Audience{"some-aud"},
					Expiry:   jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
				},
				nil,
			)
			req.Header.Set("Authorization", "bearer "+token)

			// recreate verifier pointing at closed server
			verifier = accessor.NewJWKSVerifier(jwksServer.URL, []string{"some-aud"})
		})

		It("fails with invalid token", func() {
			Expect(verifyErr).To(Equal(accessor.ErrVerificationInvalidToken))
		})
	})

	Context("key rotation", func() {
		var newPrivateKey *rsa.PrivateKey

		BeforeEach(func() {
			var err error
			newPrivateKey, err = rsa.GenerateKey(rand.Reader, 2048)
			Expect(err).NotTo(HaveOccurred())

			// Update JWKS server to serve the new key
			jwksServer.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				jwks := jose.JSONWebKeySet{
					Keys: []jose.JSONWebKey{
						{
							Key:       &newPrivateKey.PublicKey,
							KeyID:     "new-key-2",
							Algorithm: string(jose.RS256),
							Use:       "sig",
						},
					},
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(jwks)
			})

			// Sign with the new key
			signer, err := jose.NewSigner(
				jose.SigningKey{Algorithm: jose.RS256, Key: newPrivateKey},
				(&jose.SignerOptions{}).WithType("JWT").WithHeader(jose.HeaderKey("kid"), "new-key-2"),
			)
			Expect(err).NotTo(HaveOccurred())

			raw, err := jwt.Signed(signer).Claims(jwt.Claims{
				Subject:  "user-123",
				Audience: jwt.Audience{"some-aud"},
				Expiry:   jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
			}).Serialize()
			Expect(err).NotTo(HaveOccurred())

			req.Header.Set("Authorization", "bearer "+raw)
		})

		It("fetches new keys and succeeds", func() {
			Expect(verifyErr).NotTo(HaveOccurred())
			Expect(claims["sub"]).To(Equal("user-123"))
		})
	})
})

var _ = Describe("JWKSVerifier key fetch", func() {
	var (
		privateKey *rsa.PrivateKey
		gets       atomic.Int32
	)

	BeforeEach(func() {
		var err error
		privateKey, err = rsa.GenerateKey(rand.Reader, 2048)
		Expect(err).NotTo(HaveOccurred())
		gets.Store(0)
	})

	requestWithin := func(kid string, d time.Duration) *http.Request {
		GinkgoHelper()
		signer, err := jose.NewSigner(
			jose.SigningKey{Algorithm: jose.RS256, Key: privateKey},
			(&jose.SignerOptions{}).WithType("JWT").WithHeader(jose.HeaderKey("kid"), kid),
		)
		Expect(err).NotTo(HaveOccurred())
		raw, err := jwt.Signed(signer).Claims(jwt.Claims{
			Audience: jwt.Audience{"some-aud"},
			Expiry:   jwt.NewNumericDate(time.Now().Add(time.Hour)),
		}).Serialize()
		Expect(err).NotTo(HaveOccurred())

		ctx, cancel := context.WithTimeout(context.Background(), d)
		DeferCleanup(cancel)
		req, err := http.NewRequestWithContext(ctx, "GET", "http://localhost:8080", nil)
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Authorization", "Bearer "+raw)
		return req
	}

	Context("when the JWKS endpoint accepts but never answers", func() {
		var verifier accessor.TokenVerifier

		BeforeEach(func() {
			release := make(chan struct{})
			stall := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gets.Add(1)
				<-release
			}))
			DeferCleanup(func() {
				close(release)
				stall.CloseClientConnections()
				stall.Close()
			})
			verifier = accessor.NewJWKSVerifier(stall.URL, []string{"some-aud"})
		})

		It("refuses the request once its context is done", func() {
			done := make(chan error, 1)
			go func() {
				_, err := verifier.Verify(requestWithin("unknown-kid", 100*time.Millisecond))
				done <- err
			}()
			Eventually(done, 3*time.Second).Should(Receive(MatchError(accessor.ErrVerificationInvalidToken)))
		})

		It("shares one fetch between concurrent misses", func() {
			var wg sync.WaitGroup
			for range 5 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, _ = verifier.Verify(requestWithin("unknown-kid", 100*time.Millisecond))
				}()
			}
			Eventually(gets.Load, time.Second).Should(BeNumerically(">=", 1))
			Consistently(gets.Load, time.Second).Should(Equal(int32(1)))
			wg.Wait()
		})
	})

	Context("when the JWKS endpoint fails", func() {
		It("does not re-fetch on every request after the failure", func() {
			failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gets.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			DeferCleanup(failing.Close)
			verifier := accessor.NewJWKSVerifier(failing.URL, []string{"some-aud"})

			for range 5 {
				_, err := verifier.Verify(requestWithin("unknown-kid", time.Second))
				Expect(err).To(MatchError(accessor.ErrVerificationInvalidToken))
			}
			Expect(gets.Load()).To(Equal(int32(1)))
		})
	})
})
