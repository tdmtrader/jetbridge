package policychecker_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/policychecker"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/policy"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("PolicyChecker", func() {
	var (
		policyFilter policy.Filter
		claims       map[string]any
		teams        []db.Team
		fakeRequest  *http.Request
		result       policy.PolicyCheckResult
		checkErr     error
	)

	BeforeEach(func() {
		claims = map[string]any{}
		teams = nil
		fakeRequest = nil

		policyFilter = policy.Filter{
			ActionsToSkip: []string{},
			Actions:       []string{},
			HttpMethods:   []string{},
		}
	})

	JustBeforeEach(func() {
		access := accessor.NewAccessor(
			accessor.Verification{HasToken: true, IsTokenValid: true, RawClaims: claims},
			accessor.OwnerRole,
			systemClaimKey,
			[]string{systemClaimValue},
			teams,
			nil,
		)
		result, checkErr = policychecker.NewApiPolicyChecker(initializedChecker(policyFilter)).Check("some-action", access, fakeRequest)
	})

	Context("when system action", func() {
		BeforeEach(func() {
			claims[systemClaimKey] = systemClaimValue
			fakeRequest = httptest.NewRequest("PUT", "/something", nil)
			policyFilter.HttpMethods = []string{"PUT"}
		})
		It("should pass", func() {
			Expect(checkErr).ToNot(HaveOccurred())
			Expect(result.Allowed()).To(BeTrue())
		})
		It("Agent should not be called", func() {
			Expect(opaServer.Requests()).To(BeEmpty())
		})
	})

	Context("when not system action", func() {
		Context("when the action should be skipped", func() {
			BeforeEach(func() {
				policyFilter.ActionsToSkip = []string{"some-action"}
			})
			It("should pass", func() {
				Expect(checkErr).ToNot(HaveOccurred())
				Expect(result.Allowed()).To(BeTrue())
			})
			It("Agent should not be called", func() {
				Expect(opaServer.Requests()).To(BeEmpty())
			})
		})

		Context("when the http method no need to check", func() {
			BeforeEach(func() {
				fakeRequest = httptest.NewRequest("GET", "/something", nil)
				policyFilter.HttpMethods = []string{"PUT"}
			})
			It("should pass", func() {
				Expect(checkErr).ToNot(HaveOccurred())
				Expect(result.Allowed()).To(BeTrue())
			})
			It("Agent should not be called", func() {
				Expect(opaServer.Requests()).To(BeEmpty())
			})
		})

		Context("when not in action list", func() {
			BeforeEach(func() {
				fakeRequest = httptest.NewRequest("PUT", "/something", nil)
				policyFilter.HttpMethods = []string{}
				policyFilter.Actions = []string{}
			})
			It("should pass", func() {
				Expect(checkErr).ToNot(HaveOccurred())
				Expect(result.Allowed()).To(BeTrue())
			})
			It("Agent should not be called", func() {
				Expect(opaServer.Requests()).To(BeEmpty())
			})
		})

		Context("when the http method needs to check", func() {
			BeforeEach(func() {
				fakeRequest = httptest.NewRequest("PUT", "/something", nil)
				policyFilter.HttpMethods = []string{"PUT"}
			})

			Context("when request body is a bad json", func() {
				BeforeEach(func() {
					body := bytes.NewBuffer([]byte("hello"))
					fakeRequest = httptest.NewRequest("PUT", "/something", body)
					fakeRequest.Header.Add("Content-type", "application/json")
				})

				It("should error", func() {
					Expect(checkErr).To(HaveOccurred())
					Expect(checkErr.Error()).To(Equal(`invalid character 'h' looking for beginning of value`))
					Expect(result).To(BeNil())
				})
				It("Agent should not be called", func() {
					Expect(opaServer.Requests()).To(BeEmpty())
				})
			})

			Context("when request body is a bad yaml", func() {
				BeforeEach(func() {
					body := bytes.NewBuffer([]byte("a:\nb"))
					fakeRequest = httptest.NewRequest("PUT", "/something", body)
					fakeRequest.Header.Add("Content-type", "application/x-yaml")
				})

				It("should error", func() {
					Expect(checkErr).To(HaveOccurred())
					Expect(checkErr.Error()).To(Equal(`error converting YAML to JSON: yaml: line 3: could not find expected ':'`))
					Expect(result).To(BeNil())
				})

				It("Agent should not be called", func() {
					Expect(opaServer.Requests()).To(BeEmpty())
				})
			})

			Context("when every is ok", func() {
				BeforeEach(func() {
					teams = []db.Team{persistTeam("some-team", atc.TeamAuth{
						"some-role": {"users": {"test:some-user"}},
					})}
					claims["name"] = "some-user"
					claims["federated_claims"] = map[string]any{"connector_id": "test"}
					body := bytes.NewBuffer([]byte("a: b"))
					fakeRequest = httptest.NewRequest("PUT", "/something?:team_name=some-team&:pipeline_name=some-pipeline", body)
					fakeRequest.Header.Add("Content-type", "application/x-yaml")
					fakeRequest.ParseForm()
				})

				It("should not error", func() {
					Expect(checkErr).ToNot(HaveOccurred())
				})

				It("Agent should be called", func() {
					Expect(opaServer.Requests()).To(HaveLen(1))
				})

				It("Agent should take correct input", func() {
					Expect(opaServer.Requests()[0]).To(MatchJSON(`{
						"input": {
							"service": "concourse",
							"cluster_name": "some-cluster",
							"cluster_version": "some-version",
							"http_method": "PUT",
							"action": "some-action",
							"user": "some-user",
							"team": "some-team",
							"roles": ["some-role"],
							"pipeline": "some-pipeline",
							"data": {"a": "b"}
						}
					}`))
				})

				It("request body should still be readable", func() {
					body, err := io.ReadAll(fakeRequest.Body)
					Expect(err).ToNot(HaveOccurred())
					Expect(body).To(Equal([]byte("a: b")))
				})

				Context("when Agent says pass", func() {
					BeforeEach(func() {
						opaServer.Reset(`{"result": {"allowed": true}}`)
					})

					It("it should pass", func() {
						Expect(checkErr).ToNot(HaveOccurred())
						Expect(result.Allowed()).To(BeTrue())
					})
				})

				Context("when Agent says not-pass", func() {
					BeforeEach(func() {
						opaServer.Reset(`{"result": {"allowed": false, "block": true, "reasons": ["a policy says you can't do that"]}}`)
					})

					It("should not pass", func() {
						Expect(checkErr).ToNot(HaveOccurred())
						Expect(result.Allowed()).To(BeFalse())
						Expect(result.ShouldBlock()).To(BeTrue())
						Expect(result.Messages()).To(ConsistOf("a policy says you can't do that"))
					})
				})

				Context("when Agent says error", func() {
					BeforeEach(func() {
						opaServer.RespondWithStatus(http.StatusInternalServerError)
					})

					It("should not pass", func() {
						Expect(checkErr).To(HaveOccurred())
						Expect(checkErr.Error()).To(Equal("OPA server returned status: 500"))
						Expect(result).To(BeNil())
					})
				})
			})
		})
	})
})
