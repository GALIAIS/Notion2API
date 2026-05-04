package app

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

const maxConversationEntries = 1000

type ConversationAttachment struct {
	Name        string `json:"name,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Source      string `json:"source,omitempty"`
	URL         string `json:"url,omitempty"`
	Path        string `json:"path,omitempty"`
	SizeBytes   int    `json:"size_bytes,omitempty"`
}

type ConversationMessage struct {
	ID          string                   `json:"id"`
	Role        string                   `json:"role"`
	Status      string                   `json:"status"`
	Content     string                   `json:"content"`
	CreatedAt   time.Time                `json:"created_at"`
	UpdatedAt   time.Time                `json:"updated_at"`
	Attachments []ConversationAttachment `json:"attachments,omitempty"`
}

type ConversationEntry struct {
	ID                string                   `json:"id"`
	Title             string                   `json:"title"`
	Origin            string                   `json:"origin,omitempty"`
	RemoteOnly        bool                     `json:"remote_only,omitempty"`
	Ephemeral         bool                     `json:"ephemeral,omitempty"`
	EphemeralReason   string                   `json:"ephemeral_reason,omitempty"`
	AutoDeleteAt      *time.Time               `json:"auto_delete_at,omitempty"`
	Source            string                   `json:"source"`
	Transport         string                   `json:"transport"`
	Status            string                   `json:"status"`
	Model             string                   `json:"model"`
	NotionModel       string                   `json:"notion_model,omitempty"`
	UseWebSearch      bool                     `json:"use_web_search"`
	RequestPrompt     string                   `json:"request_prompt,omitempty"`
	CreatedAt         time.Time                `json:"created_at"`
	UpdatedAt         time.Time                `json:"updated_at"`
	ResponseID        string                   `json:"response_id,omitempty"`
	CompletionID      string                   `json:"completion_id,omitempty"`
	ThreadID          string                   `json:"thread_id,omitempty"`
	TraceID           string                   `json:"trace_id,omitempty"`
	MessageID         string                   `json:"message_id,omitempty"`
	AccountEmail      string                   `json:"account_email,omitempty"`
	CreatedByDisplay  string                   `json:"created_by_display_name,omitempty"`
	Error             string                   `json:"error,omitempty"`
	InputAttachments  []ConversationAttachment `json:"input_attachments,omitempty"`
	OutputAttachments []UploadedAttachment     `json:"output_attachments,omitempty"`
	Messages          []ConversationMessage    `json:"messages,omitempty"`
	cachedPreview     string                   `json:"-"`
}

type ConversationSummary struct {
	ID                    string     `json:"id"`
	Title                 string     `json:"title"`
	Origin                string     `json:"origin,omitempty"`
	RemoteOnly            bool       `json:"remote_only,omitempty"`
	Ephemeral             bool       `json:"ephemeral,omitempty"`
	EphemeralReason       string     `json:"ephemeral_reason,omitempty"`
	AutoDeleteAt          *time.Time `json:"auto_delete_at,omitempty"`
	Source                string     `json:"source"`
	Transport             string     `json:"transport"`
	Status                string     `json:"status"`
	Model                 string     `json:"model"`
	UseWebSearch          bool       `json:"use_web_search"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
	ThreadID              string     `json:"thread_id,omitempty"`
	TraceID               string     `json:"trace_id,omitempty"`
	MessageID             string     `json:"message_id,omitempty"`
	ResponseID            string     `json:"response_id,omitempty"`
	CompletionID          string     `json:"completion_id,omitempty"`
	AccountEmail          string     `json:"account_email,omitempty"`
	CreatedByDisplay      string     `json:"created_by_display_name,omitempty"`
	Error                 string     `json:"error,omitempty"`
	Preview               string     `json:"preview,omitempty"`
	MessageCount          int        `json:"message_count"`
	InputAttachmentCount  int        `json:"input_attachment_count"`
	OutputAttachmentCount int        `json:"output_attachment_count"`
}

type ConversationEvent struct {
	Type           string               `json:"type"`
	ConversationID string               `json:"conversation_id"`
	At             time.Time            `json:"at"`
	Delta          string               `json:"delta,omitempty"`
	Error          string               `json:"error,omitempty"`
	Summary        *ConversationSummary `json:"summary,omitempty"`
	Conversation   *ConversationEntry   `json:"conversation,omitempty"`
	Message        *ConversationMessage `json:"message,omitempty"`
}

type ConversationCreateRequest struct {
	PreferredID      string
	Ephemeral        bool
	EphemeralReason  string
	AutoDeleteAt     time.Time
	Source           string
	Transport        string
	Model            string
	NotionModel      string
	Prompt           string
	UseWebSearch     bool
	InputAttachments []ConversationAttachment
}

type ConversationStore struct {
	mu        sync.RWMutex
	items     map[string]*ConversationEntry
	order     []string
	subs      map[int]chan ConversationEvent
	nextSubID int
}

func newConversationStore() *ConversationStore {
	return &ConversationStore{
		items: map[string]*ConversationEntry{},
		subs:  map[int]chan ConversationEvent{},
	}
}

func newConversationStoreFromEntries(entries []ConversationEntry) *ConversationStore {
	store := newConversationStore()
	for _, entry := range entries {
		cloned := cloneConversationEntry(&entry)
		refreshConversationDerivedFields(&cloned)
		store.items[cloned.ID] = &cloned
		store.order = append(store.order, cloned.ID)
	}
	store.trimLocked()
	return store
}

func summarizeInputAttachments(items []InputAttachment) []ConversationAttachment {
	out := make([]ConversationAttachment, 0, len(items))
	for _, item := range items {
		out = append(out, ConversationAttachment{
			Name:        strings.TrimSpace(item.Name),
			ContentType: strings.TrimSpace(item.ContentType),
			Source:      strings.TrimSpace(item.Source),
			URL:         strings.TrimSpace(item.URL),
			Path:        strings.TrimSpace(item.Path),
			SizeBytes:   len(item.Data),
		})
	}
	return out
}

func summarizeUploadedAttachments(items []UploadedAttachment) []ConversationAttachment {
	out := make([]ConversationAttachment, 0, len(items))
	for _, item := range items {
		out = append(out, ConversationAttachment{
			Name:        strings.TrimSpace(item.Name),
			ContentType: strings.TrimSpace(item.ContentType),
			Source:      strings.TrimSpace(item.Source),
			URL:         strings.TrimSpace(firstNonEmpty(item.SignedGetURL, item.AttachmentURL)),
			SizeBytes:   item.SizeBytes,
		})
	}
	return out
}

func conversationTitle(prompt string, attachments []ConversationAttachment) string {
	prompt = collapseWhitespace(prompt)
	if prompt != "" {
		return truncateRunes(prompt, 72)
	}
	if len(attachments) > 0 {
		names := make([]string, 0, minInt(len(attachments), 2))
		for i := 0; i < len(attachments) && i < 2; i++ {
			name := strings.TrimSpace(attachments[i].Name)
			if name == "" {
				name = fmt.Sprintf("attachment-%d", i+1)
			}
			names = append(names, name)
		}
		return truncateRunes("Attachment · "+strings.Join(names, ", "), 72)
	}
	return "Untitled conversation"
}

func collapseWhitespace(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}

func truncateRunes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= limit {
		return string(runes)
	}
	if limit <= 1 {
		return string(runes[:limit])
	}
	return string(runes[:limit-1]) + "…"
}

func cloneTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := value.UTC()
	return &cloned
}

func timePointer(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	cloned := value.UTC()
	return &cloned
}

func cloneConversationAttachments(items []ConversationAttachment) []ConversationAttachment {
	if len(items) == 0 {
		return nil
	}
	out := make([]ConversationAttachment, len(items))
	copy(out, items)
	return out
}

func cloneUploadedAttachments(items []UploadedAttachment) []UploadedAttachment {
	if len(items) == 0 {
		return nil
	}
	out := make([]UploadedAttachment, len(items))
	for i, item := range items {
		out[i] = item
		if len(item.Metadata) > 0 {
			out[i].Metadata = cloneStringAnyMap(item.Metadata)
		}
	}
	return out
}

func cloneStringAnyMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		switch typed := value.(type) {
		case map[string]any:
			out[key] = cloneStringAnyMap(typed)
		case []any:
			out[key] = cloneAnySlice(typed)
		default:
			out[key] = typed
		}
	}
	return out
}

func cloneConversationMessage(msg ConversationMessage) ConversationMessage {
	msg.Attachments = cloneConversationAttachments(msg.Attachments)
	return msg
}

func cloneConversationEntry(entry *ConversationEntry) ConversationEntry {
	if entry == nil {
		return ConversationEntry{}
	}
	out := *entry
	out.AutoDeleteAt = cloneTimePointer(entry.AutoDeleteAt)
	out.InputAttachments = cloneConversationAttachments(entry.InputAttachments)
	out.OutputAttachments = cloneUploadedAttachments(entry.OutputAttachments)
	if len(entry.Messages) > 0 {
		out.Messages = make([]ConversationMessage, len(entry.Messages))
		for i, msg := range entry.Messages {
			out.Messages[i] = cloneConversationMessage(msg)
		}
	}
	return out
}

func copyConversationEntryValue(entry *ConversationEntry) ConversationEntry {
	if entry == nil {
		return ConversationEntry{}
	}
	return *entry
}

func buildConversationSummary(entry *ConversationEntry) ConversationSummary {
	preview := entry.cachedPreview
	if preview == "" && len(entry.Messages) > 0 {
		preview = conversationPreviewFromMessages(entry.Messages)
	}
	return ConversationSummary{
		ID:                    entry.ID,
		Title:                 entry.Title,
		Origin:                firstNonEmpty(strings.TrimSpace(entry.Origin), "local"),
		RemoteOnly:            entry.RemoteOnly,
		Ephemeral:             entry.Ephemeral,
		EphemeralReason:       entry.EphemeralReason,
		AutoDeleteAt:          cloneTimePointer(entry.AutoDeleteAt),
		Source:                entry.Source,
		Transport:             entry.Transport,
		Status:                entry.Status,
		Model:                 entry.Model,
		UseWebSearch:          entry.UseWebSearch,
		CreatedAt:             entry.CreatedAt,
		UpdatedAt:             entry.UpdatedAt,
		ThreadID:              entry.ThreadID,
		TraceID:               entry.TraceID,
		MessageID:             entry.MessageID,
		ResponseID:            entry.ResponseID,
		CompletionID:          entry.CompletionID,
		AccountEmail:          entry.AccountEmail,
		CreatedByDisplay:      entry.CreatedByDisplay,
		Error:                 entry.Error,
		Preview:               preview,
		MessageCount:          len(entry.Messages),
		InputAttachmentCount:  len(entry.InputAttachments),
		OutputAttachmentCount: len(entry.OutputAttachments),
	}
}

func conversationPreviewFromMessages(messages []ConversationMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		text := collapseWhitespace(messages[i].Content)
		if text == "" && len(messages[i].Attachments) > 0 {
			text = fmt.Sprintf("%d attachments", len(messages[i].Attachments))
		}
		if text != "" {
			return truncateRunes(text, 96)
		}
	}
	return ""
}

func refreshConversationDerivedFields(entry *ConversationEntry) {
	if entry == nil {
		return
	}
	entry.cachedPreview = conversationPreviewFromMessages(entry.Messages)
}

func conversationMessageSegments(entry *ConversationEntry) []conversationPromptSegment {
	if entry == nil || len(entry.Messages) == 0 {
		return nil
	}
	segments := make([]conversationPromptSegment, 0, len(entry.Messages))
	for _, msg := range entry.Messages {
		role := strings.TrimSpace(strings.ToLower(msg.Role))
		if role != "user" && role != "assistant" {
			continue
		}
		text := collapseWhitespace(msg.Content)
		if text == "" {
			continue
		}
		segments = append(segments, conversationPromptSegment{
			Role: role,
			Text: text,
		})
	}
	return segments
}

func conversationSegmentsMatchSuffix(entrySegments []conversationPromptSegment, history []conversationPromptSegment) bool {
	if len(entrySegments) == 0 || len(history) == 0 {
		return false
	}
	shorter := entrySegments
	longer := history
	if len(shorter) > len(longer) {
		shorter, longer = longer, shorter
	}
	offset := len(longer) - len(shorter)
	for idx := range shorter {
		if longer[offset+idx].Role != shorter[idx].Role {
			return false
		}
		if longer[offset+idx].Text != shorter[idx].Text {
			return false
		}
	}
	return true
}

func (s *ConversationStore) broadcast(event ConversationEvent) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, ch := range s.subs {
		select {
		case ch <- event:
		default:
		}
	}
}

func (s *ConversationStore) moveToFrontLocked(id string) {
	if len(s.order) == 0 || s.order[0] == id {
		return
	}
	next := make([]string, 0, len(s.order))
	next = append(next, id)
	for _, itemID := range s.order {
		if itemID == id {
			continue
		}
		next = append(next, itemID)
	}
	s.order = next
}

func (s *ConversationStore) trimLocked() {
	for len(s.order) > maxConversationEntries {
		last := s.order[len(s.order)-1]
		delete(s.items, last)
		s.order = s.order[:len(s.order)-1]
	}
}

func (s *ConversationStore) Create(req ConversationCreateRequest) ConversationEntry {
	now := time.Now().UTC()
	id := strings.TrimSpace(req.PreferredID)
	if id == "" {
		id = "conv_" + strings.ReplaceAll(randomUUID(), "-", "")
	}
	entry := ConversationEntry{
		ID:                id,
		Title:             conversationTitle(req.Prompt, req.InputAttachments),
		Origin:            "local",
		Ephemeral:         req.Ephemeral,
		EphemeralReason:   strings.TrimSpace(req.EphemeralReason),
		AutoDeleteAt:      timePointer(req.AutoDeleteAt),
		Source:            firstNonEmpty(req.Source, "api"),
		Transport:         firstNonEmpty(req.Transport, "responses"),
		Status:            "running",
		Model:             strings.TrimSpace(req.Model),
		NotionModel:       strings.TrimSpace(req.NotionModel),
		UseWebSearch:      req.UseWebSearch,
		RequestPrompt:     strings.TrimSpace(req.Prompt),
		CreatedAt:         now,
		UpdatedAt:         now,
		InputAttachments:  cloneConversationAttachments(req.InputAttachments),
		OutputAttachments: nil,
	}
	if entry.RequestPrompt != "" || len(entry.InputAttachments) > 0 {
		entry.Messages = append(entry.Messages, ConversationMessage{
			ID:          "msg_user_" + strings.ReplaceAll(randomUUID(), "-", ""),
			Role:        "user",
			Status:      "completed",
			Content:     entry.RequestPrompt,
			CreatedAt:   now,
			UpdatedAt:   now,
			Attachments: cloneConversationAttachments(entry.InputAttachments),
		})
	}
	refreshConversationDerivedFields(&entry)

	s.mu.Lock()
	if s.items[id] != nil {
		id = "conv_" + strings.ReplaceAll(randomUUID(), "-", "")
		entry.ID = id
	}
	entryPtr := &entry
	s.items[id] = entryPtr
	s.order = append([]string{id}, s.order...)
	s.trimLocked()
	cloned := copyConversationEntryValue(entryPtr)
	summary := buildConversationSummary(entryPtr)
	s.mu.Unlock()

	s.broadcast(ConversationEvent{
		Type:           "conversation.created",
		ConversationID: id,
		At:             now,
		Summary:        &summary,
		Conversation:   entryPtr,
	})
	return cloned
}

func (s *ConversationStore) Continue(conversationID string, req ConversationCreateRequest) (ConversationEntry, error) {
	now := time.Now().UTC()
	var (
		cloned  ConversationEntry
		summary ConversationSummary
		ok      bool
		entry   *ConversationEntry
	)
	s.mu.Lock()
	current := s.items[conversationID]
	if current != nil {
		next := cloneConversationEntry(current)
		next.Source = firstNonEmpty(req.Source, next.Source)
		next.Transport = firstNonEmpty(req.Transport, next.Transport)
		if req.Ephemeral {
			next.Ephemeral = true
			next.EphemeralReason = firstNonEmpty(strings.TrimSpace(req.EphemeralReason), next.EphemeralReason)
			if !req.AutoDeleteAt.IsZero() {
				next.AutoDeleteAt = timePointer(req.AutoDeleteAt)
			}
		}
		if clean := strings.TrimSpace(req.Model); clean != "" {
			next.Model = clean
		}
		if clean := strings.TrimSpace(req.NotionModel); clean != "" {
			next.NotionModel = clean
		}
		next.UseWebSearch = req.UseWebSearch
		next.Status = "running"
		next.Error = ""
		next.InputAttachments = cloneConversationAttachments(req.InputAttachments)
		next.UpdatedAt = now
		if len(next.Messages) > 0 {
			last := &next.Messages[len(next.Messages)-1]
			if last.Role == "assistant" && last.Status != "completed" {
				last.Status = "failed"
				last.UpdatedAt = now
			}
		}
		if strings.TrimSpace(req.Prompt) != "" || len(req.InputAttachments) > 0 {
			next.Messages = append(next.Messages, ConversationMessage{
				ID:          "msg_user_" + strings.ReplaceAll(randomUUID(), "-", ""),
				Role:        "user",
				Status:      "completed",
				Content:     strings.TrimSpace(req.Prompt),
				CreatedAt:   now,
				UpdatedAt:   now,
				Attachments: cloneConversationAttachments(req.InputAttachments),
			})
		}
		refreshConversationDerivedFields(&next)
		entry = &next
		s.items[conversationID] = entry
		s.moveToFrontLocked(conversationID)
		cloned = copyConversationEntryValue(entry)
		summary = buildConversationSummary(entry)
		ok = true
	}
	s.mu.Unlock()
	if !ok {
		return ConversationEntry{}, fmt.Errorf("conversation not found")
	}
	s.broadcast(ConversationEvent{
		Type:           "conversation.updated",
		ConversationID: conversationID,
		At:             now,
		Summary:        &summary,
		Conversation:   entry,
	})
	return cloned, nil
}

func (s *ConversationStore) ensureAssistantMessageLocked(entry *ConversationEntry, now time.Time) *ConversationMessage {
	if len(entry.Messages) > 0 {
		last := &entry.Messages[len(entry.Messages)-1]
		if last.Role == "assistant" && last.Status != "completed" {
			last.UpdatedAt = now
			return last
		}
	}
	entry.Messages = append(entry.Messages, ConversationMessage{
		ID:        "msg_assistant_" + strings.ReplaceAll(randomUUID(), "-", ""),
		Role:      "assistant",
		Status:    "streaming",
		CreatedAt: now,
		UpdatedAt: now,
	})
	return &entry.Messages[len(entry.Messages)-1]
}

func (s *ConversationStore) SetEnvelopeIDs(conversationID string, responseID string, completionID string) {
	now := time.Now().UTC()
	var (
		summary ConversationSummary
		ok      bool
		entry   *ConversationEntry
	)
	s.mu.Lock()
	current := s.items[conversationID]
	if current != nil {
		next := cloneConversationEntry(current)
		if strings.TrimSpace(responseID) != "" {
			next.ResponseID = strings.TrimSpace(responseID)
		}
		if strings.TrimSpace(completionID) != "" {
			next.CompletionID = strings.TrimSpace(completionID)
		}
		next.UpdatedAt = now
		refreshConversationDerivedFields(&next)
		entry = &next
		s.items[conversationID] = entry
		s.moveToFrontLocked(conversationID)
		summary = buildConversationSummary(entry)
		ok = true
	}
	s.mu.Unlock()
	if ok {
		s.broadcast(ConversationEvent{
			Type:           "conversation.updated",
			ConversationID: conversationID,
			At:             now,
			Summary:        &summary,
			Conversation:   entry,
		})
	}
}

func (s *ConversationStore) AppendAssistantDelta(conversationID string, delta string) {
	delta = strings.TrimRight(delta, "\r")
	if delta == "" {
		return
	}
	now := time.Now().UTC()
	var (
		summary ConversationSummary
		msg     *ConversationMessage
		ok      bool
		entry   *ConversationEntry
	)
	s.mu.Lock()
	current := s.items[conversationID]
	if current != nil {
		next := cloneConversationEntry(current)
		assistant := s.ensureAssistantMessageLocked(&next, now)
		assistant.Content += delta
		assistant.Status = "streaming"
		assistant.UpdatedAt = now
		next.Status = "running"
		next.UpdatedAt = now
		refreshConversationDerivedFields(&next)
		entry = &next
		s.items[conversationID] = entry
		s.moveToFrontLocked(conversationID)
		summary = buildConversationSummary(entry)
		msg = assistant
		ok = true
	}
	s.mu.Unlock()
	if ok {
		s.broadcast(ConversationEvent{
			Type:           "conversation.delta",
			ConversationID: conversationID,
			At:             now,
			Delta:          delta,
			Summary:        &summary,
			Conversation:   entry,
			Message:        msg,
		})
	}
}

func (s *ConversationStore) Complete(conversationID string, result InferenceResult) {
	now := time.Now().UTC()
	var (
		summary ConversationSummary
		ok      bool
		entry   *ConversationEntry
	)
	s.mu.Lock()
	current := s.items[conversationID]
	if current != nil {
		next := cloneConversationEntry(current)
		next.Status = "completed"
		next.UpdatedAt = now
		if next.Ephemeral {
			next.AutoDeleteAt = timePointer(now.Add(sillyTavernQuietConversationTTL))
		}
		next.ThreadID = strings.TrimSpace(result.ThreadID)
		next.TraceID = strings.TrimSpace(result.TraceID)
		next.MessageID = strings.TrimSpace(result.MessageID)
		next.AccountEmail = strings.TrimSpace(result.AccountEmail)
		next.Error = ""
		next.OutputAttachments = cloneUploadedAttachments(result.Attachments)
		assistant := s.ensureAssistantMessageLocked(&next, now)
		assistant.Status = "completed"
		assistant.Content = sanitizeAssistantVisibleText(result.Text)
		assistant.Attachments = summarizeUploadedAttachments(result.Attachments)
		assistant.UpdatedAt = now
		if len(next.Messages) > 0 {
			next.Messages[len(next.Messages)-1] = cloneConversationMessage(*assistant)
		}
		refreshConversationDerivedFields(&next)
		entry = &next
		s.items[conversationID] = entry
		s.moveToFrontLocked(conversationID)
		summary = buildConversationSummary(entry)
		ok = true
	}
	s.mu.Unlock()
	if ok {
		s.broadcast(ConversationEvent{
		Type:           "conversation.completed",
		ConversationID: conversationID,
		At:             now,
		Summary:        &summary,
		Conversation:   entry,
	})
	}
}

func (s *ConversationStore) Fail(conversationID string, err error) {
	if err == nil {
		return
	}
	now := time.Now().UTC()
	message := strings.TrimSpace(err.Error())
	var (
		summary ConversationSummary
		ok      bool
		entry   *ConversationEntry
	)
	s.mu.Lock()
	current := s.items[conversationID]
	if current != nil {
		next := cloneConversationEntry(current)
		next.Status = "failed"
		next.Error = message
		next.UpdatedAt = now
		if next.Ephemeral {
			next.AutoDeleteAt = timePointer(now.Add(sillyTavernQuietConversationTTL))
		}
		if len(next.Messages) > 0 {
			last := &next.Messages[len(next.Messages)-1]
			if last.Role == "assistant" && last.Status != "completed" {
				last.Status = "failed"
				last.UpdatedAt = now
			}
		}
		refreshConversationDerivedFields(&next)
		entry = &next
		s.items[conversationID] = entry
		s.moveToFrontLocked(conversationID)
		summary = buildConversationSummary(entry)
		ok = true
	}
	s.mu.Unlock()
	if ok {
		s.broadcast(ConversationEvent{
		Type:           "conversation.failed",
		ConversationID: conversationID,
		At:             now,
		Error:          message,
		Summary:        &summary,
		Conversation:   entry,
	})
	}
}

func (s *ConversationStore) Delete(conversationID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.items[conversationID]
	if entry == nil {
		return fmt.Errorf("conversation not found")
	}
	if entry.Status == "running" {
		return fmt.Errorf("conversation is still running")
	}
	delete(s.items, conversationID)
	next := make([]string, 0, len(s.order))
	for _, id := range s.order {
		if id != conversationID {
			next = append(next, id)
		}
	}
	s.order = next
	return nil
}

func (s *ConversationStore) List() []ConversationSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]ConversationSummary, 0, len(s.order))
	for _, id := range s.order {
		entry := s.items[id]
		if entry == nil {
			continue
		}
		items = append(items, buildConversationSummary(entry))
	}
	return items
}

func (s *ConversationStore) ListExpiredEphemeral(now time.Time, limit int) []ConversationEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 {
		limit = len(s.order)
	}
	items := make([]ConversationEntry, 0, minInt(limit, len(s.order)))
	for _, id := range s.order {
		entry := s.items[id]
		if entry == nil || !entry.Ephemeral {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(entry.Status), "running") {
			continue
		}
		if entry.AutoDeleteAt == nil || entry.AutoDeleteAt.After(now) {
			continue
		}
		items = append(items, copyConversationEntryValue(entry))
		if len(items) >= limit {
			break
		}
	}
	return items
}

func (s *ConversationStore) Get(conversationID string) (ConversationEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry := s.items[conversationID]
	if entry == nil {
		return ConversationEntry{}, false
	}
	return copyConversationEntryValue(entry), true
}

func (s *ConversationStore) FindByThreadID(threadID string) (ConversationEntry, bool) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return ConversationEntry{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, id := range s.order {
		entry := s.items[id]
		if entry == nil {
			continue
		}
		if strings.TrimSpace(entry.ThreadID) != threadID {
			continue
		}
		return copyConversationEntryValue(entry), true
	}
	return ConversationEntry{}, false
}

func (s *ConversationStore) FindContinuationBySegments(history []conversationPromptSegment) (ConversationEntry, bool) {
	normalizedHistory := normalizeConversationHistorySegments(history)
	if len(normalizedHistory) == 0 {
		return ConversationEntry{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, id := range s.order {
		entry := s.items[id]
		if entry == nil {
			continue
		}
		if strings.TrimSpace(entry.ThreadID) == "" || strings.TrimSpace(strings.ToLower(entry.Status)) == "running" {
			continue
		}
		entrySegments := conversationMessageSegments(entry)
		if !conversationSegmentsMatchSuffix(entrySegments, normalizedHistory) {
			continue
		}
		return copyConversationEntryValue(entry), true
	}
	return ConversationEntry{}, false
}

func (s *ConversationStore) Subscribe() (int, <-chan ConversationEvent) {
	ch := make(chan ConversationEvent, 128)
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextSubID
	s.nextSubID++
	s.subs[id] = ch
	return id, ch
}

func (s *ConversationStore) Unsubscribe(id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch, ok := s.subs[id]; ok {
		delete(s.subs, id)
		close(ch)
	}
}

func (s *ServerState) conversations() *ConversationStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Conversations == nil {
		s.Conversations = newConversationStore()
	}
	return s.Conversations
}

func (s *ServerState) persistConversationSnapshot(conversationID string) {
	if s == nil || strings.TrimSpace(conversationID) == "" {
		return
	}
	store := s.conversationPersistenceStore()
	s.mu.RLock()
	enabled := conversationSnapshotsPersistenceEnabled(s.Config)
	s.mu.RUnlock()
	if store == nil || !enabled {
		return
	}
	entry, ok := s.conversations().Get(conversationID)
	if !ok {
		return
	}
	if err := store.SaveConversation(entry); err != nil {
		log.Printf("[sqlite] save conversation %s failed: %v", conversationID, err)
	}
}

func (s *ServerState) deleteResponsesByConversationOrThread(conversationID string, threadID string) {
	conversationID = strings.TrimSpace(conversationID)
	threadID = strings.TrimSpace(threadID)
	if conversationID == "" && threadID == "" {
		return
	}
	s.mu.Lock()
	if s.ResponseStore != nil {
		s.ResponseStore.deleteByConversationOrThread(conversationID, threadID)
	}
	sqliteWriter := s.sqliteWriter
	store := s.Store
	storeEnabled := store != nil && responsesPersistenceEnabled(s.Config)
	s.mu.Unlock()
	if storeEnabled {
		if sqliteWriter != nil {
			sqliteWriter.EnqueueDeleteResponsesByConversationOrThread(conversationID, threadID)
			return
		}
		if err := store.DeleteResponsesByConversationOrThread(conversationID, threadID); err != nil {
			log.Printf("[sqlite] delete responses conversation=%s thread=%s failed: %v", conversationID, threadID, err)
		}
	}
}

func (a *App) beginConversation(preferredConversationID string, source string, transport string, displayPrompt string, request PromptRunRequest) string {
	entry := a.State.conversations().Create(ConversationCreateRequest{
		PreferredID:      preferredConversationID,
		Ephemeral:        request.EphemeralConversation,
		EphemeralReason:  request.EphemeralReason,
		AutoDeleteAt:     request.EphemeralDeleteAfter,
		Source:           source,
		Transport:        transport,
		Model:            request.PublicModel,
		NotionModel:      request.NotionModel,
		Prompt:           displayPrompt,
		UseWebSearch:     request.UseWebSearch,
		InputAttachments: summarizeInputAttachments(request.Attachments),
	})
	a.State.persistConversationSnapshot(entry.ID)
	return entry.ID
}

func (a *App) continueConversation(conversationID string, source string, transport string, displayPrompt string, request PromptRunRequest) (string, error) {
	entry, err := a.State.conversations().Continue(conversationID, ConversationCreateRequest{
		Ephemeral:        request.EphemeralConversation,
		EphemeralReason:  request.EphemeralReason,
		AutoDeleteAt:     request.EphemeralDeleteAfter,
		Source:           source,
		Transport:        transport,
		Model:            request.PublicModel,
		NotionModel:      request.NotionModel,
		Prompt:           displayPrompt,
		UseWebSearch:     request.UseWebSearch,
		InputAttachments: summarizeInputAttachments(request.Attachments),
	})
	if err != nil {
		return "", err
	}
	a.State.persistConversationSnapshot(entry.ID)
	return entry.ID, nil
}

func (a *App) markConversationEnvelope(conversationID string, responseID string, completionID string) {
	if conversationID == "" {
		return
	}
	a.State.conversations().SetEnvelopeIDs(conversationID, responseID, completionID)
	a.State.persistConversationSnapshot(conversationID)
}

func (a *App) pushConversationDelta(conversationID string, delta string) {
	if conversationID == "" {
		return
	}
	a.State.conversations().AppendAssistantDelta(conversationID, delta)
	a.State.persistConversationSnapshot(conversationID)
}

func (a *App) completeConversation(conversationID string, result InferenceResult) {
	if conversationID == "" {
		return
	}
	a.State.conversations().Complete(conversationID, result)
	a.State.persistConversationSnapshot(conversationID)
}

func (a *App) persistConversationSession(conversationID string, request PromptRunRequest, result InferenceResult) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" || request.SuppressUpstreamThreadPersistence || strings.TrimSpace(result.ThreadID) == "" {
		return
	}
	a.State.mu.RLock()
	store := a.State.Store
	storeEnabled := store != nil && continuationSessionsPersistenceEnabled(a.State.Config)
	a.State.mu.RUnlock()
	if !storeEnabled {
		return
	}
	now := time.Now().UTC()
	sessionID := ""
	turnCount := 1
	appendStep := true
	if request.continuationDraft != nil && strings.TrimSpace(request.continuationDraft.SessionID) != "" {
		sessionID = strings.TrimSpace(request.continuationDraft.SessionID)
		if request.SessionRepeatTurn {
			turnCount = maxInt(request.continuationDraft.TurnCount, 1)
			appendStep = false
		} else {
			turnCount = maxInt(request.continuationDraft.TurnCount+1, 1)
		}
	} else {
		sessionID = "sess_" + strings.ReplaceAll(randomUUID(), "-", "")
	}
	session := ConversationSession{
		ID:               sessionID,
		ConversationID:   conversationID,
		Fingerprint:      strings.TrimSpace(request.SessionFingerprint),
		ThreadID:         strings.TrimSpace(result.ThreadID),
		AccountEmail:     strings.TrimSpace(result.AccountEmail),
		ConfigID:         strings.TrimSpace(result.ConfigID),
		ContextID:        strings.TrimSpace(result.ContextID),
		OriginalDatetime: strings.TrimSpace(result.OriginalDatetime),
		ModelUsed:        firstNonEmpty(strings.TrimSpace(result.NotionModel), strings.TrimSpace(request.NotionModel)),
		TurnCount:        turnCount,
		RawMessageCount:  maxInt(request.RawMessageCount, 0),
		Status:           conversationSessionStatusActive,
		CreatedAt:        now,
		UpdatedAt:        now,
		LastUsedAt:       now,
	}
	if request.continuationDraft != nil {
		if existing, ok, err := store.LoadConversationSessionByConversationID(conversationID); err == nil && ok {
			session.CreatedAt = existing.CreatedAt
			if session.ConfigID == "" {
				session.ConfigID = existing.ConfigID
			}
			if session.ContextID == "" {
				session.ContextID = existing.ContextID
			}
			if session.OriginalDatetime == "" {
				session.OriginalDatetime = existing.OriginalDatetime
			}
			if session.ThreadID == "" {
				session.ThreadID = existing.ThreadID
			}
			if session.AccountEmail == "" {
				session.AccountEmail = existing.AccountEmail
			}
		}
	}
	if session.ConfigID == "" {
		session.ConfigID = randomUUID()
	} else {
		session.ConfigID = normalizeTranscriptStepID(session.ConfigID)
	}
	if session.ContextID == "" {
		session.ContextID = randomUUID()
	} else {
		session.ContextID = normalizeTranscriptStepID(session.ContextID)
	}
	if session.OriginalDatetime == "" {
		session.OriginalDatetime = isoNowMillis()
	}
	if existing, ok, err := store.LoadConversationSessionByConversationID(conversationID); err == nil && ok && strings.TrimSpace(existing.ID) != "" && existing.ID != session.ID {
		if markErr := store.MarkConversationSessionStatus(existing.ID, conversationSessionStatusStale); markErr != nil {
			log.Printf("[sqlite] stale previous continuation session conversation=%s existing=%s failed: %v", conversationID, existing.ID, markErr)
		}
	}
	if err := store.SaveConversationSession(session); err != nil {
		log.Printf("[sqlite] save continuation session conversation=%s failed: %v", conversationID, err)
		return
	}
	if !appendStep {
		return
	}
	stepIndex := turnCount - 1
	updatedConfigID := randomUUID()
	if request.continuationScaffold != nil && strings.TrimSpace(request.continuationScaffold.UpdatedConfigID) != "" {
		updatedConfigID = strings.TrimSpace(request.continuationScaffold.UpdatedConfigID)
	}
	step := ConversationSessionStep{
		SessionID:       session.ID,
		StepIndex:       stepIndex,
		UpdatedConfigID: updatedConfigID,
		ResponseID:      "",
		MessageID:       strings.TrimSpace(result.MessageID),
		CreatedAt:       now,
	}
	if entry, ok := a.State.conversations().Get(conversationID); ok {
		step.ResponseID = strings.TrimSpace(entry.ResponseID)
	}
	if err := store.SaveConversationSessionStep(step); err != nil {
		log.Printf("[sqlite] save continuation session step conversation=%s failed: %v", conversationID, err)
	}
}

func (a *App) failConversation(conversationID string, err error) {
	if conversationID == "" || err == nil {
		return
	}
	a.State.conversations().Fail(conversationID, err)
	a.State.persistConversationSnapshot(conversationID)
}

func (a *App) notionClientForAccount(ctx context.Context, accountEmail string) (*NotionAIClient, error) {
	cfg, snapshot, _ := a.State.Snapshot()
	a.State.mu.RLock()
	fallbackClient := a.State.Client
	a.State.mu.RUnlock()
	if email := strings.TrimSpace(accountEmail); email != "" {
		if account, _, found := cfg.FindAccount(email); found {
			account = ensureAccountPaths(cfg, account)
			session, err := loadSessionInfoForAccountRefresh(cfg, account)
			if err != nil {
				if fallbackClient != nil {
					return fallbackClient, nil
				}
				return nil, fmt.Errorf("load account session for %s: %w", email, err)
			}
			return newNotionAIClient(session, cfg, email), nil
		}
	}
	if fallbackClient != nil {
		return fallbackClient, nil
	}
	session, err := a.loadPrimarySession(ctx, cfg, snapshot, "admin_conversation_sync")
	if err != nil {
		return nil, err
	}
	return newNotionAIClient(session, cfg, ""), nil
}

func (a *App) deleteConversation(conversationID string) error {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return fmt.Errorf("conversation id is required")
	}
	entry, ok := a.State.conversations().Get(conversationID)
	if !ok {
		return fmt.Errorf("conversation not found")
	}
	if strings.EqualFold(strings.TrimSpace(entry.Status), "running") {
		return fmt.Errorf("conversation is still running")
	}
	if threadID := strings.TrimSpace(entry.ThreadID); threadID != "" {
		cfg, _, _ := a.State.Snapshot()
		timeout := time.Duration(maxInt(cfg.TimeoutSec, 10)) * time.Second
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		client, err := a.notionClientForAccount(ctx, entry.AccountEmail)
		if err != nil {
			return err
		}
		if err := client.deleteThread(ctx, threadID); err != nil {
			return err
		}
	}
	if err := a.State.conversations().Delete(conversationID); err != nil {
		return err
	}
	a.State.deleteResponsesByConversationOrThread(conversationID, entry.ThreadID)
	a.State.deleteConversationSessionByConversationOrThread(conversationID, entry.ThreadID)
	a.State.mu.RLock()
	store := a.State.Store
	a.State.mu.RUnlock()
	if store != nil {
		if err := store.DeleteConversation(conversationID); err != nil {
			return err
		}
	}
	a.State.deleteSillyTavernBinding(conversationID)
	return nil
}
