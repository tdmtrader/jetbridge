package buildserver_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/buildserver"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("GetBuildPlan", func() {
	requestPlan := func(build db.BuildForAPI) *httptest.ResponseRecorder {
		server := buildserver.NewServer(lagertest.NewTestLogger("test"), "", nil, nil, nil)
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/v1/builds/1/plan", nil)
		server.GetBuildPlan(build).ServeHTTP(response, request)
		return response
	}

	It("serves the stored public plan", func() {
		plan := atc.Plan{
			ID: "0",
			Do: &atc.DoPlan{
				{ID: "8/task", Task: &atc.TaskPlan{Name: "build"}},
			},
		}
		build := startedBuildForAPI(createTeam("some-team"), plan)

		response := requestPlan(build)

		Expect(response.Code).To(Equal(http.StatusOK))

		var body struct {
			Schema string          `json:"schema"`
			Plan   json.RawMessage `json:"plan"`
		}
		Expect(json.Unmarshal(response.Body.Bytes(), &body)).To(Succeed())
		Expect(body.Schema).To(Equal(build.Schema()))
		// Served verbatim: the bytes the handler writes are the bytes the row
		// holds, not a re-marshalled approximation of them.
		Expect([]byte(body.Plan)).To(MatchJSON([]byte(*build.PublicPlan())))
		Expect(string(body.Plan)).To(ContainSubstring(`"8/task"`))
	})

	It("returns not found for a build that has no plan", func() {
		// A one-off build that was never started carries no plan.
		build := buildForAPI(createBuild(createTeam("some-team")))

		Expect(requestPlan(build).Code).To(Equal(http.StatusNotFound))
	})

	It("preserves a nil plan", func() {
		// db.build cannot reach this state: HasPlan() is
		// `string(*b.publicPlan) != "{}"` (build.go:379), so a nil publicPlan
		// panics before it can be reported as present. The handler still has to
		// cope, because BuildForAPI is an interface with more than one
		// implementation -- so this one spec keeps a narrow fake for a state no
		// real row can hold.
		build := new(dbfakes.FakeBuildForAPI)
		build.HasPlanReturns(true)
		build.SchemaReturns("exec.v1")
		build.PublicPlanReturns(nil)

		response := requestPlan(build)

		Expect(response.Code).To(Equal(http.StatusOK))
		Expect(response.Body.String()).To(ContainSubstring(`"plan":null`))
	})
})
