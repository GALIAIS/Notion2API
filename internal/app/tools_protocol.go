package app

import (
	"encoding/json"
	"fmt"
	"strings"
)

// buildToolDefinitionBlock renders the tool schemas losslessly. The fence info
// string is the exact tool name, which is also what the extractor looks for.
func buildToolDefinitionBlock(tools []ToolDefinition) string {
	defs := make([]string, 0, len(tools))
	for _, tool := range tools {
		params := "{}"
		if len(tool.Parameters) > 0 {
			if encoded, err := json.Marshal(tool.Parameters); err == nil {
				params = string(encoded)
			}
		}
		description := strings.TrimSpace(tool.Description)
		if description == "" {
			description = "(no description)"
		}
		defs = append(defs, fmt.Sprintf("%s — %s\n```%s\n%s\n```", tool.Name, description, tool.Name, params))
	}
	return strings.Join(defs, "\n\n")
}

// buildNativeToolPrompt injects the tool contract directly into the user prompt.
// This is the single-round mode: the model either answers or emits a call.
func buildNativeToolPrompt(text string, tools []ToolDefinition, choice any, ledger agentLedger) string {
	if len(tools) == 0 || toolChoiceDisabled(choice) {
		return text
	}
	defs := buildToolDefinitionBlock(tools)
	if defs == "" {
		return text
	}
	requirement := "When the request requires a tool, emit ONLY one fenced block whose info string is the exact tool name and whose body is a JSON object of arguments."
	if toolChoiceRequired(choice) {
		requirement = "You MUST call a tool. Emit ONLY one fenced block whose info string is the exact tool name and whose body is a JSON object of arguments."
	}
	sections := []string{
		"You are an execution agent. The tools below are real tools exposed by the caller, not hypothetical integrations.",
		requirement + " Do not claim a tool is unavailable. Do not wrap the call in prose. Wait for the tool result before claiming the work is done.",
		"<tools>\n" + defs + "\n</tools>",
	}
	if evidence := ledger.RouterContext(); evidence != "" {
		sections = append(sections, evidence)
	}
	sections = append(sections, "User request:\n"+text)
	return strings.Join(sections, "\n\n")
}

// buildToolRouterPrompt asks for a JSON-only decision in a dedicated upstream
// round, which is more reliable than parsing a conversational reply.
func buildToolRouterPrompt(text string, tools []ToolDefinition, choice any, ledger agentLedger) string {
	defs, _ := json.Marshal(toolRouterDefinitions(tools))
	mode := normalizedToolChoiceMode(choice)
	evidence := ledger.RouterContext()
	if evidence == "" {
		evidence = "(no prior tool activity)"
	}
	return fmt.Sprintf(`Return JSON only for the next tool action.
Schema: {"calls":[{"name":"function_name","arguments":{}}]}
Rules: names must come from FUNCTION_DEFINITIONS; arguments must satisfy their schemas; use the evidence below; a completed call must not be repeated; if no external action is needed return {"calls":[]}; MODE required must return at least one call; output no markdown and no commentary.
MODE: %s
FUNCTION_DEFINITIONS: %s
EVIDENCE: %s
REQUEST: %s`, mode, defs, evidence, text)
}

func toolRouterDefinitions(tools []ToolDefinition) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		parameters := tool.Parameters
		if parameters == nil {
			parameters = map[string]any{}
		}
		out = append(out, map[string]any{
			"type": tool.Type,
			"function": map[string]any{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  parameters,
			},
		})
	}
	return out
}

func buildToolRouterRepairPrompt(raw string, limit int) string {
	return `Repair this tool routing output into JSON only with shape {"calls":[{"name":"function_name","arguments":{}}]}. Do not invent calls; return {"calls":[]} if unrecoverable. OUTPUT:
` + compactToolResult(raw, limit)
}

func buildToolRouterRequiredRetryPrompt(text string, tools []ToolDefinition, ledger agentLedger) string {
	defs, _ := json.Marshal(toolRouterDefinitions(tools))
	return `Select at least one required next tool call from FUNCTION_DEFINITIONS. Validate every argument against its schema. Return JSON only as {"calls":[{"name":"function_name","arguments":{}}]}.
REQUEST_AND_EVIDENCE:
` + text + "\n" + ledger.RouterContext() + "\nFUNCTION_DEFINITIONS:\n" + string(defs)
}

// parseToolRouterDecision extracts the router's JSON envelope. The second return
// value reports whether a decision was parsed at all, which is different from
// deciding that no call is needed.
func parseToolRouterDecision(text string, tools []ToolDefinition, choice any) ([]DetectedToolCall, bool) {
	clean := strings.TrimSpace(text)
	if index := strings.Index(clean, "```"); index >= 0 {
		clean = strings.TrimSpace(clean[index+3:])
		clean = strings.TrimPrefix(clean, "json")
		if end := strings.LastIndex(clean, "```"); end >= 0 {
			clean = clean[:end]
		}
	}
	start := strings.Index(clean, "{")
	end := strings.LastIndex(clean, "}")
	if start < 0 || end <= start {
		return nil, false
	}
	var envelope struct {
		Calls []struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		} `json:"calls"`
	}
	if json.Unmarshal([]byte(clean[start:end+1]), &envelope) != nil {
		return nil, false
	}
	out := make([]DetectedToolCall, 0, len(envelope.Calls))
	for index, call := range envelope.Calls {
		tool, ok := findToolDefinition(strings.TrimSpace(call.Name), tools)
		if !ok || !toolChoiceAllows(choice, tool.Name) {
			continue
		}
		arguments := call.Arguments
		if arguments == nil {
			arguments = map[string]any{}
		}
		if err := validateToolArguments(arguments, tool); err != nil {
			continue
		}
		encoded, err := json.Marshal(arguments)
		if err != nil {
			continue
		}
		out = append(out, DetectedToolCall{
			ID:        callID(tool.Name, string(encoded), index),
			Type:      tool.Type,
			Name:      tool.Name,
			Arguments: encoded,
		})
	}
	return out, true
}
