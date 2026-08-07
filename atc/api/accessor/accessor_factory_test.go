package accessor_test

import (
	"errors"
	"net/http"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/accessor/accessorfakes"
	"github.com/concourse/concourse/atc/atcfakes"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AccessorFactory", func() {
	var (
		systemClaimKey    string
		systemClaimValues []string

		fakeTokenVerifier *accessorfakes.FakeTokenVerifier
		teamFactory       db.TeamFactory
		teamFetcher       accessor.TeamFetcher
		dummyRequest      *http.Request

		fakeDisplayUserIdGenerator *atcfakes.FakeDisplayUserIdGenerator

		role string
	)

	BeforeEach(func() {
		systemClaimKey = "sub"
		systemClaimValues = []string{"some-sub"}

		fakeTokenVerifier = new(accessorfakes.FakeTokenVerifier)
		teamFactory = useRealTeamFactory()
		teamFetcher = teamFactory
		dummyRequest, _ = http.NewRequest("GET", "/", nil)

		fakeDisplayUserIdGenerator = new(atcfakes.FakeDisplayUserIdGenerator)

		role = accessor.ViewerRole
	})

	Describe("Create", func() {
		var (
			access accessor.Access
			err    error
		)

		JustBeforeEach(func() {
			factory := accessor.NewAccessFactory(fakeTokenVerifier, teamFetcher, systemClaimKey, systemClaimValues, fakeDisplayUserIdGenerator)
			access, err = factory.Create(dummyRequest, role)
		})

		Context("when the token is valid", func() {
			BeforeEach(func() {
				fakeTokenVerifier.VerifyReturns(map[string]any{
					"preferred_username": "user1",
					"federated_claims": map[string]any{
						"connector_id": "github",
					},
				}, nil)

				persistTeam := func(name, authenticatedUser string) {
					_, err := teamFactory.CreateTeam(atc.Team{
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
		})

		Context("when the team fetcher returns an error", func() {
			BeforeEach(func() {
				failingTeamFetcher := new(accessorfakes.FakeTeamFetcher)
				failingTeamFetcher.GetTeamsReturns(nil, errors.New("nope"))
				teamFetcher = failingTeamFetcher
			})

			It("returns an error", func() {
				Expect(err).To(HaveOccurred())
			})
		})

		Context("when the verifier returns a NoToken error", func() {
			BeforeEach(func() {
				fakeTokenVerifier.VerifyReturns(nil, accessor.ErrVerificationNoToken)
			})

			It("the accessor has no token", func() {
				Expect(err).ToNot(HaveOccurred())
				Expect(access.HasToken()).To(BeFalse())
			})
		})

		Context("when the verifier returns some other error", func() {
			BeforeEach(func() {
				fakeTokenVerifier.VerifyReturns(nil, accessor.ErrVerificationTokenExpired)
			})

			It("the accessor is unauthenticated", func() {
				Expect(err).ToNot(HaveOccurred())
				Expect(access.IsAuthenticated()).To(BeFalse())
			})
		})
	})
})
