package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/errormap"
	"github.com/concourse/concourse/atc/builds"
	"github.com/concourse/concourse/atc/configvalidate"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/google/uuid"
)

const resultTaskID = "ab35bb59-bca8-4ee7-8dd6-52f278520d08"

type RunResultDeclaration struct {
	Config atc.Config
	Err    error
}

type ResultTemplateSave struct {
	Original db.Pipeline
	Err      error
}

type ResultRunAttempt struct {
	Err          error
	Runs, Number int
}

func RunResultDeclarationDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		CheckThat[ResultRunAttempt]("the admission refusal is returned to clients as a conflict", func(in ResultRunAttempt) error {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if !errormap.Write(w, in.Err) {
					w.WriteHeader(http.StatusInternalServerError)
				}
			}))
			defer server.Close()
			response, err := server.Client().Get(server.URL)
			if err != nil {
				return err
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				return err
			}
			if response.StatusCode != http.StatusConflict || !strings.Contains(string(body), "Run result execution is not activated") {
				return fmt.Errorf("expected a clear activation conflict, got HTTP %d: %s", response.StatusCode, body)
			}
			return nil
		}),
		brine.DefineMapUsing[RunResultDeclaration, ResultRunAttempt]("the existing Run creator receives the result-bearing template", []string{"jetbridge-db"}, func(in RunResultDeclaration, _ brine.Params, _ *brine.Recorder, res brine.Resources) (ResultRunAttempt, error) {
			if in.Err != nil {
				return ResultRunAttempt{}, in.Err
			}
			jdb, err := jetbridgeDBFrom(res)
			if err != nil {
				return ResultRunAttempt{}, err
			}
			team, err := jdb.TeamFactory.CreateTeam(atc.Team{Name: "result-admission"})
			if err != nil {
				return ResultRunAttempt{}, err
			}
			pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: "review"}, in.Config, 0, false)
			if err != nil {
				return ResultRunAttempt{}, err
			}
			if err := pipeline.Unpause(); err != nil {
				return ResultRunAttempt{}, err
			}
			_, createErr := dbtest.CreateRun(jdb.Conn, db.NewPipelineRunFactory(jdb.Conn, jdb.LockFactory), context.Background(), pipeline, db.RunParams{}, "brine")
			answer := ResultRunAttempt{Err: createErr}
			err = jdb.Conn.QueryRow(`SELECT last_run_number, (SELECT count(*) FROM pipeline_runs WHERE template_pipeline_id = $1) FROM pipelines WHERE id = $1`, pipeline.ID()).Scan(&answer.Number, &answer.Runs)
			return answer, err
		}),
		CheckThat[ResultRunAttempt]("Run result execution is held without allocating a Run or number", func(in ResultRunAttempt) error {
			if in.Err == nil || !strings.Contains(in.Err.Error(), "Run result execution is not activated") {
				return fmt.Errorf("expected the activation refusal, got %v", in.Err)
			}
			if in.Runs != 0 || in.Number != 0 {
				return fmt.Errorf("refused admission allocated %d Runs and number %d", in.Runs, in.Number)
			}
			return nil
		}),
		brine.DefineMap[brine.Empty, RunResultDeclaration]("a Run result declaration with {string}", func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (RunResultDeclaration, error) {
			shape, _ := p.GetString(0)
			return resultDeclaration(shape)
		}),
		brine.DefineMap[RunResultDeclaration, RunResultDeclaration]("the template author validates the result declaration", func(in RunResultDeclaration, _ brine.Params, _ *brine.Recorder) (RunResultDeclaration, error) {
			if in.Err == nil {
				in.Err = configvalidate.ValidateTemplateDeclaration(atc.PipelineRef{Name: "review"}, in.Config)
			}
			return in, nil
		}),
		brine.DefineCheck[RunResultDeclaration]("the result declaration is refused with {string}", func(in RunResultDeclaration, p brine.Params, _ *brine.Recorder) error {
			reason, _ := p.GetString(0)
			if in.Err == nil || !strings.Contains(in.Err.Error(), reason) {
				return fmt.Errorf("expected refusal containing %q, got %v", reason, in.Err)
			}
			return nil
		}),
		CheckThat[RunResultDeclaration]("materialization and planning preserve the stable task and selected output", func(in RunResultDeclaration) error {
			if in.Err != nil {
				return in.Err
			}
			materialized, err := atc.MaterializeRunConfig(in.Config, atc.RunIdentity{ID: 17, Number: 3}, nil)
			if err != nil {
				return err
			}
			plan, err := builds.NewPlanner(atc.NewPlanFactory(0)).Create(materialized.Config.Jobs[0].StepConfig(), nil, nil, nil, nil, true)
			if err != nil {
				return err
			}
			encoded, err := json.Marshal(plan)
			if err != nil {
				return err
			}
			if !strings.Contains(string(encoded), `"task_id":"`+resultTaskID+`"`) || !strings.Contains(string(encoded), `"run_result":{"name":"findings","output":"result"}`) {
				return fmt.Errorf("execution plan lost its stable result declaration: %s", encoded)
			}
			return nil
		}),
		brine.DefineMapUsing[RunResultDeclaration, ResultTemplateSave]("two base templates try to save the same stable task identity", []string{"jetbridge-db"}, func(in RunResultDeclaration, _ brine.Params, _ *brine.Recorder, res brine.Resources) (ResultTemplateSave, error) {
			if in.Err != nil {
				return ResultTemplateSave{}, in.Err
			}
			jdb, err := jetbridgeDBFrom(res)
			if err != nil {
				return ResultTemplateSave{}, err
			}
			team, err := jdb.TeamFactory.CreateTeam(atc.Team{Name: "result-owners"})
			if err != nil {
				return ResultTemplateSave{}, err
			}
			first, _, err := team.SavePipeline(atc.PipelineRef{Name: "first"}, in.Config, 0, false)
			if err != nil {
				return ResultTemplateSave{}, err
			}
			// Editing a template may retain the identity.
			first, _, err = team.SavePipeline(first.PipelineRef(), in.Config, first.ConfigVersion(), false)
			if err != nil {
				return ResultTemplateSave{}, err
			}
			_, _, err = team.SavePipeline(atc.PipelineRef{Name: "second"}, in.Config, 0, false)
			return ResultTemplateSave{Original: first, Err: err}, nil
		}),
		CheckThat[ResultTemplateSave]("the second template is refused and the original declaration is intact", func(in ResultTemplateSave) error {
			if in.Err == nil || !strings.Contains(in.Err.Error(), "owned") {
				return fmt.Errorf("expected an ownership refusal, got %v", in.Err)
			}
			config, err := in.Original.Config()
			if err != nil {
				return err
			}
			encoded, err := json.Marshal(config)
			if err != nil {
				return err
			}
			if !strings.Contains(string(encoded), resultTaskID) {
				return fmt.Errorf("original template lost its task identity")
			}
			return nil
		}),
	}
}

func resultDeclaration(shape string) (RunResultDeclaration, error) {
	producer := map[string]any{
		"task": "review", "task_id": resultTaskID,
		"run_result": map[string]any{"name": "findings", "output": "result"},
		"config":     map[string]any{"platform": "linux", "run": map[string]any{"path": "true"}, "outputs": []any{map[string]any{"name": "result"}}},
	}
	plan := []any{producer}
	template := true
	switch shape {
	case "one inline producer":
	case "renamed producer":
		producer["task"] = "renamed-((run))"
	case "a producer in a success hook":
		plan = []any{map[string]any{"task": "prepare", "config": map[string]any{"platform": "linux", "run": map[string]any{"path": "true"}}, "on_success": producer}}
	case "a producer in a parallel group":
		plan = []any{map[string]any{"in_parallel": []any{producer}}}
	case "no task identity":
		delete(producer, "task_id")
	case "malformed task identity":
		producer["task_id"] = "review"
	case "uppercase task identity":
		producer["task_id"] = strings.ToUpper(resultTaskID)
	case "zero task identity":
		producer["task_id"] = uuid.Nil.String()
	case "repeated task identity":
		plan = append(plan, producer)
	case "repeated result name":
		other := map[string]any{}
		for key, value := range producer {
			other[key] = value
		}
		other["task_id"] = uuid.NewString()
		plan = append(plan, other)
	case "missing output":
		producer["run_result"].(map[string]any)["output"] = "absent"
	case "duplicate output declaration":
		producer["config"].(map[string]any)["outputs"] = []any{map[string]any{"name": "result"}, map[string]any{"name": "result"}}
	case "file task":
		delete(producer, "config")
		producer["file"] = "repo/task.yml"
	case "across producer":
		producer["across"] = []any{map[string]any{"var": "item", "values": []any{"a", "b"}}}
	case "retried producer":
		producer["attempts"] = 2
	case "retried enclosing group":
		plan = []any{map[string]any{"do": []any{producer}, "attempts": 2}}
	case "empty result name":
		producer["run_result"].(map[string]any)["name"] = ""
	case "path result name":
		producer["run_result"].(map[string]any)["name"] = "../findings"
	case "unknown result field":
		producer["run_result"].(map[string]any)["ouptut"] = "typo"
	case "ordinary pipeline":
		template = false
	case "unreachable producer":
	case "too many results":
		plan = nil
		for i := 0; i < 65; i++ {
			plan = append(plan, map[string]any{"task": fmt.Sprintf("task-%d", i), "task_id": uuid.NewString(), "run_result": map[string]any{"name": fmt.Sprintf("result-%d", i), "output": "result"}, "config": producer["config"]})
		}
	default:
		return RunResultDeclaration{}, fmt.Errorf("unknown declaration shape %q", shape)
	}
	jobs := []any{map[string]any{"name": "review", "plan": plan}}
	if shape == "unreachable producer" {
		jobs[0].(map[string]any)["plan"] = append([]any{map[string]any{"get": "repo", "passed": []any{"entry"}}}, plan...)
		jobs = append(jobs, map[string]any{"name": "entry", "plan": []any{map[string]any{"get": "repo"}}})
	}
	document := map[string]any{"template": template, "jobs": jobs}
	if shape == "unreachable producer" {
		document["resources"] = []any{map[string]any{"name": "repo", "type": "git", "source": map[string]any{"uri": "https://example.invalid/repo"}}}
	}
	doc, err := json.Marshal(document)
	if err != nil {
		return RunResultDeclaration{}, err
	}
	var config atc.Config
	err = atc.UnmarshalConfig(doc, &config)
	return RunResultDeclaration{Config: config, Err: err}, nil
}
