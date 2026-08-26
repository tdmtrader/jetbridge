package atc

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/concourse/concourse/vars"
)

// templatePlaceholderPattern is the same lexical grammar Concourse variable
// interpolation uses (the unexported interpolationRegex, vars/template.go:106),
// duplicated here so this check reads exactly what run materialization will
// later rewrite.
var templatePlaceholderPattern = regexp.MustCompile(`\(\((([-/\.\w\pL]+\:)?[-/\.:@"\w\pL]+)\)\)`)

// declaredTemplateParameter returns the first reference in text that names a
// declared template parameter or a reserved run value.
//
// A qualified, dotted, or undeclared reference such as ((vault:token)) is an
// ordinary Concourse variable: MaterializeRunConfig does not substitute it, so
// it is left entirely alone.
func declaredTemplateParameter(text string, declared map[string]struct{}) (string, bool) {
	for _, match := range templatePlaceholderPattern.FindAllStringSubmatch(text, -1) {
		reference, err := vars.ParseReference(match[1])
		if err != nil {
			continue
		}
		if reference.Source != "" || len(reference.Fields) > 0 {
			continue
		}
		if _, found := declared[reference.Path]; found {
			return reference.Path, true
		}
	}

	return "", false
}

// ValidateTemplatePlaceholders refuses a declared template parameter at the two
// kinds of location where run materialization does something other than
// substitute a value:
//
//   - A map key anywhere in the config. Interpolation rewrites keys as well as
//     values -- vars/template.go:124 deletes the old key and reinserts under the
//     evaluated one -- so ((param)): value renames the field it keys instead of
//     setting one, and two runs can silently collapse onto the same key.
//
//   - The template's own parameter declarations. Schemas are read before
//     substitution runs: ValidateRunParams is handed the un-interpolated
//     effective config (atc/db/pipeline_run_factory.go:100), so a placeholder in
//     a parameter name, type, default, enum value, or description is taken
//     literally and can never resolve.
//
// Interpolated job, resource and task-cache identities are deliberately still
// permitted: run_policy_key, TaskCacheIdentity.RunJobName and ChronoRunBuilds
// exist precisely to carry them.
//
// Adapted from the ANVIL branch's location-class table
// (.worktrees/anvil-pipeline-templates/atc/pipeline_template_schema.go:745-1032)
// and its refusal switch (atc/pipeline_template_placeholders.go:373-419),
// reduced to the two classes that refuse without contradicting this branch's
// design, and without ANVIL's reflective wire-schema registry -- this branch
// validates a typed atc.Config, so anything that is not a string has already
// failed to unmarshal long before validation runs.
func ValidateTemplatePlaceholders(config Config) error {
	declared := map[string]struct{}{"run": {}, "run_id": {}}
	for _, schema := range config.Params {
		declared[schema.Name] = struct{}{}
	}

	return refuseTemplateParameters(reflect.ValueOf(config), "", false, declared)
}

func refuseTemplateParameters(value reflect.Value, path string, declaration bool, declared map[string]struct{}) error {
	switch value.Kind() {
	case reflect.Pointer, reflect.Interface:
		if value.IsNil() {
			return nil
		}
		return refuseTemplateParameters(value.Elem(), path, declaration, declared)

	case reflect.Struct:
		structType := value.Type()
		if structType == reflect.TypeOf(ParamSchema{}) {
			declaration = true
		}
		for index := 0; index < structType.NumField(); index++ {
			field := structType.Field(index)
			if field.PkgPath != "" {
				continue
			}
			fieldPath := joinTemplatePath(path, templateWireFieldName(field))
			if err := refuseTemplateParameters(value.Field(index), fieldPath, declaration, declared); err != nil {
				return err
			}
		}

	case reflect.Slice, reflect.Array:
		if value.Type().Elem().Kind() == reflect.Uint8 {
			// A byte slice -- Step.UnknownFields holds *json.RawMessage -- is
			// opaque payload, not config, and unknown step fields are rejected
			// by (*Step).UnmarshalJSON before validation runs.
			return nil
		}
		for index := 0; index < value.Len(); index++ {
			elementPath := fmt.Sprintf("%s[%d]", path, index)
			if err := refuseTemplateParameters(value.Index(index), elementPath, declaration, declared); err != nil {
				return err
			}
		}

	case reflect.Map:
		iterator := value.MapRange()
		for iterator.Next() {
			key := iterator.Key()
			keyText := ""
			if key.Kind() == reflect.String {
				keyText = key.String()
			}
			keyPath := joinTemplatePath(path, keyText)
			if name, found := declaredTemplateParameter(keyText, declared); found {
				return fmt.Errorf("%s: template parameter ((%s)) is not allowed in a map key", keyPath, name)
			}
			if err := refuseTemplateParameters(iterator.Value(), keyPath, declaration, declared); err != nil {
				return err
			}
		}

	case reflect.String:
		if !declaration {
			return nil
		}
		if name, found := declaredTemplateParameter(value.String(), declared); found {
			return fmt.Errorf("%s: template parameter ((%s)) is not allowed in a parameter declaration", path, name)
		}
	}

	return nil
}

func templateWireFieldName(field reflect.StructField) string {
	wire := strings.Split(field.Tag.Get("json"), ",")[0]
	if wire == "" || wire == "-" {
		return field.Name
	}
	return wire
}

func joinTemplatePath(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}
