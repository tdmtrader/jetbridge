package steps

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/auth"
	"github.com/concourse/concourse/atc/api/configserver"
	"github.com/concourse/concourse/atc/auditor"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/creds/dummy"
	"github.com/concourse/concourse/atc/creds/noop"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/wrappa"
	"github.com/concourse/concourse/skymarshal/skycmd"
	"github.com/tedsuo/rata"
)

const (
	strictConfigAudience  = "strict-config-audience"
	strictConfigConnector = "strict-config-connector"
	strictConfigUser      = "strict-config-user"
)

type APIConfigStrictObservation struct {
	Status          int
	ContentType     string
	ResponseVersion string
	Body            []byte
	PipelineExists  bool
	PipelinePaused  bool
	PipelineVersion int
	Config          []byte
}

func APIConfigStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, APIConfigStrictObservation](
			"the strict production config API executes profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, rec *brine.Recorder, resources brine.Resources) (APIConfigStrictObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return APIConfigStrictObservation{}, fmt.Errorf("expected strict config API profile")
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return APIConfigStrictObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				return executeStrictConfigAPI(database, rec, profile)
			},
		),
		brine.DefineCheck[APIConfigStrictObservation](
			"the strict config API {string} observation is {string}",
			func(in APIConfigStrictObservation, p brine.Params, _ *brine.Recorder) error {
				kind, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected strict config API observation kind")
				}
				expected, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected strict config API observation")
				}
				actual, err := strictConfigObservation(in, kind)
				if err != nil {
					return err
				}
				if actual != expected {
					return fmt.Errorf("strict config API %s observation is %q, want %q", kind, actual, expected)
				}
				return nil
			},
		),
	}
}

func strictConfigObservation(in APIConfigStrictObservation, kind string) (string, error) {
	switch kind {
	case "status":
		return strconv.Itoa(in.Status), nil
	case "content-type":
		return in.ContentType, nil
	case "headers":
		return in.ContentType + ";version=" + in.ResponseVersion, nil
	case "body-sha256":
		canonical, err := canonicalStrictConfigJSON(in.Body)
		if err != nil {
			return "", err
		}
		return strictConfigSHA(canonical), nil
	case "config-sha256":
		return strictConfigSHA(in.Config), nil
	case "pipeline-sha256":
		canonical, err := json.Marshal(struct {
			Exists  bool            `json:"exists"`
			Paused  bool            `json:"paused"`
			Version int             `json:"version"`
			Config  json.RawMessage `json:"config,omitempty"`
		}{in.PipelineExists, in.PipelinePaused, in.PipelineVersion, in.Config})
		if err != nil {
			return "", err
		}
		return strictConfigSHA(canonical), nil
	case "full-sha256":
		body, err := canonicalStrictConfigJSON(in.Body)
		if err != nil {
			return "", err
		}
		canonical, err := json.Marshal(struct {
			Status          int             `json:"status"`
			ContentType     string          `json:"content_type"`
			ResponseVersion string          `json:"response_version"`
			Body            json.RawMessage `json:"body"`
			Exists          bool            `json:"exists"`
			Paused          bool            `json:"paused"`
			Version         int             `json:"version"`
			Config          json.RawMessage `json:"config,omitempty"`
		}{in.Status, in.ContentType, in.ResponseVersion, body, in.PipelineExists, in.PipelinePaused, in.PipelineVersion, in.Config})
		if err != nil {
			return "", err
		}
		return strictConfigSHA(canonical), nil
	default:
		return "", fmt.Errorf("unknown strict config API observation %q", kind)
	}
}

func strictConfigSHA(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func canonicalStrictConfigJSON(value []byte) ([]byte, error) {
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		return nil, fmt.Errorf("decode strict config API JSON %q: %w", value, err)
	}
	canonical, err := json.Marshal(decoded)
	if err != nil {
		return nil, fmt.Errorf("canonicalize strict config API JSON: %w", err)
	}
	return canonical, nil
}

func executeStrictConfigAPI(database JetbridgeDB, rec *brine.Recorder, profile string) (APIConfigStrictObservation, error) {
	logger := lager.NewLogger("brine-strict-config-api")
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "api-team"})
	if err != nil {
		return APIConfigStrictObservation{}, fmt.Errorf("create strict API team: %w", err)
	}
	if err := team.UpdateProviderAuth(atc.TeamAuth{
		accessor.OwnerRole: {"users": {strictConfigConnector + ":" + strictConfigUser}},
	}); err != nil {
		return APIConfigStrictObservation{}, fmt.Errorf("grant strict API role: %w", err)
	}
	if _, err := database.Conn.Exec(`UPDATE teams SET admin = TRUE WHERE id = $1`, team.ID()); err != nil {
		return APIConfigStrictObservation{}, fmt.Errorf("make strict API team admin: %w", err)
	}
	team, found, err := database.TeamFactory.FindTeam("api-team")
	if err != nil || !found {
		return APIConfigStrictObservation{}, fmt.Errorf("reload strict API team: found=%t err=%w", found, err)
	}

	secretManager := creds.Secrets(noop.Noop{})
	if strings.Contains(profile, "/credential-present") {
		secretManager = dummy.NewSecretsFactory([]dummy.VarFlag{{Name: "BAR", Value: "present"}}).NewSecrets()
	} else if strings.Contains(profile, "/credential-missing") {
		secretManager = dummy.NewSecretsFactory([]dummy.VarFlag{{Name: "OTHER", Value: "present"}}).NewSecrets()
	}

	targetTeam := team
	if profile == "put/invalid-identifier" {
		targetTeam, err = database.TeamFactory.CreateTeam(atc.Team{Name: "_team"})
		if err != nil {
			return APIConfigStrictObservation{}, fmt.Errorf("create invalid-identifier team: %w", err)
		}
	}

	router, err := strictConfigRouter(database, logger, secretManager)
	if err != nil {
		return APIConfigStrictObservation{}, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return APIConfigStrictObservation{}, fmt.Errorf("listen for strict config API: %w", err)
	}
	server := &http.Server{Handler: router, ReadHeaderTimeout: 5 * time.Second}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	rec.RegisterDisposer(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		<-serveDone
	})

	requestSpec, err := prepareStrictConfigRequest(targetTeam, profile)
	if err != nil {
		return APIConfigStrictObservation{}, err
	}
	authorization, err := createStrictConfigToken(database)
	if err != nil {
		return APIConfigStrictObservation{}, err
	}
	request, err := http.NewRequestWithContext(context.Background(), requestSpec.method,
		"http://"+listener.Addr().String()+requestSpec.path, bytes.NewReader(requestSpec.body))
	if err != nil {
		return APIConfigStrictObservation{}, err
	}
	request.Header = requestSpec.header
	if !strings.HasSuffix(profile, "/unauthenticated") {
		request.Header.Set("Authorization", authorization)
	}
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		return APIConfigStrictObservation{}, fmt.Errorf("execute strict config API request: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return APIConfigStrictObservation{}, err
	}

	observation := APIConfigStrictObservation{
		Status: response.StatusCode, ContentType: response.Header.Get("Content-Type"),
		ResponseVersion: response.Header.Get(atc.ConfigVersionHeader), Body: body,
	}
	ref := requestSpec.ref
	pipeline, found, err := targetTeam.Pipeline(ref)
	if err != nil {
		return observation, fmt.Errorf("reload strict API pipeline: %w", err)
	}
	if found {
		observation.PipelineExists = true
		observation.PipelinePaused = pipeline.Paused()
		observation.PipelineVersion = int(pipeline.ConfigVersion())
		config, err := pipeline.Config()
		if err != nil {
			return observation, fmt.Errorf("read strict API pipeline config: %w", err)
		}
		observation.Config, err = json.Marshal(config)
		if err != nil {
			return observation, err
		}
	}
	return observation, nil
}

func strictConfigRouter(database JetbridgeDB, logger lager.Logger, secretManager creds.Secrets) (http.Handler, error) {
	display, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{})
	if err != nil {
		return nil, err
	}
	accessFactory := accessor.NewAccessFactory(
		accessor.NewVerifier(db.NewAccessTokenFactory(database.Conn), []string{strictConfigAudience}),
		database.TeamFactory, "sub", []string{"strict-system"}, display,
	)
	aud := auditor.NewAuditor(false, false, false, false, false, false, false, false, false, logger)
	wrapper := wrappa.MultiWrappa{
		wrappa.NewAPIAuthWrappa(auth.NewCheckPipelineAccessHandlerFactory(database.TeamFactory), nil, nil, nil),
		wrappa.NewAccessorWrappa(logger, accessFactory, aud, map[string]string{}),
	}
	server := configserver.NewServer(logger, database.TeamFactory, secretManager)
	handlers := rata.Handlers{
		atc.GetConfig:  http.HandlerFunc(server.GetConfig),
		atc.SaveConfig: http.HandlerFunc(server.SaveConfig),
	}
	var routes rata.Routes
	for _, route := range atc.Routes {
		if route.Name == atc.GetConfig || route.Name == atc.SaveConfig {
			routes = append(routes, route)
		}
	}
	return rata.NewRouter(routes, wrapper.Wrap(handlers))
}

func createStrictConfigToken(database JetbridgeDB) (string, error) {
	payload, err := json.Marshal(map[string]any{
		"sub": strictConfigUser, "aud": []any{strictConfigAudience}, "exp": time.Now().Add(time.Hour).Unix(),
		"federated_claims": map[string]any{"connector_id": strictConfigConnector, "user_id": strictConfigUser},
	})
	if err != nil {
		return "", err
	}
	var claims db.Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", err
	}
	if err := db.NewAccessTokenFactory(database.Conn).CreateAccessToken("strict-config-token", claims); err != nil {
		return "", err
	}
	return "bearer strict-config-token", nil
}

type strictConfigRequest struct {
	method string
	path   string
	body   []byte
	header http.Header
	ref    atc.PipelineRef
}

func prepareStrictConfigRequest(team db.Team, profile string) (strictConfigRequest, error) {
	request := strictConfigRequest{method: http.MethodPut, path: "/api/v1/teams/api-team/pipelines/a-pipeline/config", header: make(http.Header), ref: atc.PipelineRef{Name: "a-pipeline"}}
	valid := []byte(`{"jobs":[{"name":"job","plan":[]}]}`)
	request.body = valid
	request.header.Set("Content-Type", "application/json")

	if strings.HasPrefix(profile, "get/") {
		request.method, request.body = http.MethodGet, nil
		request.header.Del("Content-Type")
		if profile != "get/pipeline-missing" && profile != "get/team-missing" && profile != "get/malformed-instance-vars" && profile != "get/unauthenticated" {
			pipeline, _, err := team.SavePipeline(request.ref, atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}}, 0, false)
			if err != nil {
				return request, err
			}
			if profile == "get/archived" {
				if err := pipeline.Archive(); err != nil {
					return request, err
				}
			}
		}
		switch profile {
		case "get/team-missing":
			request.path = "/api/v1/teams/missing-team/pipelines/a-pipeline/config"
		case "get/malformed-instance-vars":
			request.path += "?vars.foo=%7B"
		}
		return request, nil
	}

	create := strings.Contains(profile, "/create") || profile == "put/invalid-identifier" || strings.HasSuffix(profile, "/unauthenticated")
	if !create && profile != "put/team-missing" {
		pipeline, _, err := team.SavePipeline(request.ref, atc.Config{Jobs: atc.JobConfigs{{Name: "original"}}}, 0, false)
		if err != nil {
			return request, err
		}
		request.header.Set(atc.ConfigVersionHeader, strconv.Itoa(int(pipeline.ConfigVersion())))
	}

	switch {
	case profile == "put/invalid-identifier":
		request.path = "/api/v1/teams/_team/pipelines/_pipeline/config"
		request.ref = atc.PipelineRef{Name: "_pipeline"}
	case profile == "put/empty-identifier":
		request.path = "/api/v1/teams//pipelines//config"
	case profile == "put/malformed-json":
		request.body = []byte(`{`)
	case profile == "put/malformed-yaml":
		request.header.Set("Content-Type", "application/x-yaml")
		request.body = []byte(`{`)
	case profile == "put/valid-yaml" || profile == "put/create-yaml":
		request.header.Set("Content-Type", "application/x-yaml")
		request.body = []byte("jobs:\n- name: job\n  plan: []\n")
	case profile == "put/invalid-config":
		request.body = []byte(`{"jobs":[]}`)
	case profile == "put/suspicious-yaml":
		request.header.Set("Content-Type", "application/x-yaml")
		request.body = []byte("resources:\n- name: image\n  type: registry-image\njobs:\n- name: job\n  plan:\n  - get: image\n  - task: task\n    config:\n      platform: linux\n      run: {path: ls}\n      params: {FOO: true, BAR: 1, BAZ: 1.9}\n")
	case strings.HasPrefix(profile, "put/creds/"):
		request.header.Set("Content-Type", "application/x-yaml")
		request.path += "?check_creds="
		request.body = []byte(strictCredentialPayload(strings.Split(profile, "/")[2]))
	case strings.HasPrefix(profile, "put/inline-creds/"):
		request.header.Set("Content-Type", "application/x-yaml")
		request.path += "?check_creds="
		request.body = []byte("jobs:\n- name: job\n  plan:\n  - task: task\n    config:\n      platform: linux\n      run: {path: ls}\n      params: {FOO: ((BAR))}\n")
	case profile == "put/malformed-instance-vars":
		request.path += "?vars.foo=%7B"
	case profile == "put/valid-instance-vars":
		request.path += "?vars=%7B%22branch%22%3A%22feature%22%7D"
		request.ref.InstanceVars = atc.InstanceVars{"branch": "feature"}
	case profile == "put/team-missing":
		request.path = "/api/v1/teams/missing-team/pipelines/a-pipeline/config"
	case profile == "put/unsupported-content":
		request.header.Set("Content-Type", "application/x-toml")
	case profile == "put/top-level-extra":
		request.body = []byte(`{"extra":"ignored","jobs":[{"name":"job","plan":[]}]}`)
	case profile == "put/nested-extra":
		request.body = []byte(`{"jobs":[{"name":"job","pubic":true,"plan":[]}]}`)
	case profile == "put/malformed-version":
		request.header.Set(atc.ConfigVersionHeader, "forty-two")
	}
	return request, nil
}

func strictCredentialPayload(target string) string {
	switch target {
	case "resource-type":
		return "resource_types:\n- name: type\n  type: registry-image\n  source: {repository: ((BAR))}\njobs:\n- name: job\n  plan: []\n"
	case "resource-source":
		return "resources:\n- name: resource\n  type: registry-image\n  source: {repository: ((BAR))}\njobs:\n- name: job\n  plan:\n  - get: resource\n"
	case "webhook-token":
		return "resources:\n- name: resource\n  type: registry-image\n  webhook_token: ((BAR))\njobs:\n- name: job\n  plan:\n  - get: resource\n"
	case "task-params":
		return "jobs:\n- name: job\n  plan:\n  - task: task\n    file: task.yml\n    params: {FOO: ((BAR))}\n"
	case "task-vars":
		return "jobs:\n- name: job\n  plan:\n  - task: task\n    file: task.yml\n    vars: {FOO: ((BAR))}\n"
	case "nested-task-vars":
		return "jobs:\n- name: job\n  plan:\n  - do:\n    - task: task\n      file: task.yml\n      vars: {FOO: ((BAR))}\n"
	default:
		return ""
	}
}
