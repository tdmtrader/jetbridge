package steps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/event"
	gocache "github.com/patrickmn/go-cache"
)

type DBBuildFinalObservation struct{ Value string }

func DBBuildFinalStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBBuildFinalObservation](
			"the final real DB build evaluates profile {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBBuildFinalObservation, error) {
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBBuildFinalObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				profile, _ := p.GetString(0)
				value, err := observeDBBuildFinal(database, profile)
				return DBBuildFinalObservation{Value: value}, err
			},
		),
		CheckString[DBBuildFinalObservation]("the final DB build observation is {string}", "final DB build observation", func(in DBBuildFinalObservation) (string, error) { return in.Value, nil }),
	}
}

func dbBuildFinalPending(database JetbridgeDB, suffix string) (db.Team, db.Pipeline, db.Job, db.Build, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "db-build-final-" + suffix + "-team"})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: "pipeline"}, atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}}, 0, false)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	job, found, err := pipeline.Job("job")
	if err != nil || !found {
		return nil, nil, nil, nil, fmt.Errorf("load final job: found=%t: %w", found, err)
	}
	build, err := job.CreateBuild("brine-user")
	return team, pipeline, job, build, err
}

func dbBuildFinalEnvelopeMatches(envelope event.Envelope, expected atc.Event, eventID string) bool {
	expectedData, err := json.Marshal(expected)
	return err == nil && envelope.Event == expected.EventType() && envelope.Version == expected.Version() && envelope.EventID == eventID && envelope.Data != nil && string(*envelope.Data) == string(expectedData)
}

func observeDBBuildFinalFinish(database JetbridgeDB, profile string) (string, error) {
	if profile == "versions" {
		return observeDBBuildFinalFinishVersions(database)
	}
	scenario := &dbtest.Scenario{}
	if err := database.Builder.WithPipeline(atc.Config{Jobs: atc.JobConfigs{{Name: "job", PlanSequence: []atc.Step{{Config: &atc.TaskStep{Name: "task", Config: &atc.TaskConfig{Platform: "linux"}}}}}}})(scenario); err != nil {
		return "", err
	}
	var build db.Build
	if err := database.Builder.WithJobBuild(&build, "job", dbtest.JobInputs{}, dbtest.JobOutputs{})(scenario); err != nil {
		return "", err
	}
	if err := build.Finish(db.BuildStatusSucceeded); err != nil {
		return "", err
	}
	if found, err := build.Reload(); err != nil || !found {
		return "", fmt.Errorf("reload finished build: found=%t: %w", found, err)
	}
	switch profile {
	case "event":
		source, err := build.Events(0)
		if err != nil {
			return "", err
		}
		defer source.Close()
		envelope, err := source.Next()
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("status=%s;exact=%t", build.Status(), dbBuildFinalEnvelopeMatches(envelope, event.Status{Status: atc.StatusSucceeded, Time: build.EndTime().Unix()}, "0")), nil
	case "status":
		return "status=" + string(build.Status()), nil
	case "private-plan":
		return fmt.Sprintf("empty=%t", reflect.DeepEqual(build.PrivatePlan(), atc.Plan{})), nil
	default:
		return "", fmt.Errorf("unknown finish profile %q", profile)
	}
}

func dbBuildFinalDigest(version atc.Version) string {
	bytes, _ := json.Marshal(version)
	digest := sha256.Sum256(bytes)
	return hex.EncodeToString(digest[:])
}

func observeDBBuildFinalFinishVersions(database JetbridgeDB) (string, error) {
	config := atc.Config{
		Jobs: atc.JobConfigs{{Name: "job", PlanSequence: []atc.Step{
			{Config: &atc.GetStep{Name: "input-1", Resource: "one"}},
			{Config: &atc.GetStep{Name: "input-2", Resource: "two"}},
			{Config: &atc.GetStep{Name: "input-3", Resource: "one"}},
			{Config: &atc.GetStep{Name: "input-4", Resource: "one"}},
			{Config: &atc.PutStep{Name: "output-1", Resource: "one"}},
			{Config: &atc.PutStep{Name: "output-2", Resource: "one"}},
		}}},
		Resources: atc.ResourceConfigs{
			{Name: "one", Type: dbtest.BaseResourceType, Source: atc.Source{"source": "one"}},
			{Name: "two", Type: dbtest.BaseResourceType, Source: atc.Source{"source": "two"}},
		},
	}
	scenario := &dbtest.Scenario{}
	if err := database.Builder.WithPipeline(config)(scenario); err != nil {
		return "", err
	}
	if err := database.Builder.WithResourceVersions("one", atc.Version{"v": "1"}, atc.Version{"v": "2"}, atc.Version{"v": "3"})(scenario); err != nil {
		return "", err
	}
	if err := database.Builder.WithResourceVersions("two", atc.Version{"v": "3"})(scenario); err != nil {
		return "", err
	}
	var build db.Build
	if err := database.Builder.WithJobBuild(&build, "job", dbtest.JobInputs{
		{Name: "input-1", Version: atc.Version{"v": "1"}},
		{Name: "input-2", Version: atc.Version{"v": "3"}},
		{Name: "input-3", Version: atc.Version{"v": "2"}},
		{Name: "input-4", Version: atc.Version{"v": "2"}},
	}, dbtest.JobOutputs{"output-1": atc.Version{"v": "2"}, "output-2": atc.Version{"v": "3"}})(scenario); err != nil {
		return "", err
	}
	if err := build.Finish(db.BuildStatusSucceeded); err != nil {
		return "", err
	}
	versionsDB := db.NewVersionsDB(database.Conn, 100, gocache.New(10*time.Second, 10*time.Second))
	outputs, err := versionsDB.SuccessfulBuildOutputs(context.Background(), build.ID())
	if err != nil {
		return "", err
	}
	expected := []db.AlgorithmVersion{
		{ResourceID: scenario.Resource("one").ID(), Version: db.ResourceVersion(dbBuildFinalDigest(atc.Version{"v": "1"}))},
		{ResourceID: scenario.Resource("two").ID(), Version: db.ResourceVersion(dbBuildFinalDigest(atc.Version{"v": "3"}))},
		{ResourceID: scenario.Resource("one").ID(), Version: db.ResourceVersion(dbBuildFinalDigest(atc.Version{"v": "2"}))},
		{ResourceID: scenario.Resource("one").ID(), Version: db.ResourceVersion(dbBuildFinalDigest(atc.Version{"v": "3"}))},
	}
	return fmt.Sprintf("exact=%t", sameAlgorithmVersions(outputs, expected)), nil
}

func sameAlgorithmVersions(actual, expected []db.AlgorithmVersion) bool {
	if len(actual) != len(expected) {
		return false
	}
	matched := make([]bool, len(expected))
	for _, item := range actual {
		found := false
		for i, want := range expected {
			if !matched[i] && reflect.DeepEqual(item, want) {
				matched[i], found = true, true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func observeDBBuildFinalEvents(database JetbridgeDB, profile string) (string, error) {
	_, _, _, build, err := dbBuildFinalPending(database, "events-"+profile)
	if err != nil {
		return "", err
	}
	if started, err := build.Start(atc.Plan{}); err != nil || !started {
		return "", fmt.Errorf("start event build: started=%t: %w", started, err)
	}
	if found, err := build.Reload(); err != nil || !found {
		return "", fmt.Errorf("reload started build: found=%t: %w", found, err)
	}
	if profile == "legacy-id" {
		if _, err := database.Conn.Exec("UPDATE build_events SET build_id_old = build_id, build_id = NULL WHERE build_id = $1", build.ID()); err != nil {
			return "", err
		}
		source, err := build.Events(0)
		if err != nil {
			return "", err
		}
		defer source.Close()
		envelope, err := source.Next()
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("exact=%t", dbBuildFinalEnvelopeMatches(envelope, event.Status{Status: atc.StatusStarted, Time: build.StartTime().Unix()}, "0")), nil
	}
	source, err := build.Events(0)
	if err != nil {
		return "", err
	}
	defer source.Close()
	startedEnvelope, err := source.Next()
	if err != nil {
		return "", err
	}
	startedExact := dbBuildFinalEnvelopeMatches(startedEnvelope, event.Status{Status: atc.StatusStarted, Time: build.StartTime().Unix()}, "0")
	if err := build.Finish(db.BuildStatusSucceeded); err != nil {
		return "", err
	}
	if found, err := build.Reload(); err != nil || !found {
		return "", fmt.Errorf("reload completed event build: found=%t: %w", found, err)
	}
	finishedEnvelope, err := source.Next()
	if err != nil {
		return "", err
	}
	finishedExact := dbBuildFinalEnvelopeMatches(finishedEnvelope, event.Status{Status: atc.StatusSucceeded, Time: build.EndTime().Unix()}, "1")
	_, endErr := source.Next()
	return fmt.Sprintf("started=%t;finished=%t;ended=%t", startedExact, finishedExact, endErr == db.ErrEndOfBuildEventStream), nil
}

type dbBuildFinalEventResult struct {
	envelope event.Envelope
	err      error
}

func observeDBBuildFinalSaveEvent(database JetbridgeDB) (string, error) {
	_, _, _, build, err := dbBuildFinalPending(database, "save-event")
	if err != nil {
		return "", err
	}
	source, err := build.Events(0)
	if err != nil {
		return "", err
	}
	defer source.Close()
	logs := []event.Log{{Payload: "some "}, {Payload: "log"}}
	ordered := true
	for i, log := range logs {
		if err := build.SaveEvent(log); err != nil {
			return "", err
		}
		envelope, err := source.Next()
		ordered = ordered && err == nil && dbBuildFinalEnvelopeMatches(envelope, log, fmt.Sprint(i))
	}
	fromOne, err := build.Events(1)
	if err != nil {
		return "", err
	}
	defer fromOne.Close()
	offsetEnvelope, err := fromOne.Next()
	offset := err == nil && dbBuildFinalEnvelopeMatches(offsetEnvelope, logs[1], "1")
	wakeup := make(chan dbBuildFinalEventResult, 1)
	go func() {
		envelope, err := source.Next()
		wakeup <- dbBuildFinalEventResult{envelope: envelope, err: err}
	}()
	select {
	case <-wakeup:
		return "", fmt.Errorf("subscriber returned before a persisted event")
	case <-time.After(100 * time.Millisecond):
	}
	third := event.Log{Payload: "log 2"}
	if err := build.SaveEvent(third); err != nil {
		return "", err
	}
	woke := false
	select {
	case result := <-wakeup:
		woke = result.err == nil && dbBuildFinalEnvelopeMatches(result.envelope, third, "2")
	case <-time.After(2 * time.Second):
		return "", fmt.Errorf("persisted subscriber did not wake")
	}
	closedSource, err := build.Events(0)
	if err != nil {
		return "", err
	}
	if err := closedSource.Close(); err != nil {
		return "", err
	}
	_, closeErr := closedSource.Next()
	return fmt.Sprintf("ordered=%t;offset=%t;woke=%t;closed=%t", ordered, offset, woke, closeErr == db.ErrBuildEventStreamClosed), nil
}

func dbBuildFinalResourceScenario(database JetbridgeDB, profile string) (*dbtest.Scenario, db.Build, error) {
	config := atc.Config{
		Jobs: atc.JobConfigs{{Name: "job", PlanSequence: []atc.Step{
			{Config: &atc.GetStep{Name: "input", Resource: "one"}},
			{Config: &atc.PutStep{Name: "one"}},
			{Config: &atc.PutStep{Name: "two"}},
		}}},
		Resources: atc.ResourceConfigs{
			{Name: "one", Type: dbtest.BaseResourceType, Source: atc.Source{"source": "one"}},
			{Name: "two", Type: dbtest.BaseResourceType, Source: atc.Source{"source": "two"}},
			{Name: "unused", Type: dbtest.BaseResourceType, Source: atc.Source{"source": "unused"}},
		},
	}
	scenario := &dbtest.Scenario{}
	if err := database.Builder.WithPipeline(config)(scenario); err != nil {
		return nil, nil, err
	}
	if err := database.Builder.WithResourceVersions("one", atc.Version{"v": "1"}, atc.Version{"v": "2"})(scenario); err != nil {
		return nil, nil, err
	}
	if profile == "repeated" {
		var earlier db.Build
		if err := database.Builder.WithJobBuild(&earlier, "job", dbtest.JobInputs{{Name: "input", Version: atc.Version{"v": "1"}, FirstOccurrence: true}}, dbtest.JobOutputs{"one": atc.Version{"v": "2"}, "two": atc.Version{"v": "not-checked"}})(scenario); err != nil {
			return nil, nil, err
		}
	}
	var build db.Build
	if err := database.Builder.WithJobBuild(&build, "job", dbtest.JobInputs{{Name: "input", Version: atc.Version{"v": "1"}, FirstOccurrence: true}}, dbtest.JobOutputs{"one": atc.Version{"v": "2"}, "two": atc.Version{"v": "not-checked"}})(scenario); err != nil {
		return nil, nil, err
	}
	if profile != "exact" {
		result, err := database.Conn.Exec("UPDATE build_resource_config_version_inputs SET first_occurrence = NULL WHERE build_id = $1 AND resource_id = $2 AND version_digest = $3", build.ID(), scenario.Resource("one").ID(), dbBuildFinalDigest(atc.Version{"v": "1"}))
		if err != nil {
			return nil, nil, err
		}
		rows, err := result.RowsAffected()
		if err != nil || rows != 1 {
			return nil, nil, fmt.Errorf("clear first occurrence: rows=%d: %w", rows, err)
		}
	}
	return scenario, build, nil
}

func observeDBBuildFinalResources(database JetbridgeDB, profile string) (string, error) {
	scenario, build, err := dbBuildFinalResourceScenario(database, profile)
	if err != nil {
		return "", err
	}
	inputs, outputs, err := build.Resources()
	if err != nil {
		return "", err
	}
	if profile == "exact" {
		expectedInputs := []db.BuildInput{{Name: "input", ResourceID: scenario.Resource("one").ID(), Version: atc.Version{"v": "1"}, FirstOccurrence: true}}
		expectedOutputs := []db.BuildOutput{{Name: "one", Version: atc.Version{"v": "2"}}, {Name: "two", Version: atc.Version{"v": "not-checked"}}}
		return fmt.Sprintf("exact=%t", sameBuildInputs(inputs, expectedInputs) && sameBuildOutputs(outputs, expectedOutputs)), nil
	}
	first := false
	for _, input := range inputs {
		if input.Name == "input" {
			first = input.FirstOccurrence
		}
	}
	return fmt.Sprintf("first=%t", first), nil
}

func sameBuildInputs(actual, expected []db.BuildInput) bool {
	if len(actual) != len(expected) {
		return false
	}
	for _, want := range expected {
		found := false
		for _, got := range actual {
			found = found || reflect.DeepEqual(got, want)
		}
		if !found {
			return false
		}
	}
	return true
}

func sameBuildOutputs(actual, expected []db.BuildOutput) bool {
	if len(actual) != len(expected) {
		return false
	}
	for _, want := range expected {
		found := false
		for _, got := range actual {
			found = found || reflect.DeepEqual(got, want)
		}
		if !found {
			return false
		}
	}
	return true
}

func observeDBBuildFinal(database JetbridgeDB, profile string) (string, error) {
	switch profile {
	case "finish-event", "finish-status", "finish-private-plan", "finish-versions":
		return observeDBBuildFinalFinish(database, profile[len("finish-"):])
	case "events-lifecycle":
		return observeDBBuildFinalEvents(database, "lifecycle")
	case "events-legacy-id":
		return observeDBBuildFinalEvents(database, "legacy-id")
	case "save-event":
		return observeDBBuildFinalSaveEvent(database)
	case "resources-exact", "resources-first", "resources-repeated":
		return observeDBBuildFinalResources(database, profile[len("resources-"):])
	default:
		return "", fmt.Errorf("unknown final DB build profile %q", profile)
	}
}
