package idtoken_test

import (
	"time"

	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/creds/idtoken"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("IDToken behind the secret cache", func() {

	type tokenClaims struct {
		jwt.Claims
		Team     string `json:"team"`
		Pipeline string `json:"pipeline"`
		Job      string `json:"job"`
	}

	var verificationKey jose.JSONWebKey
	var config map[string]any
	var secrets creds.Secrets

	BeforeEach(func() {
		stored := saveSigningKey(*rsaJWK, 0).JWK()
		verificationKey = stored.Public()
		config = map[string]any{}
	})

	JustBeforeEach(func() {
		manager, err := idtoken.NewManager(testIssuer, signingKeyFactory, config)
		Expect(err).ToNot(HaveOccurred())
		Expect(manager.Validate()).To(Succeed())

		factory, err := manager.NewSecretsFactory(nil)
		Expect(err).ToNot(HaveOccurred())

		// the wrapping the ATC gives every var_source with --secret-cache-enabled
		secrets = creds.CredentialManagementConfig{
			RetryConfig: creds.SecretRetryConfig{Attempts: 1},
			CacheConfig: creds.SecretCacheConfig{
				Enabled:          true,
				Duration:         time.Minute,
				DurationNotFound: time.Minute,
				PurgeInterval:    time.Minute,
			},
		}.NewSecrets(factory)
	})

	tokenFor := func(params creds.SecretLookupParams) (string, tokenClaims) {
		token, _, found, err := creds.GetWithParams(secrets, "token", params)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())

		parsed, err := jwt.ParseSigned(token.(string), []jose.SignatureAlgorithm{idtoken.DefaultAlgorithm})
		Expect(err).ToNot(HaveOccurred())
		claims := tokenClaims{}
		Expect(parsed.Claims(verificationKey, &claims)).To(Succeed())
		return token.(string), claims
	}

	It("gives each team's job a token naming that job, not the first caller's", func() {
		jobA := creds.SecretLookupParams{Team: "team-a", Pipeline: "pipeline-a", Job: "job-a"}
		jobB := creds.SecretLookupParams{Team: "team-b", Pipeline: "pipeline-b", Job: "job-b"}

		tokenA, claimsA := tokenFor(jobA)
		Expect(claimsA.Team).To(Equal("team-a"))
		Expect(claimsA.Job).To(Equal("job-a"))

		_, claimsB := tokenFor(jobB)
		Expect(claimsB.Subject).To(Equal("team-b/pipeline-b"))
		Expect(claimsB.Team).To(Equal("team-b"))
		Expect(claimsB.Pipeline).To(Equal("pipeline-b"))
		Expect(claimsB.Job).To(Equal("job-b"))

		By("still serving the same caller from the cache")
		again, _ := tokenFor(jobA)
		Expect(again).To(Equal(tokenA))
	})

	Context("when the token expires sooner than the cache duration", func() {
		BeforeEach(func() {
			config["expires_in"] = "2s"
		})

		It("does not serve the token once it has expired", func() {
			job := creds.SecretLookupParams{Team: "team-a", Pipeline: "pipeline-a", Job: "job-a"}

			first, _ := tokenFor(job)
			time.Sleep(2100 * time.Millisecond)

			second, claims := tokenFor(job)
			Expect(second).ToNot(Equal(first))
			Expect(claims.Expiry.Time()).To(BeTemporally(">", time.Now()))
		})
	})
})
