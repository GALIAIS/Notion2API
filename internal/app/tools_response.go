package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// toolPlanSummary tells the client what is about to happen before the structured
// call. It describes the concrete operation rather than a generic phrase.
func toolPlanSummary(calls []DetectedToolCall) string {
	if len(calls) == 0 {
		return "我将整理当前请求并继续处理。"
	}
	plans := make([]string, 0, len(calls))
	for _, call := range calls {
		plans = append(plans, toolPlanFor(call.Name, call.Arguments))
	}
	return strings.Join(plans, "\n\n")
}

func toolPlanFor(name string, arguments []byte) string {
	var args map[string]any
	_ = json.Unmarshal(arguments, &args)
	verb := "调用 " + name
	purpose := "获取该工具返回的信息"
	target := ""
	for _, key := range []string{"command", "cmd", "path", "query", "url", "input", "prompt", "keyword"} {
		if value := strings.TrimSpace(stringValue(args[key])); value != "" {
			target = value
			break
		}
	}
	lowered := strings.ToLower(name)
	switch {
	case strings.Contains(lowered, "shell"), strings.Contains(lowered, "exec"), strings.Contains(lowered, "command"):
		verb = "执行命令"
		purpose = "读取项目状态、运行检查或完成用户指定的命令"
	case strings.Contains(lowered, "read"), strings.Contains(lowered, "file"):
		verb = "读取文件内容"
		purpose = "检查文件内容并据此继续处理"
	case strings.Contains(lowered, "write"), strings.Contains(lowered, "edit"), strings.Contains(lowered, "update"):
		verb = "修改文件"
		purpose = "应用请求的变更并保留现有逻辑"
	case strings.Contains(lowered, "search"), strings.Contains(lowered, "browse"), strings.Contains(lowered, "fetch"):
		verb = "查询外部信息"
		purpose = "获取相关资料并用于当前回答"
	}
	if target != "" {
		if runes := []rune(target); len(runes) > 180 {
			target = string(runes[:180]) + "…"
		}
		verb = verb + "：" + target
	}
	return fmt.Sprintf("我将执行：%s。\n\n目的：%s。\n\n预期：拿到结果后继续处理。", verb, purpose)
}

// buildToolCallCompletion renders a non-streaming chat.completion whose
// finish_reason is tool_calls, which is what OpenAI clients wait for.
func buildToolCallCompletion(calls []DetectedToolCall, modelID string, prompt string, ledger agentLedger, includeTrace bool) map[string]any {
	summary := toolPlanSummary(calls)
	message := map[string]any{
		"role":       "assistant",
		"content":    summary,
		"tool_calls": toolCallMaps(calls),
	}
	attachChatReasoningFields(message, summary)
	payload := map[string]any{
		"id":      "chatcmpl-" + strings.ReplaceAll(randomUUID(), "-", ""),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   modelID,
		"choices": []map[string]any{{
			"index":         0,
			"message":       message,
			"finish_reason": "tool_calls",
		}},
		"usage":              buildUsage(prompt, summary, ""),
		"system_fingerprint": "notion2api-local-go",
	}
	if includeTrace {
		payload["notion_tools"] = ledger.summaryPayload()
	}
	return payload
}

func buildToolCallStreamDeltaChoice(index int, call DetectedToolCall, callIndex int) map[string]any {
	callType := call.Type
	if callType == "" {
		callType = "function"
	}
	return buildChatStreamDeltaChoice(index, map[string]any{
		"tool_calls": []map[string]any{{
			"index": callIndex,
			"id":    call.ID,
			"type":  callType,
			"function": map[string]any{
				"name":      call.Name,
				"arguments": string(call.Arguments),
			},
		}},
	})
}

// buildResponsesFunctionCallItem is the Responses API counterpart of a tool call.
func buildResponsesFunctionCallItem(call DetectedToolCall, itemID string) map[string]any {
	return map[string]any{
		"id":        itemID,
		"type":      "function_call",
		"status":    "completed",
		"name":      call.Name,
		"arguments": string(call.Arguments),
		"call_id":   call.ID,
	}
}

func buildResponsesToolCallOutput(calls []DetectedToolCall, modelID string, responseID string, createdAt int64, prompt string) map[string]any {
	summary := toolPlanSummary(calls)
	output := make([]map[string]any, 0, len(calls)+1)
	output = append(output, buildResponsesMessageItem("msg_"+strings.ReplaceAll(randomUUID(), "-", ""), summary, "completed"))
	for _, call := range calls {
		output = append(output, buildResponsesFunctionCallItem(call, "fc_"+strings.ReplaceAll(randomUUID(), "-", "")))
	}
	return map[string]any{
		"id":          responseID,
		"object":      "response",
		"created_at":  createdAt,
		"status":      "completed",
		"model":       modelID,
		"output":      output,
		"output_text": summary,
		"usage":       buildUsage(prompt, summary, ""),
	}
}
