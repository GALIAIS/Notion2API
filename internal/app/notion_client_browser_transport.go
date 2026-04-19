package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	browserTransportBootstrapDelay = 2 * time.Second
	browserTransportViewportWidth  = 1440
	browserTransportViewportHeight = 900
	browserHelperCancelWaitDelay   = 2 * time.Second
)

const browserTransportFetchPageScript = `async (input) => {
  const response = await fetch(input.run_url, {
    method: 'POST',
    credentials: 'include',
    headers: input.headers,
    body: JSON.stringify(input.payload),
  });
  const result = {
    status: response.status,
    content_type: response.headers.get('content-type') || '',
    text: '',
  };
  const contentType = String(result.content_type || '').toLowerCase();
  if (!response.body || typeof response.body.getReader !== 'function' || !contentType.includes('application/x-ndjson')) {
    result.text = await response.text();
    return result;
  }

  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  const idleAfterAnswerMs = Math.max(Number(input.idle_after_answer_ms || 0), 0);
  let pending = '';
  let sawAnswer = false;
  let sawTerminal = false;

  const markLineState = (line) => {
    if (!line) {
      return;
    }
    try {
      const parsed = JSON.parse(line);
      if (String(parsed.type || '').toLowerCase() !== 'agent-inference' || !Array.isArray(parsed.value)) {
        return;
      }
      const hasVisibleText = parsed.value.some((item) => {
        const entryType = String((item && item.type) || '').toLowerCase();
        const content = String((item && item.content) || '');
        return entryType === 'text' && content.trim() !== '';
      });
      if (!hasVisibleText) {
        return;
      }
      sawAnswer = true;
      if (parsed.finishedAt != null) {
        sawTerminal = true;
      }
    } catch (_) {
    }
  };

  const noteChunk = (chunk) => {
    if (!chunk) {
      return;
    }
    result.text += chunk;
    pending += chunk;
    while (true) {
      const newlineIndex = pending.indexOf('\n');
      if (newlineIndex === -1) {
        break;
      }
      const line = pending.slice(0, newlineIndex).trim();
      pending = pending.slice(newlineIndex + 1);
      markLineState(line);
    }
  };

  try {
    while (true) {
      if (sawTerminal) {
        await reader.cancel().catch(() => {});
        break;
      }

      let readResult;
      if (sawAnswer && idleAfterAnswerMs > 0) {
        const raced = await Promise.race([
          reader.read().then((value) => ({ kind: 'read', value })),
          new Promise((resolve) => setTimeout(() => resolve({ kind: 'idle' }), idleAfterAnswerMs)),
        ]);
        if (raced.kind === 'idle') {
          await reader.cancel().catch(() => {});
          break;
        }
        readResult = raced.value;
      } else {
        readResult = await reader.read();
      }

      if (!readResult || readResult.done) {
        break;
      }
      noteChunk(decoder.decode(readResult.value, { stream: true }));
    }
  } finally {
    noteChunk(decoder.decode());
    markLineState(pending.trim());
  }

  return result;
}`

func pythonPlaywrightBrowserHelperScript() string {
	return fmt.Sprintf(`import json
import sys
import traceback

from playwright.sync_api import sync_playwright

BROWSER_FETCH_SCRIPT = %q


def main():
    request = json.load(sys.stdin)
    browser = None
    context = None
    page = None
    result = None

    def close_quietly(handle):
        if handle is None:
            return
        try:
            handle.close()
        except Exception:
            pass

    launch_kwargs = {
        "headless": True,
        "args": [
            "--no-sandbox",
            "--disable-gpu",
            "--disable-dev-shm-usage",
            "--ignore-certificate-errors",
        ],
    }
    browser_path = (request.get("browser_path") or "").strip()
    if browser_path:
        launch_kwargs["executable_path"] = browser_path

    with sync_playwright() as playwright:
        try:
            browser = playwright.chromium.launch(**launch_kwargs)
            context_kwargs = {
                "ignore_https_errors": True,
                "timezone_id": request.get("timezone_id") or "Asia/Shanghai",
                "viewport": {
                    "width": int(request.get("viewport_width") or 1440),
                    "height": int(request.get("viewport_height") or 900),
                },
            }
            user_agent = (request.get("user_agent") or "").strip()
            if user_agent:
                context_kwargs["user_agent"] = user_agent
            locale = (request.get("locale") or "").strip()
            if locale:
                context_kwargs["locale"] = locale
            context = browser.new_context(**context_kwargs)

            cookies = []
            origin_url = request.get("origin_url") or ""
            for item in request.get("cookies") or []:
                name = (item.get("name") or "").strip()
                if not name:
                    continue
                cookies.append({
                    "name": name,
                    "value": item.get("value") or "",
                    "url": origin_url,
                })
            if cookies:
                context.add_cookies(cookies)

            page = context.new_page()
            page.goto(request["ai_url"], wait_until="domcontentloaded")
            page.wait_for_timeout(int(request.get("bootstrap_delay_ms") or 2000))
            result = page.evaluate(
                BROWSER_FETCH_SCRIPT,
                {
                    "run_url": request["run_url"],
                    "headers": request["headers"],
                    "payload": request["payload"],
                    "idle_after_answer_ms": request.get("idle_after_answer_ms") or 5000,
                },
            )
        finally:
            close_quietly(page)
            close_quietly(context)
            close_quietly(browser)

    sys.stdout.write(json.dumps(result))


if __name__ == "__main__":
    try:
        main()
    except Exception:
        traceback.print_exc(file=sys.stderr)
        raise
`, browserTransportFetchPageScript)
}

func nodePlaywrightBrowserHelperScript() string {
	return fmt.Sprintf(`const fs = require('fs');

let chromium;
try {
  ({ chromium } = require('playwright-core'));
} catch (error) {
  ({ chromium } = require('playwright'));
}

const browserFetchScriptSource = %q;
const browserFetchScript = eval('(' + browserFetchScriptSource + ')');

(async () => {
  const input = JSON.parse(fs.readFileSync(0, 'utf8'));
  let browser;
  let context;
  let page;
  let result;
  const closeQuietly = async (handle) => {
    if (!handle) {
      return;
    }
    try {
      await handle.close();
    } catch (_) {
    }
  };
  const launchOptions = {
    headless: true,
    args: [
      '--no-sandbox',
      '--disable-gpu',
      '--disable-dev-shm-usage',
      '--ignore-certificate-errors',
    ],
  };
  if ((input.browser_path || '').trim()) {
    launchOptions.executablePath = input.browser_path.trim();
  }

  try {
    browser = await chromium.launch(launchOptions);
    const contextOptions = {
      ignoreHTTPSErrors: true,
      timezoneId: input.timezone_id || 'Asia/Shanghai',
      viewport: {
        width: Number(input.viewport_width || 1440),
        height: Number(input.viewport_height || 900),
      },
    };
    if ((input.user_agent || '').trim()) {
      contextOptions.userAgent = input.user_agent.trim();
    }
    if ((input.locale || '').trim()) {
      contextOptions.locale = input.locale.trim();
    }
    context = await browser.newContext(contextOptions);

    const cookies = [];
    const originURL = (input.origin_url || '').trim();
    for (const item of input.cookies || []) {
      const name = String(item.name || '').trim();
      if (!name) {
        continue;
      }
      cookies.push({
        name,
        value: String(item.value || ''),
        url: originURL,
      });
    }
    if (cookies.length > 0) {
      await context.addCookies(cookies);
    }

    page = await context.newPage();
    await page.goto(input.ai_url, { waitUntil: 'domcontentloaded' });
    await page.waitForTimeout(Number(input.bootstrap_delay_ms || 2000));
    result = await page.evaluate(browserFetchScript, {
      run_url: input.run_url,
      headers: input.headers,
      payload: input.payload,
      idle_after_answer_ms: Number(input.idle_after_answer_ms || 5000),
    });
  } finally {
    await closeQuietly(page);
    await closeQuietly(context);
    await closeQuietly(browser);
  }

  process.stdout.write(JSON.stringify(result));
})().catch((error) => {
  console.error(error && error.stack ? error.stack : String(error));
  process.exit(1);
});
`, browserTransportFetchPageScript)
}

type browserTransportRequest struct {
	OriginURL         string            `json:"origin_url"`
	AIURL             string            `json:"ai_url"`
	RunURL            string            `json:"run_url"`
	Headers           map[string]string `json:"headers"`
	Payload           map[string]any    `json:"payload"`
	Cookies           []ProbeCookie     `json:"cookies"`
	BrowserPath       string            `json:"browser_path,omitempty"`
	UserAgent         string            `json:"user_agent,omitempty"`
	Locale            string            `json:"locale,omitempty"`
	TimezoneID        string            `json:"timezone_id,omitempty"`
	BootstrapDelayMS  int               `json:"bootstrap_delay_ms"`
	IdleAfterAnswerMS int               `json:"idle_after_answer_ms"`
	ViewportWidth     int               `json:"viewport_width"`
	ViewportHeight    int               `json:"viewport_height"`
}

type browserTransportResponse struct {
	Text        string `json:"text"`
	Status      int    `json:"status"`
	ContentType string `json:"content_type"`
}

type browserHelperUnavailableError struct {
	Message string
}

var (
	runPlaywrightBrowserFallback = runInferenceTranscriptInBrowserWithPlaywright
)

func (e *browserHelperUnavailableError) Error() string {
	if e == nil {
		return ""
	}
	return strings.TrimSpace(e.Message)
}

func detectInferenceStreamResponseFormat(body string) error {
	trimmed := strings.TrimSpace(strings.TrimPrefix(body, "\uFEFF"))
	if trimmed == "" {
		return &inferenceTransportError{Message: "browser fallback returned empty response"}
	}
	if strings.HasPrefix(trimmed, "{") {
		return nil
	}

	compact := strings.Join(strings.Fields(trimmed), " ")
	if len(compact) > 220 {
		compact = compact[:220] + "..."
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(trimmed, "<") || strings.Contains(lower, "cookiepart") || strings.Contains(lower, "cloudflare") || strings.Contains(lower, "cf-chl") {
		return &inferenceTransportError{Message: fmt.Sprintf("browser fallback returned challenge/html content instead of NDJSON: %s", compact)}
	}
	return &inferenceTransportError{Message: fmt.Sprintf("browser fallback returned non-NDJSON content: %s", compact)}
}

func runInferenceTranscriptInBrowser(ctx context.Context, client *NotionAIClient, payload map[string]any) (string, error) {
	if client == nil {
		return "", fmt.Errorf("browser transport client is nil")
	}
	if len(client.Session.Cookies) == 0 {
		return "", fmt.Errorf("browser transport requires session cookies")
	}
	return runPlaywrightBrowserFallback(ctx, client, payload)
}

func runInferenceTranscriptInBrowserWithPlaywright(ctx context.Context, client *NotionAIClient, payload map[string]any) (string, error) {
	request, err := buildBrowserTransportRequest(client, payload)
	if err != nil {
		return "", err
	}

	runners := []func(context.Context, browserTransportRequest) (string, error){
		runInferenceTranscriptInBrowserWithPythonPlaywright,
		runInferenceTranscriptInBrowserWithNodePlaywright,
	}
	if runtime.GOOS != "windows" {
		runners = []func(context.Context, browserTransportRequest) (string, error){
			runInferenceTranscriptInBrowserWithNodePlaywright,
			runInferenceTranscriptInBrowserWithPythonPlaywright,
		}
	}

	var unavailableErr error
	for _, runner := range runners {
		responseText, runErr := runner(ctx, request)
		if runErr == nil {
			return responseText, nil
		}
		var helperErr *browserHelperUnavailableError
		if errors.As(runErr, &helperErr) {
			unavailableErr = runErr
			continue
		}
		return "", runErr
	}
	if unavailableErr != nil {
		return "", unavailableErr
	}
	return "", &browserHelperUnavailableError{Message: "no Playwright runtime available for browser fallback"}
}

func runInferenceTranscriptInBrowserWithPythonPlaywright(ctx context.Context, request browserTransportRequest) (string, error) {
	var unavailableErr error
	for _, runtimeName := range []string{"python", "python3"} {
		responseText, err := runPlaywrightBrowserHelper(ctx, runtimeName, ".py", pythonPlaywrightBrowserHelperScript(), request, nil)
		if err == nil {
			return responseText, nil
		}
		var helperErr *browserHelperUnavailableError
		if errors.As(err, &helperErr) {
			unavailableErr = err
			continue
		}
		return "", err
	}
	if unavailableErr != nil {
		return "", unavailableErr
	}
	return "", &browserHelperUnavailableError{Message: "python Playwright runtime not available"}
}

func runInferenceTranscriptInBrowserWithNodePlaywright(ctx context.Context, request browserTransportRequest) (string, error) {
	return runPlaywrightBrowserHelper(ctx, "node", ".cjs", nodePlaywrightBrowserHelperScript(), request, browserHelperNodeEnv())
}

func runPlaywrightBrowserHelper(ctx context.Context, runtimeName string, extension string, script string, request browserTransportRequest, extraEnv []string) (string, error) {
	if _, err := exec.LookPath(runtimeName); err != nil {
		return "", &browserHelperUnavailableError{Message: fmt.Sprintf("%s not found", runtimeName)}
	}

	requestPayload, err := json.Marshal(request)
	if err != nil {
		return "", err
	}

	scriptFile, err := os.CreateTemp("", "notion-browser-helper-*"+extension)
	if err != nil {
		return "", err
	}
	scriptPath := scriptFile.Name()
	defer os.Remove(scriptPath)
	if _, err := scriptFile.WriteString(script); err != nil {
		_ = scriptFile.Close()
		return "", err
	}
	if err := scriptFile.Close(); err != nil {
		return "", err
	}

	cmd := newBrowserHelperCommand(ctx, runtimeName, scriptPath, requestPayload, extraEnv)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := runBrowserHelperCommand(ctx, cmd); err != nil {
		return "", classifyBrowserHelperExecError(ctx, runtimeName, err, stderr.String())
	}

	var response browserTransportResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		return "", fmt.Errorf("%s browser helper returned invalid json: %w", runtimeName, err)
	}
	if strings.TrimSpace(response.Text) == "" {
		return "", fmt.Errorf("%s browser helper returned empty response (status=%d content_type=%q)", runtimeName, response.Status, response.ContentType)
	}
	if err := detectInferenceStreamResponseFormat(response.Text); err != nil {
		return "", err
	}
	return response.Text, nil
}

func newBrowserHelperCommand(ctx context.Context, runtimeName string, scriptPath string, requestPayload []byte, extraEnv []string) *exec.Cmd {
	_ = ctx
	cmd := exec.CommandContext(context.Background(), runtimeName, scriptPath)
	cmd.Stdin = bytes.NewReader(requestPayload)
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.WaitDelay = browserHelperCancelWaitDelay
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		return nil
	}
	configureBrowserHelperCommand(cmd)
	return cmd
}

func runBrowserHelperCommand(ctx context.Context, cmd *exec.Cmd) error {
	if cmd == nil {
		return fmt.Errorf("browser helper command is nil")
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()
	if ctx == nil {
		return <-waitCh
	}
	select {
	case err := <-waitCh:
		return err
	case <-ctx.Done():
		cancelErr := cancelBrowserHelperCommand(cmd)
		waitErr := <-waitCh
		if waitErr != nil {
			return waitErr
		}
		if cancelErr != nil {
			return cancelErr
		}
		return ctx.Err()
	}
}

func cancelBrowserHelperCommand(cmd *exec.Cmd) error {
	if cmd == nil {
		return nil
	}
	if cmd.Cancel != nil {
		return cmd.Cancel()
	}
	if cmd.Process == nil {
		return nil
	}
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

func classifyBrowserHelperExecError(ctx context.Context, runtimeName string, runErr error, stderrText string) error {
	if errors.Is(runErr, exec.ErrNotFound) {
		return &browserHelperUnavailableError{Message: fmt.Sprintf("%s not found", runtimeName)}
	}
	if ctx != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
	}
	trimmed := strings.TrimSpace(stderrText)
	lower := strings.ToLower(trimmed)
	switch {
	case strings.Contains(lower, "no module named") && strings.Contains(lower, "playwright"):
		return &browserHelperUnavailableError{Message: fmt.Sprintf("%s missing Playwright module", runtimeName)}
	case strings.Contains(lower, "cannot find module") && strings.Contains(lower, "playwright-core"):
		return &browserHelperUnavailableError{Message: fmt.Sprintf("%s missing playwright-core module", runtimeName)}
	case strings.Contains(lower, "cannot find module") && strings.Contains(lower, "playwright"):
		return &browserHelperUnavailableError{Message: fmt.Sprintf("%s missing Playwright module", runtimeName)}
	}
	if trimmed == "" {
		trimmed = runErr.Error()
	}
	return fmt.Errorf("%s browser helper failed: %s", runtimeName, trimmed)
}

func buildBrowserTransportRequest(client *NotionAIClient, payload map[string]any) (browserTransportRequest, error) {
	if client == nil {
		return browserTransportRequest{}, fmt.Errorf("browser transport client is nil")
	}
	originURL := firstNonEmpty(strings.TrimSpace(client.Config.NotionUpstream().OriginURL), strings.TrimSpace(client.Config.NotionUpstream().BaseURL))
	if originURL == "" {
		originURL = "https://www.notion.so"
	}
	headers := client.baseHeaders("application/x-ndjson", client.Config.NotionUpstream().AIURL())
	delete(headers, "cookie")
	return browserTransportRequest{
		OriginURL:         originURL,
		AIURL:             client.Config.NotionUpstream().AIURL(),
		RunURL:            client.Config.NotionUpstream().API("runInferenceTranscript"),
		Headers:           headers,
		Payload:           payload,
		Cookies:           client.Session.Cookies,
		BrowserPath:       resolvePlaywrightBrowserExecutablePath(),
		UserAgent:         strings.TrimSpace(headers["user-agent"]),
		Locale:            normalizeBrowserTransportLocale(client.acceptLanguageHeader()),
		TimezoneID:        "Asia/Shanghai",
		BootstrapDelayMS:  int(browserTransportBootstrapDelay / time.Millisecond),
		IdleAfterAnswerMS: int(ndjsonIdleAfterAnswerTimeout / time.Millisecond),
		ViewportWidth:     browserTransportViewportWidth,
		ViewportHeight:    browserTransportViewportHeight,
	}, nil
}

func normalizeBrowserTransportLocale(value string) string {
	clean := strings.TrimSpace(value)
	if clean == "" {
		return ""
	}
	if idx := strings.Index(clean, ","); idx >= 0 {
		clean = clean[:idx]
	}
	if idx := strings.Index(clean, ";"); idx >= 0 {
		clean = clean[:idx]
	}
	clean = strings.ReplaceAll(strings.TrimSpace(clean), "_", "-")
	if clean == "" {
		return ""
	}
	parts := strings.Split(clean, "-")
	if len(parts) == 0 {
		return ""
	}
	primary := strings.ToLower(strings.TrimSpace(parts[0]))
	if len(primary) < 2 || len(primary) > 8 {
		return ""
	}
	normalized := []string{primary}
	for _, part := range parts[1:] {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		switch {
		case len(part) == 2:
			normalized = append(normalized, strings.ToUpper(part))
		case len(part) == 4:
			normalized = append(normalized, strings.ToUpper(part[:1])+strings.ToLower(part[1:]))
		default:
			normalized = append(normalized, strings.ToUpper(part))
		}
	}
	return strings.Join(normalized, "-")
}

func browserHelperNodeEnv() []string {
	candidates := []string{}
	for _, candidate := range browserHelperNodeModuleCandidates() {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		if stat, err := os.Stat(candidate); err == nil && stat.IsDir() {
			candidates = append(candidates, candidate)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	joined := strings.Join(candidates, string(os.PathListSeparator))
	if existing := strings.TrimSpace(os.Getenv("NODE_PATH")); existing != "" {
		joined = existing + string(os.PathListSeparator) + joined
	}
	return []string{"NODE_PATH=" + joined}
}

func browserHelperNodeModuleCandidates() []string {
	candidates := []string{
		os.Getenv("NODE_PATH"),
		"/opt/playwright-helper/node_modules",
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "node_modules"))
	}
	if executable, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(executable), "node_modules"))
	}
	return splitPathListCandidates(candidates)
}

func splitPathListCandidates(values []string) []string {
	candidates := []string{}
	for _, value := range values {
		for _, item := range filepath.SplitList(strings.TrimSpace(value)) {
			if strings.TrimSpace(item) == "" {
				continue
			}
			candidates = append(candidates, item)
		}
	}
	return candidates
}

func (c *NotionAIClient) supportsBrowserRunInferenceFallback() bool {
	if c == nil {
		return false
	}
	if c.browserRunInferenceFallback != nil {
		return true
	}
	upstream := c.Config.NotionUpstream()
	if strings.TrimSpace(upstream.HostHeader) != "" || strings.TrimSpace(upstream.TLSServerName) != "" {
		return false
	}
	originURL := firstNonEmpty(strings.TrimSpace(upstream.OriginURL), strings.TrimSpace(upstream.BaseURL))
	parsed, err := url.Parse(originURL)
	if err != nil {
		return false
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "" || host == "localhost" || host == "::1" || strings.HasPrefix(host, "127.") {
		return false
	}
	return true
}

func resolvePlaywrightBrowserExecutablePath() string {
	for _, candidate := range []string{os.Getenv("CHROME_BIN"), os.Getenv("CHROMIUM_BIN")} {
		if resolved := resolveExecutableCandidate(candidate); resolved != "" {
			return resolved
		}
	}
	return ""
}

func resolveExecutableCandidate(candidate string) string {
	clean := strings.TrimSpace(candidate)
	if clean == "" {
		return ""
	}
	if filepath.IsAbs(clean) {
		if _, err := os.Stat(clean); err == nil {
			return clean
		}
		return ""
	}
	if resolved, err := exec.LookPath(clean); err == nil {
		return resolved
	}
	return ""
}
