package configserver_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/configserver"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Template configuration save", func() {
	It("rejects an instanced template before persistence", func() {
		keepLast := 1
		config := atc.Config{
			Template:     true,
			RunRetention: &atc.RunRetentionConfig{KeepLast: &keepLast},
			Jobs:         atc.JobConfigs{{Name: "entry", PlanSequence: []atc.Step{}}},
		}
		body, err := json.Marshal(config)
		Expect(err).NotTo(HaveOccurred())

		request := httptest.NewRequest(http.MethodPut, "/?%3Ateam_name=main&%3Apipeline_name=template&vars.branch=%22main%22", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()

		configserver.NewServer(lagertest.NewTestLogger("test"), nil, nil).SaveConfig(response, request)

		Expect(response.Code).To(Equal(http.StatusBadRequest))
		Expect(response.Body.Bytes()).To(MatchJSON(`{"errors":["templates cannot have instance vars"]}`))
	})
})
