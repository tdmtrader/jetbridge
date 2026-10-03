package atctest_test

import (
	"archive/tar"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"testing"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/atctest"
)

func TestMain(m *testing.M) { os.Exit(atctest.Run(m)) }

func producerTemplate(result string) atc.Config {
	return atc.Config{Jobs: atc.JobConfigs{{Name: "work", PlanSequence: []atc.Step{{Config: &atc.TaskStep{
		Name: "produce", TaskID: "2f0e9c1a-7d1b-4c56-9a53-3d6f7c1e8b10", RunResult: &atc.RunResult{Name: result, Output: "out"},
		Config: &atc.TaskConfig{Platform: "linux", RootfsURI: "docker:///" + atctest.Pin, Outputs: []atc.TaskOutputConfig{{Name: "out"}},
			Run: atc.TaskRunConfig{Path: "true"}},
	}}}}}}
}

func get(t *testing.T, client *http.Client, url string, into any) *http.Response {
	t.Helper()
	response, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	if into != nil {
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: HTTP %d", url, response.StatusCode)
		}
		if err := json.NewDecoder(response.Body).Decode(into); err != nil {
			t.Fatal(err)
		}
	}
	return response
}

// A Run the runtime starts, publishes and finishes is observed through the
// public routes exactly as the runtime left it: credentials available once
// its producer starts, and the published file served back as the result.
func TestARunIsDrivenToAPublishedResult(t *testing.T) {
	p := atctest.Get(t)
	team := p.NewTeam(t)
	p.Install(t, team, "work", producerTemplate("outcome"))
	run := p.Admit(t, team, "work")
	client := p.Client(t)
	runURL := p.URL + "/api/v1/teams/" + team + "/pipelines/work/runs/" + strconv.Itoa(run.Number)
	session := p.URL + "/api/v2/teams/" + team + "/pipelines/work/runs/" + strconv.Itoa(run.Number) + "/credentials/outcome"

	var state atc.RunCredentialSession
	get(t, client, session, &state)
	if state.Status != "waiting" || state.RunID != run.ID {
		t.Fatalf("an unstarted producer's session: %+v", state)
	}
	producer := p.Start(t, team, "work", run.Number, "outcome")
	get(t, client, session, &state)
	if state.Status != "available" {
		t.Fatalf("a started producer's session: %+v", state)
	}
	producer.Publish(t, map[string][]byte{"result.json": []byte(`{"ok":true}`)})
	p.Succeed(t, team, "work", run.Number, nil)

	var observed atc.PipelineRun
	get(t, client, runURL, &observed)
	if observed.Status != atc.RunStatusSucceeded || observed.Terminal == nil || observed.Terminal.Results["outcome"].Ref.Digest == "" {
		t.Fatalf("the finished Run: %+v", observed)
	}
	response := get(t, client, runURL+"/results/outcome", nil)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength < 0 {
		t.Fatalf("result read: HTTP %d, length %d, uncompressed %v", response.StatusCode, response.ContentLength, response.Uncompressed)
	}
	files := map[string]string{}
	archive := tar.NewReader(response.Body)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(archive)
		files[header.Name] = string(body)
	}
	if files["result.json"] != `{"ok":true}` {
		t.Fatalf("the served result is not what the producer published: %v", files)
	}
}
