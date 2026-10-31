package integration_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/concourse/concourse/atc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/tedsuo/rata"
)

var _ = Describe("anonymous Run capabilities", func() {
	JustBeforeEach(func() {
		givenARunnableTemplate()

		// Nothing else in this suite exposes a pipeline, and without it every
		// pipeline-scoped read answers about an invisible pipeline and the
		// comparison below has almost nothing to compare. atc.Config carries
		// no top-level `public` key, so this route is the only way.
		owner := login(atcURL, "test", "test")
		_, err := owner.Team("run-team").ExposePipeline(runTemplateRef)
		Expect(err).NotTo(HaveOccurred())
	})

	It("preserves anonymous authorization across a restart", func() {
		before := captureEveryRouteAnonymously()

		// The classification is used only for the guard below. It is a
		// superset of the auth wrappa's unauthenticated case -- a public
		// pipeline's read routes delegate, and both read factories answer
		// 400/404 from an unknown placeholder before any auth decision -- and
		// a superset is all that is needed: every route the wrappa leaves
		// unauthenticated answers non-401 here, so adding a route to that case
		// can only add it here too.
		reachable := []string{}
		for name, response := range before {
			if response.Status != http.StatusUnauthorized {
				reachable = append(reachable, name)
			}
		}
		Expect(reachable).NotTo(BeEmpty(), "no route answered an anonymous caller; the sweep is comparing nothing")
		Expect(reachable).To(ContainElements(atc.GetInfo, atc.ListPipelines, atc.ListAllPipelines))

		restartATC()
		after := captureEveryRouteAnonymously()

		// Compared in full, not only over the reachable set: the one
		// disclosure shape a route-level gate could plausibly introduce is a
		// route whose anonymous answer changes from 401 to something else, and
		// only comparing the 401s catches that. Comparing a 401 with a 401
		// costs nothing.
		Expect(after).To(HaveLen(len(before)))
		for name, beforeResponse := range before {
			afterResponse, found := after[name]
			Expect(found).To(BeTrue(), name)

			Expect(afterResponse.Status).To(Equal(beforeResponse.Status), name)
			Expect(string(afterResponse.Body)).To(Equal(string(beforeResponse.Body)), name)
			Expect(comparableHeaders(afterResponse.Header)).To(Equal(comparableHeaders(beforeResponse.Header)), name)
		}

		// (i) The whole info body, not only its feature_flags object.
		// atc.Info has seven top-level fields, and an eighth is the obvious
		// way to satisfy a narrow reading of this while violating it.
		Expect(string(after[atc.GetInfo].Body)).To(Equal(string(before[atc.GetInfo].Body)))
		Expect(before[atc.GetInfo].Status).To(Equal(http.StatusOK))

		// (ii) The two anonymous pipeline collections carry the fixture, with
		// can_create_run false in both states. Without the presence assertion
		// both bodies are [] and this comparison is vacuous.
		for _, collection := range []string{atc.ListPipelines, atc.ListAllPipelines} {
			for state, captured := range map[string]map[string]capturedResponse{"before": before, "after": after} {
				fixture := findFixturePipeline(captured[collection])
				Expect(fixture.Template).NotTo(BeNil(), collection+" "+state)
				Expect(*fixture.Template).To(BeTrue(), collection+" "+state)
				Expect(fixture.CanCreateRun).NotTo(BeNil(), collection+" "+state)
				Expect(*fixture.CanCreateRun).To(BeFalse(), collection+" "+state)
			}
		}

	})
})

// captureEveryRouteAnonymously issues every route in the route table with no
// credentials, using its declared method, and returns the whole answer.
//
// Some members carry a weak comparison and are still issued and still
// compared: DownloadCLI, CheckResourceWebHook and the badge routes answer
// about something this fixture does not set up, so
// their two answers agree for reasons unrelated to authorization. Naming them is
// the point -- a reader should know which parts of this sweep are load-bearing.
func captureEveryRouteAnonymously() map[string]capturedResponse {
	GinkgoHelper()

	params := anonymousRouteParams()
	Expect(params).NotTo(BeEmpty(), "no path parameters derived from the route table")

	anonymous := &http.Client{Timeout: 30 * time.Second}
	captured := map[string]capturedResponse{}

	for _, route := range atc.Routes {
		// Derived, not hand-listed: rata answers "missing param :x" rather
		// than a path, and a sweep that skipped a route on that error would
		// let the list fall silently behind a new parameter.
		path, err := atc.Routes.CreatePathForRoute(route.Name, params)
		Expect(err).NotTo(HaveOccurred(), route.Name)

		request, err := http.NewRequest(route.Method, atcURL+path, nil)
		Expect(err).NotTo(HaveOccurred(), route.Name)

		response, err := anonymous.Do(request)
		// A transport error is not a 401 and is not a pass. Without this rule
		// a server that was down for the whole sweep would yield two identical
		// empty result sets, and the comparison would succeed.
		Expect(err).NotTo(HaveOccurred(), "anonymous request to "+route.Name+" did not complete")

		captured[route.Name] = readResponse(response)
	}

	Expect(captured).To(HaveLen(len(atc.Routes)))
	return captured
}

// anonymousRouteParams derives one value for every :-prefixed segment that
// appears anywhere in the route table, so no route can be skipped for want of
// a parameter.
func anonymousRouteParams() rata.Params {
	params := rata.Params{}
	for _, route := range atc.Routes {
		for _, segment := range strings.Split(route.Path, "/") {
			name, isParam := strings.CutPrefix(segment, ":")
			if !isParam {
				continue
			}
			if _, already := params[name]; already {
				continue
			}
			switch name {
			case "team_name":
				params[name] = "run-team"
			case "pipeline_name":
				params[name] = runTemplateRef.Name
			case "build_id", "artifact_id", "id", "number", "resource_config_version_id":
				// Numeric in their handlers; a stable value either way.
				params[name] = "1"
			default:
				params[name] = "placeholder"
			}
		}
	}
	return params
}

// comparableHeaders drops the headers that differ between any two responses
// for reasons of their own. Date is stamped by net/http on every response, so
// a comparison that does not drop it fails on every route -- and one that
// drops headers wholesale leaves the header clause unchecked.
func comparableHeaders(header http.Header) http.Header {
	comparable := header.Clone()
	comparable.Del("Date")
	return comparable
}

func findFixturePipeline(response capturedResponse) atc.Pipeline {
	GinkgoHelper()

	var pipelines []atc.Pipeline
	Expect(json.Unmarshal(response.Body, &pipelines)).To(Succeed(), string(response.Body))

	for _, pipeline := range pipelines {
		if pipeline.Name == runTemplateRef.Name && pipeline.TeamName == "run-team" && len(pipeline.InstanceVars) == 0 {
			return pipeline
		}
	}

	Fail("the exposed fixture template is absent from this anonymous collection; the comparison would be vacuous: " + string(response.Body))
	return atc.Pipeline{}
}
