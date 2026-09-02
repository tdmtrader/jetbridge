package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/brine-dev/brine-go/pkg/brine"
	conc "github.com/concourse/concourse"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/teamserver"
	"github.com/concourse/concourse/atc/auditor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/fly/rc"
	clientapi "github.com/concourse/concourse/go-concourse/concourse"
	"github.com/concourse/concourse/skymarshal/skycmd"
	"github.com/tedsuo/rata"
	"golang.org/x/oauth2"
)

type FlyTeamError struct {
	Home     string
	Fly      string
	URL      string
	Token    string
	TeamName string
	Kind     string
	ExitCode int
	Stderr   string
}

func FlyTeamErrorDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, *FlyTeamError](
			"fly targets a team the real API reports as {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, rec *brine.Recorder, resources brine.Resources) (*FlyTeamError, error) {
				kind, err := paramAt("fly targets a team the real API reports as {string}", p, 0)
				if err != nil {
					return nil, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return nil, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				return newFlyTeamError(database, kind, rec)
			},
		),

		brine.DefineMap[*FlyTeamError, *FlyTeamError](
			"fly runs the {string} command with that team",
			func(in *FlyTeamError, p brine.Params, _ *brine.Recorder) (*FlyTeamError, error) {
				name, err := paramAt("fly runs the {string} command with that team", p, 0)
				if err != nil {
					return in, err
				}
				args, err := flyTeamCommand(name, in.TeamName)
				if err != nil {
					return in, err
				}
				cmd := exec.Command(in.Fly, append([]string{"-t", "brine"}, args...)...)
				cmd.Dir = filepath.Clean(filepath.Join(filepath.Dir(in.Fly), "../../../../.."))
				cmd.Env = flyEnvironment(in.Home)
				output, runErr := cmd.CombinedOutput()
				in.Stderr = string(output)
				in.ExitCode = 0
				if runErr != nil {
					var exitErr *exec.ExitError
					if !strings.Contains(runErr.Error(), "exit status") || !asExitError(runErr, &exitErr) {
						return in, fmt.Errorf("run fly %s: %w: %s", name, runErr, output)
					}
					in.ExitCode = exitErr.ExitCode()
				}
				return in, nil
			},
		),

		brine.DefineCheck[*FlyTeamError](
			"fly exits once with the matching team error",
			func(in *FlyTeamError, _ brine.Params, _ *brine.Recorder) error {
				if in.ExitCode != 1 {
					return fmt.Errorf("expected fly exit 1, got %d: %s", in.ExitCode, in.Stderr)
				}
				want := fmt.Sprintf("team '%s' does not exist", in.TeamName)
				if in.Kind == "forbidden" {
					want = fmt.Sprintf("you do not have a role on team '%s'", in.TeamName)
				}
				if !strings.Contains(in.Stderr, want) {
					return fmt.Errorf("expected fly error to contain %q, got %q", want, in.Stderr)
				}
				// Exercise the same production client branch in-process as well. The
				// mutation audit overlays this adapter, while the separately built fly
				// process proves that every command actually reaches that branch.
				httpClient := oauth2.NewClient(context.Background(), oauth2.StaticTokenSource(&oauth2.Token{
					AccessToken: in.Token,
					TokenType:   "Bearer",
				}))
				httpClient.Timeout = 30 * time.Second
				defer httpClient.CloseIdleConnections()
				_, err := clientapi.NewClient(in.URL, httpClient, false).FindTeam(in.TeamName)
				if err == nil || err.Error() != want {
					return fmt.Errorf("production FindTeam error = %v, want %q", err, want)
				}
				return nil
			},
		),
	}
}

func newFlyTeamError(database JetbridgeDB, kind string, rec *brine.Recorder) (*FlyTeamError, error) {
	logger := lager.NewLogger("brine-fly-team")
	flyBinary, err := productionFlyBinary()
	if err != nil {
		return nil, err
	}
	const (
		token    = "brine-fly-team-token"
		audience = "brine-fly-team-audience"
		user     = "brine-fly-team-user"
	)
	teamName := "doesnotexist"
	switch kind {
	case "missing":
		adminTeam, err := database.TeamFactory.CreateDefaultTeamIfNotExists()
		if err != nil {
			return nil, err
		}
		if err := adminTeam.UpdateProviderAuth(atc.TeamAuth{accessor.OwnerRole: {}}); err != nil {
			return nil, err
		}
	case "forbidden":
		teamName = "other-team"
		_, err := database.TeamFactory.CreateTeam(atc.Team{
			Name: teamName,
			Auth: atc.TeamAuth{accessor.ViewerRole: {
				"users": {"some-connector:someone-else"},
			}},
		})
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown team API result %q", kind)
	}
	payload, err := json.Marshal(map[string]any{
		"sub": user, "preferred_username": user, "aud": []any{audience},
		"exp":              time.Now().Add(time.Hour).Unix(),
		"federated_claims": map[string]any{"connector_id": "brine", "user_id": user},
	})
	if err != nil {
		return nil, err
	}
	var claims db.Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, err
	}
	accessTokens := db.NewAccessTokenFactory(database.Conn)
	if err := accessTokens.CreateAccessToken(token, claims); err != nil {
		return nil, err
	}
	display, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{})
	if err != nil {
		return nil, err
	}
	accessFactory := accessor.NewAccessFactory(
		accessor.NewVerifier(accessTokens, []string{audience}),
		database.TeamFactory, "sub", nil, display,
	)

	teamServer := teamserver.NewServer(logger, database.TeamFactory, "https://example.invalid")
	teamHandler := api.NewTeamScopedHandlerFactory(logger, database.TeamFactory).HandlerFor(teamServer.GetTeam)
	var handler http.Handler = auth.CheckAuthorizationHandler(teamHandler, auth.UnauthorizedRejector{})
	handler = accessor.NewHandler(
		logger, atc.GetTeam, handler,
		accessFactory,
		auditor.NewAuditor(false, false, false, false, false, false, false, false, false, logger),
		map[string]string{},
	)

	routes := rata.Routes{}
	for _, route := range atc.Routes {
		if route.Name == atc.GetTeam || route.Name == atc.GetInfo {
			routes = append(routes, route)
		}
	}
	handlers := rata.Handlers{
		atc.GetTeam: handler,
		atc.GetInfo: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(atc.Info{Version: conc.Version})
		}),
	}
	router, err := rata.NewRouter(routes, handlers)
	if err != nil {
		return nil, fmt.Errorf("build fly team router: %w", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	server := &http.Server{Handler: router, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	rec.RegisterDisposer(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			_ = server.Close()
		}
	})

	home, err := os.MkdirTemp("", "brine-fly-home-*")
	if err != nil {
		return nil, err
	}
	rec.RegisterDisposer(func() { _ = os.RemoveAll(home) })
	rcBytes, err := json.Marshal(rc.RC{Targets: rc.Targets{
		"brine": rc.TargetProps{
			API: "http://" + listener.Addr().String(), TeamName: atc.DefaultTeamName,
			Token: &rc.TargetToken{Type: "Bearer", Value: token},
		},
	}})
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(home, ".flyrc"), rcBytes, 0600); err != nil {
		return nil, err
	}
	return &FlyTeamError{
		Home: home, Fly: flyBinary, URL: "http://" + listener.Addr().String(), Token: token,
		TeamName: teamName, Kind: kind,
	}, nil
}

func productionFlyBinary() (string, error) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("locate fly: runtime caller unavailable")
	}
	path := filepath.Join(filepath.Dir(source), "..", ".build", "fly")
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("locate production fly binary at %s: %w", path, err)
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("production fly path is not executable: %s", path)
	}
	return filepath.Clean(path), nil
}

func flyTeamCommand(name, team string) ([]string, error) {
	commands := map[string][]string{
		"checklist":            {"checklist", "-p", "pipeline"},
		"containers":           {"containers"},
		"trigger-job":          {"trigger-job", "-j", "pipeline/job"},
		"expose-pipeline":      {"expose-pipeline", "-p", "pipeline"},
		"hide-pipeline":        {"hide-pipeline", "-p", "pipeline"},
		"hijack":               {"hijack", "--handle", "container-id"},
		"jobs":                 {"jobs", "-p", "pipeline"},
		"pause-job":            {"pause-job", "-j", "pipeline/job"},
		"pause-pipeline":       {"pause-pipeline", "-p", "pipeline"},
		"unpause-job":          {"unpause-job", "-j", "pipeline/job"},
		"unpause-pipeline":     {"unpause-pipeline", "-p", "pipeline"},
		"destroy-pipeline":     {"destroy-pipeline", "-p", "pipeline"},
		"get-pipeline":         {"get-pipeline", "-p", "pipeline"},
		"order-pipelines":      {"order-pipelines", "-p", "pipeline"},
		"abort-build":          {"abort-build", "-j", "pipeline/job", "-b", "4"},
		"archive-pipeline":     {"archive-pipeline", "-p", "pipeline"},
		"resources":            {"resources", "-p", "pipeline"},
		"check-resource-type":  {"check-resource-type", "-r", "pipeline/type"},
		"check-resource":       {"check-resource", "-r", "pipeline/resource"},
		"resource-versions":    {"resource-versions", "-r", "pipeline/resource"},
		"watch":                {"watch", "-j", "pipeline/job"},
		"clear-resource-cache": {"clear-resource-cache", "-r", "pipeline/resource"},
		"clear-task-cache":     {"clear-task-cache", "-j", "pipeline/job", "-s", "task"},
		"rename-pipeline":      {"rename-pipeline", "-o", "pipeline", "-n", "renamed"},
		"set-pipeline":         {"set-pipeline", "-p", "pipeline", "-c", "fly/integration/fixtures/testConfigValid.yml"},
	}
	args, ok := commands[name]
	if !ok {
		return nil, fmt.Errorf("unknown fly command %q", name)
	}
	return append(args, "--team", team), nil
}

func flyEnvironment(home string) []string {
	env := []string{"HOME=" + home}
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "HOME=") && !strings.HasPrefix(item, "FAKE_FLY_VERSION=") {
			env = append(env, item)
		}
	}
	return env
}

func asExitError(err error, target **exec.ExitError) bool {
	exitErr, ok := err.(*exec.ExitError)
	if ok {
		*target = exitErr
	}
	return ok
}
