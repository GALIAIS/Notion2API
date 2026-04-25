package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestResolvePlaywrightBrowserExecutablePathPrefersCHROMEBIN(t *testing.T) {
	tmpDir := t.TempDir()
	chromeBin := filepath.Join(tmpDir, "chrome-bin")
	chromiumBin := filepath.Join(tmpDir, "chromium-bin")
	if err := os.WriteFile(chromeBin, []byte(""), 0o644); err != nil {
		t.Fatalf("write chrome bin failed: %v", err)
	}
	if err := os.WriteFile(chromiumBin, []byte(""), 0o644); err != nil {
		t.Fatalf("write chromium bin failed: %v", err)
	}

	t.Setenv("CHROME_BIN", chromeBin)
	t.Setenv("CHROMIUM_BIN", chromiumBin)

	if got := resolvePlaywrightBrowserExecutablePath(); got != chromeBin {
		t.Fatalf("resolvePlaywrightBrowserExecutablePath() = %q, want %q", got, chromeBin)
	}
}

func TestResolvePlaywrightBrowserExecutablePathSkipsMissingCHROMEBIN(t *testing.T) {
	tmpDir := t.TempDir()
	chromiumBin := filepath.Join(tmpDir, "chromium-bin")
	if err := os.WriteFile(chromiumBin, []byte(""), 0o644); err != nil {
		t.Fatalf("write chromium bin failed: %v", err)
	}

	t.Setenv("CHROME_BIN", filepath.Join(tmpDir, "missing-chrome"))
	t.Setenv("CHROMIUM_BIN", chromiumBin)

	if got := resolvePlaywrightBrowserExecutablePath(); got != chromiumBin {
		t.Fatalf("resolvePlaywrightBrowserExecutablePath() = %q, want %q", got, chromiumBin)
	}
}

func TestNormalizeBrowserTransportLocale(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty", input: "", want: ""},
		{name: "accept language header", input: "en-US,en;q=0.9", want: "en-US"},
		{name: "underscore locale", input: "zh_CN", want: "zh-CN"},
		{name: "language only", input: "fr", want: "fr"},
		{name: "invalid wildcard", input: "*,en;q=0.5", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeBrowserTransportLocale(tt.input); got != tt.want {
				t.Fatalf("normalizeBrowserTransportLocale(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestBuildBrowserTransportRequestNormalizesLocaleAndIdleTimeout(t *testing.T) {
	client := newBestEffortTestClient("https://www.notion.so")

	request, err := buildBrowserTransportRequest(client, map[string]any{"threadId": "thread-test"})
	if err != nil {
		t.Fatalf("buildBrowserTransportRequest returned error: %v", err)
	}
	if got, want := request.Locale, "en-US"; got != want {
		t.Fatalf("request locale = %q, want %q", got, want)
	}
	if got, want := request.Headers["accept-language"], "en-US,en;q=0.9"; got != want {
		t.Fatalf("accept-language header = %q, want %q", got, want)
	}
	if got, want := request.IdleAfterAnswerMS, int(ndjsonIdleAfterAnswerTimeout/time.Millisecond); got != want {
		t.Fatalf("idle_after_answer_ms = %d, want %d", got, want)
	}
}

func TestBuildBrowserTransportRequestUsesCookieLocale(t *testing.T) {
	client := newBestEffortTestClient("https://www.notion.so")
	client.Session.Cookies = append(client.Session.Cookies, ProbeCookie{Name: "NEXT_LOCALE", Value: "zh_CN"})

	request, err := buildBrowserTransportRequest(client, map[string]any{"threadId": "thread-test"})
	if err != nil {
		t.Fatalf("buildBrowserTransportRequest returned error: %v", err)
	}
	if got, want := request.Locale, "zh-CN"; got != want {
		t.Fatalf("request locale = %q, want %q", got, want)
	}
}

func TestRunInferenceTranscriptInBrowserUsesPlaywrightOnly(t *testing.T) {
	client := newBestEffortTestClient("https://www.notion.so")

	origPlaywright := runPlaywrightBrowserFallback
	defer func() {
		runPlaywrightBrowserFallback = origPlaywright
	}()

	playwrightCalls := 0
	runPlaywrightBrowserFallback = func(ctx context.Context, _ *NotionAIClient, _ map[string]any) (string, error) {
		playwrightCalls++
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return "{\"type\":\"agent-inference\"}\n", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()

	body, err := runInferenceTranscriptInBrowser(ctx, client, map[string]any{"threadId": "thread-test"})
	if err != nil {
		t.Fatalf("runInferenceTranscriptInBrowser returned error: %v", err)
	}
	if body == "" {
		t.Fatalf("expected fallback body")
	}
	if playwrightCalls != 1 {
		t.Fatalf("expected playwright fallback once, got %d", playwrightCalls)
	}
}

func TestClassifyBrowserHelperExecErrorPrefersContextDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := classifyBrowserHelperExecError(ctx, "node", errors.New("signal: killed"), "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestNodePlaywrightBrowserHelperScriptClosesResourcesInFinally(t *testing.T) {
	script := nodePlaywrightBrowserHelperScript()

	required := []string{
		"let browser;",
		"let context;",
		"let page;",
		"const closeQuietly = async (handle) => {",
		"try {",
		"finally {",
		"await closeQuietly(page);",
		"await closeQuietly(context);",
		"await closeQuietly(browser);",
	}
	for _, needle := range required {
		if !strings.Contains(script, needle) {
			t.Fatalf("node helper script missing cleanup fragment %q", needle)
		}
	}
}

func TestPythonPlaywrightBrowserHelperScriptClosesResourcesInFinally(t *testing.T) {
	script := pythonPlaywrightBrowserHelperScript()

	required := []string{
		"browser = None",
		"context = None",
		"page = None",
		"def close_quietly(handle):",
		"try:",
		"finally:",
		"close_quietly(page)",
		"close_quietly(context)",
		"close_quietly(browser)",
	}
	for _, needle := range required {
		if !strings.Contains(script, needle) {
			t.Fatalf("python helper script missing cleanup fragment %q", needle)
		}
	}
}

func TestNewBrowserHelperCommandConfiguresCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	cmd := newBrowserHelperCommand(ctx, "node", "helper.cjs", []byte("{}"), []string{"CODEX_TEST_ENV=1"})
	if cmd == nil {
		t.Fatalf("expected command")
	}
	if cmd.Cancel == nil {
		t.Fatalf("expected custom cancel handler")
	}
	if got, want := cmd.WaitDelay, browserHelperCancelWaitDelay; got != want {
		t.Fatalf("WaitDelay = %s, want %s", got, want)
	}
	foundEnv := false
	for _, entry := range cmd.Env {
		if entry == "CODEX_TEST_ENV=1" {
			foundEnv = true
			break
		}
	}
	if !foundEnv {
		t.Fatalf("expected extra env to be appended")
	}
	if runtime.GOOS != "windows" && !browserHelperUsesProcessGroupKill(cmd) {
		t.Fatalf("expected process-group cleanup on %s", runtime.GOOS)
	}
}

func TestNewBrowserHelperCommandKillsProcessGroupOnCancel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group cleanup test is linux/unix specific")
	}

	tmpDir := t.TempDir()
	pidFile := filepath.Join(tmpDir, "child.pid")
	scriptPath := filepath.Join(tmpDir, "spawn-child.sh")
	script := fmt.Sprintf("#!/bin/sh\nsleep 30 &\necho $! > %q\nwait\n", pidFile)
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := newBrowserHelperCommand(ctx, "sh", scriptPath, []byte("{}"), nil)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start command failed: %v", err)
	}

	childPID := waitForBrowserHelperChildPID(t, pidFile)
	if alive, err := browserHelperProcessAlive(childPID); err != nil {
		t.Fatalf("check child process %d failed: %v", childPID, err)
	} else if !alive {
		t.Fatalf("expected child process %d to be alive", childPID)
	}

	cancel()
	_ = cmd.Wait()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		alive, err := browserHelperProcessAlive(childPID)
		if err != nil {
			t.Fatalf("check child process %d failed: %v", childPID, err)
		}
		if !alive {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("expected child process %d to exit after cancellation", childPID)
}

func waitForBrowserHelperChildPID(t *testing.T, pidFile string) int {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(pidFile)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr != nil {
				t.Fatalf("parse child pid failed: %v", parseErr)
			}
			if pid <= 0 {
				t.Fatalf("invalid child pid %d", pid)
			}
			return pid
		}
		time.Sleep(25 * time.Millisecond)
	}
	return 0
}

func TestBuildBrowserTransportRequestAddsResinAccountHeaderWhenEnabled(t *testing.T) {
	cfg := defaultConfig()
	cfg.APIKey = "test-api-key"
	cfg.UpstreamBaseURL = "https://www.notion.so"
	cfg.UpstreamOrigin = "https://www.notion.so"
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
	}, cfg, "alice@example.com", true)

	request, err := buildBrowserTransportRequest(client, map[string]any{"threadId": "thread-test"})
	if err != nil {
		t.Fatalf("buildBrowserTransportRequest returned error: %v", err)
	}
	if got, want := request.Headers[defaultResinAccountHeader], "alice"; got != want {
		t.Fatalf("%s = %q, want %q", defaultResinAccountHeader, got, want)
	}
}
