package workflow

import (
	"fmt"

	"github.com/concourse/concourse/agent/broker"
)

// CompileNodeDefinition compiles a schema-1 node source tree into an immutable
// one-leaf function while reusing the schema-3 asset and authority compiler.
func CompileNodeDefinition(m Manifest) (*CompiledNodeDefinition, error) {
	return compileNodeDefinition(m, nil)
}

// CompileNodeDefinitionWithBrokerCatalog resolves a node's neutral broker
// selectors at release time. The existing catalog-free API remains valid for
// nodes that do not expose broker tools.
func CompileNodeDefinitionWithBrokerCatalog(m Manifest, catalog *broker.Catalog) (*CompiledNodeDefinition, error) {
	if catalog == nil {
		return nil, fmt.Errorf("workflow: broker catalog is required")
	}
	return compileNodeDefinition(m, catalog)
}

func compileNodeDefinition(m Manifest, catalog *broker.Catalog) (*CompiledNodeDefinition, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	raw, found := m[NodeFileName]
	if !found {
		return nil, fmt.Errorf("workflow: manifest has no %s", NodeFileName)
	}
	definition, brokerSelectors, err := parseNodeDefinitionSource([]byte(raw))
	if err != nil {
		return nil, err
	}
	compilerDefinition := &CompiledDefinition{SchemaVersion: 3, Name: definition.Name, Function: &definition.Function}
	if err := compileFunctionAssets(m, compilerDefinition, nil, brokerSelectors, catalog); err != nil {
		if _, required := err.(BrokerCatalogRequiredError); required {
			return nil, BrokerCatalogRequiredError{DefinitionName: definition.Name}
		}
		return nil, err
	}
	if _, err := ValidateFunction(&definition.Function); err != nil {
		return nil, err
	}
	if err := definition.Validate(); err != nil {
		return nil, err
	}
	return definition, nil
}
