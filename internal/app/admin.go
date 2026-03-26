package app

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	adminLoginMaxFailures = 5
	adminLoginLockWindow  = 15 * time.Minute
)

var (
	adminSyncRequestTimeoutCap = 50 * time.Second
	adminSyncRequestTimeoutMin = 10 * time.Second
)

type AdminLoginAttempt struct {
	Failures    int
	LastFailure time.Time
	LockedUntil time.Time
}

const welcomeHTML = `<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width, initial-scale=1.0"><title>Notion2API</title>
<style>body{font-family:"Segoe UI",sans-serif;background:radial-gradient(circle at top,#efe4cf 0,#d5c0a1 45%,#7a6448 100%);color:#1b1309;display:flex;min-height:100vh;align-items:center;justify-content:center;margin:0}main{max-width:760px;padding:40px 32px;background:rgba(255,248,236,.78);border:1px solid rgba(59,37,18,.12);box-shadow:0 25px 80px rgba(33,18,4,.18);backdrop-filter:blur(12px);border-radius:28px}h1{font-size:48px;margin:0 0 12px;font-family:Georgia,"Times New Roman",serif}p{font-size:18px;line-height:1.7;margin:0 0 14px}.links{display:flex;gap:14px;flex-wrap:wrap;margin-top:24px}.links a{display:inline-flex;align-items:center;justify-content:center;padding:12px 18px;border-radius:999px;background:#1f1408;color:#fff7ea;text-decoration:none;font-weight:700}.links a.secondary{background:#efe0c7;color:#3d2812}</style>
</head><body><main><h1>Notion2API</h1><p>OpenAI 兼容桥接层，已接入模型列表、联网开关、图片 / PDF / CSV 附件，以及本地 WebUI 管理页。</p><p>默认管理入口沿用 <code>/admin</code>，OpenAI 兼容接口在 <code>/v1/*</code>。</p><div class="links"><a href="/admin">打开控制台</a><a class="secondary" href="/v1/models">查看模型</a><a class="secondary" href="/healthz">健康检查</a></div></main></body></html>`

func resolveStaticAdminDir(preferred string) string {
	preferred = strings.TrimSpace(preferred)
	if preferred == "" {
		preferred = "static/admin"
	}
	candidates := []string{preferred}
	if override := strings.TrimSpace(os.Getenv("NOTION2API_STATIC_ADMIN_DIR")); override != "" {
		candidates = append([]string{override}, candidates...)
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, preferred))
	}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(exeDir, preferred),
			filepath.Join(filepath.Dir(exeDir), preferred),
		)
	}
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		if candidate == "" {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		if stat, err := os.Stat(candidate); err == nil && stat.IsDir() {
			return candidate
		}
	}
	return filepath.Clean(preferred)
}

func requestedAdminDispatchMode(payload map[string]any) string {
	mode := strings.TrimSpace(strings.ToLower(stringValue(payload["dispatch_mode"])))
	switch mode {
	case "active", "pinned", "pin":
		return "active"
	case "pool", "auto", "":
		if boolValue(payload["pin_active_account"]) {
			return "active"
		}
		return "pool"
	default:
		if boolValue(payload["pin_active_account"]) {
			return "active"
		}
		return "pool"
	}
}

func adminSyncRequestTimeout(cfg AppConfig) time.Duration {
	timeout := time.Duration(maxInt(cfg.TimeoutSec, 1)) * time.Second
	if timeout < adminSyncRequestTimeoutMin {
		timeout = adminSyncRequestTimeoutMin
	}
	if adminSyncRequestTimeoutCap > 0 && timeout > adminSyncRequestTimeoutCap {
		timeout = adminSyncRequestTimeoutCap
	}
	return timeout
}

func cloneRequestWithTimeout(r *http.Request, timeout time.Duration) (*http.Request, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	return r.Clone(ctx), cancel
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(lower, "context deadline exceeded") || strings.Contains(lower, "timeout")
}

func writeAdminUpstreamError(w http.ResponseWriter, err error, extras map[string]any) {
	status := http.StatusBadGateway
	if isTimeoutError(err) {
		status = http.StatusGatewayTimeout
	}
	payload := map[string]any{
		"detail": err.Error(),
	}
	for key, value := range extras {
		payload[key] = value
	}
	writeJSON(w, status, payload)
}

func (a *App) serveIndex(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(welcomeHTML))
}

func adminClientIP(r *http.Request) string {
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		if len(parts) > 0 {
			if ip := strings.TrimSpace(parts[0]); ip != "" {
				return ip
			}
		}
	}
	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		return realIP
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func securePasswordEqual(expected string, candidate string) bool {
	expectedBytes := []byte(expected)
	candidateBytes := []byte(candidate)
	return subtle.ConstantTimeCompare(expectedBytes, candidateBytes) == 1
}

func shouldUseSecureCookie(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https") {
		return true
	}
	return false
}

func (a *App) cleanupAdminLoginAttemptsLocked(now time.Time) {
	for key, attempt := range a.State.AdminLoginAttempts {
		if attempt.Failures <= 0 && attempt.LockedUntil.IsZero() {
			delete(a.State.AdminLoginAttempts, key)
			continue
		}
		if !attempt.LockedUntil.IsZero() && now.After(attempt.LockedUntil) && now.Sub(attempt.LastFailure) > adminLoginLockWindow {
			delete(a.State.AdminLoginAttempts, key)
		}
	}
}

func (a *App) adminLoginLocked(clientIP string) (time.Time, bool) {
	now := time.Now()
	a.State.mu.Lock()
	defer a.State.mu.Unlock()
	a.cleanupAdminLoginAttemptsLocked(now)
	attempt, ok := a.State.AdminLoginAttempts[clientIP]
	if !ok || attempt.LockedUntil.IsZero() || now.After(attempt.LockedUntil) {
		return time.Time{}, false
	}
	return attempt.LockedUntil, true
}

func (a *App) recordAdminLoginFailure(clientIP string) (time.Time, bool) {
	now := time.Now()
	a.State.mu.Lock()
	defer a.State.mu.Unlock()
	a.cleanupAdminLoginAttemptsLocked(now)
	attempt := a.State.AdminLoginAttempts[clientIP]
	attempt.Failures++
	attempt.LastFailure = now
	if attempt.Failures >= adminLoginMaxFailures {
		attempt.LockedUntil = now.Add(adminLoginLockWindow)
	}
	a.State.AdminLoginAttempts[clientIP] = attempt
	if attempt.LockedUntil.IsZero() {
		return time.Time{}, false
	}
	return attempt.LockedUntil, true
}

func (a *App) clearAdminLoginFailures(clientIP string) {
	a.State.mu.Lock()
	defer a.State.mu.Unlock()
	delete(a.State.AdminLoginAttempts, clientIP)
}

func (a *App) issueAdminToken() string {
	token := strings.ReplaceAll(randomUUID(), "-", "")
	cfg, _, _ := a.State.Snapshot()
	expiresAt := time.Now().Add(time.Duration(maxInt(cfg.Admin.TokenTTLHours, 1)) * time.Hour)
	a.State.mu.Lock()
	defer a.State.mu.Unlock()
	a.State.AdminTokens[token] = expiresAt
	for key, deadline := range a.State.AdminTokens {
		if time.Now().After(deadline) {
			delete(a.State.AdminTokens, key)
		}
	}
	return token
}

func (a *App) revokeAdminToken(token string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}
	a.State.mu.Lock()
	defer a.State.mu.Unlock()
	delete(a.State.AdminTokens, token)
}

func (a *App) adminTokenValid(token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	now := time.Now()
	a.State.mu.Lock()
	defer a.State.mu.Unlock()
	deadline, ok := a.State.AdminTokens[token]
	if !ok {
		return false
	}
	if now.After(deadline) {
		delete(a.State.AdminTokens, token)
		return false
	}
	return true
}

func adminTokenFromRequest(r *http.Request) string {
	if token := strings.TrimSpace(r.Header.Get("X-Admin-Token")); token != "" {
		return token
	}
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	}
	if cookie, err := r.Cookie("notion2api_admin"); err == nil {
		return strings.TrimSpace(cookie.Value)
	}
	return ""
}

func (a *App) adminAuthOK(w http.ResponseWriter, r *http.Request) bool {
	cfg, _, _ := a.State.Snapshot()
	if !cfg.Admin.Enabled {
		writeJSON(w, http.StatusForbidden, map[string]any{"detail": "admin disabled"})
		return false
	}
	password := strings.TrimSpace(cfg.Admin.Password)
	if password == "" {
		writeJSON(w, http.StatusForbidden, map[string]any{"detail": "admin password is not configured"})
		return false
	}
	if a.adminTokenValid(adminTokenFromRequest(r)) {
		return true
	}
	writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "admin authentication required"})
	return false
}

func redactConfigSecrets(cfg AppConfig) AppConfig {
	cfg.APIKey = ""
	cfg.Admin.Password = ""
	return cfg
}

func (a *App) getConfigPayload() map[string]any {
	cfg, session, registry := a.State.Snapshot()
	a.State.mu.RLock()
	sessionReady := a.State.Client != nil
	lastRefresh := a.State.LastSessionRefresh
	lastRefreshError := a.State.LastSessionRefreshError
	a.State.mu.RUnlock()
	safeConfig := redactConfigSecrets(cfg)
	return map[string]any{
		"success":        true,
		"config":         safeConfig,
		"config_path":    cfg.ConfigPath,
		"active_account": cfg.ActiveAccount,
		"secrets": map[string]any{
			"api_key_set":        strings.TrimSpace(cfg.APIKey) != "",
			"admin_password_set": strings.TrimSpace(cfg.Admin.Password) != "",
		},
		"session_ready": sessionReady,
		"session": map[string]any{
			"probe_path":     session.ProbePath,
			"client_version": session.ClientVersion,
			"user_id":        session.UserID,
			"user_email":     session.UserEmail,
			"user_name":      session.UserName,
			"space_id":       session.SpaceID,
			"space_name":     session.SpaceName,
			"cookie_count":   len(session.Cookies),
		},
		"session_refresh_runtime": map[string]any{
			"last_refresh_at": formatTimeOrEmpty(lastRefresh),
			"last_error":      lastRefreshError,
		},
		"models":        registry.Entries,
		"default_model": cfg.DefaultPublicModel(),
	}
}

func (a *App) getSettingsPayload() map[string]any {
	cfg, session, registry := a.State.Snapshot()
	a.State.mu.RLock()
	sessionReady := a.State.Client != nil
	lastRefresh := a.State.LastSessionRefresh
	lastRefreshError := a.State.LastSessionRefreshError
	a.State.mu.RUnlock()
	safeConfig := redactConfigSecrets(cfg)
	return map[string]any{
		"success": true,
		"config":  safeConfig,
		"admin": map[string]any{
			"enabled":         cfg.Admin.Enabled,
			"has_password":    strings.TrimSpace(cfg.Admin.Password) != "",
			"token_ttl_hours": cfg.Admin.TokenTTLHours,
			"static_dir":      cfg.Admin.StaticDir,
		},
		"secrets": map[string]any{
			"api_key_set":        strings.TrimSpace(cfg.APIKey) != "",
			"admin_password_set": strings.TrimSpace(cfg.Admin.Password) != "",
		},
		"runtime": map[string]any{
			"timeout_sec":        cfg.TimeoutSec,
			"poll_interval_sec":  cfg.PollIntervalSec,
			"poll_max_rounds":    cfg.PollMaxRounds,
			"stream_chunk_runes": cfg.StreamChunkRunes,
		},
		"responses":       cfg.Responses,
		"features":        cfg.Features,
		"session_refresh": cfg.ResolveSessionRefresh(),
		"default_model":   cfg.DefaultPublicModel(),
		"model_aliases":   cfg.ModelAliases,
		"models":          registry.Entries,
		"active_account":  cfg.ActiveAccount,
		"session_ready":   sessionReady,
		"session": map[string]any{
			"user_email": session.UserEmail,
			"space_id":   session.SpaceID,
			"space_name": session.SpaceName,
		},
		"session_refresh_runtime": map[string]any{
			"last_refresh_at": formatTimeOrEmpty(lastRefresh),
			"last_error":      lastRefreshError,
		},
	}
}

func (a *App) mergeConfigFromBody(r *http.Request) (AppConfig, error) {
	current, _, _ := a.State.Snapshot()
	defer r.Body.Close()
	var raw map[string]any
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		return current, fmt.Errorf("invalid json")
	}
	if nested, ok := raw["config"].(map[string]any); ok {
		raw = nested
	}
	body, err := json.Marshal(raw)
	if err != nil {
		return current, err
	}
	cfg := current
	if err := json.Unmarshal(body, &cfg); err != nil {
		return current, err
	}
	cfg.ConfigPath = current.ConfigPath
	return normalizeConfig(cfg), nil
}

func (a *App) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	cfg, _, _ := a.State.Snapshot()
	if !cfg.Admin.Enabled {
		writeJSON(w, http.StatusForbidden, map[string]any{"detail": "admin disabled"})
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	password := cfg.Admin.Password
	if strings.TrimSpace(password) == "" {
		writeJSON(w, http.StatusForbidden, map[string]any{"detail": "admin password is not configured"})
		return
	}
	clientIP := adminClientIP(r)
	if lockedUntil, locked := a.adminLoginLocked(clientIP); locked {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"detail":       "too many failed login attempts",
			"locked_until": lockedUntil.Format(time.RFC3339),
		})
		return
	}
	payload, err := decodeBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if !securePasswordEqual(password, stringValue(payload["password"])) {
		lockedUntil, locked := a.recordAdminLoginFailure(clientIP)
		body := map[string]any{"detail": "wrong password"}
		if locked {
			body["locked_until"] = lockedUntil.Format(time.RFC3339)
		}
		writeJSON(w, http.StatusUnauthorized, body)
		return
	}
	a.clearAdminLoginFailures(clientIP)
	token := a.issueAdminToken()
	http.SetCookie(w, &http.Cookie{
		Name:     "notion2api_admin",
		Value:    token,
		HttpOnly: true,
		Path:     "/",
		Secure:   shouldUseSecureCookie(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxInt(cfg.Admin.TokenTTLHours, 1) * 3600,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"success":           true,
		"password_required": true,
	})
}

func (a *App) handleAdminVerify(w http.ResponseWriter, r *http.Request) {
	cfg, _, _ := a.State.Snapshot()
	passwordRequired := true
	passwordConfigured := strings.TrimSpace(cfg.Admin.Password) != ""
	authenticated := passwordConfigured && a.adminTokenValid(adminTokenFromRequest(r))
	writeJSON(w, http.StatusOK, map[string]any{
		"success":             true,
		"authenticated":       authenticated,
		"password_required":   passwordRequired,
		"password_configured": passwordConfigured,
		"admin_enabled":       cfg.Admin.Enabled,
	})
}

func (a *App) handleAdminLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	token := adminTokenFromRequest(r)
	a.revokeAdminToken(token)
	http.SetCookie(w, &http.Cookie{
		Name:     "notion2api_admin",
		Value:    "",
		HttpOnly: true,
		Path:     "/",
		Secure:   shouldUseSecureCookie(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
	})
}

func (a *App) handleAdminConfig(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthOK(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, a.getConfigPayload())
	case http.MethodPost:
		cfg, err := a.mergeConfigFromBody(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
			return
		}
		if err := a.State.SaveAndApply(cfg); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "config updated", "persisted": strings.TrimSpace(cfg.ConfigPath) != ""})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
	}
}

func configSnapshotDir(cfg AppConfig) string {
	if strings.TrimSpace(cfg.ConfigPath) != "" {
		return filepath.Join(filepath.Dir(cfg.ConfigPath), "config_snapshots")
	}
	return filepath.Clean("config_snapshots")
}

func (a *App) handleAdminConfigExport(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthOK(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	cfg, _, _ := a.State.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"success":     true,
		"exported_at": time.Now().Format(time.RFC3339),
		"config":      normalizeConfig(cfg),
	})
}

func (a *App) handleAdminConfigImport(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthOK(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	cfg, err := a.mergeConfigFromBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if err := a.State.SaveAndApply(cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "config imported"})
}

func (a *App) handleAdminConfigSnapshot(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthOK(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		cfg, _, _ := a.State.Snapshot()
		dir := configSnapshotDir(cfg)
		entries, err := os.ReadDir(dir)
		if err != nil && !os.IsNotExist(err) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
			return
		}
		items := make([]map[string]any, 0, len(entries))
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
				continue
			}
			fullPath := filepath.Join(dir, entry.Name())
			info, err := entry.Info()
			if err != nil {
				continue
			}
			items = append(items, map[string]any{
				"name":        entry.Name(),
				"path":        fullPath,
				"size_bytes":  info.Size(),
				"modified_at": info.ModTime().Format(time.RFC3339),
			})
		}
		sort.Slice(items, func(i, j int) bool {
			return stringValue(items[i]["modified_at"]) > stringValue(items[j]["modified_at"])
		})
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"dir":     dir,
			"items":   items,
		})
	case http.MethodPost:
		cfg, _, _ := a.State.Snapshot()
		dir := configSnapshotDir(cfg)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
			return
		}
		name := fmt.Sprintf("notion2api_%s.json", time.Now().Format("20060102_150405"))
		fullPath := filepath.Join(dir, name)
		exported := normalizeConfig(cfg)
		exported.ConfigPath = ""
		if err := writePrettyJSONFile(fullPath, exported); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success":    true,
			"snapshot":   fullPath,
			"created_at": time.Now().Format(time.RFC3339),
		})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
	}
}

func (a *App) handleAdminSettings(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthOK(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, a.getSettingsPayload())
	case http.MethodPut, http.MethodPost:
		cfg, err := a.mergeConfigFromBody(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
			return
		}
		if err := a.State.SaveAndApply(cfg); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "settings updated"})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
	}
}

func (a *App) handleAdminVersion(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthOK(w, r) {
		return
	}
	cfg, session, registry := a.State.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"success":       true,
		"name":          "notion2api",
		"version":       "2026.03.21-local-go",
		"checked_at":    time.Now().UTC().Format(time.RFC3339),
		"default_model": cfg.DefaultPublicModel(),
		"model_count":   len(registry.Entries),
		"user_email":    session.UserEmail,
		"space_id":      session.SpaceID,
		"features":      cfg.Features,
		"responses":     cfg.Responses,
	})
}

func (a *App) handleAdminTest(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthOK(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	payload, err := decodeBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	cfg, _, registry := a.State.Snapshot()
	prompt := strings.TrimSpace(stringValue(payload["prompt"]))
	attachments, err := extractAttachmentsFromAny(payload["attachments"])
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if prompt == "" && len(attachments) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "prompt or attachments required"})
		return
	}
	entry, err := registry.Resolve(requestedModel(payload, cfg.DefaultPublicModel()), cfg.DefaultPublicModel())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	preferredConversationID := requestedConversationID(r, payload)
	request := PromptRunRequest{
		Prompt:                            prompt,
		LatestUserPrompt:                  prompt,
		PublicModel:                       entry.ID,
		NotionModel:                       entry.NotionModel,
		UseWebSearch:                      requestedWebSearch(payload, cfg.Features.UseWebSearch),
		Attachments:                       attachments,
		SuppressUpstreamThreadPersistence: strings.TrimSpace(preferredConversationID) == "",
	}
	request.PinnedAccountEmail = requestedAccountEmail(r, payload)
	if request.PinnedAccountEmail == "" && requestedAdminDispatchMode(payload) == "active" {
		if account, _, ok := cfg.ResolveActiveAccount(); ok {
			request.PinnedAccountEmail = account.Email
		}
	}
	conversation := ConversationEntry{}
	if preferredConversationID != "" {
		if matched, ok := a.resolveContinuationConversation(r, payload, "", "", nil); ok {
			conversation = matched.Conversation
			request.UpstreamThreadID = strings.TrimSpace(conversation.ThreadID)
			request.PinnedAccountEmail = firstNonEmpty(strings.TrimSpace(conversation.AccountEmail), request.PinnedAccountEmail)
			request.continuationDraft = buildContinuationDraft(matched.Session)
		}
	}
	request.ConversationID = firstNonEmpty(strings.TrimSpace(conversation.ID), preferredConversationID)
	conversationID := a.startConversationTurn(conversation.ID, preferredConversationID, "admin_tester", "admin_test", prompt, request)
	timedRequest, cancel := cloneRequestWithTimeout(r, adminSyncRequestTimeout(cfg))
	defer cancel()
	result, err := a.runPrompt(timedRequest, request)
	if err != nil {
		a.failConversation(conversationID, err)
		writeAdminUpstreamError(w, err, nil)
		return
	}
	a.completeConversation(conversationID, result)
	a.persistConversationSession(conversationID, request, result)
	writeJSON(w, http.StatusOK, map[string]any{
		"success":         true,
		"conversation_id": conversationID,
		"result":          buildChatCompletion(result, entry.ID, true),
		"text":            sanitizeAssistantVisibleText(result.Text),
	})
}

func (a *App) serveAdminStatic(w http.ResponseWriter, r *http.Request) {
	cfg, _, _ := a.State.Snapshot()
	staticDir := resolveStaticAdminDir(cfg.Admin.StaticDir)
	if stat, err := os.Stat(staticDir); err != nil || !stat.IsDir() {
		http.Error(w, "WebUI not found. Expected static files under static/admin.", http.StatusNotFound)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/admin")
	path = strings.TrimPrefix(path, "/")
	if path != "" && strings.Contains(path, ".") {
		full := filepath.Join(staticDir, filepath.Clean(path))
		if !strings.HasPrefix(full, staticDir) {
			http.NotFound(w, r)
			return
		}
		if _, err := os.Stat(full); err == nil {
			if strings.HasPrefix(path, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-store, must-revalidate")
			}
			http.ServeFile(w, r, full)
			return
		}
		http.NotFound(w, r)
		return
	}
	index := filepath.Join(staticDir, "index.html")
	if _, err := os.Stat(index); err != nil {
		http.Error(w, "index.html not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "no-store, must-revalidate")
	http.ServeFile(w, r, index)
}

func (a *App) handleAdmin(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/admin/login":
		a.handleAdminLogin(w, r)
	case r.URL.Path == "/admin/logout":
		a.handleAdminLogout(w, r)
	case r.URL.Path == "/admin/verify":
		a.handleAdminVerify(w, r)
	case r.URL.Path == "/admin/config":
		a.handleAdminConfig(w, r)
	case r.URL.Path == "/admin/config/export":
		a.handleAdminConfigExport(w, r)
	case r.URL.Path == "/admin/config/import":
		a.handleAdminConfigImport(w, r)
	case r.URL.Path == "/admin/config/snapshot":
		a.handleAdminConfigSnapshot(w, r)
	case r.URL.Path == "/admin/settings":
		a.handleAdminSettings(w, r)
	case r.URL.Path == "/admin/version":
		a.handleAdminVersion(w, r)
	case r.URL.Path == "/admin/test":
		a.handleAdminTest(w, r)
	case r.URL.Path == "/admin/events":
		a.handleAdminEvents(w, r)
	case r.URL.Path == "/admin/conversations":
		a.handleAdminConversations(w, r)
	case r.URL.Path == "/admin/conversations/batch-delete":
		a.handleAdminConversationBatchDelete(w, r)
	case strings.HasPrefix(r.URL.Path, "/admin/conversations/"):
		a.handleAdminConversationByID(w, r)
	case r.URL.Path == "/admin/accounts":
		a.handleAdminAccounts(w, r)
	case r.URL.Path == "/admin/accounts/activate":
		a.handleAdminAccountsActivate(w, r)
	case r.URL.Path == "/admin/accounts/test":
		a.handleAdminAccountsTest(w, r)
	case r.URL.Path == "/admin/accounts/login/start":
		a.handleAdminAccountLoginStart(w, r)
	case r.URL.Path == "/admin/accounts/login/verify":
		a.handleAdminAccountLoginVerify(w, r)
	case r.URL.Path == "/admin/accounts/manual":
		a.handleAdminAccountManualImport(w, r)
	case r.URL.Path == "/admin/accounts/login/status":
		a.handleAdminAccountLoginStatus(w, r)
	case strings.HasPrefix(r.URL.Path, "/admin/accounts/"):
		a.handleAdminAccountDelete(w, r)
	default:
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": "admin route not found"})
			return
		}
		a.serveAdminStatic(w, r)
	}
}
