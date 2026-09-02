package concourse_test

import (
	"io"
	"net/http"

	"sigs.k8s.io/yaml"

	"github.com/concourse/concourse/atc"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("ATC Handler Configs", func() {
	var (
		pipelineRef atc.PipelineRef
	)

	BeforeEach(func() {
		pipelineRef = atc.PipelineRef{Name: "mypipeline"}
	})

	Describe("PipelineConfig", func() {
		expectedURL := "/api/v1/teams/some-team/pipelines/mypipeline/config"

		Context("ATC returns an error", func() {
			BeforeEach(func() {
				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", expectedURL),
						ghttp.RespondWith(http.StatusInternalServerError, ""),
					),
				)
			})

			It("returns the error", func() {
				_, _, _, err := team.PipelineConfig(pipelineRef)
				Expect(err).To(HaveOccurred())
			})
		})

		Context("ATC does not return config version", func() {
			BeforeEach(func() {
				atcServer.AppendHandlers(
					ghttp.CombineHandlers(
						ghttp.VerifyRequest("GET", expectedURL),
						ghttp.RespondWithJSONEncoded(http.StatusOK, atc.ConfigResponse{Config: atc.Config{}}),
					),
				)
			})

			It("returns an error", func() {
				_, _, _, err := team.PipelineConfig(pipelineRef)
				Expect(err).NotTo(HaveOccurred())
			})
		})
	})

	Describe("CreateOrUpdatePipelineConfig", func() {
		var (
			expectedVersion string
			expectedConfig  []byte

			returnHeader int
			returnBody   []byte

			checkCredentials bool
		)

		BeforeEach(func() {
			expectedVersion = "42"
			expectedConfig = []byte("")

			expectedPath := "/api/v1/teams/some-team/pipelines/mypipeline/config"

			checkCredentials = false

			atcServer.RouteToHandler("PUT", expectedPath,
				ghttp.CombineHandlers(
					ghttp.VerifyHeaderKV(atc.ConfigVersionHeader, "42"),
					func(w http.ResponseWriter, r *http.Request) {
						defer r.Body.Close()
						bodyConfig, err := io.ReadAll(r.Body)
						Expect(err).NotTo(HaveOccurred())

						receivedConfig := []byte("")

						err = yaml.Unmarshal(bodyConfig, &receivedConfig)
						Expect(err).NotTo(HaveOccurred())

						Expect(receivedConfig).To(Equal(expectedConfig))

						w.Header().Set("Content-Type", "application/json")

						w.WriteHeader(returnHeader)
						w.Write(returnBody)
					},
				),
			)
		})

		Context("when creating a new config", func() {
			BeforeEach(func() {
				returnHeader = http.StatusCreated
				returnBody = []byte(`{"warnings":[
				  {"type": "warning-1-type", "message": "fake-warning1"},
					{"type": "warning-2-type", "message": "fake-warning2"}
				]}`)
			})

			Context("when credential verification is enabled", func() {
				BeforeEach(func() {
					checkCredentials = true
				})

				It("submits with check_creds query param set", func() {
					Expect(atcServer.ReceivedRequests()).To(HaveLen(0))

					_, _, _, err := team.CreateOrUpdatePipelineConfig(pipelineRef, expectedVersion, expectedConfig, checkCredentials)
					Expect(err).ToNot(HaveOccurred())

					Expect(atcServer.ReceivedRequests()).To(HaveLen(1))
					Expect(atcServer.ReceivedRequests()[0].URL.RawQuery).To(Equal("check_creds="))
				})
			})

			Context("when response does not contain application/json header", func() {
				BeforeEach(func() {
					atcServer.RouteToHandler("PUT", "/api/v1/teams/some-team/pipelines/mypipeline/config",
						ghttp.CombineHandlers(
							ghttp.VerifyHeaderKV(atc.ConfigVersionHeader, "42"),
							func(w http.ResponseWriter, r *http.Request) {
								w.WriteHeader(http.StatusOK)
								w.Write([]byte(`server error`))
							},
						),
					)
				})

				It("returns an error", func() {
					_, _, _, err := team.CreateOrUpdatePipelineConfig(pipelineRef, expectedVersion, expectedConfig, checkCredentials)
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("server error"))
				})
			})
		})

		Context("when updating a config", func() {
			BeforeEach(func() {
				returnHeader = http.StatusOK
				returnBody = []byte(`{"warnings":[
				  {"type": "warning-1-type", "message": "fake-warning1"},
					{"type": "warning-2-type", "message": "fake-warning2"}
				]}`)
			})

			Context("when credential verification is enabled", func() {
				BeforeEach(func() {
					checkCredentials = true
				})

				It("submits with check_creds query param set", func() {
					Expect(atcServer.ReceivedRequests()).To(HaveLen(0))

					_, _, _, err := team.CreateOrUpdatePipelineConfig(pipelineRef, expectedVersion, expectedConfig, checkCredentials)
					Expect(err).ToNot(HaveOccurred())

					Expect(atcServer.ReceivedRequests()).To(HaveLen(1))
					Expect(atcServer.ReceivedRequests()[0].URL.RawQuery).To(Equal("check_creds="))
				})
			})
		})

		Context("when setting config returns bad request", func() {
			BeforeEach(func() {
				returnHeader = http.StatusBadRequest
				returnBody = []byte(`{"errors":["fake-error1","fake-error2"]}`)
			})

			It("returns config validation error", func() {
				_, _, _, err := team.CreateOrUpdatePipelineConfig(pipelineRef, expectedVersion, expectedConfig, checkCredentials)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("invalid pipeline config:\n"))
				Expect(err.Error()).To(ContainSubstring("fake-error1\nfake-error2"))
			})
		})

		Context("when setting config returns forbidden", func() {
			BeforeEach(func() {
				returnHeader = http.StatusForbidden
				returnBody = []byte(`policy check failed: you can't do that`)
			})

			It("returns a forbidden error", func() {
				_, _, _, err := team.CreateOrUpdatePipelineConfig(pipelineRef, expectedVersion, expectedConfig, checkCredentials)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("forbidden: policy check failed: you can't do that"))
			})
		})

		Context("when response does not contain application/json header", func() {
			BeforeEach(func() {
				atcServer.RouteToHandler("PUT", "/api/v1/teams/some-team/pipelines/mypipeline/config",
					ghttp.CombineHandlers(
						ghttp.VerifyHeaderKV(atc.ConfigVersionHeader, "42"),
						func(w http.ResponseWriter, r *http.Request) {
							w.WriteHeader(http.StatusBadRequest)
							w.Write([]byte(`server error`))
						},
					),
				)
			})

			It("returns an error", func() {
				_, _, _, err := team.CreateOrUpdatePipelineConfig(pipelineRef, expectedVersion, expectedConfig, checkCredentials)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("server error"))
			})
		})
	})
})
