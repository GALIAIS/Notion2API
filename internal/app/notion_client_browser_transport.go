package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	browserHelperCancelWaitDelay    = 2 * time.Second
	notionWreqDefaultBrowserProfile = "chrome_142"
	notionWreqDefaultRequestTimeout = 120 * time.Second
	maxBrowserHelperPoolSize        = 8
	browserHelperPoolSizeEnvKey     = "NOTION2API_BROWSER_HELPER_POOL_SIZE"
	browserHelperPoolModeEnvKey     = "NOTION2API_BROWSER_HELPER_MODE"
	browserHelperPoolMode           = "pool"
	browserHelperPoolProtoEnvKey    = "NOTION2API_BROWSER_HELPER_PROTOCOL"
	browserHelperPoolProtoV1        = "N2A_HELPER_POOL_V1"
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

type browserHelperPool struct {
	runtimeName string
	scriptPath  string
	extraEnv    []string
	size        int
	workers     chan *browserHelperPoolWorker
}

type browserHelperPoolWorker struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
}

type browserHelperPoolKey struct {
	runtimeName string
	scriptPath  string
	envKey      string
	size        int
}

var (
	runBrowserFallback = runInferenceTranscriptInBrowserWithNodeWreq
	browserHelperPools = struct {
		mu    sync.Mutex
		pools map[browserHelperPoolKey]*browserHelperPool
	}{
		pools: map[browserHelperPoolKey]*browserHelperPool{},
	}
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
	helperEnv := browserHelperNodeEnvForConfig(client.Config)
	return runHelperScript(ctx, "node", ".cjs", nodeWreqHelperScript(), request, helperEnv)
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
	startedAt := time.Now()
	defer func() {
		observeWreqFFICallDuration(time.Since(startedAt))
	}()
	if _, err := exec.LookPath(runtimeName); err != nil {
		return nil, &browserHelperUnavailableError{Message: fmt.Sprintf("%s not found", runtimeName)}
	}
	scriptPath, err := ensureHelperScriptFile("", extension, script)
	if err != nil {
		return nil, err
	}
	if size, ok := browserHelperPoolSizeFromEnv(extraEnv); ok && size > 1 {
		pooled, pooledErr := executeHelperSubprocessPooled(ctx, runtimeName, scriptPath, requestPayload, extraEnv, size)
		if pooledErr == nil {
			return pooled, nil
		}
		return nil, classifyBrowserHelperExecError(ctx, runtimeName, pooledErr, "")
	}
	cmd := newBrowserHelperCommand(ctx, runtimeName, scriptPath, requestPayload, extraEnv)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	addBrowserHelperSpawn()
	if err := runBrowserHelperCommand(ctx, cmd); err != nil {
		return nil, classifyBrowserHelperExecError(ctx, runtimeName, err, stderr.String())
	}
	return stdout.Bytes(), nil
}

func browserHelperPoolSizeFromEnv(extraEnv []string) (int, bool) {
	for _, entry := range extraEnv {
		pair := strings.SplitN(strings.TrimSpace(entry), "=", 2)
		if len(pair) != 2 {
			continue
		}
		if strings.TrimSpace(pair[0]) != browserHelperPoolSizeEnvKey {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(pair[1]))
		if err != nil || n <= 1 {
			return 0, false
		}
		if n > maxBrowserHelperPoolSize {
			n = maxBrowserHelperPoolSize
		}
		return n, true
	}
	return 0, false
}

func browserHelperPoolEnvKey(extraEnv []string) string {
	if len(extraEnv) == 0 {
		return ""
	}
	filtered := make([]string, 0, len(extraEnv))
	for _, entry := range extraEnv {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			continue
		}
		filtered = append(filtered, trimmed)
	}
	sort.Strings(filtered)
	return strings.Join(filtered, "\x00")
}

func getOrCreateBrowserHelperPool(runtimeName string, scriptPath string, extraEnv []string, size int) (*browserHelperPool, error) {
	if size <= 1 {
		return nil, fmt.Errorf("invalid browser helper pool size: %d", size)
	}
	if size > maxBrowserHelperPoolSize {
		size = maxBrowserHelperPoolSize
	}
	key := browserHelperPoolKey{
		runtimeName: strings.TrimSpace(runtimeName),
		scriptPath:  scriptPath,
		envKey:      browserHelperPoolEnvKey(extraEnv),
		size:        size,
	}
	browserHelperPools.mu.Lock()
	if existing := browserHelperPools.pools[key]; existing != nil {
		browserHelperPools.mu.Unlock()
		return existing, nil
	}
	pool := &browserHelperPool{
		runtimeName: runtimeName,
		scriptPath:  scriptPath,
		extraEnv:    append([]string(nil), extraEnv...),
		size:        size,
		workers:     make(chan *browserHelperPoolWorker, size),
	}
	startedWorkers := make([]*browserHelperPoolWorker, 0, size)
	for i := 0; i < size; i++ {
		worker, err := startBrowserHelperPoolWorker(runtimeName, scriptPath, pool.extraEnv)
		if err != nil {
			for _, started := range startedWorkers {
				stopBrowserHelperPoolWorker(started)
			}
			browserHelperPools.mu.Unlock()
			return nil, err
		}
		startedWorkers = append(startedWorkers, worker)
		pool.workers <- worker
	}
	browserHelperPools.pools[key] = pool
	browserHelperPools.mu.Unlock()
	return pool, nil
}

func startBrowserHelperPoolWorker(runtimeName string, scriptPath string, extraEnv []string) (*browserHelperPoolWorker, error) {
	cmd := exec.Command(runtimeName, scriptPath)
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Env = append(cmd.Env, browserHelperPoolModeEnvKey+"="+browserHelperPoolMode, browserHelperPoolProtoEnvKey+"="+browserHelperPoolProtoV1)
	configureBrowserHelperCommand(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}
	addBrowserHelperSpawn()
	addBrowserHelperPoolWorkerSpawn()
	return &browserHelperPoolWorker{
		cmd:    cmd,
		stdin:  stdin,
		stdout: stdout,
	}, nil
}

func stopBrowserHelperPoolWorker(worker *browserHelperPoolWorker) {
	if worker == nil {
		return
	}
	if worker.cmd != nil {
		_ = cancelBrowserHelperCommand(worker.cmd)
	}
	if worker.stdin != nil {
		_ = worker.stdin.Close()
	}
	if worker.stdout != nil {
		_ = worker.stdout.Close()
	}
	if worker.cmd != nil {
		_ = worker.cmd.Wait()
	}
}

func writePoolFrame(writer io.Writer, payload []byte) error {
	if writer == nil {
		return fmt.Errorf("pool writer unavailable")
	}
	frame := make([]byte, 4+len(payload))
	binary.LittleEndian.PutUint32(frame[:4], uint32(len(payload)))
	copy(frame[4:], payload)
	_, err := writer.Write(frame)
	return err
}

func readPoolFrame(reader io.Reader) ([]byte, error) {
	if reader == nil {
		return nil, fmt.Errorf("pool reader unavailable")
	}
	header := make([]byte, 4)
	if _, err := io.ReadFull(reader, header); err != nil {
		return nil, err
	}
	length := binary.LittleEndian.Uint32(header)
	if length == 0 {
		return []byte("{}"), nil
	}
	body := make([]byte, int(length))
	if _, err := io.ReadFull(reader, body); err != nil {
		return nil, err
	}
	return body, nil
}

func replaceBrowserHelperPoolWorker(worker *browserHelperPoolWorker, runtimeName string, scriptPath string, extraEnv []string) error {
	fresh, err := startBrowserHelperPoolWorker(runtimeName, scriptPath, extraEnv)
	if err != nil {
		return err
	}
	if worker != nil {
		stopBrowserHelperPoolWorker(worker)
		*worker = *fresh
	} else {
		stopBrowserHelperPoolWorker(fresh)
	}
	return nil
}

func executeHelperSubprocessPooled(ctx context.Context, runtimeName string, scriptPath string, requestPayload []byte, extraEnv []string, size int) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	pool, err := getOrCreateBrowserHelperPool(runtimeName, scriptPath, extraEnv, size)
	if err != nil {
		return nil, err
	}
	var worker *browserHelperPoolWorker
	select {
	case worker = <-pool.workers:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() {
		if worker != nil {
			pool.workers <- worker
		}
	}()

	type poolResult struct {
		body []byte
		err  error
	}
	done := make(chan poolResult, 1)
	go func(w *browserHelperPoolWorker) {
		if err := writePoolFrame(w.stdin, requestPayload); err != nil {
			done <- poolResult{err: err}
			return
		}
		body, err := readPoolFrame(w.stdout)
		done <- poolResult{body: body, err: err}
	}(worker)

	select {
	case res := <-done:
		if res.err != nil {
			_ = replaceBrowserHelperPoolWorker(worker, pool.runtimeName, pool.scriptPath, pool.extraEnv)
			return nil, classifyBrowserHelperExecError(ctx, runtimeName, res.err, "")
		}
		return res.body, nil
	case <-ctx.Done():
		_ = replaceBrowserHelperPoolWorker(worker, pool.runtimeName, pool.scriptPath, pool.extraEnv)
		return nil, ctx.Err()
	}
}

func resetBrowserHelperPoolsForTest() {
	browserHelperPools.mu.Lock()
	defer browserHelperPools.mu.Unlock()
	for key, pool := range browserHelperPools.pools {
		if pool == nil {
			delete(browserHelperPools.pools, key)
			continue
		}
		close(pool.workers)
		for worker := range pool.workers {
			stopBrowserHelperPoolWorker(worker)
		}
		delete(browserHelperPools.pools, key)
	}
	browserHelperPools.pools = map[browserHelperPoolKey]*browserHelperPool{}
}

func ensureHelperScriptFile(tempDir string, extension string, script string) (string, error) {
	if strings.TrimSpace(tempDir) == "" {
		tempDir = os.TempDir()
	}
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		return "", err
	}

	scriptHash := sha256.Sum256([]byte(script))
	scriptPath := filepath.Join(tempDir, fmt.Sprintf("notion-helper-%x%s", scriptHash, extension))
	if existing, err := os.ReadFile(scriptPath); err == nil {
		if string(existing) == script {
			return scriptPath, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	tmpFile, err := os.CreateTemp(tempDir, "notion-helper-write-*"+extension)
	if err != nil {
		return "", err
	}
	tmpPath := tmpFile.Name()
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmpFile.WriteString(script); err != nil {
		_ = tmpFile.Close()
		return "", err
	}
	if err := tmpFile.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpPath, scriptPath); err != nil {
		if existing, readErr := os.ReadFile(scriptPath); readErr == nil && string(existing) == script {
			cleanupTmp = false
			_ = os.Remove(tmpPath)
			return scriptPath, nil
		}
		return "", err
	}
	cleanupTmp = false
	return scriptPath, nil
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

func browserHelperNodeEnvForConfig(cfg AppConfig) []string {
	base := browserHelperNodeEnv()
	size := strings.TrimSpace(os.Getenv(browserHelperPoolSizeEnvKey))
	if size != "" {
		return append(base, browserHelperPoolSizeEnvKey+"="+size)
	}
	if cfg.Browser.HelperPoolSize <= 1 {
		return base
	}
	sizeNum := configuredBrowserHelperPoolSize(cfg)
	if sizeNum <= 1 {
		return base
	}
	return append(base, browserHelperPoolSizeEnvKey+"="+strconv.Itoa(sizeNum))
}

func configuredBrowserHelperPoolSize(cfg AppConfig) int {
	if cfg.Browser.HelperPoolSize > 0 {
		if cfg.Browser.HelperPoolSize > maxBrowserHelperPoolSize {
			return maxBrowserHelperPoolSize
		}
		return cfg.Browser.HelperPoolSize
	}
	size := runtime.NumCPU()
	if size < 1 {
		size = 1
	}
	if size > maxBrowserHelperPoolSize {
		size = maxBrowserHelperPoolSize
	}
	return size
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
