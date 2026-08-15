package gc_test

import (
	"context"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/gc"
	"github.com/go-jose/go-jose/v4/jwt"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AccessTokensCollector", func() {
	var (
		collector    GcCollector
		tokenFactory db.AccessTokenFactory
	)

	// dbNow is the database's clock, not the test process's. RemoveExpiredAccessTokens
	// compares against now() inside Postgres, so fixtures are placed relative to that
	// -- otherwise a second of clock skew becomes a flake.
	dbNow := func() time.Time {
		var t time.Time
		Expect(dbConn.QueryRow("SELECT now()").Scan(&t)).To(Succeed())
		return t
	}

	expiringIn := func(name string, d time.Duration) {
		Expect(tokenFactory.CreateAccessToken(name, db.Claims{
			Claims: jwt.Claims{Expiry: jwt.NewNumericDate(dbNow().Add(d))},
		})).To(Succeed())
	}

	exists := func(name string) bool {
		_, found, err := tokenFactory.GetAccessToken(name)
		Expect(err).NotTo(HaveOccurred())
		return found
	}

	BeforeEach(func() {
		tokenFactory = db.NewAccessTokenFactory(dbConn)
		collector = gc.NewAccessTokensCollector(db.NewAccessTokenLifecycle(dbConn), jwt.DefaultLeeway)
	})

	Describe("Run", func() {
		It("removes tokens that have expired and keeps those that have not", func() {
			expiringIn("long-expired", -24*time.Hour)
			expiringIn("still-valid", 24*time.Hour)

			Expect(collector.Run(context.TODO())).To(Succeed())

			Expect(exists("long-expired")).To(BeFalse(), "expired token should have been collected")
			Expect(exists("still-valid")).To(BeTrue(), "unexpired token should have survived")
		})

		It("forwards its configured leeway, sparing a token only just expired", func() {
			// jwt.DefaultLeeway is a minute. A token half that far past its expiry
			// survives only if the collector actually passes the leeway through; with
			// a zero leeway it would be deleted. The persisted token outcome proves
			// that the collector honors its configured boundary.
			expiringIn("just-expired", -jwt.DefaultLeeway/2)

			Expect(collector.Run(context.TODO())).To(Succeed())

			Expect(exists("just-expired")).To(BeTrue(), "leeway was not forwarded to the lifecycle")
		})
	})
})
