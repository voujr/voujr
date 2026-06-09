package tools

import (
	"encoding/json"
	"fmt"
)

// validateArgs performs lightweight JSON Schema validation sufficient to catch
// malformed model output before a tool runs: required fields and top-level
// property types. A production build would swap this for a full JSON Schema
// validator (e.g. santhosh-tekuri/jsonschema); the interface stays the same.
func validateArgs(schema map[string]any, args RawArgs) error {
	if len(args) == 0 {
		args = RawArgs("{}")
	}
	var obj map[string]any
	if err := json.Unmarshal(args, &obj); err != nil {
		return fmt.Errorf("args are not a JSON object: %w", err)
	}

	// required
	if req, ok := schema["required"].([]any); ok {
		for _, r := range req {
			name, _ := r.(string)
			if _, present := obj[name]; !present {
				return fmt.Errorf("missing required field %q", name)
			}
		}
	}

	// property types (string/number/boolean/object/array)
	props, _ := schema["properties"].(map[string]any)
	for name, v := range obj {
		spec, ok := props[name].(map[string]any)
		if !ok {
			continue // unknown property — permissive; the tool may ignore it
		}
		want, _ := spec["type"].(string)
		if want == "" {
			continue
		}
		if !typeMatches(want, v) {
			return fmt.Errorf("field %q must be %s", name, want)
		}
	}
	return nil
}

func typeMatches(want string, v any) bool {
	switch want {
	case "string":
		_, ok := v.(string)
		return ok
	case "number", "integer":
		_, ok := v.(float64) // JSON numbers decode to float64
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	default:
		return true
	}
}
