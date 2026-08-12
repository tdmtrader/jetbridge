package accessor_test

import (
	"net/http"
	"time"

	"github.com/go-jose/go-jose/v4/jwt"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc/api/accessor"
)

var _ = Describe("Verifier", func() {
	var (
		fixture   *realTeamFixture
		rawClaims map[string]any

		req *http.Request

		verifier accessor.TokenVerifier

		claims map[string]any
		err    error
	)

	BeforeEach(func() {
		fixture = useRealTeamFixture()

		rawClaims = map[string]any{
			"sub":   "some-sub",
			"aud":   []any{"some-aud"},
			"exp":   float64(time.Now().Add(time.Hour).Unix()),
			"email": "some-user@example.com",
		}
		fixture.persistAccessToken("1234567890", rawClaims)

		req, _ = http.NewRequest("GET", "localhost:8080", nil)
		req.Header.Set("Authorization", "bearer 1234567890")

		verifier = accessor.NewVerifier(fixture.AccessTokenFactory, []string{"some-aud"})
	})

	Describe("Verify", func() {

		JustBeforeEach(func() {
			claims, err = verifier.Verify(req)
		})

		Context("when request has no token", func() {
			BeforeEach(func() {
				req.Header.Del("Authorization")
			})

			It("fails with no token", func() {
				Expect(err).To(Equal(accessor.ErrVerificationNoToken))
			})
		})

		Context("when request has an invalid auth header", func() {
			BeforeEach(func() {
				req.Header.Set("Authorization", "invalid")
			})

			It("fails verification", func() {
				Expect(err).To(Equal(accessor.ErrVerificationInvalidToken))
			})
		})

		Context("when request has an invalid token type", func() {
			BeforeEach(func() {
				req.Header.Set("Authorization", "not-bearer 1234567890")
			})

			It("fails verification", func() {
				Expect(err).To(Equal(accessor.ErrVerificationInvalidToken))
			})
		})

		Context("when getting the access token errors", func() {
			BeforeEach(func() {
				fixture.disconnect()
			})

			It("errors", func() {
				Expect(err).To(MatchError(ContainSubstring("database is closed")))
			})
		})

		Context("when the token is not found in the DB", func() {
			BeforeEach(func() {
				req.Header.Set("Authorization", "bearer never-issued")
			})

			It("fails verification", func() {
				Expect(err).To(Equal(accessor.ErrVerificationInvalidToken))
			})
		})

		Context("when the claims have expired", func() {
			BeforeEach(func() {
				fixture.persistAccessToken("expired-token", map[string]any{
					"sub": "some-sub",
					"aud": []any{"some-aud"},
					"exp": float64(time.Now().Add(-time.Hour).Unix()),
				})
				req.Header.Set("Authorization", "bearer expired-token")
			})

			It("fails verification", func() {
				Expect(err).To(Equal(accessor.ErrVerificationTokenExpired))
			})
		})

		Context("when the claims have invalid audience", func() {
			BeforeEach(func() {
				fixture.persistAccessToken("wrong-audience-token", map[string]any{
					"sub": "some-sub",
					"aud": []any{"invalid"},
					"exp": float64(time.Now().Add(time.Hour).Unix()),
				})
				req.Header.Set("Authorization", "bearer wrong-audience-token")
			})

			It("fails verification", func() {
				Expect(err).To(Equal(accessor.ErrVerificationInvalidAudience))
			})
		})

		Context("when the claims are valid", func() {
			It("succeeds", func() {
				Expect(err).ToNot(HaveOccurred())
			})

			It("returns the claims as they came back out of the database", func() {
				Expect(claims).To(Equal(rawClaims))
			})

			It("reconstructs the expiry and audience the validation depends on", func() {
				stored, found, err := fixture.AccessTokenFactory.GetAccessToken("1234567890")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(stored.Token).To(Equal("1234567890"))
				Expect(stored.Claims.Audience).To(Equal(jwt.Audience{"some-aud"}))
				Expect(stored.Claims.Expiry).NotTo(BeNil())
				Expect(stored.Claims.Expiry.Time().Unix()).To(Equal(int64(rawClaims["exp"].(float64))))
				Expect(stored.Claims.Email).To(Equal("some-user@example.com"))
			})
		})
	})
})

var _ = Describe("AccessTokenFactory", func() {
	It("round trips claims that were never written as typed fields", func() {
		fixture := useRealTeamFixture()

		stored := fixture.persistAccessToken("round-trip-token", map[string]any{
			"sub":                "some-sub",
			"name":               "some-user",
			"preferred_username": "some-preferred-username",
			"email":              "some-user@example.com",
			"federated_claims": map[string]any{
				"connector_id": "some-connector",
				"user_id":      "some-user-id",
			},
		})

		Expect(stored.Subject).To(Equal("some-sub"))
		Expect(stored.Username).To(Equal("some-user"))
		Expect(stored.PreferredUsername).To(Equal("some-preferred-username"))
		Expect(stored.Email).To(Equal("some-user@example.com"))
		Expect(stored.Connector).To(Equal("some-connector"))
		Expect(stored.UserID).To(Equal("some-user-id"))
		Expect(stored.RawClaims).To(HaveKeyWithValue("federated_claims", map[string]any{
			"connector_id": "some-connector",
			"user_id":      "some-user-id",
		}))
	})

	It("reports a deleted token as not found", func() {
		fixture := useRealTeamFixture()
		fixture.persistAccessToken("doomed-token", map[string]any{"sub": "some-sub"})

		Expect(fixture.AccessTokenFactory.DeleteAccessToken("doomed-token")).To(Succeed())

		_, found, err := fixture.AccessTokenFactory.GetAccessToken("doomed-token")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
	})
})
