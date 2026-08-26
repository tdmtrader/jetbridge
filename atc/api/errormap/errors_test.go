package errormap_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/errormap"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Mutation error status", func() {
	It("classifies invalid run parameters as a bad request", func() {
		// This fails if invalid client input escapes through a mutation handler as a 500.
		status, found := errormap.Status(atc.InvalidRunParamsError{Err: errors.New("parameter environment is required")})
		Expect(found).To(BeTrue())
		Expect(status).To(Equal(http.StatusBadRequest))
	})

	DescribeTable("classifies typed run mutation conflicts",
		func(err error) {
			// This fails if a lifecycle or payload mutation is accidentally exposed as a 500.
			status, found := errormap.Status(err)
			Expect(found).To(BeTrue())
			Expect(status).To(Equal(http.StatusConflict))
		},
		Entry("non-template", db.ErrPipelineRunNotTemplate),
		Entry("instanced template", db.ErrPipelineRunInstanced),
		Entry("paused template", db.ErrPipelineRunPaused),
		Entry("archived template", db.ErrPipelineRunArchived),
		Entry("payload mutation", db.ErrPipelineRunPayloadMutation),
		Entry("template has runs", db.ErrPipelineTemplateHasRuns),
		Entry("template history", db.ErrPipelineTemplateHasRunHistory),
		Entry("ordinary template transition state", db.ErrPipelineTemplateHasOrdinaryJobState),
		Entry("completed run", db.ErrPipelineRunNotRunning),
		Entry("reclaimed payload", db.ErrPipelineRunPayloadGone),
		Entry("one-off run payload", db.ErrPipelineRunOneOffBuild),
		Entry("template build", db.ErrPipelineTemplateBuild),
		Entry("ambiguous template cache", db.TaskCacheIdentityConflictError{JobName: "deploy-((environment))"}),
	)

	It("leaves unknown errors for the existing internal-error paths", func() {
		// This fails if unrelated storage failures are incorrectly reported as user conflicts.
		_, found := errormap.Status(errors.New("database unavailable"))
		Expect(found).To(BeFalse())
	})

	It("writes an actionable typed error body as JSON", func() {
		// This fails if a client-visible conflict loses the domain message needed to
		// recover, or answers a JSON handler with a text/plain body.
		writer := httptest.NewRecorder()
		Expect(errormap.Write(writer, db.ErrPipelineRunPaused)).To(BeTrue())
		Expect(writer.Code).To(Equal(http.StatusConflict))
		Expect(writer.Header().Get("Content-Type")).To(Equal("application/json"))

		var decoded atc.SaveConfigResponse
		Expect(json.Unmarshal(writer.Body.Bytes(), &decoded)).To(Succeed())
		Expect(decoded.Errors).To(ConsistOf(ContainSubstring("paused")))
	})
})
