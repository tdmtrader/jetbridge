package api_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/creds/dummy"
	"github.com/concourse/concourse/atc/db"
	"github.com/tedsuo/rata"
	"sigs.k8s.io/yaml"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type configAPISaveCall struct {
	ref             atc.PipelineRef
	config          atc.Config
	from            db.ConfigVersion
	initiallyPaused bool
}

type configAPIPipeline struct {
	db.Pipeline
	configErr error
}

func (pipeline configAPIPipeline) Config() (atc.Config, error) {
	if pipeline.configErr != nil {
		return atc.Config{}, pipeline.configErr
	}
	return pipeline.Pipeline.Config()
}

type configAPITeam struct {
	db.Team
	state *configAPITeamState
}

type configAPITeamState struct {
	mu            sync.Mutex
	pipelineErr   error
	configErr     error
	saveErr       error
	pipelineCalls []atc.PipelineRef
	saveCalls     []configAPISaveCall
}

func cloneConfigAPIValue(value any) any {
	switch typed := value.(type) {
	case atc.InstanceVars:
		cloned := make(atc.InstanceVars, len(typed))
		for key, nested := range typed {
			cloned[key] = cloneConfigAPIValue(nested)
		}
		return cloned
	case map[string]any:
		cloned := make(map[string]any, len(typed))
		for key, nested := range typed {
			cloned[key] = cloneConfigAPIValue(nested)
		}
		return cloned
	case []any:
		cloned := make([]any, len(typed))
		for i, nested := range typed {
			cloned[i] = cloneConfigAPIValue(nested)
		}
		return cloned
	case []string:
		return append([]string(nil), typed...)
	default:
		return typed
	}
}

func cloneConfigAPIPipelineRef(ref atc.PipelineRef) atc.PipelineRef {
	cloned := atc.PipelineRef{Name: ref.Name}
	if ref.InstanceVars != nil {
		cloned.InstanceVars = make(atc.InstanceVars, len(ref.InstanceVars))
		for key, value := range ref.InstanceVars {
			cloned.InstanceVars[key] = cloneConfigAPIValue(value)
		}
	}
	return cloned
}

func cloneConfigAPIConfig(config atc.Config) (atc.Config, error) {
	payload, err := json.Marshal(config)
	if err != nil {
		return atc.Config{}, err
	}

	var cloned atc.Config
	if err := json.Unmarshal(payload, &cloned); err != nil {
		return atc.Config{}, err
	}
	return cloned, nil
}

func (team *configAPITeam) Pipeline(ref atc.PipelineRef) (db.Pipeline, bool, error) {
	team.state.mu.Lock()
	team.state.pipelineCalls = append(team.state.pipelineCalls, cloneConfigAPIPipelineRef(ref))
	pipelineErr := team.state.pipelineErr
	configErr := team.state.configErr
	team.state.mu.Unlock()

	if pipelineErr != nil {
		return nil, false, pipelineErr
	}

	pipeline, found, err := team.Team.Pipeline(ref)
	if err != nil || !found {
		return pipeline, found, err
	}
	return configAPIPipeline{Pipeline: pipeline, configErr: configErr}, true, nil
}

func (team *configAPITeam) SavePipeline(
	ref atc.PipelineRef,
	config atc.Config,
	from db.ConfigVersion,
	initiallyPaused bool,
) (db.Pipeline, bool, error) {
	clonedConfig, err := cloneConfigAPIConfig(config)
	if err != nil {
		return nil, false, err
	}

	team.state.mu.Lock()
	team.state.saveCalls = append(team.state.saveCalls, configAPISaveCall{
		ref:             cloneConfigAPIPipelineRef(ref),
		config:          clonedConfig,
		from:            from,
		initiallyPaused: initiallyPaused,
	})
	saveErr := team.state.saveErr
	team.state.mu.Unlock()

	if saveErr != nil {
		return nil, false, saveErr
	}
	return team.Team.SavePipeline(ref, config, from, initiallyPaused)
}

func (team *configAPITeam) setPipelineError(err error) {
	team.state.mu.Lock()
	defer team.state.mu.Unlock()
	team.state.pipelineErr = err
}

func (team *configAPITeam) setConfigError(err error) {
	team.state.mu.Lock()
	defer team.state.mu.Unlock()
	team.state.configErr = err
}

func (team *configAPITeam) setSaveError(err error) {
	team.state.mu.Lock()
	defer team.state.mu.Unlock()
	team.state.saveErr = err
}

func (team *configAPITeam) saveCallSnapshot() ([]configAPISaveCall, error) {
	team.state.mu.Lock()
	defer team.state.mu.Unlock()

	calls := make([]configAPISaveCall, len(team.state.saveCalls))
	for i, call := range team.state.saveCalls {
		clonedConfig, err := cloneConfigAPIConfig(call.config)
		if err != nil {
			return nil, err
		}
		calls[i] = configAPISaveCall{
			ref:             cloneConfigAPIPipelineRef(call.ref),
			config:          clonedConfig,
			from:            call.from,
			initiallyPaused: call.initiallyPaused,
		}
	}
	return calls, nil
}

func (team *configAPITeam) pipelineCallSnapshot() []atc.PipelineRef {
	team.state.mu.Lock()
	defer team.state.mu.Unlock()

	calls := make([]atc.PipelineRef, len(team.state.pipelineCalls))
	for i, ref := range team.state.pipelineCalls {
		calls[i] = cloneConfigAPIPipelineRef(ref)
	}
	return calls
}

type configAPITeamFactory struct {
	db.TeamFactory

	mu                         sync.Mutex
	team                       *configAPITeam
	findTeamErr                error
	findTeamCalls              []string
	notifyResourceScannerCalls int
}

func (factory *configAPITeamFactory) FindTeam(name string) (db.Team, bool, error) {
	factory.mu.Lock()
	factory.findTeamCalls = append(factory.findTeamCalls, name)
	findTeamErr := factory.findTeamErr
	factory.mu.Unlock()

	if findTeamErr != nil {
		return nil, false, findTeamErr
	}

	team, found, err := factory.TeamFactory.FindTeam(name)
	if err != nil || !found {
		return team, found, err
	}

	factory.mu.Lock()
	decorated := factory.team
	factory.mu.Unlock()
	return &configAPITeam{Team: team, state: decorated.state}, true, nil
}

func (factory *configAPITeamFactory) NotifyResourceScanner() error {
	factory.mu.Lock()
	factory.notifyResourceScannerCalls++
	factory.mu.Unlock()
	return factory.TeamFactory.NotifyResourceScanner()
}

func (factory *configAPITeamFactory) setFindTeamError(err error) {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	factory.findTeamErr = err
}

func (factory *configAPITeamFactory) findTeamCallSnapshot() []string {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	return append([]string(nil), factory.findTeamCalls...)
}

func (factory *configAPITeamFactory) notifyResourceScannerCallCount() int {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	return factory.notifyResourceScannerCalls
}

// configAPISecrets is the credential manager production registers under the
// name "dummy", holding the one variable given. Its lookup paths are real, so
// resolving a var walks team/pipeline/, then team/, then the bare name.
func configAPISecrets(name string, value any) creds.Secrets {
	return dummy.NewSecretsFactory([]dummy.VarFlag{{Name: name, Value: value}}).NewSecrets()
}

var _ = Describe("Config API", func() {
	var (
		pipelineConfig atc.Config
	)

	BeforeEach(func() {
		pipelineConfig = atc.Config{
			Groups: atc.GroupConfigs{
				{
					Name:      "some-group",
					Jobs:      []string{"some-job"},
					Resources: []string{"some-resource"},
				},
			},

			VarSources: atc.VarSourceConfigs{
				{
					Name: "some",
					Type: "dummy",
					Config: map[string]any{
						"vars": map[string]any{},
					},
				},
			},

			Resources: atc.ResourceConfigs{
				{
					Name: "some-resource",
					Type: "some-type",
					Source: atc.Source{
						"source-config": "some-value",
					},
				},
			},

			ResourceTypes: atc.ResourceTypes{
				{
					Name:   "custom-resource",
					Type:   "custom-type",
					Source: atc.Source{"custom": "source"},
					Tags:   atc.Tags{"some-tag"},
				},
			},

			Jobs: atc.JobConfigs{
				{
					Name:   "some-job",
					Public: true,
					Serial: true,
					PlanSequence: []atc.Step{
						{
							Config: &atc.GetStep{
								Name:     "some-input",
								Resource: "some-resource",
								Params:   atc.Params{"some-param": "some-value"},
							},
						},
						{
							Config: &atc.TaskStep{
								Name:       "some-task",
								Privileged: true,
								Config: &atc.TaskConfig{
									Platform:  "linux",
									RootfsURI: "some-image",
									Run: atc.TaskRunConfig{
										Path: "/path/to/run",
									},
								},
							},
						},
						{
							Config: &atc.PutStep{
								Name:     "some-output",
								Resource: "some-resource",
								Params:   atc.Params{"some-param": "some-value"},
							},
						},
					},
				},
			},
		}
	})

	Describe("GET /api/v1/teams/:team_name/pipelines/:name/config", func() {
		var (
			realdb            *realDB
			deps              apiDBDeps
			server            *httptest.Server
			realTeam          db.Team
			realPipeline      db.Pipeline
			configTeam        *configAPITeam
			configTeamFactory *configAPITeamFactory
			routeParams       rata.Params
			requestQuery      url.Values
			response          *http.Response
		)

		BeforeEach(func() {
			realdb = useRealDB()
			deps = realdb.Deps

			var err error
			realTeam, err = deps.teamFactory.CreateTeam(atc.Team{Name: "a-team"})
			Expect(err).NotTo(HaveOccurred())
			realPipeline = realdb.SavePipeline(realTeam, "something-else", pipelineConfig)

			configTeam = &configAPITeam{Team: realTeam, state: &configAPITeamState{}}
			configTeamFactory = &configAPITeamFactory{
				TeamFactory: deps.teamFactory,
				team:        configTeam,
			}
			deps.teamFactory = configTeamFactory

			routeParams = rata.Params{
				"team_name":     "a-team",
				"pipeline_name": "something-else",
			}
			requestQuery = url.Values{}
		})

		JustBeforeEach(func() {
			realdb.Deps = deps
			server = realdb.Serve()
			getRequestGenerator := rata.NewRequestGenerator(server.URL, atc.Routes)
			request, err := getRequestGenerator.CreateRequest(atc.GetConfig, routeParams, nil)
			Expect(err).NotTo(HaveOccurred())
			request.URL.RawQuery = requestQuery.Encode()

			response, err = client.Do(request)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				Expect(response.Body.Close()).To(Succeed())
			})
		})

		Context("when authorized", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(true)
				fakeAccess.IsAuthorizedReturns(true)
			})

			Context("when the team is found", func() {
				Context("when the pipeline is found", func() {
					Context("when instance vars ar specified", func() {
						Context("when instance vars are malformed", func() {
							BeforeEach(func() {
								requestQuery.Add("vars.branch", "{")
							})

							It("doesn't find the pipeline", func() {
								Expect(configTeamFactory.findTeamCallSnapshot()).To(BeEmpty())
								Expect(configTeam.pipelineCallSnapshot()).To(BeEmpty())
							})
						})

						Context("when instance vars is valid", func() {
							var (
								instancedConfig   atc.Config
								instancedPipeline db.Pipeline
							)

							BeforeEach(func() {
								requestQuery.Add("vars.branch", `"feature"`)

								instancedConfig = pipelineConfig
								instancedConfig.Groups = atc.GroupConfigs{{
									Name:      "instanced-group",
									Jobs:      []string{"some-job"},
									Resources: []string{"some-resource"},
								}}

								var err error
								instancedPipeline, _, err = realTeam.SavePipeline(
									atc.PipelineRef{
										Name:         "something-else",
										InstanceVars: atc.InstanceVars{"branch": "feature"},
									},
									instancedConfig,
									db.ConfigVersion(0),
									false,
								)
								Expect(err).NotTo(HaveOccurred())
							})

							It("finds the pipeline", func() {
								Expect(configTeam.pipelineCallSnapshot()).To(Equal([]atc.PipelineRef{{
									Name:         "something-else",
									InstanceVars: atc.InstanceVars{"branch": "feature"},
								}}))

								Expect(response.StatusCode).To(Equal(http.StatusOK))
								Expect(response.Header.Get(atc.ConfigVersionHeader)).To(Equal(strconv.Itoa(int(instancedPipeline.ConfigVersion()))))

								var actualConfigResponse atc.ConfigResponse
								Expect(json.NewDecoder(response.Body).Decode(&actualConfigResponse)).To(Succeed())
								Expect(actualConfigResponse).To(Equal(atc.ConfigResponse{Config: instancedConfig}))
							})
						})
					})

					Context("when the pipeline config is found", func() {

						Context("when finding the config fails", func() {
							BeforeEach(func() {
								configTeam.setConfigError(errors.New("fail"))
							})

							It("returns 500", func() {
								Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
							})
						})
					})

					Context("when the pipeline is archived", func() {
						BeforeEach(func() {
							Expect(realPipeline.Archive()).To(Succeed())
						})
					})
				})

				Context("when the pipeline is not found", func() {
					BeforeEach(func() {
						Expect(realPipeline.Destroy()).To(Succeed())
					})

				})

				Context("when finding the pipeline fails", func() {
					BeforeEach(func() {
						configTeam.setPipelineError(errors.New("failed"))
					})

					It("returns 500", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})
			})

			Context("when the team is not found", func() {
				BeforeEach(func() {
					Expect(realTeam.Delete()).To(Succeed())
				})

			})

			Context("when finding the team fails", func() {
				BeforeEach(func() {
					configTeamFactory.setFindTeamError(errors.New("failed"))
				})

				It("returns 500", func() {
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				})
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

		})
	})

	Describe("PUT /api/v1/teams/:team_name/pipelines/:name/config", func() {
		var (
			realdb                  *realDB
			deps                    apiDBDeps
			server                  *httptest.Server
			realTeam                db.Team
			realPipeline            db.Pipeline
			configTeam              *configAPITeam
			configTeamFactory       *configAPITeamFactory
			fromVersion             db.ConfigVersion
			originalPipelineConfig  atc.Config
			routeParams             rata.Params
			requestHeader           http.Header
			requestQuery            url.Values
			requestBody             []byte
			scannerSignal           *db.NotifySignal
			response                *http.Response
			saveCalls               func() []configAPISaveCall
			expectOriginalUnchanged func()
			expectPersistedPipeline func(db.Team, atc.PipelineRef, atc.Config, bool, *db.ConfigVersion) db.Pipeline
			expectUpdatedSave       func(atc.PipelineRef, atc.Config)
		)

		BeforeEach(func() {
			realdb = useRealDB()
			deps = realdb.Deps

			var err error
			realTeam, err = deps.teamFactory.CreateTeam(atc.Team{Name: "a-team"})
			Expect(err).NotTo(HaveOccurred())
			realPipeline = realdb.SavePipeline(realTeam, "a-pipeline", pipelineConfig)
			fromVersion = realPipeline.ConfigVersion()
			originalPipelineConfig, err = realPipeline.Config()
			Expect(err).NotTo(HaveOccurred())

			configTeam = &configAPITeam{Team: realTeam, state: &configAPITeamState{}}
			configTeamFactory = &configAPITeamFactory{
				TeamFactory: deps.teamFactory,
				team:        configTeam,
			}
			deps.teamFactory = configTeamFactory

			routeParams = rata.Params{
				"team_name":     "a-team",
				"pipeline_name": "a-pipeline",
			}
			requestHeader = make(http.Header)
			requestQuery = make(url.Values)
			requestBody = nil

			scannerSignal, err = realdb.Conn.Bus().ListenSignal(atc.ComponentLidarScanner)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				Expect(realdb.Conn.Bus().UnlistenSignal(atc.ComponentLidarScanner, scannerSignal)).To(Succeed())
			})

			saveCalls = func() []configAPISaveCall {
				GinkgoHelper()
				calls, err := configTeam.saveCallSnapshot()
				Expect(err).NotTo(HaveOccurred())
				return calls
			}

			expectPersistedPipeline = func(
				team db.Team,
				ref atc.PipelineRef,
				expectedConfig atc.Config,
				expectedPaused bool,
				previousVersion *db.ConfigVersion,
			) db.Pipeline {
				GinkgoHelper()
				pipeline, found, err := team.Pipeline(ref)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				persistedConfig, err := pipeline.Config()
				Expect(err).NotTo(HaveOccurred())
				Expect(persistedConfig).To(Equal(expectedConfig))
				Expect(pipeline.Paused()).To(Equal(expectedPaused))
				if previousVersion == nil {
					Expect(pipeline.ConfigVersion()).NotTo(BeZero())
				} else {
					Expect(pipeline.ConfigVersion()).NotTo(Equal(*previousVersion))
				}
				return pipeline
			}

			expectOriginalUnchanged = func() {
				GinkgoHelper()
				pipeline, found, err := realTeam.Pipeline(atc.PipelineRef{Name: "a-pipeline"})
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				persistedConfig, err := pipeline.Config()
				Expect(err).NotTo(HaveOccurred())
				Expect(persistedConfig).To(Equal(originalPipelineConfig))
				Expect(pipeline.ConfigVersion()).To(Equal(fromVersion))
				Expect(pipeline.Paused()).To(BeFalse())
			}

			expectUpdatedSave = func(ref atc.PipelineRef, expectedConfig atc.Config) {
				GinkgoHelper()
				Expect(saveCalls()).To(Equal([]configAPISaveCall{{
					ref:             ref,
					config:          expectedConfig,
					from:            fromVersion,
					initiallyPaused: true,
				}}))
				expectPersistedPipeline(realTeam, ref, expectedConfig, false, &fromVersion)
			}

		})

		JustBeforeEach(func() {
			realdb.Deps = deps
			server = realdb.Serve()
			requestGenerator := rata.NewRequestGenerator(server.URL, atc.Routes)
			request, err := requestGenerator.CreateRequest(atc.SaveConfig, routeParams, nil)
			Expect(err).NotTo(HaveOccurred())
			request.Header = requestHeader.Clone()
			request.URL.RawQuery = requestQuery.Encode()
			request.Body = io.NopCloser(bytes.NewReader(requestBody))

			response, err = client.Do(request)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(response.Body.Close)
		})

		Context("when authorized", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(true)
				fakeAccess.IsAuthorizedReturns(true)
			})

			Context("when a config version is specified", func() {
				BeforeEach(func() {
					requestHeader.Set(atc.ConfigVersionHeader, strconv.FormatInt(int64(fromVersion), 10))
				})

				Context("when the config is malformed", func() {
					Context("JSON", func() {
						BeforeEach(func() {
							requestHeader.Set("Content-Type", "application/json")
							requestBody = []byte(`{`)
						})

						It("does not save anything", func() {
							Expect(saveCalls()).To(BeEmpty())
							expectOriginalUnchanged()
						})
					})

					Context("YAML", func() {
						BeforeEach(func() {
							requestHeader.Set("Content-Type", "application/x-yaml")
							requestBody = []byte(`{`)
						})

						It("does not save anything", func() {
							Expect(saveCalls()).To(BeEmpty())
							expectOriginalUnchanged()
						})
					})
				})

				Context("when the config is valid", func() {
					Context("JSON", func() {
						BeforeEach(func() {
							requestHeader.Set("Content-Type", "application/json")

							payload, err := json.Marshal(pipelineConfig)
							Expect(err).NotTo(HaveOccurred())

							requestBody = payload
						})

						It("notifies the scanner to run", func() {
							Expect(configTeamFactory.notifyResourceScannerCallCount()).To(Equal(1))
							Eventually(scannerSignal.C()).Should(Receive())
						})

						It("saves it initially paused", func() {
							expectUpdatedSave(atc.PipelineRef{Name: "a-pipeline"}, pipelineConfig)
						})

						Context("and saving it fails", func() {
							BeforeEach(func() {
								configTeam.setSaveError(errors.New("oh no!"))
							})

							It("returns 500", func() {
								Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
							})

							It("returns the error in the response body", func() {
								Expect(io.ReadAll(response.Body)).To(Equal([]byte("failed to save config: oh no!")))
								expectOriginalUnchanged()
							})
						})

						Context("when it's the first time the pipeline has been created", func() {
							BeforeEach(func() {
								Expect(realPipeline.Destroy()).To(Succeed())
							})

							It("does not notify the scanner to run", func() {
								Expect(configTeamFactory.notifyResourceScannerCallCount()).To(BeZero())
								Consistently(scannerSignal.C()).ShouldNot(Receive())
							})
						})

						Context("when the config is invalid", func() {
							BeforeEach(func() {
								pipelineConfig.Groups[0].Resources = []string{"missing-resource"}
								payload, err := json.Marshal(pipelineConfig)
								Expect(err).NotTo(HaveOccurred())
								requestBody = payload
							})

							It("does not save it", func() {
								Expect(saveCalls()).To(BeEmpty())
								expectOriginalUnchanged()
							})
						})
					})

					Context("YAML", func() {
						BeforeEach(func() {
							requestHeader.Set("Content-Type", "application/x-yaml")

							payload, err := yaml.Marshal(pipelineConfig)
							Expect(err).NotTo(HaveOccurred())

							requestBody = payload
						})

						It("notifies the scanner to run", func() {
							Expect(configTeamFactory.notifyResourceScannerCallCount()).To(Equal(1))
							Eventually(scannerSignal.C()).Should(Receive())
						})

						It("saves it initially paused", func() {
							expectUpdatedSave(atc.PipelineRef{Name: "a-pipeline"}, pipelineConfig)
						})

						Context("when the payload contains suspicious types", func() {
							BeforeEach(func() {
								payload := `---
resources:
- name: some-resource
  type: some-type
  check_every: 10s
  check_timeout: 1m
jobs:
- name: some-job
  plan:
  - get: some-resource
  - task: some-task
    config:
      platform: linux
      run:
        path: ls
      params:
        FOO: true
        BAR: 1
        BAZ: 1.9`

								requestHeader.Set("Content-Type", "application/x-yaml")
								requestBody = []byte(payload)
							})

						})

						Describe("test validate cred params when the check_creds param is set in request", func() {
							var (
								payload string
							)

							BeforeEach(func() {
								requestQuery.Add(atc.SaveConfigCheckCreds, "")
							})

							ExpectCredsValidationPass := func() {
								Context("when the param exists in creds manager", func() {
									BeforeEach(func() {
										secretManager = configAPISecrets("BAR", "this-string-value-doesn't-matter")
									})

								})
							}

							ExpectCredsValidationFail := func() {
								Context("when the param does not exist in creds manager", func() {
									BeforeEach(func() {
										secretManager = configAPISecrets("SOME-OTHER-VAR", "this-string-value-doesn't-matter")
									})

									It("fail validation", func() {
										Expect(saveCalls()).To(BeEmpty())
										expectOriginalUnchanged()
									})

								})
							}
							Context("when there is param in resource type config", func() {
								BeforeEach(func() {
									payload = `---
resource_types:
- name: some-type
  type: some-base-resource-type
  source:
    FOO: ((BAR))

jobs:
- name: some-job
  plan:
  - task: some-task
    file: some/task/config.yaml`

									requestHeader.Set("Content-Type", "application/x-yaml")
									requestBody = []byte(payload)
								})

								ExpectCredsValidationPass()
								ExpectCredsValidationFail()
							})

							Context("when there is param in resource source config", func() {
								BeforeEach(func() {
									payload = `---
resources:
- name: some-resource
  type: some-type
  source:
    FOO: ((BAR))
jobs:
- name: some-job
  plan:
  - get: some-resource`

									requestHeader.Set("Content-Type", "application/x-yaml")
									requestBody = []byte(payload)
								})

								ExpectCredsValidationPass()
								ExpectCredsValidationFail()
							})

							Context("when there is param in resource webhook token", func() {
								BeforeEach(func() {
									payload = `---
resources:
- name: some-resource
  type: some-type
  webhook_token: ((BAR))
jobs:
- name: some-job
  plan:
  - get: some-resource`

									requestHeader.Set("Content-Type", "application/x-yaml")
									requestBody = []byte(payload)
								})

								ExpectCredsValidationPass()
								ExpectCredsValidationFail()
							})

							Context("when it contains task that uses external config file and params in task params", func() {
								BeforeEach(func() {
									payload = `---
resources:
- name: some-resource
  type: some-type
  check_every: 10s
jobs:
- name: some-job
  plan:
  - get: some-resource
  - task: some-task
    file: some-resource/config.yml
    params:
      FOO: ((BAR))`

									requestHeader.Set("Content-Type", "application/x-yaml")
									requestBody = []byte(payload)
								})

								ExpectCredsValidationPass()
								ExpectCredsValidationFail()
							})

							Context("when it contains task that uses external config file and params in task vars", func() {
								BeforeEach(func() {
									payload = `---
resources:
- name: some-resource
  type: some-type
  check_every: 10s
jobs:
- name: some-job
  plan:
  - get: some-resource
  - task: some-task
    file: some-resource/config.yml
    vars:
      FOO: ((BAR))`

									requestHeader.Set("Content-Type", "application/x-yaml")
									requestBody = []byte(payload)
								})

								ExpectCredsValidationPass()
								ExpectCredsValidationFail()
							})

							Context("when it contains nested task that uses external config file and params in task vars", func() {
								BeforeEach(func() {
									payload = `---
resources:
- name: some-resource
  type: some-type
  check_every: 10s
jobs:
- name: some-job
  plan:
  - get: some-resource
  - do:
    - task: some-task
      file: some-resource/config.yml
      vars:
        FOO: ((BAR))`

									requestHeader.Set("Content-Type", "application/x-yaml")
									requestBody = []byte(payload)
								})

								ExpectCredsValidationPass()
								ExpectCredsValidationFail()
							})
						})

						Context("when it's the first time the pipeline has been created", func() {
							BeforeEach(func() {
								Expect(realPipeline.Destroy()).To(Succeed())
							})

							It("does not notify the scanner to run", func() {
								Expect(configTeamFactory.notifyResourceScannerCallCount()).To(BeZero())
								Consistently(scannerSignal.C()).ShouldNot(Receive())
							})
						})

						Context("and saving it fails", func() {
							BeforeEach(func() {
								configTeam.setSaveError(errors.New("oh no!"))
							})

							It("returns 500", func() {
								Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
							})

							It("returns the error in the response body", func() {
								Expect(io.ReadAll(response.Body)).To(Equal([]byte("failed to save config: oh no!")))
								expectOriginalUnchanged()
							})
						})

						Context("when the config is invalid", func() {
							BeforeEach(func() {
								pipelineConfig.Groups[0].Resources = []string{"missing-resource"}
								payload, err := json.Marshal(pipelineConfig)
								Expect(err).NotTo(HaveOccurred())
								requestBody = payload
							})

							It("does not save it", func() {
								Expect(saveCalls()).To(BeEmpty())
								expectOriginalUnchanged()
							})
						})

						Context("when instance vars are specified", func() {
							Context("when instance vars are malformed", func() {
								BeforeEach(func() {
									requestQuery.Add("vars.foo", "{")
								})

								It("does not save anything", func() {
									Expect(saveCalls()).To(BeEmpty())
									expectOriginalUnchanged()
								})
							})

							Context("when instance vars is valid", func() {
								BeforeEach(func() {
									requestQuery.Add("vars", "{\"branch\":\"feature\"}")
								})

							})
						})
					})

					Context("there is a problem fetching the team", func() {
						BeforeEach(func() {
							requestHeader.Set("Content-Type", "application/json")

							payload, err := json.Marshal(pipelineConfig)
							Expect(err).NotTo(HaveOccurred())

							requestBody = payload
						})

						Context("when the team is not found", func() {
							BeforeEach(func() {
								Expect(realTeam.Delete()).To(Succeed())
							})

						})

						Context("when finding the team fails", func() {
							BeforeEach(func() {
								configTeamFactory.setFindTeamError(errors.New("failed"))
							})

							It("returns 500", func() {
								Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
							})
						})
					})

				})

				Context("when the Content-Type is unsupported", func() {
					BeforeEach(func() {
						requestHeader.Set("Content-Type", "application/x-toml")

						payload, err := yaml.Marshal(pipelineConfig)
						Expect(err).NotTo(HaveOccurred())

						requestBody = payload
					})

					It("does not save it", func() {
						Expect(saveCalls()).To(BeEmpty())
						expectOriginalUnchanged()
					})
				})

				Context("when the config contains extra keys at the toplevel", func() {
					BeforeEach(func() {
						requestHeader.Set("Content-Type", "application/json")

						remoraPayload, err := json.Marshal(map[string]any{
							"extra": "noooooo",

							"meta": map[string]any{
								"whoa": "lol",
							},

							"jobs": []map[string]any{
								{
									"name":   "some-job",
									"public": true,
									"plan":   []atc.Step{},
								},
							},
						})
						Expect(err).NotTo(HaveOccurred())

						requestBody = remoraPayload
					})

				})

				Context("when the config contains extra keys nested under a valid key", func() {
					BeforeEach(func() {
						requestHeader.Set("Content-Type", "application/json")

						remoraPayload, err := json.Marshal(map[string]any{
							"extra": "noooooo",

							"jobs": []map[string]any{
								{
									"name":  "some-job",
									"pubic": true,
									"plan":  []atc.Step{},
								},
							},
						})
						Expect(err).NotTo(HaveOccurred())

						requestBody = remoraPayload
					})

					It("does not save it", func() {
						Expect(saveCalls()).To(BeEmpty())
						expectOriginalUnchanged()
					})
				})
			})

			Context("when a config version is malformed", func() {
				BeforeEach(func() {
					requestHeader.Set(atc.ConfigVersionHeader, "forty-two")
				})

				It("does not save it", func() {
					Expect(saveCalls()).To(BeEmpty())
					expectOriginalUnchanged()
				})
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

			It("does not save the config", func() {
				Expect(saveCalls()).To(BeEmpty())
				expectOriginalUnchanged()
			})
		})
	})
})
