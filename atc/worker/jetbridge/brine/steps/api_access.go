package steps

import (
	"fmt"
	"sort"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/skymarshal/skycmd"
)

type AccessProbe struct {
	Team         db.Team
	Claims       map[string]any
	Authorized   bool
	DisplayField string
	DisplayID    string
	Observation  string
}

func AccessorDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, *AccessProbe](
			"a real team grants its {string} role to user brine-user",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (*AccessProbe, error) {
				role, err := paramAt("a real team grants its {string} role to user brine-user", p, 0)
				if err != nil {
					return nil, err
				}
				return newAccessProbe(resources, atc.TeamAuth{
					role: {"users": {"some-connector:some-user-id"}},
				}, false)
			},
		),

		brine.DefineMapUsing[brine.Empty, *AccessProbe](
			"a real team grants its {string} role to group brine-group",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (*AccessProbe, error) {
				role, err := paramAt("a real team grants its {string} role to group brine-group", p, 0)
				if err != nil {
					return nil, err
				}
				return newAccessProbe(resources, atc.TeamAuth{
					role: {"groups": {"some-connector:some-group"}},
				}, true)
			},
		),

		brine.DefineMapUsing[brine.Empty, *AccessProbe](
			"a real team grants user role {string} and group role {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (*AccessProbe, error) {
				userRole, groupRole, err := twoParams("a real team grants user role {string} and group role {string}", p)
				if err != nil {
					return nil, err
				}
				auth := atc.TeamAuth{
					userRole: {"users": {"some-connector:some-user-id"}},
				}
				if groupRole == userRole {
					auth[userRole]["groups"] = []string{"some-connector:some-group"}
				} else {
					auth[groupRole] = map[string][]string{"groups": {"some-connector:some-group"}}
				}
				return newAccessProbe(resources, auth, true)
			},
		),

		brine.DefineMap[*AccessProbe, *AccessProbe](
			"the user attempts an action requiring {string}",
			func(in *AccessProbe, p brine.Params, _ *brine.Recorder) (*AccessProbe, error) {
				requiredRole, err := paramAt("the user attempts an action requiring {string}", p, 0)
				if err != nil {
					return in, err
				}
				generator, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{})
				if err != nil {
					return in, err
				}
				access := accessor.NewAccessor(
					accessor.Verification{HasToken: true, IsTokenValid: true, RawClaims: in.Claims},
					requiredRole, "sub", []string{"system"}, []db.Team{in.Team}, generator,
				)
				in.Authorized = access.IsAuthorized(in.Team.Name())
				return in, nil
			},
		),

		brine.DefineCheck[*AccessProbe](
			"the real accessor says the request is {string}",
			func(in *AccessProbe, p brine.Params, _ *brine.Recorder) error {
				decision, err := paramAt("the real accessor says the request is {string}", p, 0)
				if err != nil {
					return err
				}
				want := decision == "authorized"
				if decision != "authorized" && decision != "denied" {
					return fmt.Errorf("unknown access decision %q", decision)
				}
				if in.Authorized != want {
					return fmt.Errorf("expected %s, accessor returned authorized=%t", decision, in.Authorized)
				}
				return nil
			},
		),

		brine.DefineMap[brine.Empty, *AccessProbe](
			"an OIDC user whose display field is {string}",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (*AccessProbe, error) {
				field, err := paramAt("an OIDC user whose display field is {string}", p, 0)
				if err != nil {
					return nil, err
				}
				generator, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{"oidc": field})
				if err != nil {
					return nil, err
				}
				claims := map[string]any{
					"sub": "some-sub", "name": "some-name", "preferred_username": "some-user-name", "email": "some-email",
					"federated_claims": map[string]any{"user_id": "some-id", "connector_id": "oidc"},
				}
				access := accessor.NewAccessor(
					accessor.Verification{HasToken: true, IsTokenValid: true, RawClaims: claims},
					accessor.ViewerRole, "sub", []string{"system"}, nil, generator,
				)
				return &AccessProbe{DisplayField: field, DisplayID: access.UserInfo().DisplayUserId}, nil
			},
		),

		CheckString[*AccessProbe]("the displayed identity is {string}", "the displayed user identity",
			func(in *AccessProbe) (string, error) { return in.DisplayID, nil }),

		brine.DefineMapUsing[brine.Empty, *AccessProbe](
			"the production accessor evaluates profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (*AccessProbe, error) {
				profile, err := paramAt("the production accessor evaluates profile {string}", p, 0)
				if err != nil {
					return nil, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return nil, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				observation, err := evaluateAccessorProfile(database, profile)
				return &AccessProbe{Observation: observation}, err
			},
		),

		CheckString[*AccessProbe]("the accessor observation is {string}", "the accessor observation",
			func(in *AccessProbe) (string, error) { return in.Observation, nil }),
	}
}

func newAccessProbe(resources brine.Resources, auth atc.TeamAuth, withGroup bool) (*AccessProbe, error) {
	database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
	if !ok {
		return nil, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
	}
	team, err := database.TeamFactory.CreateDefaultTeamIfNotExists()
	if err != nil {
		return nil, fmt.Errorf("create admin access team: %w", err)
	}
	if err := team.UpdateProviderAuth(auth); err != nil {
		return nil, fmt.Errorf("persist admin access team auth: %w", err)
	}
	team, found, err := database.TeamFactory.FindTeam(team.Name())
	if err != nil {
		return nil, fmt.Errorf("reload admin access team: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("admin access team disappeared after auth update")
	}
	claims := map[string]any{
		"federated_claims": map[string]any{
			"connector_id": "some-connector",
			"user_id":      "some-user-id",
		},
	}
	if withGroup {
		claims["groups"] = []any{"some-group"}
	}
	return &AccessProbe{Team: team, Claims: claims}, nil
}

func evaluateAccessorProfile(database JetbridgeDB, profile string) (string, error) {
	verification := accessor.Verification{}
	requiredRole := accessor.ViewerRole
	var teams []db.Team
	displayConfig := map[string]string{}
	observation := func(access accessor.Access) string { return "" }

	validUser := func() {
		verification = accessor.Verification{
			HasToken: true, IsTokenValid: true,
			RawClaims: accessClaims("some-connector", "some-user-id", "some-user-name", "some-group"),
		}
	}
	boolResult := func(value func(accessor.Access) bool) {
		observation = func(access accessor.Access) string { return fmt.Sprintf("%t", value(access)) }
	}

	switch profile {
	case "has-token/no-token":
		boolResult(func(access accessor.Access) bool { return access.HasToken() })
	case "has-token/token-present":
		verification.HasToken = true
		boolResult(func(access accessor.Access) bool { return access.HasToken() })
	case "authenticated/no-token":
		boolResult(func(access accessor.Access) bool { return access.IsAuthenticated() })
	case "authenticated/invalid-token":
		verification.HasToken = true
		boolResult(func(access accessor.Access) bool { return access.IsAuthenticated() })
	case "authenticated/valid-token":
		validUser()
		boolResult(func(access accessor.Access) bool { return access.IsAuthenticated() })
	case "authorized/no-token":
		boolResult(func(access accessor.Access) bool { return access.IsAuthorized("absent-team") })
	case "authorized/invalid-token":
		verification.HasToken = true
		boolResult(func(access accessor.Access) bool { return access.IsAuthorized("absent-team") })
	case "authorized/admin-on-another-team":
		validUser()
		admin, err := accessAdminTeam(database, accessor.OwnerRole)
		if err != nil {
			return "", err
		}
		teams = []db.Team{admin}
		boolResult(func(access accessor.Access) bool { return access.IsAuthorized("absent-team") })

	case "team-names/no-token":
		observation = teamNamesObservation
	case "team-names/invalid-token":
		verification.HasToken = true
		observation = teamNamesObservation
	case "team-names/admin":
		validUser()
		admin, err := accessAdminTeam(database, accessor.OwnerRole)
		if err != nil {
			return "", err
		}
		others, err := accessTeams(database, []accessTeamSpec{{"team-2", "", ""}, {"team-3", "", ""}})
		if err != nil {
			return "", err
		}
		teams = append([]db.Team{admin}, others...)
		observation = teamNamesObservation
	case "team-names/viewer", "team-names/member", "team-names/owner":
		validUser()
		var err error
		teams, err = accessTeams(database, []accessTeamSpec{
			{"team-1", accessor.OwnerRole, "user-id"},
			{"team-2", accessor.MemberRole, "user-id"},
			{"team-3", accessor.ViewerRole, "user-id"},
		})
		if err != nil {
			return "", err
		}
		requiredRole = strings.TrimPrefix(profile, "team-names/")
		observation = teamNamesObservation

	case "admin/no-token":
		boolResult(func(access accessor.Access) bool { return access.IsAdmin() })
	case "admin/invalid-token":
		verification.HasToken = true
		boolResult(func(access accessor.Access) bool { return access.IsAdmin() })
	case "admin/non-admin-teams":
		validUser()
		var err error
		teams, err = accessTeams(database, []accessTeamSpec{
			{"team-1", accessor.ViewerRole, "user-id"},
			{"team-2", accessor.MemberRole, "user-id"},
			{"team-3", accessor.OwnerRole, "user-id"},
		})
		if err != nil {
			return "", err
		}
		boolResult(func(access accessor.Access) bool { return access.IsAdmin() })
	case "admin/viewer", "admin/member", "admin/owner":
		validUser()
		role := strings.TrimPrefix(profile, "admin/")
		admin, err := accessAdminTeam(database, role)
		if err != nil {
			return "", err
		}
		teams = []db.Team{admin}
		boolResult(func(access accessor.Access) bool { return access.IsAdmin() })

	case "system/no-token":
		boolResult(func(access accessor.Access) bool { return access.IsSystem() })
	case "system/invalid-token":
		verification.HasToken = true
		boolResult(func(access accessor.Access) bool { return access.IsSystem() })
	case "system/wrong-subject":
		verification = accessor.Verification{HasToken: true, IsTokenValid: true, RawClaims: map[string]any{"sub": "not-system"}}
		boolResult(func(access accessor.Access) bool { return access.IsSystem() })
	case "system/matching-subject":
		verification = accessor.Verification{HasToken: true, IsTokenValid: true, RawClaims: map[string]any{"sub": "system"}}
		boolResult(func(access accessor.Access) bool { return access.IsSystem() })

	case "claims/no-token":
		observation = claimsObservation
	case "claims/invalid-token":
		verification.HasToken = true
		observation = claimsObservation
	case "claims/valid-token":
		verification = accessor.Verification{HasToken: true, IsTokenValid: true, RawClaims: fullAccessClaims("some-connector")}
		observation = claimsObservation

	case "user-info/all-fields":
		verification = accessor.Verification{HasToken: true, IsTokenValid: true, RawClaims: fullAccessClaims("oidc")}
		displayConfig = map[string]string{"oidc": "user_id"}
		observation = userInfoObservation
	case "user-info/unconfigured-display":
		verification = accessor.Verification{HasToken: true, IsTokenValid: true, RawClaims: fullAccessClaims("oidc")}
		observation = func(access accessor.Access) string { return access.UserInfo().DisplayUserId }

	case "team-roles/no-token":
		observation = teamRolesObservation
	case "team-roles/invalid-token":
		verification.HasToken = true
		observation = teamRolesObservation
	case "team-roles/no-membership":
		validUser()
		var err error
		teams, err = accessTeams(database, []accessTeamSpec{{"team-1", accessor.OwnerRole, "other-user"}})
		if err != nil {
			return "", err
		}
		observation = teamRolesObservation
	case "team-roles/by-user-id", "team-roles/by-user-name", "team-roles/by-group", "team-roles/cloudfoundry-alias":
		validUser()
		grant := strings.TrimPrefix(profile, "team-roles/by-")
		if profile == "team-roles/cloudfoundry-alias" {
			grant = "cf"
			verification.RawClaims = accessClaims("cloudfoundry", "some-user-id", "some-user-name", "some-group", "some-other-group")
		}
		var err error
		teams, err = accessTeams(database, []accessTeamSpec{
			{"team-1", accessor.OwnerRole, grant},
			{"team-2", accessor.MemberRole, grant},
			{"team-3", accessor.ViewerRole, grant},
		})
		if err != nil {
			return "", err
		}
		observation = teamRolesObservation
	case "team-roles/multiple":
		validUser()
		team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "team-1", Auth: atc.TeamAuth{
			accessor.OwnerRole:  {"users": {"some-connector:some-user-id"}},
			accessor.MemberRole: {"groups": {"some-connector:some-group"}},
		}})
		if err != nil {
			return "", err
		}
		teams = []db.Team{team}
		observation = teamRolesObservation
	case "team-roles/deduplicated":
		validUser()
		team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "team-1", Auth: atc.TeamAuth{
			accessor.OwnerRole: {
				"users": {"some-connector:some-user-id"}, "groups": {"some-connector:some-group"},
			},
		}})
		if err != nil {
			return "", err
		}
		teams = []db.Team{team}
		observation = teamRolesObservation
	default:
		return "", fmt.Errorf("unknown accessor profile %q", profile)
	}

	generator, err := skycmd.NewSkyDisplayUserIdGenerator(displayConfig)
	if err != nil {
		return "", err
	}
	access := accessor.NewAccessor(verification, requiredRole, "sub", []string{"system"}, teams, generator)
	return observation(access), nil
}

type accessTeamSpec struct {
	name  string
	role  string
	grant string
}

func accessAdminTeam(database JetbridgeDB, role string) (db.Team, error) {
	team, err := database.TeamFactory.CreateDefaultTeamIfNotExists()
	if err != nil {
		return nil, err
	}
	if err := team.UpdateProviderAuth(atc.TeamAuth{
		role: {"users": {"some-connector:some-user-id"}},
	}); err != nil {
		return nil, err
	}
	return team, nil
}

func accessTeams(database JetbridgeDB, specs []accessTeamSpec) ([]db.Team, error) {
	teams := make([]db.Team, 0, len(specs))
	for _, spec := range specs {
		auth := atc.TeamAuth{}
		if spec.role != "" {
			key, value := "users", "some-connector:some-user-id"
			switch spec.grant {
			case "user-name":
				value = "some-connector:some-user-name"
			case "group":
				key, value = "groups", "some-connector:some-group"
			case "cf":
				if spec.role == accessor.OwnerRole {
					value = "cf:some-user-id"
				} else {
					key, value = "groups", "cf:some-group"
				}
			case "other-user":
				value = "some-connector:someone-else"
			}
			auth[spec.role] = map[string][]string{key: {value}}
		}
		team, err := database.TeamFactory.CreateTeam(atc.Team{Name: spec.name, Auth: auth})
		if err != nil {
			return nil, err
		}
		teams = append(teams, team)
	}
	return teams, nil
}

func accessClaims(connector, userID, username string, groups ...string) map[string]any {
	groupClaims := make([]any, len(groups))
	for i, group := range groups {
		groupClaims[i] = group
	}
	return map[string]any{
		"preferred_username": username,
		"federated_claims":   map[string]any{"connector_id": connector, "user_id": userID},
		"groups":             groupClaims,
	}
}

func fullAccessClaims(connector string) map[string]any {
	return map[string]any{
		"sub": "some-sub", "name": "some-name", "preferred_username": "some-user-name", "email": "some-email",
		"federated_claims": map[string]any{"user_id": "some-id", "connector_id": connector},
	}
}

func teamNamesObservation(access accessor.Access) string {
	names := access.TeamNames()
	sort.Strings(names)
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ",")
}

func claimsObservation(access accessor.Access) string {
	claims := access.Claims()
	if claims == (accessor.Claims{}) {
		return "none"
	}
	return fmt.Sprintf("sub=%s,name=%s,user-id=%s,username=%s,email=%s,connector=%s",
		claims.Sub, claims.UserName, claims.UserID, claims.PreferredUsername, claims.Email, claims.Connector)
}

func userInfoObservation(access accessor.Access) string {
	info := access.UserInfo()
	return fmt.Sprintf("sub=%s,name=%s,user-id=%s,username=%s,email=%s,admin=%t,system=%t,teams=%s,connector=%s,display=%s",
		info.Sub, info.Name, info.UserId, info.UserName, info.Email, info.IsAdmin, info.IsSystem,
		canonicalTeamRoles(info.Teams), info.Connector, info.DisplayUserId)
}

func teamRolesObservation(access accessor.Access) string {
	return canonicalTeamRoles(access.TeamRoles())
}

func canonicalTeamRoles(roles map[string][]string) string {
	if len(roles) == 0 {
		return "none"
	}
	teams := make([]string, 0, len(roles))
	for team := range roles {
		teams = append(teams, team)
	}
	sort.Strings(teams)
	parts := make([]string, 0, len(teams))
	for _, team := range teams {
		teamRoles := append([]string(nil), roles[team]...)
		sort.Strings(teamRoles)
		parts = append(parts, team+"="+strings.Join(teamRoles, ","))
	}
	return strings.Join(parts, ";")
}
