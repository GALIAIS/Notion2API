package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const (
	notionToolCallOpenTag  = "<notion-tool-call>"
	notionToolCallCloseTag = "</notion-tool-call>"
)

// fencedToolCallPattern matches a Markdown fence whose info string is the exact
// tool name, which is the convention the tool protocol prompt asks for.
var fencedToolCallPattern = regexp.MustCompile("(?s)```([A-Za-z0-9_.-]+)[ \t]*\r?\n(.*?)```")

func callID(name string, args string, index int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s:%s", index, name, args)))
	return "call_" + hex.EncodeToString(sum[:8])
}

func scopedCallID(name string, args string, index int, scope string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s:%s", scope, index, name, args)))
	return "call_" + hex.EncodeToString(sum[:8])
}

func canonicalToolArgumentsJSON(raw string) string {
	clean := strings.TrimSpace(raw)
	if clean == "" {
		return "{}"
	}
	var decoded any
	if json.Unmarshal([]byte(clean), &decoded) != nil {
		return clean
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return clean
	}
	return string(encoded)
}

// extractFencedToolCalls finds fenced blocks whose info string names an allowed
// tool and whose body parses as a JSON object of arguments.
func extractFencedToolCalls(text string, tools []ToolDefinition, choice any) []DetectedToolCall {
	if strings.TrimSpace(text) == "" || len(tools) == 0 {
		return nil
	}
	allowed := allowedToolNames(tools)
	var out []DetectedToolCall
	for _, match := range fencedToolCallPattern.FindAllStringSubmatch(text, -1) {
		name := strings.TrimSpace(match[1])
		if !allowed[name] || !toolChoiceAllows(choice, name) {
			continue
		}
		body := strings.TrimSpace(match[2])
		var decoded any
		if json.Unmarshal([]byte(body), &decoded) != nil {
			continue
		}
		arguments := mapValue(decoded)
		if arguments == nil {
			// A non-object payload cannot satisfy a function schema.
			continue
		}
		if tool, ok := findToolDefinition(name, tools); ok {
			if err := validateToolArguments(arguments, tool); err != nil {
				continue
			}
		}
		encoded, err := json.Marshal(arguments)
		if err != nil {
			continue
		}
		out = append(out, DetectedToolCall{
			ID:        callID(name, string(encoded), len(out)),
			Type:      toolTypeOf(name, tools),
			Name:      name,
			Arguments: encoded,
		})
	}
	return out
}

// extractTaggedToolCalls parses the <notion-tool-call> envelope, which accepts a
// single object or an array of objects.
func extractTaggedToolCalls(text string, tools []ToolDefinition, choice any) []DetectedToolCall {
	start := strings.Index(text, notionToolCallOpenTag)
	end := strings.Index(text, notionToolCallCloseTag)
	if start < 0 || end <= start {
		return nil
	}
	body := strings.TrimSpace(text[start+len(notionToolCallOpenTag) : end])
	var decoded any
	if json.Unmarshal([]byte(body), &decoded) != nil {
		return nil
	}
	items := []any{decoded}
	if list, ok := decoded.([]any); ok {
		items = list
	}
	allowed := allowedToolNames(tools)
	var out []DetectedToolCall
	for _, item := range items {
		entry := mapValue(item)
		if entry == nil {
			continue
		}
		name := strings.TrimSpace(stringValue(entry["name"]))
		if !allowed[name] || !toolChoiceAllows(choice, name) {
			continue
		}
		arguments := mapValue(entry["arguments"])
		if arguments == nil {
			arguments = map[string]any{}
		}
		if tool, ok := findToolDefinition(name, tools); ok {
			if err := validateToolArguments(arguments, tool); err != nil {
				continue
			}
		}
		encoded, err := json.Marshal(arguments)
		if err != nil {
			continue
		}
		out = append(out, DetectedToolCall{
			ID:        callID(name, string(encoded), len(out)),
			Type:      toolTypeOf(name, tools),
			Name:      name,
			Arguments: encoded,
		})
	}
	return out
}

// extractToolCallsFromText tries every supported emission format and returns the
// first that yields at least one valid call.
func extractToolCallsFromText(text string, tools []ToolDefinition, choice any) []DetectedToolCall {
	if toolChoiceDisabled(choice) {
		return nil
	}
	if calls := extractTaggedToolCalls(text, tools, choice); len(calls) > 0 {
		return calls
	}
	return extractFencedToolCalls(text, tools, choice)
}

// stripToolCallMarkup removes emitted call payloads from text that is about to
// be shown to a user, so a fallback answer never leaks the raw protocol.
func stripToolCallMarkup(text string, tools []ToolDefinition) string {
	if strings.TrimSpace(text) == "" {
		return text
	}
	if start := strings.Index(text, notionToolCallOpenTag); start >= 0 {
		if end := strings.Index(text, notionToolCallCloseTag); end > start {
			text = text[:start] + text[end+len(notionToolCallCloseTag):]
		}
	}
	if len(tools) > 0 {
		allowed := allowedToolNames(tools)
		text = fencedToolCallPattern.ReplaceAllStringFunc(text, func(block string) string {
			match := fencedToolCallPattern.FindStringSubmatch(block)
			if len(match) < 2 {
				return block
			}
			if allowed[strings.TrimSpace(match[1])] {
				return ""
			}
			return block
		})
	}
	return strings.TrimSpace(text)
}

// textMayContainToolCall reports whether a partial stream could still grow into
// a tool call, which tells the stream gate to keep buffering.
func textMayContainToolCall(text string, tools []ToolDefinition) bool {
	if strings.Contains(text, "```") {
		return true
	}
	for _, prefix := range partialPrefixes(notionToolCallOpenTag) {
		if strings.HasSuffix(text, prefix) {
			return true
		}
	}
	return false
}

func partialPrefixes(tag string) []string {
	out := make([]string, 0, len(tag))
	for i := 1; i <= len(tag); i++ {
		out = append(out, tag[:i])
	}
	return out
}
