package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type manualAccountImportRequest struct {
	Email         string `json:"email"`
	UserID        string `json:"user_id"`
	UserName      string `json:"user_name"`
	SpaceID       string `json:"space_id"`
	SpaceViewID   string `json:"space_view_id"`
	SpaceName     string `json:"space_name"`
	ClientVersion string `json:"client_version"`
	CookieHeader  string `json:"cookie_header"`
	ProbeJSONText string `json:"probe_json_text"`
	Active        bool   `json:"active"`
}

func parseManualImportProbeJSON(raw string) (probePayload, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return probePayload{}, nil
	}
	var probe probePayload
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return probePayload{}, fmt.Errorf("probe_json_text invalid: %w", err)
	}
	probe.Email = strings.TrimSpace(probe.Email)
	probe.UserID = strings.TrimSpace(probe.UserID)
	probe.UserName = strings.TrimSpace(probe.UserName)
	probe.SpaceID = strings.TrimSpace(probe.SpaceID)
	probe.SpaceViewID = strings.TrimSpace(probe.SpaceViewID)
	probe.SpaceName = strings.TrimSpace(probe.SpaceName)
	probe.ClientVersion = strings.TrimSpace(probe.ClientVersion)
	return probe, nil
}

func (a *App) accountRuntimeSummary(cfg AppConfig, account NotionAccount) map[string]any {
	account = ensureAccountPaths(cfg, account)
	now := time.Now()
	remainingQuota, quotaLimited := accountRemainingQuota(account, now)
	cooldownUntil := parseOptionalRFC3339(account.CooldownUntil)
	item := map[string]any{
		"email":                  account.Email,
		"probe_json":             account.ProbeJSON,
		"probe_exists":           fileExists(account.ProbeJSON),
		"profile_dir":            account.ProfileDir,
		"profile_dir_exists":     dirExists(account.ProfileDir),
		"storage_state_path":     account.StorageStatePath,
		"storage_state_exists":   fileExists(account.StorageStatePath),
		"pending_state_path":     account.PendingStatePath,
		"pending_state_exists":   fileExists(account.PendingStatePath),
		"user_id":                account.UserID,
		"user_name":              account.UserName,
		"space_id":               account.SpaceID,
		"space_view_id":          account.SpaceViewID,
		"space_name":             account.SpaceName,
		"plan_type":              account.PlanType,
		"client_version":         account.ClientVersion,
		"status":                 account.Status,
		"last_error":             account.LastError,
		"last_login_at":          account.LastLoginAt,
		"disabled":               account.Disabled,
		"priority":               account.Priority,
		"hourly_quota":           account.HourlyQuota,
		"max_concurrency":        normalizeAccountMaxConcurrency(account.MaxConcurrency),
		"quota_limited":          quotaLimited,
		"remaining_quota":        remainingQuota,
		"window_started_at":      account.WindowStartedAt,
		"window_request_count":   account.WindowRequestCount,
		"cooldown_until":         account.CooldownUntil,
		"cooldown_active":        accountCooldownActive(account, now),
		"cooldown_remaining_sec": maxInt(int(time.Until(cooldownUntil).Seconds()), 0),
		"last_used_at":           account.LastUsedAt,
		"last_success_at":        account.LastSuccessAt,
		"last_refresh_at":        account.LastRefreshAt,
		"last_relogin_at":        account.LastReloginAt,
		"consecutive_failures":   account.ConsecutiveFailures,
		"total_successes":        account.TotalSuccesses,
		"total_failures":         account.TotalFailures,
		"active":                 canonicalEmailKey(cfg.ActiveAccount) == getAccountEmailKey(account),
	}
	if status, err := readLoginStatusFile(account.PendingStatePath); err == nil {
		item["login_status"] = status
		if text := firstNonEmpty(status.Status, account.Status); text != "" {
			item["status"] = text
		}
		if text := firstNonEmpty(status.Error, account.LastError); text != "" {
			item["last_error"] = text
		}
		if text := firstNonEmpty(status.LastLoginAt, account.LastLoginAt); text != "" {
			item["last_login_at"] = text
		}
		if text := firstNonEmpty(status.UserID, account.UserID); text != "" {
			item["user_id"] = text
		}
		if text := firstNonEmpty(status.UserName, account.UserName); text != "" {
			item["user_name"] = text
		}
		if text := firstNonEmpty(status.SpaceID, account.SpaceID); text != "" {
			item["space_id"] = text
		}
		if text := firstNonEmpty(status.SpaceViewID, account.SpaceViewID); text != "" {
			item["space_view_id"] = text
		}
		if text := firstNonEmpty(status.SpaceName, account.SpaceName); text != "" {
			item["space_name"] = text
		}
		if text := firstNonEmpty(status.ClientVersion, account.ClientVersion); text != "" {
			item["client_version"] = text
		}
	}
	return item
}

func (a *App) buildAccountsPayload() map[string]any {
	cfg, session, _ := a.State.Snapshot()
	a.State.mu.RLock()
	sessionReady := a.State.Client != nil
	lastRefresh := a.State.LastSessionRefresh
	lastRefreshError := a.State.LastSessionRefreshError
	a.State.mu.RUnlock()
	items := make([]map[string]any, 0, len(cfg.Accounts))
	for _, account := range cfg.Accounts {
		items = append(items, a.accountRuntimeSummary(cfg, account))
	}
	return map[string]any{
		"success":        true,
		"items":          items,
		"active_account": cfg.ActiveAccount,
		"session_ready":  sessionReady,
		"session": map[string]any{
			"user_email":    session.UserEmail,
			"user_id":       session.UserID,
			"space_id":      session.SpaceID,
			"space_view_id": session.SpaceViewID,
			"space_name":    session.SpaceName,
			"probe_path":    session.ProbePath,
		},
		"login_helper":    cfg.ResolveLoginHelper(),
		"session_refresh": cfg.ResolveSessionRefresh(),
		"session_refresh_runtime": map[string]any{
			"last_refresh_at": formatTimeOrEmpty(lastRefresh),
			"last_error":      lastRefreshError,
		},
	}
}

func decodeAccountPayload(payload map[string]any) (NotionAccount, bool, error) {
	if nested, ok := payload["account"].(map[string]any); ok {
		payload = nested
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return NotionAccount{}, false, err
	}
	var account NotionAccount
	if err := json.Unmarshal(raw, &account); err != nil {
		return NotionAccount{}, false, err
	}
	account.Email = strings.TrimSpace(account.Email)
	if account.Email == "" {
		return NotionAccount{}, false, fmt.Errorf("email is required")
	}
	active, _ := payload["active"].(bool)
	return account, active, nil
}

func accountPayloadMap(payload map[string]any) map[string]any {
	if nested, ok := payload["account"].(map[string]any); ok {
		return nested
	}
	return payload
}

func accountEmailFromPayload(payload map[string]any) string {
	return strings.TrimSpace(stringValue(accountPayloadMap(payload)["email"]))
}

func intFromPayloadValue(value any) (int, error) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, err
		}
		return int(parsed), nil
	case float64:
		return int(typed), nil
	case float32:
		return int(typed), nil
	case int:
		return typed, nil
	case int64:
		return int(typed), nil
	case string:
		clean := strings.TrimSpace(typed)
		if clean == "" {
			return 0, nil
		}
		return strconv.Atoi(clean)
	default:
		return 0, fmt.Errorf("unsupported numeric value: %T", value)
	}
}

func mergeEditableAccountFields(existing NotionAccount, payload map[string]any) (NotionAccount, bool, error) {
	next := existing
	accountPayload := accountPayloadMap(payload)
	if raw, ok := accountPayload["disabled"]; ok {
		disabled, ok := raw.(bool)
		if !ok {
			return NotionAccount{}, false, fmt.Errorf("disabled must be boolean")
		}
		next.Disabled = disabled
	}
	if raw, ok := accountPayload["priority"]; ok {
		priority, err := intFromPayloadValue(raw)
		if err != nil {
			return NotionAccount{}, false, fmt.Errorf("priority invalid: %w", err)
		}
		next.Priority = priority
	}
	if raw, ok := accountPayload["hourly_quota"]; ok {
		quota, err := intFromPayloadValue(raw)
		if err != nil {
			return NotionAccount{}, false, fmt.Errorf("hourly_quota invalid: %w", err)
		}
		if quota < 0 {
			return NotionAccount{}, false, fmt.Errorf("hourly_quota must be >= 0")
		}
		next.HourlyQuota = quota
	}
	if raw, ok := accountPayload["max_concurrency"]; ok {
		limit, err := intFromPayloadValue(raw)
		if err != nil {
			return NotionAccount{}, false, fmt.Errorf("max_concurrency invalid: %w", err)
		}
		if limit < 1 {
			return NotionAccount{}, false, fmt.Errorf("max_concurrency must be >= 1")
		}
		next.MaxConcurrency = limit
	}
	makeActive, _ := payload["active"].(bool)
	return next, makeActive, nil
}

func (a *App) handleAdminAccounts(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthOK(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, a.buildAccountsPayload())
	case http.MethodPost:
		payload, err := a.decodeBody(w, r)
		if err != nil {
			writeInvalidBodyError(w, err)
			return
		}
		account, makeActive, err := decodeAccountPayload(payload)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
			return
		}
		cfg, _, _ := a.State.Snapshot()
		account, _ = cfg.UpsertAccount(account)
		if makeActive {
			if !fileExists(account.ProbeJSON) {
				writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "account probe_json not found; cannot activate"})
				return
			}
			cfg.ActiveAccount = account.Email
			cfg.ProbeJSON = account.ProbeJSON
		}
		if err := a.State.SaveAndApply(cfg); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
			return
		}
		a.invalidateDispatchProbeCache()
		writeJSON(w, http.StatusOK, a.buildAccountsPayload())
	case http.MethodPut:
		payload, err := a.decodeBody(w, r)
		if err != nil {
			writeInvalidBodyError(w, err)
			return
		}
		email := accountEmailFromPayload(payload)
		if email == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "email is required"})
			return
		}
		cfg, _, _ := a.State.Snapshot()
		existing, index, ok := cfg.FindAccount(email)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": "account not found"})
			return
		}
		next, makeActive, err := mergeEditableAccountFields(existing, payload)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
			return
		}
		if next.Disabled && makeActive {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "disabled account cannot be activated"})
			return
		}
		cfg.Accounts[index] = ensureAccountPaths(cfg, next)
		if canonicalEmailKey(cfg.ActiveAccount) == getAccountEmailKey(next) && next.Disabled {
			cfg.ActiveAccount = ""
			cfg.ProbeJSON = ""
		}
		if makeActive {
			if !fileExists(next.ProbeJSON) {
				writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "account probe_json not found; cannot activate"})
				return
			}
			cfg.ActiveAccount = next.Email
			cfg.ProbeJSON = next.ProbeJSON
		}
		if err := a.State.SaveAndApply(cfg); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
			return
		}
		a.invalidateDispatchProbeCache()
		writeJSON(w, http.StatusOK, a.buildAccountsPayload())
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
	}
}

func (a *App) handleAdminAccountDelete(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthOK(w, r) {
		return
	}
	if r.Method != http.MethodDelete {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	raw := strings.TrimPrefix(r.URL.Path, "/admin/accounts/")
	if decoded, err := url.PathUnescape(raw); err == nil {
		raw = decoded
	}
	email := strings.TrimSpace(raw)
	if email == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "email is required"})
		return
	}
	cfg, _, _ := a.State.Snapshot()
	if !cfg.DeleteAccount(email) {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "account not found"})
		return
	}
	if err := a.State.SaveAndApply(cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	a.invalidateDispatchProbeCache()
	writeJSON(w, http.StatusOK, a.buildAccountsPayload())
}

func (a *App) handleAdminAccountsActivate(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthOK(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	payload, err := a.decodeBody(w, r)
	if err != nil {
		writeInvalidBodyError(w, err)
		return
	}
	email := strings.TrimSpace(stringValue(payload["email"]))
	if email == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "email is required"})
		return
	}
	cfg, _, _ := a.State.Snapshot()
	account, _, ok := cfg.FindAccount(email)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "account not found"})
		return
	}
	account = ensureAccountPaths(cfg, account)
	if !fileExists(account.ProbeJSON) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "probe_json not found for account"})
		return
	}
	cfg.ActiveAccount = account.Email
	cfg.ProbeJSON = account.ProbeJSON
	if err := a.State.SaveAndApply(cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	a.invalidateDispatchProbeCache()
	writeJSON(w, http.StatusOK, a.buildAccountsPayload())
}

func (a *App) handleAdminAccountsTest(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthOK(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	payload, err := a.decodeBody(w, r)
	if err != nil {
		writeInvalidBodyError(w, err)
		return
	}
	cfg, _, registry := a.State.Snapshot()
	prompt := strings.TrimSpace(stringValue(payload["prompt"]))
	if prompt == "" {
		prompt = "Reply with NOTION2API_ACCOUNT_OK only."
	}
	attachments, err := extractAttachmentsFromAny(payload["attachments"])
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	entry, err := registry.Resolve(requestedModel(payload, cfg.DefaultPublicModel()), cfg.DefaultPublicModel())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}

	email := strings.TrimSpace(stringValue(payload["email"]))
	probePath, userName, spaceName, activeEmail := cfg.ResolveSessionTarget()
	if email != "" {
		account, _, ok := cfg.FindAccount(email)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": "account not found"})
			return
		}
		account = ensureAccountPaths(cfg, account)
		probePath = account.ProbeJSON
		userName = firstNonEmpty(account.UserName, cfg.UserName)
		spaceName = firstNonEmpty(account.SpaceName, cfg.SpaceName)
		activeEmail = account.Email
	}
	if strings.TrimSpace(probePath) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "no probe_json configured for target account"})
		return
	}
	session, err := loadSessionInfo(probePath, userName, spaceName)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), adminSyncRequestTimeout(cfg))
	defer cancel()
	request := PromptRunRequest{
		Prompt:                            prompt,
		LatestUserPrompt:                  prompt,
		PublicModel:                       entry.ID,
		NotionModel:                       entry.NotionModel,
		UseWebSearch:                      requestedWebSearch(payload, cfg.Features.UseWebSearch),
		Attachments:                       attachments,
		SuppressUpstreamThreadPersistence: true,
	}
	conversationID := a.beginConversation("", "admin_account_test", "account_test", prompt, request)
	result, err := a.runPromptWithSession(ctx, cfg, session, activeEmail, request, nil)
	if err != nil {
		a.failConversation(conversationID, err)
		writeAdminUpstreamError(w, err, map[string]any{"account": activeEmail})
		return
	}
	result.AccountEmail = activeEmail
	a.completeConversation(conversationID, result)
	writeJSON(w, http.StatusOK, map[string]any{
		"success":         true,
		"account":         activeEmail,
		"conversation_id": conversationID,
		"result":          buildChatCompletion(result, entry.ID, true),
		"text":            sanitizeAssistantVisibleText(result.Text),
	})
}

func mergeAccountWithStatus(cfg AppConfig, account NotionAccount, status LoginStatusFile) NotionAccount {
	account = ensureAccountPaths(cfg, account)
	account.Email = firstNonEmpty(status.Email, account.Email)
	account.ProfileDir = firstNonEmpty(status.ProfileDir, account.ProfileDir)
	account.PendingStatePath = firstNonEmpty(status.PendingStatePath, account.PendingStatePath)
	account.StorageStatePath = firstNonEmpty(status.StorageStatePath, account.StorageStatePath)
	account.ProbeJSON = firstNonEmpty(status.ProbePath, account.ProbeJSON)
	account.UserID = firstNonEmpty(status.UserID, account.UserID)
	account.UserName = firstNonEmpty(status.UserName, account.UserName)
	account.SpaceID = firstNonEmpty(status.SpaceID, account.SpaceID)
	account.SpaceViewID = firstNonEmpty(status.SpaceViewID, account.SpaceViewID)
	account.SpaceName = firstNonEmpty(status.SpaceName, account.SpaceName)
	account.ClientVersion = firstNonEmpty(status.ClientVersion, account.ClientVersion)
	account.Status = firstNonEmpty(status.Status, account.Status)
	account.LastError = firstNonEmpty(status.Error, status.Message, account.LastError)
	account.LastLoginAt = firstNonEmpty(status.LastLoginAt, account.LastLoginAt)
	return account
}

func parseCookieHeader(header string) []ProbeCookie {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil
	}
	parts := strings.Split(header, ";")
	out := make([]ProbeCookie, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, value, ok := strings.Cut(part, "=")
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if !ok || name == "" {
			continue
		}
		out = append(out, ProbeCookie{Name: name, Value: value})
	}
	return out
}

func normalizeProbeCookies(cookies []ProbeCookie) []ProbeCookie {
	if len(cookies) == 0 {
		return nil
	}
	out := make([]ProbeCookie, 0, len(cookies))
	seen := map[string]struct{}{}
	for _, item := range cookies {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, ProbeCookie{
			Name:  name,
			Value: strings.TrimSpace(item.Value),
		})
	}
	return out
}

func decodeManualImportRequest(payload map[string]any) (manualAccountImportRequest, error) {
	var req manualAccountImportRequest
	raw, err := json.Marshal(payload)
	if err != nil {
		return req, err
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return req, err
	}
	req.Email = strings.TrimSpace(req.Email)
	req.UserID = strings.TrimSpace(req.UserID)
	req.UserName = strings.TrimSpace(req.UserName)
	req.SpaceID = strings.TrimSpace(req.SpaceID)
	req.SpaceViewID = strings.TrimSpace(req.SpaceViewID)
	req.SpaceName = strings.TrimSpace(req.SpaceName)
	req.ClientVersion = strings.TrimSpace(req.ClientVersion)
	req.CookieHeader = strings.TrimSpace(req.CookieHeader)
	req.ProbeJSONText = strings.TrimSpace(req.ProbeJSONText)
	probe, err := parseManualImportProbeJSON(req.ProbeJSONText)
	if err != nil {
		return req, err
	}
	req.Email = firstNonEmpty(req.Email, probe.Email)
	req.UserID = firstNonEmpty(req.UserID, probe.UserID)
	req.UserName = firstNonEmpty(req.UserName, probe.UserName)
	req.SpaceID = firstNonEmpty(req.SpaceID, probe.SpaceID)
	req.SpaceViewID = firstNonEmpty(req.SpaceViewID, probe.SpaceViewID)
	req.SpaceName = firstNonEmpty(req.SpaceName, probe.SpaceName)
	req.ClientVersion = firstNonEmpty(req.ClientVersion, probe.ClientVersion)
	if req.Email == "" && req.CookieHeader == "" && len(probe.Cookies) == 0 {
		return req, fmt.Errorf("email is required; fill it manually or paste a complete probe_json_text")
	}
	return req, nil
}

func buildImportedSession(ctx context.Context, cfg AppConfig, req manualAccountImportRequest) (probePayload, loginStorageState, LoginStatusFile, discoveredAccountMetadata, error) {
	probe, err := parseManualImportProbeJSON(req.ProbeJSONText)
	if err != nil {
		return probePayload{}, loginStorageState{}, LoginStatusFile{}, discoveredAccountMetadata{}, err
	}
	userName := req.UserName
	spaceName := req.SpaceName

	probe.Email = firstNonEmpty(req.Email, probe.Email)
	probe.UserID = firstNonEmpty(req.UserID, probe.UserID)
	probe.UserName = firstNonEmpty(req.UserName, probe.UserName)
	probe.ClientVersion = firstNonEmpty(req.ClientVersion, probe.ClientVersion)
	probe.SpaceID = firstNonEmpty(req.SpaceID, probe.SpaceID)
	probe.SpaceViewID = firstNonEmpty(req.SpaceViewID, probe.SpaceViewID)
	probe.SpaceName = firstNonEmpty(req.SpaceName, probe.SpaceName)
	if req.CookieHeader != "" {
		probe.Cookies = parseCookieHeader(req.CookieHeader)
	}
	probe.Cookies = normalizeProbeCookies(probe.Cookies)
	if len(probe.Cookies) == 0 {
		return probePayload{}, loginStorageState{}, LoginStatusFile{}, discoveredAccountMetadata{}, fmt.Errorf("cookies are required; paste cookie_header or probe_json_text")
	}

	needsDiscovery := probe.Email == "" || probe.UserID == "" || probe.SpaceID == "" || probe.SpaceViewID == "" || probe.ClientVersion == "" || userName == "" || spaceName == ""
	shouldTryDiscovery := needsDiscovery || (req.CookieHeader != "" && req.ProbeJSONText == "")
	var discovered discoveredAccountMetadata
	var discoverErr error
	if shouldTryDiscovery {
		discovered, discoverErr = discoverImportedAccountMetadata(ctx, cfg, probe.Email, probe.Cookies, discoveredAccountMetadata{
			Email:         probe.Email,
			UserID:        probe.UserID,
			UserName:      userName,
			SpaceID:       probe.SpaceID,
			SpaceViewID:   probe.SpaceViewID,
			SpaceName:     spaceName,
			ClientVersion: probe.ClientVersion,
		})
		probe.Email = firstNonEmpty(probe.Email, discovered.Email)
		probe.UserID = firstNonEmpty(probe.UserID, discovered.UserID)
		probe.SpaceID = firstNonEmpty(probe.SpaceID, discovered.SpaceID)
		probe.SpaceViewID = firstNonEmpty(probe.SpaceViewID, discovered.SpaceViewID)
		probe.ClientVersion = firstNonEmpty(probe.ClientVersion, discovered.ClientVersion)
		userName = firstNonEmpty(userName, discovered.UserName)
		spaceName = firstNonEmpty(spaceName, discovered.SpaceName)
	}
	if probe.Email == "" || probe.UserID == "" || probe.SpaceID == "" || probe.ClientVersion == "" {
		if discoverErr != nil {
			return probePayload{}, loginStorageState{}, LoginStatusFile{}, discoveredAccountMetadata{}, fmt.Errorf("email, user_id, space_id and client_version are required; auto-discovery failed: %w", discoverErr)
		}
		return probePayload{}, loginStorageState{}, LoginStatusFile{}, discoveredAccountMetadata{}, fmt.Errorf("email, user_id, space_id and client_version are required")
	}
	localPart := probe.Email
	if idx := strings.Index(localPart, "@"); idx > 0 {
		localPart = localPart[:idx]
	}
	userName = firstNonEmpty(userName, localPart, accountPathSlug(probe.Email))
	if userName == "" {
		userName = accountPathSlug(probe.Email)
	}
	spaceName = firstNonEmpty(spaceName, userName+"'s Space")

	storage := loginStorageState{
		Email:         probe.Email,
		ClientVersion: probe.ClientVersion,
		Cookies:       probe.Cookies,
	}
	status := LoginStatusFile{
		Success:       true,
		Status:        "ready",
		Email:         probe.Email,
		UserID:        probe.UserID,
		UserName:      userName,
		SpaceID:       probe.SpaceID,
		SpaceViewID:   probe.SpaceViewID,
		SpaceName:     spaceName,
		ClientVersion: probe.ClientVersion,
		Title:         "Notion",
		Message:       "manual session imported",
		LastLoginAt:   time.Now().Format(time.RFC3339),
	}
	return probe, storage, status, discovered, nil
}

func (a *App) handleAdminAccountManualImport(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthOK(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	payload, err := a.decodeBody(w, r)
	if err != nil {
		writeInvalidBodyError(w, err)
		return
	}
	req, err := decodeManualImportRequest(payload)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	cfg, _, _ := a.State.Snapshot()
	probe, storage, status, discovered, err := buildImportedSession(r.Context(), cfg, req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	accountEmail := strings.TrimSpace(probe.Email)
	account, _, ok := cfg.FindAccount(accountEmail)
	if !ok {
		account = NotionAccount{Email: accountEmail}
	}
	account = ensureAccountPaths(cfg, account)
	if err := ensureParentDir(account.ProbeJSON); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if err := ensureParentDir(account.StorageStatePath); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if err := ensureParentDir(account.PendingStatePath); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if err := os.MkdirAll(account.ProfileDir, 0o755); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if err := writePrettyJSONFile(account.ProbeJSON, probe); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if err := writeLoginStorageState(account.StorageStatePath, storage); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	status.ProfileDir = account.ProfileDir
	status.PendingStatePath = account.PendingStatePath
	status.StorageStatePath = account.StorageStatePath
	status.ProbePath = account.ProbeJSON
	if err := writeLoginPendingState(account.PendingStatePath, loginPendingState{LoginStatusFile: status}); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}

	account = mergeAccountWithStatus(cfg, account, status)
	account.Status = "ready"
	account.LastError = ""
	account.LastLoginAt = status.LastLoginAt
	account.PlanType = firstNonEmpty(account.PlanType, discovered.PlanType)
	account.UserName = firstNonEmpty(account.UserName, discovered.UserName)
	account.SpaceName = firstNonEmpty(account.SpaceName, discovered.SpaceName)
	if len(discovered.Models) > 0 {
		cfg.Models = mergeModelDefinitions(discovered.Models, cfg.Models)
	}
	cfg.UpsertAccount(account)
	if req.Active {
		cfg.ActiveAccount = account.Email
		cfg.ProbeJSON = account.ProbeJSON
	}
	if err := a.State.SaveAndApply(cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	a.invalidateDispatchProbeCache()
	cfg, _, _ = a.State.Snapshot()
	account, _, _ = cfg.FindAccount(accountEmail)
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"account": a.accountRuntimeSummary(cfg, account),
		"status":  status,
	})
}

func (a *App) handleAdminAccountLoginStart(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthOK(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	payload, err := a.decodeBody(w, r)
	if err != nil {
		writeInvalidBodyError(w, err)
		return
	}
	email := strings.TrimSpace(stringValue(payload["email"]))
	if email == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "email is required"})
		return
	}

	cfg, _, _ := a.State.Snapshot()
	account, _, ok := cfg.FindAccount(email)
	if !ok {
		account = NotionAccount{Email: email, Status: "new"}
	}
	account = ensureAccountPaths(cfg, account)
	account.Status = "starting"
	account.LastError = ""
	account, _ = cfg.UpsertAccount(account)
	if err := a.State.SaveAndApply(cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}

	status, err := StartEmailLogin(r.Context(), cfg, LoginStartRequest{
		Email:            email,
		ProfileDir:       account.ProfileDir,
		PendingPath:      account.PendingStatePath,
		StorageStatePath: account.StorageStatePath,
		AccountEmail:     account.Email,
	})

	cfg, _, _ = a.State.Snapshot()
	account, _, _ = cfg.FindAccount(email)
	account = ensureAccountPaths(cfg, account)
	if err != nil {
		account.Status = "failed"
		account.LastError = firstNonEmpty(status.Error, status.Message, err.Error())
		cfg.UpsertAccount(account)
		_ = a.State.SaveAndApply(cfg)
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": account.LastError, "account": account.Email})
		return
	}
	account = mergeAccountWithStatus(cfg, account, status)
	account.Status = firstNonEmpty(account.Status, "pending_code")
	account.LastError = ""
	cfg.UpsertAccount(account)
	if err := a.State.SaveAndApply(cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	a.invalidateDispatchProbeCache()
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"account": a.accountRuntimeSummary(cfg, account),
		"status":  status,
	})
}

func (a *App) handleAdminAccountLoginVerify(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthOK(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	payload, err := a.decodeBody(w, r)
	if err != nil {
		writeInvalidBodyError(w, err)
		return
	}
	email := strings.TrimSpace(stringValue(payload["email"]))
	code := strings.TrimSpace(stringValue(payload["code"]))
	if email == "" || code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "email and code are required"})
		return
	}

	cfg, _, _ := a.State.Snapshot()
	account, _, ok := cfg.FindAccount(email)
	if !ok {
		account = NotionAccount{Email: email}
	}
	account = ensureAccountPaths(cfg, account)

	status, err := VerifyEmailLogin(r.Context(), cfg, LoginVerifyRequest{
		Email:            email,
		Code:             code,
		ProfileDir:       account.ProfileDir,
		PendingPath:      account.PendingStatePath,
		StorageStatePath: account.StorageStatePath,
		ProbePath:        account.ProbeJSON,
		AccountEmail:     account.Email,
	})

	cfg, _, _ = a.State.Snapshot()
	account, _, _ = cfg.FindAccount(email)
	if account.Email == "" {
		account = NotionAccount{Email: email}
	}
	account = ensureAccountPaths(cfg, account)
	if err != nil {
		account.Status = "failed"
		account.LastError = firstNonEmpty(status.Error, status.Message, err.Error())
		cfg.UpsertAccount(account)
		_ = a.State.SaveAndApply(cfg)
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": account.LastError, "account": account.Email})
		return
	}

	account = mergeAccountWithStatus(cfg, account, status)
	if status.Success && fileExists(account.ProbeJSON) {
		account.Status = "ready"
		account.LastError = ""
		if account.LastLoginAt == "" {
			account.LastLoginAt = time.Now().Format(time.RFC3339)
		}
		cfg.ActiveAccount = account.Email
		cfg.ProbeJSON = account.ProbeJSON
	}
	cfg.UpsertAccount(account)
	if err := a.State.SaveAndApply(cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	a.invalidateDispatchProbeCache()
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"account": a.accountRuntimeSummary(cfg, account),
		"status":  status,
	})
}

func (a *App) handleAdminAccountLoginStatus(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthOK(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	email := strings.TrimSpace(r.URL.Query().Get("email"))
	cfg, _, _ := a.State.Snapshot()
	if email == "" {
		writeJSON(w, http.StatusOK, a.buildAccountsPayload())
		return
	}
	account, _, ok := cfg.FindAccount(email)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "account not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"item":    a.accountRuntimeSummary(cfg, account),
	})
}
