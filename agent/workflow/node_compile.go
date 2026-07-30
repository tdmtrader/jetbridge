package workflow

import "fmt"

// CompileNodeDefinition compiles a schema-1 node source tree into an immutable
// one-leaf function while reusing the schema-3 asset and authority compiler.
func CompileNodeDefinition(m Manifest) (*CompiledNodeDefinition, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	raw, found := m[NodeFileName]
	if !found {
		return nil, fmt.Errorf("workflow: manifest has no %s", NodeFileName)
	}
	definition, err := parseNodeDefinitionSource([]byte(raw))
	if err != nil {
		return nil, err
	}
	compilerDefinition := &CompiledDefinition{SchemaVersion: 3, Name: definition.Name, Function: &definition.Function}
	if err := compileFunctionAssets(m, compilerDefinition, nil, nil, nil); err != nil {
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
