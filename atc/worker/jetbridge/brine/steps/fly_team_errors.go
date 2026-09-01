package steps

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/teamserver"
	"github.com/concourse/concourse/atc/auditor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/fly/rc"
	"github.com/tedsuo/rata"
)

type FlyTeamError struct {
	Server   *httptest.Server
	Home     string
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
				executable, err := os.Executable()
				if err != nil {
					return in, err
				}
				cmd := exec.Command(filepath.Join(filepath.Dir(executable), "fly"), append([]string{"-t", "brine"}, args...)...)
				cmd.Dir = filepath.Clean(filepath.Join(filepath.Dir(executable), "../../../../.."))
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
				return nil
			},
		),
	}
}

func newFlyTeamError(database JetbridgeDB, kind string, rec *brine.Recorder) (*FlyTeamError, error) {
	logger := lagertest.NewTestLogger("brine-fly-team")
	var teams []db.Team
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
		teams = []db.Team{adminTeam}
	case "forbidden":
		teamName = "other-team"
		team, err := database.TeamFactory.CreateTeam(atc.Team{
			Name: teamName,
			Auth: atc.TeamAuth{accessor.ViewerRole: {
				"users": {"some-connector:someone-else"},
			}},
		})
		if err != nil {
			return nil, err
		}
		teams = []db.Team{team}
	default:
		return nil, fmt.Errorf("unknown team API result %q", kind)
	}

	teamServer := teamserver.NewServer(logger, database.TeamFactory, "https://example.invalid")
	teamHandler := api.NewTeamScopedHandlerFactory(logger, database.TeamFactory).HandlerFor(teamServer.GetTeam)
	var handler http.Handler = auth.CheckAuthorizationHandler(teamHandler, auth.UnauthorizedRejector{})
	handler = accessor.NewHandler(
		logger, atc.GetTeam, handler,
		pipelineAPIAccessFactory{teams: teams},
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
			_ = json.NewEncoder(w).Encode(atc.Info{Version: "0.0.0"})
		}),
	}
	router, err := rata.NewRouter(routes, handlers)
	if err != nil {
		return nil, fmt.Errorf("build fly team router: %w", err)
	}
	server := httptest.NewServer(router)
	rec.RegisterDisposer(server.Close)

	home, err := os.MkdirTemp("", "brine-fly-home-*")
	if err != nil {
		return nil, err
	}
	rec.RegisterDisposer(func() { _ = os.RemoveAll(home) })
	rcBytes, err := json.Marshal(rc.RC{Targets: rc.Targets{
		"brine": rc.TargetProps{
			API: server.URL, TeamName: atc.DefaultTeamName,
			Token: &rc.TargetToken{Type: "Bearer", Value: "brine-token"},
		},
	}})
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(home, ".flyrc"), rcBytes, 0600); err != nil {
		return nil, err
	}
	return &FlyTeamError{Server: server, Home: home, TeamName: teamName, Kind: kind}, nil
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
	env := []string{"HOME=" + home, "FAKE_FLY_VERSION=0.0.0"}
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
