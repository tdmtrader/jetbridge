package accessor_test

import (
	"net/http"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/skymarshal/skycmd"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AccessorFactory", func() {
	var (
		systemClaimKey    string
		systemClaimValues []string

		fixture       *realTeamFixture
		tokenVerifier accessor.TokenVerifier
		teamFetcher   accessor.TeamFetcher
		dummyRequest  *http.Request

		displayUserIdGenerator atc.DisplayUserIdGenerator

		role string
	)

	BeforeEach(func() {
		systemClaimKey = "sub"
		systemClaimValues = []string{"some-sub"}

		fixture = useRealTeamFixture()
		tokenVerifier = accessor.NewVerifier(fixture.AccessTokenFactory, []string{"some-aud"})
		teamFetcher = fixture.TeamFactory
		dummyRequest, _ = http.NewRequest("GET", "/", nil)

		var err error
		displayUserIdGenerator, err = skycmd.NewSkyDisplayUserIdGenerator(map[string]string{"github": "email"})
		Expect(err).NotTo(HaveOccurred())

		role = accessor.ViewerRole
	})

	Describe("Create", func() {
		var (
			access accessor.Access
			err    error
		)

		JustBeforeEach(func() {
			factory := accessor.NewAccessFactory(tokenVerifier, teamFetcher, systemClaimKey, systemClaimValues, displayUserIdGenerator)
			access, err = factory.Create(dummyRequest, role)
		})

		Context("when the token is valid", func() {
			BeforeEach(func() {
				fixture.persistAccessToken("user1-token", map[string]any{
					"sub":                "user1-sub",
					"aud":                []any{"some-aud"},
					"exp":                float64(time.Now().Add(time.Hour).Unix()),
					"preferred_username": "user1",
					"email":              "user1@example.com",
					"federated_claims": map[string]any{
						"connector_id": "github",
					},
				})
				dummyRequest.Header.Set("Authorization", "bearer user1-token")

				persistTeam := func(name, authenticatedUser string) {
					_, err := fixture.TeamFactory.CreateTeam(atc.Team{
						Name: name,
						Auth: atc.TeamAuth{
							accessor.ViewerRole: {"users": {authenticatedUser}},
						},
					})
					Expect(err).NotTo(HaveOccurred())
				}

				persistTeam("t1", "github:user1")
				persistTeam("t2", "github:another-user")
				persistTeam("t3", "github:user1")
			})

			It("returns an accessor authorized for the matching persisted teams", func() {
				Expect(access.TeamNames()).To(ConsistOf("t1", "t3"))
			})

			It("returns an accessor that uses the configured display user id", func() {
				Expect(access.UserInfo().DisplayUserId).To(Equal("user1@example.com"))
			})
		})

		Context("when the team fetcher returns an error", func() {
			BeforeEach(func() {
				fixture.disconnect()
			})

			It("returns an error", func() {
				Expect(err).To(MatchError(ContainSubstring("fetch teams")))
			})
		})

		Context("when the request carries no token", func() {
			It("the accessor has no token", func() {
				Expect(err).ToNot(HaveOccurred())
				Expect(access.HasToken()).To(BeFalse())
			})
		})

		Context("when the token has expired", func() {
			BeforeEach(func() {
				fixture.persistAccessToken("expired-token", map[string]any{
					"sub": "user1-sub",
					"aud": []any{"some-aud"},
					"exp": float64(time.Now().Add(-time.Hour).Unix()),
				})
				dummyRequest.Header.Set("Authorization", "bearer expired-token")
			})

			It("the accessor is unauthenticated", func() {
				Expect(err).ToNot(HaveOccurred())
				Expect(access.HasToken()).To(BeTrue())
				Expect(access.IsAuthenticated()).To(BeFalse())
			})
		})
	})
})
