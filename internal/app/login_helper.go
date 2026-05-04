package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	notionLoginUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36"
)

type LoginStartRequest struct {
	Email            string
	ProfileDir       string
	PendingPath      string
	StorageStatePath string
	AccountEmail     string
}

type LoginVerifyRequest struct {
	Email            string
	Code             string
	ProfileDir       string
	PendingPath      string
	StorageStatePath string
	ProbePath        string
	AccountEmail     string
}

type loginStorageState struct {
	Email         string        `json:"email,omitempty"`
	ClientVersion string        `json:"client_version,omitempty"`
	Cookies       []ProbeCookie `json:"cookies"`
	UpdatedAt     string        `json:"updated_at,omitempty"`
}

type loginPendingState struct {
	LoginStatusFile
	LoginOptionsToken string `json:"login_options_token,omitempty"`
	CSRFState         string `json:"csrf_state,omitempty"`
	DeviceID          string `json:"device_id,omitempty"`
}

type loginBootstrap struct {
	ClientVersion string
	DeviceID      string
}

type loginSpaceBootstrap struct {
	Email       string
	UserName    string
	SpaceID     string
	SpaceViewID string
}

type notionLoginAPIError struct {
	URL            string
	StatusCode     int
	Message        string
	ClientDataType string
	RetryAfter     time.Duration
}

func (e *notionLoginAPIError) Error() string {
	if e == nil {
		return ""
	}
	message := strings.TrimSpace(e.Message)
	if message == "" {
		message = http.StatusText(e.StatusCode)
	}
	if strings.TrimSpace(e.ClientDataType) != "" {
		return fmt.Sprintf("%s failed: %d %s (%s)", e.URL, e.StatusCode, message, e.ClientDataType)
	}
	return fmt.Sprintf("%s failed: %d %s", e.URL, e.StatusCode, message)
}

var notionVersionPattern = regexp.MustCompile(`data-notion-version=["']([^"']+)["']`)
var htmlTitlePattern = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
var htmlH1Pattern = regexp.MustCompile(`(?is)<h1[^>]*>(.*?)</h1>`)

func helperNowISO() string {
	return time.Now().Format(time.RFC3339)
}

func cleanOptionalPath(path string) string {
	clean := strings.TrimSpace(path)
	if clean == "" {
		return ""
	}
	return filepath.Clean(clean)
}

func ensureParentDir(path string) error {
	clean := strings.TrimSpace(path)
	if clean == "" {
		return nil
	}
	return os.MkdirAll(filepath.Dir(clean), 0o755)
}

func writePrettyJSONFile(path string, payload any) error {
	clean := strings.TrimSpace(path)
	if clean == "" {
		return fmt.Errorf("empty path")
	}
	if err := ensureParentDir(clean); err != nil {
		return err
	}
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(clean, append(body, '\n'), 0o644)
}

func readLoginPendingState(path string) (loginPendingState, error) {
	raw, err := os.ReadFile(strings.TrimSpace(path))
	if err != nil {
		return loginPendingState{}, err
	}
	var payload loginPendingState
	if err := json.Unmarshal(raw, &payload); err != nil {
		return loginPendingState{}, err
	}
	return payload, nil
}

func writeLoginPendingState(path string, payload loginPendingState) error {
	payload.PendingStatePath = firstNonEmpty(payload.PendingStatePath, path)
	payload.UpdatedAt = helperNowISO()
	return writePrettyJSONFile(path, payload)
}

func readLoginStorageState(path string) (loginStorageState, error) {
	raw, err := os.ReadFile(strings.TrimSpace(path))
	if err != nil {
		return loginStorageState{}, err
	}
	var payload loginStorageState
	if err := json.Unmarshal(raw, &payload); err != nil {
		return loginStorageState{}, err
	}
	return payload, nil
}

func writeLoginStorageState(path string, payload loginStorageState) error {
	payload.UpdatedAt = helperNowISO()
	return writePrettyJSONFile(path, payload)
}

func newNotionLoginSession(timeout time.Duration, upstream NotionUpstream, resolver *ProxyResolver, accountEmail string, cfg AppConfig) (*loginHTTPSession, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &loginHTTPSession{
		Client:                 &http.Client{Timeout: timeout, Jar: jar},
		ProxyResolver:          resolver,
		AccountEmail:           strings.TrimSpace(accountEmail),
		Timeout:                timeout,
		Upstream:               upstream,
		UseSurfHelperTransport: cfg.Features.UseSurfHelperTransport,
	}, nil
}

func notionLoginURLMust(raw string) *url.URL {
	parsed, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return parsed
}

func probeCookiesFromJar(jar http.CookieJar, rawURL string) []ProbeCookie {
	if jar == nil {
		return nil
	}
	parsed := notionLoginURLMust(rawURL)
	items := jar.Cookies(parsed)
	if len(items) == 0 {
		return nil
	}
	out := make([]ProbeCookie, 0, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		out = append(out, ProbeCookie{
			Name:  name,
			Value: item.Value,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

func restoreProbeCookies(jar http.CookieJar, rawURL string, cookies []ProbeCookie) {
	if jar == nil || len(cookies) == 0 {
		return
	}
	parsed := notionLoginURLMust(rawURL)
	items := make([]*http.Cookie, 0, len(cookies))
	for _, item := range cookies {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		items = append(items, &http.Cookie{
			Name:  name,
			Value: item.Value,
			Path:  "/",
		})
	}
	if len(items) > 0 {
		jar.SetCookies(parsed, items)
	}
}

func probeCookieValue(cookies []ProbeCookie, name string) string {
	target := strings.TrimSpace(name)
	for _, item := range cookies {
		if strings.EqualFold(strings.TrimSpace(item.Name), target) {
			return strings.TrimSpace(item.Value)
		}
	}
	return ""
}

func parseRetryAfter(headers http.Header) time.Duration {
	if headers == nil {
		return 0
	}
	value := strings.TrimSpace(headers.Get("Retry-After"))
	if value == "" {
		return 0
	}
	if seconds, err := time.ParseDuration(value + "s"); err == nil && seconds > 0 {
		return seconds
	}
	return 0
}

func stripHTMLTags(text string) string {
	inTag := false
	var b strings.Builder
	for _, r := range text {
		switch r {
		case '<':
			inTag = true
		case '>':
			inTag = false
		default:
			if !inTag {
				b.WriteRune(r)
			}
		}
	}
	return collapseWhitespace(b.String())
}

func summarizeHTMLLikeBody(body []byte, headers http.Header) string {
	snippet := strings.TrimSpace(string(body))
	if snippet == "" {
		return ""
	}
	contentType := strings.ToLower(strings.TrimSpace(headers.Get("Content-Type")))
	looksLikeHTML := strings.Contains(contentType, "text/html") ||
		strings.HasPrefix(strings.ToLower(snippet), "<!doctype html") ||
		strings.HasPrefix(strings.ToLower(snippet), "<html")
	if !looksLikeHTML {
		return ""
	}
	if match := htmlTitlePattern.FindStringSubmatch(snippet); len(match) == 2 {
		title := stripHTMLTags(match[1])
		if title != "" {
			if matchH1 := htmlH1Pattern.FindStringSubmatch(snippet); len(matchH1) == 2 {
				h1 := stripHTMLTags(matchH1[1])
				if h1 != "" && !strings.EqualFold(h1, title) {
					return fmt.Sprintf("HTML error page: %s | %s", title, h1)
				}
			}
			return fmt.Sprintf("HTML error page: %s", title)
		}
	}
	if match := htmlH1Pattern.FindStringSubmatch(snippet); len(match) == 2 {
		h1 := stripHTMLTags(match[1])
		if h1 != "" {
			return fmt.Sprintf("HTML error page: %s", h1)
		}
	}
	return "HTML error page returned by upstream"
}

func loginJSONError(targetURL string, status int, body []byte, headers http.Header) error {
	snippet := strings.TrimSpace(string(body))
	if len(snippet) > 400 {
		snippet = snippet[:400]
	}
	apiErr := &notionLoginAPIError{
		URL:        targetURL,
		StatusCode: status,
		RetryAfter: parseRetryAfter(headers),
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err == nil {
		clientData := mapValue(payload["clientData"])
		dataType := strings.TrimSpace(stringValue(clientData["type"]))
		apiErr.ClientDataType = dataType
		if debugMessage := strings.TrimSpace(stringValue(payload["debugMessage"])); debugMessage != "" {
			apiErr.Message = debugMessage
			return apiErr
		}
		if message := strings.TrimSpace(stringValue(payload["message"])); message != "" {
			apiErr.Message = message
			return apiErr
		}
	}
	if htmlSummary := summarizeHTMLLikeBody(body, headers); htmlSummary != "" {
		apiErr.Message = htmlSummary
		return apiErr
	}
	if snippet == "" {
		snippet = http.StatusText(status)
	}
	apiErr.Message = snippet
	return apiErr
}

func fetchLoginBootstrap(ctx context.Context, session *loginHTTPSession, upstream NotionUpstream) (loginBootstrap, error) {
	headers := map[string]string{
		"accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"accept-language": "zh-CN,zh;q=0.9",
		"user-agent":      notionLoginUA,
	}
	status, respHeaders, body, err := loginTransportDoRequest(ctx, session, http.MethodGet, upstream.LoginURL(), headers, nil)
	if err != nil {
		return loginBootstrap{}, err
	}
	if len(body) > 2*1024*1024 {
		body = body[:2*1024*1024]
	}
	if status < 200 || status >= 300 {
		return loginBootstrap{}, loginJSONError(upstream.LoginURL(), status, body, respHeaders)
	}

	clientVersion := ""
	if match := notionVersionPattern.FindSubmatch(body); len(match) == 2 {
		clientVersion = strings.TrimSpace(string(match[1]))
	}
	if clientVersion == "" {
		return loginBootstrap{}, fmt.Errorf("login page missing data-notion-version")
	}

	cookies := probeCookiesFromJar(session.Jar, upstream.LoginURL())
	deviceID := firstNonEmpty(
		probeCookieValue(cookies, "notion_browser_id"),
		probeCookieValue(cookies, "device_id"),
		randomUUID(),
	)
	return loginBootstrap{
		ClientVersion: clientVersion,
		DeviceID:      deviceID,
	}, nil
}

func postNotionLoginJSON(ctx context.Context, session *loginHTTPSession, upstream NotionUpstream, targetURL string, clientVersion string, referer string, activeUserID string, payload any) (map[string]any, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	headers := map[string]string{
		"accept":                      "application/json",
		"accept-language":             "zh-CN",
		"content-type":                "application/json",
		"origin":                      upstream.OriginURL,
		"referer":                     firstNonEmpty(referer, upstream.LoginURL()),
		"user-agent":                  notionLoginUA,
		"notion-client-version":       clientVersion,
		"notion-audit-log-platform":   "web",
		"x-notion-active-user-header": strings.TrimSpace(activeUserID),
	}
	status, respHeaders, respBody, err := loginTransportDoRequest(ctx, session, http.MethodPost, targetURL, headers, body)
	if err != nil {
		return nil, err
	}
	if len(respBody) > 2*1024*1024 {
		respBody = respBody[:2*1024*1024]
	}
	if status < 200 || status >= 300 {
		return nil, loginJSONError(targetURL, status, respBody, respHeaders)
	}
	if len(bytes.TrimSpace(respBody)) == 0 {
		return map[string]any{}, nil
	}
	if htmlSummary := summarizeHTMLLikeBody(respBody, respHeaders); htmlSummary != "" {
		return nil, &notionLoginAPIError{
			URL:        targetURL,
			StatusCode: status,
			Message:    htmlSummary,
			RetryAfter: parseRetryAfter(respHeaders),
		}
	}
	var decoded map[string]any
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func getLoginOptions(ctx context.Context, session *loginHTTPSession, upstream NotionUpstream, email string, clientVersion string) (string, error) {
	payload, err := postNotionLoginJSON(ctx, session, upstream, upstream.API("getLoginOptions"), clientVersion, upstream.LoginURL(), "", map[string]any{
		"email":                strings.TrimSpace(email),
		"requireWorkTypeEmail": false,
	})
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(stringValue(payload["loginOptionsToken"]))
	if token == "" {
		return "", fmt.Errorf("getLoginOptions returned empty loginOptionsToken")
	}
	return token, nil
}

func sendTemporaryPassword(ctx context.Context, session *loginHTTPSession, upstream NotionUpstream, email string, clientVersion string, loginOptionsToken string, deviceID string) (string, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		payload, err := postNotionLoginJSON(ctx, session, upstream, upstream.API("sendTemporaryPassword"), clientVersion, upstream.LoginURL(), "", map[string]any{
			"email":              strings.TrimSpace(email),
			"disableLoginLink":   false,
			"native":             false,
			"isSignup":           false,
			"shouldHidePasscode": false,
			"loginOptionsToken":  loginOptionsToken,
			"deviceId":           strings.TrimSpace(deviceID),
			"loginRouteOrigin":   "login",
		})
		if err == nil {
			csrfState := strings.TrimSpace(stringValue(payload["csrfState"]))
			if csrfState == "" {
				return "", fmt.Errorf("sendTemporaryPassword returned empty csrfState")
			}
			return csrfState, nil
		}
		lastErr = err
		apiErr, ok := err.(*notionLoginAPIError)
		if !ok {
			break
		}
		if apiErr.StatusCode != http.StatusTooManyRequests && apiErr.StatusCode < 500 {
			break
		}
		if attempt == 2 {
			break
		}
		wait := apiErr.RetryAfter
		if wait <= 0 {
			wait = time.Duration(attempt+1) * time.Second
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
	return "", lastErr
}

func loginWithEmail(ctx context.Context, session *loginHTTPSession, upstream NotionUpstream, clientVersion string, csrfState string, code string) (string, error) {
	payload, err := postNotionLoginJSON(ctx, session, upstream, upstream.API("loginWithEmail"), clientVersion, upstream.LoginURL(), "", map[string]any{
		"state":            strings.TrimSpace(csrfState),
		"password":         strings.TrimSpace(code),
		"loginRouteOrigin": "login",
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(stringValue(payload["userId"])), nil
}

func firstSpacePointer(pointers []any) (string, string) {
	for _, rawPointer := range pointers {
		pointer := mapValue(rawPointer)
		spaceID := strings.TrimSpace(stringValue(pointer["spaceId"]))
		if spaceID != "" {
			return spaceID, strings.TrimSpace(stringValue(pointer["id"]))
		}
	}
	return "", ""
}

func parseSpacesInitial(payload map[string]any, userID string) loginSpaceBootstrap {
	users := mapValue(payload["users"])
	userEntry := mapValue(users[userID])

	notionUserEntry := mapValue(mapValue(mapValue(userEntry["notion_user"])[userID])["value"])
	notionUserValue := mapValue(notionUserEntry["value"])

	userRootEntry := mapValue(mapValue(mapValue(userEntry["user_root"])[userID])["value"])
	userRootValue := mapValue(userRootEntry["value"])
	spaceID, spaceViewID := firstSpacePointer(sliceValue(userRootValue["space_view_pointers"]))

	return loginSpaceBootstrap{
		Email:       strings.TrimSpace(stringValue(notionUserValue["email"])),
		UserName:    strings.TrimSpace(stringValue(notionUserValue["name"])),
		SpaceID:     spaceID,
		SpaceViewID: spaceViewID,
	}
}

func getSpacesInitial(ctx context.Context, session *loginHTTPSession, upstream NotionUpstream, clientVersion string, userID string) (loginSpaceBootstrap, error) {
	payload, err := postNotionLoginJSON(ctx, session, upstream, upstream.API("getSpacesInitial"), clientVersion, upstream.HomeURL(), userID, map[string]any{})
	if err != nil {
		return loginSpaceBootstrap{}, err
	}
	result := parseSpacesInitial(payload, userID)
	if result.SpaceID == "" {
		return loginSpaceBootstrap{}, fmt.Errorf("getSpacesInitial returned empty space_id")
	}
	return result, nil
}

func loginBaseState(email string, profileDir string, pendingPath string, storageStatePath string, probePath string) loginPendingState {
	return loginPendingState{
		LoginStatusFile: LoginStatusFile{
			Email:            strings.TrimSpace(email),
			ProfileDir:       cleanOptionalPath(profileDir),
			PendingStatePath: cleanOptionalPath(pendingPath),
			StorageStatePath: cleanOptionalPath(storageStatePath),
			ProbePath:        cleanOptionalPath(probePath),
		},
	}
}

func failLoginState(path string, state loginPendingState, err error) (LoginStatusFile, error) {
	message := strings.TrimSpace(err.Error())
	state.Success = false
	state.Status = "failed"
	var apiErr *notionLoginAPIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusTooManyRequests:
			state.Status = "rate_limited"
		case http.StatusBadRequest:
			if apiErr.ClientDataType == "invalid_or_expired_password" {
				state.Status = "invalid_code"
			}
		}
	}
	state.Message = message
	state.Error = message
	state.UpdatedAt = helperNowISO()
	if strings.TrimSpace(path) != "" {
		_ = writeLoginPendingState(path, state)
	}
	return state.LoginStatusFile, err
}

func helperContext(parent context.Context, cfg AppConfig) (context.Context, context.CancelFunc) {
	timeout := helperTimeout(cfg)
	return context.WithTimeout(parent, timeout)
}

func helperTimeout(cfg AppConfig) time.Duration {
	return time.Duration(maxInt(cfg.ResolveLoginHelper().TimeoutSec, 30)) * time.Second
}

func wrapLoginStageError(cfg AppConfig, upstream NotionUpstream, stage string, err error) error {
	if err == nil {
		return nil
	}
	wrapped := fmt.Errorf("%s: %w", stage, err)
	if !cfg.DebugUpstream {
		return wrapped
	}
	detail := fmt.Sprintf(
		"upstream{base=%s origin=%s host=%q sni=%q env_proxy=%t}",
		upstream.BaseURL,
		upstream.OriginURL,
		upstream.HostHeader,
		upstream.TLSServerName,
		upstream.UseEnvProxy,
	)
	log.Printf("[login-helper] %s failed: %v | %s", stage, err, detail)
	return fmt.Errorf("%s [%s]", wrapped.Error(), detail)
}

func StartEmailLogin(ctx context.Context, cfg AppConfig, req LoginStartRequest) (LoginStatusFile, error) {
	state := loginBaseState(req.Email, req.ProfileDir, req.PendingPath, req.StorageStatePath, "")
	if err := os.MkdirAll(state.ProfileDir, 0o755); err != nil {
		return failLoginState(req.PendingPath, state, err)
	}
	ctx, cancel := helperContext(ctx, cfg)
	defer cancel()
	upstream := cfg.NotionUpstream()
	resolver := NewProxyResolver(cfg)

	session, err := newNotionLoginSession(helperTimeout(cfg), upstream, resolver, firstNonEmpty(req.AccountEmail, req.Email), cfg)
	if err != nil {
		return failLoginState(req.PendingPath, state, err)
	}

	bootstrap, err := fetchLoginBootstrap(ctx, session, upstream)
	if err != nil {
		return failLoginState(req.PendingPath, state, wrapLoginStageError(cfg, upstream, "fetch login bootstrap", err))
	}

	loginOptionsToken, err := getLoginOptions(ctx, session, upstream, req.Email, bootstrap.ClientVersion)
	if err != nil {
		return failLoginState(req.PendingPath, state, wrapLoginStageError(cfg, upstream, "get login options", err))
	}
	csrfState, err := sendTemporaryPassword(ctx, session, upstream, req.Email, bootstrap.ClientVersion, loginOptionsToken, bootstrap.DeviceID)
	if err != nil {
		return failLoginState(req.PendingPath, state, wrapLoginStageError(cfg, upstream, "request verification code", err))
	}

	storage := loginStorageState{
		Email:         strings.TrimSpace(req.Email),
		ClientVersion: bootstrap.ClientVersion,
		Cookies:       probeCookiesFromJar(session.Jar, upstream.LoginURL()),
	}
	if err := writeLoginStorageState(req.StorageStatePath, storage); err != nil {
		return failLoginState(req.PendingPath, state, err)
	}

	state.Success = true
	state.Status = "pending_code"
	state.ClientVersion = bootstrap.ClientVersion
	state.CurrentURL = upstream.LoginURL()
	state.Title = "Notion Login"
	state.Message = "verification code requested"
	state.Error = ""
	state.LoginOptionsToken = loginOptionsToken
	state.CSRFState = csrfState
	state.DeviceID = bootstrap.DeviceID
	if err := writeLoginPendingState(req.PendingPath, state); err != nil {
		return failLoginState(req.PendingPath, state, err)
	}
	return state.LoginStatusFile, nil
}

func VerifyEmailLogin(ctx context.Context, cfg AppConfig, req LoginVerifyRequest) (LoginStatusFile, error) {
	baseState := loginBaseState(req.Email, req.ProfileDir, req.PendingPath, req.StorageStatePath, req.ProbePath)
	pending, err := readLoginPendingState(req.PendingPath)
	if err != nil {
		return failLoginState(req.PendingPath, baseState, fmt.Errorf("pending login state not found: %w", err))
	}
	pending.Email = firstNonEmpty(strings.TrimSpace(req.Email), pending.Email)
	pending.ProfileDir = firstNonEmpty(baseState.ProfileDir, pending.ProfileDir)
	pending.PendingStatePath = firstNonEmpty(baseState.PendingStatePath, pending.PendingStatePath)
	pending.StorageStatePath = firstNonEmpty(baseState.StorageStatePath, pending.StorageStatePath)
	pending.ProbePath = firstNonEmpty(strings.TrimSpace(req.ProbePath), pending.ProbePath)
	if err := os.MkdirAll(pending.ProfileDir, 0o755); err != nil {
		return failLoginState(req.PendingPath, pending, err)
	}

	storage, err := readLoginStorageState(req.StorageStatePath)
	if err != nil {
		return failLoginState(req.PendingPath, pending, fmt.Errorf("read storage state failed: %w", err))
	}

	ctx, cancel := helperContext(ctx, cfg)
	defer cancel()
	upstream := cfg.NotionUpstream()
	resolver := NewProxyResolver(cfg)

	session, err := newNotionLoginSession(helperTimeout(cfg), upstream, resolver, firstNonEmpty(req.AccountEmail, req.Email), cfg)
	if err != nil {
		return failLoginState(req.PendingPath, pending, err)
	}
	restoreProbeCookies(session.Jar, upstream.LoginURL(), storage.Cookies)

	clientVersion := firstNonEmpty(pending.ClientVersion, storage.ClientVersion)
	if clientVersion == "" {
		return failLoginState(req.PendingPath, pending, fmt.Errorf("pending login state missing client_version"))
	}
	if strings.TrimSpace(pending.CSRFState) == "" {
		return failLoginState(req.PendingPath, pending, fmt.Errorf("pending login state missing csrf_state"))
	}

	userID, err := loginWithEmail(ctx, session, upstream, clientVersion, pending.CSRFState, req.Code)
	if err != nil {
		return failLoginState(req.PendingPath, pending, wrapLoginStageError(cfg, upstream, "verify login code", err))
	}
	if userID == "" {
		userID = probeCookieValue(probeCookiesFromJar(session.Jar, upstream.HomeURL()), "notion_user_id")
	}
	if userID == "" {
		return failLoginState(req.PendingPath, pending, fmt.Errorf("notion_user_id missing after OTP verify"))
	}

	spaces, err := getSpacesInitial(ctx, session, upstream, clientVersion, userID)
	if err != nil {
		return failLoginState(req.PendingPath, pending, wrapLoginStageError(cfg, upstream, "load spaces after login", err))
	}

	cookies := probeCookiesFromJar(session.Jar, upstream.HomeURL())
	if len(cookies) == 0 {
		cookies = probeCookiesFromJar(session.Jar, upstream.LoginURL())
	}
	if len(cookies) == 0 {
		return failLoginState(req.PendingPath, pending, fmt.Errorf("cookie jar empty after OTP verify"))
	}
	storage = loginStorageState{
		Email:         firstNonEmpty(spaces.Email, req.Email),
		ClientVersion: clientVersion,
		Cookies:       cookies,
	}
	if err := writeLoginStorageState(req.StorageStatePath, storage); err != nil {
		return failLoginState(req.PendingPath, pending, err)
	}

	probePayload := map[string]any{
		"email":          firstNonEmpty(spaces.Email, req.Email),
		"user_id":        userID,
		"user_name":      spaces.UserName,
		"space_id":       spaces.SpaceID,
		"space_view_id":  spaces.SpaceViewID,
		"space_name":     firstNonEmpty(pending.SpaceName, spaces.UserName+"'s Space"),
		"client_version": clientVersion,
		"cookies":        cookies,
	}
	if err := writePrettyJSONFile(req.ProbePath, probePayload); err != nil {
		return failLoginState(req.PendingPath, pending, err)
	}

	pending.Success = true
	pending.Status = "ready"
	pending.Email = firstNonEmpty(spaces.Email, req.Email)
	pending.ProbePath = req.ProbePath
	pending.UserID = userID
	pending.UserName = spaces.UserName
	pending.SpaceID = spaces.SpaceID
	pending.SpaceViewID = spaces.SpaceViewID
	pending.ClientVersion = clientVersion
	pending.CurrentURL = upstream.HomeURL()
	pending.FinalURL = upstream.HomeURL()
	pending.Title = "Notion"
	pending.Message = "login verified"
	pending.Error = ""
	pending.LastLoginAt = helperNowISO()
	if err := writeLoginPendingState(req.PendingPath, pending); err != nil {
		return failLoginState(req.PendingPath, pending, err)
	}
	return pending.LoginStatusFile, nil
}
