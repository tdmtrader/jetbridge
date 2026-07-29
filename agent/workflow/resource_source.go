package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
)

// ResourceSource declares ordinary Concourse selection whose result becomes a
// typed snapshot input. The executable workflow never performs this lookup.
type ResourceSource struct {
	Name     string             `json:"name" yaml:"name"`
	Resource string             `json:"resource" yaml:"resource"`
	Type     snapshot.TypeRef   `json:"type" yaml:"type"`
	Trigger  bool               `json:"trigger,omitempty" yaml:"trigger,omitempty"`
	Version  *atc.VersionConfig `json:"version,omitempty" yaml:"version,omitempty"`
}

func (source *ResourceSource) UnmarshalJSON(data []byte) error {
	type wire ResourceSource
	var decoded wire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("workflow: unexpected trailing resource source value")
		}
		return fmt.Errorf("workflow: trailing resource source value: %w", err)
	}
	*source = ResourceSource(decoded)
	return nil
}

// ValidateResourceSources enforces an exact resource-to-source mapping. The
// normal Concourse validator owns the ordinary resource configuration rules.
func ValidateResourceSources(sources []ResourceSource, resources atc.ResourceConfigs, resourceTypes atc.ResourceTypes) error {
	names := map[string]struct{}{}
	selected := map[string]string{}
	for index, source := range sources {
		if strings.TrimSpace(source.Name) == "" {
			return fmt.Errorf("workflow: resource_sources[%d]: name is required", index)
		}
		if strings.TrimSpace(source.Name) != source.Name {
			return fmt.Errorf("workflow: resource_sources[%d]: name must be canonical", index)
		}
		if _, found := names[source.Name]; found {
			return fmt.Errorf("workflow: resource_sources: duplicate source name %q", source.Name)
		}
		names[source.Name] = struct{}{}
		if strings.TrimSpace(source.Resource) == "" {
			return fmt.Errorf("workflow: resource_sources[%d] %q: resource is required", index, source.Name)
		}
		if strings.TrimSpace(source.Resource) != source.Resource {
			return fmt.Errorf("workflow: resource_sources[%d] %q: resource must be canonical", index, source.Name)
		}
		if err := source.Type.Validate(); err != nil {
			return fmt.Errorf("workflow: resource_sources[%d] %q: %w", index, source.Name, err)
		}
		if _, found := resources.Lookup(source.Resource); !found {
			return fmt.Errorf("workflow: resource_sources[%d] %q: unknown resource %q", index, source.Name, source.Resource)
		}
		if previous, found := selected[source.Resource]; found {
			return fmt.Errorf("workflow: resource %q has more than one source (%q and %q)", source.Resource, previous, source.Name)
		}
		selected[source.Resource] = source.Name
	}
	for _, resource := range resources {
		if _, found := selected[resource.Name]; !found {
			return fmt.Errorf("workflow: resource %q has no resource source", resource.Name)
		}
	}
	_, err := ResourceSourceResourceTypes(sources, resources, resourceTypes)
	return err
}

// ResourceSourceResourceTypes returns only the custom type closure required
// by the standing source pipeline, in the compiled declaration order.
func ResourceSourceResourceTypes(sources []ResourceSource, resources atc.ResourceConfigs, resourceTypes atc.ResourceTypes) (atc.ResourceTypes, error) {
	reachable, visiting := map[string]struct{}{}, map[string]struct{}{}
	var visit func(string) error
	visit = func(name string) error {
		resourceType, custom := resourceTypes.Lookup(name)
		if !custom {
			return nil
		}
		if _, found := reachable[name]; found {
			return nil
		}
		if _, found := visiting[name]; found {
			return fmt.Errorf("workflow: resource type cycle reaches %q", name)
		}
		visiting[name] = struct{}{}
		if resourceType.Image == "" && resourceType.Type != "" {
			if err := visit(resourceType.Type); err != nil {
				return err
			}
		}
		delete(visiting, name)
		reachable[name] = struct{}{}
		return nil
	}
	for _, source := range sources {
		resource, found := resources.Lookup(source.Resource)
		if !found {
			return nil, fmt.Errorf("workflow: resource source %q references unknown resource %q", source.Name, source.Resource)
		}
		if err := visit(resource.Type); err != nil {
			return nil, err
		}
	}
	filtered := make(atc.ResourceTypes, 0, len(reachable))
	for _, resourceType := range resourceTypes {
		if _, found := reachable[resourceType.Name]; found {
			filtered = append(filtered, resourceType)
		}
	}
	return filtered, nil
}

func ResourceSourcePorts(sources []ResourceSource) []snapshot.Port {
	ports := make([]snapshot.Port, len(sources))
	for index, source := range sources {
		ports[index] = snapshot.Port{Name: source.Name, Type: source.Type}
	}
	return ports
}
