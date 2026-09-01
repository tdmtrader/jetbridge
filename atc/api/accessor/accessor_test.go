package accessor_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/skymarshal/skycmd"
)

type accessorTeamFixture struct {
	Name  string
	Admin bool
	Auth  atc.TeamAuth
}

var _ = Describe("Accessor", func() {
	var (
		verification accessor.Verification
		requiredRole string
		teams        []db.Team
		access       accessor.Access
		teamFixtures [3]accessorTeamFixture
		useRealTeams bool
		persistTeams func() []db.Team

		displayUserIdConfig    map[string]string
		displayUserIdGenerator atc.DisplayUserIdGenerator
	)

	BeforeEach(func() {
		verification = accessor.Verification{}
		teamFixtures = [3]accessorTeamFixture{
			{Name: "some-team-1", Auth: atc.TeamAuth{}},
			{Name: "some-team-2", Auth: atc.TeamAuth{}},
			{Name: "some-team-3", Auth: atc.TeamAuth{}},
		}
		teams = []db.Team{}
		useRealTeams = false

		persistTeams = func() []db.Team {
			GinkgoHelper()
			if teamsAlreadyPersisted := len(teams) > 0; teamsAlreadyPersisted {
				return teams
			}

			fixture := useRealTeamFixture()
			persistedTeams := make([]db.Team, 0, len(teamFixtures))
			persistedIDs := map[int]struct{}{}
			for _, expectedTeam := range teamFixtures {
				persistedTeam := fixture.persistTeam(atc.Team{
					Name: expectedTeam.Name,
					Auth: expectedTeam.Auth,
				}, expectedTeam.Admin)

				Expect(persistedTeam.ID()).To(BeNumerically(">", 0))
				Expect(persistedTeam.Name()).To(Equal(expectedTeam.Name))
				Expect(persistedTeam.Admin()).To(Equal(expectedTeam.Admin))
				Expect(persistedTeam.Auth()).To(Equal(expectedTeam.Auth))

				persistedIDs[persistedTeam.ID()] = struct{}{}
				persistedTeams = append(persistedTeams, persistedTeam)
			}
			Expect(persistedIDs).To(HaveLen(len(teamFixtures)))

			allTeams, err := fixture.TeamFactory.GetTeams()
			Expect(err).NotTo(HaveOccurred())
			Expect(allTeams).To(HaveLen(len(teamFixtures)))
			for _, persistedTeam := range allTeams {
				Expect(persistedIDs).To(HaveKey(persistedTeam.ID()))
			}

			teams = persistedTeams
			return teams
		}

		displayUserIdConfig = map[string]string{}
	})

	JustBeforeEach(func() {
		if useRealTeams {
			teams = persistTeams()
		}

		var err error
		displayUserIdGenerator, err = skycmd.NewSkyDisplayUserIdGenerator(displayUserIdConfig)
		Expect(err).NotTo(HaveOccurred())

		access = accessor.NewAccessor(verification, requiredRole, "sub", []string{"system"}, teams, displayUserIdGenerator)
	})

	DescribeTable("IsAuthorized for users",
		func(requiredRole string, actualRole string, expected bool) {

			verification.HasToken = true
			verification.IsTokenValid = true
			verification.RawClaims = map[string]any{
				"federated_claims": map[string]any{
					"connector_id": "some-connector",
					"user_id":      "some-user-id",
				},
			}

			teamFixtures[0].Name = "some-team"
			teamFixtures[0].Admin = true
			teamFixtures[0].Auth = atc.TeamAuth{
				actualRole: map[string][]string{
					"users": {"some-connector:some-user-id"},
				},
			}

			teams = persistTeams()
			access = accessor.NewAccessor(verification, requiredRole, "sub", []string{"system"}, teams, displayUserIdGenerator)
			result := access.IsAuthorized("some-team")
			Expect(expected).Should(Equal(result))
		},

		Entry("viewer attempting viewer action", "viewer", "viewer", true),
		Entry("pipeline-operator attempting viewer action", "viewer", "pipeline-operator", true),
		Entry("member attempting viewer action", "viewer", "member", true),
		Entry("owner attempting viewer action", "viewer", "owner", true),

		Entry("viewer attempting pipeline-operator action", "pipeline-operator", "viewer", false),
		Entry("pipeline-operator attempting pipeline-operator action", "pipeline-operator", "pipeline-operator", true),
		Entry("member attempting pipeline-operator action", "pipeline-operator", "member", true),
		Entry("owner attempting pipeline-operator action", "pipeline-operator", "owner", true),

		Entry("viewer attempting member action", "member", "viewer", false),
		Entry("pipeline-operator attempting member action", "member", "pipeline-operator", false),
		Entry("member attempting member action", "member", "member", true),
		Entry("owner attempting member action", "member", "owner", true),

		Entry("viewer attempting owner action", "owner", "viewer", false),
		Entry("pipeline-operator attempting owner action", "owner", "pipeline-operator", false),
		Entry("member attempting owner action", "owner", "member", false),
		Entry("owner attempting owner action", "owner", "owner", true),
	)

	DescribeTable("IsAuthorized for groups",
		func(requiredRole string, actualRole string, expected bool) {

			verification.HasToken = true
			verification.IsTokenValid = true

			verification.RawClaims = map[string]any{
				"groups": []any{"some-group"},
				"federated_claims": map[string]any{
					"connector_id": "some-connector",
				},
			}

			teamFixtures[0].Name = "some-team"
			teamFixtures[0].Admin = true
			teamFixtures[0].Auth = atc.TeamAuth{
				actualRole: map[string][]string{
					"groups": {"some-connector:some-group"},
				},
			}

			teams = persistTeams()
			access = accessor.NewAccessor(verification, requiredRole, "sub", []string{"system"}, teams, displayUserIdGenerator)
			result := access.IsAuthorized("some-team")
			Expect(expected).Should(Equal(result))
		},

		Entry("viewer attempting viewer action", "viewer", "viewer", true),
		Entry("pipeline-operator attempting viewer action", "viewer", "pipeline-operator", true),
		Entry("member attempting viewer action", "viewer", "member", true),
		Entry("owner attempting viewer action", "viewer", "owner", true),

		Entry("viewer attempting pipeline-operator action", "pipeline-operator", "viewer", false),
		Entry("pipeline-operator attempting pipeline-operator action", "pipeline-operator", "pipeline-operator", true),
		Entry("member attempting pipeline-operator action", "pipeline-operator", "member", true),
		Entry("owner attempting pipeline-operator action", "pipeline-operator", "owner", true),

		Entry("viewer attempting member action", "member", "viewer", false),
		Entry("pipeline-operator attempting member action", "member", "pipeline-operator", false),
		Entry("member attempting member action", "member", "member", true),
		Entry("owner attempting member action", "member", "owner", true),

		Entry("viewer attempting owner action", "owner", "viewer", false),
		Entry("pipeline-operator attempting owner action", "owner", "pipeline-operator", false),
		Entry("member attempting owner action", "owner", "member", false),
		Entry("owner attempting owner action", "owner", "owner", true),
	)

	DescribeTable("IsAuthorized for both users and groups",
		func(requiredRole string, actualUserRole, actualGroupRole string, expected bool) {

			verification.HasToken = true
			verification.IsTokenValid = true

			verification.RawClaims = map[string]any{
				"groups": []any{"some-group"},
				"federated_claims": map[string]any{
					"connector_id": "some-connector",
					"user_id":      "some-user-id",
				},
			}

			teamFixtures[0].Name = "some-team"
			teamFixtures[0].Admin = true

			if actualUserRole == actualGroupRole {
				teamFixtures[0].Auth = atc.TeamAuth{
					actualUserRole: map[string][]string{
						"users":  {"some-connector:some-user-id"},
						"groups": {"some-connector:some-group"},
					},
				}
			} else {
				teamFixtures[0].Auth = atc.TeamAuth{
					actualUserRole: map[string][]string{
						"users": {"some-connector:some-user-id"},
					},
					actualGroupRole: map[string][]string{
						"groups": {"some-connector:some-group"},
					},
				}
			}

			teams = persistTeams()
			access = accessor.NewAccessor(verification, requiredRole, "sub", []string{"system"}, teams, displayUserIdGenerator)
			result := access.IsAuthorized("some-team")
			Expect(expected).Should(Equal(result))
		},

		Entry("user is member and group is viewer attempting owner action", "owner", "member", "viewer", false),
		Entry("user is viewer and group is member attempting owner action", "owner", "viewer", "member", false),
		Entry("user is member and group is viewer attempting owner action", "owner", "member", "member", false),
		Entry("user is viewer and group is member attempting owner action", "owner", "viewer", "viewer", false),
		Entry("user is member and group is viewer attempting member action", "member", "member", "viewer", true),
		Entry("user is viewer and group is member attempting member action", "member", "viewer", "member", true),
		Entry("user is member and group is viewer attempting member action", "member", "member", "member", true),
		Entry("user is viewer and group is member attempting member action", "member", "viewer", "viewer", false),
		Entry("user is member and group is viewer attempting pipeline-operator action", "pipeline-operator", "member", "viewer", true),
		Entry("user is viewer and group is member attempting pipeline-operator action", "pipeline-operator", "viewer", "member", true),
		Entry("user is member and group is viewer attempting pipeline-operator action", "pipeline-operator", "member", "member", true),
		Entry("user is viewer and group is member attempting pipeline-operator action", "pipeline-operator", "viewer", "viewer", false),
		Entry("user is member and group is viewer attempting viewer action", "viewer", "member", "viewer", true),
		Entry("user is viewer and group is member attempting viewer action", "viewer", "viewer", "member", true),
		Entry("user is member and group is viewer attempting viewer action", "viewer", "member", "member", true),
		Entry("user is viewer and group is member attempting viewer action", "viewer", "viewer", "viewer", true),
	)

	Describe("UserInfo", func() {
		Context("when there is a valid token", func() {
			BeforeEach(func() {
				displayUserIdConfig = map[string]string{"oidc": "user_id"}

				verification.HasToken = true
				verification.IsTokenValid = true
				verification.RawClaims = map[string]any{
					"sub":                "some-sub",
					"name":               "some-name",
					"preferred_username": "some-user-name",
					"email":              "some-email",
					"federated_claims": map[string]any{
						"user_id":      "some-id",
						"connector_id": "oidc",
					},
				}
			})

			DescribeTable("DisplayUserId for the field configured on the connector",
				func(fieldName string, expected string) {
					generator, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{"oidc": fieldName})
					Expect(err).NotTo(HaveOccurred())

					access = accessor.NewAccessor(verification, requiredRole, "sub", []string{"system"}, teams, generator)
					Expect(access.UserInfo().DisplayUserId).To(Equal(expected))
				},

				Entry("user_id", "user_id", "some-id"),
				Entry("name", "name", "some-name"),
				Entry("username", "username", "some-user-name"),
				Entry("email", "email", "some-email"),
			)

		})
	})

})
