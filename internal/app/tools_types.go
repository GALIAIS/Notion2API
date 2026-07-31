package app

import (
	"encoding/json"
	"strings"
)

// ToolDefinition is the normalized form of an OpenAI tool entry. Both the
// modern `tools` array and the legacy `functions` array collapse into this
// shape so downstream code never has to branch on request vintage.
type ToolDefinition struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Source      string         `json:"source,omitempty"`
}

// DetectedToolCall is a single validated call the gateway extracted from model
// output. Arguments stay as raw JSON so re-serialization is lossless.
type DetectedToolCall struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

const (
	toolSourceClient = "client"
	toolSourceMCP    = "mcp"
)

func normalizeToolDefinitions(raw any) []ToolDefinition {
	items := sliceValue(raw)
	if len(items) == 0 {
		if decoded := decodeJSONArrayAny(raw); len(decoded) > 0 {
			items = decoded
		}
	}
	out := make([]ToolDefinition, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		entry := mapValue(item)
		if entry == nil {
			continue
		}
		definition, ok := normalizeToolDefinitionEntry(entry)
		if !ok {
			continue
		}
		if _, exists := seen[definition.Name]; exists {
			continue
		}
		seen[definition.Name] = struct{}{}
		out = append(out, definition)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeToolDefinitionEntry(entry map[string]any) (ToolDefinition, bool) {
	toolType := strings.TrimSpace(stringValue(entry["type"]))
	function := mapValue(entry["function"])
	if function == nil {
		// Legacy `functions` entries carry name/description/parameters inline.
		function = entry
	}
	name := strings.TrimSpace(stringValue(function["name"]))
	if name == "" {
		return ToolDefinition{}, false
	}
	if toolType == "" {
		toolType = "function"
	}
	if strings.Contains(toolType, "web_search") {
		// Web search is handled by the Notion upstream flag, not by the tool loop.
		return ToolDefinition{}, false
	}
	return ToolDefinition{
		Type:        toolType,
		Name:        name,
		Description: strings.TrimSpace(stringValue(function["description"])),
		Parameters:  mapValue(function["parameters"]),
		Source:      toolSourceClient,
	}, true
}

func normalizeLegacyToolDefinitions(tools any, functions any) []ToolDefinition {
	if definitions := normalizeToolDefinitions(tools); len(definitions) > 0 {
		return definitions
	}
	return normalizeToolDefinitions(functions)
}

func decodeJSONArrayAny(raw any) []any {
	if raw == nil {
		return nil
	}
	var decoded []any
	switch value := raw.(type) {
	case json.RawMessage:
		if json.Unmarshal(value, &decoded) == nil {
			return decoded
		}
	case []byte:
		if json.Unmarshal(value, &decoded) == nil {
			return decoded
		}
	case string:
		if strings.TrimSpace(value) == "" {
			return nil
		}
		if json.Unmarshal([]byte(value), &decoded) == nil {
			return decoded
		}
	}
	return nil
}

func findToolDefinition(name string, tools []ToolDefinition) (ToolDefinition, bool) {
	for _, tool := range tools {
		if tool.Name == name {
			return tool, true
		}
	}
	return ToolDefinition{}, false
}

func allowedToolNames(tools []ToolDefinition) map[string]bool {
	out := make(map[string]bool, len(tools))
	for _, tool := range tools {
		if tool.Name != "" {
			out[tool.Name] = true
		}
	}
	return out
}

func toolTypeOf(name string, tools []ToolDefinition) string {
	if tool, ok := findToolDefinition(name, tools); ok && tool.Type != "" {
		return tool.Type
	}
	return "function"
}

// normalizedToolChoiceMode reduces the many accepted tool_choice shapes to
// auto / none / required for prompt and logging purposes.
func normalizedToolChoiceMode(choice any) string {
	switch value := choice.(type) {
	case nil:
		return "auto"
	case string:
		clean := strings.ToLower(strings.TrimSpace(value))
		switch clean {
		case "none", "required", "auto":
			return clean
		case "":
			return "auto"
		default:
			return "required"
		}
	case map[string]any:
		return "required"
	default:
		return "auto"
	}
}

// toolChoiceAllows reports whether the caller's tool_choice permits calling
// the named tool.
func toolChoiceAllows(choice any, name string) bool {
	if choice == nil {
		return true
	}
	if value, ok := choice.(string); ok {
		clean := strings.ToLower(strings.TrimSpace(value))
		if clean == "none" {
			return false
		}
		if clean == "" || clean == "auto" || clean == "required" {
			return true
		}
		// A bare string is treated as an explicit function name.
		return strings.TrimSpace(value) == name
	}
	if entry := mapValue(choice); entry != nil {
		if function := mapValue(entry["function"]); function != nil {
			if pinned := strings.TrimSpace(stringValue(function["name"])); pinned != "" {
				return pinned == name
			}
		}
		if pinned := strings.TrimSpace(stringValue(entry["name"])); pinned != "" {
			return pinned == name
		}
		return true
	}
	return true
}

func toolChoiceDisabled(choice any) bool {
	return normalizedToolChoiceMode(choice) == "none"
}

func toolChoiceRequired(choice any) bool {
	return normalizedToolChoiceMode(choice) == "required"
}

// toolCallMaps converts detected calls into the OpenAI wire shape used by both
// the streaming and non-streaming responses.
func toolCallMaps(calls []DetectedToolCall) []map[string]any {
	out := make([]map[string]any, 0, len(calls))
	for _, call := range calls {
		callType := call.Type
		if callType == "" {
			callType = "function"
		}
		out = append(out, map[string]any{
			"id":   call.ID,
			"type": callType,
			"function": map[string]any{
				"name":      call.Name,
				"arguments": string(call.Arguments),
			},
		})
	}
	return out
}

// adaptiveToolCallLimit permits parallel calls only when every selected tool
// looks like a read-only, independently addressable operation. Any write,
// execution, or ambiguous tool is serialized.
func adaptiveToolCallLimit(calls []DetectedToolCall, configured int, parallelReadOnly bool) int {
	if !parallelReadOnly || len(calls) < 2 || configured < 2 {
		return 1
	}
	for _, call := range calls {
		name := strings.ToLower(strings.TrimSpace(call.Name))
		if name == "" || toolLooksMutating(name) || !toolLooksReadOnly(name) {
			return 1
		}
	}
	return configured
}

func toolLooksMutating(name string) bool {
	for _, word := range []string{"exec", "shell", "command", "write", "edit", "update", "delete", "remove", "move", "rename", "create", "patch", "apply", "install", "run", "send", "post"} {
		if strings.Contains(name, word) {
			return true
		}
	}
	return false
}

func toolLooksReadOnly(name string) bool {
	for _, word := range []string{"read", "list", "search", "find", "get", "fetch", "browse", "lookup", "inspect", "stat", "status", "describe", "info", "query"} {
		if strings.Contains(name, word) {
			return true
		}
	}
	return false
}

func limitToolCalls(calls []DetectedToolCall, limit int) []DetectedToolCall {
	if limit < 1 {
		limit = 1
	}
	if len(calls) > limit {
		return calls[:limit]
	}
	return calls
}
