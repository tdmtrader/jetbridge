package atc_test

import (
	"encoding/json"
	"reflect"
	"strings"

	"github.com/concourse/concourse/atc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/format"
)

var _ = Describe("Plan", func() {
	Describe("Public exhaustiveness", func() {
		// Public() is a hand-maintained mirror of atc.Plan. A step type
		// missing from it serializes as a typeless {"id": ...} object, which
		// the web UI can only render as an anonymous fallback step (this is
		// exactly how the harvest step went missing from the build page).
		It("serializes every step pointer field of Plan under its JSON key", func() {
			planType := reflect.TypeOf(atc.Plan{})
			for i := 0; i < planType.NumField(); i++ {
				field := planType.Field(i)
				if field.Type.Kind() != reflect.Ptr {
					continue
				}
				tag := strings.Split(field.Tag.Get("json"), ",")[0]
				Expect(tag).ToNot(BeEmpty(), "step field %q needs a json tag", field.Name)

				plan := atc.Plan{ID: "1"}
				reflect.ValueOf(&plan).Elem().Field(i).Set(reflect.New(field.Type.Elem()))

				public := plan.Public()
				Expect(public).ToNot(BeNil())

				var decoded map[string]json.RawMessage
				Expect(json.Unmarshal([]byte(*public), &decoded)).To(Succeed())
				Expect(decoded).To(HaveKey(tag),
					"atc.Plan field %q (json %q) is dropped by Plan.Public() — add a case for it so the build page can render the step",
					field.Name, tag)
			}
		})
	})

	Describe("SidecarPlanID", func() {
		It("derives a plan ID from the parent and sidecar name", func() {
			id := atc.SidecarPlanID("42", "cloud-sql-proxy")
			Expect(id).To(Equal(atc.PlanID("42/sidecar/cloud-sql-proxy")))
		})
	})

	Describe("NewSidecarPlan", func() {
		It("constructs a plan with a sidecar and derived ID", func() {
			config := atc.SidecarConfig{
				Name:  "redis",
				Image: "redis:7",
			}
			plan := atc.NewSidecarPlan("10", config)
			Expect(plan.ID).To(Equal(atc.PlanID("10/sidecar/redis")))
			Expect(plan.Sidecar).ToNot(BeNil())
			Expect(plan.Sidecar.Name).To(Equal("redis"))
			Expect(plan.Sidecar.Image).To(Equal("redis:7"))
		})
	})

	Describe("SidecarPlan Public", func() {
		It("serializes name and image", func() {
			plan := atc.Plan{
				ID: "5/sidecar/postgres",
				Sidecar: &atc.SidecarPlan{
					Name:  "postgres",
					Image: "postgres:16",
				},
			}
			json := plan.Public()
			Expect(json).ToNot(BeNil())
			Expect([]byte(*json)).To(MatchJSON(`{
				"id": "5/sidecar/postgres",
				"sidecar": {
					"name": "postgres",
					"image": "postgres:16"
				}
			}`))
		})

		It("omits image when empty", func() {
			plan := atc.Plan{
				ID: "5/sidecar/helper",
				Sidecar: &atc.SidecarPlan{
					Name: "helper",
				},
			}
			json := plan.Public()
			Expect(json).ToNot(BeNil())
			Expect([]byte(*json)).To(MatchJSON(`{
				"id": "5/sidecar/helper",
				"sidecar": {
					"name": "helper"
				}
			}`))
		})
	})

	Describe("AgentPlan Public", func() {
		It("exposes only name and model, redacting prompt, env, budget, and sidecars", func() {
			plan := atc.Plan{
				ID: "7/agent",
				Agent: &atc.AgentPlan{
					Name:           "reviewer",
					Model:          "claude-opus-4",
					Prompt:         "secret prompt text",
					PromptFile:     "prompts/review.md",
					MaxTurns:       12,
					BudgetSliceUSD: 3.50,
					OutputSchema:   "schema.json",
					Sidecars:       []atc.SidecarSource{{File: "sidecars/db.yml"}},
					Inputs:         []string{"repo"},
					Outputs:        []string{"result"},
					Env: map[string]string{
						"AGENT_IDENTITY": "svc-account",
						"SECRET_TOKEN":   "literal-secret",
					},
					Timeout: "10m",
				},
			}
			json := plan.Public()
			Expect(json).ToNot(BeNil())
			Expect([]byte(*json)).To(MatchJSON(`{
				"id": "7/agent",
				"agent": {
					"name": "reviewer",
					"model": "claude-opus-4"
				}
			}`))
		})

		It("omits model when empty", func() {
			plan := atc.Plan{
				ID: "7/agent",
				Agent: &atc.AgentPlan{
					Name:   "reviewer",
					Prompt: "secret prompt text",
				},
			}
			json := plan.Public()
			Expect(json).ToNot(BeNil())
			Expect([]byte(*json)).To(MatchJSON(`{
				"id": "7/agent",
				"agent": {
					"name": "reviewer"
				}
			}`))
		})
	})

	Describe("HarvestPlan Public", func() {
		It("exposes name, repo, and branch under a \"harvest\" key, redacting env, judge, and ids", func() {
			plan := atc.Plan{
				ID: "8/harvest",
				Harvest: &atc.HarvestPlan{
					Name:          "harvest",
					Workspace:     "workspace",
					Repo:          "tdmtrader/jetbridge",
					TargetBranch:  "main",
					TicketID:      28,
					PipelineRunID: 42,
					Branch:        "agent/ticket-28",
					Push:          true,
					Env: map[string]string{
						"SECRET_TOKEN": "literal-secret",
					},
				},
			}
			json := plan.Public()
			Expect(json).ToNot(BeNil())
			Expect([]byte(*json)).To(MatchJSON(`{
				"id": "8/harvest",
				"harvest": {
					"name": "harvest",
					"repo": "tdmtrader/jetbridge",
					"branch": "agent/ticket-28"
				}
			}`))
		})

		It("omits branch when empty", func() {
			plan := atc.Plan{
				ID: "8/harvest",
				Harvest: &atc.HarvestPlan{
					Name: "harvest",
					Repo: "tdmtrader/jetbridge",
				},
			}
			json := plan.Public()
			Expect(json).ToNot(BeNil())
			Expect([]byte(*json)).To(MatchJSON(`{
				"id": "8/harvest",
				"harvest": {
					"name": "harvest",
					"repo": "tdmtrader/jetbridge"
				}
			}`))
		})
	})

	Describe("Public", func() {
		It("returns a sanitized form of the plan", func() {
			plan := atc.Plan{
				ID: "0",
				InParallel: &atc.InParallelPlan{
					Steps: []atc.Plan{
						{
							ID: "1",
							InParallel: &atc.InParallelPlan{
								Steps: []atc.Plan{
									{
										ID: "2",
										Task: &atc.TaskPlan{
											Name:       "name",
											ConfigPath: "some/config/path.yml",
											Config: &atc.TaskConfig{
												Params: atc.TaskEnv{"some": "secret"},
											},
										},
									},
								},
							},
						},
						{
							ID: "3",
							Get: &atc.GetPlan{
								Type:     "type",
								Name:     "name",
								Resource: "resource",
								Source:   atc.Source{"some": "source"},
								Params:   atc.Params{"some": "params"},
								Version:  &atc.Version{"some": "version"},
								Tags:     atc.Tags{"tags"},
								TypeImage: atc.TypeImage{
									BaseType: "some-base-type",
									GetPlan: &atc.Plan{
										ID: "3/image-get",
										Get: &atc.GetPlan{
											Type:   "some-base-type",
											Name:   "name",
											Source: atc.Source{"some": "source"},
											TypeImage: atc.TypeImage{
												BaseType: "some-base-type",
											},
										},
									},
									CheckPlan: &atc.Plan{
										ID: "3/image-check",
										Check: &atc.CheckPlan{
											Type:   "some-base-type",
											Name:   "name",
											Source: atc.Source{"some": "source"},
											TypeImage: atc.TypeImage{
												BaseType: "some-base-type",
											},
										},
									},
								},
							},
						},

						{
							ID: "3.1",
							Get: &atc.GetPlan{
								Name:     "name",
								Resource: "resource",
								Type:     "some-custom-type",
								Source:   atc.Source{"some": "source"},
								Params:   atc.Params{"some": "params"},
								Version:  &atc.Version{"some": "version"},
								Tags:     atc.Tags{"tags"},
								TypeImage: atc.TypeImage{
									BaseType: "some-base-type",
									GetPlan: &atc.Plan{
										ID: "3.1/image-get",
										Get: &atc.GetPlan{
											Name:   "some-custom-type",
											Type:   "second-custom-type",
											Source: atc.Source{"custom": "source"},
											TypeImage: atc.TypeImage{
												BaseType: "some-base-type",
												GetPlan: &atc.Plan{
													ID: "3.1/image-get/image-get",
													Get: &atc.GetPlan{
														Name:   "second-custom-type",
														Type:   "some-base-type",
														Source: atc.Source{"custom": "second-source"},
														TypeImage: atc.TypeImage{
															BaseType: "some-base-type",
														},
													},
												},
												CheckPlan: &atc.Plan{
													ID: "3.1/image-get/image-check",
													Check: &atc.CheckPlan{
														Name:   "second-custom-type",
														Type:   "some-base-type",
														Source: atc.Source{"custom": "second-source"},
														TypeImage: atc.TypeImage{
															BaseType: "some-base-type",
														},
													},
												},
											},
										},
									},
									CheckPlan: &atc.Plan{
										ID: "3.1/image-check",
										Check: &atc.CheckPlan{
											Name:   "some-custom-type",
											Type:   "second-custom-type",
											Source: atc.Source{"custom": "source"},
											TypeImage: atc.TypeImage{
												BaseType: "some-base-type",
												GetPlan: &atc.Plan{
													ID: "3.1/image-check/image-get",
													Get: &atc.GetPlan{
														Name:   "second-custom-type",
														Type:   "some-base-type",
														Source: atc.Source{"custom": "second-source"},
														TypeImage: atc.TypeImage{
															BaseType: "some-base-type",
														},
													},
												},
												CheckPlan: &atc.Plan{
													ID: "3.1/image-check/image-check",
													Check: &atc.CheckPlan{
														Name:   "second-custom-type",
														Type:   "some-base-type",
														Source: atc.Source{"custom": "second-source"},
														TypeImage: atc.TypeImage{
															BaseType: "some-base-type",
														},
													},
												},
											},
										},
									},
								},
							},
						},
						{
							ID: "4",
							Put: &atc.PutPlan{
								Type:     "type",
								Name:     "name",
								Resource: "resource",
								Source:   atc.Source{"some": "source"},
								Params:   atc.Params{"some": "params"},
								Tags:     atc.Tags{"tags"},
								TypeImage: atc.TypeImage{
									BaseType: "some-base-type",
									GetPlan: &atc.Plan{
										ID: "4/image-get",
										Get: &atc.GetPlan{
											Type:   "some-base-type",
											Name:   "name",
											Source: atc.Source{"some": "source"},
											TypeImage: atc.TypeImage{
												BaseType: "some-base-type",
											},
										},
									},
									CheckPlan: &atc.Plan{
										ID: "4/image-check",
										Check: &atc.CheckPlan{
											Type:   "some-base-type",
											Name:   "name",
											Source: atc.Source{"some": "source"},
											TypeImage: atc.TypeImage{
												BaseType: "some-base-type",
											},
										},
									},
								},
							},
						},
						{
							ID: "4.2",
							Check: &atc.CheckPlan{
								Type:   "type",
								Name:   "name",
								Source: atc.Source{"some": "source"},
								Tags:   atc.Tags{"tags"},
								TypeImage: atc.TypeImage{
									BaseType: "some-base-type",
									GetPlan: &atc.Plan{
										ID: "4.2/image-get",
										Get: &atc.GetPlan{
											Type:   "some-base-type",
											Name:   "name",
											Source: atc.Source{"some": "source"},
											TypeImage: atc.TypeImage{
												BaseType: "some-base-type",
											},
										},
									},
									CheckPlan: &atc.Plan{
										ID: "4.2/image-check",
										Check: &atc.CheckPlan{
											Type:   "some-base-type",
											Name:   "name",
											Source: atc.Source{"some": "source"},
											TypeImage: atc.TypeImage{
												BaseType: "some-base-type",
											},
										},
									},
								},
							},
						},

						{
							ID: "5",
							Task: &atc.TaskPlan{
								Name:       "name",
								Privileged: true,
								Hermetic:   true,
								Tags:       atc.Tags{"tags"},
								ConfigPath: "some/config/path.yml",
								Config: &atc.TaskConfig{
									Params: atc.TaskEnv{"some": "secret"},
								},
							},
						},

						{
							ID: "6",
							Ensure: &atc.EnsurePlan{
								Step: atc.Plan{
									ID: "7",
									Task: &atc.TaskPlan{
										Name:       "name",
										ConfigPath: "some/config/path.yml",
										Config: &atc.TaskConfig{
											Params: atc.TaskEnv{"some": "secret"},
										},
									},
								},
								Next: atc.Plan{
									ID: "8",
									Task: &atc.TaskPlan{
										Name:       "name",
										ConfigPath: "some/config/path.yml",
										Config: &atc.TaskConfig{
											Params: atc.TaskEnv{"some": "secret"},
										},
									},
								},
							},
						},

						{
							ID: "9",
							OnSuccess: &atc.OnSuccessPlan{
								Step: atc.Plan{
									ID: "10",
									Task: &atc.TaskPlan{
										Name:       "name",
										ConfigPath: "some/config/path.yml",
										Config: &atc.TaskConfig{
											Params: atc.TaskEnv{"some": "secret"},
										},
									},
								},
								Next: atc.Plan{
									ID: "11",
									Task: &atc.TaskPlan{
										Name:       "name",
										ConfigPath: "some/config/path.yml",
										Config: &atc.TaskConfig{
											Params: atc.TaskEnv{"some": "secret"},
										},
									},
								},
							},
						},

						{
							ID: "12",
							OnFailure: &atc.OnFailurePlan{
								Step: atc.Plan{
									ID: "13",
									Task: &atc.TaskPlan{
										Name:       "name",
										ConfigPath: "some/config/path.yml",
										Config: &atc.TaskConfig{
											Params: atc.TaskEnv{"some": "secret"},
										},
									},
								},
								Next: atc.Plan{
									ID: "14",
									Task: &atc.TaskPlan{
										Name:       "name",
										ConfigPath: "some/config/path.yml",
										Config: &atc.TaskConfig{
											Params: atc.TaskEnv{"some": "secret"},
										},
									},
								},
							},
						},

						{
							ID: "15",
							OnAbort: &atc.OnAbortPlan{
								Step: atc.Plan{
									ID: "16",
									Task: &atc.TaskPlan{
										Name:       "name",
										ConfigPath: "some/config/path.yml",
										Config: &atc.TaskConfig{
											Params: atc.TaskEnv{"some": "secret"},
										},
									},
								},
								Next: atc.Plan{
									ID: "17",
									Task: &atc.TaskPlan{
										Name:       "name",
										ConfigPath: "some/config/path.yml",
										Config: &atc.TaskConfig{
											Params: atc.TaskEnv{"some": "secret"},
										},
									},
								},
							},
						},

						{
							ID: "18",
							Try: &atc.TryPlan{
								Step: atc.Plan{
									ID: "19",
									Task: &atc.TaskPlan{
										Name:       "name",
										ConfigPath: "some/config/path.yml",
										Config: &atc.TaskConfig{
											Params: atc.TaskEnv{"some": "secret"},
										},
									},
								},
							},
						},

						{
							ID: "20",
							Timeout: &atc.TimeoutPlan{
								Step: atc.Plan{
									ID: "21",
									Task: &atc.TaskPlan{
										Name:       "name",
										ConfigPath: "some/config/path.yml",
										Config: &atc.TaskConfig{
											Params: atc.TaskEnv{"some": "secret"},
										},
									},
								},
								Duration: "lol",
							},
						},

						{
							ID: "22",
							Do: &atc.DoPlan{
								atc.Plan{
									ID: "23",
									Task: &atc.TaskPlan{
										Name:       "name",
										ConfigPath: "some/config/path.yml",
										Config: &atc.TaskConfig{
											Params: atc.TaskEnv{"some": "secret"},
										},
									},
								},
							},
						},

						{
							ID: "24",
							Retry: &atc.RetryPlan{
								atc.Plan{
									ID: "25",
									Task: &atc.TaskPlan{
										Name:       "name",
										ConfigPath: "some/config/path.yml",
										Config: &atc.TaskConfig{
											Params: atc.TaskEnv{"some": "secret"},
										},
									},
								},
								atc.Plan{
									ID: "26",
									Task: &atc.TaskPlan{
										Name:       "name",
										ConfigPath: "some/config/path.yml",
										Config: &atc.TaskConfig{
											Params: atc.TaskEnv{"some": "secret"},
										},
									},
								},
								atc.Plan{
									ID: "27",
									Task: &atc.TaskPlan{
										Name:       "name",
										ConfigPath: "some/config/path.yml",
										Config: &atc.TaskConfig{
											Params: atc.TaskEnv{"some": "secret"},
										},
									},
								},
							},
						},

						{
							ID: "28",
							OnAbort: &atc.OnAbortPlan{
								Step: atc.Plan{
									ID: "29",
									Task: &atc.TaskPlan{
										Name:       "name",
										ConfigPath: "some/config/path.yml",
										Config: &atc.TaskConfig{
											Params: atc.TaskEnv{"some": "secret"},
										},
									},
								},
								Next: atc.Plan{
									ID: "30",
									Task: &atc.TaskPlan{
										Name:       "name",
										ConfigPath: "some/config/path.yml",
										Config: &atc.TaskConfig{
											Params: atc.TaskEnv{"some": "secret"},
										},
									},
								},
							},
						},

						{
							ID: "31",
							ArtifactInput: &atc.ArtifactInputPlan{
								ArtifactID: 17,
								Name:       "some-name",
							},
						},

						{
							ID: "32",
							ArtifactOutput: &atc.ArtifactOutputPlan{
								Name: "some-name",
							},
						},

						{
							ID: "33",
							OnError: &atc.OnErrorPlan{
								Step: atc.Plan{
									ID: "34",
									Task: &atc.TaskPlan{
										Name:       "name",
										ConfigPath: "some/config/path.yml",
										Config: &atc.TaskConfig{
											Params: atc.TaskEnv{"some": "secret"},
										},
									},
								},
								Next: atc.Plan{
									ID: "35",
									Task: &atc.TaskPlan{
										Name:       "name",
										ConfigPath: "some/config/path.yml",
										Config: &atc.TaskConfig{
											Params: atc.TaskEnv{"some": "secret"},
										},
									},
								},
							},
						},
						{
							ID: "36",
							InParallel: &atc.InParallelPlan{
								Limit:    1,
								FailFast: true,
								Steps: []atc.Plan{
									{
										ID: "37",
										Task: &atc.TaskPlan{
											Name:       "name",
											ConfigPath: "some/config/path.yml",
											Config: &atc.TaskConfig{
												Params: atc.TaskEnv{"some": "secret"},
											},
										},
									},
								},
							},
						},
						{
							ID: "38",
							SetPipeline: &atc.SetPipelinePlan{
								Name:         "some-pipeline",
								Team:         "some-team",
								File:         "some-file",
								VarFiles:     []string{"vf"},
								Vars:         map[string]any{"k1": "v1"},
								InstanceVars: map[string]any{"branch": "feature/foo"},
							},
						},
						{
							ID: "39",
							Across: &atc.AcrossPlan{
								Vars: []atc.AcrossVar{
									{
										Var:         "v1",
										Values:      []any{"a"},
										MaxInFlight: &atc.MaxInFlightConfig{Limit: 1},
									},
									{
										Var:         "v2",
										Values:      []any{"b"},
										MaxInFlight: &atc.MaxInFlightConfig{All: true},
									},
								},
								SubStepTemplate: `{"id":"ACROSS_STEP_TEMPLATE"}`,
								FailFast:        true,
							},
						},
						{
							ID: "42",
							LoadVar: &atc.LoadVarPlan{
								Name:   "some-name",
								File:   "some-file",
								Format: "some-format",
								Reveal: true,
							},
						},
					},
				},
			}
			format.MaxLength = 999999

			json := plan.Public()
			Expect(json).ToNot(BeNil())
			Expect([]byte(*json)).To(MatchJSON(`{
  "id": "0",
  "in_parallel": {
    "steps": [
      {
        "id": "1",
        "in_parallel": {
          "steps": [
            {
              "id": "2",
              "task": {
                "name": "name",
                "privileged": false,
								"hermetic": false
              }
            }
          ]
        }
      },
			{
				"id": "3",
				"get": {
					"type": "type",
					"name": "name",
					"resource": "resource",
					"version": {
						"some": "version"
					},
					"image_get_plan": {
						"id": "3/image-get",
						"get": {
							"type": "some-base-type",
							"name": "name"
						}
					},
					"image_check_plan": {
						"id": "3/image-check",
						"check": {
							"type": "some-base-type",
							"name": "name"
						}
					}
				}
			},
			{
				"id": "3.1",
				"get": {
					"name": "name",
					"type": "some-custom-type",
					"resource": "resource",
					"version": {
						"some": "version"
					},
					"image_get_plan": {
						"id": "3.1/image-get",
						"get": {
							"name": "some-custom-type",
							"type": "second-custom-type",
							"image_get_plan": {
								"id": "3.1/image-get/image-get",
								"get": {
									"name": "second-custom-type",
									"type": "some-base-type"
								}
							},
							"image_check_plan": {
								"id": "3.1/image-get/image-check",
								"check": {
									"name": "second-custom-type",
									"type": "some-base-type"
								}
							}
						}
					},
					"image_check_plan": {
						"id": "3.1/image-check",
						"check": {
							"name": "some-custom-type",
							"type": "second-custom-type",
							"image_get_plan": {
								"id": "3.1/image-check/image-get",
								"get": {
									"name": "second-custom-type",
									"type": "some-base-type"
								}
							},
							"image_check_plan": {
								"id": "3.1/image-check/image-check",
								"check": {
									"name": "second-custom-type",
									"type": "some-base-type"
								}
							}
						}
					}
				}
			},
			{
				"id": "4",
				"put": {
					"type": "type",
					"name": "name",
					"resource": "resource",
					"image_get_plan": {
						"id": "4/image-get",
						"get": {
							"type": "some-base-type",
							"name": "name"
						}
					},
					"image_check_plan": {
						"id": "4/image-check",
						"check": {
							"type": "some-base-type",
							"name": "name"
						}
					}
				}
			},
			{
				"id": "4.2",
				"check": {
					"type": "type",
					"name": "name",
					"image_get_plan": {
						"id": "4.2/image-get",
						"get": {
							"type": "some-base-type",
							"name": "name"
						}
					},
					"image_check_plan": {
						"id": "4.2/image-check",
						"check": {
							"type": "some-base-type",
							"name": "name"
						}
					}
				}
			},
			{
				"id": "5",
				"task": {
					"name": "name",
					"privileged": true,
					"hermetic": true
				}
			},
			{
				"id": "6",
				"ensure": {
					"step": {
						"id": "7",
						"task": {
							"name": "name",
							"privileged": false,
							"hermetic": false
						}
					},
					"ensure": {
						"id": "8",
						"task": {
							"name": "name",
							"privileged": false,
							"hermetic": false
						}
					}
				}
			},
			{
				"id": "9",
				"on_success": {
					"step": {
						"id": "10",
						"task": {
							"name": "name",
							"privileged": false,
							"hermetic": false
						}
					},
					"on_success": {
						"id": "11",
						"task": {
							"name": "name",
							"privileged": false,
							"hermetic": false
						}
					}
				}
			},
			{
				"id": "12",
				"on_failure": {
					"step": {
						"id": "13",
						"task": {
							"name": "name",
							"privileged": false,
							"hermetic": false
						}
					},
					"on_failure": {
						"id": "14",
						"task": {
							"name": "name",
							"privileged": false,
							"hermetic": false
						}
					}
				}
			},
			{
				"id": "15",
				"on_abort": {
					"step": {
						"id": "16",
						"task": {
							"name": "name",
							"privileged": false,
							"hermetic": false
						}
					},
					"on_abort": {
						"id": "17",
						"task": {
							"name": "name",
							"privileged": false,
							"hermetic": false
						}
					}
				}
			},
			{
				"id": "18",
				"try": {
					"step": {
						"id": "19",
						"task": {
							"name": "name",
							"privileged": false,
							"hermetic": false
						}
					}
				}
			},
			{
				"id": "20",
				"timeout": {
					"step": {
						"id": "21",
						"task": {
							"name": "name",
							"privileged": false,
							"hermetic": false
						}
					},
					"duration": "lol"
				}
			},
			{
				"id": "22",
				"do": [
					{
						"id": "23",
						"task": {
							"name": "name",
							"privileged": false,
							"hermetic": false
						}
					}
				]
			},
			{
				"id": "24",
				"retry": [
					{
						"id": "25",
						"task": {
							"name": "name",
							"privileged": false,
							"hermetic": false
						}
					},
					{
						"id": "26",
						"task": {
							"name": "name",
							"privileged": false,
							"hermetic": false
						}
					},
					{
						"id": "27",
						"task": {
							"name": "name",
							"privileged": false,
							"hermetic": false
						}
					}
				]
			},
			{
				"id": "28",
				"on_abort": {
					"step": {
						"id": "29",
						"task": {
							"name": "name",
							"privileged": false,
							"hermetic": false
						}
					},
					"on_abort": {
						"id": "30",
						"task": {
							"name": "name",
							"privileged": false,
							"hermetic": false
						}
					}
				}
			},
			{
				"id": "31",
				"artifact_input": {
					"artifact_id": 17,
					"name": "some-name"
				}
			},
			{
				"id": "32",
				"artifact_output": {
					"name": "some-name"
				}
			},
			{
				"id": "33",
				"on_error": {
					"step": {
						"id": "34",
						"task": {
							"name": "name",
							"privileged": false,
							"hermetic": false
						}
					},
					"on_error": {
						"id": "35",
						"task": {
							"name": "name",
							"privileged": false,
							"hermetic": false
						}
					}
				}
			},
			{
				"id": "36",
				"in_parallel": {
					"steps": [
						{
							"id": "37",
							"task": {
								"name": "name",
								"privileged": false,
								"hermetic": false
							}
						}
					],
					"limit": 1,
					"fail_fast": true
				}
			},
			{
				"id": "38",
				"set_pipeline": {
					"name": "some-pipeline",
					"team": "some-team",
					"instance_vars": {
						"branch": "feature/foo"
					}
				}
			},
      {
        "id": "39",
        "across": {
          "vars": [
            {
              "name": "v1"
            },
            {
              "name": "v2"
            }
          ],
          "fail_fast": true
        }
      },
			{
				"id": "42",
				"load_var": {
					"name": "some-name"
				}
			}
		]
	}
}
`))
		})
	})
})
