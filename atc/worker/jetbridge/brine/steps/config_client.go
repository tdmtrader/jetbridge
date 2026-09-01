package steps

import (
	"fmt"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	clientapi "github.com/concourse/concourse/go-concourse/concourse"
)

var clientPipelineYAML = []byte(`jobs:
- name: build
  plan:
  - task: hello
    config:
      platform: linux
      image_resource:
        type: registry-image
        source:
          repository: busybox
      run:
        path: "true"
`)

type ConfigClientState struct {
	API      *PipelineAPI
	Team     clientapi.Team
	Ref      atc.PipelineRef
	Created  bool
	Updated  bool
	Found    bool
	Err      error
	Version  string
	Jobs     int
	Warnings int
}

func ConfigClientDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, *ConfigClientState](
			"the production Go config client, real API, and PostgreSQL",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, rec *brine.Recorder, resources brine.Resources) (*ConfigClientState, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return nil, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				api, err := newPipelineAPI(database, rec)
				if err != nil {
					return nil, err
				}
				client := clientapi.NewClient(api.Server.URL, api.Client, false)
				return &ConfigClientState{
					API: api, Team: client.Team("api-team"), Ref: atc.PipelineRef{Name: "target"},
				}, nil
			},
		),
		brine.DefineMap[*ConfigClientState, *ConfigClientState](
			"the config client uses an {string} reference",
			func(in *ConfigClientState, p brine.Params, _ *brine.Recorder) (*ConfigClientState, error) {
				kind, _ := p.GetString(0)
				if kind == "instanced" {
					in.Ref.InstanceVars = atc.InstanceVars{"branch": "master"}
				} else if kind != "ordinary" {
					return in, fmt.Errorf("unknown config reference kind %q", kind)
				}
				return in, nil
			},
		),
		brine.DefineMap[*ConfigClientState, *ConfigClientState](
			"the real pipeline config already exists",
			func(in *ConfigClientState, _ brine.Params, _ *brine.Recorder) (*ConfigClientState, error) {
				_, _, err := in.API.Team.SavePipeline(in.Ref, atc.Config{Jobs: atc.JobConfigs{{Name: "build"}}}, 0, false)
				return in, err
			},
		),
		brine.DefineMap[*ConfigClientState, *ConfigClientState](
			"the Go client reads the pipeline config",
			func(in *ConfigClientState, _ brine.Params, _ *brine.Recorder) (*ConfigClientState, error) {
				config, version, found, err := in.Team.PipelineConfig(in.Ref)
				in.Jobs, in.Version, in.Found, in.Err = len(config.Jobs), version, found, err
				return in, nil
			},
		),
		brine.DefineMap[*ConfigClientState, *ConfigClientState](
			"the Go client saves a {string} pipeline config with credential checking {string}",
			func(in *ConfigClientState, p brine.Params, _ *brine.Recorder) (*ConfigClientState, error) {
				mode, rawCheck, err := twoParams("the Go client saves a {string} pipeline config with credential checking {string}", p)
				if err != nil {
					return in, err
				}
				version := ""
				if mode == "update" {
					created, _, _, createErr := in.Team.CreateOrUpdatePipelineConfig(in.Ref, "", clientPipelineYAML, false)
					if createErr != nil || !created {
						return in, firstError(createErr, fmt.Errorf("initial config was not created"))
					}
					_, version, in.Found, in.Err = in.Team.PipelineConfig(in.Ref)
					if in.Err != nil || !in.Found {
						return in, firstError(in.Err, fmt.Errorf("created config was not found"))
					}
				} else if mode != "create" {
					return in, fmt.Errorf("unknown config save mode %q", mode)
				}
				checkCreds := rawCheck == "enabled"
				var warnings []clientapi.ConfigWarning
				in.Created, in.Updated, warnings, in.Err = in.Team.CreateOrUpdatePipelineConfig(in.Ref, version, clientPipelineYAML, checkCreds)
				in.Warnings = len(warnings)
				return in, nil
			},
		),
		brine.DefineMap[*ConfigClientState, *ConfigClientState](
			"the Go client submits an invalid pipeline config",
			func(in *ConfigClientState, _ brine.Params, _ *brine.Recorder) (*ConfigClientState, error) {
				in.Created, in.Updated, _, in.Err = in.Team.CreateOrUpdatePipelineConfig(in.Ref, "", []byte("jobs: wrong"), false)
				return in, nil
			},
		),
		brine.DefineCheck[*ConfigClientState]("the Go config client found the config", func(in *ConfigClientState, _ brine.Params, _ *brine.Recorder) error {
			if !in.Found {
				return fmt.Errorf("config client did not find config")
			}
			return nil
		}),
		brine.DefineCheck[*ConfigClientState]("the Go config client did not find the config", func(in *ConfigClientState, _ brine.Params, _ *brine.Recorder) error {
			if in.Found {
				return fmt.Errorf("config client unexpectedly found config")
			}
			return nil
		}),
		brine.DefineCheck[*ConfigClientState]("the Go config client returned no error", func(in *ConfigClientState, _ brine.Params, _ *brine.Recorder) error {
			return in.Err
		}),
		brine.DefineCheck[*ConfigClientState]("the Go config client returned a validation error", func(in *ConfigClientState, _ brine.Params, _ *brine.Recorder) error {
			if in.Err == nil {
				return fmt.Errorf("invalid config returned no error")
			}
			if _, ok := in.Err.(clientapi.InvalidConfigError); !ok {
				return fmt.Errorf("expected InvalidConfigError, got %T: %v", in.Err, in.Err)
			}
			return nil
		}),
		brine.DefineCheck[*ConfigClientState]("the Go config client returned a nonempty version", func(in *ConfigClientState, _ brine.Params, _ *brine.Recorder) error {
			if in.Version == "" {
				return fmt.Errorf("config version was empty")
			}
			return nil
		}),
		CheckInt[*ConfigClientState]("the Go config client returned {int} job(s)", "config job count", func(in *ConfigClientState) (int, error) {
			return in.Jobs, nil
		}),
		brine.DefineCheck[*ConfigClientState]("the Go config client reported {string}", func(in *ConfigClientState, p brine.Params, _ *brine.Recorder) error {
			want, _ := p.GetString(0)
			got := fmt.Sprintf("created=%t;updated=%t;warnings=%d", in.Created, in.Updated, in.Warnings)
			if got != want {
				return fmt.Errorf("expected config result %q, got %q", want, got)
			}
			return nil
		}),
	}
}
