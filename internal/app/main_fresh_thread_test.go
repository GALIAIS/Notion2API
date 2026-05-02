package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"expvar"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
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

func TestNormalizeConfigSetsPprofDefaults(t *testing.T) {
	cfg := normalizeConfig(AppConfig{})
	if cfg.Debug.PprofEnabled {
		t.Fatalf("expected pprof disabled by default")
	}
	if cfg.Debug.PprofAddr != "127.0.0.1:6060" {
		t.Fatalf("unexpected default pprof addr: %q", cfg.Debug.PprofAddr)
	}
}

func TestDefaultConfigSetsDispatchProbeCacheTTLDefault(t *testing.T) {
	cfg := defaultConfig()
	if cfg.Dispatch.ProbeCacheTTLSeconds != 45 {
		t.Fatalf("unexpected default dispatch probe cache ttl: %d", cfg.Dispatch.ProbeCacheTTLSeconds)
	}
}

func TestNormalizeConfigClampsNegativeDispatchProbeCacheTTL(t *testing.T) {
	cfg := normalizeConfig(AppConfig{
		Dispatch: DispatchConfig{ProbeCacheTTLSeconds: -3},
	})
	if cfg.Dispatch.ProbeCacheTTLSeconds != 0 {
		t.Fatalf("expected negative dispatch probe cache ttl to clamp to 0, got %d", cfg.Dispatch.ProbeCacheTTLSeconds)
	}
}

func TestDefaultConfigBrowserHelperPoolSizeDefaultZero(t *testing.T) {
	cfg := defaultConfig()
	if got := cfg.Browser.HelperPoolSize; got != 0 {
		t.Fatalf("unexpected default browser helper pool size: got %d want %d", got, 0)
	}
}

func TestNormalizeConfigClampsBrowserHelperPoolSizeBounds(t *testing.T) {
	negative := normalizeConfig(AppConfig{
		Browser: BrowserConfig{HelperPoolSize: -2},
	})
	if got := negative.Browser.HelperPoolSize; got != 0 {
		t.Fatalf("expected negative helper pool size clamp to 0, got %d", got)
	}
	tooLarge := normalizeConfig(AppConfig{
		Browser: BrowserConfig{HelperPoolSize: 99},
	})
	if got := tooLarge.Browser.HelperPoolSize; got != 8 {
		t.Fatalf("expected oversized helper pool size clamp to 8, got %d", got)
	}
}

func TestEmbeddedBrowserHelperAssetsLoaded(t *testing.T) {
	helper := strings.TrimSpace(nodeWreqHelperScript())
	if helper == "" {
		t.Fatalf("expected embedded browser helper script to be non-empty")
	}
	for _, needle := range []string{
		"const { fetch } = require('node-wreq');",
		"process.stdout.write(JSON.stringify(result));",
	} {
		if !strings.Contains(helper, needle) {
			t.Fatalf("embedded browser helper script missing %q", needle)
		}
	}

	login := strings.TrimSpace(nodeWreqLoginHelperScript())
	if login == "" {
		t.Fatalf("expected embedded browser login helper script to be non-empty")
	}
	for _, needle := range []string{
		"const { fetch } = require('node-wreq');",
		"result.set_cookies = [...setCookieRecord.entries()]",
	} {
		if !strings.Contains(login, needle) {
			t.Fatalf("embedded browser login helper script missing %q", needle)
		}
	}
}

func TestNormalizeConfigPrecomputesRetryPrefixes(t *testing.T) {
	cfg := normalizeConfig(AppConfig{
		Prompt: PromptConfig{
			CodingRetryPrefixes:       []string{"custom-coding-prefix"},
			GeneralRetryPrefixes:      []string{"custom-general-prefix"},
			DirectAnswerRetryPrefixes: []string{"custom-direct-prefix"},
		},
	})
	if len(cfg.Prompt.precomputedAllRetryPrefixes) == 0 {
		t.Fatalf("expected precomputed retry prefixes")
	}
	joined := strings.Join(cfg.Prompt.precomputedAllRetryPrefixes, "\n")
	for _, required := range []string{
		"custom-coding-prefix",
		"custom-general-prefix",
		"custom-direct-prefix",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("precomputed retry prefixes missing %q", required)
		}
	}
}

func TestEnsureAccountPathsSetsEmailKey(t *testing.T) {
	cfg := normalizeConfig(AppConfig{
		LoginHelper: LoginHelperConfig{SessionsDir: "probe_files/notion_accounts"},
	})
	account := ensureAccountPaths(cfg, NotionAccount{Email: " Alice@Example.COM "})
	if account.emailKey != "alice@example.com" {
		t.Fatalf("unexpected cached email key: %q", account.emailKey)
	}
}

func BenchmarkPromptGuardLooksLikeCodingRequest(b *testing.B) {
	text := "Please help debug this golang function and refactor the docker deployment script."
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = promptGuardLooksLikeCodingRequest(text)
	}
}

func BenchmarkPromptGuardStripRetryPrefixes(b *testing.B) {
	cfg := normalizeConfig(AppConfig{
		Prompt: PromptConfig{
			CodingRetryPrefixes:       []string{"custom-coding-prefix"},
			GeneralRetryPrefixes:      []string{"custom-general-prefix"},
			DirectAnswerRetryPrefixes: []string{"custom-direct-prefix"},
		},
	})
	base := "this is a coding request body"
	input := cfg.Prompt.CodingRetryPrefixes[0] + cfg.Prompt.GeneralRetryPrefixes[0] + base
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = promptGuardStripRetryPrefixes(cfg, input)
	}
}

func BenchmarkServeModelsCaching(b *testing.B) {
	cfg := defaultConfig()
	cfg.APIKey = "bench-api-key"
	cfg.Storage.SQLitePath = ""
	state, err := newServerState(cfg)
	if err != nil {
		b.Fatalf("newServerState failed: %v", err)
	}
	defer func() {
		_ = state.Close()
	}()
	app := &App{State: state}
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer bench-api-key")

	b.Run("cached", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				b.Fatalf("unexpected status: got %d want %d", rec.Code, http.StatusOK)
			}
		}
	})

	b.Run("uncached_fallback", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			state.cachedModelsListJSON.Store(nil)
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				b.Fatalf("unexpected status: got %d want %d", rec.Code, http.StatusOK)
			}
		}
	})
}

func BenchmarkDecodeChatCompletionsTypedFirst(b *testing.B) {
	raw := []byte(`{
		"model":"gpt-5.4",
		"stream":true,
		"stream_options":{"include_usage":"1"},
		"messages":[
			{"role":"system","content":"You are helpful."},
			{"role":"user","content":"请总结这段文本并给出要点。"}
		],
		"metadata":{"use_web_search":false}
	}`)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		typed, payload, err := decodeChatCompletionsRequestBodyFromRaw(raw)
		if err != nil {
			b.Fatalf("decodeChatCompletionsRequestBodyFromRaw failed: %v", err)
		}
		if payload != nil {
			b.Fatalf("unexpected map fallback on typed benchmark path")
		}
		if len(sliceValue(typed.Messages)) == 0 {
			b.Fatalf("expected typed messages")
		}
	}
}

func BenchmarkDecodeChatCompletionsMapOnly(b *testing.B) {
	raw := []byte(`{
		"model":"gpt-5.4",
		"stream":true,
		"stream_options":{"include_usage":"1"},
		"messages":[
			{"role":"system","content":"You are helpful."},
			{"role":"user","content":"请总结这段文本并给出要点。"}
		],
		"metadata":{"use_web_search":false}
	}`)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		payload, err := decodeBodyMapFromRaw(raw)
		if err != nil {
			b.Fatalf("decodeBodyMapFromRaw failed: %v", err)
		}
		typed := extractChatCompletionsRequestBody(payload)
		if len(sliceValue(typed.Messages)) == 0 {
			b.Fatalf("expected map-extracted messages")
		}
	}
}

func BenchmarkNormalizeChatInputFromTypedMessages(b *testing.B) {
	raw := []byte(`{
		"messages":[
			{"role":"system","content":"You are helpful."},
			{"role":"user","content":[{"type":"text","text":"hello"},{"type":"text","text":"world"}]}
		],
		"attachments":[{"type":"image_url","url":"https://example.com/a.png"}]
	}`)
	typed, _, err := decodeChatCompletionsRequestBodyFromRaw(raw)
	if err != nil {
		b.Fatalf("decodeChatCompletionsRequestBodyFromRaw failed: %v", err)
	}
	messages := sliceValue(typed.Messages)
	if len(messages) == 0 {
		b.Fatalf("expected typed messages")
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		normalized, err := normalizeChatInputFromParts(messages, typed.Attachments)
		if err != nil {
			b.Fatalf("normalizeChatInputFromParts failed: %v", err)
		}
		if normalized.Prompt == "" {
			b.Fatalf("expected normalized prompt")
		}
	}
}

func BenchmarkNormalizeChatInputFromMapMessages(b *testing.B) {
	raw := []byte(`{
		"messages":[
			{"role":"system","content":"You are helpful."},
			{"role":"user","content":[{"type":"text","text":"hello"},{"type":"text","text":"world"}]}
		],
		"attachments":[{"type":"image_url","url":"https://example.com/a.png"}]
	}`)
	payload, err := decodeBodyMapFromRaw(raw)
	if err != nil {
		b.Fatalf("decodeBodyMapFromRaw failed: %v", err)
	}
	messages := sliceValue(payload["messages"])
	if len(messages) == 0 {
		b.Fatalf("expected map messages")
	}
	attachments := payload["attachments"]
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		normalized, err := normalizeChatInputFromParts(messages, attachments)
		if err != nil {
			b.Fatalf("normalizeChatInputFromParts failed: %v", err)
		}
		if normalized.Prompt == "" {
			b.Fatalf("expected normalized prompt")
		}
	}
}

func BenchmarkDecodeResponsesTypedFirst(b *testing.B) {
	raw := []byte(`{
		"model":"gpt-5.4",
		"stream":false,
		"previous_response_id":"resp_123",
		"input":[
			{"type":"input_text","text":"hello"},
			{"type":"input_text","text":"world"}
		],
		"metadata":{"use_web_search":"1"},
		"attachments":[{"type":"file","file_url":"https://example.com/f.txt"}]
	}`)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		typed, payload, err := decodeResponsesRequestBodyFromRaw(raw)
		if err != nil {
			b.Fatalf("decodeResponsesRequestBodyFromRaw failed: %v", err)
		}
		if payload != nil {
			b.Fatalf("unexpected map fallback on typed benchmark path")
		}
		if len(sliceValue(typed.Input)) == 0 {
			b.Fatalf("expected typed input items")
		}
	}
}

func BenchmarkDecodeResponsesMapOnly(b *testing.B) {
	raw := []byte(`{
		"model":"gpt-5.4",
		"stream":false,
		"previous_response_id":"resp_123",
		"input":[
			{"type":"input_text","text":"hello"},
			{"type":"input_text","text":"world"}
		],
		"metadata":{"use_web_search":"1"},
		"attachments":[{"type":"file","file_url":"https://example.com/f.txt"}]
	}`)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		payload, err := decodeBodyMapFromRaw(raw)
		if err != nil {
			b.Fatalf("decodeBodyMapFromRaw failed: %v", err)
		}
		typed := extractResponsesRequestBody(payload)
		if len(sliceValue(typed.Input)) == 0 {
			b.Fatalf("expected map-extracted responses input")
		}
	}
}

func BenchmarkNormalizeResponsesInputFromTyped(b *testing.B) {
	raw := []byte(`{
		"input":[
			{"type":"input_text","text":"hello"},
			{"type":"input_text","text":"world"}
		],
		"attachments":[{"type":"file","file_url":"https://example.com/f.txt"}]
	}`)
	typed, _, err := decodeResponsesRequestBodyFromRaw(raw)
	if err != nil {
		b.Fatalf("decodeResponsesRequestBodyFromRaw failed: %v", err)
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		normalized, err := normalizeResponsesInputFromParts(typed.Input, typed.Attachments, nil)
		if err != nil {
			b.Fatalf("normalizeResponsesInputFromParts failed: %v", err)
		}
		if normalized.Prompt == "" {
			b.Fatalf("expected normalized prompt")
		}
	}
}

func BenchmarkNormalizeResponsesInputFromMap(b *testing.B) {
	raw := []byte(`{
		"input":[
			{"type":"input_text","text":"hello"},
			{"type":"input_text","text":"world"}
		],
		"attachments":[{"type":"file","file_url":"https://example.com/f.txt"}]
	}`)
	payload, err := decodeBodyMapFromRaw(raw)
	if err != nil {
		b.Fatalf("decodeBodyMapFromRaw failed: %v", err)
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		normalized, err := normalizeResponsesInputFromParts(payload["input"], payload["attachments"], nil)
		if err != nil {
			b.Fatalf("normalizeResponsesInputFromParts failed: %v", err)
		}
		if normalized.Prompt == "" {
			b.Fatalf("expected normalized prompt")
		}
	}
}

func BenchmarkChatDecodeAndNormalizeTypedFirst(b *testing.B) {
	raw := []byte(`{
		"model":"gpt-5.4",
		"stream":false,
		"messages":[
			{"role":"system","content":"You are helpful."},
			{"role":"user","content":[{"type":"text","text":"hello"},{"type":"text","text":"world"}]}
		],
		"attachments":[{"type":"image_url","url":"https://example.com/a.png"}]
	}`)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		typed, payload, err := decodeChatCompletionsRequestBodyFromRaw(raw)
		if err != nil {
			b.Fatalf("decodeChatCompletionsRequestBodyFromRaw failed: %v", err)
		}
		if payload != nil {
			b.Fatalf("unexpected map fallback on typed benchmark path")
		}
		normalized, err := normalizeChatInputFromParts(sliceValue(typed.Messages), typed.Attachments)
		if err != nil {
			b.Fatalf("normalizeChatInputFromParts failed: %v", err)
		}
		if normalized.Prompt == "" {
			b.Fatalf("expected normalized prompt")
		}
	}
}

func BenchmarkChatDecodeAndNormalizeMapOnly(b *testing.B) {
	raw := []byte(`{
		"model":"gpt-5.4",
		"stream":false,
		"messages":[
			{"role":"system","content":"You are helpful."},
			{"role":"user","content":[{"type":"text","text":"hello"},{"type":"text","text":"world"}]}
		],
		"attachments":[{"type":"image_url","url":"https://example.com/a.png"}]
	}`)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		payload, err := decodeBodyMapFromRaw(raw)
		if err != nil {
			b.Fatalf("decodeBodyMapFromRaw failed: %v", err)
		}
		typed := extractChatCompletionsRequestBody(payload)
		normalized, err := normalizeChatInputFromParts(sliceValue(typed.Messages), typed.Attachments)
		if err != nil {
			b.Fatalf("normalizeChatInputFromParts failed: %v", err)
		}
		if normalized.Prompt == "" {
			b.Fatalf("expected normalized prompt")
		}
	}
}

func BenchmarkResponsesDecodeAndNormalizeTypedFirst(b *testing.B) {
	raw := []byte(`{
		"model":"gpt-5.4",
		"input":[
			{"type":"input_text","text":"hello"},
			{"type":"input_text","text":"world"}
		],
		"attachments":[{"type":"file","file_url":"https://example.com/f.txt"}]
	}`)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		typed, payload, err := decodeResponsesRequestBodyFromRaw(raw)
		if err != nil {
			b.Fatalf("decodeResponsesRequestBodyFromRaw failed: %v", err)
		}
		if payload != nil {
			b.Fatalf("unexpected map fallback on typed benchmark path")
		}
		normalized, err := normalizeResponsesInputFromParts(typed.Input, typed.Attachments, nil)
		if err != nil {
			b.Fatalf("normalizeResponsesInputFromParts failed: %v", err)
		}
		if normalized.Prompt == "" {
			b.Fatalf("expected normalized prompt")
		}
	}
}

func BenchmarkResponsesDecodeAndNormalizeMapOnly(b *testing.B) {
	raw := []byte(`{
		"model":"gpt-5.4",
		"input":[
			{"type":"input_text","text":"hello"},
			{"type":"input_text","text":"world"}
		],
		"attachments":[{"type":"file","file_url":"https://example.com/f.txt"}]
	}`)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		payload, err := decodeBodyMapFromRaw(raw)
		if err != nil {
			b.Fatalf("decodeBodyMapFromRaw failed: %v", err)
		}
		typed := extractResponsesRequestBody(payload)
		normalized, err := normalizeResponsesInputFromParts(typed.Input, typed.Attachments, nil)
		if err != nil {
			b.Fatalf("normalizeResponsesInputFromParts failed: %v", err)
		}
		if normalized.Prompt == "" {
			b.Fatalf("expected normalized prompt")
		}
	}
}

func TestServeModelsUsesStaticJSONCache(t *testing.T) {
	app := newFreshThreadTestApp(t)
	raw := []byte(`{"object":"list","data":[{"id":"cached-model","object":"model"}]}`)
	ready := append([]byte(nil), raw...)
	app.State.cachedModelsListJSON.Store(&ready)
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-api-key")
	rec := httptest.NewRecorder()

	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: got %d want %d", rec.Code, http.StatusOK)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != string(raw) {
		t.Fatalf("expected cached body, got %s", got)
	}
}

func TestServeModelByIDUsesStaticJSONCache(t *testing.T) {
	app := newFreshThreadTestApp(t)
	_, _, registry := app.State.Snapshot()
	entry, err := registry.Resolve("gpt-5.4", "auto")
	if err != nil {
		t.Fatalf("resolve model failed: %v", err)
	}
	body := []byte(`{"id":"gpt-5.4","object":"model","cached":true}`)
	cache := map[string][]byte{
		normalizeLookupKey(entry.ID): append([]byte(nil), body...),
	}
	app.State.cachedModelByIDJSON.Store(&cache)
	req := httptest.NewRequest(http.MethodGet, "/v1/models/"+entry.ID, nil)
	req.Header.Set("Authorization", "Bearer test-api-key")
	rec := httptest.NewRecorder()

	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: got %d want %d", rec.Code, http.StatusOK)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != string(body) {
		t.Fatalf("expected cached body, got %s", got)
	}
}

func TestServeHealthzIncludesRefreshRuntimeFieldsWhenStaticCacheExists(t *testing.T) {
	app := newFreshThreadTestApp(t)
	static := []byte(`{"ok":true,"default_model":"gpt-5.4","model_count":3,"user_email":"user@example.com","space_id":"space-id","active_account":"acc@example.com","session_refresh_enabled":true}`)
	staticCopy := append([]byte(nil), static...)
	app.State.cachedHealthzStaticJSON.Store(&staticCopy)
	app.State.mu.Lock()
	app.State.LastSessionRefresh = time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	app.State.LastSessionRefreshError = "refresh failed"
	app.State.mu.Unlock()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: got %d want %d", rec.Code, http.StatusOK)
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal healthz failed: %v", err)
	}
	if got, _ := payload["default_model"].(string); got != "gpt-5.4" {
		t.Fatalf("unexpected default_model: %q", got)
	}
	if got, ok := payload["session_ready"].(bool); !ok || got {
		t.Fatalf("unexpected session_ready: %#v", payload["session_ready"])
	}
	if got, _ := payload["last_session_refresh"].(string); got != "2026-01-02T03:04:05Z" {
		t.Fatalf("unexpected last_session_refresh: %q", got)
	}
	if got, _ := payload["last_session_refresh_error"].(string); got != "refresh failed" {
		t.Fatalf("unexpected last_session_refresh_error: %q", got)
	}
}

func TestServeHTTPDebugVarsExposesWreqClientMetric(t *testing.T) {
	app := newFreshThreadTestApp(t)
	before := int64(0)
	if value := wreqClientNewTotalMetric.Get("standard"); value != nil {
		before = value.(*expvar.Int).Value()
	}
	wreqClientNewTotalMetric.Add("standard", 1)
	req := httptest.NewRequest(http.MethodGet, "/debug/vars", nil)
	req.Header.Set("Authorization", "Bearer test-api-key")
	rec := httptest.NewRecorder()

	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: got %d want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"notion2api_wreq_client_new_total"`) {
		t.Fatalf("expected metrics payload to include wreq client metric, got %s", body)
	}
	if !strings.Contains(body, `"notion2api_http_transport_cache_total"`) {
		t.Fatalf("expected metrics payload to include transport cache metric, got %s", body)
	}
	after := int64(0)
	if value := wreqClientNewTotalMetric.Get("standard"); value != nil {
		after = value.(*expvar.Int).Value()
	}
	if after < before+1 {
		t.Fatalf("expected metric value to be incremented, before=%d after=%d", before, after)
	}
}

func TestServeHTTPMetricsExposesCorePrometheusSeries(t *testing.T) {
	resetMetricsForTest()
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

	setDispatchSlotInflight("alice@example.com", 2)
	observeWreqFFICallDuration(25 * time.Millisecond)
	observeSQLiteOpDuration("save_response", 2*time.Millisecond)
	addBrowserHelperSpawn()
	addBrowserHelperPoolWorkerSpawn()

	warmReq := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	warmRec := httptest.NewRecorder()
	app.ServeHTTP(warmRec, warmReq)
	if warmRec.Code != http.StatusOK {
		t.Fatalf("unexpected warm-up status: got %d want %d", warmRec.Code, http.StatusOK)
	}

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer test-api-key")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: got %d want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"notion2api_request_duration_seconds_bucket",
		"notion2api_dispatch_slot_inflight",
		"notion2api_wreq_ffi_call_duration_seconds_bucket",
		"notion2api_browser_helper_spawn_total",
		"notion2api_browser_helper_pool_worker_spawn_total",
		"notion2api_sqlite_op_duration_seconds_bucket",
		"notion2api_response_store_prune_total",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected /metrics output to include %q, got: %s", want, body)
		}
	}
	if !strings.Contains(body, "notion2api_browser_helper_pool_worker_spawn_total 1") {
		t.Fatalf("expected pool worker spawn counter value to be 1, got: %s", body)
	}
}

func TestSnapshotReadsFromAtomicBundle(t *testing.T) {
	state := &ServerState{}
	cfg := defaultConfig()
	cfg.APIKey = "snapshot-api-key"
	session := SessionInfo{UserID: "user-1", SpaceID: "space-1"}
	registry := ModelRegistry{
		Entries: []ModelDefinition{
			{ID: "gpt-5.4", Enabled: true},
		},
	}

	state.mu.Lock()
	state.Config = cfg
	state.Session = session
	state.ModelRegistry = registry
	state.updateSnapshotBundleLocked()
	state.mu.Unlock()

	gotCfg, gotSession, gotRegistry := state.Snapshot()
	if gotCfg.APIKey != cfg.APIKey {
		t.Fatalf("snapshot cfg mismatch: got %q want %q", gotCfg.APIKey, cfg.APIKey)
	}
	if gotSession.UserID != session.UserID || gotSession.SpaceID != session.SpaceID {
		t.Fatalf("snapshot session mismatch: got %+v want %+v", gotSession, session)
	}
	if len(gotRegistry.Entries) != 1 || gotRegistry.Entries[0].ID != "gpt-5.4" {
		t.Fatalf("snapshot registry mismatch: %+v", gotRegistry.Entries)
	}
	if len(state.snap.Load().DispatchOrder) != 0 {
		t.Fatalf("expected empty dispatch order for empty accounts")
	}
}

func TestSnapshotDispatchOrderPrecomputed(t *testing.T) {
	tempDir := t.TempDir()
	aliceProbe := filepath.Join(tempDir, "alice-probe.json")
	bobProbe := filepath.Join(tempDir, "bob-probe.json")
	if err := os.WriteFile(aliceProbe, []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatalf("write alice probe failed: %v", err)
	}
	if err := os.WriteFile(bobProbe, []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatalf("write bob probe failed: %v", err)
	}

	cfg := defaultConfig()
	cfg.APIKey = "snapshot-dispatch-order-api-key"
	cfg.ActiveAccount = "bob@example.com"
	cfg.Accounts = []NotionAccount{
		{Email: "alice@example.com", Priority: 10, MaxConcurrency: 1, ProbeJSON: aliceProbe},
		{Email: "bob@example.com", Priority: 1, MaxConcurrency: 1, ProbeJSON: bobProbe},
		{Email: "carol@example.com", Priority: 50, MaxConcurrency: 1, Disabled: true},
	}
	cfg = normalizeConfig(cfg)
	state, err := newServerState(cfg)
	if err != nil {
		t.Fatalf("newServerState failed: %v", err)
	}
	defer func() {
		_ = state.Close()
	}()

	snap := state.snap.Load()
	if snap == nil {
		t.Fatalf("expected non-nil snapshot bundle")
	}
	if len(snap.DispatchOrder) != 2 {
		t.Fatalf("unexpected dispatch order length: got %d want 2", len(snap.DispatchOrder))
	}
	if getAccountEmailKey(snap.DispatchOrder[0]) != "bob@example.com" {
		t.Fatalf("expected active account first in precomputed dispatch order, got %q", snap.DispatchOrder[0].Email)
	}
	if getAccountEmailKey(snap.DispatchOrder[1]) != "alice@example.com" {
		t.Fatalf("expected second candidate to be alice, got %q", snap.DispatchOrder[1].Email)
	}
}

func TestResolveDispatchCandidatesFromSnapshotUsesPrecomputedOrder(t *testing.T) {
	cfg := normalizeConfig(AppConfig{
		APIKey: "test-api-key",
		Accounts: []NotionAccount{
			{Email: "first@example.com", Priority: 10, MaxConcurrency: 1},
			{Email: "second@example.com", Priority: 20, MaxConcurrency: 1},
		},
		ActiveAccount: "first@example.com",
	})
	now := time.Now()
	bundle := &snapshotBundle{
		Config: cfg,
		DispatchOrder: []NotionAccount{
			{Email: "second@example.com", Priority: 20, MaxConcurrency: 1},
			{Email: "first@example.com", Priority: 10, MaxConcurrency: 1},
		},
	}
	candidates, err := resolveDispatchCandidatesFromSnapshot(bundle, PromptRunRequest{}, now)
	if err != nil {
		t.Fatalf("resolveDispatchCandidatesFromSnapshot failed: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("unexpected candidates length: got %d want 2", len(candidates))
	}
	if getAccountEmailKey(candidates[0]) != "second@example.com" || getAccountEmailKey(candidates[1]) != "first@example.com" {
		t.Fatalf("unexpected candidate order from snapshot: %+v", candidates)
	}
}

func TestConversationStoreGetReturnsValueSnapshotAfterMutation(t *testing.T) {
	store := newConversationStore()
	created := store.Create(ConversationCreateRequest{
		PreferredID: "conv-value-snapshot",
		Source:      "api",
		Transport:   "chat_completions",
		Model:       "gpt-5.4",
		Prompt:      "hello",
	})
	got1, ok := store.Get(created.ID)
	if !ok {
		t.Fatalf("expected created conversation to exist")
	}
	if got1.Status != "running" {
		t.Fatalf("unexpected initial status: %q", got1.Status)
	}

	store.Complete(created.ID, InferenceResult{
		Text:         "done",
		ThreadID:     "thread-1",
		AccountEmail: "alice@example.com",
	})

	got2, ok := store.Get(created.ID)
	if !ok {
		t.Fatalf("expected conversation after completion")
	}
	if got2.Status != "completed" {
		t.Fatalf("unexpected status after complete: %q", got2.Status)
	}
	if got1.Status == got2.Status {
		t.Fatalf("expected old value snapshot to remain unchanged, got1=%q got2=%q", got1.Status, got2.Status)
	}
}

func TestConversationStoreSummaryUsesCachedPreviewAfterMutations(t *testing.T) {
	store := newConversationStore()
	created := store.Create(ConversationCreateRequest{
		PreferredID: "conv-preview-cache",
		Source:      "api",
		Transport:   "chat_completions",
		Model:       "gpt-5.4",
		Prompt:      "first question",
	})
	list1 := store.List()
	if len(list1) == 0 {
		t.Fatalf("expected list to have one entry")
	}
	if !strings.Contains(list1[0].Preview, "first question") {
		t.Fatalf("unexpected initial preview: %q", list1[0].Preview)
	}

	store.AppendAssistantDelta(created.ID, "assistant draft")
	list2 := store.List()
	if len(list2) == 0 {
		t.Fatalf("expected list to have one entry after delta")
	}
	if !strings.Contains(list2[0].Preview, "assistant draft") {
		t.Fatalf("expected preview to reflect assistant delta, got %q", list2[0].Preview)
	}

	store.Complete(created.ID, InferenceResult{
		Text:         "final assistant reply",
		ThreadID:     "thread-preview",
		AccountEmail: "preview@example.com",
	})
	list3 := store.List()
	if len(list3) == 0 {
		t.Fatalf("expected list to have one entry after complete")
	}
	if !strings.Contains(list3[0].Preview, "final assistant reply") {
		t.Fatalf("expected preview to reflect completed assistant text, got %q", list3[0].Preview)
	}
}

func TestRequestedWebSearchFromTypedMetadataAndTools(t *testing.T) {
	if got := requestedWebSearchFromTyped(nil, json.RawMessage(`{"use_web_search": true}`), nil, false); !got {
		t.Fatalf("expected use_web_search=true from metadata to enable web search")
	}
	if got := requestedWebSearchFromTyped(nil, json.RawMessage(`{"notion_use_web_search":"false"}`), nil, true); got {
		t.Fatalf("expected notion_use_web_search=false metadata to disable web search")
	}
	if got := requestedWebSearchFromTyped(nil, nil, json.RawMessage(`[{"type":"web_search_preview"}]`), false); !got {
		t.Fatalf("expected web_search tool to enable web search")
	}
	if got := requestedWebSearchFromTyped(nil, map[string]any{"use_web_search": "1"}, nil, false); !got {
		t.Fatalf("expected use_web_search=1 map metadata to enable web search")
	}
	if got := requestedWebSearchFromTyped(nil, nil, []map[string]any{{"type": "web_search_legacy"}}, false); !got {
		t.Fatalf("expected web_search tool map slice to enable web search")
	}
}

func TestExtractTypedRequestBodies(t *testing.T) {
	chatPayload := map[string]any{
		"model":                "gpt-5.4",
		"stream":               true,
		"stream_options":       map[string]any{"include_usage": true},
		"conversation_id":      "conv-typed-chat",
		"account_email":        "typed@example.com",
		"use_web_search":       "true",
		"metadata":             map[string]any{"notion_use_web_search": false},
		"attachments":          []any{map[string]any{"type": "image_url", "url": "https://example.com/image.png"}},
		"messages":             []any{map[string]any{"role": "user", "content": "hello"}},
		"type":                 "continue",
		"user_name":            "user",
		"char_name":            "char",
		"group_names":          []any{"g1"},
		"continue_prefill":     "next",
		"show_thoughts":        true,
		"notion_account_email": "typed2@example.com",
	}
	chatTyped := extractChatCompletionsRequestBody(chatPayload)
	if chatTyped.Model != "gpt-5.4" || !chatTyped.Stream {
		t.Fatalf("unexpected typed chat body: %+v", chatTyped)
	}
	if chatTyped.UseWebSearch == nil || !*chatTyped.UseWebSearch {
		t.Fatalf("expected typed chat use_web_search=true")
	}
	if chatTyped.StreamIncludeUsage == nil || !*chatTyped.StreamIncludeUsage {
		t.Fatalf("expected typed chat stream include_usage=true")
	}
	if _, ok := chatTyped.Attachments.([]any); !ok {
		t.Fatalf("expected typed chat attachments to keep raw array type")
	}
	if _, ok := chatTyped.Messages.([]any); !ok {
		t.Fatalf("expected typed chat messages to keep raw array type")
	}
	if !chatTyped.likelySillyTavernByEnvelope() {
		t.Fatalf("expected chat body to be identified as likely sillytavern by envelope")
	}

	respPayload := map[string]any{
		"model":                "gpt-5.4",
		"stream":               false,
		"previous_response_id": "resp_123",
		"conversation_id":      "conv-typed-responses",
		"thread_id":            "thread-typed",
		"account_email":        "resp@example.com",
		"use_web_search":       true,
		"metadata":             map[string]any{"use_web_search": true},
		"input":                []any{map[string]any{"type": "text", "text": "input payload"}},
		"attachments":          []any{map[string]any{"type": "file", "file_url": "https://example.com/file.txt"}},
	}
	respTyped := extractResponsesRequestBody(respPayload)
	if respTyped.Model != "gpt-5.4" || respTyped.Stream {
		t.Fatalf("unexpected typed responses body: %+v", respTyped)
	}
	if respTyped.PreviousResponseID != "resp_123" || respTyped.ConversationID != "conv-typed-responses" {
		t.Fatalf("unexpected typed responses ids: %+v", respTyped)
	}
	if respTyped.UseWebSearch == nil || !*respTyped.UseWebSearch {
		t.Fatalf("expected typed responses use_web_search=true")
	}
	if _, ok := respTyped.Input.([]any); !ok {
		t.Fatalf("expected typed responses input to keep raw array type")
	}
	if _, ok := respTyped.Attachments.([]any); !ok {
		t.Fatalf("expected typed responses attachments to keep raw array type")
	}
}

func TestExtractChatTypedStreamIncludeUsageParsing(t *testing.T) {
	fromRaw := extractChatCompletionsRequestBody(map[string]any{
		"stream_options": json.RawMessage(`{"include_usage":"1"}`),
	})
	if fromRaw.StreamIncludeUsage == nil || !*fromRaw.StreamIncludeUsage {
		t.Fatalf("expected stream include_usage to parse true from raw json string flag")
	}

	fromMapFalse := extractChatCompletionsRequestBody(map[string]any{
		"stream_options": map[string]any{"include_usage": false},
	})
	if fromMapFalse.StreamIncludeUsage == nil {
		t.Fatalf("expected stream include_usage pointer to be populated for explicit false")
	}
	if *fromMapFalse.StreamIncludeUsage {
		t.Fatalf("expected stream include_usage=false from typed stream_options map")
	}
}

func TestRequestedIdentifiersFromTypedRespectHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("X-Conversation-ID", "header-conv")
	req.Header.Set("X-Thread-ID", "header-thread")
	req.Header.Set("X-Account-Email", "header@example.com")

	if got := requestedConversationIDFromTyped(req, "body-conv", "body-conv2", map[string]any{"conversation_id": "meta-conv"}); got != "header-conv" {
		t.Fatalf("conversation id should prefer header, got %q", got)
	}
	if got := requestedThreadIDFromTyped(req, "body-thread", "body-thread2", "body-thread3", map[string]any{"thread_id": "meta-thread"}); got != "header-thread" {
		t.Fatalf("thread id should prefer header, got %q", got)
	}
	if got := requestedAccountEmailFromTyped(req, "body@example.com", "body2@example.com", map[string]any{"account_email": "meta@example.com"}); got != "header@example.com" {
		t.Fatalf("account email should prefer header, got %q", got)
	}
}

func TestRequestedIdentifiersFromTypedFallbackToMetadata(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	metadata := json.RawMessage(`{"conversation_id":"meta-conv","thread_id":"meta-thread","account_email":"meta@example.com"}`)

	if got := requestedConversationIDFromTyped(req, "", "", metadata); got != "meta-conv" {
		t.Fatalf("conversation id should fallback to metadata, got %q", got)
	}
	if got := requestedThreadIDFromTyped(req, "", "", "", metadata); got != "meta-thread" {
		t.Fatalf("thread id should fallback to metadata, got %q", got)
	}
	if got := requestedAccountEmailFromTyped(req, "", "", metadata); got != "meta@example.com" {
		t.Fatalf("account email should fallback to metadata, got %q", got)
	}
}

func TestResolveContinuationConversationWithExplicitUsesTypedThreadID(t *testing.T) {
	app := newFreshThreadTestApp(t)
	seeded := seedCompletedConversation(t, app, "conv-typed-explicit", "Seed question", "Seed answer", "thread-explicit")

	segments := []conversationPromptSegment{
		{Role: "user", Text: "follow up"},
	}

	target, ok := app.resolveContinuationConversationWithExplicit("", "", segments, "", "thread-explicit")
	if !ok {
		t.Fatalf("expected explicit typed thread id to resolve continuation target")
	}
	if strings.TrimSpace(target.Conversation.ID) != seeded.ID {
		t.Fatalf("unexpected resolved conversation id: got %q want %q", target.Conversation.ID, seeded.ID)
	}
	if strings.TrimSpace(target.Conversation.ThreadID) != "thread-explicit" {
		t.Fatalf("unexpected resolved thread id: got %q", target.Conversation.ThreadID)
	}
}

func TestTypedEnvelopeExtractionFallsBackToLegacyWhenTypedFieldsMissing(t *testing.T) {
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

	var captured PromptRunRequest
	app.runPromptOverride = func(_ *http.Request, request PromptRunRequest) (InferenceResult, error) {
		captured = request
		return InferenceResult{
			Text:         "typed fallback ok",
			ThreadID:     "thread-typed-fallback",
			MessageID:    "msg-typed-fallback",
			TraceID:      "trace-typed-fallback",
			AccountEmail: "header@example.com",
		}, nil
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", mustJSONBody(t, map[string]any{
		"model": "gpt-5.4",
		"messages": []map[string]any{
			{"role": "user", "content": "hello"},
		},
	}))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-api-key")
	req.Header.Set("X-Account-Email", "header@example.com")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: got %d body=%s", rec.Code, rec.Body.String())
	}
	if captured.PinnedAccountEmail != "header@example.com" {
		t.Fatalf("expected pinned account from header fallback path, got %q", captured.PinnedAccountEmail)
	}
	if captured.PublicModel != "gpt-5.4" {
		t.Fatalf("expected resolved model from legacy payload path, got %q", captured.PublicModel)
	}
}

func sqliteWriterFallbackValue(reason string) int64 {
	if strings.TrimSpace(reason) == "" {
		return 0
	}
	value := sqliteWriterFallbackTotalMetric.Get(reason)
	if value == nil {
		return 0
	}
	counter, ok := value.(*expvar.Int)
	if !ok || counter == nil {
		return 0
	}
	return counter.Value()
}

func boolPtr(value bool) *bool {
	return &value
}

func TestSaveResponseWithAccountPersistsViaAsyncSQLiteWriter(t *testing.T) {
	tempDir := t.TempDir()
	cfg := defaultConfig()
	cfg.APIKey = "test-api-key"
	cfg.Storage.SQLitePath = filepath.Join(tempDir, "responses.sqlite")
	cfg.Storage.PersistConversations = true
	cfg.Storage.PersistResponses = boolPtr(true)
	cfg.Responses.StoreTTLSeconds = 3600

	state, err := newServerState(cfg)
	if err != nil {
		t.Fatalf("newServerState failed: %v", err)
	}
	defer func() {
		_ = state.Close()
	}()

	responseID := "resp_async_test_1"
	payload := map[string]any{
		"id":     responseID,
		"object": "response",
		"output": []any{
			map[string]any{
				"type": "message",
				"content": []any{
					map[string]any{
						"type": "output_text",
						"text": "hello from async sqlite writer",
					},
				},
			},
		},
	}
	state.saveResponseWithAccount(responseID, payload, "conv-async", "thread-async", "async@example.com")

	deadline := time.Now().Add(3 * time.Second)
	for {
		record, ok := state.getStoredResponse(responseID)
		if ok && strings.TrimSpace(record.ThreadID) == "thread-async" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("response not visible in in-memory store before deadline")
		}
		time.Sleep(25 * time.Millisecond)
	}

	readStore, err := openSQLiteStore(cfg)
	if err != nil {
		t.Fatalf("openSQLiteStore(read) failed: %v", err)
	}
	defer func() {
		_ = readStore.Close()
	}()

	waitUntil := time.Now().Add(3 * time.Second)
	for {
		rows, queryErr := readStore.db.Query(`SELECT payload_json, conversation_id, thread_id, account_email FROM responses WHERE response_id = ?`, responseID)
		if queryErr != nil {
			t.Fatalf("query persisted response failed: %v", queryErr)
		}
		found := false
		var rawPayload string
		var conversationID string
		var threadID string
		var accountEmail string
		for rows.Next() {
			found = true
			if scanErr := rows.Scan(&rawPayload, &conversationID, &threadID, &accountEmail); scanErr != nil {
				_ = rows.Close()
				t.Fatalf("scan persisted response failed: %v", scanErr)
			}
		}
		_ = rows.Close()
		if found {
			if strings.TrimSpace(conversationID) != "conv-async" {
				t.Fatalf("conversation_id mismatch: got %q want %q", conversationID, "conv-async")
			}
			if strings.TrimSpace(threadID) != "thread-async" {
				t.Fatalf("thread_id mismatch: got %q want %q", threadID, "thread-async")
			}
			if strings.TrimSpace(accountEmail) != "async@example.com" {
				t.Fatalf("account_email mismatch: got %q want %q", accountEmail, "async@example.com")
			}
			if !strings.Contains(rawPayload, "hello from async sqlite writer") {
				t.Fatalf("unexpected payload_json: %s", rawPayload)
			}
			break
		}
		if time.Now().After(waitUntil) {
			t.Fatalf("persisted response not found before deadline")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestSQLiteWriterCloseFlushesQueuedResponseWrites(t *testing.T) {
	tempDir := t.TempDir()
	cfg := defaultConfig()
	cfg.APIKey = "test-api-key"
	cfg.Storage.SQLitePath = filepath.Join(tempDir, "close-flush.sqlite")
	cfg.Storage.PersistConversations = true
	cfg.Storage.PersistResponses = boolPtr(true)
	cfg.Responses.StoreTTLSeconds = 3600

	state, err := newServerState(cfg)
	if err != nil {
		t.Fatalf("newServerState failed: %v", err)
	}

	total := 12
	for i := 0; i < total; i++ {
		responseID := "resp_flush_" + strconv.Itoa(i)
		state.saveResponseWithAccount(responseID, map[string]any{
			"id":     responseID,
			"object": "response",
			"idx":    i,
		}, "conv-flush", "thread-flush", "flush@example.com")
	}

	if err := state.Close(); err != nil {
		t.Fatalf("state.Close failed: %v", err)
	}

	readStore, err := openSQLiteStore(cfg)
	if err != nil {
		t.Fatalf("openSQLiteStore(read) failed: %v", err)
	}
	defer func() {
		_ = readStore.Close()
	}()

	row := readStore.db.QueryRow(`SELECT COUNT(1) FROM responses WHERE conversation_id = ? AND thread_id = ?`, "conv-flush", "thread-flush")
	var persisted int
	if scanErr := row.Scan(&persisted); scanErr != nil {
		t.Fatalf("scan persisted count failed: %v", scanErr)
	}
	if persisted != total {
		t.Fatalf("persisted response count mismatch after close flush: got %d want %d", persisted, total)
	}
}

func TestSQLiteWriterFallbackMetricRemainsStableUnderNormalLoad(t *testing.T) {
	tempDir := t.TempDir()
	cfg := defaultConfig()
	cfg.APIKey = "test-api-key"
	cfg.Storage.SQLitePath = filepath.Join(tempDir, "fallback-metric.sqlite")
	cfg.Storage.PersistConversations = true
	cfg.Storage.PersistResponses = boolPtr(true)
	cfg.Responses.StoreTTLSeconds = 3600

	beforeChannelFull := sqliteWriterFallbackValue("channel_full")
	beforeUnavailable := sqliteWriterFallbackValue("writer_unavailable")

	state, err := newServerState(cfg)
	if err != nil {
		t.Fatalf("newServerState failed: %v", err)
	}
	defer func() {
		_ = state.Close()
	}()

	for i := 0; i < 8; i++ {
		responseID := "resp_metric_" + strconv.Itoa(i)
		state.saveResponseWithAccount(responseID, map[string]any{
			"id":     responseID,
			"object": "response",
			"idx":    i,
		}, "conv-metric", "thread-metric", "metric@example.com")
	}

	time.Sleep(250 * time.Millisecond)

	afterChannelFull := sqliteWriterFallbackValue("channel_full")
	afterUnavailable := sqliteWriterFallbackValue("writer_unavailable")
	if afterChannelFull != beforeChannelFull {
		t.Fatalf("expected no channel_full fallback in normal load; before=%d after=%d", beforeChannelFull, afterChannelFull)
	}
	if afterUnavailable != beforeUnavailable {
		t.Fatalf("expected no writer_unavailable fallback in normal load; before=%d after=%d", beforeUnavailable, afterUnavailable)
	}
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

func TestServerStateSaveAndApplyInvalidatesDispatchProbeCacheOnActiveAccountChange(t *testing.T) {
	cfg := defaultConfig()
	cfg.APIKey = "test-api-key"
	cfg.Storage.SQLitePath = ""
	cfg.Accounts = []NotionAccount{
		{
			Email:         "alice@example.com",
			ProbeJSON:     "probe_files/notion_accounts/alice/probe.json",
			UserID:        "alice-user",
			SpaceID:       "alice-space",
			ClientVersion: "v1",
		},
		{
			Email:         "bob@example.com",
			ProbeJSON:     "probe_files/notion_accounts/bob/probe.json",
			UserID:        "bob-user",
			SpaceID:       "bob-space",
			ClientVersion: "v1",
		},
	}
	cfg.ActiveAccount = "alice@example.com"

	state, err := newServerState(cfg)
	if err != nil {
		t.Fatalf("newServerState failed: %v", err)
	}
	defer func() {
		_ = state.Close()
	}()
	if state.DispatchProbeCache == nil {
		t.Fatalf("expected dispatch probe cache to be initialized")
	}
	state.DispatchProbeCache.markSuccess("alice@example.com", time.Now())
	if state.DispatchProbeCache.shouldProbe("alice@example.com", 45*time.Second, time.Now()) {
		t.Fatalf("expected warm cache entry before active-account change")
	}

	next := state.Config
	next.ActiveAccount = "bob@example.com"
	if err := state.SaveAndApply(next); err != nil {
		t.Fatalf("SaveAndApply failed: %v", err)
	}
	if !state.DispatchProbeCache.shouldProbe("alice@example.com", 45*time.Second, time.Now()) {
		t.Fatalf("expected cache invalidation after active-account switch")
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

func TestHandleChatCompletionsStreamIncludeUsageFromTypedMessages(t *testing.T) {
	app := newFreshThreadTestApp(t)
	app.runPromptStreamSinkOverride = func(_ *http.Request, _ PromptRunRequest, sink InferenceStreamSink) (InferenceResult, error) {
		if sink.Text != nil {
			if err := sink.Text("hello "); err != nil {
				t.Fatalf("stream text write failed: %v", err)
			}
			if err := sink.Text("world"); err != nil {
				t.Fatalf("stream text write failed: %v", err)
			}
		}
		return InferenceResult{
			Text:      "hello world",
			Prompt:    "hello world",
			ThreadID:  "thread-stream-usage",
			MessageID: "msg-stream-usage",
		}, nil
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", mustJSONBody(t, map[string]any{
		"model":          "gpt-5.4",
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
		"messages": []map[string]any{
			{"role": "user", "content": "hello"},
		},
	}))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-api-key")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "\"usage\"") {
		t.Fatalf("expected stream output to include usage chunk, got body=%s", body)
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

func TestRefreshSessionInvalidatesDispatchProbeCacheOnSuccess(t *testing.T) {
	cfg := normalizeConfig(AppConfig{
		APIKey: "test-api-key",
		Accounts: []NotionAccount{
			{
				Email:            "alice@example.com",
				ProbeJSON:        "/tmp/alice/probe.json",
				StorageStatePath: "/tmp/alice/storage_state.json",
				PendingStatePath: "/tmp/alice/pending_login.json",
				UserID:           "alice-user",
				SpaceID:          "alice-space",
				UserName:         "alice",
				SpaceName:        "alice-space-name",
				ClientVersion:    "v1",
				Status:           "ready",
			},
		},
		ActiveAccount: "alice@example.com",
		SessionRefresh: SessionRefreshConfig{
			Enabled:          true,
			RetryOnAuthError: true,
			AutoSwitch:       true,
		},
	})
	state := &ServerState{
		Config:             cfg,
		Session:            SessionInfo{UserID: "alice-user", SpaceID: "alice-space"},
		DispatchProbeCache: newProbeCache(),
		ResponseStore:      newResponseStore(45 * time.Second),
		Conversations:      newConversationStore(),
		AdminTokens:        map[string]time.Time{},
		AdminLoginAttempts: map[string]AdminLoginAttempt{},
	}
	slot := &accountSlot{}
	slot.max.Store(1)
	slot.inflight.Store(0)
	slotMap := map[string]*accountSlot{
		"alice@example.com": slot,
	}
	state.slots.Store(&slotMap)
	syncDispatchSlotInflightFromSlots(slotMap)
	state.DispatchProbeCache.markSuccess("alice@example.com", time.Now())

	originalTryRefresh := testHookTryRefreshAccount
	originalSaveAndApply := testHookSaveAndApply
	defer func() {
		testHookTryRefreshAccount = originalTryRefresh
		testHookSaveAndApply = originalSaveAndApply
	}()

	testHookTryRefreshAccount = func(ctx context.Context, cfg AppConfig, account NotionAccount) (AppConfig, error) {
		account.Status = "ready"
		account.LastError = ""
		account.LastRefreshAt = time.Now().Format(time.RFC3339)
		cfg.UpsertAccount(account)
		return cfg, nil
	}
	testHookSaveAndApply = func(s *ServerState, cfg AppConfig) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.Config = cfg
		s.updateSnapshotBundleLocked()
		return nil
	}

	if err := state.RefreshSession(context.Background(), "test_refresh_success"); err != nil {
		t.Fatalf("refresh session failed: %v", err)
	}
	if !state.DispatchProbeCache.shouldProbe("alice@example.com", 45*time.Second, time.Now()) {
		t.Fatalf("expected probe cache to be invalidated after refresh success")
	}
}

func newSQLiteStoreTestConfig(path string) AppConfig {
	cfg := defaultConfig()
	cfg.APIKey = "test-api-key"
	cfg.Storage.SQLitePath = path
	return cfg
}

func TestOpenSQLiteStoreConfiguresReadWriteAndReadOnlyPools(t *testing.T) {
	cfg := newSQLiteStoreTestConfig(filepath.Join(t.TempDir(), "notion2api.sqlite"))
	store, err := openSQLiteStore(cfg)
	if err != nil {
		t.Fatalf("openSQLiteStore failed: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()
	if store.db == nil {
		t.Fatalf("expected writable sqlite connection")
	}
	if store.roDB == nil {
		t.Fatalf("expected read-only sqlite connection")
	}
	if got := store.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("unexpected write db max open conns: got %d want 1", got)
	}
	wantReadConns := maxInt(2, runtime.NumCPU())
	if got := store.roDB.Stats().MaxOpenConnections; got != wantReadConns {
		t.Fatalf("unexpected read db max open conns: got %d want %d", got, wantReadConns)
	}
}

func TestSQLiteStoreInitAppliesExtendedPragmas(t *testing.T) {
	cfg := newSQLiteStoreTestConfig(filepath.Join(t.TempDir(), "notion2api.sqlite"))
	store, err := openSQLiteStore(cfg)
	if err != nil {
		t.Fatalf("openSQLiteStore failed: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	var mmapSize int64
	if err := store.db.QueryRow("PRAGMA mmap_size;").Scan(&mmapSize); err != nil {
		t.Fatalf("query mmap_size failed: %v", err)
	}
	if mmapSize != 268435456 {
		t.Fatalf("unexpected mmap_size: got %d want %d", mmapSize, int64(268435456))
	}

	var cacheSize int64
	if err := store.db.QueryRow("PRAGMA cache_size;").Scan(&cacheSize); err != nil {
		t.Fatalf("query cache_size failed: %v", err)
	}
	if cacheSize != -65536 {
		t.Fatalf("unexpected cache_size: got %d want %d", cacheSize, int64(-65536))
	}

	var tempStore int64
	if err := store.db.QueryRow("PRAGMA temp_store;").Scan(&tempStore); err != nil {
		t.Fatalf("query temp_store failed: %v", err)
	}
	if tempStore != 2 {
		t.Fatalf("unexpected temp_store: got %d want 2(memory)", tempStore)
	}

	var autoCheckpoint int64
	if err := store.db.QueryRow("PRAGMA wal_autocheckpoint;").Scan(&autoCheckpoint); err != nil {
		t.Fatalf("query wal_autocheckpoint failed: %v", err)
	}
	if autoCheckpoint != 1000 {
		t.Fatalf("unexpected wal_autocheckpoint: got %d want 1000", autoCheckpoint)
	}
}

func TestSQLiteStoreReadOnlyConnectionRejectsWrites(t *testing.T) {
	cfg := newSQLiteStoreTestConfig(filepath.Join(t.TempDir(), "notion2api.sqlite"))
	store, err := openSQLiteStore(cfg)
	if err != nil {
		t.Fatalf("openSQLiteStore failed: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()
	_, err = store.roDB.Exec("CREATE TABLE read_only_write_should_fail(id INTEGER)")
	if err == nil {
		t.Fatalf("expected write on read-only connection to fail")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "readonly") {
		t.Fatalf("expected readonly error, got: %v", err)
	}
}

func TestSQLiteStoreLoadAccountsUsesReadOnlyConnection(t *testing.T) {
	cfg := newSQLiteStoreTestConfig(filepath.Join(t.TempDir(), "notion2api.sqlite"))
	store, err := openSQLiteStore(cfg)
	if err != nil {
		t.Fatalf("openSQLiteStore failed: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	saveCfg := normalizeConfig(AppConfig{
		APIKey:        "test-api-key",
		Storage:       StorageConfig{SQLitePath: cfg.Storage.SQLitePath},
		LoginHelper:   LoginHelperConfig{SessionsDir: "probe_files/notion_accounts"},
		Accounts:      []NotionAccount{{Email: "alice@example.com"}},
		ActiveAccount: "alice@example.com",
	})
	if err := store.SaveAccounts(saveCfg); err != nil {
		t.Fatalf("SaveAccounts failed: %v", err)
	}

	if err := store.db.Close(); err != nil {
		t.Fatalf("close write db failed: %v", err)
	}
	store.db = nil
	accounts, activeAccount, ok, err := store.LoadAccounts()
	if err != nil {
		t.Fatalf("LoadAccounts failed: %v", err)
	}
	if !ok {
		t.Fatalf("expected persisted accounts to be available")
	}
	if len(accounts) != 1 {
		t.Fatalf("unexpected account count: got %d want 1", len(accounts))
	}
	if getAccountEmailKey(accounts[0]) != "alice@example.com" {
		t.Fatalf("unexpected loaded account email: %q", accounts[0].Email)
	}
	if canonicalEmailKey(activeAccount) != "alice@example.com" {
		t.Fatalf("unexpected active account: %q", activeAccount)
	}
}

func TestServeHTTPOptionsReturnsCORSNoContent(t *testing.T) {
	app := newFreshThreadTestApp(t)
	req := httptest.NewRequest(http.MethodOptions, "/v1/models", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("unexpected options status: got %d want %d", rec.Code, http.StatusNoContent)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != corsAllowOrigin {
		t.Fatalf("unexpected Access-Control-Allow-Origin: got %q want %q", got, corsAllowOrigin)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != corsAllowHeaders {
		t.Fatalf("unexpected Access-Control-Allow-Headers: got %q want %q", got, corsAllowHeaders)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got != corsAllowMethods {
		t.Fatalf("unexpected Access-Control-Allow-Methods: got %q want %q", got, corsAllowMethods)
	}
}

func TestServeIndexIncludesCORSHeaders(t *testing.T) {
	app := newFreshThreadTestApp(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: got %d want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != corsAllowOrigin {
		t.Fatalf("unexpected Access-Control-Allow-Origin: got %q want %q", got, corsAllowOrigin)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != corsAllowHeaders {
		t.Fatalf("unexpected Access-Control-Allow-Headers: got %q want %q", got, corsAllowHeaders)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got != corsAllowMethods {
		t.Fatalf("unexpected Access-Control-Allow-Methods: got %q want %q", got, corsAllowMethods)
	}
}

func TestNormalizeConfigSetsMaxRequestBodyBytesDefault(t *testing.T) {
	cfg := normalizeConfig(AppConfig{})
	if got := cfg.Limits.MaxRequestBodyBytes; got != 4*1024*1024 {
		t.Fatalf("unexpected max request body bytes default: got %d want %d", got, int64(4*1024*1024))
	}
}

func TestNormalizeConfigClampsNonPositiveMaxRequestBodyBytes(t *testing.T) {
	cfg := normalizeConfig(AppConfig{Limits: LimitsConfig{MaxRequestBodyBytes: -1}})
	if got := cfg.Limits.MaxRequestBodyBytes; got != 4*1024*1024 {
		t.Fatalf("unexpected max request body bytes clamp: got %d want %d", got, int64(4*1024*1024))
	}
}

func TestHandleChatCompletionsRejectsTooLargeBody(t *testing.T) {
	cfg := defaultConfig()
	cfg.APIKey = "test-api-key"
	cfg.Storage.SQLitePath = ""
	cfg.Limits.MaxRequestBodyBytes = 128
	state, err := newServerState(cfg)
	if err != nil {
		t.Fatalf("newServerState failed: %v", err)
	}
	defer func() {
		_ = state.Close()
	}()
	app := &App{State: state}

	oversizeText := strings.Repeat("x", 512)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", mustJSONBody(t, map[string]any{
		"model": "gpt-5.4",
		"messages": []map[string]any{
			{"role": "user", "content": oversizeText},
		},
	}))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-api-key")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("unexpected status: got %d want %d body=%s", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"code":"request_too_large"`) {
		t.Fatalf("expected request_too_large code, got %s", body)
	}
	if !strings.Contains(body, `"type":"invalid_request_error"`) {
		t.Fatalf("expected invalid_request_error type, got %s", body)
	}
}

func TestDecodeBodyRawWithLimitRejectsTrailingContent(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"a":1} {"b":2}`))
	raw, err := decodeBodyRawWithLimit(nil, req, 0)
	if err == nil {
		t.Fatalf("expected trailing content error, got raw=%q", string(raw))
	}
	if !strings.Contains(err.Error(), "invalid json") {
		t.Fatalf("expected invalid json error, got %v", err)
	}
}

func TestDecodeBodyRawWithLimitNormalizesWhitespace(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("  \n\t {\"a\":1}\n\t  "))
	raw, err := decodeBodyRawWithLimit(nil, req, 0)
	if err != nil {
		t.Fatalf("decodeBodyRawWithLimit failed: %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got != "{\"a\":1}" {
		t.Fatalf("unexpected normalized raw body: got %q", got)
	}
}

func TestDecodeBodyRawWithLimitTreatsEmptyBodyAsEmptyObject(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("   \n\t "))
	raw, err := decodeBodyRawWithLimit(nil, req, 0)
	if err != nil {
		t.Fatalf("decodeBodyRawWithLimit failed: %v", err)
	}
	if string(raw) != "{}" {
		t.Fatalf("expected empty object for empty body, got %q", string(raw))
	}
}

func TestDecodeChatCompletionsRequestBodyFromRawFallsBackToMapOnTypedDecodeMismatch(t *testing.T) {
	raw := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"group_names":[1]}`)
	typed, payload, err := decodeChatCompletionsRequestBodyFromRaw(raw)
	if err != nil {
		t.Fatalf("decodeChatCompletionsRequestBodyFromRaw failed: %v", err)
	}
	if payload == nil {
		t.Fatalf("expected payload fallback map to be populated")
	}
	messages := sliceValue(typed.Messages)
	if len(messages) != 1 {
		t.Fatalf("expected typed messages recovered via map fallback, got len=%d", len(messages))
	}
	msg := mapValue(messages[0])
	if strings.TrimSpace(stringValue(msg["content"])) != "hello" {
		t.Fatalf("expected fallback-typed message content 'hello', got %#v", msg["content"])
	}
}

func TestDecodeChatCompletionsRequestBodyFromRawParsesStreamIncludeUsageWithoutMapFallback(t *testing.T) {
	raw := []byte(`{"model":"gpt-5.4","stream_options":{"include_usage":"1"},"messages":[{"role":"user","content":"hello"}]}`)
	typed, payload, err := decodeChatCompletionsRequestBodyFromRaw(raw)
	if err != nil {
		t.Fatalf("decodeChatCompletionsRequestBodyFromRaw failed: %v", err)
	}
	if payload != nil {
		t.Fatalf("expected typed decode path without map fallback")
	}
	if typed.StreamIncludeUsage == nil || !*typed.StreamIncludeUsage {
		t.Fatalf("expected stream include_usage=true from typed decode path")
	}
}

func TestHandleChatCompletionsSillyTavernFallbackOnContinuePrefillKey(t *testing.T) {
	app := newFreshThreadTestApp(t)
	captured := PromptRunRequest{}
	app.runPromptOverride = func(_ *http.Request, request PromptRunRequest) (InferenceResult, error) {
		captured = request
		return InferenceResult{
			Text:         "ok",
			ThreadID:     "thread-st-fallback",
			AccountEmail: "seed@example.com",
		}, nil
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-5.4",
		"messages":[{"role":"user","content":"Hello there"}],
		"continue_prefill":"...",
		"group_names":[1]
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-api-key")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: got %d body=%s", rec.Code, rec.Body.String())
	}
	if captured.ClientProfile != sillyTavernClientProfile {
		t.Fatalf("expected sillytavern client profile, got %q", captured.ClientProfile)
	}
	if strings.TrimSpace(captured.Prompt) == "" {
		t.Fatalf("expected non-empty prompt for sillytavern fallback")
	}
}

func TestDecodeResponsesRequestBodyFromRawFallsBackToMapOnTypedDecodeMismatch(t *testing.T) {
	raw := []byte(`{"model":"gpt-5.4","input":"hello","attachments":[{"type":"file","file_url":"https://example.com/f.txt"}],"conversation_id":1}`)
	typed, payload, err := decodeResponsesRequestBodyFromRaw(raw)
	if err != nil {
		t.Fatalf("decodeResponsesRequestBodyFromRaw failed: %v", err)
	}
	if payload == nil {
		t.Fatalf("expected payload fallback map to be populated")
	}
	if strings.TrimSpace(typed.Model) != "gpt-5.4" {
		t.Fatalf("unexpected model after fallback: %q", typed.Model)
	}
	if strings.TrimSpace(flattenContent(typed.Input)) != "hello" {
		t.Fatalf("expected fallback-typed input 'hello', got %#v", typed.Input)
	}
	atts := sliceValue(typed.Attachments)
	if len(atts) != 1 {
		t.Fatalf("expected one attachment after fallback, got %d", len(atts))
	}
}

func TestHandleResponsesTypedFirstDecodeFallbackOnConversationIDTypeMismatch(t *testing.T) {
	app := newFreshThreadTestApp(t)
	seeded := seedCompletedConversation(t, app, "conv-responses-fallback", "Please remember this.", "Remembered.", "thread-old-responses-fallback")

	var captured PromptRunRequest
	app.runPromptOverride = func(_ *http.Request, request PromptRunRequest) (InferenceResult, error) {
		captured = request
		return InferenceResult{
			Text:         "Summary ready.",
			ThreadID:     "thread-new-responses-fallback",
			MessageID:    "msg-new-responses-fallback",
			TraceID:      "trace-new-responses-fallback",
			AccountEmail: "seed@example.com",
		}, nil
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5.4",
		"input":"Summarize that.",
		"attachments":[{"type":"file","file_url":"https://example.com/f.txt"}],
		"conversation_id":1
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-api-key")
	req.Header.Set("X-Conversation-ID", seeded.ID)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status mismatch: got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Conversation-ID"); got != seeded.ID {
		t.Fatalf("conversation header mismatch: got %q want %q", got, seeded.ID)
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
	assertConversationContinued(t, app, seeded.ID, "thread-new-responses-fallback", "Summary ready.")
}

func TestCollectProbeModelPathsIncludesActiveAndAccountProbeJSON(t *testing.T) {
	cfg := normalizeConfig(AppConfig{
		ProbeJSON: " probe_files/notion_accounts/active/probe.json ",
		Accounts: []NotionAccount{
			{ProbeJSON: "probe_files/notion_accounts/alpha/probe.json"},
			{ProbeJSON: "probe_files/notion_accounts/alpha/probe.json"},
			{ProbeJSON: " probe_files/notion_accounts/beta/probe.json "},
		},
	})
	paths := collectProbeModelPaths(cfg)
	for i := range paths {
		paths[i] = strings.ReplaceAll(paths[i], "\\", "/")
	}
	sort.Strings(paths)
	expected := []string{
		"probe_files/notion_accounts/active/probe.json",
		"probe_files/notion_accounts/alpha/probe.json",
		"probe_files/notion_accounts/beta/probe.json",
	}
	if len(paths) != len(expected) {
		t.Fatalf("unexpected path count: got %d want %d (%v)", len(paths), len(expected), paths)
	}
	for i := range expected {
		if paths[i] != expected[i] {
			t.Fatalf("unexpected path[%d]: got %q want %q", i, paths[i], expected[i])
		}
	}
}

func TestBuildModelRegistryLoadsProbeModelsFromActiveAndAccountPaths(t *testing.T) {
	dir := t.TempDir()
	activeProbe := filepath.Join(dir, "active-probe.json")
	accountProbe := filepath.Join(dir, "account-probe.json")
	activeBlob := `{"models":[{"model":"active-model-raw","modelMessage":"Active Model","modelFamily":"openai","displayGroup":"fast","isDisabled":false,"markdownChat":{"beta":false},"workflow":{"finalModelName":"active-notion-model","beta":false},"customAgent":{"finalModelName":"","beta":false}}]}`
	accountBlob := `{"models":[{"model":"account-model-raw","modelMessage":"Account Model","modelFamily":"anthropic","displayGroup":"intelligent","isDisabled":false,"markdownChat":{"beta":false},"workflow":{"finalModelName":"account-notion-model","beta":false},"customAgent":{"finalModelName":"","beta":false}}]}`
	writeProbeFile := func(path string, blob string) {
		payload := map[string]any{
			"email":          "tester@example.com",
			"userId":         "user-id",
			"spaceId":        "space-id",
			"clientVersion":  "v1",
			"embeddedModels": blob,
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal probe payload failed: %v", err)
		}
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatalf("write probe payload failed: %v", err)
		}
	}
	writeProbeFile(activeProbe, activeBlob)
	writeProbeFile(accountProbe, accountBlob)

	cfg := normalizeConfig(AppConfig{
		ProbeJSON: activeProbe,
		Accounts: []NotionAccount{
			{Email: "alpha@example.com", ProbeJSON: accountProbe},
		},
	})
	registry := buildModelRegistry(cfg)
	if _, err := registry.Resolve("active-model", ""); err != nil {
		t.Fatalf("expected active probe model to be loaded, got err=%v", err)
	}
	if _, err := registry.Resolve("account-model", ""); err != nil {
		t.Fatalf("expected account probe model to be loaded, got err=%v", err)
	}
}

func TestDeleteAccountUsesCanonicalKeyComparison(t *testing.T) {
	cfg := normalizeConfig(AppConfig{
		ActiveAccount: " Alice@Example.com ",
		ProbeJSON:     "probe_files/notion_accounts/alice/probe.json",
		Accounts: []NotionAccount{
			{Email: "alice@example.com"},
		},
	})
	ok := cfg.DeleteAccount("ALICE@example.com")
	if !ok {
		t.Fatalf("expected delete to succeed")
	}
	if len(cfg.Accounts) != 0 {
		t.Fatalf("expected accounts to be empty after delete, got %d", len(cfg.Accounts))
	}
	if cfg.ActiveAccount != "" {
		t.Fatalf("expected active account to be cleared, got %q", cfg.ActiveAccount)
	}
	if cfg.ProbeJSON != "" {
		t.Fatalf("expected probe json to be cleared, got %q", cfg.ProbeJSON)
	}
}
