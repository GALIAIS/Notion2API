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
<style>:root{--bg-base:#F8FAFC;--bg-soft:#EEF2FF;--card:#FFFFFF;--border:#E2E8F0;--ink:#0F172A;--ink-soft:#475569;--ink-muted:#64748B;--brand-1:#4F46E5;--brand-2:#7C3AED;--brand-3:#A855F7;--shadow-soft:0 4px 20px -2px rgba(79,70,229,0.10);--shadow-elevated:0 12px 28px -10px rgba(79,70,229,0.18),0 6px 12px -8px rgba(124,58,237,0.12);--shadow-button:0 8px 18px -8px rgba(79,70,229,0.55);--shadow-button-hover:0 12px 24px -10px rgba(79,70,229,0.65)}*{box-sizing:border-box}body{font-family:"Plus Jakarta Sans","Inter",system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;background:linear-gradient(135deg,var(--bg-soft) 0%,var(--bg-base) 60%,#FAF5FF 100%);color:var(--ink);display:flex;min-height:100vh;align-items:center;justify-content:center;margin:0;padding:32px;-webkit-font-smoothing:antialiased}.shell{position:relative;width:100%;max-width:760px}.shell::before{content:"";position:absolute;inset:-40px;background:radial-gradient(circle at 25% 20%,rgba(79,70,229,0.18),transparent 55%),radial-gradient(circle at 80% 80%,rgba(168,85,247,0.16),transparent 60%);filter:blur(40px);z-index:0;pointer-events:none}main{position:relative;z-index:1;background:var(--card);border:1px solid var(--border);box-shadow:var(--shadow-elevated);border-radius:16px;padding:40px 40px 36px;overflow:hidden}main::before{content:"";position:absolute;left:0;right:0;top:0;height:3px;background:linear-gradient(135deg,var(--brand-1) 0%,var(--brand-2) 50%,var(--brand-3) 100%)}.brand{display:inline-flex;align-items:center;gap:10px;padding:6px 12px;border-radius:999px;background:color-mix(in oklab,var(--brand-1) 8%,var(--card));border:1px solid color-mix(in oklab,var(--brand-1) 22%,var(--border));font-size:12px;font-weight:600;letter-spacing:0.08em;text-transform:uppercase;color:var(--brand-1);margin-bottom:18px}.brand-dot{width:8px;height:8px;border-radius:999px;background:linear-gradient(135deg,var(--brand-1),var(--brand-3));box-shadow:0 0 0 3px color-mix(in oklab,var(--brand-1) 18%,transparent)}h1{font-size:44px;line-height:1.15;letter-spacing:-0.02em;margin:0 0 14px;font-weight:700;background:linear-gradient(135deg,var(--brand-1) 0%,var(--brand-2) 50%,var(--brand-3) 100%);-webkit-background-clip:text;background-clip:text;color:transparent}p{font-size:16px;line-height:1.7;margin:0 0 12px;color:var(--ink-soft)}p code{font-family:"JetBrains Mono","SF Mono",ui-monospace,Menlo,Consolas,monospace;background:color-mix(in oklab,var(--brand-1) 8%,var(--card));border:1px solid color-mix(in oklab,var(--brand-1) 18%,var(--border));color:var(--brand-1);padding:1px 8px;border-radius:8px;font-size:13px;font-weight:600}.links{display:flex;gap:10px;flex-wrap:wrap;margin-top:28px}.links a{display:inline-flex;align-items:center;gap:8px;padding:11px 18px;border-radius:10px;font-size:14px;font-weight:600;text-decoration:none;transition:transform .15s ease,box-shadow .15s ease,background .15s ease,border-color .15s ease;letter-spacing:-0.005em}.links a.primary{background:linear-gradient(135deg,var(--brand-1) 0%,var(--brand-2) 100%);color:#fff;box-shadow:var(--shadow-button);border:1px solid transparent}.links a.primary:hover{box-shadow:var(--shadow-button-hover);transform:translateY(-1px)}.links a.secondary{background:var(--card);color:var(--ink);border:1px solid var(--border);box-shadow:var(--shadow-soft)}.links a.secondary:hover{border-color:color-mix(in oklab,var(--brand-1) 35%,var(--border));color:var(--brand-1);transform:translateY(-1px)}.links svg{width:14px;height:14px;flex-shrink:0}.meta{margin-top:24px;padding-top:18px;border-top:1px solid var(--border);font-size:12px;color:var(--ink-muted);display:flex;align-items:center;gap:8px;flex-wrap:wrap}.meta span{font-family:"JetBrains Mono",ui-monospace,Menlo,Consolas,monospace;font-size:11px;letter-spacing:0.02em}.meta .dot{width:4px;height:4px;border-radius:999px;background:var(--ink-muted);opacity:.5}@media (prefers-color-scheme:dark){:root{--bg-base:#0B1120;--bg-soft:#10172A;--card:#111827;--border:#1F2937;--ink:#F8FAFC;--ink-soft:#CBD5E1;--ink-muted:#94A3B8}body{background:linear-gradient(135deg,var(--bg-soft) 0%,var(--bg-base) 60%,#1A0B2E 100%)}.shell::before{background:radial-gradient(circle at 25% 20%,rgba(79,70,229,0.32),transparent 55%),radial-gradient(circle at 80% 80%,rgba(168,85,247,0.24),transparent 60%)}}@media (max-width:560px){main{padding:28px 22px}h1{font-size:32px}.links a{flex:1 1 100%;justify-content:center}}</style>
</head><body><div class="shell"><main><div class="brand"><span class="brand-dot"></span>Notion2API · Enterprise</div><h1>OpenAI 兼容的 Notion 智能接入层</h1><p>提供模型列表、联网开关、图片 / PDF / CSV 附件上传，以及一套本地可控的 WebUI 管理控制台。</p><p>管理入口<code>/admin</code>，OpenAI 兼容接口<code>/v1/*</code>，健康探针<code>/healthz</code>。</p><div class="links"><a class="primary" href="/admin"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="M3 12h18M13 5l7 7-7 7"/></svg>打开控制台</a><a class="secondary" href="/v1/models"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 6h16M4 12h16M4 18h10"/></svg>模型列表</a><a class="secondary" href="/healthz"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M22 12h-4l-3 9L9 3l-3 9H2"/></svg>健康检查</a></div><div class="meta"><span>service:notion2api</span><span class="dot"></span><span>protocol:openai-compatible</span><span class="dot"></span><span>ui:enterprise</span></div></main></div></body></html>`

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
	freshThreadMode := forceFreshThreadPerRequest(cfg)
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
			request.PinnedAccountEmail = firstNonEmpty(strings.TrimSpace(conversation.AccountEmail), request.PinnedAccountEmail)
			if freshThreadMode {
				request.ForceLocalConversationContinue = strings.TrimSpace(conversation.ID) != ""
				request.Prompt = buildFreshThreadReplayPromptFromConversation(conversation, prompt, attachments, prompt)
			} else {
				request.UpstreamThreadID = strings.TrimSpace(conversation.ThreadID)
				request.continuationDraft = buildContinuationDraft(matched.Session)
			}
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
