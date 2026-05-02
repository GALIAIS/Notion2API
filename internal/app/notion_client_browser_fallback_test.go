package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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

func TestClassifyBrowserHelperExecErrorBranches(t *testing.T) {
	t.Run("err not found maps to unavailable", func(t *testing.T) {
		err := classifyBrowserHelperExecError(context.Background(), "node", exec.ErrNotFound, "")
		var unavailable *browserHelperUnavailableError
		if !errors.As(err, &unavailable) {
			t.Fatalf("expected browserHelperUnavailableError, got %T %v", err, err)
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Fatalf("expected not found message, got %v", err)
		}
	})

	t.Run("context cancellation takes precedence", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := classifyBrowserHelperExecError(ctx, "node", errors.New("exec failed"), "stderr text")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context canceled, got %v", err)
		}
	})

	t.Run("missing node-wreq maps to unavailable", func(t *testing.T) {
		err := classifyBrowserHelperExecError(context.Background(), "node", errors.New("exec failed"), "Error: Cannot find module 'node-wreq'")
		var unavailable *browserHelperUnavailableError
		if !errors.As(err, &unavailable) {
			t.Fatalf("expected browserHelperUnavailableError, got %T %v", err, err)
		}
		if !strings.Contains(err.Error(), "missing node-wreq module") {
			t.Fatalf("unexpected unavailable message: %v", err)
		}
	})

	t.Run("generic stderr path", func(t *testing.T) {
		err := classifyBrowserHelperExecError(context.Background(), "node", errors.New("exec failed"), "boom")
		if err == nil || !strings.Contains(err.Error(), "node helper failed: boom") {
			t.Fatalf("expected generic helper failed error, got %v", err)
		}
	})
}

func TestSupportsBrowserRunInferenceFallbackMatrix(t *testing.T) {
	baseCfg := defaultConfig()
	baseCfg.APIKey = "test-key"
	baseCfg.UpstreamBaseURL = "https://www.notion.so"
	baseCfg.UpstreamOrigin = "https://www.notion.so"
	client := newNotionAIClient(SessionInfo{
		ClientVersion: "test-client-version",
		UserID:        "test-user",
		SpaceID:       "test-space",
		Cookies:       []ProbeCookie{{Name: "token_v2", Value: "cookie"}},
	}, baseCfg, "")

	if !client.supportsBrowserRunInferenceFallback() {
		t.Fatalf("expected fallback support for default https notion upstream")
	}

	clientWithOverride := *client
	clientWithOverride.browserRunInferenceFallback = func(ctx context.Context, payload map[string]any) (string, error) {
		return "", nil
	}
	if !clientWithOverride.supportsBrowserRunInferenceFallback() {
		t.Fatalf("expected explicit fallback override to force support")
	}

	hostHeaderCfg := baseCfg
	hostHeaderCfg.UpstreamHost = "example.com"
	hostHeaderClient := newNotionAIClient(client.Session, hostHeaderCfg, "")
	if hostHeaderClient.supportsBrowserRunInferenceFallback() {
		t.Fatalf("expected fallback disabled when upstream host header override is set")
	}

	localCfg := baseCfg
	localCfg.UpstreamBaseURL = "https://127.0.0.1:8443"
	localCfg.UpstreamOrigin = "https://127.0.0.1:8443"
	localClient := newNotionAIClient(client.Session, localCfg, "")
	if localClient.supportsBrowserRunInferenceFallback() {
		t.Fatalf("expected fallback disabled for local upstream")
	}

	httpCfg := baseCfg
	httpCfg.UpstreamBaseURL = "http://www.notion.so"
	httpCfg.UpstreamOrigin = "http://www.notion.so"
	httpClient := newNotionAIClient(client.Session, httpCfg, "")
	if httpClient.supportsBrowserRunInferenceFallback() {
		t.Fatalf("expected fallback disabled for non-https upstream")
	}
}

func TestEnsureHelperScriptFileStageAStablePath(t *testing.T) {
	tempDir := t.TempDir()
	script := "console.log('stage-a-stable')\n"

	gotPath, err := ensureHelperScriptFile(tempDir, ".cjs", script)
	if err != nil {
		t.Fatalf("ensureHelperScriptFile failed: %v", err)
	}

	sum := sha256.Sum256([]byte(script))
	wantPath := filepath.Join(tempDir, fmt.Sprintf("notion-helper-%x.cjs", sum))
	if gotPath != wantPath {
		t.Fatalf("script path mismatch: got %q want %q", gotPath, wantPath)
	}

	content, err := os.ReadFile(gotPath)
	if err != nil {
		t.Fatalf("read script failed: %v", err)
	}
	if string(content) != script {
		t.Fatalf("script content mismatch: got %q want %q", string(content), script)
	}
}

func TestEnsureHelperScriptFileStageARepeatedCallsKeepSingleScript(t *testing.T) {
	tempDir := t.TempDir()
	script := "console.log('stage-a-repeat')\n"

	for i := 0; i < 100; i++ {
		if _, err := ensureHelperScriptFile(tempDir, ".cjs", script); err != nil {
			t.Fatalf("iteration %d ensureHelperScriptFile failed: %v", i+1, err)
		}
	}

	matches, err := filepath.Glob(filepath.Join(tempDir, "notion-helper-*.cjs"))
	if err != nil {
		t.Fatalf("glob helper scripts failed: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly 1 helper script, got %d (%v)", len(matches), matches)
	}
}

func TestBrowserHelperNodeEnvForConfigIncludesPoolSize(t *testing.T) {
	cfg := defaultConfig()
	cfg.Browser.HelperPoolSize = 3
	env := browserHelperNodeEnvForConfig(cfg)
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "NOTION2API_BROWSER_HELPER_POOL_SIZE=3") {
		t.Fatalf("expected helper pool size env to include configured size, got %v", env)
	}
}

func TestConfiguredBrowserHelperPoolSizeBoundsAndFallback(t *testing.T) {
	if got := configuredBrowserHelperPoolSize(normalizeConfig(AppConfig{
		Browser: BrowserConfig{HelperPoolSize: 99},
	})); got != 8 {
		t.Fatalf("expected oversized config to clamp to 8, got %d", got)
	}
	if got := configuredBrowserHelperPoolSize(normalizeConfig(AppConfig{
		Browser: BrowserConfig{HelperPoolSize: -2},
	})); got < 1 || got > 8 {
		t.Fatalf("expected fallback cpu-based pool size in [1,8], got %d", got)
	}
}

func TestBrowserHelperNodeEnvForConfigOmitsPoolSizeWhenDisabled(t *testing.T) {
	cfg := defaultConfig()
	cfg.Browser.HelperPoolSize = 0
	env := browserHelperNodeEnvForConfig(cfg)
	for _, item := range env {
		if strings.Contains(item, "NOTION2API_BROWSER_HELPER_POOL_SIZE=") {
			t.Fatalf("expected pool-size env to be omitted for disabled pool, got %v", env)
		}
	}
}

func TestBrowserHelperNodeEnvForConfigHonorsEnvOverride(t *testing.T) {
	t.Setenv("NOTION2API_BROWSER_HELPER_POOL_SIZE", "5")
	cfg := defaultConfig()
	cfg.Browser.HelperPoolSize = 2
	env := browserHelperNodeEnvForConfig(cfg)
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "NOTION2API_BROWSER_HELPER_POOL_SIZE=5") {
		t.Fatalf("expected env override to win, got %v", env)
	}
	if strings.Contains(joined, "NOTION2API_BROWSER_HELPER_POOL_SIZE=2") {
		t.Fatalf("expected config value to be ignored when env override is set, got %v", env)
	}
}

func TestExecuteHelperSubprocessPooledSpawnsWorkersAndReusesPool(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skipf("skip pooled helper runtime test: node unavailable: %v", err)
	}

	resetMetricsForTest()
	resetBrowserHelperPoolsForTest()
	t.Cleanup(func() {
		resetBrowserHelperPoolsForTest()
	})

	script := `
const poolMode = String(process.env.NOTION2API_BROWSER_HELPER_MODE || '').trim().toLowerCase() === 'pool';
function writeFrame(body) {
  const header = Buffer.allocUnsafe(4);
  header.writeUInt32LE(body.length, 0);
  process.stdout.write(header);
  process.stdout.write(body);
}
if (!poolMode) {
  process.stdin.setEncoding('utf8');
  let raw = '';
  process.stdin.on('data', (chunk) => { raw += chunk; });
  process.stdin.on('end', () => {
    const parsed = JSON.parse(raw || '{}');
    process.stdout.write(JSON.stringify({ ok: true, echo: parsed.echo || '' }));
  });
} else {
  let pending = Buffer.alloc(0);
  const queue = [];
  let draining = false;
  async function drain() {
    if (draining) return;
    draining = true;
    while (queue.length > 0) {
      const payload = queue.shift();
      const parsed = JSON.parse(payload.toString('utf8'));
      const out = Buffer.from(JSON.stringify({
        status: 200,
        content_type: 'application/x-ndjson',
        text: JSON.stringify({ type: 'record-map', echo: parsed.echo || '' }),
      }));
      writeFrame(out);
    }
    draining = false;
  }
  process.stdin.on('data', (chunk) => {
    const incoming = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk);
    pending = Buffer.concat([pending, incoming]);
    while (pending.length >= 4) {
      const n = pending.readUInt32LE(0);
      if (pending.length < 4 + n) break;
      queue.push(pending.subarray(4, 4 + n));
      pending = pending.subarray(4 + n);
    }
    void drain();
  });
  process.stdin.on('end', () => process.exit(0));
}
`

	extraEnv := []string{browserHelperPoolSizeEnvKey + "=2"}

	out1, err := executeHelperSubprocess(context.Background(), "node", ".cjs", script, []byte(`{"echo":"one"}`), extraEnv)
	if err != nil {
		t.Fatalf("first pooled executeHelperSubprocess failed: %v", err)
	}
	var resp1 map[string]any
	if err := json.Unmarshal(out1, &resp1); err != nil {
		t.Fatalf("unmarshal first pooled response failed: %v", err)
	}
	text1 := stringValue(resp1["text"])
	if !strings.Contains(text1, `"echo":"one"`) {
		t.Fatalf("unexpected first pooled response text: %q", text1)
	}

	browserPoolWorkerMu.Lock()
	spawnAfterFirst := browserPoolWorkerTotal
	browserPoolWorkerMu.Unlock()
	if spawnAfterFirst != 2 {
		t.Fatalf("expected two pool workers after first call, got %d", spawnAfterFirst)
	}
	t.Logf("pooled helper worker spawns after first call: %d", spawnAfterFirst)

	out2, err := executeHelperSubprocess(context.Background(), "node", ".cjs", script, []byte(`{"echo":"two"}`), extraEnv)
	if err != nil {
		t.Fatalf("second pooled executeHelperSubprocess failed: %v", err)
	}
	var resp2 map[string]any
	if err := json.Unmarshal(out2, &resp2); err != nil {
		t.Fatalf("unmarshal second pooled response failed: %v", err)
	}
	text2 := stringValue(resp2["text"])
	if !strings.Contains(text2, `"echo":"two"`) {
		t.Fatalf("unexpected second pooled response text: %q", text2)
	}

	browserPoolWorkerMu.Lock()
	spawnAfterSecond := browserPoolWorkerTotal
	browserPoolWorkerMu.Unlock()
	if spawnAfterSecond != spawnAfterFirst {
		t.Fatalf("expected pooled workers to be reused (no extra spawns), first=%d second=%d", spawnAfterFirst, spawnAfterSecond)
	}
	t.Logf("pooled helper worker spawns after second call: %d", spawnAfterSecond)
}

func TestExecuteHelperSubprocessPooledAllowsNilContext(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skipf("skip nil-context pooled helper test: node unavailable: %v", err)
	}

	resetBrowserHelperPoolsForTest()
	t.Cleanup(func() {
		resetBrowserHelperPoolsForTest()
	})

	script := `
const poolMode = String(process.env.NOTION2API_BROWSER_HELPER_MODE || '').trim().toLowerCase() === 'pool';
function writeFrame(body) {
  const header = Buffer.allocUnsafe(4);
  header.writeUInt32LE(body.length, 0);
  process.stdout.write(header);
  process.stdout.write(body);
}
if (poolMode) {
  let pending = Buffer.alloc(0);
  process.stdin.on('data', (chunk) => {
    const incoming = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk);
    pending = Buffer.concat([pending, incoming]);
    while (pending.length >= 4) {
      const n = pending.readUInt32LE(0);
      if (pending.length < 4 + n) break;
      pending = pending.subarray(4 + n);
      const out = Buffer.from(JSON.stringify({
        status: 200,
        content_type: 'application/x-ndjson',
        text: JSON.stringify({ type: 'record-map', ok: true }),
      }));
      writeFrame(out);
    }
  });
  process.stdin.on('end', () => process.exit(0));
} else {
  process.exit(2);
}
`
	extraEnv := []string{browserHelperPoolSizeEnvKey + "=2"}
	out, err := executeHelperSubprocess(nil, "node", ".cjs", script, []byte(`{"x":1}`), extraEnv)
	if err != nil {
		t.Fatalf("executeHelperSubprocess with nil context failed: %v", err)
	}
	var resp map[string]any
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal pooled nil-context response failed: %v", err)
	}
	if got := stringValue(resp["content_type"]); got != "application/x-ndjson" {
		t.Fatalf("unexpected content_type: %q", got)
	}
	if !strings.Contains(stringValue(resp["text"]), `"ok":true`) {
		t.Fatalf("unexpected text payload: %q", stringValue(resp["text"]))
	}
}
