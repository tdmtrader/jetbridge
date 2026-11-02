package token_test

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/skymarshal/skycmd"
	"github.com/concourse/concourse/skymarshal/token"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var idTokenSigningKey *rsa.PrivateKey

func signIDToken(claims map[string]any) string {
	GinkgoHelper()

	if idTokenSigningKey == nil {
		var err error
		idTokenSigningKey, err = rsa.GenerateKey(rand.Reader, 2048)
		Expect(err).NotTo(HaveOccurred())
	}

	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: idTokenSigningKey},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	Expect(err).NotTo(HaveOccurred())

	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	Expect(err).NotTo(HaveOccurred())

	return raw
}

var _ = Describe("Access Tokens", func() {

	Describe("StoreAccessToken", func() {
		var (
			realdb                 *realTokenDB
			generator              token.Generator
			claimsParser           token.ClaimsParser
			accessTokenFactory     db.AccessTokenFactory
			userFactory            db.UserFactory
			displayUserIdGenerator atc.DisplayUserIdGenerator

			dummyLogger *lagertest.TestLogger
		)

		BeforeEach(func() {
			realdb = useRealTokenDB()
			generator = token.Factory{}
			claimsParser = token.NewClaimsParser()
			accessTokenFactory = realdb.AccessTokens
			userFactory = realdb.Users
			var err error
			displayUserIdGenerator, err = skycmd.NewSkyDisplayUserIdGenerator(map[string]string{"oidc": "email"})
			Expect(err).NotTo(HaveOccurred())

			dummyLogger = lagertest.NewTestLogger("whatever")
		})

		type testCase struct {
			it string

			path       string
			statusCode int
			body       string

			omitExpiryClaim  bool
			storeTokenErrors bool
			storeUserErrors  bool

			expectStatusCode     int
			expectBody           string
			expectLogError       string
			expectNewAccessToken bool
			expectTokenDelta     int
			expectUserDelta      int
		}

		for _, t := range []testCase{
			{
				it: "forwards non-token requests",

				path:       "/sky/issuer/callback",
				statusCode: 200,
				body:       "some payload",

				expectStatusCode: 200,
				expectBody:       "some payload",
			},
			{
				it: "modifies the access token",

				path:       "/sky/issuer/token",
				statusCode: 200,
				body:       `{"access_token":"123","token_type":"bearer","expires_in":1234,"id_token":"ID_TOKEN"}`,

				expectStatusCode:     200,
				expectNewAccessToken: true,
				expectTokenDelta:     1,
				expectUserDelta:      1,
			},
			{
				it: "forwards failure response",

				path:       "/sky/issuer/token",
				statusCode: 418,
				body:       "i've made a huge mistake",

				expectStatusCode: 418,
				expectBody:       "i've made a huge mistake",
			},
			{
				it: "errors if parsing claims fails",

				path:       "/sky/issuer/token",
				statusCode: 200,
				body:       `{"access_token":"123","token_type":"bearer","expires_in":1234,"id_token":"invalid"}`,

				expectStatusCode: 500,
				expectLogError:   "parse-id-token",
			},
			{
				it: "errors if generating token fails",

				path:       "/sky/issuer/token",
				statusCode: 200,
				body:       `{"access_token":"123","token_type":"bearer","expires_in":1234,"id_token":"ID_TOKEN"}`,

				omitExpiryClaim: true,

				expectStatusCode: 500,
				expectLogError:   "generate-access-token",
			},
			{
				it: "errors if storing token fails",

				path:       "/sky/issuer/token",
				statusCode: 200,
				body:       `{"access_token":"123","token_type":"bearer","expires_in":1234,"id_token":"ID_TOKEN"}`,

				storeTokenErrors: true,

				expectStatusCode: 500,
				expectLogError:   "create-access-token-in-db",
			},
			{
				it: "errors if storing user fails",

				path:       "/sky/issuer/token",
				statusCode: 200,
				body:       `{"access_token":"123","token_type":"bearer","expires_in":1234,"id_token":"ID_TOKEN"}`,

				storeUserErrors: true,

				expectStatusCode: 500,
				expectLogError:   "create-or-update-user",
				expectTokenDelta: 1,
			},
		} {
			t := t

			It(t.it, func() {
				expiry := jwt.NumericDate(2000000000)
				rawClaims := map[string]any{
					"sub":                "some-subject",
					"name":               "some-username",
					"preferred_username": "some-preferred-username",
					"email":              "some@example.com",
					"federated_claims": map[string]any{
						"user_id":      "some-user-id",
						"connector_id": "oidc",
					},
				}
				if !t.omitExpiryClaim {
					rawClaims["exp"] = expiry
				}
				idToken := signIDToken(rawClaims)

				baseHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(t.statusCode)
					w.Write([]byte(strings.ReplaceAll(t.body, "ID_TOKEN", idToken)))
				})
				r, _ := http.NewRequest("GET", t.path, nil)
				rec := httptest.NewRecorder()

				if t.storeTokenErrors {
					doomed := postgresRunner.OpenConn()
					accessTokenFactory = db.NewAccessTokenFactory(doomed)
					Expect(doomed.Close()).To(Succeed())
				}

				if t.storeUserErrors {
					doomed := postgresRunner.OpenConn()
					userFactory = db.NewUserFactory(doomed)
					Expect(doomed.Close()).To(Succeed())
				}

				var tokenCountBefore, userCountBefore int
				Expect(realdb.Conn.QueryRow(`SELECT count(*) FROM access_tokens`).Scan(&tokenCountBefore)).To(Succeed())
				Expect(realdb.Conn.QueryRow(`SELECT count(*) FROM users`).Scan(&userCountBefore)).To(Succeed())
				Expect(tokenCountBefore).To(BeZero())

				handler := token.StoreAccessToken(dummyLogger, baseHandler, generator, claimsParser, accessTokenFactory, userFactory, displayUserIdGenerator)
				handler.ServeHTTP(rec, r)

				result := rec.Result()
				Expect(result.StatusCode).To(Equal(t.expectStatusCode))

				if t.expectLogError != "" {
					Expect(dummyLogger.LogMessages()).To(ContainElement("whatever.token-request." + t.expectLogError))
				}

				var issuedToken string
				if t.expectNewAccessToken {
					var resp map[string]any
					Expect(json.Unmarshal(rec.Body.Bytes(), &resp)).To(Succeed())

					issuedToken, _ = resp["access_token"].(string)
					Expect(issuedToken).NotTo(BeEmpty())
					Expect(issuedToken).NotTo(Equal("123"))
					Expect(resp["token_type"]).To(Equal("bearer"))
					Expect(resp["expires_in"]).To(Equal(float64(1234)))
					Expect(resp["id_token"]).To(Equal(idToken))

					issuedExpiry, err := token.Factory{}.ParseExpiry(issuedToken)
					Expect(err).NotTo(HaveOccurred())
					Expect(issuedExpiry).To(Equal(expiry.Time()))
				} else {
					Expect(rec.Body.String()).To(Equal(t.expectBody))
				}

				var tokenCountAfter, userCountAfter int
				Expect(realdb.Conn.QueryRow(`SELECT count(*) FROM access_tokens`).Scan(&tokenCountAfter)).To(Succeed())
				Expect(realdb.Conn.QueryRow(`SELECT count(*) FROM users`).Scan(&userCountAfter)).To(Succeed())
				Expect(tokenCountAfter - tokenCountBefore).To(Equal(t.expectTokenDelta))
				Expect(userCountAfter - userCountBefore).To(Equal(t.expectUserDelta))

				if t.expectTokenDelta == 1 {
					var storedTokenValue string
					Expect(realdb.Conn.QueryRow(`SELECT token FROM access_tokens`).Scan(&storedTokenValue)).To(Succeed())
					if issuedToken != "" {
						Expect(storedTokenValue).To(Equal(issuedToken))
					}

					storedToken, tokenFound, err := realdb.AccessTokens.GetAccessToken(storedTokenValue)
					Expect(err).NotTo(HaveOccurred())
					Expect(tokenFound).To(BeTrue())
					Expect(storedToken.Token).To(Equal(storedTokenValue))
					Expect(storedToken.Claims.Subject).To(Equal("some-subject"))
					Expect(storedToken.Claims.Expiry).NotTo(BeNil())
					Expect(*storedToken.Claims.Expiry).To(Equal(expiry))
					Expect(storedToken.Claims.FederatedClaims).To(Equal(db.FederatedClaims{
						UserID:    "some-user-id",
						Connector: "oidc",
					}))
					Expect(storedToken.Claims.Username).To(Equal("some-username"))
					Expect(storedToken.Claims.PreferredUsername).To(Equal("some-preferred-username"))
					Expect(storedToken.Claims.Email).To(Equal("some@example.com"))
				}

				if t.expectUserDelta == 1 {
					var username, connector, subject string
					Expect(realdb.Conn.QueryRow(`
						SELECT username, connector, sub
						FROM users
						WHERE sub = $1
					`, "some-subject").Scan(&username, &connector, &subject)).To(Succeed())
					Expect(username).To(Equal("some@example.com"))
					Expect(connector).To(Equal("oidc"))
					Expect(subject).To(Equal("some-subject"))
				}
			})
		}
	})

	Describe("Token Generation", func() {
		It("generates a token with the unix timestamp", func() {
			factory := token.Factory{}
			expectExpiry := jwt.NewNumericDate(time.Now())
			rawToken, err := factory.GenerateAccessToken(db.Claims{
				Claims: jwt.Claims{Expiry: expectExpiry},
			})
			Expect(err).ToNot(HaveOccurred())
			expiry, err := factory.ParseExpiry(rawToken)
			Expect(err).ToNot(HaveOccurred())

			Expect(expiry).To(Equal(expectExpiry.Time()))
		})
	})
})
