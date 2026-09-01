package steps

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/auditor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/skymarshal/skycmd"
)

const (
	brineAuthAudience  = "brine-audience"
	brineAuthConnector = "brine-connector"
	brineAuthUserID    = "brine-user"
)

type AuthHTTPOutcome struct {
	Status int
	Body   string
}

func APIAuthDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, AuthHTTPOutcome](
			"the real API auth boundary {string} receives identity {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (AuthHTTPOutcome, error) {
				boundary, identity, err := twoParams("the real API auth boundary {string} receives identity {string}", p)
				if err != nil {
					return AuthHTTPOutcome{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return AuthHTTPOutcome{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				return exerciseAPIAuthBoundary(database, boundary, identity)
			},
		),

		CheckInt[AuthHTTPOutcome]("the auth response status is {int}", "the auth response status",
			func(in AuthHTTPOutcome) (int, error) { return in.Status, nil }),
		CheckString[AuthHTTPOutcome]("the auth response body is {string}", "the auth response body",
			func(in AuthHTTPOutcome) (string, error) { return in.Body, nil }),
		brine.DefineCheck[AuthHTTPOutcome](
			"the auth delegate was reached",
			func(in AuthHTTPOutcome, _ brine.Params, _ *brine.Recorder) error {
				if in.Body != "delegate" {
					return fmt.Errorf("auth delegate was not reached; response body is %q", in.Body)
				}
				return nil
			},
		),
		brine.DefineCheck[AuthHTTPOutcome](
			"the auth delegate was not reached",
			func(in AuthHTTPOutcome, _ brine.Params, _ *brine.Recorder) error {
				if in.Body == "delegate" {
					return fmt.Errorf("auth delegate was reached")
				}
				return nil
			},
		),
	}
}

func exerciseAPIAuthBoundary(database JetbridgeDB, boundary, identity string) (AuthHTTPOutcome, error) {
	logger := lagertest.NewTestLogger("brine-api-auth")
	display, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{})
	if err != nil {
		return AuthHTTPOutcome{}, err
	}
	accessFactory := accessor.NewAccessFactory(
		accessor.NewVerifier(db.NewAccessTokenFactory(database.Conn), []string{brineAuthAudience}),
		database.TeamFactory, "sub", []string{"brine-system"}, display,
	)
	aud := auditor.NewAuditor(true, true, true, true, true, true, true, true, true, logger)
	rejector := auth.UnauthorizedRejector{}
	delegate := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "delegate")
	})

	var inner http.Handler
	var action string
	teamName := "some-team"
	authorization := ""

	switch boundary {
	case "admin":
		action = atc.ListActiveUsersSince
		inner = auth.CheckAdminHandler(delegate, rejector)
		switch identity {
		case "admin":
			team, err := database.TeamFactory.CreateDefaultTeamIfNotExists()
			if err != nil {
				return AuthHTTPOutcome{}, err
			}
			if err := grantAPIAuthRole(team, accessor.OwnerRole); err != nil {
				return AuthHTTPOutcome{}, err
			}
			authorization, err = persistAPIAuthToken(database, "admin-token", "brine-subject", time.Now().Add(time.Hour))
		case "team-owner":
			team, createErr := database.TeamFactory.CreateTeam(atc.Team{Name: teamName})
			if createErr != nil {
				return AuthHTTPOutcome{}, createErr
			}
			if err := grantAPIAuthRole(team, accessor.OwnerRole); err != nil {
				return AuthHTTPOutcome{}, err
			}
			authorization, err = persistAPIAuthToken(database, "owner-token", "brine-subject", time.Now().Add(time.Hour))
		case "anonymous":
		default:
			return AuthHTTPOutcome{}, fmt.Errorf("unknown admin identity %q", identity)
		}

	case "authentication":
		action = atc.GetUser
		inner = auth.CheckAuthenticationHandler(delegate, rejector)
		switch identity {
		case "valid":
			authorization, err = persistAPIAuthToken(database, "valid-token", "brine-subject", time.Now().Add(time.Hour))
		case "anonymous":
		default:
			return AuthHTTPOutcome{}, fmt.Errorf("unknown authentication identity %q", identity)
		}

	case "authentication-if-provided":
		action = atc.GetSigningKeys
		inner = auth.CheckAuthenticationIfProvidedHandler(delegate, rejector)
		switch identity {
		case "expired":
			authorization, err = persistAPIAuthToken(database, "expired-token", "brine-subject", time.Now().Add(-time.Hour))
		case "valid":
			authorization, err = persistAPIAuthToken(database, "optional-valid-token", "brine-subject", time.Now().Add(time.Hour))
		case "anonymous":
		default:
			return AuthHTTPOutcome{}, fmt.Errorf("unknown optional-auth identity %q", identity)
		}

	case "team-authorization":
		action = atc.ListPipelines
		inner = auth.CheckAuthorizationHandler(delegate, rejector)
		target, createErr := database.TeamFactory.CreateTeam(atc.Team{Name: teamName})
		if createErr != nil {
			return AuthHTTPOutcome{}, createErr
		}
		switch identity {
		case "same-team":
			if err := grantAPIAuthRole(target, accessor.ViewerRole); err != nil {
				return AuthHTTPOutcome{}, err
			}
			authorization, err = persistAPIAuthToken(database, "same-team-token", "brine-subject", time.Now().Add(time.Hour))
		case "other-team":
			other, createErr := database.TeamFactory.CreateTeam(atc.Team{Name: "other-team"})
			if createErr != nil {
				return AuthHTTPOutcome{}, createErr
			}
			if err := grantAPIAuthRole(other, accessor.ViewerRole); err != nil {
				return AuthHTTPOutcome{}, err
			}
			authorization, err = persistAPIAuthToken(database, "other-team-token", "brine-subject", time.Now().Add(time.Hour))
		case "anonymous":
			if err := grantAPIAuthRole(target, accessor.ViewerRole); err != nil {
				return AuthHTTPOutcome{}, err
			}
		default:
			return AuthHTTPOutcome{}, fmt.Errorf("unknown authorization identity %q", identity)
		}
	default:
		return AuthHTTPOutcome{}, fmt.Errorf("unknown auth boundary %q", boundary)
	}
	if err != nil {
		return AuthHTTPOutcome{}, err
	}

	handler := accessor.NewHandler(logger, action, inner, accessFactory, aud, map[string]string{})
	server := httptest.NewServer(handler)
	defer server.Close()
	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		return AuthHTTPOutcome{}, err
	}
	req.URL.RawQuery = url.Values{":team_name": {teamName}}.Encode()
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		return AuthHTTPOutcome{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return AuthHTTPOutcome{}, err
	}
	return AuthHTTPOutcome{Status: resp.StatusCode, Body: string(body)}, nil
}

func grantAPIAuthRole(team db.Team, role string) error {
	return team.UpdateProviderAuth(atc.TeamAuth{
		role: {"users": {brineAuthConnector + ":" + brineAuthUserID}},
	})
}

func makeAPIAuthAdmin(database JetbridgeDB, team db.Team) error {
	if err := grantAPIAuthRole(team, accessor.OwnerRole); err != nil {
		return err
	}
	result, err := database.Conn.Exec(`UPDATE teams SET admin = TRUE WHERE id = $1`, team.ID())
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("mark team %q admin: updated %d rows", team.Name(), rows)
	}
	return nil
}

func persistAPIAuthToken(database JetbridgeDB, token, subject string, expiry time.Time) (string, error) {
	payload, err := json.Marshal(map[string]any{
		"sub": subject, "aud": []any{brineAuthAudience}, "exp": expiry.Unix(),
		"federated_claims": map[string]any{"connector_id": brineAuthConnector, "user_id": brineAuthUserID},
	})
	if err != nil {
		return "", err
	}
	var claims db.Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", err
	}
	if err := db.NewAccessTokenFactory(database.Conn).CreateAccessToken(token, claims); err != nil {
		return "", err
	}
	return "bearer " + token, nil
}
