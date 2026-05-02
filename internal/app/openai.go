package app

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"mime"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

type InputAttachment struct {
	Name        string `json:"name,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Source      string `json:"source,omitempty"`
	URL         string `json:"url,omitempty"`
	Path        string `json:"path,omitempty"`
	Data        []byte `json:"-"`
}

type NormalizedInput struct {
	Prompt                 string                      `json:"prompt"`
	DisplayPrompt          string                      `json:"display_prompt,omitempty"`
	HiddenPrompt           string                      `json:"hidden_prompt,omitempty"`
	PreviousResponsePrompt string                      `json:"previous_response_prompt,omitempty"`
	Attachments            []InputAttachment           `json:"attachments,omitempty"`
	Segments               []conversationPromptSegment `json:"-"`
}

type conversationPromptSegment struct {
	Role string
	Text string
}

const defaultUploadedAttachmentPrompt = "Analyze the uploaded attachment."

var (
	hiddenMetaOpenTagPattern = regexp.MustCompile(`(?is)<([a-z][a-z0-9_-]*[-_][a-z0-9_-]*)\b[^>]*>`)
	hiddenMetaGapPattern     = regexp.MustCompile(`\n{3,}`)
)

func estimateTokens(text string) int {
	stripped := strings.TrimSpace(text)
	if stripped == "" {
		return 0
	}
	return maxInt(int(math.Ceil(float64(utf8.RuneCountInString(stripped))/4.0)), 1)
}

func buildUsage(prompt string, text string, reasoning string) map[string]any {
	promptTokens := estimateTokens(prompt)
	completionTokens := estimateTokens(text)
	reasoningTokens := estimateTokens(reasoning)
	return map[string]any{
		"prompt_tokens":     promptTokens,
		"completion_tokens": completionTokens,
		"total_tokens":      promptTokens + completionTokens,
		"completion_tokens_details": map[string]any{
			"reasoning_tokens": reasoningTokens,
		},
	}
}

func flattenContent(content any) string {
	switch x := content.(type) {
	case nil:
		return ""
	case string:
		return x
	case []any:
		parts := make([]string, 0, len(x))
		for _, raw := range x {
			switch item := raw.(type) {
			case string:
				parts = append(parts, item)
			case map[string]any:
				itemType := stringValue(item["type"])
				if itemType != "text" && itemType != "input_text" && itemType != "output_text" {
					continue
				}
				text := extractTextField(item)
				if text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "")
	case map[string]any:
		return extractTextField(x)
	default:
		return fmt.Sprint(x)
	}
}

func normalizeChatInput(payload map[string]any) (NormalizedInput, error) {
	rawMessages, ok := payload["messages"].([]any)
	if !ok {
		return NormalizedInput{}, fmt.Errorf("messages must be an array")
	}
	return normalizeChatInputFromParts(rawMessages, payload["attachments"])
}

func normalizeChatInputFromParts(rawMessages []any, attachmentsRaw any) (NormalizedInput, error) {
	if rawMessages == nil {
		return NormalizedInput{}, fmt.Errorf("messages must be an array")
	}
	segments := make([]conversationPromptSegment, 0, len(rawMessages))
	hiddenParts := make([]string, 0, len(rawMessages))
	attachments := []InputAttachment{}
	hasNonUserHistory := false
	for _, raw := range rawMessages {
		message, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		segment, hiddenSegments, atts, err := extractChatConversationPromptSegment(message)
		if err != nil {
			return NormalizedInput{}, err
		}
		if segment != nil {
			segments = append(segments, *segment)
			if strings.TrimSpace(segment.Role) != "user" {
				hasNonUserHistory = true
			}
		}
		hiddenParts = append(hiddenParts, hiddenSegments...)
		attachments = append(attachments, atts...)
	}
	extra, err := extractAttachmentsFromAny(attachmentsRaw)
	if err != nil {
		return NormalizedInput{}, err
	}
	attachments = append(attachments, extra...)
	prompt := buildConversationPrompt(segments, hasNonUserHistory)
	if prompt == "" && len(attachments) > 0 {
		if hasNonUserHistory && len(segments) > 0 {
			segmentsWithAttachment := append(append([]conversationPromptSegment(nil), segments...), conversationPromptSegment{
				Role: "user",
				Text: defaultUploadedAttachmentPrompt,
			})
			prompt = buildConversationTranscriptPrompt(segmentsWithAttachment)
		} else {
			prompt = defaultUploadedAttachmentPrompt
		}
	}
	return NormalizedInput{
		Prompt:        prompt,
		DisplayPrompt: firstNonEmpty(latestUserConversationSegmentText(segments), prompt),
		HiddenPrompt:  strings.TrimSpace(strings.Join(hiddenParts, "\n\n")),
		Attachments:   attachments,
		Segments:      cloneConversationPromptSegments(segments),
	}, nil
}

func extractChatConversationPromptSegment(message map[string]any) (*conversationPromptSegment, []string, []InputAttachment, error) {
	role := strings.TrimSpace(stringValue(message["role"]))
	if role == "" {
		role = "user"
	}
	text, atts, err := parseStructuredContent(message["content"])
	if err != nil {
		return nil, nil, nil, err
	}
	visibleText, hiddenMeta := splitHiddenMetaBlocks(text)
	hiddenParts := []string{}
	if hiddenMeta != "" {
		hiddenParts = append(hiddenParts, hiddenMeta)
	}
	visibleText = strings.TrimSpace(visibleText)
	if visibleText == "" && len(atts) > 0 && strings.EqualFold(role, "user") {
		visibleText = defaultUploadedAttachmentPrompt
	}
	if role == "tool" || visibleText == "" {
		return nil, hiddenParts, atts, nil
	}
	return &conversationPromptSegment{
		Role: role,
		Text: visibleText,
	}, hiddenParts, atts, nil
}

func buildConversationPrompt(segments []conversationPromptSegment, forceTranscript bool) string {
	if len(segments) == 0 {
		return ""
	}
	if !forceTranscript {
		parts := make([]string, 0, len(segments))
		for _, segment := range segments {
			if strings.TrimSpace(segment.Role) != "user" {
				forceTranscript = true
				break
			}
			if clean := strings.TrimSpace(segment.Text); clean != "" {
				parts = append(parts, clean)
			}
		}
		if !forceTranscript {
			return strings.TrimSpace(strings.Join(parts, "\n\n"))
		}
	}
	return buildConversationTranscriptPrompt(segments)
}

func buildConversationTranscriptPrompt(segments []conversationPromptSegment) string {
	if len(segments) == 0 {
		return ""
	}
	parts := make([]string, 0, len(segments)+1)
	parts = append(parts, "Continue the conversation using the transcript below. Reply as the assistant to the final [user] message only. Do not mention or repeat the role labels in your reply.")
	for _, segment := range segments {
		role := strings.TrimSpace(segment.Role)
		if role == "" {
			role = "user"
		}
		body := strings.TrimSpace(segment.Text)
		if body == "" {
			continue
		}
		parts = append(parts, formatPromptSection(strings.ToLower(role), body))
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func normalizeResponsesInput(payload map[string]any, previousResponse map[string]any) (NormalizedInput, error) {
	return normalizeResponsesInputFromParts(payload["input"], payload["attachments"], previousResponse)
}

func normalizeResponsesInputFromParts(rawInput any, attachmentsRaw any, previousResponse map[string]any) (NormalizedInput, error) {
	var (
		prompt       string
		hiddenPrompt string
		attachments  []InputAttachment
		segments     []conversationPromptSegment
		err          error
	)
	switch x := rawInput.(type) {
	case string:
		prompt = strings.TrimSpace(x)
		segments = appendConversationPromptSegment(segments, "user", prompt)
	case []any:
		prompt, hiddenPrompt, attachments, err = parseResponsesInputItems(x)
		if err != nil {
			return NormalizedInput{}, err
		}
		segments, err = extractResponsesInputSegments(x)
		if err != nil {
			return NormalizedInput{}, err
		}
	default:
		prompt = strings.TrimSpace(flattenContent(rawInput))
		segments = appendConversationPromptSegment(segments, "user", prompt)
	}
	extra, err := extractAttachmentsFromAny(attachmentsRaw)
	if err != nil {
		return NormalizedInput{}, err
	}
	attachments = append(attachments, extra...)
	previousPrompt := serializeStoredResponsePrompt(previousResponse)
	prompt = strings.TrimSpace(prompt)
	if prompt == "" && len(attachments) > 0 {
		prompt = defaultUploadedAttachmentPrompt
	}
	return NormalizedInput{
		Prompt:                 prompt,
		DisplayPrompt:          firstNonEmpty(latestUserConversationSegmentText(segments), prompt),
		HiddenPrompt:           hiddenPrompt,
		PreviousResponsePrompt: previousPrompt,
		Attachments:            attachments,
		Segments:               cloneConversationPromptSegments(segments),
	}, nil
}

func appendConversationPromptSegment(segments []conversationPromptSegment, role string, text string) []conversationPromptSegment {
	role = strings.ToLower(strings.TrimSpace(role))
	text = strings.TrimSpace(text)
	if role == "" || text == "" {
		return segments
	}
	return append(segments, conversationPromptSegment{
		Role: role,
		Text: text,
	})
}

func cloneConversationPromptSegments(input []conversationPromptSegment) []conversationPromptSegment {
	if len(input) == 0 {
		return nil
	}
	out := make([]conversationPromptSegment, len(input))
	copy(out, input)
	return out
}

func latestUserConversationSegmentText(segments []conversationPromptSegment) string {
	for i := len(segments) - 1; i >= 0; i-- {
		if strings.TrimSpace(strings.ToLower(segments[i].Role)) != "user" {
			continue
		}
		if clean := strings.TrimSpace(segments[i].Text); clean != "" {
			return clean
		}
	}
	return ""
}

func normalizeConversationHistorySegments(segments []conversationPromptSegment) []conversationPromptSegment {
	if len(segments) == 0 {
		return nil
	}
	out := make([]conversationPromptSegment, 0, len(segments))
	for _, segment := range segments {
		role := strings.TrimSpace(strings.ToLower(segment.Role))
		if role != "user" && role != "assistant" {
			continue
		}
		text := collapseWhitespace(segment.Text)
		if text == "" {
			continue
		}
		out = append(out, conversationPromptSegment{
			Role: role,
			Text: text,
		})
	}
	return out
}

func continuationHistorySegments(segments []conversationPromptSegment) []conversationPromptSegment {
	history := normalizeConversationHistorySegments(segments)
	if len(history) == 0 {
		return nil
	}
	if history[len(history)-1].Role == "user" {
		history = history[:len(history)-1]
	}
	if len(history) == 0 {
		return nil
	}
	hasUser := false
	hasAssistant := false
	for _, item := range history {
		switch item.Role {
		case "user":
			hasUser = true
		case "assistant":
			hasAssistant = true
		}
	}
	if !hasUser || !hasAssistant {
		return nil
	}
	return history
}

func parseResponsesInputItems(items []any) (string, string, []InputAttachment, error) {
	parts := make([]string, 0, len(items))
	hiddenParts := make([]string, 0, len(items))
	attachments := []InputAttachment{}
	for _, raw := range items {
		switch item := raw.(type) {
		case string:
			clean := strings.TrimSpace(item)
			if clean != "" {
				parts = append(parts, clean)
			}
		case map[string]any:
			visibleSegments, hiddenSegments, atts, err := renderResponsesInputItemPromptParts(item)
			if err != nil {
				return "", "", nil, err
			}
			parts = append(parts, visibleSegments...)
			hiddenParts = append(hiddenParts, hiddenSegments...)
			attachments = append(attachments, atts...)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n")), strings.TrimSpace(strings.Join(hiddenParts, "\n\n")), attachments, nil
}

func extractResponsesInputSegments(items []any) ([]conversationPromptSegment, error) {
	segments := make([]conversationPromptSegment, 0, len(items))
	for _, raw := range items {
		switch item := raw.(type) {
		case string:
			segments = appendConversationPromptSegment(segments, "user", item)
		case map[string]any:
			segment, _, _, err := extractResponsesConversationPromptSegment(item)
			if err != nil {
				return nil, err
			}
			if segment != nil {
				segments = appendConversationPromptSegment(segments, segment.Role, segment.Text)
			}
		}
	}
	return segments, nil
}

func extractResponsesConversationPromptSegment(item map[string]any) (*conversationPromptSegment, []string, []InputAttachment, error) {
	if role := strings.TrimSpace(stringValue(item["role"])); role != "" {
		text, atts, err := parseStructuredContent(item["content"])
		if err != nil {
			return nil, nil, nil, err
		}
		visibleText, hiddenMeta := splitHiddenMetaBlocks(text)
		hiddenParts := []string{}
		if hiddenMeta != "" {
			hiddenParts = append(hiddenParts, hiddenMeta)
		}
		visibleText = strings.TrimSpace(visibleText)
		if visibleText == "" && len(atts) > 0 && strings.EqualFold(role, "user") {
			visibleText = defaultUploadedAttachmentPrompt
		}
		if visibleText == "" {
			return nil, hiddenParts, atts, nil
		}
		return &conversationPromptSegment{
			Role: strings.ToLower(role),
			Text: visibleText,
		}, hiddenParts, atts, nil
	}
	itemType := strings.TrimSpace(stringValue(item["type"]))
	switch itemType {
	case "message":
		role := firstNonEmpty(strings.TrimSpace(stringValue(item["role"])), "user")
		text, atts, err := parseStructuredContent(item["content"])
		if err != nil {
			return nil, nil, nil, err
		}
		visibleText, hiddenMeta := splitHiddenMetaBlocks(text)
		hiddenParts := []string{}
		if hiddenMeta != "" {
			hiddenParts = append(hiddenParts, hiddenMeta)
		}
		visibleText = strings.TrimSpace(visibleText)
		if visibleText == "" && len(atts) > 0 && strings.EqualFold(role, "user") {
			visibleText = defaultUploadedAttachmentPrompt
		}
		if visibleText == "" {
			return nil, hiddenParts, atts, nil
		}
		return &conversationPromptSegment{
			Role: strings.ToLower(role),
			Text: visibleText,
		}, hiddenParts, atts, nil
	case "text", "input_text", "output_text":
		text, hiddenMeta := splitHiddenMetaBlocks(extractTextField(item))
		text = strings.TrimSpace(text)
		if text == "" && hiddenMeta == "" {
			return nil, nil, nil, nil
		}
		if text == "" {
			return nil, []string{hiddenMeta}, nil, nil
		}
		hiddenParts := []string{}
		if hiddenMeta != "" {
			hiddenParts = append(hiddenParts, hiddenMeta)
		}
		return &conversationPromptSegment{
			Role: "user",
			Text: text,
		}, hiddenParts, nil, nil
	default:
		att, ok, err := parseAttachmentDescriptor(item)
		if err != nil {
			return nil, nil, nil, err
		}
		if ok {
			return nil, nil, []InputAttachment{att}, nil
		}
	}
	return nil, nil, nil, nil
}

func parseStructuredContent(content any) (string, []InputAttachment, error) {
	switch x := content.(type) {
	case nil:
		return "", nil, nil
	case string:
		return strings.TrimSpace(x), nil, nil
	case []any:
		parts := []string{}
		attachments := []InputAttachment{}
		for _, raw := range x {
			switch item := raw.(type) {
			case string:
				clean := strings.TrimSpace(item)
				if clean != "" {
					parts = append(parts, clean)
				}
			case map[string]any:
				itemType := strings.TrimSpace(stringValue(item["type"]))
				switch itemType {
				case "text", "input_text", "output_text":
					text := extractTextField(item)
					if strings.TrimSpace(text) != "" {
						parts = append(parts, strings.TrimSpace(text))
					}
				default:
					att, ok, err := parseAttachmentDescriptor(item)
					if err != nil {
						return "", nil, err
					}
					if ok {
						attachments = append(attachments, att)
					}
				}
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n")), attachments, nil
	case map[string]any:
		itemType := strings.TrimSpace(stringValue(x["type"]))
		if itemType == "text" || itemType == "input_text" || itemType == "output_text" {
			return strings.TrimSpace(extractTextField(x)), nil, nil
		}
		att, ok, err := parseAttachmentDescriptor(x)
		if err != nil {
			return "", nil, err
		}
		if ok {
			return "", []InputAttachment{att}, nil
		}
		return strings.TrimSpace(flattenContent(x)), nil, nil
	default:
		return strings.TrimSpace(flattenContent(x)), nil, nil
	}
}

func renderChatMessagePromptParts(message map[string]any) ([]string, []string, []InputAttachment, error) {
	role := strings.TrimSpace(stringValue(message["role"]))
	if role == "" {
		role = "user"
	}
	text, atts, err := parseStructuredContent(message["content"])
	if err != nil {
		return nil, nil, nil, err
	}
	visibleParts := make([]string, 0, 2)
	hiddenParts := make([]string, 0, 4)
	visibleText, hiddenMeta := splitHiddenMetaBlocks(text)
	if hiddenMeta != "" {
		hiddenParts = append(hiddenParts, hiddenMeta)
	}
	if role != "tool" && strings.TrimSpace(visibleText) != "" {
		if isVisibleConversationRole(role) {
			visibleParts = append(visibleParts, formatConversationPromptSection(role, visibleText))
		} else {
			hiddenParts = append(hiddenParts, formatConversationPromptSection(role, visibleText))
		}
	}
	return visibleParts, hiddenParts, atts, nil
}

func renderResponsesInputItemPromptParts(item map[string]any) ([]string, []string, []InputAttachment, error) {
	if role := strings.TrimSpace(stringValue(item["role"])); role != "" {
		text, atts, err := parseStructuredContent(item["content"])
		if err != nil {
			return nil, nil, nil, err
		}
		visibleText, hiddenMeta := splitHiddenMetaBlocks(text)
		hiddenParts := []string{}
		if hiddenMeta != "" {
			hiddenParts = append(hiddenParts, hiddenMeta)
		}
		if strings.TrimSpace(visibleText) == "" {
			return nil, hiddenParts, atts, nil
		}
		if isVisibleConversationRole(role) {
			return []string{formatConversationPromptSection(role, visibleText)}, hiddenParts, atts, nil
		}
		hiddenParts = append(hiddenParts, formatConversationPromptSection(role, visibleText))
		return nil, hiddenParts, atts, nil
	}
	itemType := strings.TrimSpace(stringValue(item["type"]))
	switch itemType {
	case "message":
		role := firstNonEmpty(strings.TrimSpace(stringValue(item["role"])), "user")
		text, atts, err := parseStructuredContent(item["content"])
		if err != nil {
			return nil, nil, nil, err
		}
		visibleText, hiddenMeta := splitHiddenMetaBlocks(text)
		hiddenParts := []string{}
		if hiddenMeta != "" {
			hiddenParts = append(hiddenParts, hiddenMeta)
		}
		if strings.TrimSpace(visibleText) == "" {
			return nil, hiddenParts, atts, nil
		}
		if isVisibleConversationRole(role) {
			return []string{formatConversationPromptSection(role, visibleText)}, hiddenParts, atts, nil
		}
		hiddenParts = append(hiddenParts, formatConversationPromptSection(role, visibleText))
		return nil, hiddenParts, atts, nil
	case "text", "input_text", "output_text":
		text, hiddenMeta := splitHiddenMetaBlocks(extractTextField(item))
		text = strings.TrimSpace(text)
		if text == "" && hiddenMeta == "" {
			return nil, nil, nil, nil
		}
		if text == "" {
			return nil, []string{hiddenMeta}, nil, nil
		}
		if hiddenMeta == "" {
			return []string{text}, nil, nil, nil
		}
		return []string{text}, []string{hiddenMeta}, nil, nil
	default:
		att, ok, err := parseAttachmentDescriptor(item)
		if err != nil {
			return nil, nil, nil, err
		}
		if ok {
			return nil, nil, []InputAttachment{att}, nil
		}
	}
	return nil, nil, nil, nil
}

func formatPromptSection(header string, body string) string {
	header = strings.TrimSpace(header)
	body = strings.TrimSpace(body)
	switch {
	case header == "":
		return body
	case body == "":
		return "[" + header + "]"
	default:
		return "[" + header + "]\n" + body
	}
}

func formatConversationPromptSection(role string, body string) string {
	role = strings.TrimSpace(role)
	body = strings.TrimSpace(body)
	if role == "user" {
		return body
	}
	return formatPromptSection(role, body)
}

func isVisibleConversationRole(role string) bool {
	return strings.TrimSpace(role) == "user"
}

func splitHiddenMetaBlocks(text string) (string, string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", ""
	}
	lowerText := strings.ToLower(text)
	pos := 0
	visible := strings.Builder{}
	hiddenBlocks := []string{}
	for pos < len(text) {
		loc := hiddenMetaOpenTagPattern.FindStringSubmatchIndex(text[pos:])
		if loc == nil {
			visible.WriteString(text[pos:])
			break
		}
		start := pos + loc[0]
		end := pos + loc[1]
		name := strings.ToLower(text[pos+loc[2] : pos+loc[3]])
		closeTag := "</" + name + ">"
		closeOffset := strings.Index(lowerText[end:], closeTag)
		if closeOffset < 0 {
			visible.WriteString(text[pos:])
			break
		}
		closeEnd := end + closeOffset + len(closeTag)
		visible.WriteString(text[pos:start])
		hiddenBlocks = append(hiddenBlocks, strings.TrimSpace(text[start:closeEnd]))
		pos = closeEnd
	}
	visibleText := strings.TrimSpace(hiddenMetaGapPattern.ReplaceAllString(visible.String(), "\n\n"))
	hiddenText := strings.TrimSpace(strings.Join(hiddenBlocks, "\n\n"))
	return visibleText, hiddenText
}

func normalizeArgumentsJSON(value any) string {
	if value == nil {
		return "{}"
	}
	if text, ok := value.(string); ok {
		if clean := strings.TrimSpace(text); clean != "" {
			return clean
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) == 0 {
		return "{}"
	}
	return string(encoded)
}

func normalizePromptValue(value any) string {
	if value == nil {
		return ""
	}
	if text, atts, err := parseStructuredContent(value); err == nil && len(atts) == 0 && strings.TrimSpace(text) != "" {
		return strings.TrimSpace(text)
	}
	if text, ok := value.(string); ok {
		if clean := strings.TrimSpace(text); clean != "" {
			return clean
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) == 0 {
		return ""
	}
	return string(encoded)
}

func firstNonNilValue(values ...any) any {
	for _, value := range values {
		if value == nil {
			continue
		}
		if text, ok := value.(string); ok && strings.TrimSpace(text) == "" {
			continue
		}
		return value
	}
	return nil
}

func serializeStoredResponsePrompt(previousResponse map[string]any) string {
	if len(previousResponse) == 0 {
		return ""
	}
	items := sliceValue(previousResponse["output"])
	if len(items) == 0 {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, raw := range items {
		item := mapValue(raw)
		if len(item) == 0 {
			continue
		}
		switch strings.TrimSpace(stringValue(item["type"])) {
		case "message":
			role := firstNonEmpty(strings.TrimSpace(stringValue(item["role"])), "assistant")
			text, _, err := parseStructuredContent(item["content"])
			if err != nil {
				continue
			}
			if strings.TrimSpace(text) != "" {
				parts = append(parts, formatConversationPromptSection(role, text))
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func extractTextField(item map[string]any) string {
	switch textValue := item["text"].(type) {
	case string:
		return textValue
	case map[string]any:
		if nested := stringValue(textValue["value"]); nested != "" {
			return nested
		}
		if nested := stringValue(textValue["content"]); nested != "" {
			return nested
		}
	}
	if value := stringValue(item["value"]); value != "" {
		return value
	}
	if content := stringValue(item["content"]); content != "" {
		return content
	}
	return ""
}

func extractAttachmentsFromAny(raw any) ([]InputAttachment, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, nil
	}
	attachments := make([]InputAttachment, 0, len(items))
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		att, parsed, err := parseAttachmentDescriptor(item)
		if err != nil {
			return nil, err
		}
		if parsed {
			attachments = append(attachments, att)
		}
	}
	return attachments, nil
}

func parseAttachmentDescriptor(item map[string]any) (InputAttachment, bool, error) {
	itemType := strings.TrimSpace(stringValue(item["type"]))
	switch itemType {
	case "image_url", "input_image":
		return parseImageAttachment(item)
	case "file", "input_file":
		return parseFileAttachment(item)
	case "attachment":
		return parseGenericAttachment(item)
	default:
		if hasAttachmentHints(item) {
			return parseGenericAttachment(item)
		}
		return InputAttachment{}, false, nil
	}
}

func hasAttachmentHints(item map[string]any) bool {
	keys := []string{"url", "file_url", "file_data", "data", "path", "image_url"}
	for _, key := range keys {
		if strings.TrimSpace(stringValue(item[key])) != "" {
			return true
		}
		if nested, ok := item[key].(map[string]any); ok && strings.TrimSpace(stringValue(nested["url"])) != "" {
			return true
		}
	}
	return false
}

func parseImageAttachment(item map[string]any) (InputAttachment, bool, error) {
	name := firstNonEmptyString(item["filename"], item["name"])
	rawURL := ""
	switch value := item["image_url"].(type) {
	case string:
		rawURL = value
	case map[string]any:
		rawURL = firstNonEmptyString(value["url"], value["value"])
	default:
		rawURL = firstNonEmptyString(item["url"], item["file_url"], item["path"])
	}
	return buildAttachmentFromReference(rawURL, name, firstNonEmptyString(item["mime_type"], item["content_type"]), true)
}

func parseFileAttachment(item map[string]any) (InputAttachment, bool, error) {
	name := firstNonEmptyString(item["filename"], item["name"])
	if inline := firstNonEmptyString(item["file_data"], item["data"]); inline != "" {
		return buildAttachmentFromInlineData(inline, name, firstNonEmptyString(item["mime_type"], item["content_type"]))
	}
	rawURL := firstNonEmptyString(item["file_url"], item["url"], item["path"])
	return buildAttachmentFromReference(rawURL, name, firstNonEmptyString(item["mime_type"], item["content_type"]), false)
}

func parseGenericAttachment(item map[string]any) (InputAttachment, bool, error) {
	name := firstNonEmptyString(item["filename"], item["name"])
	if inline := firstNonEmptyString(item["data"], item["file_data"]); inline != "" {
		return buildAttachmentFromInlineData(inline, name, firstNonEmptyString(item["mime_type"], item["content_type"]))
	}
	rawURL := firstNonEmptyString(item["url"], item["file_url"], item["path"])
	return buildAttachmentFromReference(rawURL, name, firstNonEmptyString(item["mime_type"], item["content_type"]), false)
}

func buildAttachmentFromInlineData(raw string, name string, contentType string) (InputAttachment, bool, error) {
	decoded, detectedType, err := decodeInlineAttachment(raw)
	if err != nil {
		return InputAttachment{}, false, err
	}
	if contentType == "" {
		contentType = detectedType
	}
	return finalizeAttachment(InputAttachment{
		Name:        name,
		ContentType: contentType,
		Source:      "inline_data",
		Data:        decoded,
	}, false)
}

func buildAttachmentFromReference(rawRef string, name string, contentType string, forceImage bool) (InputAttachment, bool, error) {
	ref := strings.TrimSpace(rawRef)
	if ref == "" {
		return InputAttachment{}, false, nil
	}
	if strings.HasPrefix(ref, "data:") {
		return buildAttachmentFromInlineData(ref, name, contentType)
	}
	if isLikelyFilePath(ref) {
		return finalizeAttachment(InputAttachment{
			Name:        name,
			ContentType: contentType,
			Source:      "file_path",
			Path:        normalizeLocalPath(ref),
		}, forceImage)
	}
	return finalizeAttachment(InputAttachment{
		Name:        name,
		ContentType: contentType,
		Source:      "remote_url",
		URL:         ref,
	}, forceImage)
}

func finalizeAttachment(att InputAttachment, forceImage bool) (InputAttachment, bool, error) {
	att.Name = strings.TrimSpace(att.Name)
	if att.Name == "" {
		att.Name = inferAttachmentName(att.URL, att.Path, forceImage)
	}
	att.ContentType = normalizeContentType(att.ContentType)
	if att.ContentType == "" {
		att.ContentType = inferContentTypeFromName(att.Name, forceImage)
	}
	if att.ContentType == "" && forceImage {
		att.ContentType = "image/png"
	}
	return att, true, nil
}

func decodeInlineAttachment(raw string) ([]byte, string, error) {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "data:") {
		mimeType, data, err := decodeDataURL(trimmed)
		return data, mimeType, err
	}
	decoded, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(trimmed)
		if err != nil {
			return nil, "", fmt.Errorf("invalid base64 attachment payload")
		}
	}
	return decoded, "", nil
}

func decodeDataURL(raw string) (string, []byte, error) {
	comma := strings.Index(raw, ",")
	if comma < 0 {
		return "", nil, fmt.Errorf("invalid data url")
	}
	head := raw[:comma]
	body := raw[comma+1:]
	mimeType := ""
	if strings.HasPrefix(head, "data:") {
		mimeType = strings.TrimPrefix(head, "data:")
		mimeType = strings.TrimSuffix(mimeType, ";base64")
		mimeType = normalizeContentType(mimeType)
	}
	decoded, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(body)
		if err != nil {
			return "", nil, fmt.Errorf("invalid data url payload")
		}
	}
	return mimeType, decoded, nil
}

func firstNonEmptyString(values ...any) string {
	for _, raw := range values {
		value := strings.TrimSpace(stringValue(raw))
		if value != "" {
			return value
		}
	}
	return ""
}

func inferAttachmentName(rawURL string, rawPath string, forceImage bool) string {
	if base := filepath.Base(strings.TrimSpace(rawPath)); base != "" && base != "." && base != string(filepath.Separator) {
		return base
	}
	if parsed, err := url.Parse(strings.TrimSpace(rawURL)); err == nil {
		if base := path.Base(parsed.Path); base != "" && base != "/" && base != "." {
			return base
		}
	}
	if forceImage {
		return "image"
	}
	return "attachment"
}

func inferContentTypeFromName(name string, forceImage bool) string {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(name)))
	if ext == ".jpg" {
		return "image/jpeg"
	}
	if ext == ".csv" {
		return "text/csv"
	}
	if contentType := normalizeContentType(mime.TypeByExtension(ext)); contentType != "" {
		return contentType
	}
	if forceImage {
		return "image/png"
	}
	return ""
}

func normalizeContentType(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if semi := strings.Index(trimmed, ";"); semi >= 0 {
		trimmed = trimmed[:semi]
	}
	return strings.ToLower(strings.TrimSpace(trimmed))
}

func isLikelyFilePath(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "file://") {
		return true
	}
	if strings.HasPrefix(trimmed, `\\`) {
		return true
	}
	if len(trimmed) >= 3 && trimmed[1] == ':' && (trimmed[2] == '\\' || trimmed[2] == '/') {
		return true
	}
	if strings.HasPrefix(trimmed, "/") {
		return true
	}
	if !strings.Contains(trimmed, "://") && (strings.Contains(trimmed, "\\") || strings.Contains(trimmed, "/")) {
		return true
	}
	return false
}

func normalizeLocalPath(value string) string {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "file://") {
		if parsed, err := url.Parse(trimmed); err == nil {
			if parsed.Path != "" {
				return filepath.Clean(strings.TrimPrefix(parsed.Path, "/"))
			}
		}
		trimmed = strings.TrimPrefix(trimmed, "file://")
	}
	return filepath.Clean(trimmed)
}

func buildChatCompletion(result InferenceResult, modelID string, includeTrace bool) map[string]any {
	assistantText := sanitizeAssistantVisibleText(result.Text)
	reasoningText := sanitizeAssistantVisibleText(result.Reasoning)
	message := map[string]any{
		"role":    "assistant",
		"content": assistantText,
	}
	attachChatReasoningFields(message, reasoningText)
	payload := map[string]any{
		"id":      "chatcmpl-" + strings.ReplaceAll(randomUUID(), "-", ""),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   modelID,
		"choices": []map[string]any{{
			"index":         0,
			"message":       message,
			"finish_reason": "stop",
		}},
		"usage":              buildUsage(result.Prompt, assistantText, reasoningText),
		"system_fingerprint": "notion2api-local-go",
	}
	if includeTrace {
		payload["notion_trace"] = buildTrace(result)
	}
	return payload
}

func buildResponsesOutputTextPart(text string) map[string]any {
	return map[string]any{
		"type":        "output_text",
		"text":        text,
		"annotations": []any{},
	}
}

func buildResponsesMessageItem(itemID string, text string, status string) map[string]any {
	return buildResponsesMessageItemWithReasoning(itemID, text, "", status)
}

func buildResponsesMessageItemWithReasoning(itemID string, text string, reasoning string, status string) map[string]any {
	if strings.TrimSpace(status) == "" {
		status = "completed"
	}
	item := map[string]any{
		"id":     itemID,
		"type":   "message",
		"status": status,
		"role":   "assistant",
		"content": []map[string]any{
			buildResponsesOutputTextPart(text),
		},
	}
	if reasoningText := sanitizeAssistantVisibleText(reasoning); reasoningText != "" {
		item["reasoning"] = reasoningText
	}
	return item
}

func buildResponsesStreamTerminalItem(itemID string, status string) map[string]any {
	if strings.TrimSpace(status) == "" {
		status = "completed"
	}
	return map[string]any{
		"id":      itemID,
		"type":    "message",
		"status":  status,
		"role":    "assistant",
		"content": []any{},
	}
}

func buildResponsesStreamCompletedResponse(response map[string]any, itemID string) map[string]any {
	if response == nil {
		return nil
	}
	cloned := make(map[string]any, len(response))
	for key, value := range response {
		cloned[key] = value
	}
	cloned["output_text"] = ""
	if itemID != "" {
		cloned["output"] = []any{buildResponsesStreamTerminalItem(itemID, "completed")}
	} else {
		cloned["output"] = []any{}
	}
	delete(cloned, "reasoning")
	return cloned
}

func attachChatReasoningFields(target map[string]any, reasoning string) {
	reasoning = sanitizeAssistantVisibleText(reasoning)
	if reasoning == "" || target == nil {
		return
	}
	target["reasoning"] = reasoning
	target["reasoning_content"] = reasoning
}

func buildChatStreamReasoningChoice(index int, delta string) map[string]any {
	return map[string]any{
		"index": index,
		"delta": map[string]any{
			"reasoning":         delta,
			"reasoning_content": delta,
		},
	}
}

func buildResponsesInProgressObject(responseID string, modelID string, createdAt int64) map[string]any {
	return map[string]any{
		"id":                 responseID,
		"object":             "response",
		"created_at":         createdAt,
		"status":             "in_progress",
		"model":              modelID,
		"output":             []any{},
		"output_text":        "",
		"error":              nil,
		"incomplete_details": nil,
		"usage":              nil,
	}
}

func buildResponsesFailedObject(responseID string, modelID string, createdAt int64, message string) map[string]any {
	return map[string]any{
		"id":          responseID,
		"object":      "response",
		"created_at":  createdAt,
		"status":      "failed",
		"model":       modelID,
		"output":      []any{},
		"output_text": "",
		"error": map[string]any{
			"message": message,
			"type":    "api_error",
			"code":    "upstream_error",
		},
		"incomplete_details": nil,
		"usage":              nil,
	}
}

func buildResponsesOutputWithIDs(result InferenceResult, modelID string, includeTrace bool, responseID string, outputItemID string, createdAt int64) map[string]any {
	assistantText := sanitizeAssistantVisibleText(result.Text)
	reasoningText := sanitizeAssistantVisibleText(result.Reasoning)
	usage := buildUsage(result.Prompt, assistantText, reasoningText)
	payload := map[string]any{
		"id":         responseID,
		"object":     "response",
		"created_at": createdAt,
		"status":     "completed",
		"model":      modelID,
		"output": []any{
			buildResponsesMessageItemWithReasoning(outputItemID, assistantText, reasoningText, "completed"),
		},
		"output_text":        assistantText,
		"error":              nil,
		"incomplete_details": nil,
		"usage": map[string]any{
			"input_tokens":  usage["prompt_tokens"],
			"output_tokens": usage["completion_tokens"],
			"total_tokens":  usage["total_tokens"],
			"output_tokens_details": map[string]any{
				"reasoning_tokens": mapValue(usage["completion_tokens_details"])["reasoning_tokens"],
			},
		},
	}
	if reasoningText != "" {
		payload["reasoning"] = reasoningText
	}
	if includeTrace {
		payload["notion_trace"] = buildTrace(result)
	}
	return payload
}

func buildResponsesOutput(result InferenceResult, modelID string, includeTrace bool) map[string]any {
	return buildResponsesOutputWithIDs(
		result,
		modelID,
		includeTrace,
		"resp_"+strings.ReplaceAll(randomUUID(), "-", ""),
		"msg_"+strings.ReplaceAll(randomUUID(), "-", ""),
		time.Now().Unix(),
	)
}

func buildTrace(result InferenceResult) map[string]any {
	trace := map[string]any{
		"thread_id":         result.ThreadID,
		"trace_id":          result.TraceID,
		"message_id":        result.MessageID,
		"completed_time":    result.CompletedTime,
		"ndjson_line_count": result.NDJSONLineCount,
		"notion_model":      result.NotionModel,
	}
	if strings.TrimSpace(result.AccountEmail) != "" {
		trace["account_email"] = result.AccountEmail
	}
	if reasoning := sanitizeAssistantVisibleText(result.Reasoning); reasoning != "" {
		trace["reasoning_chars"] = len([]rune(reasoning))
	}
	if len(result.Attachments) > 0 {
		trace["attachments"] = result.Attachments
	}
	return trace
}

func splitTextChunks(text string, chunkRunes int) []string {
	if chunkRunes <= 0 {
		chunkRunes = 24
	}
	runes := []rune(text)
	if len(runes) == 0 {
		return []string{""}
	}
	chunks := make([]string, 0, (len(runes)/chunkRunes)+1)
	for start := 0; start < len(runes); start += chunkRunes {
		end := start + chunkRunes
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[start:end]))
	}
	return chunks
}

func buildChatStreamChunk(completionID string, created int64, modelID string, choices []map[string]any, usage map[string]any) map[string]any {
	payload := map[string]any{
		"id":      completionID,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   modelID,
		"choices": choices,
	}
	if len(usage) > 0 {
		payload["usage"] = usage
	}
	return payload
}

func buildChatStreamDeltaChoice(index int, delta map[string]any) map[string]any {
	return map[string]any{
		"index": index,
		"delta": delta,
	}
}

func buildChatStreamHeartbeatChoice(index int) map[string]any {
	return buildChatStreamDeltaChoice(index, map[string]any{"content": ""})
}

func buildChatStreamFinishChoice(index int, finishReason string) map[string]any {
	return map[string]any{
		"index":         index,
		"delta":         map[string]any{},
		"finish_reason": finishReason,
	}
}

func buildResponsesStreamEvent(eventType string, payload map[string]any) map[string]any {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["type"] = eventType
	return payload
}

func buildResponsesCreatedEvent(response map[string]any) map[string]any {
	return buildResponsesStreamEvent("response.created", map[string]any{"response": response})
}

func buildResponsesInProgressEvent(response map[string]any) map[string]any {
	return buildResponsesStreamEvent("response.in_progress", map[string]any{"response": response})
}

func buildResponsesOutputItemAddedEventAt(responseID string, outputIndex int, item map[string]any) map[string]any {
	return buildResponsesStreamEvent("response.output_item.added", map[string]any{
		"response_id":  responseID,
		"output_index": outputIndex,
		"item":         item,
	})
}

func buildResponsesOutputItemAddedEvent(responseID string, item map[string]any) map[string]any {
	return buildResponsesOutputItemAddedEventAt(responseID, 0, item)
}

func buildResponsesContentPartAddedEvent(responseID string, itemID string) map[string]any {
	return buildResponsesStreamEvent("response.content_part.added", map[string]any{
		"response_id":   responseID,
		"output_index":  0,
		"item_id":       itemID,
		"content_index": 0,
		"part":          buildResponsesOutputTextPart(""),
	})
}

func buildResponsesOutputTextDeltaEvent(responseID string, itemID string, delta string) map[string]any {
	return buildResponsesStreamEvent("response.output_text.delta", map[string]any{
		"response_id":   responseID,
		"output_index":  0,
		"item_id":       itemID,
		"content_index": 0,
		"delta":         delta,
	})
}

func buildResponsesOutputTextDoneEvent(responseID string, itemID string, text string) map[string]any {
	return buildResponsesStreamEvent("response.output_text.done", map[string]any{
		"response_id":   responseID,
		"output_index":  0,
		"item_id":       itemID,
		"content_index": 0,
		"text":          text,
	})
}

func buildResponsesReasoningDeltaEvent(responseID string, itemID string, delta string) map[string]any {
	return buildResponsesStreamEvent("response.reasoning.delta", map[string]any{
		"response_id":  responseID,
		"output_index": 0,
		"item_id":      itemID,
		"delta":        delta,
	})
}

func buildResponsesReasoningDoneEvent(responseID string, itemID string, reasoning string) map[string]any {
	return buildResponsesStreamEvent("response.reasoning.done", map[string]any{
		"response_id":  responseID,
		"output_index": 0,
		"item_id":      itemID,
		"text":         reasoning,
	})
}

func buildResponsesContentPartDoneEvent(responseID string, itemID string, text string) map[string]any {
	return buildResponsesStreamEvent("response.content_part.done", map[string]any{
		"response_id":   responseID,
		"output_index":  0,
		"item_id":       itemID,
		"content_index": 0,
		"part":          buildResponsesOutputTextPart(text),
	})
}

func buildResponsesOutputItemDoneEventAt(responseID string, outputIndex int, item map[string]any) map[string]any {
	return buildResponsesStreamEvent("response.output_item.done", map[string]any{
		"response_id":  responseID,
		"output_index": outputIndex,
		"item":         item,
	})
}

func buildResponsesOutputItemDoneEvent(responseID string, item map[string]any) map[string]any {
	return buildResponsesOutputItemDoneEventAt(responseID, 0, item)
}

func buildResponsesCompletedEvent(response map[string]any) map[string]any {
	return buildResponsesStreamEvent("response.completed", map[string]any{"response": response})
}

func buildResponsesFailedEvent(response map[string]any) map[string]any {
	return buildResponsesStreamEvent("response.failed", map[string]any{"response": response})
}

func marshalJSON(data any) []byte {
	payload, err := json.Marshal(data)
	if err != nil {
		panic(err)
	}
	return payload
}
