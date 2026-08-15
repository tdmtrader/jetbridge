package api_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"

	"code.cloudfoundry.org/lager/v3"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awssecretsmanager "github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	awsssm "github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/concourse/concourse/atc/creds/credhub"
	"github.com/concourse/concourse/atc/creds/secretsmanager"
	"github.com/concourse/concourse/atc/creds/ssm"
	"github.com/concourse/concourse/atc/creds/vault"
	vaultapi "github.com/hashicorp/vault/api"

	. "github.com/concourse/concourse/atc/testhelpers"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
	. "github.com/onsi/gomega/gstruct"
)

var _ = Describe("Info API", func() {
	Describe("GET /api/v1/info", func() {
		var response *http.Response

		BeforeEach(func() {
			useProfile(anonymousProfile)
		})

		JustBeforeEach(func() {
			var err error

			response, err = client.Get(server.URL + "/api/v1/info")
			Expect(err).NotTo(HaveOccurred())
		})

		It("returns Content-Type 'application/json'", func() {
			expectedHeaderEntries := map[string]string{
				"Content-Type": "application/json",
			}
			Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))
		})

		It("contains the version", func() {
			body, err := io.ReadAll(response.Body)
			Expect(err).NotTo(HaveOccurred())

			Expect(body).To(MatchJSON(fmt.Sprintf(`{
				"version": "1.2.3",
				"worker_version": "4.5.6",
				"feature_flags": %v,
				"external_url": "https://example.com",
				"cluster_name": "Test Cluster",
				"jetbridge_version": "0.1.0-test",
				"concourse_version": "8.0.1-test"
			}`, featureFlagsJson)))
		})
	})

	Describe("GET /api/v1/info/creds", func() {
		var (
			response   *http.Response
			credServer *ghttp.Server
			body       []byte
		)

		BeforeEach(func() {
			useProfile(adminProfile)
		})

		JustBeforeEach(func() {
			req, err := http.NewRequest("GET", server.URL+"/api/v1/info/creds", nil)
			Expect(err).NotTo(HaveOccurred())
			req.Header.Set("Content-Type", "application/json")

			response, err = client.Do(req)
			Expect(err).NotTo(HaveOccurred())

			Expect(response.StatusCode).To(Equal(http.StatusOK))
			expectedHeaderEntries := map[string]string{
				"Content-Type": "application/json",
			}
			Expect(response).Should(IncludeHeaderEntries(expectedHeaderEntries))

			body, err = io.ReadAll(response.Body)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("SSM", func() {
			var ssmService *ssmProtocolServer

			BeforeEach(func() {
				ssmService = startSSMProtocolServer()

				ssmAccess := ssm.NewSsm(lager.NewLogger("ssm_test"), ssmService.client, nil, "")
				ssmManager := &ssm.SsmManager{
					AwsAccessKeyID:         "",
					AwsSecretAccessKey:     "",
					AwsSessionToken:        "",
					AwsRegion:              "blah",
					PipelineSecretTemplate: "pipeline-secret-template",
					TeamSecretTemplate:     "team-secret-template",
					Ssm:                    ssmAccess,
				}

				credsManagers["ssm"] = ssmManager
			})

			Context("returns configured ssm manager", func() {
				Context("get ssm manager info returns error", func() {
					BeforeEach(func() {
						ssmService.respondWithError("InternalServerError", "some error occured")
					})

					It("includes the error in json response", func() {
						var health struct {
							Ssm struct {
								Health struct {
									Error  string `json:"error"`
									Method string `json:"method"`
								} `json:"health"`
							} `json:"ssm"`
						}

						Expect(json.Unmarshal(body, &health)).To(Succeed())
						Expect(health.Ssm.Health.Method).To(Equal("GetParameter"))
						Expect(health.Ssm.Health.Error).To(ContainSubstring("some error occured"))
					})
				})

				Context("get ssm manager info", func() {
					BeforeEach(func() {
						ssmService.respondWithError("ParameterNotFound", "dontcare")
					})

					It("includes the ssm health info in json response", func() {
						Expect(body).To(MatchJSON(`{
          "ssm": {
						"aws_region": "blah",
						"health": {
							"response": {
								"status": "UP"
							},
							"method": "GetParameter"
						},
						"pipeline_secret_template": "pipeline-secret-template",
            "shared_path": "",
						"team_secret_template": "team-secret-template"
          }
        }`))
					})
				})
			})
		})

		Context("vault", func() {
			BeforeEach(func() {
				authConfig := vault.AuthConfig{
					Backend:       "backend-server",
					BackendMaxTTL: 20,
					RetryMax:      5,
					RetryInitial:  2,
				}

				tls := vault.TLSConfig{
					CACert:     "",
					ServerName: "server-name",
				}

				credServer = ghttp.NewServer()
				vaultManager := &vault.VaultManager{
					URL:             credServer.URL(),
					Namespace:       "testnamespace",
					PathPrefix:      "testpath",
					LookupTemplates: []string{"/{{.Team}}/{{.Pipeline}}/{{.Secret}}", "/{{.Team}}/{{.Secret}}"},
					TLS:             tls,
					Auth:            authConfig,
				}

				err := vaultManager.Init(lager.NewLogger("test"))
				Expect(err).ToNot(HaveOccurred())

				credsManagers["vault"] = vaultManager

				credServer.RouteToHandler("GET", "/v1/sys/health", ghttp.RespondWithJSONEncoded(
					http.StatusOK,
					&vaultapi.HealthResponse{
						Initialized:                true,
						Sealed:                     false,
						Standby:                    false,
						ReplicationPerformanceMode: "foo",
						ReplicationDRMode:          "blah",
						ServerTimeUTC:              0,
						Version:                    "1.0.0",
					},
				))
			})

			Context("get vault health info returns error", func() {
				BeforeEach(func() {
					credServer.RouteToHandler("GET", "/v1/sys/health", ghttp.RespondWithJSONEncoded(
						http.StatusInternalServerError,
						"some error occurred",
					))
				})

				It("returns configured creds manager with error", func() {
					var errorBody struct {
						Vault struct {
							Health struct {
								Error  string `json:"error"`
								Method string `json:"method"`
							} `json:"health"`
						} `json:"vault"`
					}

					err := json.Unmarshal(body, &errorBody)
					Expect(err).ToNot(HaveOccurred())

					Expect(errorBody.Vault.Health.Error).To(ContainSubstring("some error occurred"))
				})
			})

			Context("get vault health info", func() {
				BeforeEach(func() {
					credServer.RouteToHandler("GET", "/v1/sys/health", ghttp.RespondWithJSONEncoded(
						http.StatusOK,
						&vaultapi.HealthResponse{
							Initialized:                true,
							Sealed:                     false,
							Standby:                    false,
							ReplicationPerformanceMode: "foo",
							ReplicationDRMode:          "blah",
							ServerTimeUTC:              0,
							Version:                    "1.0.0",
						},
					))
				})

				It("returns configured creds manager", func() {
					Expect(body).To(MatchJSON(`{
          "vault": {
            "url": "` + credServer.URL() + `",
            "path_prefix": "testpath",
            "lookup_templates": ["/{{.Team}}/{{.Pipeline}}/{{.Secret}}", "/{{.Team}}/{{.Secret}}"],
			"shared_path": "",
			"namespace": "testnamespace",
            "ca_cert": "",
            "server_name": "server-name",
			"auth_backend": "backend-server",
			"auth_max_ttl": 20,
			"auth_retry_max": 5,
			"auth_retry_initial": 2,
			"health": {
				"response": {
                  "initialized": true,
                  "sealed": false,
                  "standby": false,
				  "performance_standby": false,
                  "replication_performance_mode": "foo",
                  "replication_dr_mode": "blah",
                  "server_time_utc": 0,
                  "version": "1.0.0",
				  "enterprise": false,
				  "echo_duration_ms": 0,
				  "clock_skew_ms": 0,
				  "replication_primary_canary_age_ms": 0
                },
                "method": "/v1/sys/health"
			}
          }
        }`))
				})
			})
		})

		Context("credhub", func() {
			var (
				tls credhub.TLS
				uaa credhub.UAA
			)

			BeforeEach(func() {
				tls = credhub.TLS{
					CACerts: []string{},
				}
				uaa = credhub.UAA{
					ClientId:     "client-id",
					ClientSecret: "client-secret",
				}
			})

			Context("get credhub help info succeeds", func() {
				BeforeEach(func() {
					credServer = ghttp.NewServer()
					credServer.RouteToHandler("GET", "/health", ghttp.RespondWithJSONEncoded(
						http.StatusOK, map[string]string{
							"status": "UP",
						},
					))

					credhubManager := &credhub.CredHubManager{
						URL:        credServer.URL(),
						PathPrefix: "some-prefix",
						TLS:        tls,
						UAA:        uaa,
						Client:     &credhub.LazyCredhub{},
					}

					credsManagers["credhub"] = credhubManager
				})

				It("returns configured creds manager with response", func() {
					Expect(body).To(MatchJSON(`{
					"credhub": {
						"url": "` + credServer.URL() + `",
						"ca_certs": [],
						"health": {
							"response": {
								"status": "UP"
							},
							"method": "/health"
						},
						"path_prefix": "some-prefix",
						"uaa_client_id": "client-id"
						}
					}`))
				})
			})

			Context("get credhub health info returns error", func() {
				type responseSkeleton struct {
					CredHub struct {
						Url     string   `json:"url"`
						CACerts []string `json:"ca_certs"`
						Health  struct {
							Error    string `json:"error"`
							Response struct {
								Status string `json:"status"`
							} `json:"response"`
							Method string `json:"method"`
						} `json:"health"`
						PathPrefix  string `json:"path_prefix"`
						UAAClientId string `json:"uaa_client_id"`
					} `json:"credhub"`
				}

				BeforeEach(func() {
					credhubManager := &credhub.CredHubManager{
						URL:        "http://wrong.inexistent.tld",
						PathPrefix: "some-prefix",
						TLS:        tls,
						UAA:        uaa,
						Client:     &credhub.LazyCredhub{},
					}

					credsManagers["credhub"] = credhubManager
				})

				It("returns configured creds manager with error", func() {
					var parsedResponse responseSkeleton

					err := json.Unmarshal(body, &parsedResponse)
					Expect(err).ToNot(HaveOccurred())

					Expect(parsedResponse.CredHub.Url).To(Equal("http://wrong.inexistent.tld"))
					Expect(parsedResponse.CredHub.CACerts).To(BeEmpty())
					Expect(parsedResponse.CredHub.PathPrefix).To(Equal("some-prefix"))
					Expect(parsedResponse.CredHub.UAAClientId).To(Equal("client-id"))
					Expect(parsedResponse.CredHub.Health.Response).ToNot(BeNil())
					Expect(parsedResponse.CredHub.Health.Response.Status).To(BeEmpty())
					Expect(parsedResponse.CredHub.Health.Method).To(Equal("/health"))
					Expect(parsedResponse.CredHub.Health.Error).To(ContainSubstring("no such host"))
				})
			})
		})

		Context("SecretsManager", func() {
			var service *secretsManagerProtocolServer

			BeforeEach(func() {
				service = startSecretsManagerProtocolServer()

				secretsManagerAccess := secretsmanager.NewSecretsManager(lager.NewLogger("secretsmanager_test"), service.client, nil)

				secretsManager := &secretsmanager.Manager{
					AwsAccessKeyID:         "",
					AwsSecretAccessKey:     "",
					AwsSessionToken:        "",
					AwsRegion:              "blah",
					PipelineSecretTemplate: "pipeline-secret-template",
					TeamSecretTemplate:     "team-secret-template",
					SharedSecretTemplate:   "shared-secret-template",
					SecretManager:          secretsManagerAccess,
				}

				credsManagers["secretsmanager"] = secretsManager
			})

			Context("returns configured secretsmanager manager", func() {
				Context("get secretsmanager info returns error", func() {
					BeforeEach(func() {
						service.respondWithError(http.StatusInternalServerError, "InternalServiceError", "some error occurred")
					})

					It("includes the error in json response", func() {
						var info struct {
							SecretsManager struct {
								Health struct {
									Error  string `json:"error"`
									Method string `json:"method"`
								} `json:"health"`
							} `json:"secretsmanager"`
						}

						Expect(json.Unmarshal(body, &info)).To(Succeed())
						Expect(info.SecretsManager.Health.Method).To(Equal("GetSecretValue"))
						Expect(info.SecretsManager.Health.Error).To(ContainSubstring("some error occurred"))
					})

				})

				Context("get secretsmanager info", func() {
					BeforeEach(func() {
						service.respondWithError(http.StatusBadRequest, "ResourceNotFoundException", "dontcare")
					})

					It("includes the secretsmanager info in json response", func() {
						Expect(body).To(MatchJSON(`{
					"secretsmanager": {
						"aws_region": "blah",
						"pipeline_secret_template": "pipeline-secret-template",
						"team_secret_template": "team-secret-template",
						"shared_secret_template": "shared-secret-template",
						"health": {
							"response": {
								"status": "UP"
							},
							"method": "GetSecretValue"
						}
					}
				}`))
					})
				})
			})

		})
	})
})

// ssmProtocolServer speaks the AWS JSON 1.1 wire protocol so the info endpoint
// drives a real *ssm.Client. The health probe only cares about the error the
// client raises, and the client wraps every one of them in a
// *smithy.OperationError -- which a hand-built types.ParameterNotFound would
// not be.
type ssmProtocolServer struct {
	client *awsssm.Client

	mutex     sync.Mutex
	errorType string
	message   string
}

func startSSMProtocolServer() *ssmProtocolServer {
	service := &ssmProtocolServer{errorType: "ParameterNotFound", message: "not found"}

	server := httptest.NewServer(service)
	DeferCleanup(server.Close)

	service.client = awsssm.New(awsssm.Options{
		Region:       "blah",
		Credentials:  credentials.NewStaticCredentialsProvider("access-key", "secret-key", ""),
		BaseEndpoint: aws.String(server.URL),
		Retryer:      aws.NopRetryer{},
	})

	return service
}

func (service *ssmProtocolServer) respondWithError(errorType string, message string) {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	service.errorType = errorType
	service.message = message
}

func (service *ssmProtocolServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer GinkgoRecover()

	Expect(r.Header.Get("X-Amz-Target")).To(Equal("AmazonSSM.GetParameter"))

	var input awsssm.GetParameterInput
	Expect(json.NewDecoder(r.Body).Decode(&input)).To(Succeed())
	Expect(input.Name).To(PointTo(Equal("__concourse-health-check")))

	service.mutex.Lock()
	errorType, message := service.errorType, service.message
	service.mutex.Unlock()

	status := http.StatusInternalServerError
	if errorType == "ParameterNotFound" {
		status = http.StatusBadRequest
	}

	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.Header().Set("X-Amzn-Errortype", errorType)
	w.WriteHeader(status)
	Expect(json.NewEncoder(w).Encode(map[string]string{"message": message})).To(Succeed())
}

// secretsManagerProtocolServer speaks the AWS JSON 1.1 protocol used by the
// production SDK client. The handler records no collaborator calls: the public
// info response is the observable result of signing, serializing, and decoding
// a real GetSecretValue request.
type secretsManagerProtocolServer struct {
	client *awssecretsmanager.Client

	mutex     sync.Mutex
	status    int
	errorType string
	message   string
}

func startSecretsManagerProtocolServer() *secretsManagerProtocolServer {
	service := &secretsManagerProtocolServer{
		status:    http.StatusBadRequest,
		errorType: "ResourceNotFoundException",
		message:   "not found",
	}

	server := httptest.NewServer(service)
	DeferCleanup(server.Close)

	service.client = awssecretsmanager.New(awssecretsmanager.Options{
		Region:       "blah",
		Credentials:  credentials.NewStaticCredentialsProvider("access-key", "secret-key", ""),
		BaseEndpoint: aws.String(server.URL),
		Retryer:      aws.NopRetryer{},
	})

	return service
}

func (service *secretsManagerProtocolServer) respondWithError(status int, errorType string, message string) {
	service.mutex.Lock()
	defer service.mutex.Unlock()

	service.status = status
	service.errorType = errorType
	service.message = message
}

func (service *secretsManagerProtocolServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer GinkgoRecover()

	Expect(r.Method).To(Equal(http.MethodPost))
	Expect(r.Header.Get("X-Amz-Target")).To(Equal("secretsmanager.GetSecretValue"))
	Expect(r.Header.Get("Content-Type")).To(ContainSubstring("application/x-amz-json-1.1"))
	Expect(r.Header.Get("Authorization")).To(ContainSubstring("Credential=access-key/"))

	var input struct {
		SecretID string `json:"SecretId"`
	}
	Expect(json.NewDecoder(r.Body).Decode(&input)).To(Succeed())
	Expect(input.SecretID).To(Equal("__concourse-health-check"))

	service.mutex.Lock()
	status, errorType, message := service.status, service.errorType, service.message
	service.mutex.Unlock()

	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.Header().Set("X-Amzn-Errortype", errorType)
	w.WriteHeader(status)
	Expect(json.NewEncoder(w).Encode(map[string]string{
		"__type":  errorType,
		"message": message,
	})).To(Succeed())
}
