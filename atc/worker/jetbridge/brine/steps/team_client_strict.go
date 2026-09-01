package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/teamserver"
	"github.com/concourse/concourse/atc/auditor"
	"github.com/concourse/concourse/atc/db"
	clientapi "github.com/concourse/concourse/go-concourse/concourse"
	"github.com/tedsuo/rata"
	"golang.org/x/oauth2"
)

type strictTeamObservation struct {
	Value string
}

type strictTeamHTTPServer struct {
	baseURL string
	server  *http.Server
	done    chan error
	client  *http.Client
}

func TeamClientStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, strictTeamObservation](
			"strict team client profile {string} is exercised over real HTTP",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (strictTeamObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return strictTeamObservation{}, fmt.Errorf("strict team client profile is not a string")
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return strictTeamObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				value, err := observeStrictTeamClient(database, profile)
				return strictTeamObservation{Value: value}, err
			},
		),
		brine.DefineMapUsing[brine.Empty, strictTeamObservation](
			"strict team API profile {string} is exercised over real HTTP",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (strictTeamObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return strictTeamObservation{}, fmt.Errorf("strict team API profile is not a string")
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return strictTeamObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				value, err := observeStrictTeamAPI(database, profile)
				return strictTeamObservation{Value: value}, err
			},
		),
		CheckString[strictTeamObservation](
			"the strict team observation is {string}",
			"strict team observation",
			func(in strictTeamObservation) (string, error) { return in.Value, nil },
		),
	}
}

func observeStrictTeamClient(database JetbridgeDB, profile string) (string, error) {
	admin := profile == "find-missing" || profile == "find-not-belonging-404" ||
		profile == "create-returned-team" || profile == "create-flags" ||
		profile == "create-warning" || profile == "destroy-success"
	authorization, err := prepareStrictTeamIdentity(database, "client-"+profile, admin)
	if err != nil {
		return "", err
	}

	switch profile {
	case "list-teams":
		for _, name := range []string{"alpha", "beta"} {
			if _, err := createStrictTeam(database, name, strictTeamAuth()); err != nil {
				return "", err
			}
		}
	case "find-found":
		if _, err := createStrictTeam(database, "target", strictTeamAuth()); err != nil {
			return "", err
		}
	case "update-flags":
		if _, err := createStrictTeam(database, "target", strictTeamAuth()); err != nil {
			return "", err
		}
	case "destroy-success":
		if _, err := createStrictTeam(database, "target", strictTeamAuth()); err != nil {
			return "", err
		}
	case "find-missing", "find-not-belonging-404", "create-returned-team", "create-flags", "create-warning":
	default:
		return "", fmt.Errorf("unknown strict team client profile %q", profile)
	}

	harness, err := newStrictTeamHTTPServer(database)
	if err != nil {
		return "", err
	}
	defer harness.close()
	client := clientapi.NewClient(harness.baseURL, strictTeamOAuthClient(authorization), false)

	switch profile {
	case "list-teams":
		teams, err := client.ListTeams()
		if err != nil {
			return "", fmt.Errorf("strict team client ListTeams: %w", err)
		}
		if err := validateStrictReturnedTeams(database, teams, []string{"alpha", "beta", "brine-access"}); err != nil {
			return "", err
		}
		return "teams=alpha,beta,brine-access;exact=true", nil
	case "find-found":
		team, err := client.FindTeam("target")
		if err != nil {
			return "", fmt.Errorf("strict team client FindTeam: %w", err)
		}
		if team.Name() != "target" || !reflect.DeepEqual(team.Auth(), strictTeamAuth()) {
			return "", fmt.Errorf("strict team client returned name=%q auth=%v", team.Name(), team.Auth())
		}
		return "name=target;auth=owner", nil
	case "find-missing":
		_, err := client.FindTeam("missing")
		return strictTeamExactError(err, "team 'missing' does not exist")
	case "find-not-belonging-404":
		_, err := client.FindTeam("not-belonging")
		return strictTeamExactError(err, "team 'not-belonging' does not exist")
	case "create-returned-team":
		returned, _, _, _, err := client.Team("target").CreateOrUpdate(atc.Team{Auth: strictTeamAuth()})
		if err != nil {
			return "", fmt.Errorf("strict team client create: %w", err)
		}
		persisted, found, err := database.TeamFactory.FindTeam("target")
		if err != nil || !found {
			return "", fmt.Errorf("strict team persisted target found=%t err=%v", found, err)
		}
		want := atc.Team{ID: persisted.ID(), Name: persisted.Name(), Auth: persisted.Auth()}
		if !reflect.DeepEqual(returned, want) {
			return "", fmt.Errorf("strict team client returned %#v, want %#v", returned, want)
		}
		return "name=target;auth=owner;id=persisted", nil
	case "create-flags":
		_, created, updated, _, err := client.Team("target").CreateOrUpdate(atc.Team{Auth: strictTeamAuth()})
		if err != nil {
			return "", fmt.Errorf("strict team client create flags: %w", err)
		}
		return fmt.Sprintf("created=%t;updated=%t", created, updated), nil
	case "update-flags":
		_, created, updated, _, err := client.Team("target").CreateOrUpdate(atc.Team{Auth: strictTeamUpdatedAuth()})
		if err != nil {
			return "", fmt.Errorf("strict team client update flags: %w", err)
		}
		return fmt.Sprintf("created=%t;updated=%t", created, updated), nil
	case "create-warning":
		_, created, updated, warnings, err := client.Team("_warning").CreateOrUpdate(atc.Team{Auth: strictTeamAuth()})
		if err != nil {
			return "", fmt.Errorf("strict team client warning: %w", err)
		}
		if len(warnings) != 1 {
			return "", fmt.Errorf("strict team client got %d warnings", len(warnings))
		}
		return fmt.Sprintf("created=%t;updated=%t;type=%s;message=%s", created, updated, warnings[0].Type, warnings[0].Message), nil
	case "destroy-success":
		if err := client.Team("brine-admin").DestroyTeam("target"); err != nil {
			return "", fmt.Errorf("strict team client destroy: %w", err)
		}
		return "error=nil", nil
	default:
		return "", fmt.Errorf("unknown strict team client profile %q", profile)
	}
}

func observeStrictTeamAPI(database JetbridgeDB, profile string) (string, error) {
	admin := profile == "create-persisted" || profile == "warning-persisted" ||
		profile == "delete-persisted" || profile == "delete-missing"
	authorization, err := prepareStrictTeamIdentity(database, "api-"+profile, admin)
	if err != nil {
		return "", err
	}

	switch profile {
	case "list-authorized":
		if _, err := createStrictTeam(database, "alpha", strictTeamAuth()); err != nil {
			return "", err
		}
		if _, err := createStrictTeam(database, "beta", atc.TeamAuth{accessor.OwnerRole: {"users": {"someone-else"}}}); err != nil {
			return "", err
		}
	case "get-persisted", "update-auth":
		if _, err := createStrictTeam(database, "target", strictTeamAuth()); err != nil {
			return "", err
		}
	case "delete-persisted":
		if _, err := createStrictTeam(database, "target", strictTeamAuth()); err != nil {
			return "", err
		}
	case "create-persisted", "warning-persisted", "delete-missing":
	default:
		return "", fmt.Errorf("unknown strict team API profile %q", profile)
	}

	harness, err := newStrictTeamHTTPServer(database)
	if err != nil {
		return "", err
	}
	defer harness.close()

	switch profile {
	case "list-authorized":
		status, contentType, body, err := harness.request(authorization, atc.ListTeams, nil, nil)
		if err != nil {
			return "", err
		}
		var teams []atc.Team
		if err := json.Unmarshal(body, &teams); err != nil {
			return "", fmt.Errorf("strict team API list decode: %w", err)
		}
		if err := validateStrictReturnedTeams(database, teams, []string{"alpha", "brine-access"}); err != nil {
			return "", err
		}
		return fmt.Sprintf("status=%d;content-type=%s;teams=alpha,brine-access;exact=true", status, contentType), nil
	case "get-persisted":
		status, contentType, body, err := harness.request(authorization, atc.GetTeam, rata.Params{"team_name": "target"}, nil)
		if err != nil {
			return "", err
		}
		var returned atc.Team
		if err := json.Unmarshal(body, &returned); err != nil {
			return "", fmt.Errorf("strict team API get decode: %w", err)
		}
		persisted, found, err := database.TeamFactory.FindTeam("target")
		if err != nil || !found {
			return "", fmt.Errorf("strict team API target found=%t err=%v", found, err)
		}
		want := atc.Team{ID: persisted.ID(), Name: persisted.Name(), Auth: persisted.Auth()}
		if !reflect.DeepEqual(returned, want) {
			return "", fmt.Errorf("strict team API returned %#v, want %#v", returned, want)
		}
		return fmt.Sprintf("status=%d;content-type=%s;name=target;auth=owner;id=persisted", status, contentType), nil
	case "create-persisted", "warning-persisted":
		name := "target"
		if profile == "warning-persisted" {
			name = "_warning"
		}
		requested := atc.Team{Name: name, Auth: strictTeamAuth()}
		payload, err := json.Marshal(atc.Team{Auth: requested.Auth})
		if err != nil {
			return "", err
		}
		status, contentType, body, err := harness.request(authorization, atc.SetTeam, rata.Params{"team_name": name}, payload)
		if err != nil {
			return "", err
		}
		var response teamserver.SetTeamResponse
		if err := json.Unmarshal(body, &response); err != nil {
			return "", fmt.Errorf("strict team API set decode: %w", err)
		}
		persisted, found, err := database.TeamFactory.FindTeam(name)
		if err != nil || !found {
			return "", fmt.Errorf("strict team API created %q found=%t err=%v", name, found, err)
		}
		want := atc.Team{ID: persisted.ID(), Name: requested.Name, Auth: requested.Auth}
		if persisted.Name() != requested.Name || !reflect.DeepEqual(persisted.Auth(), requested.Auth) {
			return "", fmt.Errorf("strict team API persisted name=%q auth=%v, want name=%q auth=%v", persisted.Name(), persisted.Auth(), requested.Name, requested.Auth)
		}
		if !reflect.DeepEqual(response.Team, want) {
			return "", fmt.Errorf("strict team API response team %#v, want %#v", response.Team, want)
		}
		if profile == "create-persisted" {
			if len(response.Warnings) != 0 {
				return "", fmt.Errorf("strict team API create returned warnings %v", response.Warnings)
			}
			return fmt.Sprintf("status=%d;content-type=%s;name=target;auth=owner;id=persisted", status, contentType), nil
		}
		if len(response.Warnings) != 1 {
			return "", fmt.Errorf("strict team API warning count %d", len(response.Warnings))
		}
		return fmt.Sprintf("status=%d;type=%s;message=%s;name=_warning;id=persisted", status, response.Warnings[0].Type, response.Warnings[0].Message), nil
	case "update-auth":
		payload, err := json.Marshal(atc.Team{Auth: strictTeamUpdatedAuth()})
		if err != nil {
			return "", err
		}
		status, _, _, err := harness.request(authorization, atc.SetTeam, rata.Params{"team_name": "target"}, payload)
		if err != nil {
			return "", err
		}
		persisted, found, err := database.TeamFactory.FindTeam("target")
		if err != nil || !found {
			return "", fmt.Errorf("strict team API updated target found=%t err=%v", found, err)
		}
		if !reflect.DeepEqual(persisted.Auth(), strictTeamUpdatedAuth()) {
			return "", fmt.Errorf("strict team API persisted auth %v", persisted.Auth())
		}
		return fmt.Sprintf("status=%d;auth=owner-user-and-new-group", status), nil
	case "delete-persisted":
		status, _, _, err := harness.request(authorization, atc.DestroyTeam, rata.Params{"team_name": "target"}, nil)
		if err != nil {
			return "", err
		}
		team, found, err := database.TeamFactory.FindTeam("target")
		if err != nil {
			return "", err
		}
		if found || team != nil {
			return "", fmt.Errorf("strict team API target still exists")
		}
		return fmt.Sprintf("status=%d;persisted=absent", status), nil
	case "delete-missing":
		status, _, _, err := harness.request(authorization, atc.DestroyTeam, rata.Params{"team_name": "missing"}, nil)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("status=%d", status), nil
	default:
		return "", fmt.Errorf("unknown strict team API profile %q", profile)
	}
}

func newStrictTeamHTTPServer(database JetbridgeDB) (*strictTeamHTTPServer, error) {
	logger := lager.NewLogger("brine-team-strict")
	accessFactory, err := strictResourceAccessFactory(database)
	if err != nil {
		return nil, err
	}
	aud := auditor.NewAuditor(true, true, true, true, true, true, true, true, true, logger)
	teamServer := teamserver.NewServer(logger, database.TeamFactory, "https://example.com")
	teamScoped := api.NewTeamScopedHandlerFactory(logger, database.TeamFactory)
	rejector := auth.UnauthorizedRejector{}

	handlers := rata.Handlers{
		atc.ListTeams:   http.HandlerFunc(teamServer.ListTeams),
		atc.GetTeam:     teamScoped.HandlerFor(teamServer.GetTeam),
		atc.SetTeam:     http.HandlerFunc(teamServer.SetTeam),
		atc.DestroyTeam: teamScoped.HandlerFor(teamServer.DestroyTeam),
	}
	for action, handler := range handlers {
		switch action {
		case atc.ListTeams:
			handler = auth.CheckAuthenticationIfProvidedHandler(handler, rejector)
		case atc.GetTeam, atc.SetTeam:
			handler = auth.CheckAuthorizationHandler(handler, rejector)
		case atc.DestroyTeam:
			handler = auth.CheckAdminHandler(handler, rejector)
		}
		handlers[action] = accessor.NewHandler(logger, action, handler, accessFactory, aud, map[string]string{})
	}

	routes := rata.Routes{}
	for _, route := range atc.Routes {
		if _, ok := handlers[route.Name]; ok {
			routes = append(routes, route)
		}
	}
	if len(routes) != len(handlers) {
		return nil, fmt.Errorf("strict team API matched %d routes for %d handlers", len(routes), len(handlers))
	}
	router, err := rata.NewRouter(routes, handlers)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	server := &http.Server{Handler: router, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	return &strictTeamHTTPServer{
		baseURL: "http://" + listener.Addr().String(),
		server:  server,
		done:    done,
		client:  &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func (server *strictTeamHTTPServer) close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shutdownErr := server.server.Shutdown(ctx)
	serveErr := <-server.done
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return shutdownErr
}

func (server *strictTeamHTTPServer) request(authorization, action string, params rata.Params, body []byte) (int, string, []byte, error) {
	generator := rata.NewRequestGenerator(server.baseURL, atc.Routes)
	request, err := generator.CreateRequest(action, params, bytes.NewReader(body))
	if err != nil {
		return 0, "", nil, err
	}
	request.Header.Set("Authorization", authorization)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := server.client.Do(request)
	if err != nil {
		return 0, "", nil, err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, "", nil, err
	}
	return response.StatusCode, response.Header.Get("Content-Type"), responseBody, nil
}

func strictTeamOAuthClient(authorization string) *http.Client {
	_, token, _ := strings.Cut(authorization, " ")
	client := oauth2.NewClient(context.Background(), oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken: token,
		TokenType:   "Bearer",
	}))
	client.Timeout = 10 * time.Second
	return client
}

func prepareStrictTeamIdentity(database JetbridgeDB, profile string, admin bool) (string, error) {
	name := "brine-access"
	if admin {
		name = "brine-admin"
	}
	team, err := createStrictTeam(database, name, strictTeamAuth())
	if err != nil {
		return "", err
	}
	if admin {
		if err := makeAPIAuthAdmin(database, team); err != nil {
			return "", err
		}
	}
	return persistAPIAuthToken(database, "strict-team-"+profile, brineAuthSubject, time.Now().Add(time.Hour))
}

func createStrictTeam(database JetbridgeDB, name string, teamAuth atc.TeamAuth) (db.Team, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: name, Auth: teamAuth})
	if err != nil {
		return nil, fmt.Errorf("create strict team %q: %w", name, err)
	}
	return team, nil
}

func strictTeamAuth() atc.TeamAuth {
	return atc.TeamAuth{
		accessor.OwnerRole: {"users": {brineAuthConnector + ":" + brineAuthUserID}},
	}
}

func strictTeamUpdatedAuth() atc.TeamAuth {
	return atc.TeamAuth{
		accessor.OwnerRole: {
			"users":  {brineAuthConnector + ":" + brineAuthUserID},
			"groups": {"new-group"},
		},
	}
}

func strictTeamExactError(err error, want string) (string, error) {
	if err == nil || err.Error() != want {
		return "", fmt.Errorf("strict team client error=%v, want %q", err, want)
	}
	return "error=" + want, nil
}

func validateStrictReturnedTeams(database JetbridgeDB, teams []atc.Team, names []string) error {
	sort.Strings(names)
	if len(teams) != len(names) {
		return fmt.Errorf("strict team list length=%d, want %d", len(teams), len(names))
	}
	sort.Slice(teams, func(i, j int) bool { return teams[i].Name < teams[j].Name })
	for index, name := range names {
		persisted, found, err := database.TeamFactory.FindTeam(name)
		if err != nil || !found {
			return fmt.Errorf("strict team %q found=%t err=%v", name, found, err)
		}
		want := atc.Team{ID: persisted.ID(), Name: persisted.Name(), Auth: persisted.Auth()}
		if !reflect.DeepEqual(teams[index], want) {
			return fmt.Errorf("strict team list[%d]=%#v, want %#v", index, teams[index], want)
		}
	}
	return nil
}
