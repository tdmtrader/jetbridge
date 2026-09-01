package steps

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
)

type CoreValueObservation struct {
	Value string
	Err   error
}

func CoreValueDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, CoreValueObservation](
			"the production ATC value model handles profile {string}",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (CoreValueObservation, error) {
				profile, _ := p.GetString(0)
				value, observedErr, setupErr := observeCoreValue(profile)
				return CoreValueObservation{Value: value, Err: observedErr}, setupErr
			},
		),
		CheckString[CoreValueObservation]("the ATC value result is {string}", "ATC value result", func(in CoreValueObservation) (string, error) {
			return in.Value, nil
		}),
		brine.DefineCheck[CoreValueObservation]("the ATC value operation returned no error", func(in CoreValueObservation, _ brine.Params, _ *brine.Recorder) error {
			return in.Err
		}),
		brine.DefineCheck[CoreValueObservation]("the ATC value operation returned an error", func(in CoreValueObservation, _ brine.Params, _ *brine.Recorder) error {
			if in.Err == nil {
				return fmt.Errorf("ATC value operation returned no error")
			}
			return nil
		}),
	}
}

func observeCoreValue(profile string) (string, error, error) {
	if strings.HasPrefix(profile, "memory-") {
		return observeMemoryValue(strings.TrimPrefix(profile, "memory-"))
	}
	if strings.HasPrefix(profile, "build-") {
		return observeBuildValue(strings.TrimPrefix(profile, "build-"))
	}
	switch profile {
	case "ephemeral-number", "ephemeral-5g", "ephemeral-2gib":
		raw := map[string]string{
			"ephemeral-number": `{"ephemeral_storage":1073741824}`,
			"ephemeral-5g":     `{"ephemeral_storage":"5G"}`,
			"ephemeral-2gib":   `{"ephemeral_storage":"2GiB"}`,
		}[profile]
		var limits atc.ContainerLimits
		err := json.Unmarshal([]byte(raw), &limits)
		if err != nil || limits.EphemeralStorage == nil {
			return "", err, nil
		}
		return fmt.Sprintf("%d", *limits.EphemeralStorage), nil, nil
	case "ephemeral-omit":
		payload, err := json.Marshal(atc.ContainerLimits{})
		return string(payload), err, nil
	case "worker-empty":
		return "valid", atc.Worker{}.Validate(), nil
	case "worker-numeric":
		return "valid", (atc.Worker{Version: "1.2.3"}).Validate(), nil
	case "worker-invalid":
		err := (atc.Worker{Version: "a.b.c"}).Validate()
		return err.Error(), err, nil
	case "sidecar-id":
		return string(atc.SidecarPlanID("42", "cloud-sql-proxy")), nil, nil
	case "sidecar-new":
		plan := atc.NewSidecarPlan("10", atc.SidecarConfig{Name: "redis", Image: "redis:7"})
		return fmt.Sprintf("%s:%s:%s", plan.ID, plan.Sidecar.Name, plan.Sidecar.Image), nil, nil
	case "sidecar-public":
		plan := atc.Plan{ID: "5/sidecar/postgres", Sidecar: &atc.SidecarPlan{Name: "postgres", Image: "postgres:16"}}
		return canonicalPublicPlan(plan)
	case "sidecar-public-no-image":
		plan := atc.Plan{ID: "5/sidecar/helper", Sidecar: &atc.SidecarPlan{Name: "helper"}}
		return canonicalPublicPlan(plan)
	case "plan-sanitized":
		plan := fullCorePublicPlan()
		public := plan.Public()
		if public == nil {
			return "", nil, fmt.Errorf("public plan was nil")
		}
		var decoded any
		if err := json.Unmarshal(*public, &decoded); err != nil {
			return "", err, nil
		}
		canonical, err := json.Marshal(decoded)
		if err != nil {
			return "", err, nil
		}
		return fmt.Sprintf("sha256:%x", sha256.Sum256(canonical)), nil, nil
	default:
		return "", nil, fmt.Errorf("unknown ATC value profile %q", profile)
	}
}

func observeMemoryValue(profile string) (string, error, error) {
	inputs := map[string]string{
		"bytes": "1024", "kb": "1KB", "mb": "1MB", "gb": "1GB",
		"kib": "1KiB", "mib": "1MiB", "gib": "1GiB",
	}
	if input, ok := inputs[profile]; ok {
		limit, err := atc.ParseMemoryLimit(input)
		return fmt.Sprintf("%d", limit), err, nil
	}
	switch profile {
	case "case-unit":
		lower, err := atc.ParseMemoryLimit("1kb")
		if err != nil {
			return "", err, nil
		}
		upper, err := atc.ParseMemoryLimit("1KB")
		return fmt.Sprintf("equal=%t", lower == upper), err, nil
	case "case-binary":
		lower, err := atc.ParseMemoryLimit("1kib")
		if err != nil {
			return "", err, nil
		}
		upper, err := atc.ParseMemoryLimit("1KiB")
		return fmt.Sprintf("equal=%t", lower == upper), err, nil
	case "invalid":
		_, err := atc.ParseMemoryLimit("invalid")
		return "", err, nil
	case "single-prefix":
		k, err := atc.ParseMemoryLimit("1K")
		if err != nil {
			return "", err, nil
		}
		m, err := atc.ParseMemoryLimit("1m")
		if err != nil {
			return "", err, nil
		}
		g, err := atc.ParseMemoryLimit("1G")
		return fmt.Sprintf("%d,%d,%d", k, m, g), err, nil
	default:
		return "", nil, fmt.Errorf("unknown memory profile %q", profile)
	}
}

func observeBuildValue(profile string) (string, error, error) {
	switch profile {
	case "one-off":
		return fmt.Sprintf("%t", (atc.Build{}).OneOff()), nil, nil
	case "job":
		return fmt.Sprintf("%t", (atc.Build{JobName: "job"}).OneOff()), nil, nil
	}
	parts := strings.Split(profile, "-")
	if len(parts) != 2 {
		return "", nil, fmt.Errorf("unknown build value profile %q", profile)
	}
	statuses := map[string]atc.BuildStatus{
		"pending": atc.StatusPending, "started": atc.StatusStarted,
	}
	status, ok := statuses[parts[1]]
	if !ok && parts[1] == "finished" {
		for _, finished := range []atc.BuildStatus{atc.StatusAborted, atc.StatusErrored, atc.StatusFailed, atc.StatusSucceeded} {
			build := atc.Build{Status: finished}
			if build.IsRunning() || build.Abortable() {
				return "", nil, fmt.Errorf("finished status %s was running or abortable", finished)
			}
		}
		return "false", nil, nil
	}
	build := atc.Build{Status: status}
	if parts[0] == "running" {
		// The source specs under the IsRunning description call Abortable. Keep
		// this profile paired to the behavior those specs actually execute.
		return fmt.Sprintf("%t", build.Abortable()), nil, nil
	}
	if parts[0] == "abortable" {
		return fmt.Sprintf("%t", build.Abortable()), nil, nil
	}
	return "", nil, fmt.Errorf("unknown build predicate %q", parts[0])
}

func fullCorePublicPlan() atc.Plan {
	task := func(id atc.PlanID) atc.Plan {
		return atc.Plan{ID: id, Task: &atc.TaskPlan{
			Name: "name", ConfigPath: "some/config/path.yml",
			Config: &atc.TaskConfig{Params: atc.TaskEnv{"some": "secret"}},
		}}
	}
	imagePlan := func(id atc.PlanID, check bool) *atc.Plan {
		plan := &atc.Plan{ID: id}
		if check {
			plan.Check = &atc.CheckPlan{Type: "some-base-type", Name: "name", Source: atc.Source{"some": "source"}, TypeImage: atc.TypeImage{BaseType: "some-base-type"}}
		} else {
			plan.Get = &atc.GetPlan{Type: "some-base-type", Name: "name", Source: atc.Source{"some": "source"}, TypeImage: atc.TypeImage{BaseType: "some-base-type"}}
		}
		return plan
	}
	baseImage := func(prefix atc.PlanID) atc.TypeImage {
		return atc.TypeImage{
			BaseType:  "some-base-type",
			GetPlan:   imagePlan(prefix+"/image-get", false),
			CheckPlan: imagePlan(prefix+"/image-check", true),
		}
	}
	customImagePlan := func(id atc.PlanID, check bool) *atc.Plan {
		second := atc.TypeImage{
			BaseType: "some-base-type",
			GetPlan: &atc.Plan{ID: id + "/image-get", Get: &atc.GetPlan{
				Name: "second-custom-type", Type: "some-base-type", Source: atc.Source{"custom": "second-source"},
				TypeImage: atc.TypeImage{BaseType: "some-base-type"},
			}},
			CheckPlan: &atc.Plan{ID: id + "/image-check", Check: &atc.CheckPlan{
				Name: "second-custom-type", Type: "some-base-type", Source: atc.Source{"custom": "second-source"},
				TypeImage: atc.TypeImage{BaseType: "some-base-type"},
			}},
		}
		plan := &atc.Plan{ID: id}
		if check {
			plan.Check = &atc.CheckPlan{Name: "some-custom-type", Type: "second-custom-type", Source: atc.Source{"custom": "source"}, TypeImage: second}
		} else {
			plan.Get = &atc.GetPlan{Name: "some-custom-type", Type: "second-custom-type", Source: atc.Source{"custom": "source"}, TypeImage: second}
		}
		return plan
	}

	return atc.Plan{ID: "0", InParallel: &atc.InParallelPlan{Steps: []atc.Plan{
		{ID: "1", InParallel: &atc.InParallelPlan{Steps: []atc.Plan{task("2")}}},
		{ID: "3", Get: &atc.GetPlan{
			Type: "type", Name: "name", Resource: "resource", Source: atc.Source{"some": "source"},
			Params: atc.Params{"some": "params"}, Version: &atc.Version{"some": "version"}, Tags: atc.Tags{"tags"}, TypeImage: baseImage("3"),
		}},
		{ID: "3.1", Get: &atc.GetPlan{
			Name: "name", Type: "some-custom-type", Resource: "resource", Source: atc.Source{"some": "source"},
			Params: atc.Params{"some": "params"}, Version: &atc.Version{"some": "version"}, Tags: atc.Tags{"tags"},
			TypeImage: atc.TypeImage{BaseType: "some-base-type", GetPlan: customImagePlan("3.1/image-get", false), CheckPlan: customImagePlan("3.1/image-check", true)},
		}},
		{ID: "4", Put: &atc.PutPlan{
			Type: "type", Name: "name", Resource: "resource", Source: atc.Source{"some": "source"},
			Params: atc.Params{"some": "params"}, Tags: atc.Tags{"tags"}, TypeImage: baseImage("4"),
		}},
		{ID: "4.2", Check: &atc.CheckPlan{
			Type: "type", Name: "name", Source: atc.Source{"some": "source"}, Tags: atc.Tags{"tags"}, TypeImage: baseImage("4.2"),
		}},
		{ID: "5", Task: &atc.TaskPlan{
			Name: "name", Privileged: true, Hermetic: true, Tags: atc.Tags{"tags"}, ConfigPath: "some/config/path.yml",
			Config: &atc.TaskConfig{Params: atc.TaskEnv{"some": "secret"}},
		}},
		{ID: "6", Ensure: &atc.EnsurePlan{Step: task("7"), Next: task("8")}},
		{ID: "9", OnSuccess: &atc.OnSuccessPlan{Step: task("10"), Next: task("11")}},
		{ID: "12", OnFailure: &atc.OnFailurePlan{Step: task("13"), Next: task("14")}},
		{ID: "15", OnAbort: &atc.OnAbortPlan{Step: task("16"), Next: task("17")}},
		{ID: "18", Try: &atc.TryPlan{Step: task("19")}},
		{ID: "20", Timeout: &atc.TimeoutPlan{Step: task("21"), Duration: "lol"}},
		{ID: "22", Do: &atc.DoPlan{task("23")}},
		{ID: "24", Retry: &atc.RetryPlan{task("25"), task("26"), task("27")}},
		{ID: "28", OnAbort: &atc.OnAbortPlan{Step: task("29"), Next: task("30")}},
		{ID: "31", ArtifactInput: &atc.ArtifactInputPlan{ArtifactID: 17, Name: "some-name"}},
		{ID: "32", ArtifactOutput: &atc.ArtifactOutputPlan{Name: "some-name"}},
		{ID: "33", OnError: &atc.OnErrorPlan{Step: task("34"), Next: task("35")}},
		{ID: "36", InParallel: &atc.InParallelPlan{Limit: 1, FailFast: true, Steps: []atc.Plan{task("37")}}},
		{ID: "38", SetPipeline: &atc.SetPipelinePlan{
			Name: "some-pipeline", Team: "some-team", File: "some-file", VarFiles: []string{"vf"},
			Vars: map[string]any{"k1": "v1"}, InstanceVars: map[string]any{"branch": "feature/foo"},
		}},
		{ID: "39", Across: &atc.AcrossPlan{
			Vars: []atc.AcrossVar{
				{Var: "v1", Values: []any{"a"}, MaxInFlight: &atc.MaxInFlightConfig{Limit: 1}},
				{Var: "v2", Values: []any{"b"}, MaxInFlight: &atc.MaxInFlightConfig{All: true}},
			},
			SubStepTemplate: `{"id":"ACROSS_STEP_TEMPLATE"}`, FailFast: true,
		}},
		{ID: "42", LoadVar: &atc.LoadVarPlan{Name: "some-name", File: "some-file", Format: "some-format", Reveal: true}},
	}}}
}

func canonicalPublicPlan(plan atc.Plan) (string, error, error) {
	public := plan.Public()
	if public == nil {
		return "", nil, fmt.Errorf("public plan was nil")
	}
	var decoded map[string]any
	if err := json.Unmarshal(*public, &decoded); err != nil {
		return "", err, nil
	}
	sidecar := decoded["sidecar"].(map[string]any)
	image, hasImage := sidecar["image"]
	return fmt.Sprintf("id=%s;name=%s;image=%v;has-image=%t", decoded["id"], sidecar["name"], image, hasImage), nil, nil
}
