package steps

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
)

type ATCConfigObservation struct{ Value string }

func ATCConfigBehaviorDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, ATCConfigObservation](
			"the production ATC config evaluates profile {string}",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (ATCConfigObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return ATCConfigObservation{}, fmt.Errorf("expected ATC config profile")
				}
				value, err := observeATCConfig(profile)
				return ATCConfigObservation{Value: value}, err
			},
		),
		CheckString[ATCConfigObservation]("the ATC config observation is {string}", "ATC config observation",
			func(in ATCConfigObservation) (string, error) { return in.Value, nil }),
		CheckContains[ATCConfigObservation]("the ATC config observation contains {string}", "ATC config observation",
			func(in ATCConfigObservation) (string, error) { return in.Value, nil }),
	}
}

func observeATCConfig(profile string) (string, error) {
	switch profile {
	case "type-image/direct":
		image := atc.ResourceTypes{{Name: "custom-git", Image: "my-registry/custom-git:latest", Privileged: true}}.ImageForType("some-plan", "custom-git", nil, false)
		return fmt.Sprintf("ref=%s;base=%s;privileged=%t;get=%t;check=%t", image.ImageRef, image.BaseType, image.Privileged, image.GetPlan != nil, image.CheckPlan != nil), nil
	case "type-image/source-plans":
		image := registryImageType().ImageForType("some-plan", "custom-git", nil, false)
		return fmt.Sprintf("ref=%s;get=%t;check=%t", image.ImageRef, image.GetPlan != nil, image.CheckPlan != nil), nil
	case "type-image/registry-skip-download":
		image := registryImageType().ImageForType("some-plan", "custom-git", nil, false)
		return fmt.Sprintf("skip-download=%t", image.GetPlan.Get.SkipDownload), nil
	case "type-image/non-registry-download":
		image := atc.ResourceTypes{{Name: "custom-s3", Type: "s3", Source: atc.Source{"bucket": "my-bucket"}}}.ImageForType("some-plan", "custom-s3", nil, false)
		return fmt.Sprintf("skip-download=%t", image.GetPlan.Get.SkipDownload), nil
	case "type-image/resolved-digest":
		image := atc.ResourceTypes{{Name: "custom-git", Type: "registry-image", Source: atc.Source{"repository": "my-registry/custom-git"}, Image: "my-registry/custom-git@sha256:abc123"}}.ImageForType("some-plan", "custom-git", nil, false)
		return fmt.Sprintf("ref=%s;base=%s;get=%t;check=%t", image.ImageRef, image.BaseType, image.GetPlan != nil, image.CheckPlan != nil), nil
	case "type-image/base":
		image := atc.ResourceTypes{}.ImageForType("some-plan", "git", nil, false)
		return fmt.Sprintf("ref=%s;base=%s;get=%t;check=%t", image.ImageRef, image.BaseType, image.GetPlan != nil, image.CheckPlan != nil), nil
	case "version/string-values":
		var config atc.VersionConfig
		if err := json.Unmarshal([]byte(`{"some":"version","other":"8"}`), &config); err != nil {
			return "error:" + err.Error(), nil
		}
		return "some=" + config.Pinned["some"] + ";other=" + config.Pinned["other"], nil
	case "version/non-string":
		var config atc.VersionConfig
		err := json.Unmarshal([]byte(`{"some":8}`), &config)
		return configError(err), nil
	case "var-sources/ideal":
		return observeVarSourceOrder([]string{"vs1", "vs2", "vs3", "vs4", "vs5"})
	case "var-sources/random":
		return observeVarSourceOrder([]string{"vs4", "vs2", "vs5", "vs1", "vs3"})
	case "var-sources/unresolved":
		return observeVarSourceOrder([]string{"vs4", "vs2", "vs5", "vs3"})
	case "var-sources/cyclic":
		return observeVarSourceOrder([]string{"vs1-cyclic", "vs4", "vs2", "vs5", "vs3"})
	case "check-every/unmarshal-never":
		return observeCheckEveryJSON(`{"check_every":"never"}`)
	case "check-every/unmarshal-duration":
		return observeCheckEveryJSON(`{"check_every":"10s"}`)
	case "check-every/unmarshal-invalid-duration":
		return observeCheckEveryJSON(`{"check_every":"some-string"}`)
	case "check-every/unmarshal-non-string":
		return observeCheckEveryJSON(`{"check_every":[1,2,3]}`)
	case "check-every/marshal-never":
		checkEvery := atc.CheckEvery{Never: true}
		result, err := checkEvery.MarshalJSON()
		return configMarshalObservation(result, err), nil
	case "check-every/marshal-duration":
		checkEvery := atc.CheckEvery{Interval: 10 * time.Second}
		result, err := checkEvery.MarshalJSON()
		return configMarshalObservation(result, err), nil
	case "check-every/marshal-empty":
		checkEvery := atc.CheckEvery{}
		result, err := checkEvery.MarshalJSON()
		return configMarshalObservation(result, err), nil
	default:
		return "", fmt.Errorf("unknown ATC config profile %q", profile)
	}
}

func registryImageType() atc.ResourceTypes {
	return atc.ResourceTypes{{Name: "custom-git", Type: "registry-image", Source: atc.Source{"repository": "my-registry/custom-git"}}}
}

func observeVarSourceOrder(names []string) (string, error) {
	base := map[string]atc.VarSourceConfig{
		"vs1":        {Name: "vs1", Type: "dummy", Config: map[string]any{"vars": map[string]any{"pk": "pv"}}},
		"vs1-cyclic": {Name: "vs1", Type: "dummy", Config: map[string]any{"vars": map[string]any{"pk": "((vs5:pk))"}}},
		"vs2":        {Name: "vs2", Type: "dummy", Config: map[string]any{"vars": map[string]any{"pk": "pv"}}},
		"vs3":        {Name: "vs3", Type: "dummy", Config: map[string]any{"vars": map[string]any{"pk": "((vs1:pk))"}}},
		"vs4":        {Name: "vs4", Type: "dummy", Config: map[string]any{"vars": map[string]any{"pk": "((vs2:pk))"}}},
		"vs5":        {Name: "vs5", Type: "dummy", Config: map[string]any{"vars": map[string]any{"pk": "((vs3:pk))", "pk2": "((vs4:pk))"}}},
	}
	configs := make(atc.VarSourceConfigs, 0, len(names))
	for _, name := range names {
		configs = append(configs, base[name])
	}
	ordered, err := configs.OrderByDependency()
	if err != nil {
		return "error:" + err.Error(), nil
	}
	orderedNames := make([]string, len(ordered))
	for i := range ordered {
		orderedNames[i] = ordered[i].Name
	}
	return strings.Join(orderedNames, ","), nil
}

func observeCheckEveryJSON(input string) (string, error) {
	var config atc.ResourceConfig
	if err := json.Unmarshal([]byte(input), &config); err != nil {
		return configError(err), nil
	}
	return fmt.Sprintf("never=%t;interval=%s", config.CheckEvery.Never, config.CheckEvery.Interval), nil
}

func configError(err error) string {
	if err == nil {
		return "no-error"
	}
	return "error:" + err.Error()
}

func configMarshalObservation(result []byte, err error) string {
	if err != nil {
		return configError(err)
	}
	return strings.Trim(string(result), `"`)
}
