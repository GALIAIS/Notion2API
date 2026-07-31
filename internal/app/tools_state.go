package app

import (
	"fmt"
	"strings"
)

// toolTranscriptMessage is the tool-relevant projection of one inbound OpenAI
// message. It exists so the ledger and the protocol validator do not depend on
// the raw request shape.
type toolTranscriptMessage struct {
	Role       string
	Text       string
	Name       string
	ToolCallID string
	ToolCalls  []any
}

func buildToolTranscriptMessages(rawMessages []any) []toolTranscriptMessage {
	out := make([]toolTranscriptMessage, 0, len(rawMessages))
	for _, raw := range rawMessages {
		message := mapValue(raw)
		if message == nil {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(stringValue(message["role"])))
		if role == "" {
			role = "user"
		}
		entry := toolTranscriptMessage{
			Role:       role,
			Text:       strings.TrimSpace(flattenContent(message["content"])),
			Name:       strings.TrimSpace(stringValue(message["name"])),
			ToolCallID: strings.TrimSpace(stringValue(message["tool_call_id"])),
			ToolCalls:  sliceValue(message["tool_calls"]),
		}
		if len(entry.ToolCalls) == 0 {
			// Legacy single function_call carries the same information.
			if call := mapValue(message["function_call"]); call != nil {
				name := strings.TrimSpace(stringValue(call["name"]))
				if name != "" {
					entry.ToolCalls = []any{map[string]any{
						"id":       callID(name, canonicalToolArgumentsJSON(stringValue(call["arguments"])), 0),
						"type":     "function",
						"function": map[string]any{"name": name, "arguments": stringValue(call["arguments"])},
					}}
				}
			}
		}
		out = append(out, entry)
	}
	return out
}

// buildToolTranscriptFromResponsesInput projects Responses API input items into
// the same transcript shape. The Responses protocol spells tool activity as
// standalone function_call / function_call_output items rather than as fields on
// a message, so it needs its own reader.
func buildToolTranscriptFromResponsesInput(rawInput any) []toolTranscriptMessage {
	items := sliceValue(rawInput)
	if len(items) == 0 {
		return nil
	}
	out := make([]toolTranscriptMessage, 0, len(items))
	for _, raw := range items {
		item := mapValue(raw)
		if item == nil {
			continue
		}
		switch strings.TrimSpace(stringValue(item["type"])) {
		case "function_call":
			name := strings.TrimSpace(stringValue(item["name"]))
			if name == "" {
				continue
			}
			id := firstNonEmpty(
				strings.TrimSpace(stringValue(item["call_id"])),
				strings.TrimSpace(stringValue(item["id"])),
			)
			if id == "" {
				id = callID(name, canonicalToolArgumentsJSON(stringValue(item["arguments"])), len(out))
			}
			out = append(out, toolTranscriptMessage{
				Role: "assistant",
				ToolCalls: []any{map[string]any{
					"id":       id,
					"type":     "function",
					"function": map[string]any{"name": name, "arguments": stringValue(item["arguments"])},
				}},
			})
		case "function_call_output":
			id := firstNonEmpty(
				strings.TrimSpace(stringValue(item["call_id"])),
				strings.TrimSpace(stringValue(item["id"])),
			)
			if id == "" {
				continue
			}
			out = append(out, toolTranscriptMessage{
				Role:       "tool",
				ToolCallID: id,
				Text:       strings.TrimSpace(firstNonEmptyString(item["output"], item["result"], item["content"])),
			})
		default:
			role := strings.ToLower(strings.TrimSpace(stringValue(item["role"])))
			if role == "" {
				continue
			}
			text := strings.TrimSpace(flattenContent(item["content"]))
			if text == "" {
				continue
			}
			out = append(out, toolTranscriptMessage{Role: role, Text: text})
		}
	}
	return out
}

func toolTranscriptHasToolActivity(messages []toolTranscriptMessage) bool {
	for _, message := range messages {
		if len(message.ToolCalls) > 0 || message.Role == "tool" {
			return true
		}
	}
	return false
}

// validateToolConversation enforces the OpenAI tool protocol without assuming
// what any tool does: every assistant call needs exactly one matching tool
// result before another model turn is requested.
func validateToolConversation(messages []toolTranscriptMessage) error {
	pending := map[string]bool{}
	completed := map[string]bool{}
	for index, message := range messages {
		switch message.Role {
		case "assistant":
			if len(pending) > 0 {
				return fmt.Errorf("tool results missing before assistant message at index %d", index)
			}
			for _, raw := range message.ToolCalls {
				call := mapValue(raw)
				if call == nil {
					continue
				}
				id := strings.TrimSpace(stringValue(call["id"]))
				if id == "" {
					return fmt.Errorf("assistant tool call missing id at index %d", index)
				}
				if pending[id] || completed[id] {
					return fmt.Errorf("duplicate tool call id: %s", id)
				}
				pending[id] = true
			}
		case "tool":
			if message.ToolCallID == "" {
				return fmt.Errorf("tool_call_id required at index %d", index)
			}
			if !pending[message.ToolCallID] {
				return fmt.Errorf("unexpected tool result: %s", message.ToolCallID)
			}
			delete(pending, message.ToolCallID)
			completed[message.ToolCallID] = true
		}
	}
	for id := range pending {
		return fmt.Errorf("missing tool result for tool_call_id: %s", id)
	}
	return nil
}
