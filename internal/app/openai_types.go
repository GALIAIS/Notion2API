package app

import (
	"encoding/json"
	"net/http"
	"strings"
)

type chatCompletionsRequestBody struct {
	Model              string   `json:"model,omitempty"`
	Stream             bool     `json:"stream,omitempty"`
	ConversationID     string   `json:"conversation_id,omitempty"`
	Conversation       string   `json:"conversation,omitempty"`
	ThreadID           string   `json:"thread_id,omitempty"`
	Thread             string   `json:"thread,omitempty"`
	NotionThreadID     string   `json:"notion_thread_id,omitempty"`
	AccountEmail       string   `json:"account_email,omitempty"`
	NotionAccountEmail string   `json:"notion_account_email,omitempty"`
	UseWebSearch       *bool    `json:"use_web_search,omitempty"`
	Metadata           any      `json:"metadata,omitempty"`
	Tools              any      `json:"tools,omitempty"`
	StreamOptions      any      `json:"stream_options,omitempty"`
	Messages           any      `json:"messages,omitempty"`
	Attachments        any      `json:"attachments,omitempty"`
	StreamIncludeUsage *bool    `json:"-"`
	Type               string   `json:"type,omitempty"`
	UserName           string   `json:"user_name,omitempty"`
	CharName           string   `json:"char_name,omitempty"`
	GroupNames         []string `json:"group_names,omitempty"`
	ContinuePrefill    string   `json:"continue_prefill,omitempty"`
	ShowThoughts       *bool    `json:"show_thoughts,omitempty"`
}

type responsesRequestBody struct {
	Model              string `json:"model,omitempty"`
	Stream             bool   `json:"stream,omitempty"`
	PreviousResponseID string `json:"previous_response_id,omitempty"`
	ConversationID     string `json:"conversation_id,omitempty"`
	Conversation       string `json:"conversation,omitempty"`
	ThreadID           string `json:"thread_id,omitempty"`
	Thread             string `json:"thread,omitempty"`
	NotionThreadID     string `json:"notion_thread_id,omitempty"`
	AccountEmail       string `json:"account_email,omitempty"`
	NotionAccountEmail string `json:"notion_account_email,omitempty"`
	UseWebSearch       *bool  `json:"use_web_search,omitempty"`
	Metadata           any    `json:"metadata,omitempty"`
	Tools              any    `json:"tools,omitempty"`
	Input              any    `json:"input,omitempty"`
	Attachments        any    `json:"attachments,omitempty"`
}

func trimStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if clean := strings.TrimSpace(value); clean != "" {
			out = append(out, clean)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeTypedChatCompletionsRequestBody(body chatCompletionsRequestBody) chatCompletionsRequestBody {
	body.Model = strings.TrimSpace(body.Model)
	body.ConversationID = strings.TrimSpace(body.ConversationID)
	body.Conversation = strings.TrimSpace(body.Conversation)
	body.ThreadID = strings.TrimSpace(body.ThreadID)
	body.Thread = strings.TrimSpace(body.Thread)
	body.NotionThreadID = strings.TrimSpace(body.NotionThreadID)
	body.AccountEmail = strings.TrimSpace(body.AccountEmail)
	body.NotionAccountEmail = strings.TrimSpace(body.NotionAccountEmail)
	body.Type = strings.TrimSpace(body.Type)
	body.UserName = strings.TrimSpace(body.UserName)
	body.CharName = strings.TrimSpace(body.CharName)
	body.ContinuePrefill = strings.TrimSpace(body.ContinuePrefill)
	body.GroupNames = trimStringSlice(body.GroupNames)
	if value, ok := parseIncludeUsageFromStreamOptionsAny(body.StreamOptions); ok {
		copyValue := value
		body.StreamIncludeUsage = &copyValue
	}
	return body
}

func normalizeTypedResponsesRequestBody(body responsesRequestBody) responsesRequestBody {
	body.Model = strings.TrimSpace(body.Model)
	body.PreviousResponseID = strings.TrimSpace(body.PreviousResponseID)
	body.ConversationID = strings.TrimSpace(body.ConversationID)
	body.Conversation = strings.TrimSpace(body.Conversation)
	body.ThreadID = strings.TrimSpace(body.ThreadID)
	body.Thread = strings.TrimSpace(body.Thread)
	body.NotionThreadID = strings.TrimSpace(body.NotionThreadID)
	body.AccountEmail = strings.TrimSpace(body.AccountEmail)
	body.NotionAccountEmail = strings.TrimSpace(body.NotionAccountEmail)
	return body
}

func requestedModelFromTyped(model string, fallback string) string {
	modelID := strings.TrimSpace(model)
	if modelID == "" {
		return fallback
	}
	return modelID
}

func extractChatCompletionsRequestBody(payload map[string]any) chatCompletionsRequestBody {
	if payload == nil {
		return chatCompletionsRequestBody{}
	}
	body := chatCompletionsRequestBody{
		Model:              strings.TrimSpace(stringValue(payload["model"])),
		ConversationID:     strings.TrimSpace(stringValue(payload["conversation_id"])),
		Conversation:       strings.TrimSpace(stringValue(payload["conversation"])),
		ThreadID:           strings.TrimSpace(stringValue(payload["thread_id"])),
		Thread:             strings.TrimSpace(stringValue(payload["thread"])),
		NotionThreadID:     strings.TrimSpace(stringValue(payload["notion_thread_id"])),
		AccountEmail:       strings.TrimSpace(stringValue(payload["account_email"])),
		NotionAccountEmail: strings.TrimSpace(stringValue(payload["notion_account_email"])),
		Type:               strings.TrimSpace(stringValue(payload["type"])),
		UserName:           strings.TrimSpace(stringValue(payload["user_name"])),
		CharName:           strings.TrimSpace(stringValue(payload["char_name"])),
		ContinuePrefill:    strings.TrimSpace(stringValue(payload["continue_prefill"])),
		GroupNames:         stringSliceValue(payload["group_names"]),
	}
	body.Stream, _ = payload["stream"].(bool)
	if value, ok := parseBoolField(payload["use_web_search"]); ok {
		copyValue := value
		body.UseWebSearch = &copyValue
	}
	if value, ok := parseBoolField(payload["show_thoughts"]); ok {
		copyValue := value
		body.ShowThoughts = &copyValue
	}
	body.Metadata = payload["metadata"]
	body.Tools = payload["tools"]
	body.StreamOptions = payload["stream_options"]
	body.Messages = payload["messages"]
	body.Attachments = payload["attachments"]
	if value, ok := parseIncludeUsageFromStreamOptionsAny(body.StreamOptions); ok {
		copyValue := value
		body.StreamIncludeUsage = &copyValue
	}
	return normalizeTypedChatCompletionsRequestBody(body)
}

func extractResponsesRequestBody(payload map[string]any) responsesRequestBody {
	if payload == nil {
		return responsesRequestBody{}
	}
	body := responsesRequestBody{
		Model:              strings.TrimSpace(stringValue(payload["model"])),
		PreviousResponseID: strings.TrimSpace(stringValue(payload["previous_response_id"])),
		ConversationID:     strings.TrimSpace(stringValue(payload["conversation_id"])),
		Conversation:       strings.TrimSpace(stringValue(payload["conversation"])),
		ThreadID:           strings.TrimSpace(stringValue(payload["thread_id"])),
		Thread:             strings.TrimSpace(stringValue(payload["thread"])),
		NotionThreadID:     strings.TrimSpace(stringValue(payload["notion_thread_id"])),
		AccountEmail:       strings.TrimSpace(stringValue(payload["account_email"])),
		NotionAccountEmail: strings.TrimSpace(stringValue(payload["notion_account_email"])),
	}
	body.Stream, _ = payload["stream"].(bool)
	if value, ok := parseBoolField(payload["use_web_search"]); ok {
		copyValue := value
		body.UseWebSearch = &copyValue
	}
	body.Metadata = payload["metadata"]
	body.Tools = payload["tools"]
	body.Input = payload["input"]
	body.Attachments = payload["attachments"]
	return normalizeTypedResponsesRequestBody(body)
}

func requestedConversationIDFromTyped(r *http.Request, conversationID string, conversation string, metadata any) string {
	if fromHeader := firstRequestValue(r, "X-Conversation-ID", "X-Notion-Conversation-ID"); fromHeader != "" {
		return fromHeader
	}
	if value := strings.TrimSpace(conversationID); value != "" {
		return value
	}
	if value := strings.TrimSpace(conversation); value != "" {
		return value
	}
	return parseStringFieldFromMetadataAny(metadata, "conversation_id", "notion_conversation_id")
}

func requestedThreadIDFromTyped(r *http.Request, threadID string, thread string, notionThreadID string, metadata any) string {
	if fromHeader := firstRequestValue(r, "X-Thread-ID", "X-Notion-Thread-ID"); fromHeader != "" {
		return fromHeader
	}
	for _, value := range []string{threadID, thread, notionThreadID} {
		if clean := strings.TrimSpace(value); clean != "" {
			return clean
		}
	}
	return parseStringFieldFromMetadataAny(metadata, "thread_id", "notion_thread_id")
}

func requestedAccountEmailFromTyped(r *http.Request, accountEmail string, notionAccountEmail string, metadata any) string {
	if fromHeader := firstRequestValue(r, "X-Account-Email", "X-Notion-Account-Email"); fromHeader != "" {
		return fromHeader
	}
	for _, value := range []string{accountEmail, notionAccountEmail} {
		if clean := strings.TrimSpace(value); clean != "" {
			return clean
		}
	}
	return parseStringFieldFromMetadataAny(metadata, "account_email", "notion_account_email")
}

func requestedWebSearchFromTyped(useWebSearch *bool, metadata any, tools any, fallback bool) bool {
	if useWebSearch != nil {
		return *useWebSearch
	}
	if value, ok := parseWebSearchFromMetadataAny(metadata); ok {
		return value
	}
	if value, ok := parseWebSearchFromToolsAny(tools); ok {
		return value
	}
	return fallback
}

func parseWebSearchFromMetadataAny(raw any) (bool, bool) {
	meta := decodeJSONObjectAny(raw)
	if meta == nil {
		return false, false
	}
	for _, key := range []string{"use_web_search", "notion_use_web_search"} {
		if value, ok := meta[key]; ok {
			if parsed, parsedOK := parseBoolField(value); parsedOK {
				return parsed, true
			}
		}
	}
	return false, false
}

func parseStringFieldFromMetadataAny(raw any, keys ...string) string {
	meta := decodeJSONObjectAny(raw)
	if meta == nil {
		return ""
	}
	for _, key := range keys {
		if value := strings.TrimSpace(stringValue(meta[key])); value != "" {
			return value
		}
	}
	return ""
}

func decodeJSONObjectAny(raw any) map[string]any {
	if raw == nil {
		return nil
	}
	if meta := mapValue(raw); meta != nil {
		return meta
	}
	var decoded map[string]any
	switch value := raw.(type) {
	case json.RawMessage:
		if err := json.Unmarshal(value, &decoded); err == nil {
			return decoded
		}
	case []byte:
		if err := json.Unmarshal(value, &decoded); err == nil {
			return decoded
		}
	case string:
		if err := json.Unmarshal([]byte(value), &decoded); err == nil {
			return decoded
		}
	}
	return nil
}

func parseWebSearchFromToolsAny(raw any) (bool, bool) {
	if raw == nil {
		return false, false
	}
	toolItems := sliceValue(raw)
	if len(toolItems) == 0 {
		switch value := raw.(type) {
		case json.RawMessage:
			var decoded []map[string]any
			if err := json.Unmarshal(value, &decoded); err == nil {
				toolItems = sliceValue(decoded)
			}
		case []byte:
			var decoded []map[string]any
			if err := json.Unmarshal(value, &decoded); err == nil {
				toolItems = sliceValue(decoded)
			}
		case string:
			var decoded []map[string]any
			if err := json.Unmarshal([]byte(value), &decoded); err == nil {
				toolItems = sliceValue(decoded)
			}
		}
	}
	for _, item := range toolItems {
		tool := mapValue(item)
		if tool == nil {
			continue
		}
		toolType := strings.TrimSpace(stringValue(tool["type"]))
		if strings.Contains(toolType, "web_search") {
			return true, true
		}
	}
	return false, false
}

func parseIncludeUsageFromStreamOptionsAny(raw any) (bool, bool) {
	options := decodeJSONObjectAny(raw)
	if options == nil {
		return false, false
	}
	return parseBoolField(options["include_usage"])
}

func (body chatCompletionsRequestBody) likelySillyTavernByEnvelope() bool {
	if strings.TrimSpace(body.Type) != "" {
		return true
	}
	if strings.TrimSpace(body.UserName) != "" && strings.TrimSpace(body.CharName) != "" {
		return true
	}
	if len(body.GroupNames) > 0 {
		return true
	}
	if strings.TrimSpace(body.ContinuePrefill) != "" {
		return true
	}
	return body.ShowThoughts != nil
}
