package accessor_test

import (
	"time"

	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ClaimsCacher", func() {
	var (
		fixture           *realTeamFixture
		maxCacheSizeBytes int

		claimsCacher accessor.AccessTokenFetcher
	)

	BeforeEach(func() {
		fixture = useRealTeamFixture()
		maxCacheSizeBytes = 1000
	})

	JustBeforeEach(func() {
		claimsCacher = accessor.NewClaimsCacher(fixture.AccessTokenFactory, maxCacheSizeBytes)
	})

	It("fetches claims from the DB", func() {
		stored := fixture.persistAccessToken("token", map[string]any{"name": "foo"})

		token, found, err := claimsCacher.GetAccessToken("token")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(token).To(Equal(db.AccessToken{Token: "token", Claims: stored}))
		Expect(token.Claims.Username).To(Equal("foo"))
	})

	It("returns not found if the token isn't found", func() {
		_, found, err := claimsCacher.GetAccessToken("token")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	It("doesn't fetch from the DB when the result is cached", func() {
		stored := fixture.persistAccessToken("token", map[string]any{"name": "foo"})

		_, found, err := claimsCacher.GetAccessToken("token")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(fixture.AccessTokenFactory.DeleteAccessToken("token")).To(Succeed())

		cached, found, err := claimsCacher.GetAccessToken("token")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(cached).To(Equal(db.AccessToken{Token: "token", Claims: stored}))
	})

	It("doesn't cache claims when cache size is exceeded", func() {
		fixture.persistAccessToken("token", map[string]any{"a": stringWithLen(2000)})

		_, found, err := claimsCacher.GetAccessToken("token")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(fixture.AccessTokenFactory.DeleteAccessToken("token")).To(Succeed())

		_, found, err = claimsCacher.GetAccessToken("token")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	It("evicts the least recently used access token when size limit exceeded", func() {
		expected := map[string]db.AccessToken{}
		for _, token := range []string{"token1", "token2", "token3"} {
			claims := fixture.persistAccessToken(token, map[string]any{"a": stringWithLen(400)})
			expected[token] = db.AccessToken{Token: token, Claims: claims}
		}

		By("filling the cache")
		for _, token := range []string{"token1", "token2", "token3"} {
			fetched, found, err := claimsCacher.GetAccessToken(token)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(fetched).To(Equal(expected[token]))
		}

		By("removing the database fallback")
		for _, token := range []string{"token1", "token2", "token3"} {
			Expect(fixture.AccessTokenFactory.DeleteAccessToken(token)).To(Succeed())
		}

		By("observing the least recently used token is absent from both stores")
		_, found, err := claimsCacher.GetAccessToken("token1")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())

		By("observing the most recently used token is still cached")
		cached, found, err := claimsCacher.GetAccessToken("token3")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(cached).To(Equal(expected["token3"]))
	})

	It("fetches claims from the DB concurrently", func() {
		type fetchResult struct {
			rawToken string
			token    db.AccessToken
			found    bool
			err      error
		}

		rawTokens := []string{"token1", "token2", "token3"}
		expected := make(map[string]db.AccessToken, len(rawTokens))
		for _, rawToken := range rawTokens {
			claims := fixture.persistAccessToken(rawToken, map[string]any{"name": rawToken})
			expected[rawToken] = db.AccessToken{Token: rawToken, Claims: claims}
		}

		results := make(chan fetchResult, len(rawTokens))
		for _, rawToken := range rawTokens {
			rawToken := rawToken
			go func() {
				token, found, err := claimsCacher.GetAccessToken(rawToken)
				results <- fetchResult{rawToken: rawToken, token: token, found: found, err: err}
			}()
		}

		Eventually(results).WithTimeout(3 * time.Second).Should(HaveLen(len(rawTokens)))
		close(results)
		for result := range results {
			Expect(result.err).NotTo(HaveOccurred())
			Expect(result.found).To(BeTrue())
			Expect(result.token).To(Equal(expected[result.rawToken]))
		}
	})

	It("deletes token from cache", func() {
		fixture.persistAccessToken("token1", map[string]any{"name": "foo"})

		By("fetching the token for the first time (populates cache)")
		_, found, err := claimsCacher.GetAccessToken("token1")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())

		By("deleting the backing row")
		Expect(fixture.AccessTokenFactory.DeleteAccessToken("token1")).To(Succeed())

		By("deleting the token from the cache")
		Expect(claimsCacher.DeleteAccessToken("token1")).To(Succeed())

		By("observing the token is absent on the next fetch")
		_, found, err = claimsCacher.GetAccessToken("token1")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	It("leaves the row in place when only the cache entry is dropped", func() {
		fixture.persistAccessToken("token1", map[string]any{"name": "foo"})

		_, _, err := claimsCacher.GetAccessToken("token1")
		Expect(err).ToNot(HaveOccurred())
		Expect(claimsCacher.DeleteAccessToken("token1")).To(Succeed())

		_, found, err := fixture.AccessTokenFactory.GetAccessToken("token1")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
	})
})

func stringWithLen(l int) string {
	b := make([]byte, l)
	for i := 0; i < l; i++ {
		b[i] = 'a'
	}
	return string(b)
}
