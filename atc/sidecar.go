package atc

import (
	"encoding/json"
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

// SidecarSource is a union type: either a file path (string) referencing a
// sidecar definition in a build artifact, or an inline SidecarConfig object.
// In JSON/YAML, a string value becomes a file reference and an object value
// becomes an inline config.
type SidecarSource struct {
	File   string         `json:"-"`
	Config *SidecarConfig `json:"-"`
}

func (s SidecarSource) MarshalJSON() ([]byte, error) {
	if s.File != "" {
		return json.Marshal(s.File)
	}
	if s.Config != nil {
		return json.Marshal(*s.Config)
	}
	return json.Marshal(nil)
}

func (s *SidecarSource) UnmarshalJSON(data []byte) error {
	// Try string first (file reference)
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		s.File = str
		return nil
	}

	// Try object (inline config)
	var cfg SidecarConfig
	if err := json.Unmarshal(data, &cfg); err == nil {
		s.Config = &cfg
		return nil
	}

	return fmt.Errorf("sidecar entry must be a string (file path) or object (inline config)")
}

// reservedContainerNames are names used internally by the jetbridge runtime
// and cannot be used as sidecar names.
var reservedContainerNames = map[string]bool{
	"main":            true,
	"artifact-helper": true,
}

// IsReservedContainerName returns true if the given name is reserved by the
// runtime and cannot be used for sidecar containers.
func IsReservedContainerName(name string) bool {
	return reservedContainerNames[name]
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
