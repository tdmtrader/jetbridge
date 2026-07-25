package exec

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func recordAuthorityEnv(
	inputs snapshotInputBindings,
	outputs map[string]snapshot.TypeRef,
	existing []string,
) ([]string, error) {
	existingNames := make(map[string]struct{}, len(existing))
	for _, row := range existing {
		name, _, found := strings.Cut(row, "=")
		if found {
			existingNames[name] = struct{}{}
		}
	}

	values := make(map[string]string, len(inputs.refs)*2+len(outputs)*2)
	add := func(name, value string) error {
		if _, found := existingNames[name]; found {
			return fmt.Errorf("record authority environment %s collides with an authored environment variable", name)
		}
		if _, found := values[name]; found {
			return fmt.Errorf("record authority environment names normalize to duplicate %s", name)
		}
		values[name] = value
		return nil
	}
	for name, ref := range inputs.refs {
		prefix := "AGENT_INPUT_" + authorityEnvPort(name)
		if err := add(prefix+"_SNAPSHOT_TYPE", ref.Type.String()); err != nil {
			return nil, err
		}
		if err := add(prefix+"_SNAPSHOT_DIGEST", ref.Digest.String()); err != nil {
			return nil, err
		}
	}
	for name, typeRef := range outputs {
		schema, recordType := recordSchemaIdentity(typeRef)
		if !recordType {
			continue
		}
		prefix := "AGENT_OUTPUT_" + authorityEnvPort(name)
		if err := add(prefix+"_RECORD_TYPE", typeRef.String()); err != nil {
			return nil, err
		}
		if err := add(prefix+"_RECORD_SCHEMA", schema); err != nil {
			return nil, err
		}
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	rows := make([]string, len(names))
	for index, name := range names {
		rows[index] = name + "=" + values[name]
	}
	return rows, nil
}

func recordSchemaIdentity(typeRef snapshot.TypeRef) (string, bool) {
	digest, found := contracts.SchemaDigestFor(typeRef)
	return digest.String(), found
}

func authorityEnvPort(port string) string {
	return strings.Map(func(value rune) rune {
		if unicode.IsLetter(value) || unicode.IsDigit(value) {
			return unicode.ToUpper(value)
		}
		return '_'
	}, port)
}
