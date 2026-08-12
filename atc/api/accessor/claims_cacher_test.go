package accessor_test

import (
	"sync"

	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// countingAccessTokenFetcher counts the reads that reach PostgreSQL, which is
// the only way a spec can tell a cache hit from a fetch.
type countingAccessTokenFetcher struct {
	accessor.AccessTokenFetcher

	mu      sync.Mutex
	fetched int
}

func (f *countingAccessTokenFetcher) GetAccessToken(rawToken string) (db.AccessToken, bool, error) {
	f.mu.Lock()
	f.fetched++
	f.mu.Unlock()

	return f.AccessTokenFetcher.GetAccessToken(rawToken)
}

func (f *countingAccessTokenFetcher) fetches() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.fetched
}

var _ = Describe("ClaimsCacher", func() {
	var (
		fixture           *realTeamFixture
		accessTokens      *countingAccessTokenFetcher
		maxCacheSizeBytes int

		claimsCacher accessor.AccessTokenFetcher
	)

	BeforeEach(func() {
		fixture = useRealTeamFixture()
		accessTokens = &countingAccessTokenFetcher{AccessTokenFetcher: fixture.AccessTokenFactory}
		maxCacheSizeBytes = 1000
	})

	JustBeforeEach(func() {
		claimsCacher = accessor.NewClaimsCacher(accessTokens, maxCacheSizeBytes)
	})

	It("fetches claims from the DB", func() {
		stored := fixture.persistAccessToken("token", map[string]any{"name": "foo"})

		token, found, err := claimsCacher.GetAccessToken("token")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(token).To(Equal(db.AccessToken{Token: "token", Claims: stored}))
		Expect(token.Claims.Username).To(Equal("foo"))

		Expect(accessTokens.fetches()).To(Equal(1), "did not fetch from DB")
	})

	It("returns not found if the token isn't found", func() {
		_, found, err := claimsCacher.GetAccessToken("token")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	It("doesn't fetch from the DB when the result is cached", func() {
		stored := fixture.persistAccessToken("token", map[string]any{"name": "foo"})

		claimsCacher.GetAccessToken("token")
		cached, found, err := claimsCacher.GetAccessToken("token")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(cached).To(Equal(db.AccessToken{Token: "token", Claims: stored}))

		Expect(accessTokens.fetches()).To(Equal(1), "did not cache claims")
	})

	It("doesn't cache claims when cache size is exceeded", func() {
		fixture.persistAccessToken("token", map[string]any{"a": stringWithLen(2000)})

		claimsCacher.GetAccessToken("token")
		claimsCacher.GetAccessToken("token")
		Expect(accessTokens.fetches()).To(Equal(2), "cached claims that exceed length")
	})

	It("evicts the least recently used access token when size limit exceeded", func() {
		for _, token := range []string{"token1", "token2", "token3"} {
			fixture.persistAccessToken(token, map[string]any{"a": stringWithLen(400)})
		}

		By("filling the cache")
		claimsCacher.GetAccessToken("token1")
		claimsCacher.GetAccessToken("token2")
		Expect(accessTokens.fetches()).To(Equal(2))

		By("overflowing the cache")
		claimsCacher.GetAccessToken("token3")
		Expect(accessTokens.fetches()).To(Equal(3))

		By("fetching the least recently used token")
		claimsCacher.GetAccessToken("token1")
		Expect(accessTokens.fetches()).To(Equal(4), "did not evict least recently used")

		By("ensuring the latest token was not evicted")
		claimsCacher.GetAccessToken("token3")
		Expect(accessTokens.fetches()).To(Equal(4), "evicted the latest token")
	})

	It("errors when the DB fails", func() {
		fixture.disconnect()

		_, _, err := claimsCacher.GetAccessToken("token")
		Expect(err).To(MatchError(ContainSubstring("database is closed")))
	})

	It("fetches claims from the DB concurrently", func() {
		// this is designed purely to trigger the race detector
		go claimsCacher.GetAccessToken("token1")
		go claimsCacher.GetAccessToken("token2")
		go claimsCacher.GetAccessToken("token3")
		Eventually(accessTokens.fetches).Should(Equal(3))
	})

	It("deletes token from cache", func() {
		fixture.persistAccessToken("token1", map[string]any{"name": "foo"})

		By("fetching the token for the first time (populates cache)")
		_, _, err := claimsCacher.GetAccessToken("token1")
		Expect(err).ToNot(HaveOccurred())
		Expect(accessTokens.fetches()).To(Equal(1))

		By("deleting the token from the cache")
		Expect(claimsCacher.DeleteAccessToken("token1")).To(Succeed())

		By("fetching the token again, should refetch from DB due to deletion")
		claimsCacher.GetAccessToken("token1")
		Expect(accessTokens.fetches()).To(Equal(2), "expected to refetch after delete")
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
