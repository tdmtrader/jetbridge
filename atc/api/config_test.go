package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/configvalidate"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/creds/dummy"
	"github.com/concourse/concourse/atc/creds/noop"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/event"
	. "github.com/concourse/concourse/atc/testhelpers"
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

	Describe("PUT materialized run payload config", func() {
		It("returns a payload mutation conflict before template declaration validation", func() {
			realdb := useRealDB()
			team, err := realdb.Deps.teamFactory.CreateTeam(atc.Team{Name: "run-config-team"})
			Expect(err).NotTo(HaveOccurred())
			keepLast := 1
			template := realdb.SavePipeline(team, "run-config-template", atc.Config{
				Template: true,
				Params:   []atc.ParamSchema{{Name: "environment", Type: atc.ParamTypeString, Required: true}},
				RunRetention: &atc.RunRetentionConfig{
					KeepLast: &keepLast,
				},
				Jobs: atc.JobConfigs{{Name: "entry"}},
			})
			creation, err := db.NewPipelineRunFactory(realdb.Conn, realdb.LockFactory).CreateRun(
				context.Background(), template, db.RunParams{Vars: atc.RunParams{"environment": "production"}}, "api-user",
			)
			Expect(err).NotTo(HaveOccurred())
			payload, found, err := team.Pipeline(atc.PipelineRef{
				Name: template.Name(), InstanceVars: atc.InstanceVars{"run": float64(creation.Run.Number())},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())

			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			server := realdb.Serve()
			generator := rata.NewRequestGenerator(server.URL, atc.Routes)
			routeParams := rata.Params{"team_name": team.Name(), "pipeline_name": template.Name()}
			query := payload.PipelineRef().QueryParams().Encode()
			get, err := generator.CreateRequest(atc.GetConfig, routeParams, nil)
			Expect(err).NotTo(HaveOccurred())
			get.URL.RawQuery = query
			getResponse, err := client.Do(get)
			Expect(err).NotTo(HaveOccurred())
			Expect(getResponse.StatusCode).To(Equal(http.StatusOK))
			var materialized atc.ConfigResponse
			Expect(json.NewDecoder(getResponse.Body).Decode(&materialized)).To(Succeed())
			Expect(getResponse.Body.Close()).To(Succeed())
			Expect(materialized.Config.Template).To(BeFalse())
			// A payload declares nothing of its own: what GetConfig emits for a run must be
			// something SaveConfig would accept, not a config refused for carrying params
			// while template is false.
			Expect(materialized.Config.Params).To(BeNil())
			Expect(materialized.Config.RunRetention).To(BeNil())
			Expect(configvalidate.ValidateTemplateDeclaration(payload.PipelineRef(), materialized.Config)).To(Succeed())

			// The PUT still carries a declaration, so the payload-mutation conflict is
			// still proven to win over template declaration validation.
			declared := materialized.Config
			declared.Params = []atc.ParamSchema{{Name: "environment", Type: atc.ParamTypeString, Required: true}}
			body, err := json.Marshal(declared)
			Expect(err).NotTo(HaveOccurred())
			put, err := generator.CreateRequest(atc.SaveConfig, routeParams, nil)
			Expect(err).NotTo(HaveOccurred())
			put.URL.RawQuery = query
			put.Header.Set("Content-Type", "application/json")
			put.Body = io.NopCloser(bytes.NewReader(body))
			putResponse, err := client.Do(put)
			Expect(err).NotTo(HaveOccurred())
			defer putResponse.Body.Close()

			Expect(putResponse.StatusCode).To(Equal(http.StatusConflict))
			responseBody, err := io.ReadAll(putResponse.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(responseBody)).To(ContainSubstring(db.ErrPipelineRunPayloadMutation.Error()))
		})
	})

	Describe("PUT ordinary pipeline template conversion", func() {
		It("preserves ordinary job history while allowing a fresh pipeline to become a template", func() {
			realdb := useRealDB()
			team, err := realdb.Deps.teamFactory.CreateTeam(atc.Team{Name: "ordinary-conversion-team"})
			Expect(err).NotTo(HaveOccurred())
			ordinary := realdb.SavePipeline(team, "ordinary-history", atc.Config{Jobs: atc.JobConfigs{{Name: "entry"}}})
			job, found, err := ordinary.Job("entry")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			build, err := job.CreateBuild("manual-user")
			Expect(err).NotTo(HaveOccurred())

			fakeAccess.IsAuthenticatedReturns(true)
			fakeAccess.IsAuthorizedReturns(true)
			server := realdb.Serve()
			generator := rata.NewRequestGenerator(server.URL, atc.Routes)
			templateConfig := atc.Config{Template: true, Jobs: atc.JobConfigs{{Name: "entry"}}}
			save := func(pipeline db.Pipeline) *http.Response {
				GinkgoHelper()
				body, err := json.Marshal(templateConfig)
				Expect(err).NotTo(HaveOccurred())
				request, err := generator.CreateRequest(atc.SaveConfig, rata.Params{
					"team_name": team.Name(), "pipeline_name": pipeline.Name(),
				}, nil)
				Expect(err).NotTo(HaveOccurred())
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set(atc.ConfigVersionHeader, strconv.Itoa(int(pipeline.ConfigVersion())))
				request.Body = io.NopCloser(bytes.NewReader(body))
				response, err := client.Do(request)
				Expect(err).NotTo(HaveOccurred())
				return response
			}

			response := save(ordinary)
			Expect(response.StatusCode).To(Equal(http.StatusConflict))
			body, err := io.ReadAll(response.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(response.Body.Close()).To(Succeed())
			Expect(string(body)).To(ContainSubstring("ordinary job history or task caches"))
			Expect(ordinary.Reload()).To(BeTrue())
			Expect(ordinary.Template()).To(BeFalse())
			Expect(build.SaveEvent(event.Log{Payload: "still reachable"})).To(Succeed())
			var events int
			Expect(realdb.Conn.QueryRow(
				fmt.Sprintf("SELECT count(*) FROM pipeline_build_events_%d WHERE build_id = $1", ordinary.ID()), build.ID(),
			).Scan(&events)).To(Succeed())
			Expect(events).To(BeNumerically(">", 0))

			fresh := realdb.SavePipeline(team, "ordinary-fresh", atc.Config{Jobs: atc.JobConfigs{{Name: "entry"}}})
			response = save(fresh)
			Expect(response.StatusCode).To(Equal(http.StatusOK))
			Expect(response.Body.Close()).To(Succeed())
			Expect(fresh.Reload()).To(BeTrue())
			Expect(fresh.Template()).To(BeTrue())
		})
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
			realdb                  *realDB
			deps                    apiDBDeps
			server                  *httptest.Server
			realTeam                db.Team
			realRequestedTeam       db.Team
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

			Context("when an identifier is invalid", func() {
				Context("and is a string", func() {
					BeforeEach(func() {
						var err error
						realRequestedTeam, err = configTeamFactory.TeamFactory.CreateTeam(atc.Team{Name: "_team"})
						Expect(err).NotTo(HaveOccurred())
						routeParams = rata.Params{
							"team_name":     "_team",
							"pipeline_name": "_pipeline",
						}

						requestHeader.Set("Content-Type", "application/json")

						payload, err := json.Marshal(pipelineConfig)
						Expect(err).NotTo(HaveOccurred())

						requestBody = payload
					})

					It("returns warnings in the response body", func() {
						Expect(response.StatusCode).To(Equal(http.StatusCreated))
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
						persisted := expectPersistedPipeline(
							realRequestedTeam,
							atc.PipelineRef{Name: "_pipeline"},
							pipelineConfig,
							true,
							nil,
						)
						Expect(persisted.ConfigVersion()).NotTo(BeZero())
					})
				})
				Context("and is an empty string", func() {
					BeforeEach(func() {
						routeParams = rata.Params{
							"team_name":     "",
							"pipeline_name": "",
						}

						requestHeader.Set("Content-Type", "application/json")

						payload, err := json.Marshal(pipelineConfig)
						Expect(err).NotTo(HaveOccurred())

						requestBody = payload
					})

					It("returns warnings in the response body", func() {
						Expect(io.ReadAll(response.Body)).To(MatchJSON(`
							{
								"errors": [
										"pipeline: identifier cannot be an empty string"
								]
							}`))
						Expect(configTeamFactory.findTeamCallSnapshot()).To(BeEmpty())
						Expect(saveCalls()).To(BeEmpty())
						expectOriginalUnchanged()
					})
				})

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
							Expect(saveCalls()).To(BeEmpty())
							expectOriginalUnchanged()
						})
					})

					Context("YAML", func() {
						BeforeEach(func() {
							requestHeader.Set("Content-Type", "application/x-yaml")
							requestBody = []byte(`{`)
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

						It("returns 200", func() {
							Expect(response.StatusCode).To(Equal(http.StatusOK))
						})

						It("notifies the scanner to run", func() {
							Expect(configTeamFactory.notifyResourceScannerCallCount()).To(Equal(1))
							Eventually(scannerSignal.C()).Should(Receive())
						})

						It("returns Content-Type 'application/json'", func() {
							expectedHeaderEntries := map[string]string{
								"Content-Type": "application/json",
							}
							Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
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

							It("returns 201", func() {
								Expect(response.StatusCode).To(Equal(http.StatusCreated))
								Expect(saveCalls()).To(Equal([]configAPISaveCall{{
									ref:             atc.PipelineRef{Name: "a-pipeline"},
									config:          pipelineConfig,
									from:            fromVersion,
									initiallyPaused: true,
								}}))
								expectPersistedPipeline(realTeam, atc.PipelineRef{Name: "a-pipeline"}, pipelineConfig, true, nil)
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

						It("returns 200", func() {
							Expect(response.StatusCode).To(Equal(http.StatusOK))
						})

						It("notifies the scanner to run", func() {
							Expect(configTeamFactory.notifyResourceScannerCallCount()).To(Equal(1))
							Eventually(scannerSignal.C()).Should(Receive())
						})

						It("returns Content-Type 'application/json'", func() {
							expectedHeaderEntries := map[string]string{
								"Content-Type": "application/json",
							}
							Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
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
								expectedConfig := atc.Config{
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
								}
								expectUpdatedSave(atc.PipelineRef{Name: "a-pipeline"}, expectedConfig)
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

									It("passes validation", func() {
										var expectedConfig atc.Config
										Expect(yaml.Unmarshal([]byte(payload), &expectedConfig)).To(Succeed())
										expectUpdatedSave(atc.PipelineRef{Name: "a-pipeline"}, expectedConfig)
									})

									It("returns 200 ok", func() {
										Expect(response.StatusCode).To(Equal(http.StatusOK))
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

								requestHeader.Set("Content-Type", "application/x-yaml")
								requestBody = []byte(payload)
							})

							Context("when the check_creds param is set", func() {
								BeforeEach(func() {
									requestQuery.Add(atc.SaveConfigCheckCreds, "")
								})

								Context("when the credential exists in the credential manager", func() {
									BeforeEach(func() {
										secretManager = configAPISecrets("BAR", "this-string-value-doesn't-matter")
									})

									It("passes validation and saves it un-interpolated", func() {
										expectUpdatedSave(atc.PipelineRef{Name: "a-pipeline"}, payloadAsConfig)
									})

									It("returns 200", func() {
										Expect(response.StatusCode).To(Equal(http.StatusOK))
									})
								})

								Context("when the credential does not exist in the credential manager", func() {
									BeforeEach(func() {
										secretManager = configAPISecrets("SOME-OTHER-VAR", "this-string-value-doesn't-matter")
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
										secretManager = noop.Noop{}
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
								Expect(realPipeline.Destroy()).To(Succeed())
							})

							It("returns 201", func() {
								Expect(response.StatusCode).To(Equal(http.StatusCreated))
								Expect(saveCalls()).To(Equal([]configAPISaveCall{{
									ref:             atc.PipelineRef{Name: "a-pipeline"},
									config:          pipelineConfig,
									from:            fromVersion,
									initiallyPaused: true,
								}}))
								expectPersistedPipeline(realTeam, atc.PipelineRef{Name: "a-pipeline"}, pipelineConfig, true, nil)
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
								Expect(saveCalls()).To(BeEmpty())
								expectOriginalUnchanged()
							})
						})

						Context("when instance vars are specified", func() {
							Context("when instance vars are malformed", func() {
								BeforeEach(func() {
									requestQuery.Add("vars.foo", "{")
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
									Expect(saveCalls()).To(BeEmpty())
									expectOriginalUnchanged()
								})
							})

							Context("when instance vars is valid", func() {
								BeforeEach(func() {
									requestQuery.Add("vars", "{\"branch\":\"feature\"}")
								})

								It("saves an instanced pipeline", func() {
									ref := atc.PipelineRef{
										Name:         "a-pipeline",
										InstanceVars: atc.InstanceVars{"branch": "feature"},
									}
									Expect(saveCalls()).To(Equal([]configAPISaveCall{{
										ref:             ref,
										config:          pipelineConfig,
										from:            fromVersion,
										initiallyPaused: true,
									}}))
									expectPersistedPipeline(realTeam, ref, pipelineConfig, true, nil)
									expectOriginalUnchanged()
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

				})

				Context("when the Content-Type is unsupported", func() {
					BeforeEach(func() {
						requestHeader.Set("Content-Type", "application/x-toml")

						payload, err := yaml.Marshal(pipelineConfig)
						Expect(err).NotTo(HaveOccurred())

						requestBody = payload
					})

					It("returns Unsupported Media Type", func() {
						Expect(response.StatusCode).To(Equal(http.StatusUnsupportedMediaType))
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
						expectedConfig := atc.Config{
							Jobs: atc.JobConfigs{
								{
									Name:         "some-job",
									Public:       true,
									PlanSequence: []atc.Step{},
								},
							},
						}
						expectUpdatedSave(atc.PipelineRef{Name: "a-pipeline"}, expectedConfig)
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
						Expect(saveCalls()).To(BeEmpty())
						expectOriginalUnchanged()
					})
				})
			})

			Context("when a config version is malformed", func() {
				BeforeEach(func() {
					requestHeader.Set(atc.ConfigVersionHeader, "forty-two")
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
					Expect(saveCalls()).To(BeEmpty())
					expectOriginalUnchanged()
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
				Expect(saveCalls()).To(BeEmpty())
				expectOriginalUnchanged()
			})
		})
	})
})
