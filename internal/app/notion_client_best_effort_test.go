package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"expvar"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newBestEffortTestClient(baseURL string) *NotionAIClient {
	cfg := defaultConfig()
	cfg.APIKey = "test-api-key"
	cfg.UpstreamBaseURL = baseURL
	cfg.UpstreamOrigin = baseURL
	return newNotionAIClient(SessionInfo{
		ClientVersion: "test-client-version",
		UserID:        "test-user",
		SpaceID:       "test-space",
		Cookies: []ProbeCookie{{
			Name:  "token_v2",
			Value: "test-cookie",
		}},
	}, cfg, "")
}

func buildThreadErrorRecordMap(threadID string, spaceID string, messageID string, message string, subType string, traceID string) map[string]any {
	return map[string]any{
		"thread": map[string]any{
			threadID: map[string]any{
				"spaceId": spaceID,
				"value": map[string]any{
					"value": map[string]any{
						"id":           threadID,
						"space_id":     spaceID,
						"messages":     []string{messageID},
						"parent_id":    spaceID,
						"parent_table": "space",
					},
				},
			},
		},
		"thread_message": map[string]any{
			messageID: map[string]any{
				"spaceId": spaceID,
				"value": map[string]any{
					"value": map[string]any{
						"id":       messageID,
						"space_id": spaceID,
						"step": map[string]any{
							"id":          messageID,
							"type":        "error",
							"message":     message,
							"subType":     subType,
							"traceId":     traceID,
							"isRetryable": false,
						},
						"parent_id":    threadID,
						"parent_table": "thread",
						"data": map[string]any{
							"inference_id": traceID,
						},
					},
				},
			},
		},
	}
}

func TestEnsureSessionLiveMetadataUsesBestEffortTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/loadUserContent", "/api/v3/getSpacesInitial":
			time.Sleep(300 * time.Millisecond)
			http.Error(w, "request canceled", http.StatusGatewayTimeout)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := newBestEffortTestClient(server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	started := time.Now()
	client.ensureSessionLiveMetadata(ctx)
	elapsed := time.Since(started)

	if elapsed >= 140*time.Millisecond {
		t.Fatalf("expected metadata backfill to stop early, took %v", elapsed)
	}
}

func TestProbeAccountProtocolHealthIgnoresContextAbort(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/getInferenceTranscriptsForUser" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		time.Sleep(300 * time.Millisecond)
		http.Error(w, "request canceled", http.StatusGatewayTimeout)
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.APIKey = "test-api-key"
	cfg.UpstreamBaseURL = server.URL
	cfg.UpstreamOrigin = server.URL

	session := SessionInfo{
		ClientVersion: "test-client-version",
		UserID:        "test-user",
		SpaceID:       "test-space",
		Cookies: []ProbeCookie{{
			Name:  "token_v2",
			Value: "test-cookie",
		}},
	}

	app := &App{}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	if err := app.probeAccountProtocolHealth(ctx, cfg, session, ""); err != nil {
		t.Fatalf("expected context abort probe to be ignored, got %v", err)
	}
}

func TestProbeAccountProtocolHealthCachesProbeSuccessWithinTTL(t *testing.T) {
	cfg := defaultConfig()
	cfg.Dispatch.ProbeCacheTTLSeconds = 45
	state := &ServerState{DispatchProbeCache: newProbeCache()}
	app := &App{
		State: state,
	}
	callCount := 0
	app.accountProtocolProbeOverride = func(ctx context.Context, cfg AppConfig, session SessionInfo) error {
		callCount++
		return nil
	}

	session := SessionInfo{
		UserEmail: "alice@example.com",
	}
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		if err := app.probeAccountProtocolHealth(ctx, cfg, session, "alice@example.com"); err != nil {
			t.Fatalf("probe call %d failed: %v", i+1, err)
		}
	}
	if callCount != 1 {
		t.Fatalf("expected one upstream probe call within ttl window, got %d", callCount)
	}
}

func TestProbeAccountProtocolHealthReprobesAfterFailure(t *testing.T) {
	cfg := defaultConfig()
	cfg.Dispatch.ProbeCacheTTLSeconds = 45
	state := &ServerState{DispatchProbeCache: newProbeCache()}
	app := &App{
		State: state,
	}
	callCount := 0
	app.accountProtocolProbeOverride = func(ctx context.Context, cfg AppConfig, session SessionInfo) error {
		callCount++
		if callCount == 1 {
			return errors.New("probe failed once")
		}
		return nil
	}

	session := SessionInfo{
		UserEmail: "alice@example.com",
	}
	ctx := context.Background()

	if err := app.probeAccountProtocolHealth(ctx, cfg, session, "alice@example.com"); err == nil {
		t.Fatalf("expected first probe failure")
	}
	if err := app.probeAccountProtocolHealth(ctx, cfg, session, "alice@example.com"); err != nil {
		t.Fatalf("expected second probe to run and succeed, got %v", err)
	}
	if callCount != 2 {
		t.Fatalf("expected second request to reprobe after failure, got callCount=%d", callCount)
	}
}

func TestRunPromptWithSessionIncrementsWreqClientMetric(t *testing.T) {
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
	session := SessionInfo{
		ClientVersion: "test-client-version",
		UserID:        "test-user",
		SpaceID:       "test-space",
		Cookies: []ProbeCookie{{
			Name:  "token_v2",
			Value: "test-cookie",
		}},
	}
	beforeStandard := int64(0)
	if v := wreqClientNewTotalMetric.Get("standard"); v != nil {
		beforeStandard = v.(*expvar.Int).Value()
	}
	beforeStreaming := int64(0)
	if v := wreqClientNewTotalMetric.Get("streaming"); v != nil {
		beforeStreaming = v.(*expvar.Int).Value()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = app.runPromptWithSession(ctx, cfg, session, "", PromptRunRequest{Prompt: "hi"}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
	_, err = app.runPromptWithSession(ctx, cfg, session, "", PromptRunRequest{Prompt: "hi"}, func(string) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled for streaming run, got %v", err)
	}

	afterStandard := int64(0)
	if v := wreqClientNewTotalMetric.Get("standard"); v != nil {
		afterStandard = v.(*expvar.Int).Value()
	}
	afterStreaming := int64(0)
	if v := wreqClientNewTotalMetric.Get("streaming"); v != nil {
		afterStreaming = v.(*expvar.Int).Value()
	}
	if afterStandard-beforeStandard < 1 {
		t.Fatalf("expected standard metric increment, before=%d after=%d", beforeStandard, afterStandard)
	}
	if afterStreaming-beforeStreaming < 1 {
		t.Fatalf("expected streaming metric increment, before=%d after=%d", beforeStreaming, afterStreaming)
	}
}

func TestConsumeNDJSONStreamWithIdleCloseReturnsUpstreamErrorStep(t *testing.T) {
	threadID := "thread-error"
	messageID := "msg-error"
	recordMap := buildThreadErrorRecordMap(threadID, "test-space", messageID, "AI inference is not allowed.", "trust-rule-denied", "trace-error")
	line, err := json.Marshal(map[string]any{
		"type":      "record-map",
		"recordMap": recordMap,
	})
	if err != nil {
		t.Fatalf("marshal ndjson line failed: %v", err)
	}

	_, gotErr := consumeNDJSONStreamWithIdleClose(io.NopCloser(strings.NewReader(string(line)+"\n")), threadID, InferenceStreamSink{}, 0)
	if gotErr == nil || !strings.Contains(gotErr.Error(), "AI inference is not allowed") {
		t.Fatalf("expected upstream error step, got %v", gotErr)
	}
}

func TestConsumeNDJSONStreamWithIdleCloseParsesFinalLineWithoutTrailingNewline(t *testing.T) {
	threadID := "thread-error-no-newline"
	messageID := "msg-error-no-newline"
	recordMap := buildThreadErrorRecordMap(threadID, "test-space", messageID, "AI inference is not allowed.", "trust-rule-denied", "trace-error")
	line, err := json.Marshal(map[string]any{
		"type":      "record-map",
		"recordMap": recordMap,
	})
	if err != nil {
		t.Fatalf("marshal ndjson line failed: %v", err)
	}

	_, gotErr := consumeNDJSONStreamWithIdleClose(io.NopCloser(strings.NewReader(string(line))), threadID, InferenceStreamSink{}, 0)
	if gotErr == nil || !strings.Contains(gotErr.Error(), "AI inference is not allowed") {
		t.Fatalf("expected upstream error step without trailing newline, got %v", gotErr)
	}
}

func TestConsumeNDJSONStreamWithIdleCloseRejectsOversizedLine(t *testing.T) {
	threadID := "thread-oversized-line"
	oversizedLine := append(bytes.Repeat([]byte("a"), ndjsonMaxLineBytes+1), '\n')

	_, gotErr := consumeNDJSONStreamWithIdleClose(io.NopCloser(bytes.NewReader(oversizedLine)), threadID, InferenceStreamSink{}, 0)
	if gotErr == nil {
		t.Fatalf("expected oversized NDJSON line error, got nil")
	}
	if !errors.Is(gotErr, errNDJSONLineTooLarge) {
		t.Fatalf("expected errNDJSONLineTooLarge, got %v", gotErr)
	}
}

func TestConsumeNDJSONStreamParsesFinalLineWithoutTrailingNewline(t *testing.T) {
	threadID := "thread-error-no-newline-fallback"
	messageID := "msg-error-no-newline-fallback"
	recordMap := buildThreadErrorRecordMap(threadID, "test-space", messageID, "AI inference is not allowed.", "trust-rule-denied", "trace-error")
	line, err := json.Marshal(map[string]any{
		"type":      "record-map",
		"recordMap": recordMap,
	})
	if err != nil {
		t.Fatalf("marshal ndjson line failed: %v", err)
	}

	_, gotErr := consumeNDJSONStream(strings.NewReader(string(line)), threadID, InferenceStreamSink{})
	if gotErr == nil || !strings.Contains(gotErr.Error(), "AI inference is not allowed") {
		t.Fatalf("expected upstream error step without trailing newline, got %v", gotErr)
	}
}

func TestConsumeNDJSONStreamRejectsOversizedLine(t *testing.T) {
	threadID := "thread-oversized-line-fallback"
	oversizedLine := append(bytes.Repeat([]byte("a"), ndjsonMaxLineBytes+1), '\n')

	_, gotErr := consumeNDJSONStream(bytes.NewReader(oversizedLine), threadID, InferenceStreamSink{})
	if gotErr == nil {
		t.Fatalf("expected oversized NDJSON line error, got nil")
	}
	if !errors.Is(gotErr, errNDJSONLineTooLarge) {
		t.Fatalf("expected errNDJSONLineTooLarge, got %v", gotErr)
	}
}

func TestRunPromptReturnsUpstreamErrorStep(t *testing.T) {
	messageID := "msg-error"
	var recordMap map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/runInferenceTranscript":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode runInference payload failed: %v", err)
			}
			threadID := strings.TrimSpace(stringValue(payload["threadId"]))
			if threadID == "" {
				t.Fatalf("runInference payload missing threadId")
			}
			recordMap = buildThreadErrorRecordMap(threadID, "test-space", messageID, "AI inference is not allowed.", "trust-rule-denied", "trace-error")
			w.Header().Set("Content-Type", "application/x-ndjson")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"type":      "record-map",
				"recordMap": recordMap,
			})
		case "/api/v3/syncRecordValuesSpaceInitial":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read sync body failed: %v", err)
			}
			text := string(body)
			w.Header().Set("Content-Type", "application/json")
			switch {
			case strings.Contains(text, "\"table\":\"thread_message\""):
				_ = json.NewEncoder(w).Encode(map[string]any{
					"recordMap": map[string]any{
						"thread_message": recordMap["thread_message"],
					},
				})
			case strings.Contains(text, "\"table\":\"thread\""):
				_ = json.NewEncoder(w).Encode(map[string]any{
					"recordMap": map[string]any{
						"thread": recordMap["thread"],
					},
				})
			default:
				t.Fatalf("unexpected sync payload: %s", text)
			}
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := newBestEffortTestClient(server.URL)
	client.Session.UserName = "tester"
	client.Session.SpaceName = "tester-space"
	client.Session.SpaceViewID = "space-view"

	_, err := client.RunPrompt(context.Background(), PromptRunRequest{
		Prompt:       "hello",
		PublicModel:  "opus-4.7",
		NotionModel:  "apricot-sorbet-medium",
		UseWebSearch: false,
	})
	if err == nil || !strings.Contains(err.Error(), "AI inference is not allowed") {
		t.Fatalf("expected upstream error step, got %v", err)
	}
}
