package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newFreshThreadTestApp(t *testing.T) *App {
	t.Helper()
	cfg := defaultConfig()
	cfg.APIKey = "test-api-key"
	cfg.Storage.SQLitePath = ""
	cfg.Features.ForceFreshThreadPerRequest = true
	state, err := newServerState(cfg)
	if err != nil {
		t.Fatalf("newServerState failed: %v", err)
	}
	t.Cleanup(func() {
		_ = state.Close()
	})
	return &App{State: state}
}

func seedCompletedConversation(t *testing.T, app *App, conversationID string, userText string, assistantText string, threadID string) ConversationEntry {
	t.Helper()
	entry := app.State.conversations().Create(ConversationCreateRequest{
		PreferredID: conversationID,
		Source:      "api",
		Transport:   "chat_completions",
		Model:       "gpt-5.4",
		NotionModel: "oval-kumquat-medium",
		Prompt:      userText,
	})
	app.State.conversations().Complete(entry.ID, InferenceResult{
		Text:         assistantText,
		ThreadID:     threadID,
		AccountEmail: "seed@example.com",
	})
	seeded, ok := app.State.conversations().Get(entry.ID)
	if !ok {
		t.Fatalf("conversation %s not found after seed", entry.ID)
	}
	return seeded
}

func mustJSONBody(t *testing.T, payload map[string]any) *bytes.Reader {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload failed: %v", err)
	}
	return bytes.NewReader(body)
}

func assertPromptContains(t *testing.T, prompt string, parts ...string) {
	t.Helper()
	for _, part := range parts {
		if !strings.Contains(prompt, part) {
			t.Fatalf("prompt missing %q\nfull prompt:\n%s", part, prompt)
		}
	}
}

func assertConversationContinued(t *testing.T, app *App, conversationID string, expectedThreadID string, expectedAssistant string) {
	t.Helper()
	entry, ok := app.State.conversations().Get(conversationID)
	if !ok {
		t.Fatalf("conversation %s missing", conversationID)
	}
	if entry.ThreadID != expectedThreadID {
		t.Fatalf("thread mismatch: got %q want %q", entry.ThreadID, expectedThreadID)
	}
	if len(entry.Messages) < 4 {
		t.Fatalf("expected continued conversation to have at least 4 messages, got %d", len(entry.Messages))
	}
	if got := strings.TrimSpace(entry.Messages[len(entry.Messages)-1].Content); got != expectedAssistant {
		t.Fatalf("assistant message mismatch: got %q want %q", got, expectedAssistant)
	}
}

func TestHandleChatCompletionsFreshThreadReplaysLocalConversation(t *testing.T) {
	app := newFreshThreadTestApp(t)
	seeded := seedCompletedConversation(t, app, "conv-chat", "Hello", "Hi there", "thread-old-chat")

	var captured PromptRunRequest
	app.runPromptOverride = func(_ *http.Request, request PromptRunRequest) (InferenceResult, error) {
		captured = request
		return InferenceResult{
			Text:         "Doing well.",
			ThreadID:     "thread-new-chat",
			MessageID:    "msg-new-chat",
			TraceID:      "trace-new-chat",
			AccountEmail: "seed@example.com",
		}, nil
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", mustJSONBody(t, map[string]any{
		"model":           "gpt-5.4",
		"conversation_id": seeded.ID,
		"messages": []map[string]any{
			{"role": "user", "content": "How are you?"},
		},
	}))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-api-key")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status mismatch: got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Conversation-ID"); got != seeded.ID {
		t.Fatalf("conversation header mismatch: got %q want %q", got, seeded.ID)
	}
	if captured.UpstreamThreadID != "" {
		t.Fatalf("expected empty upstream thread id, got %q", captured.UpstreamThreadID)
	}
	if captured.continuationDraft != nil {
		t.Fatalf("expected no continuation draft in fresh-thread mode")
	}
	if !captured.ForceLocalConversationContinue {
		t.Fatalf("expected ForceLocalConversationContinue to be enabled")
	}
	if strings.TrimSpace(captured.Prompt) == "How are you?" {
		t.Fatalf("expected replay prompt, got latest prompt only: %q", captured.Prompt)
	}
	assertPromptContains(t, captured.Prompt,
		"Continue the conversation using the transcript below.",
		"[user]\nHello",
		"[assistant]\nHi there",
		"[user]\nHow are you?",
	)
	assertConversationContinued(t, app, seeded.ID, "thread-new-chat", "Doing well.")
}

func TestHandleResponsesFreshThreadReplaysLocalConversation(t *testing.T) {
	app := newFreshThreadTestApp(t)
	seeded := seedCompletedConversation(t, app, "conv-responses", "Please remember this.", "Remembered.", "thread-old-responses")

	var captured PromptRunRequest
	app.runPromptOverride = func(_ *http.Request, request PromptRunRequest) (InferenceResult, error) {
		captured = request
		return InferenceResult{
			Text:         "Summary ready.",
			ThreadID:     "thread-new-responses",
			MessageID:    "msg-new-responses",
			TraceID:      "trace-new-responses",
			AccountEmail: "seed@example.com",
		}, nil
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", mustJSONBody(t, map[string]any{
		"model":           "gpt-5.4",
		"conversation_id": seeded.ID,
		"input":           "Summarize that.",
	}))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-api-key")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status mismatch: got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Conversation-ID"); got != seeded.ID {
		t.Fatalf("conversation header mismatch: got %q want %q", got, seeded.ID)
	}
	if captured.UpstreamThreadID != "" {
		t.Fatalf("expected empty upstream thread id, got %q", captured.UpstreamThreadID)
	}
	if captured.continuationDraft != nil {
		t.Fatalf("expected no continuation draft in fresh-thread mode")
	}
	if !captured.ForceLocalConversationContinue {
		t.Fatalf("expected ForceLocalConversationContinue to be enabled")
	}
	assertPromptContains(t, captured.Prompt,
		"Continue the conversation using the transcript below.",
		"[user]\nPlease remember this.",
		"[assistant]\nRemembered.",
		"[user]\nSummarize that.",
	)
	assertConversationContinued(t, app, seeded.ID, "thread-new-responses", "Summary ready.")
}

func TestHandleSillyTavernFreshThreadReplaysLocalConversation(t *testing.T) {
	app := newFreshThreadTestApp(t)
	seeded := seedCompletedConversation(t, app, "conv-st", "Tell a story.", "Once upon a time.", "thread-old-st")

	var captured PromptRunRequest
	app.runPromptOverride = func(_ *http.Request, request PromptRunRequest) (InferenceResult, error) {
		captured = request
		return InferenceResult{
			Text:         "The story continues.",
			ThreadID:     "thread-new-st",
			MessageID:    "msg-new-st",
			TraceID:      "trace-new-st",
			AccountEmail: "seed@example.com",
		}, nil
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", mustJSONBody(t, map[string]any{
		"model":           "gpt-5.4",
		"type":            "continue",
		"conversation_id": seeded.ID,
		"messages": []map[string]any{
			{"role": "user", "content": sillyTavernContinuePrompt},
		},
	}))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-api-key")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status mismatch: got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Conversation-ID"); got != seeded.ID {
		t.Fatalf("conversation header mismatch: got %q want %q", got, seeded.ID)
	}
	if captured.UpstreamThreadID != "" {
		t.Fatalf("expected empty upstream thread id, got %q", captured.UpstreamThreadID)
	}
	if captured.continuationDraft != nil {
		t.Fatalf("expected no continuation draft in fresh-thread mode")
	}
	if !captured.ForceLocalConversationContinue {
		t.Fatalf("expected ForceLocalConversationContinue to be enabled")
	}
	assertPromptContains(t, captured.Prompt,
		"Continue the conversation using the transcript below.",
		"[user]\nTell a story.",
		"[assistant]\nOnce upon a time.",
		"[user]\n"+sillyTavernContinuePrompt,
	)
	assertConversationContinued(t, app, seeded.ID, "thread-new-st", "The story continues.")
}

func TestHandleChatCompletionsFreshThreadContinuesExplicitConversationIDWithLatestUserOnly(t *testing.T) {
	app := newFreshThreadTestApp(t)

	callCount := 0
	var secondRequest PromptRunRequest
	app.runPromptOverride = func(_ *http.Request, request PromptRunRequest) (InferenceResult, error) {
		callCount++
		switch callCount {
		case 1:
			return InferenceResult{
				Text:         "我会先扶你躺好，再慢慢安抚你。",
				ThreadID:     "thread-first-turn",
				MessageID:    "msg-first-turn",
				TraceID:      "trace-first-turn",
				AccountEmail: "seed@example.com",
			}, nil
		case 2:
			secondRequest = request
			return InferenceResult{
				Text:         "把药茶递到你手里时，我会轻声让你慢点喝。",
				ThreadID:     "thread-second-turn",
				MessageID:    "msg-second-turn",
				TraceID:      "trace-second-turn",
				AccountEmail: "seed@example.com",
			}, nil
		default:
			t.Fatalf("unexpected extra runPrompt call %d", callCount)
			return InferenceResult{}, nil
		}
	}

	firstReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", mustJSONBody(t, map[string]any{
		"model": "gpt-5.4",
		"messages": []map[string]any{
			{"role": "system", "content": "Stay in character."},
			{"role": "assistant", "content": "终于醒了，小召唤师。"},
			{"role": "user", "content": "我有点头晕。你会怎么安抚我？"},
		},
	}))
	firstReq.Header.Set("Content-Type", "application/json")
	firstReq.Header.Set("Authorization", "Bearer test-api-key")
	firstRec := httptest.NewRecorder()
	app.ServeHTTP(firstRec, firstReq)

	if firstRec.Code != http.StatusOK {
		t.Fatalf("first request status mismatch: got %d body=%s", firstRec.Code, firstRec.Body.String())
	}
	conversationID := strings.TrimSpace(firstRec.Header().Get("X-Conversation-ID"))
	if conversationID == "" {
		t.Fatalf("expected first request to return conversation id")
	}

	secondReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", mustJSONBody(t, map[string]any{
		"model":           "gpt-5.4",
		"conversation_id": conversationID,
		"messages": []map[string]any{
			{"role": "user", "content": "那你把药茶递给我时，会怎么说？"},
		},
	}))
	secondReq.Header.Set("Content-Type", "application/json")
	secondReq.Header.Set("Authorization", "Bearer test-api-key")
	secondRec := httptest.NewRecorder()
	app.ServeHTTP(secondRec, secondReq)

	if secondRec.Code != http.StatusOK {
		t.Fatalf("second request status mismatch: got %d body=%s", secondRec.Code, secondRec.Body.String())
	}
	if got := secondRec.Header().Get("X-Conversation-ID"); got != conversationID {
		t.Fatalf("second conversation header mismatch: got %q want %q", got, conversationID)
	}
	if got := secondRec.Header().Get("X-Notion-Thread-ID"); got != "thread-second-turn" {
		t.Fatalf("second thread header mismatch: got %q want %q", got, "thread-second-turn")
	}
	if !secondRequest.ForceLocalConversationContinue {
		t.Fatalf("expected second request to continue local conversation")
	}
	if secondRequest.UpstreamThreadID != "" {
		t.Fatalf("expected fresh-thread mode to keep upstream thread empty, got %q", secondRequest.UpstreamThreadID)
	}
	assertPromptContains(t, secondRequest.Prompt,
		"Continue the conversation using the transcript below.",
		"[user]\n我有点头晕。你会怎么安抚我？",
		"[assistant]\n我会先扶你躺好，再慢慢安抚你。",
		"[user]\n那你把药茶递给我时，会怎么说？",
	)
	assertConversationContinued(t, app, conversationID, "thread-second-turn", "把药茶递到你手里时，我会轻声让你慢点喝。")
}

func TestNewServerStateRejectsEmptyAPIKey(t *testing.T) {
	cfg := defaultConfig()
	cfg.APIKey = ""
	cfg.Storage.SQLitePath = ""

	state, err := newServerState(cfg)
	if err == nil {
		if state != nil {
			_ = state.Close()
		}
		t.Fatalf("expected newServerState to reject empty API key")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "api key") {
		t.Fatalf("expected api key error, got %v", err)
	}
}

func TestServerStateSaveAndApplyRejectsEmptyAPIKey(t *testing.T) {
	cfg := defaultConfig()
	cfg.APIKey = "test-api-key"
	cfg.Storage.SQLitePath = ""

	state, err := newServerState(cfg)
	if err != nil {
		t.Fatalf("newServerState failed: %v", err)
	}
	defer func() {
		_ = state.Close()
	}()

	next := cfg
	next.APIKey = ""
	if err := state.SaveAndApply(next); err == nil {
		t.Fatalf("expected SaveAndApply to reject empty API key")
	}
}

func TestHandleChatCompletionsStreamWritesErrorAfterHeadersSent(t *testing.T) {
	app := newFreshThreadTestApp(t)
	app.runPromptStreamSinkOverride = func(_ *http.Request, _ PromptRunRequest, sink InferenceStreamSink) (InferenceResult, error) {
		if sink.KeepAlive != nil {
			if err := sink.KeepAlive(); err != nil {
				t.Fatalf("keepalive failed: %v", err)
			}
		}
		return InferenceResult{}, fmt.Errorf("upstream exploded")
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", mustJSONBody(t, map[string]any{
		"model":  "gpt-5.4",
		"stream": true,
		"messages": []map[string]any{
			{"role": "user", "content": "hello"},
		},
	}))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-api-key")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "\"message\":\"upstream exploded\"") || !strings.Contains(body, "\"code\":\"upstream_error\"") {
		t.Fatalf("expected stream error payload, got body=%s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("expected stream done marker, got body=%s", body)
	}
}

func TestNormalizeConfigDefaultsAccountMaxConcurrencyToOne(t *testing.T) {
	cfg := normalizeConfig(AppConfig{
		APIKey: "test-api-key",
		Accounts: []NotionAccount{
			{Email: "alice@example.com", MaxConcurrency: 0},
			{Email: "bob@example.com", MaxConcurrency: -3},
			{Email: "carol@example.com", MaxConcurrency: 4},
		},
	})
	if got := cfg.Accounts[0].MaxConcurrency; got != 1 {
		t.Fatalf("expected default max concurrency 1 for zero, got %d", got)
	}
	if got := cfg.Accounts[1].MaxConcurrency; got != 1 {
		t.Fatalf("expected default max concurrency 1 for negative, got %d", got)
	}
	if got := cfg.Accounts[2].MaxConcurrency; got != 4 {
		t.Fatalf("expected explicit max concurrency preserved, got %d", got)
	}
}

func TestWriteUpstreamErrorMapsDispatchCapacityTo429(t *testing.T) {
	cfg := defaultConfig()
	cfg.APIKey = "test-api-key"
	cfg.Storage.SQLitePath = ""
	state, err := newServerState(cfg)
	if err != nil {
		t.Fatalf("newServerState failed: %v", err)
	}
	defer func() {
		_ = state.Close()
	}()
	app := &App{State: state}

	rec := httptest.NewRecorder()
	app.writeUpstreamError(rec, noDispatchCapacityError())

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected status 429, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"rate_limit_error"`) {
		t.Fatalf("expected rate_limit_error body, got %s", body)
	}
	if !strings.Contains(body, `"code":"dispatch_capacity_exceeded"`) {
		t.Fatalf("expected dispatch_capacity_exceeded code, got %s", body)
	}
}

func TestRunPromptWithAccountPoolReturnsCapacityErrorWhenAllSlotsOccupied(t *testing.T) {
	probePath := filepath.Join(t.TempDir(), "probe.json")
	if err := os.WriteFile(probePath, []byte(`{"cookies":[{"name":"token_v2","value":"test-cookie"}]}`), 0o644); err != nil {
		t.Fatalf("write probe file failed: %v", err)
	}

	cfg := normalizeConfig(AppConfig{
		APIKey: "test-api-key",
		Accounts: []NotionAccount{
			{Email: "alice@example.com", MaxConcurrency: 1, ProbeJSON: probePath},
		},
	})
	state, err := newServerState(cfg)
	if err != nil {
		t.Fatalf("newServerState failed: %v", err)
	}
	defer func() {
		_ = state.Close()
	}()

	if !state.TryAcquireAccountDispatchSlot("alice@example.com") {
		t.Fatal("expected pre-acquire slot success")
	}
	defer state.ReleaseAccountDispatchSlot("alice@example.com")

	app := &App{State: state}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	_, runErr := app.runPromptWithAccountPool(req, PromptRunRequest{Prompt: "hello"}, nil)
	if runErr == nil {
		t.Fatal("expected dispatch capacity error, got nil")
	}
	if !isDispatchCapacityExceededError(runErr) {
		t.Fatalf("expected dispatch capacity sentinel error, got %v", runErr)
	}
	if !errors.Is(runErr, errDispatchCapacityExceeded) {
		t.Fatalf("expected wrapped sentinel error, got %v", runErr)
	}
}
