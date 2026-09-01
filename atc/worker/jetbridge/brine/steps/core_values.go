package steps

import (
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
		return "", (atc.Worker{Version: "a.b.c"}).Validate(), nil
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
		plan := atc.Plan{ID: "0", Task: &atc.TaskPlan{Name: "task", ConfigPath: "ci/task.yml", Config: &atc.TaskConfig{
			Params: atc.TaskEnv{"password": "secret"}, Run: atc.TaskRunConfig{Path: "true"},
		}}}
		public := plan.Public()
		if public == nil {
			return "", nil, fmt.Errorf("public plan was nil")
		}
		raw := string(*public)
		if strings.Contains(raw, "secret") || strings.Contains(raw, "config_path") || !strings.Contains(raw, `"name":"task"`) {
			return "", nil, fmt.Errorf("plan was not safely sanitized: %s", raw)
		}
		return "sanitized=true", nil, nil
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
		return fmt.Sprintf("%t", build.IsRunning()), nil, nil
	}
	if parts[0] == "abortable" {
		return fmt.Sprintf("%t", build.Abortable()), nil, nil
	}
	return "", nil, fmt.Errorf("unknown build predicate %q", parts[0])
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
