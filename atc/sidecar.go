package atc

import (
	"fmt"
	"strings"

	"sigs.k8s.io/yaml"
)

// SidecarConfig defines a sidecar container to run alongside a task.
// The format intentionally mirrors a subset of the Kubernetes container spec.
type SidecarConfig struct {
	Name       string            `json:"name"`
	Image      string            `json:"image"`
	Command    []string          `json:"command,omitempty"`
	Args       []string          `json:"args,omitempty"`
	Env        []SidecarEnvVar   `json:"env,omitempty"`
	Ports      []SidecarPort     `json:"ports,omitempty"`
	Resources  *SidecarResources `json:"resources,omitempty"`
	WorkingDir string            `json:"workingDir,omitempty"`
}

// SidecarEnvVar is a name/value pair for environment variables,
// matching the Kubernetes EnvVar structure.
type SidecarEnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// SidecarPort defines a port exposed by a sidecar container.
type SidecarPort struct {
	ContainerPort int    `json:"containerPort"`
	Protocol      string `json:"protocol,omitempty"`
}

// SidecarResources defines resource requests and limits for a sidecar,
// using Kubernetes quantity strings (e.g. "100m", "256Mi").
type SidecarResources struct {
	Requests SidecarResourceList `json:"requests,omitempty"`
	Limits   SidecarResourceList `json:"limits,omitempty"`
}

// SidecarResourceList holds CPU and memory quantity strings.
type SidecarResourceList struct {
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
}

// reservedContainerNames are names used internally by the jetbridge runtime
// and cannot be used as sidecar names.
var reservedContainerNames = map[string]bool{
	"main":            true,
	"artifact-helper": true,
}

// Validate checks that the sidecar config has all required fields.
func (sc SidecarConfig) Validate() error {
	var errors []string
	if sc.Name == "" {
		errors = append(errors, "missing 'name'")
	}
	if sc.Image == "" {
		errors = append(errors, "missing 'image'")
	}
	if len(errors) > 0 {
		return fmt.Errorf("invalid sidecar configuration: %s", strings.Join(errors, ", "))
	}
	return nil
}

// ParseSidecarConfigs parses a YAML list of sidecar container definitions.
// Each file contains a YAML list (one or more sidecars). All entries are
// validated, and duplicate or reserved names are rejected.
func ParseSidecarConfigs(data []byte) ([]SidecarConfig, error) {
	var configs []SidecarConfig
	if err := yaml.UnmarshalStrict(data, &configs); err != nil {
		return nil, fmt.Errorf("parsing sidecar config: %w", err)
	}

	seen := make(map[string]bool, len(configs))
	for _, sc := range configs {
		if err := sc.Validate(); err != nil {
			return nil, err
		}
		if reservedContainerNames[sc.Name] {
			return nil, fmt.Errorf("invalid sidecar configuration: reserved container name %q", sc.Name)
		}
		if seen[sc.Name] {
			return nil, fmt.Errorf("invalid sidecar configuration: duplicate sidecar name %q", sc.Name)
		}
		seen[sc.Name] = true
	}

	return configs, nil
}
