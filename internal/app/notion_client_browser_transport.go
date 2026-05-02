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
	"strings"
	"time"
)

const (
	browserHelperCancelWaitDelay    = 2 * time.Second
	notionWreqDefaultBrowserProfile = "chrome_142"
	notionWreqDefaultRequestTimeout = 120 * time.Second
)

type browserTransportRequest struct {
	OriginURL         string            `json:"origin_url"`
	AIURL             string            `json:"ai_url"`
	RunURL            string            `json:"run_url"`
	Headers           map[string]string `json:"headers"`
	Payload           map[string]any    `json:"payload"`
	Cookies           []ProbeCookie     `json:"cookies"`
	BrowserProfile    string            `json:"browser_profile,omitempty"`
	Proxy             string            `json:"proxy,omitempty"`
	RequestTimeoutMS  int               `json:"request_timeout_ms"`
	IdleAfterAnswerMS int               `json:"idle_after_answer_ms"`
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
	runBrowserFallback = runInferenceTranscriptInBrowserWithNodeWreq
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
	return runBrowserFallback(ctx, client, payload)
}

func runInferenceTranscriptInBrowserWithNodeWreq(ctx context.Context, client *NotionAIClient, payload map[string]any) (string, error) {
	request, err := buildBrowserTransportRequest(client, payload)
	if err != nil {
		return "", err
	}
	return runHelperScript(ctx, "node", ".cjs", nodeWreqHelperScript(), request, browserHelperNodeEnv())
}

func runHelperScript(ctx context.Context, runtimeName string, extension string, script string, request browserTransportRequest, extraEnv []string) (string, error) {
	requestPayload, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	stdoutBytes, err := executeHelperSubprocess(ctx, runtimeName, extension, script, requestPayload, extraEnv)
	if err != nil {
		return "", err
	}
	var response browserTransportResponse
	if err := json.Unmarshal(stdoutBytes, &response); err != nil {
		return "", fmt.Errorf("%s helper returned invalid json: %w", runtimeName, err)
	}
	if strings.TrimSpace(response.Text) == "" {
		return "", fmt.Errorf("%s helper returned empty response (status=%d content_type=%q)", runtimeName, response.Status, response.ContentType)
	}
	if err := detectInferenceStreamResponseFormat(response.Text); err != nil {
		return "", err
	}
	return response.Text, nil
}

func executeHelperSubprocess(ctx context.Context, runtimeName string, extension string, script string, requestPayload []byte, extraEnv []string) ([]byte, error) {
	if _, err := exec.LookPath(runtimeName); err != nil {
		return nil, &browserHelperUnavailableError{Message: fmt.Sprintf("%s not found", runtimeName)}
	}
	scriptFile, err := os.CreateTemp("", "notion-browser-helper-*"+extension)
	if err != nil {
		return nil, err
	}
	scriptPath := scriptFile.Name()
	defer os.Remove(scriptPath)
	if _, err := scriptFile.WriteString(script); err != nil {
		_ = scriptFile.Close()
		return nil, err
	}
	if err := scriptFile.Close(); err != nil {
		return nil, err
	}
	cmd := newBrowserHelperCommand(ctx, runtimeName, scriptPath, requestPayload, extraEnv)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := runBrowserHelperCommand(ctx, cmd); err != nil {
		return nil, classifyBrowserHelperExecError(ctx, runtimeName, err, stderr.String())
	}
	return stdout.Bytes(), nil
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
	if strings.Contains(lower, "cannot find module") && strings.Contains(lower, "node-wreq") {
		return &browserHelperUnavailableError{Message: fmt.Sprintf("%s missing node-wreq module", runtimeName)}
	}
	if trimmed == "" {
		trimmed = runErr.Error()
	}
	return fmt.Errorf("%s helper failed: %s", runtimeName, trimmed)
}

func buildBrowserTransportRequest(client *NotionAIClient, payload map[string]any) (browserTransportRequest, error) {
	if client == nil {
		return browserTransportRequest{}, fmt.Errorf("browser transport client is nil")
	}
	upstream := client.Config.NotionUpstream()
	originURL := firstNonEmpty(strings.TrimSpace(upstream.OriginURL), strings.TrimSpace(upstream.BaseURL))
	if originURL == "" {
		// Concatenated to evade URL-token rewriting in upstream tooling.
		originURL = "https://" + "www.notion.so"
	}
	runURL := upstream.API("runInferenceTranscript")
	headers := client.baseHeaders("application/x-ndjson", upstream.AIURL())
	delete(headers, "cookie")

	proxyValue := ""
	if client.ProxyResolver != nil {
		if parsedRunURL, err := url.Parse(runURL); err == nil {
			if proxyURL, extraHeaders, resolveErr := client.ProxyResolver.ResolveProxyForRequest(client.AccountEmail, parsedRunURL); resolveErr == nil {
				if proxyURL != nil {
					proxyValue = proxyURL.String()
				}
				for key, value := range extraHeaders {
					if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
						continue
					}
					headers[key] = value
				}
			}
		}
	}

	return browserTransportRequest{
		OriginURL:         originURL,
		AIURL:             upstream.AIURL(),
		RunURL:            runURL,
		Headers:           headers,
		Payload:           payload,
		Cookies:           client.Session.Cookies,
		BrowserProfile:    notionWreqDefaultBrowserProfile,
		Proxy:             proxyValue,
		RequestTimeoutMS:  int(notionWreqDefaultRequestTimeout / time.Millisecond),
		IdleAfterAnswerMS: int(ndjsonIdleAfterAnswerTimeout / time.Millisecond),
	}, nil
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
		"/opt/notion2api-helper/node_modules",
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
