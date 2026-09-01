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
	"github.com/concourse/concourse/atc/api/usersserver"
	"github.com/concourse/concourse/atc/auditor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/skymarshal/skycmd"
	"github.com/tedsuo/rata"
)

const (
	usersAPIAudience  = "brine-users-audience"
	usersAPIConnector = "some-connector"
	usersAPIUserID    = "some-user-id"
	usersAPISubject   = "some-sub"
)

type UsersAPIObservation struct {
	Status       int
	ContentType  string
	Body         string
	Current      atc.UserInfo
	Users        []atc.User
	ExpectedUser atc.User
}

func UsersAPIDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, UsersAPIObservation](
			"the production users API handles profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (UsersAPIObservation, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return UsersAPIObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				profile, err := paramAt("the production users API handles profile {string}", p, 0)
				if err != nil {
					return UsersAPIObservation{}, err
				}
				return observeUsersAPI(database, profile)
			},
		),
		CheckInt[UsersAPIObservation]("the users API returned status {int}", "users API status",
			func(in UsersAPIObservation) (int, error) { return in.Status, nil }),
		CheckString[UsersAPIObservation]("the users API content type is {string}", "users API content type",
			func(in UsersAPIObservation) (string, error) { return in.ContentType, nil }),
		brine.DefineCheck[UsersAPIObservation](
			"the users API returns the exact persisted identity",
			func(in UsersAPIObservation, _ brine.Params, _ *brine.Recorder) error {
				expected := atc.UserInfo{
					Sub: usersAPISubject, Name: "some-name", UserId: usersAPIUserID,
					UserName: "some-user-name", Email: "some@email.com", IsAdmin: true,
					IsSystem: false,
					Teams: map[string][]string{
						"some-team":       {accessor.OwnerRole},
						"some-other-team": {accessor.ViewerRole},
					},
					Connector: usersAPIConnector, DisplayUserId: "some-user-name",
				}
				actualJSON, _ := json.Marshal(in.Current)
				expectedJSON, _ := json.Marshal(expected)
				if string(actualJSON) != string(expectedJSON) {
					return fmt.Errorf("current users API identity: got %s, want %s", actualJSON, expectedJSON)
				}
				return nil
			},
		),
		brine.DefineCheck[UsersAPIObservation](
			"the users API returns the exact empty JSON array",
			func(in UsersAPIObservation, _ brine.Params, _ *brine.Recorder) error {
				if in.Body != "[]\n" || len(in.Users) != 0 {
					return fmt.Errorf("users API empty result: body=%q decoded=%#v", in.Body, in.Users)
				}
				return nil
			},
		),
		brine.DefineCheck[UsersAPIObservation](
			"the users API returns the exact persisted user metadata",
			func(in UsersAPIObservation, _ brine.Params, _ *brine.Recorder) error {
				if len(in.Users) != 1 || in.Users[0] != in.ExpectedUser {
					return fmt.Errorf("users API metadata: got %#v, want sole %#v", in.Users, in.ExpectedUser)
				}
				return nil
			},
		),
		brine.DefineCheck[UsersAPIObservation](
			"the users API returns the exact invalid-date document",
			func(in UsersAPIObservation, _ brine.Params, _ *brine.Recorder) error {
				if in.Body != "{\"error\":\"wrong date format (yyyy-mm-dd)\"}\n" {
					return fmt.Errorf("users API invalid-date document: got %q", in.Body)
				}
				return nil
			},
		),
	}
}

func observeUsersAPI(database JetbridgeDB, profile string) (UsersAPIObservation, error) {
	logger := lager.NewLogger("brine-users-api")
	users := usersserver.NewServer(logger, db.NewUserFactory(database.Conn))
	authorization, err := prepareUsersAPIIdentity(database)
	if err != nil {
		return UsersAPIObservation{}, err
	}

	action := atc.ListActiveUsersSince
	path := "/api/v1/users"
	guarded := auth.CheckAdminHandler(http.HandlerFunc(users.GetUsersSince), auth.UnauthorizedRejector{})
	if profile == "current" {
		action = atc.GetUser
		path = "/api/v1/user"
		guarded = auth.CheckAuthenticationHandler(http.HandlerFunc(users.GetUser), auth.UnauthorizedRejector{})
	} else {
		if err := validateUsersAPIProfile(profile); err != nil {
			return UsersAPIObservation{}, err
		}
	}

	observation := UsersAPIObservation{}
	userFactory := db.NewUserFactory(database.Conn)
	if profile == "list-user" || profile == "since-past" || profile == "since-future" {
		if err := userFactory.CreateOrUpdateUser("bob", "github", "bob-sub"); err != nil {
			return observation, fmt.Errorf("persist users API login: %w", err)
		}
		stored, err := userFactory.GetAllUsers()
		if err != nil {
			return observation, fmt.Errorf("reload users API login: %w", err)
		}
		if len(stored) != 1 {
			return observation, fmt.Errorf("reload users API login: got %d rows, want 1", len(stored))
		}
		observation.ExpectedUser = atc.User{
			ID: stored[0].ID(), Username: stored[0].Name(), Connector: stored[0].Connector(),
			LastLogin: stored[0].LastLogin().Unix(),
		}
	}

	switch profile {
	case "since-past":
		path += "?since=1969-12-30"
	case "since-future":
		path += "?since=" + time.Now().UTC().AddDate(0, 0, 2).Format("2006-01-02")
	case "since-invalid":
		path += "?since=1969-14-30"
	case "since-empty":
		path += "?since="
	}

	display, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{})
	if err != nil {
		return observation, err
	}
	accessFactory := accessor.NewAccessFactory(
		accessor.NewVerifier(db.NewAccessTokenFactory(database.Conn), []string{usersAPIAudience}),
		database.TeamFactory, "sub", []string{"brine-system"}, display,
	)
	aud := auditor.NewAuditor(true, true, true, true, true, true, true, true, true, logger)
	handler := accessor.NewHandler(logger, action, guarded, accessFactory, aud, map[string]string{})
	router, err := usersAPIRouter(action, handler)
	if err != nil {
		return observation, err
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return observation, fmt.Errorf("listen for production users HTTP server: %w", err)
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
		return observation, err
	}
	req.Header.Set("Authorization", authorization)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return observation, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return observation, err
	}
	observation.Status = resp.StatusCode
	observation.ContentType = resp.Header.Get("Content-Type")
	observation.Body = string(body)
	if profile == "current" && resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, &observation.Current); err != nil {
			return observation, fmt.Errorf("decode current users API response: %w", err)
		}
	} else if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, &observation.Users); err != nil {
			return observation, fmt.Errorf("decode users API response: %w", err)
		}
	}
	return observation, nil
}

func validateUsersAPIProfile(profile string) error {
	switch profile {
	case "list-empty", "list-user", "since-past", "since-future", "since-invalid", "since-empty":
		return nil
	default:
		return fmt.Errorf("unknown users API profile %q", profile)
	}
}

func usersAPIRouter(action string, handler http.Handler) (http.Handler, error) {
	var routes rata.Routes
	for _, route := range atc.Routes {
		if route.Name == action {
			routes = append(routes, route)
		}
	}
	if len(routes) != 1 {
		return nil, fmt.Errorf("production users route %q matched %d routes", action, len(routes))
	}
	return rata.NewRouter(routes, rata.Handlers{action: handler})
}

func prepareUsersAPIIdentity(database JetbridgeDB) (string, error) {
	adminTeam, err := database.TeamFactory.CreateTeam(atc.Team{Name: "some-team"})
	if err != nil {
		return "", err
	}
	if err := adminTeam.UpdateProviderAuth(atc.TeamAuth{
		accessor.OwnerRole: {"users": {usersAPIConnector + ":" + usersAPIUserID}},
	}); err != nil {
		return "", err
	}
	if _, err := database.Conn.Exec(`UPDATE teams SET admin = TRUE WHERE id = $1`, adminTeam.ID()); err != nil {
		return "", err
	}
	viewerTeam, err := database.TeamFactory.CreateTeam(atc.Team{Name: "some-other-team"})
	if err != nil {
		return "", err
	}
	if err := viewerTeam.UpdateProviderAuth(atc.TeamAuth{
		accessor.ViewerRole: {"users": {usersAPIConnector + ":" + usersAPIUserID}},
	}); err != nil {
		return "", err
	}

	payload, err := json.Marshal(map[string]any{
		"sub": usersAPISubject, "name": "some-name", "preferred_username": "some-user-name",
		"email": "some@email.com", "aud": []any{usersAPIAudience}, "exp": time.Now().Add(time.Hour).Unix(),
		"federated_claims": map[string]any{"connector_id": usersAPIConnector, "user_id": usersAPIUserID},
	})
	if err != nil {
		return "", err
	}
	var claims db.Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", err
	}
	if err := db.NewAccessTokenFactory(database.Conn).CreateAccessToken("brine-users-token", claims); err != nil {
		return "", err
	}
	return "bearer brine-users-token", nil
}
