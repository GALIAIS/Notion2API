package app

import (
	"encoding/json"
	"strings"
)

// defaultToolResultCharLimit bounds how much of a tool result is replayed into
// the upstream prompt. The configurable ToolsConfig.ResultCharLimit governs the
// router evidence ledger; this constant governs plain transcript rendering,
// which has no access to the live config.
const defaultToolResultCharLimit = 4000

// renderAssistantToolCalls turns an assistant message's tool_calls into readable
// transcript text. Without this the upstream model never learns that it already
// requested a tool, and it repeats the same call forever.
func renderAssistantToolCalls(rawCalls []any) string {
	if len(rawCalls) == 0 {
		return ""
	}
	lines := make([]string, 0, len(rawCalls))
	for _, raw := range rawCalls {
		call := mapValue(raw)
		if call == nil {
			continue
		}
		function := mapValue(call["function"])
		name := strings.TrimSpace(stringValue(function["name"]))
		if name == "" {
			continue
		}
		id := strings.TrimSpace(stringValue(call["id"]))
		arguments := canonicalToolArgumentsJSON(stringValue(function["arguments"]))
		if id == "" {
			lines = append(lines, name+" "+arguments)
			continue
		}
		lines = append(lines, "id="+id+" "+name+" "+arguments)
	}
	if len(lines) == 0 {
		return ""
	}
	return "called tools:\n" + strings.Join(lines, "\n")
}

// renderToolResult formats a tool role message so the model can attribute the
// output to the call it made.
func renderToolResult(toolCallID string, name string, text string, limit int) string {
	body := compactToolResult(text, limit)
	if strings.TrimSpace(body) == "" {
		body = "(empty result)"
	}
	header := "tool result"
	if id := strings.TrimSpace(toolCallID); id != "" {
		header += " id=" + id
	}
	if clean := strings.TrimSpace(name); clean != "" {
		header += " name=" + clean
	}
	return header + "\n" + body
}

// renderToolTranscript projects tool-bearing messages into conversation segments
// that buildConversationTranscriptPrompt can render.
func renderToolTranscript(messages []toolTranscriptMessage, limit int) []conversationPromptSegment {
	out := make([]conversationPromptSegment, 0, len(messages))
	for _, message := range messages {
		switch message.Role {
		case "assistant":
			parts := make([]string, 0, 2)
			if clean := strings.TrimSpace(message.Text); clean != "" {
				parts = append(parts, clean)
			}
			if rendered := renderAssistantToolCalls(message.ToolCalls); rendered != "" {
				parts = append(parts, rendered)
			}
			if len(parts) == 0 {
				continue
			}
			out = append(out, conversationPromptSegment{Role: "assistant", Text: strings.Join(parts, "\n\n")})
		case "tool":
			out = append(out, conversationPromptSegment{
				Role: "tool",
				Text: renderToolResult(message.ToolCallID, message.Name, message.Text, limit),
			})
		default:
			if clean := strings.TrimSpace(message.Text); clean != "" {
				out = append(out, conversationPromptSegment{Role: message.Role, Text: clean})
			}
		}
	}
	return out
}

// mustJSONString is a compact marshal helper for prompt embedding.
func mustJSONString(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}
