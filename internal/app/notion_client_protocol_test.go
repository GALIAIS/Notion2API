package app

import (
	"context"
	"encoding/json"
	"expvar"
	"net/http"
	"net/http/httptest"
	"testing"
)

func resetNotionTransportCacheForTest() {
	notionTransportCache.mu.Lock()
	defer notionTransportCache.mu.Unlock()
	for _, transport := range notionTransportCache.items {
		if transport != nil {
			transport.CloseIdleConnections()
		}
	}
	notionTransportCache.items = map[notionHTTPTransportCacheKey]*http.Transport{}
	notionHTTPTransportCacheMetric.Init()
}

func newProtocolTestClient(cfg AppConfig) *NotionAIClient {
	cfg.APIKey = "test-api-key"
	if cfg.UpstreamBaseURL == "" {
		cfg.UpstreamBaseURL = "https://www.notion.so"
	}
	if cfg.UpstreamOrigin == "" {
		cfg.UpstreamOrigin = cfg.UpstreamBaseURL
	}
	return newNotionAIClient(SessionInfo{
		ClientVersion: "test-client-version",
		UserID:        "test-user",
		UserName:      "tester",
		UserEmail:     "tester@example.com",
		SpaceID:       "test-space",
		SpaceName:     "test-space-name",
		SpaceViewID:   "test-space-view",
		Cookies: []ProbeCookie{{
			Name:  "token_v2",
			Value: "test-cookie",
		}},
	}, cfg, "")
}

func transcriptStepValue(t *testing.T, payload map[string]any, stepType string) map[string]any {
	t.Helper()
	steps, ok := payload["transcript"].([]map[string]any)
	if !ok {
		t.Fatalf("payload transcript missing or wrong type: %#v", payload["transcript"])
	}
	for _, step := range steps {
		if stringValue(step["type"]) == stepType {
			return mapValue(step["value"])
		}
	}
	t.Fatalf("transcript step %q missing", stepType)
	return nil
}

func TestBuildDefaultWorkflowConfigValueMatchesCurrentWebDefaults(t *testing.T) {
	client := newProtocolTestClient(defaultConfig())

	value := client.buildDefaultWorkflowConfigValue("workflow", true, "")

	if !booleanValue(value["enableAgentAutomations"]) {
		t.Fatalf("expected enableAgentAutomations=true")
	}
	if !booleanValue(value["enableAgentIntegrations"]) {
		t.Fatalf("expected enableAgentIntegrations=true")
	}
	if !booleanValue(value["enableCustomAgents"]) {
		t.Fatalf("expected enableCustomAgents=true")
	}
	if !booleanValue(value["enableAgentDiffs"]) {
		t.Fatalf("expected enableAgentDiffs=true")
	}
	if !booleanValue(value["enableAgentGenerateImage"]) {
		t.Fatalf("expected enableAgentGenerateImage=true")
	}
	if !booleanValue(value["enableMailExplicitToolCalls"]) {
		t.Fatalf("expected enableMailExplicitToolCalls=true")
	}
	if !booleanValue(value["useRulePrioritization"]) {
		t.Fatalf("expected useRulePrioritization=true")
	}
	if !booleanValue(value["useWebSearch"]) {
		t.Fatalf("expected useWebSearch=true")
	}
	if booleanValue(value["useReadOnlyMode"]) {
		t.Fatalf("expected useReadOnlyMode=false")
	}
	if !booleanValue(value["enableUpdatePageAutofixer"]) {
		t.Fatalf("expected enableUpdatePageAutofixer=true")
	}
	if !booleanValue(value["enableUpdatePageOrderUpdates"]) {
		t.Fatalf("expected enableUpdatePageOrderUpdates=true")
	}
	if !booleanValue(value["enableAgentSupportPropertyReorder"]) {
		t.Fatalf("expected enableAgentSupportPropertyReorder=true")
	}
	if !booleanValue(value["enableAgentAskSurvey"]) {
		t.Fatalf("expected enableAgentAskSurvey=true")
	}
}

func TestBuildInferencePayloadPlacesSelectedModelInConfigAndCreatedSource(t *testing.T) {
	client := newProtocolTestClient(defaultConfig())

	payload, _ := client.buildInferencePayload(PromptRunRequest{
		Prompt:       "hello",
		NotionModel:  "apricot-sorbet-medium",
		UseWebSearch: true,
	}, "thread-1", nil)

	if got := stringValue(payload["createdSource"]); got != "ai_module" {
		t.Fatalf("createdSource mismatch: got %q want %q", got, "ai_module")
	}
	configValue := transcriptStepValue(t, payload, "config")
	if got := stringValue(configValue["model"]); got != "apricot-sorbet-medium" {
		t.Fatalf("config model mismatch: got %q want %q", got, "apricot-sorbet-medium")
	}
	if !booleanValue(configValue["modelFromUser"]) {
		t.Fatalf("expected config modelFromUser=true")
	}
	debugOverrides := mapValue(payload["debugOverrides"])
	if _, exists := debugOverrides["model"]; exists {
		t.Fatalf("expected debugOverrides.model to be omitted, got %#v", debugOverrides["model"])
	}
}

func TestMarkInferenceTranscriptSeenIncludesSpaceID(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/markInferenceTranscriptSeen" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body failed: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.UpstreamBaseURL = server.URL
	cfg.UpstreamOrigin = server.URL
	client := newProtocolTestClient(cfg)

	if err := client.markInferenceTranscriptSeen(context.Background(), "thread-1"); err != nil {
		t.Fatalf("markInferenceTranscriptSeen failed: %v", err)
	}
	if got := stringValue(gotBody["threadId"]); got != "thread-1" {
		t.Fatalf("threadId mismatch: got %q want %q", got, "thread-1")
	}
	if got := stringValue(gotBody["spaceId"]); got != "test-space" {
		t.Fatalf("spaceId mismatch: got %q want %q", got, "test-space")
	}
}

func TestSaveContinuationScaffoldOmitsUnretryableErrorBehavior(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/saveTransactionsFanout" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body failed: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.UpstreamBaseURL = server.URL
	cfg.UpstreamOrigin = server.URL
	client := newProtocolTestClient(cfg)

	if _, err := client.saveContinuationScaffold(context.Background(), "thread-1", "hello", &continuationTurnDraft{}); err != nil {
		t.Fatalf("saveContinuationScaffold failed: %v", err)
	}
	if _, exists := gotBody["unretryable_error_behavior"]; exists {
		t.Fatalf("expected saveTransactionsFanout payload to omit unretryable_error_behavior")
	}
}

func TestPostJSONResponseAddsResinAccountHeaderWhenEnabled(t *testing.T) {
	capturedHeader := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeader = r.Header.Get(defaultResinAccountHeader)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer server.Close()

	cfg := defaultConfig()
	cfg.UpstreamBaseURL = server.URL
	cfg.UpstreamOrigin = server.URL
	cfg.ProxyMode = proxyModeResinForward
	cfg.ResinEnabled = true
	cfg.ResinURL = "http://127.0.0.1:2260/my-token"
	cfg.ResinPlatform = "Default"
	cfg.Accounts = []NotionAccount{{
		Email:              "alice@example.com",
		StickyProxyAccount: "alice",
	}}

	client := newNotionAIClientWithMode(SessionInfo{
		ClientVersion: "test-client-version",
		UserID:        "test-user",
		SpaceID:       "test-space",
		Cookies: []ProbeCookie{{
			Name:  "token_v2",
			Value: "test-cookie",
		}},
	}, cfg, "alice@example.com", false)
	client.HTTPClient.Transport = &http.Transport{}
	client.AccountEmail = "alice@example.com"

	if _, err := client.postJSONResponse(context.Background(), server.URL+"/api/v3/markInferenceTranscriptSeen", map[string]any{
		"threadId": "thread-test",
		"spaceId":  "test-space",
	}, "application/json"); err != nil {
		t.Fatalf("postJSONResponse failed: %v", err)
	}
	if got, want := capturedHeader, "alice"; got != want {
		t.Fatalf("%s = %q, want %q", defaultResinAccountHeader, got, want)
	}
}

func TestNewNotionAIClientWithModeReusesTransportForSameConfigAndAccount(t *testing.T) {
	resetNotionTransportCacheForTest()
	cfg := defaultConfig()
	cfg.APIKey = "test-api-key"
	session := SessionInfo{
		ClientVersion: "test-client-version",
		UserID:        "test-user",
		SpaceID:       "test-space",
		Cookies: []ProbeCookie{{
			Name:  "token_v2",
			Value: "test-cookie",
		}},
	}
	first := newNotionAIClientWithMode(session, cfg, "alice@example.com", false)
	second := newNotionAIClientWithMode(session, cfg, "alice@example.com", false)
	streaming := newNotionAIClientWithMode(session, cfg, "alice@example.com", true)

	if first.HTTPClient == nil || second.HTTPClient == nil || streaming.HTTPClient == nil {
		t.Fatalf("expected HTTP clients to be initialized")
	}
	if first.HTTPClient.Transport == nil || second.HTTPClient.Transport == nil || streaming.HTTPClient.Transport == nil {
		t.Fatalf("expected transports to be initialized")
	}
	if first.HTTPClient.Transport != second.HTTPClient.Transport {
		t.Fatalf("expected transport reuse for same account/config")
	}
	if first.HTTPClient.Transport != streaming.HTTPClient.Transport {
		t.Fatalf("expected streaming and standard clients to share transport cache")
	}
	if first.HTTPClient.Timeout <= 0 {
		t.Fatalf("expected non-streaming timeout to be configured")
	}
	if streaming.HTTPClient.Timeout != 0 {
		t.Fatalf("expected streaming client timeout to be disabled, got %s", streaming.HTTPClient.Timeout)
	}
}

func TestNewNotionAIClientWithModeSeparatesTransportWhenProxyPolicyDiffers(t *testing.T) {
	resetNotionTransportCacheForTest()
	cfg := defaultConfig()
	cfg.APIKey = "test-api-key"
	cfg.Accounts = []NotionAccount{
		{
			Email:     "alice@example.com",
			ProxyMode: proxyModeHTTP,
			ProxyURL:  "http://127.0.0.1:18080",
		},
		{
			Email:     "bob@example.com",
			ProxyMode: proxyModeHTTP,
			ProxyURL:  "http://127.0.0.1:28080",
		},
	}
	session := SessionInfo{
		ClientVersion: "test-client-version",
		UserID:        "test-user",
		SpaceID:       "test-space",
		Cookies: []ProbeCookie{{
			Name:  "token_v2",
			Value: "test-cookie",
		}},
	}
	alice := newNotionAIClientWithMode(session, cfg, "alice@example.com", false)
	bob := newNotionAIClientWithMode(session, cfg, "bob@example.com", false)

	if alice.HTTPClient == nil || bob.HTTPClient == nil {
		t.Fatalf("expected HTTP clients to be initialized")
	}
	if alice.HTTPClient.Transport == nil || bob.HTTPClient.Transport == nil {
		t.Fatalf("expected transports to be initialized")
	}
	if alice.HTTPClient.Transport == bob.HTTPClient.Transport {
		t.Fatalf("expected separate transports when account proxy policy differs")
	}
}

func TestCachedNotionHTTPTransportRecordsCacheMetrics(t *testing.T) {
	resetNotionTransportCacheForTest()
	cfg := defaultConfig()
	cfg.APIKey = "test-api-key"
	session := SessionInfo{
		ClientVersion: "test-client-version",
		UserID:        "test-user",
		SpaceID:       "test-space",
		Cookies: []ProbeCookie{{
			Name:  "token_v2",
			Value: "test-cookie",
		}},
	}
	_ = newNotionAIClientWithMode(session, cfg, "alice@example.com", false)
	_ = newNotionAIClientWithMode(session, cfg, "alice@example.com", false)
	_ = newNotionAIClientWithMode(session, cfg, "alice@example.com", true)

	mustAtLeast := func(label string, wantMin int64) {
		var got int64
		if v := notionHTTPTransportCacheMetric.Get(label); v != nil {
			got = v.(*expvar.Int).Value()
		}
		if got < wantMin {
			t.Fatalf("metric %s too small: got %d want >= %d", label, got, wantMin)
		}
	}
	mustAtLeast("miss_new", 1)
	mustAtLeast("hit_rlock", 1)
}

func BenchmarkNewNotionAIClientWithModeTransportCache(b *testing.B) {
	cfg := defaultConfig()
	cfg.APIKey = "test-api-key"
	session := SessionInfo{
		ClientVersion: "test-client-version",
		UserID:        "test-user",
		SpaceID:       "test-space",
		Cookies: []ProbeCookie{{
			Name:  "token_v2",
			Value: "test-cookie",
		}},
	}

	b.Run("warm_cache", func(b *testing.B) {
		resetNotionTransportCacheForTest()
		_ = newNotionAIClientWithMode(session, cfg, "alice@example.com", false)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			client := newNotionAIClientWithMode(session, cfg, "alice@example.com", false)
			if client == nil || client.HTTPClient == nil || client.HTTPClient.Transport == nil {
				b.Fatalf("expected client with transport")
			}
		}
	})

	b.Run("cold_cache_reset_each_iter", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			resetNotionTransportCacheForTest()
			client := newNotionAIClientWithMode(session, cfg, "alice@example.com", false)
			if client == nil || client.HTTPClient == nil || client.HTTPClient.Transport == nil {
				b.Fatalf("expected client with transport")
			}
		}
	})
}
