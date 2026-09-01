package steps

import (
	"encoding/json"
	"fmt"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
)

type ContainerLimitsStrictObservation struct{ Value string }

func ContainerLimitsStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, ContainerLimitsStrictObservation](
			"the production container limits evaluate profile {string}",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (ContainerLimitsStrictObservation, error) {
				profile, ok := p.GetString(0)
				if !ok {
					return ContainerLimitsStrictObservation{}, fmt.Errorf("expected container limits profile")
				}
				value, err := observeContainerLimitsStrict(profile)
				return ContainerLimitsStrictObservation{Value: value}, err
			},
		),
		CheckString[ContainerLimitsStrictObservation](
			"the strict container limits observation is {string}",
			"strict container limits observation",
			func(in ContainerLimitsStrictObservation) (string, error) { return in.Value, nil },
		),
	}
}

func observeContainerLimitsStrict(profile string) (string, error) {
	switch profile {
	case "memory/plain":
		return observeMemoryLimitStrict("1024"), nil
	case "memory/kb":
		return observeMemoryLimitStrict("1KB"), nil
	case "memory/mb":
		return observeMemoryLimitStrict("1MB"), nil
	case "memory/gb":
		return observeMemoryLimitStrict("1GB"), nil
	case "memory/kib":
		return observeMemoryLimitStrict("1KiB"), nil
	case "memory/mib":
		return observeMemoryLimitStrict("1MiB"), nil
	case "memory/gib":
		return observeMemoryLimitStrict("1GiB"), nil
	case "memory/case-unit":
		return compareMemoryLimitsStrict("1kb", "1KB"), nil
	case "memory/case-prefix":
		return compareMemoryLimitsStrict("1kib", "1KiB"), nil
	case "memory/invalid":
		return observeMemoryLimitStrict("invalid"), nil
	case "memory/single-prefixes":
		return fmt.Sprintf("K=%s;M=%s;G=%s", observeMemoryLimitStrict("1K"), observeMemoryLimitStrict("1m"), observeMemoryLimitStrict("1G")), nil
	case "ephemeral/numeric":
		return observeEphemeralLimitStrict(`{"ephemeral_storage":1073741824}`), nil
	case "ephemeral/5g":
		return observeEphemeralLimitStrict(`{"ephemeral_storage":"5G"}`), nil
	case "ephemeral/2gib":
		return observeEphemeralLimitStrict(`{"ephemeral_storage":"2GiB"}`), nil
	case "ephemeral/omit-nil":
		data, err := json.Marshal(atc.ContainerLimits{})
		if err != nil {
			return "error:" + err.Error(), nil
		}
		return string(data), nil
	default:
		return "", fmt.Errorf("unknown container limits profile %q", profile)
	}
}

func observeMemoryLimitStrict(input string) string {
	limit, err := atc.ParseMemoryLimit(input)
	if err != nil {
		return "error:" + err.Error()
	}
	return fmt.Sprintf("value=%d", limit)
}

func compareMemoryLimitsStrict(lower, upper string) string {
	lowerLimit, lowerErr := atc.ParseMemoryLimit(lower)
	upperLimit, upperErr := atc.ParseMemoryLimit(upper)
	return fmt.Sprintf("equal=%t;lower=%s;upper=%s", lowerErr == nil && upperErr == nil && lowerLimit == upperLimit, errorTextStrict(lowerErr), errorTextStrict(upperErr))
}

func observeEphemeralLimitStrict(input string) string {
	var limits atc.ContainerLimits
	if err := json.Unmarshal([]byte(input), &limits); err != nil {
		return "error:" + err.Error()
	}
	if limits.EphemeralStorage == nil {
		return "nil"
	}
	return fmt.Sprintf("value=%d", *limits.EphemeralStorage)
}

func errorTextStrict(err error) string {
	if err == nil {
		return "none"
	}
	return err.Error()
}
