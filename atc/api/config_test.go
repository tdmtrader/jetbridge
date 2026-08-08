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
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/creds/noop"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	. "github.com/concourse/concourse/atc/testhelpers"
	"github.com/onsi/gomega/gbytes"
	"github.com/tedsuo/rata"
	"sigs.k8s.io/yaml"

	// load dummy credential manager
	_ "github.com/concourse/concourse/atc/creds/dummy"

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

var _ = Describe("Config API", func() {
	var (
		pipelineConfig   atc.Config
		requestGenerator *rata.RequestGenerator
	)

	BeforeEach(func() {
		requestGenerator = rata.NewRequestGenerator(server.URL, atc.Routes)

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

							It("returns 400", func() {
								Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
							})

							It("returns Content-Type 'application/json'", func() {
								expectedHeaderEntries := map[string]string{
									"Content-Type": "application/json",
								}
								Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
							})

							It("returns an error in the response body", func() {
								Expect(io.ReadAll(response.Body)).To(MatchJSON(`
										{
											"errors": [
												"instance vars are malformed: unexpected end of JSON input"
											]
										}`))
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
						It("returns 200", func() {
							Expect(response.StatusCode).To(Equal(http.StatusOK))
						})

						It("returns Content-Type 'application/json' and config version as X-Concourse-Config-Version", func() {
							expectedHeaderEntries := map[string]string{
								"Content-Type":          "application/json",
								atc.ConfigVersionHeader: strconv.Itoa(int(realPipeline.ConfigVersion())),
							}
							Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
						})

						It("returns the config", func() {
							var actualConfigResponse atc.ConfigResponse
							err := json.NewDecoder(response.Body).Decode(&actualConfigResponse)
							Expect(err).NotTo(HaveOccurred())

							Expect(actualConfigResponse).To(Equal(atc.ConfigResponse{
								Config: pipelineConfig,
							}))
						})

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
						It("returns 404", func() {
							Expect(response.StatusCode).To(Equal(http.StatusNotFound))
						})
					})
				})

				Context("when the pipeline is not found", func() {
					BeforeEach(func() {
						Expect(realPipeline.Destroy()).To(Succeed())
					})

					It("returns 404", func() {
						Expect(response.StatusCode).To(Equal(http.StatusNotFound))
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

				It("returns 404", func() {
					Expect(response.StatusCode).To(Equal(http.StatusNotFound))
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

			It("returns 401", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	Describe("PUT /api/v1/teams/:team_name/pipelines/:name/config", func() {
		var (
			request  *http.Request
			response *http.Response
		)

		BeforeEach(func() {
			var err error
			request, err = requestGenerator.CreateRequest(atc.SaveConfig, rata.Params{
				"team_name":     "a-team",
				"pipeline_name": "a-pipeline",
			}, nil)
			Expect(err).NotTo(HaveOccurred())
		})

		JustBeforeEach(func() {
			var err error
			response, err = client.Do(request)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authorized", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(true)
				fakeAccess.IsAuthorizedReturns(true)
			})

			Context("when an identifier is invalid", func() {
				Context("and is a string", func() {
					BeforeEach(func() {
						var err error
						request, err = requestGenerator.CreateRequest(atc.SaveConfig, rata.Params{
							"team_name":     "_team",
							"pipeline_name": "_pipeline",
						}, nil)
						Expect(err).NotTo(HaveOccurred())

						request.Header.Set("Content-Type", "application/json")

						payload, err := json.Marshal(pipelineConfig)
						Expect(err).NotTo(HaveOccurred())

						request.Body = gbytes.BufferWithBytes(payload)
					})

					It("returns warnings in the response body", func() {
						Expect(io.ReadAll(response.Body)).To(MatchJSON(`
							{
								"warnings": [
									{
										"type": "invalid_identifier",
										"message": "pipeline: '_pipeline' is not a valid identifier: must start with a lowercase letter or a number"
									},
									{
										"type": "invalid_identifier",
										"message": "team: '_team' is not a valid identifier: must start with a lowercase letter or a number"
									}
								]
							}`))
					})
				})
				Context("and is an empty string", func() {
					BeforeEach(func() {
						var err error
						request, err = requestGenerator.CreateRequest(atc.SaveConfig, rata.Params{
							"team_name":     "",
							"pipeline_name": "",
						}, nil)
						Expect(err).NotTo(HaveOccurred())

						request.Header.Set("Content-Type", "application/json")

						payload, err := json.Marshal(pipelineConfig)
						Expect(err).NotTo(HaveOccurred())

						request.Body = gbytes.BufferWithBytes(payload)
					})

					It("returns warnings in the response body", func() {
						Expect(io.ReadAll(response.Body)).To(MatchJSON(`
							{
								"errors": [
										"pipeline: identifier cannot be an empty string"
								]
							}`))
					})
				})

			})

			Context("when a config version is specified", func() {
				BeforeEach(func() {
					request.Header.Set(atc.ConfigVersionHeader, "42")
				})

				Context("when the config is malformed", func() {
					Context("JSON", func() {
						BeforeEach(func() {
							request.Header.Set("Content-Type", "application/json")
							request.Body = gbytes.BufferWithBytes([]byte(`{`))
						})

						It("returns 400", func() {
							Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
						})

						It("returns Content-Type 'application/json'", func() {
							expectedHeaderEntries := map[string]string{
								"Content-Type": "application/json",
							}
							Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
						})

						It("returns error JSON", func() {
							Expect(io.ReadAll(response.Body)).To(MatchJSON(`
								{
									"errors": [
										"malformed config: error converting YAML to JSON: yaml: line 1: did not find expected node content"
									]
								}`))
						})

						It("does not save anything", func() {
							Expect(dbTeam.SavePipelineCallCount()).To(Equal(0))
						})
					})

					Context("YAML", func() {
						BeforeEach(func() {
							request.Header.Set("Content-Type", "application/x-yaml")
							request.Body = gbytes.BufferWithBytes([]byte(`{`))
						})

						It("returns 400", func() {
							Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
						})

						It("returns Content-Type 'application/json'", func() {
							expectedHeaderEntries := map[string]string{
								"Content-Type": "application/json",
							}
							Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
						})

						It("returns error JSON", func() {
							Expect(io.ReadAll(response.Body)).To(MatchJSON(`
								{
									"errors": [
										"malformed config: error converting YAML to JSON: yaml: line 1: did not find expected node content"
									]
								}`))
						})

						It("does not save anything", func() {
							Expect(dbTeam.SavePipelineCallCount()).To(Equal(0))
						})
					})
				})

				Context("when the config is valid", func() {
					Context("JSON", func() {
						BeforeEach(func() {
							request.Header.Set("Content-Type", "application/json")

							payload, err := json.Marshal(pipelineConfig)
							Expect(err).NotTo(HaveOccurred())

							request.Body = gbytes.BufferWithBytes(payload)
						})

						It("returns 200", func() {
							Expect(response.StatusCode).To(Equal(http.StatusOK))
						})

						It("notifies the scanner to run", func() {
							Expect(dbTeamFactory.NotifyResourceScannerCallCount()).To(Equal(1))
						})

						It("returns Content-Type 'application/json'", func() {
							expectedHeaderEntries := map[string]string{
								"Content-Type": "application/json",
							}
							Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
						})

						It("saves it initially paused", func() {
							Expect(dbTeam.SavePipelineCallCount()).To(Equal(1))

							ref, savedConfig, id, initiallyPaused := dbTeam.SavePipelineArgsForCall(0)
							Expect(ref.Name).To(Equal("a-pipeline"))
							Expect(savedConfig).To(Equal(pipelineConfig))
							Expect(id).To(Equal(db.ConfigVersion(42)))
							Expect(initiallyPaused).To(BeTrue())
						})

						Context("and saving it fails", func() {
							BeforeEach(func() {
								dbTeam.SavePipelineReturns(nil, false, errors.New("oh no!"))
							})

							It("returns 500", func() {
								Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
							})

							It("returns the error in the response body", func() {
								Expect(io.ReadAll(response.Body)).To(Equal([]byte("failed to save config: oh no!")))
							})
						})

						Context("when the pipeline is an immutable workflow-run template", func() {
							BeforeEach(func() {
								dbTeam.SavePipelineReturns(nil, false, db.ErrWorkflowRunTemplateImmutable)
							})

							It("returns 409 Conflict", func() {
								Expect(response.StatusCode).To(Equal(http.StatusConflict))
							})
						})

						Context("when it's the first time the pipeline has been created", func() {
							BeforeEach(func() {
								returnedPipeline := new(dbfakes.FakePipeline)
								dbTeam.SavePipelineReturns(returnedPipeline, true, nil)
							})

							It("returns 201", func() {
								Expect(response.StatusCode).To(Equal(http.StatusCreated))
							})

							It("does not notify the scanner to run", func() {
								Expect(dbTeamFactory.NotifyResourceScannerCallCount()).To(Equal(0))
							})
						})

						Context("when the config is invalid", func() {
							BeforeEach(func() {
								pipelineConfig.Groups[0].Resources = []string{"missing-resource"}
								payload, err := json.Marshal(pipelineConfig)
								Expect(err).NotTo(HaveOccurred())
								request.Body = gbytes.BufferWithBytes(payload)
							})

							It("returns 400", func() {
								Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
							})

							It("returns Content-Type 'application/json'", func() {
								expectedHeaderEntries := map[string]string{
									"Content-Type": "application/json",
								}
								Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
							})

							It("returns error JSON", func() {
								Expect(io.ReadAll(response.Body)).To(MatchJSON(`
								{
									"errors": [
										"invalid groups:\n\tgroup 'some-group' has unknown resource 'missing-resource'\n"
									]
								}`))
							})

							It("does not save it", func() {
								Expect(dbTeam.SavePipelineCallCount()).To(Equal(0))
							})
						})
					})

					Context("YAML", func() {
						BeforeEach(func() {
							request.Header.Set("Content-Type", "application/x-yaml")

							payload, err := yaml.Marshal(pipelineConfig)
							Expect(err).NotTo(HaveOccurred())

							request.Body = gbytes.BufferWithBytes(payload)
						})

						It("returns 200", func() {
							Expect(response.StatusCode).To(Equal(http.StatusOK))
						})

						It("notifies the scanner to run", func() {
							Expect(dbTeamFactory.NotifyResourceScannerCallCount()).To(Equal(1))
						})

						It("returns Content-Type 'application/json'", func() {
							expectedHeaderEntries := map[string]string{
								"Content-Type": "application/json",
							}
							Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
						})

						It("saves it initially paused", func() {
							Expect(dbTeam.SavePipelineCallCount()).To(Equal(1))

							ref, savedConfig, id, initiallyPaused := dbTeam.SavePipelineArgsForCall(0)
							Expect(ref.Name).To(Equal("a-pipeline"))
							Expect(savedConfig).To(Equal(pipelineConfig))
							Expect(id).To(Equal(db.ConfigVersion(42)))
							Expect(initiallyPaused).To(BeTrue())
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

								request.Header.Set("Content-Type", "application/x-yaml")
								request.Body = io.NopCloser(bytes.NewBufferString(payload))
							})

							It("returns 200", func() {
								Expect(response.StatusCode).To(Equal(http.StatusOK))
							})

							It("returns Content-Type 'application/json'", func() {
								expectedHeaderEntries := map[string]string{
									"Content-Type": "application/json",
								}
								Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
							})

							It("saves it", func() {
								Expect(dbTeam.SavePipelineCallCount()).To(Equal(1))

								ref, savedConfig, id, initiallyPaused := dbTeam.SavePipelineArgsForCall(0)
								Expect(ref.Name).To(Equal("a-pipeline"))
								Expect(savedConfig).To(Equal(atc.Config{
									Resources: []atc.ResourceConfig{
										{
											Name:         "some-resource",
											Type:         "some-type",
											Source:       nil,
											CheckEvery:   &atc.CheckEvery{Interval: 10 * time.Second},
											CheckTimeout: "1m",
										},
									},
									Jobs: atc.JobConfigs{
										{
											Name: "some-job",
											PlanSequence: []atc.Step{
												{
													Config: &atc.GetStep{
														Name: "some-resource",
													},
												},
												{
													Config: &atc.TaskStep{
														Name: "some-task",
														Config: &atc.TaskConfig{
															Platform: "linux",

															Run: atc.TaskRunConfig{
																Path: "ls",
															},

															Params: atc.TaskEnv{
																"FOO": "true",
																"BAR": "1",
																"BAZ": "1.9",
															},
														},
													},
												},
											},
										},
									},
								}))
								Expect(id).To(Equal(db.ConfigVersion(42)))
								Expect(initiallyPaused).To(BeTrue())
							})
						})

						Describe("test validate cred params when the check_creds param is set in request", func() {
							var (
								payload string
							)

							BeforeEach(func() {
								query := request.URL.Query()
								query.Add(atc.SaveConfigCheckCreds, "")
								request.URL.RawQuery = query.Encode()
							})

							ExpectCredsValidationPass := func() {
								Context("when the param exists in creds manager", func() {
									BeforeEach(func() {
										fakeSecretManager.GetReturns("this-string-value-doesn't-matter", nil, true, nil)
									})

									It("passes validation", func() {
										Expect(dbTeam.SavePipelineCallCount()).To(Equal(1))
									})

									It("returns 200 ok", func() {
										Expect(response.StatusCode).To(Equal(http.StatusOK))
									})
								})
							}

							ExpectCredsValidationFail := func() {
								Context("when the param does not exist in creds manager", func() {
									BeforeEach(func() {
										fakeSecretManager.GetReturns(nil, nil, false, nil)
									})

									It("fail validation", func() {
										Expect(dbTeam.SavePipelineCallCount()).To(Equal(0))
									})

									It("returns 400", func() {
										Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
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

									request.Header.Set("Content-Type", "application/x-yaml")
									request.Body = io.NopCloser(bytes.NewBufferString(payload))
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

									request.Header.Set("Content-Type", "application/x-yaml")
									request.Body = io.NopCloser(bytes.NewBufferString(payload))
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

									request.Header.Set("Content-Type", "application/x-yaml")
									request.Body = io.NopCloser(bytes.NewBufferString(payload))
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

									request.Header.Set("Content-Type", "application/x-yaml")
									request.Body = io.NopCloser(bytes.NewBufferString(payload))
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

									request.Header.Set("Content-Type", "application/x-yaml")
									request.Body = io.NopCloser(bytes.NewBufferString(payload))
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

									request.Header.Set("Content-Type", "application/x-yaml")
									request.Body = io.NopCloser(bytes.NewBufferString(payload))
								})

								ExpectCredsValidationPass()
								ExpectCredsValidationFail()
							})
						})

						Context("when it contains credentials to be interpolated", func() {
							var (
								payloadAsConfig atc.Config
								payload         string
							)

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
    config:
      platform: linux
      run:
        path: ls
      params:
        FOO: ((BAR))`
								payloadAsConfig = atc.Config{Resources: []atc.ResourceConfig{
									{
										Name:       "some-resource",
										Type:       "some-type",
										Source:     nil,
										CheckEvery: &atc.CheckEvery{Interval: 10 * time.Second},
									},
								},
									Jobs: atc.JobConfigs{
										{
											Name: "some-job",
											PlanSequence: []atc.Step{
												{
													Config: &atc.GetStep{
														Name: "some-resource",
													},
												},
												{
													Config: &atc.TaskStep{
														Name: "some-task",
														Config: &atc.TaskConfig{
															Platform: "linux",

															Run: atc.TaskRunConfig{
																Path: "ls",
															},

															Params: atc.TaskEnv{
																"FOO": "((BAR))",
															},
														},
													},
												},
											},
										},
									},
								}

								request.Header.Set("Content-Type", "application/x-yaml")
								request.Body = io.NopCloser(bytes.NewBufferString(payload))
							})

							Context("when the check_creds param is set", func() {
								BeforeEach(func() {
									query := request.URL.Query()
									query.Add(atc.SaveConfigCheckCreds, "")
									request.URL.RawQuery = query.Encode()
								})

								Context("when the credential exists in the credential manager", func() {
									BeforeEach(func() {
										fakeSecretManager.GetReturns("this-string-value-doesn't-matter", nil, true, nil)
									})

									It("passes validation and saves it un-interpolated", func() {
										Expect(dbTeam.SavePipelineCallCount()).To(Equal(1))

										ref, savedConfig, id, initiallyPaused := dbTeam.SavePipelineArgsForCall(0)
										Expect(ref.Name).To(Equal("a-pipeline"))
										Expect(savedConfig).To(Equal(payloadAsConfig))
										Expect(id).To(Equal(db.ConfigVersion(42)))
										Expect(initiallyPaused).To(BeTrue())
									})

									It("returns 200", func() {
										Expect(response.StatusCode).To(Equal(http.StatusOK))
									})
								})

								Context("when the credential does not exist in the credential manager", func() {
									BeforeEach(func() {
										fakeSecretManager.GetReturns(nil, nil, false, nil) // nil value, nil expiration, not found, no error
									})

									It("returns 400", func() {
										Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
									})

									It("returns the credential name that was missing", func() {
										Expect(io.ReadAll(response.Body)).To(MatchJSON(`{"errors":["credential validation failed\n\n1 error occurred:\n\t* failed to interpolate task config: undefined vars: BAR\n\n"]}`))
									})
								})

								Context("when a credentials manager is not used", func() {
									BeforeEach(func() {
										fakeSecretManager.GetStub = func(secretPath string) (any, *time.Time, bool, error) {
											return noop.Noop{}.Get(secretPath)
										}
									})

									It("returns 400", func() {
										Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
									})

									It("returns the credential name that was missing", func() {
										Expect(io.ReadAll(response.Body)).To(MatchJSON(`{"errors":["credential validation failed\n\n1 error occurred:\n\t* failed to interpolate task config: undefined vars: BAR\n\n"]}`))
									})
								})
							})

						})

						Context("when it's the first time the pipeline has been created", func() {
							BeforeEach(func() {
								returnedPipeline := new(dbfakes.FakePipeline)
								dbTeam.SavePipelineReturns(returnedPipeline, true, nil)
							})

							It("returns 201", func() {
								Expect(response.StatusCode).To(Equal(http.StatusCreated))
							})

							It("does not notify the scanner to run", func() {
								Expect(dbTeamFactory.NotifyResourceScannerCallCount()).To(Equal(0))
							})
						})

						Context("and saving it fails", func() {
							BeforeEach(func() {
								dbTeam.SavePipelineReturns(nil, false, errors.New("oh no!"))
							})

							It("returns 500", func() {
								Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
							})

							It("returns the error in the response body", func() {
								Expect(io.ReadAll(response.Body)).To(Equal([]byte("failed to save config: oh no!")))
							})
						})

						Context("when the config is invalid", func() {
							BeforeEach(func() {
								pipelineConfig.Groups[0].Resources = []string{"missing-resource"}
								payload, err := json.Marshal(pipelineConfig)
								Expect(err).NotTo(HaveOccurred())
								request.Body = gbytes.BufferWithBytes(payload)
							})

							It("returns 400", func() {
								Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
							})

							It("returns Content-Type 'application/json'", func() {
								expectedHeaderEntries := map[string]string{
									"Content-Type": "application/json",
								}
								Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
							})

							It("returns error JSON", func() {
								Expect(io.ReadAll(response.Body)).To(MatchJSON(`
								{
									"errors": [
										"invalid groups:\n\tgroup 'some-group' has unknown resource 'missing-resource'\n"
									]
								}`))
							})

							It("does not save it", func() {
								Expect(dbTeam.SavePipelineCallCount()).To(BeZero())
							})
						})

						Context("when instance vars are specified", func() {
							Context("when instance vars are malformed", func() {
								BeforeEach(func() {
									query := request.URL.Query()
									query.Add("vars.foo", "{")
									request.URL.RawQuery = query.Encode()
								})

								It("returns 400", func() {
									Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
								})

								It("returns Content-Type 'application/json'", func() {
									expectedHeaderEntries := map[string]string{
										"Content-Type": "application/json",
									}
									Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
								})

								It("returns an error in the response body", func() {
									Expect(io.ReadAll(response.Body)).To(MatchJSON(`
										{
											"errors": [
												"instance vars are malformed: unexpected end of JSON input"
											]
										}`))
								})

								It("does not save anything", func() {
									Expect(dbTeam.SavePipelineCallCount()).To(Equal(0))
								})
							})

							Context("when instance vars is valid", func() {
								BeforeEach(func() {
									query := request.URL.Query()
									query.Add("vars", "{\"branch\":\"feature\"}")
									request.URL.RawQuery = query.Encode()
								})

								It("saves an instanced pipeline", func() {
									Expect(dbTeam.SavePipelineCallCount()).To(Equal(1))

									ref, _, _, _ := dbTeam.SavePipelineArgsForCall(0)
									Expect(ref).To(Equal(atc.PipelineRef{
										Name:         "a-pipeline",
										InstanceVars: atc.InstanceVars{"branch": "feature"},
									}))
								})
							})
						})
					})

					Context("there is a problem fetching the team", func() {
						BeforeEach(func() {
							request.Header.Set("Content-Type", "application/json")

							payload, err := json.Marshal(pipelineConfig)
							Expect(err).NotTo(HaveOccurred())

							request.Body = gbytes.BufferWithBytes(payload)
						})

						Context("when the team is not found", func() {
							BeforeEach(func() {
								dbTeamFactory.FindTeamReturns(nil, false, nil)
							})

							It("returns 404", func() {
								Expect(response.StatusCode).To(Equal(http.StatusNotFound))
							})
						})

						Context("when finding the team fails", func() {
							BeforeEach(func() {
								dbTeamFactory.FindTeamReturns(nil, false, errors.New("failed"))
							})

							It("returns 500", func() {
								Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
							})
						})
					})

				})

				Context("when the Content-Type is unsupported", func() {
					BeforeEach(func() {
						request.Header.Set("Content-Type", "application/x-toml")

						payload, err := yaml.Marshal(pipelineConfig)
						Expect(err).NotTo(HaveOccurred())

						request.Body = gbytes.BufferWithBytes(payload)
					})

					It("returns Unsupported Media Type", func() {
						Expect(response.StatusCode).To(Equal(http.StatusUnsupportedMediaType))
					})

					It("does not save it", func() {
						Expect(dbTeam.SavePipelineCallCount()).To(Equal(0))
					})
				})

				Context("when the config contains extra keys at the toplevel", func() {
					BeforeEach(func() {
						request.Header.Set("Content-Type", "application/json")

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

						request.Body = gbytes.BufferWithBytes(remoraPayload)
					})

					It("returns 200", func() {
						Expect(response.StatusCode).To(Equal(http.StatusOK))
					})

					It("returns Content-Type 'application/json'", func() {
						expectedHeaderEntries := map[string]string{
							"Content-Type": "application/json",
						}
						Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
					})

					It("saves it", func() {
						Expect(dbTeam.SavePipelineCallCount()).To(Equal(1))

						ref, savedConfig, id, initiallyPaused := dbTeam.SavePipelineArgsForCall(0)
						Expect(ref.Name).To(Equal("a-pipeline"))
						Expect(savedConfig).To(Equal(atc.Config{
							Jobs: atc.JobConfigs{
								{
									Name:         "some-job",
									Public:       true,
									PlanSequence: []atc.Step{},
								},
							},
						}))
						Expect(id).To(Equal(db.ConfigVersion(42)))
						Expect(initiallyPaused).To(BeTrue())
					})
				})

				Context("when the config contains extra keys nested under a valid key", func() {
					BeforeEach(func() {
						request.Header.Set("Content-Type", "application/json")

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

						request.Body = gbytes.BufferWithBytes(remoraPayload)
					})

					It("returns 400", func() {
						Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
					})

					It("returns Content-Type 'application/json'", func() {
						expectedHeaderEntries := map[string]string{
							"Content-Type": "application/json",
						}
						Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
					})

					It("returns an error in the response body", func() {
						Expect(io.ReadAll(response.Body)).To(ContainSubstring(`malformed config: error unmarshaling JSON: while decoding JSON: json: unknown field \"pubic\"`))
					})

					It("does not save it", func() {
						Expect(dbTeam.SavePipelineCallCount()).To(Equal(0))
					})
				})
			})

			Context("when a config version is malformed", func() {
				BeforeEach(func() {
					request.Header.Set(atc.ConfigVersionHeader, "forty-two")
				})

				It("returns 400", func() {
					Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
				})

				It("returns Content-Type 'application/json'", func() {
					expectedHeaderEntries := map[string]string{
						"Content-Type": "application/json",
					}
					Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
				})

				It("returns an error in the response body", func() {
					Expect(io.ReadAll(response.Body)).To(MatchJSON(`
							{
								"errors": [
									"config version is malformed: expected integer"
								]
							}`))
				})

				It("does not save it", func() {
					Expect(dbTeam.SavePipelineCallCount()).To(Equal(0))
				})
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				fakeAccess.IsAuthenticatedReturns(false)
			})

			It("returns 401", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})

			It("does not save the config", func() {
				Expect(dbTeam.SavePipelineCallCount()).To(Equal(0))
			})
		})
	})
})
