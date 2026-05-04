package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newBrowserFallbackTestClient(baseURL string) *NotionAIClient {
	client := newBestEffortTestClient(baseURL)
	client.Session.UserName = "tester"
	client.Session.SpaceName = "tester-space"
	client.Session.SpaceViewID = "space-view"
	return client
}

func buildSuccessfulAgentInferenceNDJSON(t *testing.T, messageID string, text string) string {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"type":       "agent-inference",
		"id":         messageID,
		"finishedAt": "2026-04-18T12:45:00.000Z",
		"value": []map[string]any{{
			"type":    "text",
			"content": text,
		}},
	})
	if err != nil {
		t.Fatalf("marshal success ndjson failed: %v", err)
	}
	return string(line) + "\n"
}

func buildTrustRuleDeniedResponse(t *testing.T, threadID string, messageID string, subType string) string {
	t.Helper()
	recordMap := buildThreadErrorRecordMap(threadID, "test-space", messageID, "AI inference is not allowed.", subType, "trace-trust")
	line, err := json.Marshal(map[string]any{
		"type":      "record-map",
		"recordMap": recordMap,
	})
	if err != nil {
		t.Fatalf("marshal trust response failed: %v", err)
	}
	return string(line) + "\n"
}

func TestRunPromptFallsBackToBrowserTransportOnTrustRuleDenied(t *testing.T) {
	const trustMessageID = "msg-trust"
	const browserMessageID = "msg-browser"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/runInferenceTranscript" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload failed: %v", err)
		}
		threadID := strings.TrimSpace(stringValue(payload["threadId"]))
		if threadID == "" {
			t.Fatalf("runInference payload missing threadId")
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(buildTrustRuleDeniedResponse(t, threadID, trustMessageID, "trust-rule-denied")))
	}))
	defer server.Close()

	client := newBrowserFallbackTestClient(server.URL)
	fallbackCalls := 0
	client.browserRunInferenceFallback = func(ctx context.Context, payload map[string]any) (string, error) {
		fallbackCalls++
		if got := strings.TrimSpace(stringValue(payload["threadId"])); got == "" {
			t.Fatalf("browser fallback payload missing threadId")
		}
		return buildSuccessfulAgentInferenceNDJSON(t, browserMessageID, "OK"), nil
	}

	result, err := client.RunPrompt(context.Background(), PromptRunRequest{
		Prompt:       "hello",
		PublicModel:  "opus-4.7",
		NotionModel:  "apricot-sorbet-medium",
		UseWebSearch: false,
	})
	if err != nil {
		t.Fatalf("RunPrompt returned error: %v", err)
	}
	if fallbackCalls != 1 {
		t.Fatalf("expected browser fallback to run once, got %d", fallbackCalls)
	}
	if result.Text != "OK" {
		t.Fatalf("result text mismatch: got %q want %q", result.Text, "OK")
	}
	if result.MessageID != browserMessageID {
		t.Fatalf("message id mismatch: got %q want %q", result.MessageID, browserMessageID)
	}
}

func TestRunPromptStreamWithSinkFallsBackToBrowserTransportOnTrustRuleDenied(t *testing.T) {
	const trustMessageID = "msg-trust-stream"
	const browserMessageID = "msg-browser-stream"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/runInferenceTranscript" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload failed: %v", err)
		}
		threadID := strings.TrimSpace(stringValue(payload["threadId"]))
		if threadID == "" {
			t.Fatalf("runInference payload missing threadId")
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(buildTrustRuleDeniedResponse(t, threadID, trustMessageID, "trust-rule-denied")))
	}))
	defer server.Close()

	client := newBrowserFallbackTestClient(server.URL)
	var streamed strings.Builder
	fallbackCalls := 0
	client.browserRunInferenceFallback = func(ctx context.Context, payload map[string]any) (string, error) {
		fallbackCalls++
		return buildSuccessfulAgentInferenceNDJSON(t, browserMessageID, "stream-ok"), nil
	}

	result, err := client.RunPromptStreamWithSink(context.Background(), PromptRunRequest{
		Prompt:       "hello",
		PublicModel:  "opus-4.7",
		NotionModel:  "apricot-sorbet-medium",
		UseWebSearch: false,
	}, InferenceStreamSink{
		Text: func(delta string) error {
			streamed.WriteString(delta)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("RunPromptStreamWithSink returned error: %v", err)
	}
	if fallbackCalls != 1 {
		t.Fatalf("expected browser fallback to run once, got %d", fallbackCalls)
	}
	if streamed.String() != "stream-ok" {
		t.Fatalf("streamed text mismatch: got %q want %q", streamed.String(), "stream-ok")
	}
	if result.Text != "stream-ok" {
		t.Fatalf("result text mismatch: got %q want %q", result.Text, "stream-ok")
	}
}

func TestRunPromptDoesNotFallbackForOtherInferenceErrors(t *testing.T) {
	const trustMessageID = "msg-not-trust"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/runInferenceTranscript" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload failed: %v", err)
		}
		threadID := strings.TrimSpace(stringValue(payload["threadId"]))
		if threadID == "" {
			t.Fatalf("runInference payload missing threadId")
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(buildTrustRuleDeniedResponse(t, threadID, trustMessageID, "quota-exhausted")))
	}))
	defer server.Close()

	client := newBrowserFallbackTestClient(server.URL)
	fallbackCalls := 0
	client.browserRunInferenceFallback = func(ctx context.Context, payload map[string]any) (string, error) {
		fallbackCalls++
		return buildSuccessfulAgentInferenceNDJSON(t, "unexpected", "should-not-run"), nil
	}

	_, err := client.RunPrompt(context.Background(), PromptRunRequest{
		Prompt:       "hello",
		PublicModel:  "opus-4.7",
		NotionModel:  "apricot-sorbet-medium",
		UseWebSearch: false,
	})
	if err == nil || !strings.Contains(err.Error(), "quota-exhausted") {
		t.Fatalf("expected original inference error, got %v", err)
	}
	if fallbackCalls != 0 {
		t.Fatalf("expected browser fallback to stay unused, got %d calls", fallbackCalls)
	}
}

func TestRunPromptReturnsBrowserChallengeErrorWithoutPolling(t *testing.T) {
	const trustMessageID = "msg-trust-challenge"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/runInferenceTranscript" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload failed: %v", err)
		}
		threadID := strings.TrimSpace(stringValue(payload["threadId"]))
		if threadID == "" {
			t.Fatalf("runInference payload missing threadId")
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(buildTrustRuleDeniedResponse(t, threadID, trustMessageID, "trust-rule-denied")))
	}))
	defer server.Close()

	client := newBrowserFallbackTestClient(server.URL)
	fallbackCalls := 0
	client.browserRunInferenceFallback = func(ctx context.Context, payload map[string]any) (string, error) {
		fallbackCalls++
		return "<!DOCTYPE html><html><script>var cookiePart = 'challenge';</script></html>", nil
	}

	_, err := client.RunPrompt(context.Background(), PromptRunRequest{
		Prompt:       "hello",
		PublicModel:  "opus-4.7",
		NotionModel:  "apricot-sorbet-medium",
		UseWebSearch: false,
	})
	if err == nil || !strings.Contains(err.Error(), "challenge/html content") {
		t.Fatalf("expected browser challenge error, got %v", err)
	}
	if fallbackCalls != 1 {
		t.Fatalf("expected browser fallback to run once, got %d", fallbackCalls)
	}
}

func TestRunPromptBrowserFallbackUsesFullParentBudget(t *testing.T) {
	const trustMessageID = "msg-trust-budget"
	const browserMessageID = "msg-browser-budget"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/runInferenceTranscript" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload failed: %v", err)
		}
		threadID := strings.TrimSpace(stringValue(payload["threadId"]))
		if threadID == "" {
			t.Fatalf("runInference payload missing threadId")
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(buildTrustRuleDeniedResponse(t, threadID, trustMessageID, "trust-rule-denied")))
	}))
	defer server.Close()

	client := newBrowserFallbackTestClient(server.URL)
	client.browserRunInferenceFallback = func(ctx context.Context, payload map[string]any) (string, error) {
		select {
		case <-time.After(60 * time.Millisecond):
		case <-ctx.Done():
			return "", ctx.Err()
		}
		return buildSuccessfulAgentInferenceNDJSON(t, browserMessageID, "budget-ok"), nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()

	result, err := client.RunPrompt(ctx, PromptRunRequest{
		Prompt:       "hello",
		PublicModel:  "opus-4.7",
		NotionModel:  "apricot-sorbet-medium",
		UseWebSearch: false,
	})
	if err != nil {
		t.Fatalf("RunPrompt returned error: %v", err)
	}
	if result.Text != "budget-ok" {
		t.Fatalf("result text mismatch: got %q want %q", result.Text, "budget-ok")
	}
	if result.MessageID != browserMessageID {
		t.Fatalf("message id mismatch: got %q want %q", result.MessageID, browserMessageID)
	}
}

func TestRunPromptReturnsClearClientCanceledBrowserFallbackError(t *testing.T) {
	const trustMessageID = "msg-trust-canceled"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/runInferenceTranscript" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload failed: %v", err)
		}
		threadID := strings.TrimSpace(stringValue(payload["threadId"]))
		if threadID == "" {
			t.Fatalf("runInference payload missing threadId")
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(buildTrustRuleDeniedResponse(t, threadID, trustMessageID, "trust-rule-denied")))
	}))
	defer server.Close()

	client := newBrowserFallbackTestClient(server.URL)
	client.browserRunInferenceFallback = func(ctx context.Context, payload map[string]any) (string, error) {
		return "", context.Canceled
	}

	_, err := client.RunPrompt(context.Background(), PromptRunRequest{
		Prompt:       "hello",
		PublicModel:  "opus-4.7",
		NotionModel:  "apricot-sorbet-medium",
		UseWebSearch: false,
	})
	if err == nil {
		t.Fatalf("expected browser fallback cancellation error")
	}
	if !strings.Contains(err.Error(), "request was canceled by caller/client before browser fallback completed") {
		t.Fatalf("expected clear canceled message, got %v", err)
	}
}

func TestBrowserFallbackTimeoutForPayloadScalesWithPayloadSize(t *testing.T) {
	small := browserFallbackTimeoutForPayload(context.Background(), map[string]any{
		"prompt": "short",
	})
	large := browserFallbackTimeoutForPayload(context.Background(), map[string]any{
		"prompt": strings.Repeat("x", 20*1024),
	})

	if small != browserFallbackTimeout {
		t.Fatalf("small payload timeout = %s, want %s", small, browserFallbackTimeout)
	}
	if large <= small {
		t.Fatalf("large payload timeout = %s, want > %s", large, small)
	}
	if large > browserFallbackTimeoutMax {
		t.Fatalf("large payload timeout = %s, want <= %s", large, browserFallbackTimeoutMax)
	}
}

func TestBrowserFallbackTimeoutForPayloadHonorsParentDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	got := browserFallbackTimeoutForPayload(ctx, map[string]any{
		"prompt": strings.Repeat("x", 20*1024),
	})

	if got <= 0 {
		t.Fatalf("expected positive timeout, got %s", got)
	}
	if got > 40*time.Millisecond {
		t.Fatalf("timeout = %s, want <= 40ms", got)
	}
}
