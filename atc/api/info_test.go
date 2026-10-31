package api_test

import (
	"io"
	"net/http"

	"github.com/concourse/concourse/atc/creds/kubernetes"
	. "github.com/concourse/concourse/atc/testhelpers"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Pipelines API", func() {
	Describe("GET /api/v1/info", func() {
		var response *http.Response

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

			Expect(body).To(MatchJSON(`{
				"version": "1.2.3",
				"worker_version": "4.5.6",
				"external_url": "https://example.com",
				"cluster_name": "Test Cluster",
				"jetbridge_version": "0.1.0-test",
				"concourse_version": "8.0.1-test"
			}`))
		})
	})

	Describe("GET /api/v1/info/creds", func() {
		var (
			response *http.Response
			body     []byte
		)

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

		BeforeEach(func() {
			scriptedAccess.authenticated = true
			scriptedAccess.admin = true
			credsManagers["kubernetes"] = &kubernetes.KubernetesManager{
				InClusterConfig: true,
				NamespacePrefix: "concourse-",
			}
		})

		It("returns the configured Kubernetes manager", func() {
			Expect(body).To(MatchJSON(`{"kubernetes": {
                "in_cluster_config": true,
                "config_path": "",
                "namespace_config": "concourse-"
            }}`))
		})
	})
})
