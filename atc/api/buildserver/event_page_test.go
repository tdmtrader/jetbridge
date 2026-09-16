package buildserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"

	"github.com/concourse/concourse/atc"
	. "github.com/concourse/concourse/atc/api/buildserver"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// cursorRejectingBuild answers every page with the sentinel whose text opens
// with the same code the envelope carries.
type cursorRejectingBuild struct {
	db.BuildForAPI
}

func (cursorRejectingBuild) EventPage(context.Context, atc.BuildEventPageRequest) (atc.BuildEventPage, error) {
	return atc.BuildEventPage{}, db.ErrBuildEventCursor
}

var _ = Describe("Event page errors", func() {
	It("states the code once", func() {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest("GET", "/?format=json&cursor=stale", nil)

		(&Server{}).BuildEvents(cursorRejectingBuild{}).ServeHTTP(recorder, request)

		Expect(recorder.Code).To(Equal(http.StatusBadRequest))
		var envelope atc.ErrorResponse
		Expect(json.Unmarshal(recorder.Body.Bytes(), &envelope)).To(Succeed())
		Expect(envelope.Code).To(Equal("INVALID_CURSOR"))
		// A client that prefixes the code -- the MCP adapter does -- would
		// otherwise print "INVALID_CURSOR: INVALID_CURSOR: ...".
		Expect(envelope.Errors).To(HaveLen(1))
		Expect(envelope.Errors[0]).ToNot(HavePrefix("INVALID_CURSOR"))
		Expect(envelope.Errors[0]).To(ContainSubstring("cursor"))
	})
})
