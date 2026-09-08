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

// Whether this server holds run creation is an operator's business, not an
// anonymous caller's. This sweep issues every route in the route table with no
// credentials in each gate state and compares the two answers in full, because
// the disclosure could be anywhere: a status, a header, a field, a route that
// changes from 401 to something else.

// The spellings that must never reach an anonymous body, in any key or value.
var gateSpellings = []string{
	"enable-pipeline-run-creation",
	"EnablePipelineRunCreation",
	"enablePipelineRunCreation",
	"pipeline_run_creation",
}

var _ = Describe("the gate is invisible to an anonymous caller", func() {
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

	It("answers an anonymous caller identically whether creation is held or admitted", func() {
		waitForSigningKeys()

		held := captureEveryRouteAnonymously()

		// The classification is used only for the guard below. It is a
		// superset of the auth wrappa's unauthenticated case -- a public
		// pipeline's read routes delegate, and both read factories answer
		// 400/404 from an unknown placeholder before any auth decision -- and
		// a superset is all that is needed: every route the wrappa leaves
		// unauthenticated answers non-401 here, so adding a route to that case
		// can only add it here too.
		reachable := []string{}
		for name, response := range held {
			if response.Status != http.StatusUnauthorized {
				reachable = append(reachable, name)
			}
		}
		Expect(reachable).NotTo(BeEmpty(), "no route answered an anonymous caller; the sweep is comparing nothing")
		Expect(reachable).To(ContainElements(atc.GetInfo, atc.ListPipelines, atc.ListAllPipelines))

		restartATCWithGate(true)
		admitted := captureEveryRouteAnonymously()

		// Compared in full, not only over the reachable set: the one
		// disclosure shape a route-level gate could plausibly introduce is a
		// route whose anonymous answer changes from 401 to something else, and
		// only comparing the 401s catches that. Comparing a 401 with a 401
		// costs nothing.
		Expect(admitted).To(HaveLen(len(held)))
		for name, heldResponse := range held {
			admittedResponse, found := admitted[name]
			Expect(found).To(BeTrue(), name)

			Expect(admittedResponse.Status).To(Equal(heldResponse.Status), name)
			Expect(string(admittedResponse.Body)).To(Equal(string(heldResponse.Body)), name)
			Expect(comparableHeaders(admittedResponse.Header)).To(Equal(comparableHeaders(heldResponse.Header)), name)
		}

		// (i) The whole info body, not only its feature_flags object.
		// atc.Info has seven top-level fields, and an eighth is the obvious
		// way to satisfy a narrow reading of this while violating it.
		Expect(string(admitted[atc.GetInfo].Body)).To(Equal(string(held[atc.GetInfo].Body)))
		Expect(held[atc.GetInfo].Status).To(Equal(http.StatusOK))

		// (ii) The two anonymous pipeline collections carry the fixture, with
		// can_create_run false in both states. Without the presence assertion
		// both bodies are [] and this comparison is vacuous.
		for _, collection := range []string{atc.ListPipelines, atc.ListAllPipelines} {
			for state, captured := range map[string]map[string]capturedResponse{"held": held, "admitted": admitted} {
				fixture := findFixturePipeline(captured[collection])
				Expect(fixture.Template).NotTo(BeNil(), collection+" "+state)
				Expect(*fixture.Template).To(BeTrue(), collection+" "+state)
				Expect(fixture.CanCreateRun).NotTo(BeNil(), collection+" "+state)
				Expect(*fixture.CanCreateRun).To(BeFalse(), collection+" "+state)
			}
		}

		// (iii) No body from either boot, on any route, names the switch.
		for state, captured := range map[string]map[string]capturedResponse{"held": held, "admitted": admitted} {
			for name, response := range captured {
				for _, spelling := range gateSpellings {
					Expect(string(response.Body)).NotTo(ContainSubstring(spelling), name+" "+state)
				}
			}
		}
	})
})

// captureEveryRouteAnonymously issues every route in the route table with no
// credentials, using its declared method, and returns the whole answer.
//
// Some members carry a weak comparison and are still issued and still
// compared: DownloadCLI, CheckResourceWebHook, the badge routes and the
// .well-known routes answer about something this fixture does not set up, so
// their two answers agree for reasons unrelated to the gate. Naming them is
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

// waitForSigningKeys settles the one piece of startup state that is not ready
// when the server first answers. The signing key lifecycler generates and
// stores its keys asynchronously, so a capture taken too early reads an empty
// key set while the second boot -- which finds them already in the database --
// does not. Waiting keeps GetSigningKeys inside the comparison; excluding its
// body would have taken a route out of it.
func waitForSigningKeys() {
	GinkgoHelper()

	path, err := atc.Routes.CreatePathForRoute(atc.GetSigningKeys, rata.Params{})
	Expect(err).NotTo(HaveOccurred())

	Eventually(func() int {
		response, err := http.Get(atcURL + path)
		if err != nil {
			return 0
		}
		var keys struct {
			Keys []json.RawMessage `json:"keys"`
		}
		if json.Unmarshal(readResponse(response).Body, &keys) != nil {
			return 0
		}
		return len(keys.Keys)
	}, 30*time.Second).Should(BeNumerically(">", 0), "the signing keys never appeared; every capture below would race the lifecycler")
}
