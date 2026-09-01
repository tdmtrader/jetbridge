package steps

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
)

type SidecarObservation struct {
	Value string
	Err   error
}

func SidecarBehaviorDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, SidecarObservation](
			"the production sidecar model handles profile {string}",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (SidecarObservation, error) {
				profile, _ := p.GetString(0)
				value, observedErr, setupErr := observeSidecar(profile)
				return SidecarObservation{Value: value, Err: observedErr}, setupErr
			},
		),
		CheckString[SidecarObservation]("the sidecar result is {string}", "sidecar result", func(in SidecarObservation) (string, error) {
			return in.Value, nil
		}),
		brine.DefineCheck[SidecarObservation]("the sidecar operation returned no error", func(in SidecarObservation, _ brine.Params, _ *brine.Recorder) error {
			return in.Err
		}),
		brine.DefineCheck[SidecarObservation]("the sidecar error contains {string}", func(in SidecarObservation, p brine.Params, _ *brine.Recorder) error {
			want, _ := p.GetString(0)
			if in.Err == nil {
				return fmt.Errorf("sidecar operation returned no error")
			}
			if !strings.Contains(in.Err.Error(), want) {
				return fmt.Errorf("expected error containing %q, got %q", want, in.Err)
			}
			return nil
		}),
	}
}

func observeSidecar(profile string) (string, error, error) {
	switch profile {
	case "parse-full":
		configs, err := atc.ParseSidecarConfigs([]byte(`
- name: postgres
  image: postgres:15
  command: ["docker-entrypoint.sh"]
  args: ["postgres"]
  workingDir: /var/lib/postgresql
  env:
  - name: POSTGRES_PASSWORD
    value: test
  - name: POSTGRES_DB
    value: myapp_test
  ports:
  - containerPort: 5432
    protocol: TCP
  resources:
    requests: {cpu: 100m, memory: 256Mi}
    limits: {cpu: 500m, memory: 512Mi}
`))
		if err != nil {
			return "", err, nil
		}
		expected := atc.SidecarConfig{
			Name: "postgres", Image: "postgres:15", Command: []string{"docker-entrypoint.sh"},
			Args: []string{"postgres"}, WorkingDir: "/var/lib/postgresql",
			Env: []atc.SidecarEnvVar{
				{Name: "POSTGRES_PASSWORD", Value: "test"},
				{Name: "POSTGRES_DB", Value: "myapp_test"},
			},
			Ports: []atc.SidecarPort{{ContainerPort: 5432, Protocol: "TCP"}},
			Resources: &atc.SidecarResources{
				Requests: atc.SidecarResourceList{CPU: "100m", Memory: "256Mi"},
				Limits:   atc.SidecarResourceList{CPU: "500m", Memory: "512Mi"},
			},
		}
		if len(configs) != 1 || !reflect.DeepEqual(configs[0], expected) {
			return "", nil, fmt.Errorf("full sidecar mismatch: %#v", configs)
		}
		return "full", nil, nil
	case "parse-multiple":
		configs, err := atc.ParseSidecarConfigs([]byte("- name: postgres\n  image: postgres:15\n  env:\n  - name: POSTGRES_PASSWORD\n    value: test\n  ports:\n  - containerPort: 5432\n- name: redis\n  image: redis:7\n  ports:\n  - containerPort: 6379\n"))
		if err != nil {
			return "", err, nil
		}
		if len(configs) != 2 {
			return "", nil, fmt.Errorf("expected two sidecars, got %d", len(configs))
		}
		return configs[0].Name + ":" + configs[0].Image + "," + configs[1].Name + ":" + configs[1].Image, nil, nil
	case "parse-minimal":
		configs, err := atc.ParseSidecarConfigs([]byte("- name: redis\n  image: redis:7\n"))
		if err != nil {
			return "", err, nil
		}
		if len(configs) != 1 {
			return "", nil, fmt.Errorf("expected one sidecar, got %d", len(configs))
		}
		minimal := len(configs) == 1 && configs[0].Command == nil && configs[0].Env == nil && configs[0].Ports == nil && configs[0].Resources == nil
		return fmt.Sprintf("%s:%s:minimal=%t", configs[0].Name, configs[0].Image, minimal), nil, nil
	case "parse-empty":
		configs, err := atc.ParseSidecarConfigs([]byte("[]"))
		return fmt.Sprintf("count=%d", len(configs)), err, nil
	case "json-round-trip":
		original := atc.SidecarConfig{
			Name: "postgres", Image: "postgres:15", Command: []string{"docker-entrypoint.sh"}, Args: []string{"postgres"},
			Env:   []atc.SidecarEnvVar{{Name: "POSTGRES_PASSWORD", Value: "test"}},
			Ports: []atc.SidecarPort{{ContainerPort: 5432, Protocol: "TCP"}},
			Resources: &atc.SidecarResources{
				Requests: atc.SidecarResourceList{CPU: "100m", Memory: "256Mi"},
				Limits:   atc.SidecarResourceList{CPU: "500m", Memory: "512Mi"},
			},
		}
		payload, err := json.Marshal(original)
		if err != nil {
			return "", err, nil
		}
		var restored atc.SidecarConfig
		err = json.Unmarshal(payload, &restored)
		return fmt.Sprintf("equal=%t", reflect.DeepEqual(original, restored)), err, nil
	case "source-invalid":
		var source atc.SidecarSource
		err := json.Unmarshal([]byte(`123`), &source)
		return "", err, nil
	case "marshal-object":
		payload, err := json.Marshal(atc.SidecarSource{Config: &atc.SidecarConfig{Name: "redis", Image: "redis:7"}})
		if err != nil {
			return "", err, nil
		}
		var restored atc.SidecarSource
		err = json.Unmarshal(payload, &restored)
		return restored.Config.Name + ":" + restored.Config.Image, err, nil
	case "validate-valid":
		return "valid", atc.SidecarConfig{Name: "db", Image: "postgres:15"}.Validate(), nil
	case "validate-protocols":
		for _, protocol := range []string{"", "TCP", "UDP", "SCTP"} {
			if err := (atc.SidecarConfig{Name: "db", Image: "postgres:15", Ports: []atc.SidecarPort{{ContainerPort: 5432, Protocol: protocol}}}).Validate(); err != nil {
				return "", err, nil
			}
		}
		return "valid", nil, nil
	case "validate-empty-name":
		return "", atc.SidecarConfig{Image: "postgres:15"}.Validate(), nil
	case "validate-empty-image":
		return "", atc.SidecarConfig{Name: "db"}.Validate(), nil
	case "validate-invalid-protocol":
		return "", atc.SidecarConfig{Name: "db", Image: "postgres:15", Ports: []atc.SidecarPort{{ContainerPort: 5432, Protocol: "HTTP"}}}.Validate(), nil
	default:
		fixtures := map[string]string{
			"missing-name":    "- image: postgres:15\n",
			"missing-image":   "- name: postgres\n",
			"empty-name":      "- name: \"\"\n  image: postgres:15\n",
			"duplicate":       "- name: db\n  image: postgres:15\n- name: db\n  image: mysql:8\n",
			"reserved-main":   "- name: main\n  image: postgres:15\n",
			"reserved-helper": "- name: artifact-helper\n  image: postgres:15\n",
			"unknown-field":   "- name: postgres\n  image: postgres:15\n  bogusField: true\n",
			"invalid-yaml":    "not: valid: yaml: [",
		}
		raw, ok := fixtures[profile]
		if !ok {
			return "", nil, fmt.Errorf("unknown sidecar profile %q", profile)
		}
		_, err := atc.ParseSidecarConfigs([]byte(raw))
		return "", err, nil
	}
}
