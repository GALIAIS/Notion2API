package app

import (
	"encoding/json"
	"fmt"
	"math"
)

// validateToolArguments checks decoded arguments against the tool's declared
// JSON Schema. A tool without parameters accepts anything.
func validateToolArguments(args map[string]any, tool ToolDefinition) error {
	if len(tool.Parameters) == 0 {
		return nil
	}
	return validateJSONSchema(args, tool.Parameters, "arguments")
}

// validateJSONSchema implements the subset of JSON Schema that OpenAI tool
// parameters realistically use. Unknown keywords are ignored rather than
// treated as failures, so a stricter caller schema never blocks a valid call.
func validateJSONSchema(value any, schema map[string]any, path string) error {
	if schema == nil {
		return nil
	}
	if enums := sliceValue(schema["enum"]); len(enums) > 0 {
		if !enumContains(enums, value) {
			return fmt.Errorf("%s is not an allowed enum value", path)
		}
	}
	switch stringValue(schema["type"]) {
	case "object":
		return validateObjectSchema(value, schema, path)
	case "array":
		return validateArraySchema(value, schema, path)
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s must be string", path)
		}
	case "number":
		if _, ok := toSchemaNumber(value); !ok {
			return fmt.Errorf("%s must be number", path)
		}
	case "integer":
		number, ok := toSchemaNumber(value)
		if !ok || math.Trunc(number) != number {
			return fmt.Errorf("%s must be integer", path)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be boolean", path)
		}
	case "null":
		if value != nil {
			return fmt.Errorf("%s must be null", path)
		}
	}
	return nil
}

func validateObjectSchema(value any, schema map[string]any, path string) error {
	entry := mapValue(value)
	if entry == nil {
		return fmt.Errorf("%s must be object", path)
	}
	properties := mapValue(schema["properties"])
	for _, raw := range sliceValue(schema["required"]) {
		name := stringValue(raw)
		if name == "" {
			continue
		}
		if _, ok := entry[name]; !ok {
			return fmt.Errorf("missing required argument %s", name)
		}
	}
	if additional, ok := schema["additionalProperties"].(bool); ok && !additional {
		for name := range entry {
			if _, declared := properties[name]; !declared {
				return fmt.Errorf("%s.%s is not allowed", path, name)
			}
		}
	}
	for name, child := range entry {
		childSchema := mapValue(properties[name])
		if childSchema == nil {
			continue
		}
		if err := validateJSONSchema(child, childSchema, path+"."+name); err != nil {
			return err
		}
	}
	return nil
}

func validateArraySchema(value any, schema map[string]any, path string) error {
	items, ok := value.([]any)
	if !ok {
		return fmt.Errorf("%s must be array", path)
	}
	itemSchema := mapValue(schema["items"])
	if itemSchema == nil {
		return nil
	}
	for index, child := range items {
		if err := validateJSONSchema(child, itemSchema, fmt.Sprintf("%s[%d]", path, index)); err != nil {
			return err
		}
	}
	return nil
}

func enumContains(enums []any, value any) bool {
	encoded, err := json.Marshal(value)
	if err != nil {
		return false
	}
	for _, candidate := range enums {
		other, err := json.Marshal(candidate)
		if err != nil {
			continue
		}
		if string(other) == string(encoded) {
			return true
		}
	}
	return false
}

func toSchemaNumber(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case float32:
		return float64(number), true
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	case json.Number:
		parsed, err := number.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}
