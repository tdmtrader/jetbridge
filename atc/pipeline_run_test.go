package atc_test

import (
	"encoding/json"
	"time"

	"github.com/concourse/concourse/atc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("PipelineRun JSON", func() {
	It("emits the public timestamp fields as Unix seconds", func() {
		completedAt := time.Unix(1700000011, 0).UTC()
		reclaimRetryAfter := time.Unix(1700000022, 0).UTC()
		run := atc.PipelineRun{
			ID:                 42,
			TemplatePipelineID: 7,
			Number:             3,
			Status:             atc.RunStatusSucceeded,
			CreatedBy:          "api-user",
			CreatedAt:          time.Unix(1700000000, 0).UTC(),
			CompletedAt:        &completedAt,
			ReclaimRetryAfter:  &reclaimRetryAfter,
		}

		encoded, err := json.Marshal(run)

		Expect(err).NotTo(HaveOccurred())
		Expect(string(encoded)).To(MatchJSON(`{
			"id": 42,
			"template_pipeline_id": 7,
			"number": 3,
			"status": "succeeded",
			"created_by": "api-user",
			"created_at": 1700000000,
			"completed_at": 1700000011,
			"reclaim_retry_after": 1700000022,
			"reclaimed": false
		}`))
	})

	It("round-trips Unix-second timestamps", func() {
		completedAt := time.Unix(1700000011, 0).UTC()
		reclaimRetryAfter := time.Unix(1700000022, 0).UTC()
		run := atc.PipelineRun{
			ID:                 42,
			TemplatePipelineID: 7,
			Number:             3,
			Status:             atc.RunStatusSucceeded,
			CreatedBy:          "api-user",
			CreatedAt:          time.Unix(1700000000, 0).UTC(),
			CompletedAt:        &completedAt,
			ReclaimRetryAfter:  &reclaimRetryAfter,
		}

		encoded, err := json.Marshal(run)
		Expect(err).NotTo(HaveOccurred())

		var decoded atc.PipelineRun
		Expect(json.Unmarshal(encoded, &decoded)).To(Succeed())
		Expect(decoded).To(Equal(run))
	})

	It("rejects legacy string timestamps", func() {
		var decoded atc.PipelineRun

		Expect(json.Unmarshal([]byte(`{
			"id": 42,
			"template_pipeline_id": 7,
			"number": 3,
			"status": "succeeded",
			"created_by": "api-user",
			"created_at": "2023-11-14T22:13:20Z",
			"completed_at": "2023-11-14T22:13:31Z",
			"reclaim_retry_after": "2023-11-14T22:13:42Z",
			"reclaimed": false
		}`), &decoded)).To(MatchError(ContainSubstring("cannot unmarshal string into Go struct field")))
	})
})
