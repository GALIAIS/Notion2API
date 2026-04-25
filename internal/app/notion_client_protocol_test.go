package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
