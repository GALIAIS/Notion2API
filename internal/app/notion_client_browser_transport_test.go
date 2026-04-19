package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResolveBrowserExecutablePathPrefersCHROMEBIN(t *testing.T) {
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

	if got := resolveBrowserExecutablePath(); got != chromeBin {
		t.Fatalf("resolveBrowserExecutablePath() = %q, want %q", got, chromeBin)
	}
}

func TestResolveBrowserExecutablePathSkipsMissingCHROMEBIN(t *testing.T) {
	tmpDir := t.TempDir()
	chromiumBin := filepath.Join(tmpDir, "chromium-bin")
	if err := os.WriteFile(chromiumBin, []byte(""), 0o644); err != nil {
		t.Fatalf("write chromium bin failed: %v", err)
	}

	t.Setenv("CHROME_BIN", filepath.Join(tmpDir, "missing-chrome"))
	t.Setenv("CHROMIUM_BIN", chromiumBin)

	if got := resolveBrowserExecutablePath(); got != chromiumBin {
		t.Fatalf("resolveBrowserExecutablePath() = %q, want %q", got, chromiumBin)
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
