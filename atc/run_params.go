package atc

import (
	"fmt"
	"strconv"
)

// ValidateRunParams validates params given for a pipeline run against a
// template's params schema (shared-contracts §7): unknown names rejected,
// missing required params rejected, defaults filled server-side, values
// coerced per JSON type. The returned map is stored on the run row and
// interpolated into the instanced pipeline.
func ValidateRunParams(schema []ParamSchema, given map[string]any) (map[string]any, error) {
	byName := make(map[string]ParamSchema, len(schema))
	for _, p := range schema {
		byName[p.Name] = p
	}

	for name := range given {
		if _, ok := byName[name]; !ok {
			return nil, fmt.Errorf("unknown param %q", name)
		}
	}

	validated := make(map[string]any, len(schema))
	for _, p := range schema {
		raw, ok := given[p.Name]
		if !ok {
			if p.Default != nil {
				coerced, err := coerceParam(p, p.Default)
				if err != nil {
					return nil, fmt.Errorf("invalid default: %w", err)
				}
				validated[p.Name] = coerced
				continue
			}
			if p.Required {
				return nil, fmt.Errorf("missing required param %q", p.Name)
			}
			continue
		}

		coerced, err := coerceParam(p, raw)
		if err != nil {
			return nil, err
		}
		validated[p.Name] = coerced
	}

	return validated, nil
}

func coerceParam(p ParamSchema, raw any) (any, error) {
	switch p.Type {
	case "string":
		s, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("param %q: expected string, got %T", p.Name, raw)
		}
		return s, nil

	case "number":
		switch v := raw.(type) {
		case int:
			return float64(v), nil
		case int64:
			return float64(v), nil
		case float64:
			return v, nil
		case string:
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return nil, fmt.Errorf("param %q: %q is not a number", p.Name, v)
			}
			return f, nil
		}
		return nil, fmt.Errorf("param %q: expected number, got %T", p.Name, raw)

	case "bool":
		switch v := raw.(type) {
		case bool:
			return v, nil
		case string:
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, fmt.Errorf("param %q: %q is not a bool", p.Name, v)
			}
			return b, nil
		}
		return nil, fmt.Errorf("param %q: expected bool, got %T", p.Name, raw)

	case "enum":
		s, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("param %q: expected one of %v, got %T", p.Name, p.Values, raw)
		}
		for _, allowed := range p.Values {
			if s == allowed {
				return s, nil
			}
		}
		return nil, fmt.Errorf("param %q: %q is not one of %v", p.Name, s, p.Values)
	}

	return nil, fmt.Errorf("param %q: unknown type %q", p.Name, p.Type)
}
