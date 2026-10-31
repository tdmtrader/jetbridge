package behavioral_test

import (
	"encoding/json"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("K8s Worker Registration", func() {

	// The namespace's one synthetic worker is registered by the web's own
	// registrar. With fly workers gone, /api/v1/health is the operator's view
	// of it: "workers" is "ok" only when a worker record exists.
	It("registers the synthetic worker", func() {
		status, body := apiGet("/api/v1/health")
		Expect(status).To(Equal(http.StatusOK), "health returned %d: %s", status, string(body))

		var health struct {
			Workers string `json:"workers"`
		}
		Expect(json.Unmarshal(body, &health)).To(Succeed())
		Expect(health.Workers).To(Equal("ok"), "health body: %s", string(body))
	})
})
