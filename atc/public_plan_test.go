package atc_test

import (
	"encoding/json"
	"reflect"
	"strings"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
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
					Hermetic:       true,
					Model:          "claude-opus-4",
					Prompt:         "secret prompt text",
					MaxTurns:       12,
					BudgetSliceUSD: 3.50,
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
					"hermetic": true,
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

	Describe("typed snapshot declarations", func() {
		It("exposes Task types and workflow metadata without exposing task configuration or params", func() {
			plan := atc.Plan{
				ID: "8/task",
				Task: &atc.TaskPlan{
					Name:       "transform",
					Privileged: true,
					Hermetic:   true,
					ConfigPath: "secret/task.yml",
					Config: &atc.TaskConfig{
						Platform: "linux",
						Params:   atc.TaskEnv{"TOKEN": "secret"},
					},
					Params: atc.TaskEnv{"TOKEN": "secret"},
					SnapshotInputs: map[string]atc.SnapshotInputConfig{
						"repository": {Type: snapshot.TypeRef("repository/v1"), Optional: true},
					},
					SnapshotOutputs: map[string]atc.SnapshotOutputConfig{
						"change": {
							Type:                 snapshot.TypeRef("repository-change/v1"),
							Retention:            snapshot.RetentionClassWorkflow,
							WorkflowPort:         "change",
							WorkflowDefinitionID: 17,
							WorkflowRunID:        "9007199254740993",
							SourceMetadata:       json.RawMessage(`{"adapter":"resource-version","credential":"literal-secret"}`),
						},
					},
				},
			}
			Expect([]byte(*plan.Public())).To(MatchJSON(`{
				"id":"8/task",
				"task":{
					"name":"transform",
					"privileged":true,
					"hermetic":true,
					"input_types":{"repository":{"type":"repository/v1","optional":true}},
					"output_types":{"change":{
						"type":"repository-change/v1",
						"retention":"workflow",
						"workflow_port":"change",
						"workflow_definition_id":17,
						"workflow_run_id":"9007199254740993"
					}}
				}
			}`))
		})

		It("exposes Agent types while redacting all agent instructions and credentials", func() {
			plan := atc.Plan{
				ID: "8/agent",
				Agent: &atc.AgentPlan{
					Name:    "review",
					Model:   "claude-opus-4",
					Prompt:  "secret prompt",
					Context: "secret context",
					Skills:  []string{"private-skill"},
					Env:     map[string]string{"TOKEN": "secret"},
					SnapshotInputs: map[string]atc.SnapshotInputConfig{
						"change": {Type: snapshot.TypeRef("repository-change/v1")},
					},
					SnapshotOutputs: map[string]atc.SnapshotOutputConfig{
						"review": {Type: snapshot.TypeRef("review/v1")},
					},
				},
			}
			Expect([]byte(*plan.Public())).To(MatchJSON(`{
				"id":"8/agent",
				"agent":{
					"name":"review",
					"model":"claude-opus-4",
					"input_types":{"change":{"type":"repository-change/v1"}},
					"output_types":{"review":{"type":"review/v1"}}
				}
			}`))
		})
	})
})

var _ = Describe("LoadSnapshot public plan", func() {
	It("exposes the typed producer and redacts both durable identifiers", func() {
		plan := atc.Plan{
			ID: "9",
			LoadSnapshot: &atc.LoadSnapshotPlan{
				Name:          "subject",
				ID:            "9007199254740993",
				Type:          snapshot.TypeRef("review/v1"),
				Optional:      true,
				WorkflowRunID: "9223372036854775807",
			},
		}
		Expect([]byte(*plan.Public())).To(MatchJSON(`{
			"id":"9",
			"load_snapshot":{"name":"subject","type":"review/v1","optional":true}
		}`))
	})
})

var _ = Describe("AwaitSnapshot public plan", func() {
	It("exposes the interaction contract while redacting durable identifiers", func() {
		plan := atc.Plan{
			ID: "10",
			AwaitSnapshot: &atc.AwaitSnapshotPlan{
				Name: "answer", Question: "question", Type: snapshot.TypeRef("human-answer/v1"),
				OnTimeout: atc.AwaitSnapshotOnTimeoutDefault, DefaultSnapshotID: "9007199254740993",
				WorkflowRunID: "9223372036854775807", WorkflowDefinitionID: 17, WorkflowPort: "approval",
			},
		}
		Expect([]byte(*plan.Public())).To(MatchJSON(`{
			"id":"10",
			"await_snapshot":{
				"name":"answer",
				"question":"question",
				"type":"human-answer/v1",
				"on_timeout":"default",
				"has_default":true,
				"workflow_port":"approval"
			}
		}`))
	})

	It("shows a server-bound merge decision without exposing destination details", func() {
		plan := atc.Plan{ID: "11", AwaitSnapshot: &atc.AwaitSnapshotPlan{
			Name: "approval", Type: snapshot.TypeRef("human-answer/v1"), OnTimeout: atc.AwaitSnapshotOnTimeoutFail,
			MergeApproval: &atc.MergeApprovalIntent{
				Input: "change", Publisher: publisher.GitPublisher,
				Destination:           "https://credential@git.example/private",
				Parameters:            map[string]string{"target_branch": "main", "token": "secret"},
				ApprovalPolicyVersion: "engineering/v1", Prompt: "private prompt",
			},
		}}
		public := []byte(*plan.Public())
		Expect(public).To(MatchJSON(`{
			"id":"11",
			"await_snapshot":{
				"name":"approval",
				"merge_approval_input":"change",
				"merge_approval_publisher":"git-publisher/v1",
				"merge_destination_configured":true,
				"type":"human-answer/v1",
				"on_timeout":"fail"
			}
		}`))
		Expect(string(public)).NotTo(ContainSubstring("credential"))
		Expect(string(public)).NotTo(ContainSubstring("secret"))
		Expect(string(public)).NotTo(ContainSubstring("private prompt"))
	})
})

var _ = Describe("PublishSnapshot public plan", func() {
	It("exposes the typed operation while redacting destination details and parameters", func() {
		plan := atc.Plan{
			ID: "11",
			PublishSnapshot: &atc.PublishSnapshotPlan{
				Name: "publish-change", Publisher: publisher.GitPublisher, Input: "change",
				InputType:             snapshot.TypeRef("repository-change/v1"),
				Destination:           "https://person:credential@github.example/team/repo?token=secret",
				Mode:                  publisher.ModeBranch,
				Parameters:            map[string]string{"body": "private", "credential": "literal-secret"},
				ApprovalPolicyVersion: "engineering/v2",
			},
		}
		public := []byte(*plan.Public())
		Expect(public).To(MatchJSON(`{
			"id":"11",
			"publish_snapshot":{
				"name":"publish-change",
				"publisher":"git-publisher/v1",
				"input":"change",
				"input_type":"repository-change/v1",
				"mode":"branch",
				"approval_policy_version":"engineering/v2",
				"destination_configured":true
			}
		}`))
		Expect(string(public)).ToNot(ContainSubstring("credential"))
		Expect(string(public)).ToNot(ContainSubstring("literal-secret"))
	})

	It("shows the approval artifact for a visible merge without exposing trusted run identity", func() {
		plan := atc.Plan{
			ID: "12",
			PublishSnapshot: &atc.PublishSnapshotPlan{
				Name: "merge-change", Publisher: publisher.GitPublisher, Input: "change",
				InputType: snapshot.TypeRef("repository-change/v1"), Destination: "github.example/team/repo",
				Mode: publisher.ModeMerge, Parameters: map[string]string{
					"target_branch": "main", "expected_base_sha": strings.Repeat("a", 40),
				},
				ApprovalPolicyVersion: "engineering/v2", Approval: "merge-approval", WorkflowRunID: "91",
			},
		}
		public := []byte(*plan.Public())
		Expect(public).To(MatchJSON(`{
			"id":"12",
			"publish_snapshot":{
				"name":"merge-change",
				"publisher":"git-publisher/v1",
				"input":"change",
				"input_type":"repository-change/v1",
				"mode":"merge",
				"approval_policy_version":"engineering/v2",
				"approval":"merge-approval",
				"destination_configured":true
			}
		}`))
		Expect(string(public)).ToNot(ContainSubstring("workflow_run_id"))
		Expect(string(public)).ToNot(ContainSubstring("expected_base_sha"))
	})
})
