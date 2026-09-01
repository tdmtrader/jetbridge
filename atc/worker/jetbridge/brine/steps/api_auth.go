package steps

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/idtokenserver"
	"github.com/concourse/concourse/atc/api/pipelineserver"
	"github.com/concourse/concourse/atc/api/usersserver"
	"github.com/concourse/concourse/atc/auditor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/skymarshal/skycmd"
	"github.com/tedsuo/rata"
)

const (
	brineAuthAudience     = "brine-audience"
	brineAuthConnector    = "brine-connector"
	brineAuthUserID       = "brine-user"
	brineAuthSubject      = "brine-subject"
	brineAuthTeam         = "some-team"
	brineAuthPipelineName = "auth-pipeline"
)

type AuthHTTPOutcome struct {
	Status      int
	Body        string
	ContentType string
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
		CheckString[AuthHTTPOutcome]("the auth response content type is {string}", "the auth response content type",
			func(in AuthHTTPOutcome) (string, error) { return in.ContentType, nil }),
		brine.DefineCheck[AuthHTTPOutcome](
			"the auth response is the exact empty active-users document",
			func(in AuthHTTPOutcome, _ brine.Params, _ *brine.Recorder) error {
				if in.Body != "[]\n" {
					return fmt.Errorf("active-users response: got %q, want exact empty JSON array with newline", in.Body)
				}
				return nil
			},
		),
		brine.DefineCheck[AuthHTTPOutcome](
			"the auth response is the exact empty signing-keys document",
			func(in AuthHTTPOutcome, _ brine.Params, _ *brine.Recorder) error {
				if in.Body != "{\"keys\":[]}\n" {
					return fmt.Errorf("signing-keys response: got %q, want exact empty JWKS JSON with newline", in.Body)
				}
				return nil
			},
		),
		brine.DefineCheck[AuthHTTPOutcome](
			"the auth response identifies subject {string}",
			func(in AuthHTTPOutcome, p brine.Params, _ *brine.Recorder) error {
				expected, err := paramAt("the auth response identifies subject {string}", p, 0)
				if err != nil {
					return err
				}
				var user atc.UserInfo
				if err := json.Unmarshal([]byte(in.Body), &user); err != nil {
					return fmt.Errorf("decode production user response: %w", err)
				}
				if user.Sub != expected {
					return fmt.Errorf("auth response subject: got %q, want %q", user.Sub, expected)
				}
				return nil
			},
		),
		brine.DefineCheck[AuthHTTPOutcome](
			"the auth response lists pipeline {string}",
			func(in AuthHTTPOutcome, p brine.Params, _ *brine.Recorder) error {
				expected, err := paramAt("the auth response lists pipeline {string}", p, 0)
				if err != nil {
					return err
				}
				var pipelines []atc.Pipeline
				if err := json.Unmarshal([]byte(in.Body), &pipelines); err != nil {
					return fmt.Errorf("decode production pipeline response: %w", err)
				}
				if len(pipelines) != 1 || pipelines[0].Name != expected || pipelines[0].TeamName != brineAuthTeam {
					return fmt.Errorf("auth response pipelines: got %#v, want sole %q pipeline on %q", pipelines, expected, brineAuthTeam)
				}
				return nil
			},
		),
	}
}

func exerciseAPIAuthBoundary(database JetbridgeDB, boundary, identity string) (AuthHTTPOutcome, error) {
	logger := lager.NewLogger("brine-api-auth")
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

	var guarded http.Handler
	var action string
	var path string
	authorization := ""

	switch boundary {
	case "admin":
		action = atc.ListActiveUsersSince
		path = "/api/v1/users"
		users := usersserver.NewServer(logger, db.NewUserFactory(database.Conn))
		guarded = auth.CheckAdminHandler(http.HandlerFunc(users.GetUsersSince), rejector)
		switch identity {
		case "admin":
			team, createErr := database.TeamFactory.CreateTeam(atc.Team{Name: brineAuthTeam})
			if createErr != nil {
				return AuthHTTPOutcome{}, createErr
			}
			if err := makeAPIAuthAdmin(database, team); err != nil {
				return AuthHTTPOutcome{}, err
			}
			authorization, err = persistAPIAuthToken(database, "admin-token", brineAuthSubject, time.Now().Add(time.Hour))
		case "team-owner":
			team, createErr := database.TeamFactory.CreateTeam(atc.Team{Name: brineAuthTeam})
			if createErr != nil {
				return AuthHTTPOutcome{}, createErr
			}
			if err := grantAPIAuthRole(team, accessor.OwnerRole); err != nil {
				return AuthHTTPOutcome{}, err
			}
			authorization, err = persistAPIAuthToken(database, "owner-token", brineAuthSubject, time.Now().Add(time.Hour))
		case "anonymous":
		default:
			return AuthHTTPOutcome{}, fmt.Errorf("unknown admin identity %q", identity)
		}

	case "authentication":
		action = atc.GetUser
		path = "/api/v1/user"
		users := usersserver.NewServer(logger, db.NewUserFactory(database.Conn))
		guarded = auth.CheckAuthenticationHandler(http.HandlerFunc(users.GetUser), rejector)
		switch identity {
		case "valid":
			authorization, err = persistAPIAuthToken(database, "valid-token", brineAuthSubject, time.Now().Add(time.Hour))
		case "anonymous":
		default:
			return AuthHTTPOutcome{}, fmt.Errorf("unknown authentication identity %q", identity)
		}

	case "authentication-if-provided":
		action = atc.GetSigningKeys
		path = "/.well-known/jwks.json"
		identityTokens := idtokenserver.NewServer(logger, "https://concourse.invalid", db.NewSigningKeyFactory(database.Conn))
		guarded = auth.CheckAuthenticationIfProvidedHandler(http.HandlerFunc(identityTokens.SigningKeys), rejector)
		switch identity {
		case "expired":
			authorization, err = persistAPIAuthToken(database, "expired-token", brineAuthSubject, time.Now().Add(-time.Hour))
		case "valid":
			authorization, err = persistAPIAuthToken(database, "optional-valid-token", brineAuthSubject, time.Now().Add(time.Hour))
		case "anonymous":
		default:
			return AuthHTTPOutcome{}, fmt.Errorf("unknown optional-auth identity %q", identity)
		}

	case "team-authorization":
		action = atc.ListPipelines
		path = "/api/v1/teams/" + brineAuthTeam + "/pipelines"
		target, createErr := database.TeamFactory.CreateTeam(atc.Team{Name: brineAuthTeam})
		if createErr != nil {
			return AuthHTTPOutcome{}, createErr
		}
		if _, _, err := target.SavePipeline(
			atc.PipelineRef{Name: brineAuthPipelineName}, atc.Config{}, db.ConfigVersion(1), false,
		); err != nil {
			return AuthHTTPOutcome{}, fmt.Errorf("save production auth pipeline: %w", err)
		}
		pipelines := pipelineserver.NewServer(
			logger, database.TeamFactory, db.NewPipelineFactory(database.Conn, database.LockFactory), "https://concourse.invalid",
		)
		guarded = auth.CheckAuthorizationHandler(http.HandlerFunc(pipelines.ListPipelines), rejector)
		switch identity {
		case "same-team":
			if err := grantAPIAuthRole(target, accessor.ViewerRole); err != nil {
				return AuthHTTPOutcome{}, err
			}
			authorization, err = persistAPIAuthToken(database, "same-team-token", brineAuthSubject, time.Now().Add(time.Hour))
		case "other-team":
			other, createErr := database.TeamFactory.CreateTeam(atc.Team{Name: "other-team"})
			if createErr != nil {
				return AuthHTTPOutcome{}, createErr
			}
			if err := grantAPIAuthRole(other, accessor.ViewerRole); err != nil {
				return AuthHTTPOutcome{}, err
			}
			authorization, err = persistAPIAuthToken(database, "other-team-token", brineAuthSubject, time.Now().Add(time.Hour))
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

	handler := accessor.NewHandler(logger, action, guarded, accessFactory, aud, map[string]string{})
	router, err := selectedAPIAuthRouter(action, handler)
	if err != nil {
		return AuthHTTPOutcome{}, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return AuthHTTPOutcome{}, fmt.Errorf("listen for production auth HTTP server: %w", err)
	}
	server := &http.Server{Handler: router, ReadHeaderTimeout: 5 * time.Second}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	defer func() {
		_ = server.Close()
		<-serveDone
	}()

	req, err := http.NewRequest(http.MethodGet, "http://"+listener.Addr().String()+path, nil)
	if err != nil {
		return AuthHTTPOutcome{}, err
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return AuthHTTPOutcome{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return AuthHTTPOutcome{}, err
	}
	return AuthHTTPOutcome{
		Status: resp.StatusCode, Body: string(body), ContentType: resp.Header.Get("Content-Type"),
	}, nil
}

func selectedAPIAuthRouter(action string, handler http.Handler) (http.Handler, error) {
	var routes rata.Routes
	for _, route := range atc.Routes {
		if route.Name == action {
			routes = append(routes, route)
		}
	}
	if len(routes) != 1 {
		return nil, fmt.Errorf("production route %q matched %d routes", action, len(routes))
	}
	return rata.NewRouter(routes, rata.Handlers{action: handler})
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
