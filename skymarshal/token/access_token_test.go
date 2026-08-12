package token_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/skymarshal/skycmd"
	"github.com/concourse/concourse/skymarshal/token"
	"github.com/concourse/concourse/skymarshal/token/tokenfakes"
	"github.com/go-jose/go-jose/v4/jwt"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Access Tokens", func() {

	Describe("StoreAccessToken", func() {
		var (
			realdb                 *realTokenDB
			generator              *tokenfakes.FakeGenerator
			claimsParser           *tokenfakes.FakeClaimsParser
			accessTokenFactory     db.AccessTokenFactory
			userFactory            db.UserFactory
			displayUserIdGenerator atc.DisplayUserIdGenerator

			dummyLogger *lagertest.TestLogger
		)

		BeforeEach(func() {
			realdb = useRealTokenDB()
			generator = new(tokenfakes.FakeGenerator)
			claimsParser = new(tokenfakes.FakeClaimsParser)
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

			parseClaimsErrors   bool
			generateTokenErrors bool
			storeTokenErrors    bool
			storeUserErrors     bool

			expectStatusCode int
			expectBody       string
			expectTokenDelta int
			expectUserDelta  int
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
				body:       `{"access_token":"123","token_type":"bearer","expires_in":1234,"id_token":"a.b.c"}`,

				expectStatusCode: 200,
				expectBody:       `{"access_token":"123abc","token_type":"bearer","expires_in":1234,"id_token":"a.b.c"}`,
				expectTokenDelta: 1,
				expectUserDelta:  1,
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

				parseClaimsErrors: true,

				expectStatusCode: 500,
			},
			{
				it: "errors if generating token fails",

				path:       "/sky/issuer/token",
				statusCode: 200,
				body:       `{"access_token":"123","token_type":"bearer","expires_in":1234,"id_token":"a.b.c"}`,

				generateTokenErrors: true,

				expectStatusCode: 500,
			},
			{
				it: "errors if storing token fails",

				path:       "/sky/issuer/token",
				statusCode: 200,
				body:       `{"access_token":"123","token_type":"bearer","expires_in":1234,"id_token":"a.b.c"}`,

				storeTokenErrors: true,

				expectStatusCode: 500,
			},
			{
				it: "errors if storing user fails",

				path:       "/sky/issuer/token",
				statusCode: 200,
				body:       `{"access_token":"123","token_type":"bearer","expires_in":1234,"id_token":"a.b.c"}`,

				storeUserErrors: true,

				expectStatusCode: 500,
				expectTokenDelta: 1,
			},
		} {
			t := t

			It(t.it, func() {
				expiry := jwt.NumericDate(2000000000)
				claims := db.Claims{
					Claims: jwt.Claims{
						Subject: "some-subject",
						Expiry:  &expiry,
					},
					FederatedClaims: db.FederatedClaims{
						UserID:    "some-user-id",
						Connector: "oidc",
					},
					Username:          "some-username",
					PreferredUsername: "some-preferred-username",
					Email:             "some@example.com",
					RawClaims: map[string]any{
						"sub":                "some-subject",
						"exp":                expiry,
						"name":               "some-username",
						"preferred_username": "some-preferred-username",
						"email":              "some@example.com",
						"federated_claims": map[string]any{
							"user_id":      "some-user-id",
							"connector_id": "oidc",
						},
					},
				}
				claimsParser.ParseClaimsReturns(claims, nil)

				baseHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(t.statusCode)
					w.Write([]byte(t.body))
				})
				r, _ := http.NewRequest("GET", t.path, nil)
				rec := httptest.NewRecorder()

				if t.parseClaimsErrors {
					claimsParser.ParseClaimsReturns(db.Claims{}, errors.New("claims parse error"))
				}

				if t.generateTokenErrors {
					generator.GenerateAccessTokenReturns("", errors.New("generate error"))
				} else {
					generator.GenerateAccessTokenReturns("123abc", nil)
				}

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

				handler := token.StoreAccessToken(dummyLogger, baseHandler, generator, claimsParser, accessTokenFactory, userFactory, displayUserIdGenerator)
				handler.ServeHTTP(rec, r)

				result := rec.Result()
				Expect(result.StatusCode).To(Equal(t.expectStatusCode))
				Expect(rec.Body.String()).To(Equal(t.expectBody))

				var tokenCountAfter, userCountAfter int
				Expect(realdb.Conn.QueryRow(`SELECT count(*) FROM access_tokens`).Scan(&tokenCountAfter)).To(Succeed())
				Expect(realdb.Conn.QueryRow(`SELECT count(*) FROM users`).Scan(&userCountAfter)).To(Succeed())
				Expect(tokenCountAfter - tokenCountBefore).To(Equal(t.expectTokenDelta))
				Expect(userCountAfter - userCountBefore).To(Equal(t.expectUserDelta))

				storedToken, tokenFound, err := realdb.AccessTokens.GetAccessToken("123abc")
				Expect(err).NotTo(HaveOccurred())
				Expect(tokenFound).To(Equal(t.expectTokenDelta == 1))
				if tokenFound {
					Expect(storedToken.Token).To(Equal("123abc"))
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
