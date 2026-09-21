package steps

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/builds"
	"github.com/google/uuid"
)

func RunInputDeclarationDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, RunResultDeclaration]("a Run input declaration with {string}", func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (RunResultDeclaration, error) {
			shape, _ := p.GetString(0)
			return inputDeclaration(shape)
		}),
		CheckThat[RunResultDeclaration]("the execution plan retains every named Run input route", func(in RunResultDeclaration) error {
			if in.Err != nil {
				return in.Err
			}
			before, err := json.Marshal(in.Config)
			if err != nil {
				return err
			}
			materialized, err := atc.MaterializeRunConfig(in.Config, atc.RunIdentity{ID: 17, Number: 3}, nil)
			if err != nil {
				return err
			}
			plan, err := builds.NewPlanner(atc.NewPlanFactory(0)).Create(materialized.Config.Jobs[0].StepConfig(), nil, nil, nil, nil, true)
			if err != nil {
				return err
			}
			after, err := json.Marshal(plan)
			if err != nil {
				return err
			}
			original, err := inputRoutes(before)
			if err != nil {
				return err
			}
			planned, err := inputRoutes(after)
			if err != nil {
				return err
			}
			if len(original) == 0 || !reflect.DeepEqual(original, planned) {
				return fmt.Errorf("planning changed or omitted named Run input routes: %v -> %v", original, planned)
			}
			return nil
		}),
	}
}

func inputRoutes(encoded []byte) (map[string]any, error) {
	var document any
	if err := json.Unmarshal(encoded, &document); err != nil {
		return nil, err
	}
	routes := map[string]any{}
	var walk func(any)
	walk = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			if id, ok := v["task_id"].(string); ok && v["run_inputs"] != nil {
				routes[id] = v["run_inputs"]
			}
			for _, child := range v {
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(document)
	return routes, nil
}

func inputDeclaration(shape string) (RunResultDeclaration, error) {
	route := map[string]any{"name": "change", "input": "source"}
	config := map[string]any{"platform": "linux", "run": map[string]any{"path": "true"}, "inputs": []any{map[string]any{"name": "source"}}}
	target := map[string]any{"task": "review", "task_id": resultTaskID, "run_inputs": []any{route}, "config": config}
	plan := []any{target}
	template := true
	switch shape {
	case "one inline target":
	case "renamed target":
		target["task"] = "review-((run))"
	case "two distinct slots":
		config["inputs"] = append(config["inputs"].([]any), map[string]any{"name": "other"})
		target["run_inputs"] = append(target["run_inputs"].([]any), map[string]any{"name": "change", "input": "other"})
	case "two task targets":
		plan = append(plan, map[string]any{"task": "second", "task_id": uuid.NewString(), "run_inputs": []any{route}, "config": config})
	case "separate input path":
		config["inputs"].([]any)[0].(map[string]any)["path"] = "sources/repo"
	case "no task identity":
		delete(target, "task_id")
	case "missing input":
		route["input"] = "missing"
	case "duplicate input declaration":
		config["inputs"] = append(config["inputs"].([]any), map[string]any{"name": "source"})
	case "duplicate route":
		target["run_inputs"] = []any{route, route}
	case "conflicting slot":
		target["run_inputs"] = []any{route, map[string]any{"name": "plan", "input": "source"}}
	case "empty input name":
		route["name"] = ""
	case "path input name":
		route["name"] = "../change"
	case "unknown input field":
		route["slot"] = "typo"
	case "file task":
		delete(target, "config")
		target["file"] = "repo/task.yml"
	case "across target":
		target["across"] = []any{map[string]any{"var": "item", "values": []any{"a", "b"}}}
	case "retried target":
		target["attempts"] = 2
	case "unreachable target":
	case "ordinary pipeline":
		template = false
	case "too many input names", "too many routes":
		count := 65
		if shape == "too many routes" {
			count = 257
		}
		inputs, routes := []any{}, []any{}
		for i := 0; i < count; i++ {
			slot, name := fmt.Sprintf("input-%d", i), "change"
			if shape == "too many input names" {
				name = slot
			}
			inputs = append(inputs, map[string]any{"name": slot})
			routes = append(routes, map[string]any{"name": name, "input": slot})
		}
		config["inputs"], target["run_inputs"] = inputs, routes
	case "ambient input mapping":
		target["input_mapping"] = map[string]any{"source": "ambient"}
	case "output collision":
		config["outputs"] = []any{map[string]any{"name": "source"}}
	case "nested output collision":
		config["outputs"] = []any{map[string]any{"name": "output", "path": "source/report"}}
	case "cache collision":
		config["caches"] = []any{map[string]any{"path": "source/cache"}}
	case "scratch collision":
		config["scratch_paths"] = []any{map[string]any{"path": "source/scratch"}}
	case "ambient input collision":
		config["inputs"] = append(config["inputs"].([]any), map[string]any{"name": "ambient", "path": "source/ambient"})
	case "escaping input path":
		config["inputs"].([]any)[0].(map[string]any)["path"] = "../escape"
	case "interpolated input path":
		config["inputs"].([]any)[0].(map[string]any)["path"] = "((destination))"
	default:
		return RunResultDeclaration{}, fmt.Errorf("unknown input shape %q", shape)
	}
	jobs := []any{map[string]any{"name": "review", "plan": plan}}
	document := map[string]any{"template": template, "jobs": jobs}
	if shape == "unreachable target" {
		jobs[0].(map[string]any)["plan"] = append([]any{map[string]any{"get": "repo", "passed": []any{"entry"}}}, plan...)
		document["jobs"] = append(jobs, map[string]any{"name": "entry", "plan": []any{map[string]any{"get": "repo"}}})
		document["resources"] = []any{map[string]any{"name": "repo", "type": "git", "source": map[string]any{"uri": "https://example.invalid/repo"}}}
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return RunResultDeclaration{}, err
	}
	var decoded atc.Config
	err = atc.UnmarshalConfig(encoded, &decoded)
	return RunResultDeclaration{Config: decoded, Err: err}, nil
}
