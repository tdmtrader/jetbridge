package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/configserver"
	"github.com/concourse/concourse/atc/auditor"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/creds/noop"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/wrappa"
	clientapi "github.com/concourse/concourse/go-concourse/concourse"
	"github.com/concourse/concourse/skymarshal/skycmd"
	"github.com/tedsuo/rata"
	"golang.org/x/oauth2"
)

const (
	configClientAudience  = "config-client-audience"
	configClientConnector = "config-client-connector"
	configClientUser      = "config-client-user"
)

var (
	configClientReadConfig = atc.Config{
		Groups: atc.GroupConfigs{{Name: "all", Jobs: []string{"build"}}},
		Resources: atc.ResourceConfigs{{
			Name: "source", Type: "registry-image", Source: atc.Source{"repository": "busybox"},
		}},
		Jobs: atc.JobConfigs{{Name: "build", Public: true, Serial: true}},
	}
	configClientValidYAML = []byte(`jobs:
- name: build
  plan: []
`)
	configClientWarningYAML = []byte(`groups:
- name: _group
  jobs: [_job]
jobs:
- name: _job
  plan: []
`)
)

type ConfigClientObservation struct {
	Profile          string
	Config           atc.Config
	ExpectedConfig   atc.Config
	Version          string
	ExpectedVersion  string
	Found            bool
	Created          bool
	Updated          bool
	Warnings         []clientapi.ConfigWarning
	Err              error
	InstanceExists   bool
	OrdinaryExists   bool
	PersistedJobName string
}

type configClientBoundary struct {
	database JetbridgeDB
	team     db.Team
	client   clientapi.Team
	server   *http.Server
	done     chan error
}

func ConfigClientDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, ConfigClientObservation](
			"the strict production config client behavior {string} is exercised",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, rec *brine.Recorder, resources brine.Resources) (ConfigClientObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return ConfigClientObservation{}, fmt.Errorf("expected config client profile")
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return ConfigClientObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				boundary, err := newConfigClientBoundary(database)
				if err != nil {
					return ConfigClientObservation{}, err
				}
				rec.RegisterDisposer(func() { _ = boundary.close() })
				return boundary.observe(profile)
			},
		),
		brine.DefineCheck[ConfigClientObservation](
			"the strict production config client behavior exactly matches {string}",
			func(in ConfigClientObservation, p brine.Params, _ *brine.Recorder) error {
				profile, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected config client profile")
				}
				if profile != in.Profile {
					return fmt.Errorf("config client profile got %q, want %q", in.Profile, profile)
				}
				return validateConfigClientObservation(in)
			},
		),
	}
}

func newConfigClientBoundary(database JetbridgeDB) (*configClientBoundary, error) {
	logger := lager.NewLogger("brine-config-client-strict")
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "config-client-team"})
	if err != nil {
		return nil, fmt.Errorf("create config client team: %w", err)
	}
	if err := team.UpdateProviderAuth(atc.TeamAuth{
		accessor.OwnerRole: {"users": {configClientConnector + ":" + configClientUser}},
	}); err != nil {
		return nil, fmt.Errorf("grant config client team access: %w", err)
	}
	team, found, err := database.TeamFactory.FindTeam("config-client-team")
	if err != nil || !found {
		return nil, fmt.Errorf("reload config client team: found=%t err=%w", found, err)
	}

	display, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{})
	if err != nil {
		return nil, fmt.Errorf("create display user generator: %w", err)
	}
	accessFactory := accessor.NewAccessFactory(
		accessor.NewVerifier(db.NewAccessTokenFactory(database.Conn), []string{configClientAudience}),
		database.TeamFactory,
		"sub",
		[]string{"config-client-system"},
		display,
	)
	aud := auditor.NewAuditor(false, false, false, false, false, false, false, false, false, logger)
	wrapper := wrappa.MultiWrappa{
		wrappa.NewAPIAuthWrappa(auth.NewCheckPipelineAccessHandlerFactory(database.TeamFactory), nil, nil, nil),
		wrappa.NewAccessorWrappa(logger, accessFactory, aud, map[string]string{}),
	}
	configServer := configserver.NewServer(logger, database.TeamFactory, creds.Secrets(noop.Noop{}))
	handlers := rata.Handlers{
		atc.GetConfig:  http.HandlerFunc(configServer.GetConfig),
		atc.SaveConfig: http.HandlerFunc(configServer.SaveConfig),
	}
	var routes rata.Routes
	for _, route := range atc.Routes {
		if _, ok := handlers[route.Name]; ok {
			routes = append(routes, route)
		}
	}
	if len(routes) != len(handlers) {
		return nil, fmt.Errorf("config client matched %d routes for %d handlers", len(routes), len(handlers))
	}
	router, err := rata.NewRouter(routes, wrapper.Wrap(handlers))
	if err != nil {
		return nil, fmt.Errorf("build config client router: %w", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for config client API: %w", err)
	}
	server := &http.Server{Handler: router, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()

	token, err := persistConfigClientToken(database)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	httpClient := oauth2.NewClient(context.Background(), oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken: token,
		TokenType:   "Bearer",
	}))
	httpClient.Timeout = 10 * time.Second
	client := clientapi.NewClient("http://"+listener.Addr().String(), httpClient, false)
	return &configClientBoundary{
		database: database,
		team:     team,
		client:   client.Team(team.Name()),
		server:   server,
		done:     done,
	}, nil
}

func persistConfigClientToken(database JetbridgeDB) (string, error) {
	payload, err := json.Marshal(map[string]any{
		"sub": configClientUser,
		"aud": []any{configClientAudience},
		"exp": time.Now().Add(time.Hour).Unix(),
		"federated_claims": map[string]any{
			"connector_id": configClientConnector,
			"user_id":      configClientUser,
		},
	})
	if err != nil {
		return "", err
	}
	var claims db.Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", err
	}
	const token = "config-client-token"
	if err := db.NewAccessTokenFactory(database.Conn).CreateAccessToken(token, claims); err != nil {
		return "", err
	}
	return token, nil
}

func (boundary *configClientBoundary) close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shutdownErr := boundary.server.Shutdown(ctx)
	serveErr := <-boundary.done
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return shutdownErr
}

func (boundary *configClientBoundary) observe(profile string) (ConfigClientObservation, error) {
	out := ConfigClientObservation{Profile: profile}
	ref := atc.PipelineRef{Name: "target"}
	if profile == "read-instanced" || profile == "create-instanced" || profile == "update-instanced" {
		ref.InstanceVars = atc.InstanceVars{"branch": "feature"}
	}

	switch profile {
	case "read-ordinary", "read-instanced":
		pipeline, _, err := boundary.team.SavePipeline(ref, configClientReadConfig, 0, false)
		if err != nil {
			return out, err
		}
		out.ExpectedConfig = configClientReadConfig
		out.ExpectedVersion = strconv.Itoa(int(pipeline.ConfigVersion()))
		out.Config, out.Version, out.Found, out.Err = boundary.client.PipelineConfig(ref)
	case "read-missing":
		out.Config, out.Version, out.Found, out.Err = boundary.client.PipelineConfig(ref)
	case "create-result":
		out.Created, out.Updated, out.Warnings, out.Err = boundary.client.CreateOrUpdatePipelineConfig(ref, "", configClientWarningYAML, false)
	case "update-result":
		pipeline, _, err := boundary.team.SavePipeline(ref, atc.Config{Jobs: atc.JobConfigs{{Name: "before"}}}, 0, false)
		if err != nil {
			return out, err
		}
		out.Created, out.Updated, out.Warnings, out.Err = boundary.client.CreateOrUpdatePipelineConfig(
			ref, strconv.Itoa(int(pipeline.ConfigVersion())), configClientWarningYAML, false,
		)
	case "create-instanced":
		_, _, _, out.Err = boundary.client.CreateOrUpdatePipelineConfig(ref, "", configClientValidYAML, false)
		if out.Err == nil {
			if err := boundary.observeInstancePersistence(&out, ref); err != nil {
				return out, err
			}
		}
	case "update-instanced":
		pipeline, _, err := boundary.team.SavePipeline(ref, atc.Config{Jobs: atc.JobConfigs{{Name: "before"}}}, 0, false)
		if err != nil {
			return out, err
		}
		_, _, _, out.Err = boundary.client.CreateOrUpdatePipelineConfig(
			ref, strconv.Itoa(int(pipeline.ConfigVersion())), configClientValidYAML, false,
		)
		if out.Err == nil {
			if err := boundary.observeInstancePersistence(&out, ref); err != nil {
				return out, err
			}
		}
	default:
		return out, fmt.Errorf("unknown config client profile %q", profile)
	}
	return out, nil
}

func (boundary *configClientBoundary) observeInstancePersistence(out *ConfigClientObservation, ref atc.PipelineRef) error {
	pipeline, found, err := boundary.team.Pipeline(ref)
	if err != nil {
		return err
	}
	out.InstanceExists = found
	if found {
		config, err := pipeline.Config()
		if err != nil {
			return err
		}
		if len(config.Jobs) == 1 {
			out.PersistedJobName = config.Jobs[0].Name
		}
	}
	_, out.OrdinaryExists, err = boundary.team.Pipeline(atc.PipelineRef{Name: ref.Name})
	return err
}

func validateConfigClientObservation(in ConfigClientObservation) error {
	if in.Err != nil {
		return in.Err
	}
	switch in.Profile {
	case "read-ordinary", "read-instanced":
		if !in.Found || in.Version != in.ExpectedVersion || !reflect.DeepEqual(in.Config, in.ExpectedConfig) {
			return fmt.Errorf("config read got found=%t version=%q config=%#v; want found=true version=%q config=%#v", in.Found, in.Version, in.Config, in.ExpectedVersion, in.ExpectedConfig)
		}
	case "read-missing":
		if in.Found {
			return fmt.Errorf("missing config was reported found")
		}
	case "create-result", "update-result":
		wantCreated := in.Profile == "create-result"
		if in.Created != wantCreated || in.Updated == wantCreated {
			return fmt.Errorf("config result got created=%t updated=%t", in.Created, in.Updated)
		}
		gotWarnings := make([]string, 0, len(in.Warnings))
		for _, warning := range in.Warnings {
			gotWarnings = append(gotWarnings, warning.Type+":"+warning.Message)
		}
		sort.Strings(gotWarnings)
		wantWarnings := []string{
			"invalid_identifier:groups._group: '_group' is not a valid identifier: must start with a lowercase letter or a number",
			"invalid_identifier:jobs._job: '_job' is not a valid identifier: must start with a lowercase letter or a number",
		}
		if !reflect.DeepEqual(gotWarnings, wantWarnings) {
			return fmt.Errorf("config warnings got %q, want %q", gotWarnings, wantWarnings)
		}
	case "create-instanced", "update-instanced":
		if !in.InstanceExists || in.OrdinaryExists || in.PersistedJobName != "build" {
			return fmt.Errorf("instance persistence got instance=%t ordinary=%t job=%q", in.InstanceExists, in.OrdinaryExists, in.PersistedJobName)
		}
	default:
		return fmt.Errorf("unknown config client profile %q", in.Profile)
	}
	return nil
}
