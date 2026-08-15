package api_test

import (
	"encoding/json"
	"net/http"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/infoserver"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Health", func() {
	BeforeEach(func() {
		useProfile(anonymousProfile)
	})

	saveHealthyWorker := func() {
		GinkgoHelper()

		_, err := apiDB.Deps.workerFactory.SaveWorker(atc.Worker{
			Name:     "health-worker",
			Platform: "linux",
			Version:  "1.2.3",
			State:    string(db.WorkerStateRunning),
		}, 0)
		Expect(err).NotTo(HaveOccurred())
	}

	expectHealth := func(expectedHTTPStatus int, expected infoserver.HealthStatus) {
		GinkgoHelper()

		response, err := client.Get(server.URL + "/api/v1/health")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(response.Body.Close)

		Expect(response.StatusCode).To(Equal(expectedHTTPStatus))
		Expect(response.Header.Get("Content-Type")).To(Equal("application/json"))

		var status infoserver.HealthStatus
		Expect(json.NewDecoder(response.Body).Decode(&status)).To(Succeed())
		Expect(status.Healthy).To(Equal(expected.Healthy))
		Expect(status.DB).To(Equal(expected.DB))
		Expect(status.Workers).To(Equal(expected.Workers))
	}

	It("reports a live database and persisted worker as healthy", func() {
		saveHealthyWorker()

		expectHealth(http.StatusOK, infoserver.HealthStatus{
			Healthy: true,
			DB:      "ok",
			Workers: "ok",
		})
	})

	It("reports that no workers are registered", func() {
		expectHealth(http.StatusServiceUnavailable, infoserver.HealthStatus{
			Healthy: false,
			DB:      "ok",
			Workers: "none",
		})
	})

	It("reports an unavailable health connection", func() {
		saveHealthyWorker()
		apiDB.disconnectHealth()

		expectHealth(http.StatusServiceUnavailable, infoserver.HealthStatus{
			Healthy: false,
			DB:      "unhealthy",
			Workers: "ok",
		})
	})

	It("reports an unavailable worker connection", func() {
		saveHealthyWorker()
		apiDB.disconnectWorker()

		expectHealth(http.StatusServiceUnavailable, infoserver.HealthStatus{
			Healthy: false,
			DB:      "ok",
			Workers: "error",
		})
	})

	It("reports both unavailable database dependencies", func() {
		saveHealthyWorker()
		apiDB.disconnectHealth()
		apiDB.disconnectWorker()

		expectHealth(http.StatusServiceUnavailable, infoserver.HealthStatus{
			Healthy: false,
			DB:      "unhealthy",
			Workers: "error",
		})
	})
})
