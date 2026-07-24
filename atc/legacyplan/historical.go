package legacyplan

import (
	"encoding/json"
	"errors"
	"fmt"
)

var ErrActiveHarvestPlan = errors.New("legacy plan: harvest is retired and cannot execute")

func ContainsHarvest(raw []byte) (bool, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, fmt.Errorf("decode legacy plan: %w", err)
	}

	return containsHarvest(value)
}

func DecodeCompletedPublic(raw *json.RawMessage) (*json.RawMessage, error) {
	if raw == nil {
		return nil, nil
	}

	var value any
	if err := json.Unmarshal(*raw, &value); err != nil {
		return nil, fmt.Errorf("decode historical public plan: %w", err)
	}

	rewritten, _, err := rewrite(value)
	if err != nil {
		return nil, err
	}

	payload, err := json.Marshal(rewritten)
	if err != nil {
		return nil, fmt.Errorf("encode historical public plan: %w", err)
	}

	result := json.RawMessage(payload)
	return &result, nil
}

func containsHarvest(value any) (bool, error) {
	node, ok := value.(map[string]any)
	if !ok {
		return false, nil
	}

	_, hasStringID := node["id"].(string)
	_, hasHarvestObject := node["harvest"].(map[string]any)
	if hasStringID && hasHarvestObject {
		return true, nil
	}

	for _, child := range planChildren(node) {
		found, err := containsHarvest(child.value())
		if err != nil || found {
			return found, err
		}
	}

	template, found, err := decodeAcrossTemplate(node)
	if err != nil {
		return false, err
	}
	if found {
		return containsHarvest(template)
	}

	return false, nil
}

func rewrite(value any) (any, bool, error) {
	node, ok := value.(map[string]any)
	if !ok {
		return value, false, nil
	}

	changed := false
	_, hasStringID := node["id"].(string)
	harvest, hasHarvest := node["harvest"]
	if hasStringID && hasHarvest {
		harvestObject, ok := harvest.(map[string]any)
		if !ok {
			return nil, false, errors.New("decode historical public plan: harvest node must be an object")
		}

		name := ""
		if rawName, found := harvestObject["name"]; found {
			var ok bool
			name, ok = rawName.(string)
			if !ok {
				return nil, false, errors.New("decode historical public plan: harvest name must be a string")
			}
		}

		delete(node, "harvest")
		node["retired_step"] = map[string]any{
			"kind": "harvest",
			"name": name,
		}
		changed = true
	}

	for _, child := range planChildren(node) {
		rewritten, childChanged, err := rewrite(child.value())
		if err != nil {
			return nil, false, err
		}
		if childChanged {
			child.set(rewritten)
			changed = true
		}
	}

	template, found, err := decodeAcrossTemplate(node)
	if err != nil {
		return nil, false, err
	}
	if found {
		rewritten, templateChanged, err := rewrite(template)
		if err != nil {
			return nil, false, err
		}
		if templateChanged {
			payload, err := json.Marshal(rewritten)
			if err != nil {
				return nil, false, fmt.Errorf("encode across substep template: %w", err)
			}
			node["across"].(map[string]any)["substep_template"] = string(payload)
			changed = true
		}
	}

	return node, changed, nil
}

type planChild struct {
	object map[string]any
	key    string
	array  []any
	index  int
}

func (child planChild) value() any {
	if child.object != nil {
		return child.object[child.key]
	}
	return child.array[child.index]
}

func (child planChild) set(value any) {
	if child.object != nil {
		child.object[child.key] = value
		return
	}
	child.array[child.index] = value
}

func planChildren(node map[string]any) []planChild {
	var children []planChild

	appendObject := func(object map[string]any, key string) {
		if _, found := object[key]; found {
			children = append(children, planChild{object: object, key: key})
		}
	}
	appendArray := func(object map[string]any, key string) {
		values, ok := object[key].([]any)
		if !ok {
			return
		}
		for index := range values {
			children = append(children, planChild{array: values, index: index})
		}
	}

	appendArray(node, "do")
	appendArray(node, "retry")

	if inParallel, ok := node["in_parallel"].(map[string]any); ok {
		appendArray(inParallel, "steps")
	}

	for wrapper, childKeys := range map[string][]string{
		"on_success": {"step", "on_success"},
		"on_failure": {"step", "on_failure"},
		"on_abort":   {"step", "on_abort"},
		"on_error":   {"step", "on_error"},
		"ensure":     {"step", "ensure"},
		"try":        {"step"},
		"timeout":    {"step"},
	} {
		if payload, ok := node[wrapper].(map[string]any); ok {
			for _, childKey := range childKeys {
				appendObject(payload, childKey)
			}
		}
	}

	for _, resourceStep := range []string{"get", "put", "check"} {
		payload, ok := node[resourceStep].(map[string]any)
		if !ok {
			continue
		}

		appendObject(payload, "image_get_plan")
		appendObject(payload, "image_check_plan")
		if image, ok := payload["image"].(map[string]any); ok {
			appendObject(image, "get_plan")
			appendObject(image, "check_plan")
		}
	}

	return children
}

func decodeAcrossTemplate(node map[string]any) (any, bool, error) {
	across, ok := node["across"].(map[string]any)
	if !ok {
		return nil, false, nil
	}

	rawTemplate, found := across["substep_template"]
	if !found {
		return nil, false, nil
	}
	template, ok := rawTemplate.(string)
	if !ok {
		return nil, false, nil
	}

	var value any
	if err := json.Unmarshal([]byte(template), &value); err != nil {
		return nil, false, fmt.Errorf("decode across substep template: %w", err)
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, false, errors.New("decode across substep template: expected plan object")
	}

	return value, true, nil
}
