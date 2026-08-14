package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/creds/dummy"
	"github.com/concourse/concourse/atc/creds/noop"
	"github.com/concourse/concourse/atc/db"
	. "github.com/concourse/concourse/atc/testhelpers"
	"github.com/tedsuo/rata"
	"sigs.k8s.io/yaml"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

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
			realdb       *realDB
			deps         apiDBDeps
			server       *httptest.Server
			realTeam     db.Team
			realPipeline db.Pipeline
			routeParams  rata.Params
			requestQuery url.Values
			response     *http.Response
		)

		BeforeEach(func() {
			realdb = useRealDB()
			deps = realdb.Deps

			var err error
			realTeam, err = deps.teamFactory.CreateTeam(atc.Team{Name: "a-team"})
			Expect(err).NotTo(HaveOccurred())
			realPipeline = realdb.SavePipeline(realTeam, "something-else", pipelineConfig)

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
				grantProfile(realTeam, memberProfile, accessor.ViewerRole)
				useProfile(memberProfile)
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

							It("returns the instanced pipeline config", func() {
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

			})

			Context("when the team is not found", func() {
				BeforeEach(func() {
					Expect(realTeam.Delete()).To(Succeed())
					useProfile(adminProfile)
				})

				It("returns 404", func() {
					Expect(response.StatusCode).To(Equal(http.StatusNotFound))
				})
			})

		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				useProfile(anonymousProfile)
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
			fromVersion             db.ConfigVersion
			originalPipelineConfig  atc.Config
			routeParams             rata.Params
			requestHeader           http.Header
			requestQuery            url.Values
			requestBody             []byte
			scannerSignal           *db.NotifySignal
			response                *http.Response
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
			for {
				select {
				case <-scannerSignal.C():
					continue
				default:
					goto scannerSignalDrained
				}
			}
		scannerSignalDrained:

			response, err = client.Do(request)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(response.Body.Close)
		})

		Context("when authorized", func() {
			BeforeEach(func() {
				grantProfile(realTeam, memberProfile, accessor.MemberRole)
				useProfile(memberProfile)
			})

			Context("when an identifier is invalid", func() {
				Context("and is a string", func() {
					BeforeEach(func() {
						var err error
						realRequestedTeam, err = deps.teamFactory.CreateTeam(atc.Team{Name: "_team"})
						Expect(err).NotTo(HaveOccurred())
						grantProfile(realRequestedTeam, memberProfile, accessor.MemberRole)
						useProfile(memberProfile)
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
						useProfile(adminProfile)
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

						Context("when it's the first time the pipeline has been created", func() {
							BeforeEach(func() {
								Expect(realPipeline.Destroy()).To(Succeed())
							})

							It("returns 201", func() {
								Expect(response.StatusCode).To(Equal(http.StatusCreated))
								expectPersistedPipeline(realTeam, atc.PipelineRef{Name: "a-pipeline"}, pipelineConfig, true, nil)
							})

							It("does not notify the scanner to run", func() {
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
								expectPersistedPipeline(realTeam, atc.PipelineRef{Name: "a-pipeline"}, pipelineConfig, true, nil)
							})

							It("does not notify the scanner to run", func() {
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
								useProfile(adminProfile)
							})

							It("returns 404", func() {
								Expect(response.StatusCode).To(Equal(http.StatusNotFound))
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
					expectOriginalUnchanged()
				})
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				useProfile(anonymousProfile)
			})

			It("returns 401", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})

			It("does not save the config", func() {
				expectOriginalUnchanged()
			})
		})
	})
})
