package accessor_test

import (
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc/api/accessor"
)

var _ = Describe("Verifier", func() {
	var (
		fixture *realTeamFixture

		req *http.Request

		verifier accessor.TokenVerifier

		err error
	)

	BeforeEach(func() {
		fixture = useRealTeamFixture()

		fixture.persistAccessToken("1234567890", map[string]any{
			"sub":   "some-sub",
			"aud":   []any{"some-aud"},
			"exp":   float64(time.Now().Add(time.Hour).Unix()),
			"email": "some-user@example.com",
		})

		req, _ = http.NewRequest("GET", "localhost:8080", nil)
		req.Header.Set("Authorization", "bearer 1234567890")

		verifier = accessor.NewVerifier(fixture.AccessTokenFactory, []string{"some-aud"})
	})

	Describe("Verify", func() {

		JustBeforeEach(func() {
			_, err = verifier.Verify(req)
		})

		Context("when getting the access token errors", func() {
			BeforeEach(func() {
				fixture.disconnect()
			})

			It("errors", func() {
				Expect(err).To(MatchError(ContainSubstring("database is closed")))
			})
		})
	})
})
