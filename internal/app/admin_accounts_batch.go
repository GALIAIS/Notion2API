package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
)

const adminAccountsBatchLimit = 50

const adminAccountsBatchUsageDetail = "unsupported batch payload; provide one of: emails (string or array of addresses), items (array of manual import objects), text (segments separated by --- lines or JSONL, each a probe_json object or a cookie header)"

const (
	adminBatchActionLoginStarted = "login_started"
	adminBatchActionImported     = "imported"
	adminBatchActionActivated    = "activated"
)

type adminBatchItemResult struct {
	Email  string `json:"email"`
	OK     bool   `json:"ok"`
	Action string `json:"action"`
	Detail string `json:"detail"`
}

// adminBatchTask carries a per-entry decode failure instead of returning it, so a single
// malformed line cannot abort the whole batch.
type adminBatchTask struct {
	action    string
	email     string
	request   manualAccountImportRequest
	decodeErr error
}

func splitBatchEmailList(raw any) []string {
	out := []string{}
	appendParts := func(text string) {
		for _, part := range strings.FieldsFunc(text, func(r rune) bool {
			switch r {
			case ',', ';', '\n', '\r', '\t', ' ':
				return true
			}
			return false
		}) {
			part = strings.TrimSpace(part)
			if part != "" {
				out = append(out, part)
			}
		}
	}
	if text, ok := raw.(string); ok {
		appendParts(text)
		return out
	}
	for _, item := range sliceValue(raw) {
		appendParts(stringValue(item))
	}
	return out
}

func batchEmailLooksValid(email string) bool {
	at := strings.Index(email, "@")
	if at <= 0 || at == len(email)-1 {
		return false
	}
	if strings.ContainsAny(email, " \t\"") {
		return false
	}
	return strings.Contains(email[at+1:], ".")
}

func isBatchSeparatorLine(line string) bool {
	clean := strings.TrimSpace(line)
	if len(clean) < 3 {
		return false
	}
	return strings.Trim(clean, "-") == "" || strings.Trim(clean, "=") == ""
}

// batchJSONObject reuses the request decoder so numbers keep their literal form,
// matching what decodeManualImportRequest expects downstream. The json.Valid guard is
// required because the streaming decoder would accept a JSONL chunk as its first object
// and silently drop the rest.
func batchJSONObject(raw string) map[string]any {
	clean := strings.TrimSpace(raw)
	if !strings.HasPrefix(clean, "{") || !json.Valid([]byte(clean)) {
		return nil
	}
	object, err := decodeBodyMapFromRaw([]byte(clean))
	if err != nil {
		return nil
	}
	return object
}

func expandBatchTextChunk(chunk string) []string {
	if batchJSONObject(chunk) != nil {
		return []string{strings.TrimSpace(chunk)}
	}
	out := []string{}
	for _, line := range strings.Split(chunk, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

func splitBatchTextSegments(text string) []string {
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	chunks := []string{}
	current := []string{}
	flush := func() {
		joined := strings.TrimSpace(strings.Join(current, "\n"))
		current = nil
		if joined != "" {
			chunks = append(chunks, joined)
		}
	}
	for _, line := range strings.Split(normalized, "\n") {
		if isBatchSeparatorLine(line) {
			flush()
			continue
		}
		current = append(current, line)
	}
	flush()
	segments := []string{}
	for _, chunk := range chunks {
		segments = append(segments, expandBatchTextChunk(chunk)...)
	}
	return segments
}

func batchLooksLikeProbeObject(object map[string]any) bool {
	for _, key := range []string{"email", "user_id", "space_id", "space_view_id", "client_version", "cookies"} {
		if _, ok := object[key]; ok {
			return true
		}
	}
	return false
}

// A cookie dump without one of these names cannot authenticate, so requiring a hint
// keeps arbitrary "key=value" prose from being misread as a session.
var batchCookieHintNames = map[string]struct{}{
	"token_v2":          {},
	"notion_user_id":    {},
	"notion_users":      {},
	"notion_browser_id": {},
}

func batchLooksLikeCookieHeader(segment string) bool {
	for _, cookie := range parseCookieHeader(segment) {
		if _, ok := batchCookieHintNames[strings.ToLower(strings.TrimSpace(cookie.Name))]; ok {
			return true
		}
	}
	return false
}

func batchSegmentToImportRequest(segment string) (manualAccountImportRequest, error) {
	if object := batchJSONObject(segment); object != nil {
		if _, ok := object["cookie_header"]; ok {
			return decodeManualImportRequest(object)
		}
		if _, ok := object["probe_json_text"]; ok {
			return decodeManualImportRequest(object)
		}
		if batchLooksLikeProbeObject(object) {
			return decodeManualImportRequest(map[string]any{"probe_json_text": segment})
		}
		return manualAccountImportRequest{}, fmt.Errorf("json segment has no email/user_id/space_id/client_version/cookies field")
	}
	if batchLooksLikeCookieHeader(segment) {
		return decodeManualImportRequest(map[string]any{"cookie_header": segment})
	}
	return manualAccountImportRequest{}, fmt.Errorf("segment matched neither probe_json nor cookie_header")
}

func buildAdminBatchTasks(payload map[string]any) ([]adminBatchTask, error) {
	tasks := []adminBatchTask{}
	seenLogin := map[string]struct{}{}
	for _, email := range splitBatchEmailList(payload["emails"]) {
		task := adminBatchTask{action: adminBatchActionLoginStarted, email: email}
		key := canonicalEmailKey(email)
		if !batchEmailLooksValid(email) {
			task.decodeErr = fmt.Errorf("invalid email address")
		} else if _, ok := seenLogin[key]; ok {
			task.decodeErr = fmt.Errorf("duplicate email in batch")
		} else {
			seenLogin[key] = struct{}{}
		}
		tasks = append(tasks, task)
	}
	for index, raw := range sliceValue(payload["items"]) {
		task := adminBatchTask{action: adminBatchActionImported}
		object := mapValue(raw)
		if object == nil {
			task.decodeErr = fmt.Errorf("items[%d] must be an object", index)
			tasks = append(tasks, task)
			continue
		}
		task.email = strings.TrimSpace(stringValue(object["email"]))
		request, err := decodeManualImportRequest(object)
		if err != nil {
			task.decodeErr = err
		} else {
			task.request = request
			task.email = firstNonEmpty(request.Email, task.email)
		}
		tasks = append(tasks, task)
	}
	for _, segment := range splitBatchTextSegments(stringValue(payload["text"])) {
		task := adminBatchTask{action: adminBatchActionImported}
		request, err := batchSegmentToImportRequest(segment)
		if err != nil {
			task.decodeErr = err
		} else {
			task.request = request
			task.email = request.Email
		}
		tasks = append(tasks, task)
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("%s", adminAccountsBatchUsageDetail)
	}
	return tasks, nil
}

// applyBatchLoginStart mirrors handleAdminAccountLoginStart but defers persistence to the
// caller so the whole batch shares a single SaveAndApply.
func applyBatchLoginStart(ctx context.Context, cfg AppConfig, email string) (AppConfig, error) {
	account, _, ok := cfg.FindAccount(email)
	if !ok {
		account = NotionAccount{Email: email, Status: "new"}
	}
	account = ensureAccountPaths(cfg, account)
	account.Status = "starting"
	account.LastError = ""
	account, _ = cfg.UpsertAccount(account)

	status, err := StartEmailLogin(ctx, cfg, LoginStartRequest{
		Email:            email,
		ProfileDir:       account.ProfileDir,
		PendingPath:      account.PendingStatePath,
		StorageStatePath: account.StorageStatePath,
		AccountEmail:     account.Email,
	})
	if err != nil {
		account.Status = "failed"
		account.LastError = firstNonEmpty(status.Error, status.Message, err.Error())
		cfg.UpsertAccount(account)
		return cfg, fmt.Errorf("%s", account.LastError)
	}
	account = mergeAccountWithStatus(cfg, account, status)
	account.Status = firstNonEmpty(account.Status, "pending_code")
	account.LastError = ""
	cfg.UpsertAccount(account)
	return cfg, nil
}

// applyBatchAccountImport mirrors handleAdminAccountManualImport. It takes cfg by value and
// returns the updated copy so each entry sees the accumulated state, which matters because
// account paths resolve against cfg.LoginHelper.SessionsDir.
func applyBatchAccountImport(ctx context.Context, cfg AppConfig, req manualAccountImportRequest) (AppConfig, string, error) {
	probe, storage, status, discovered, err := buildImportedSession(ctx, cfg, req)
	if err != nil {
		return cfg, req.Email, err
	}
	accountEmail := strings.TrimSpace(probe.Email)
	account, _, ok := cfg.FindAccount(accountEmail)
	if !ok {
		account = NotionAccount{Email: accountEmail}
	}
	account = ensureAccountPaths(cfg, account)
	for _, path := range []string{account.ProbeJSON, account.StorageStatePath, account.PendingStatePath} {
		if err := ensureParentDir(path); err != nil {
			return cfg, accountEmail, err
		}
	}
	if err := os.MkdirAll(account.ProfileDir, 0o755); err != nil {
		return cfg, accountEmail, err
	}
	if err := writePrettyJSONFile(account.ProbeJSON, probe); err != nil {
		return cfg, accountEmail, err
	}
	if err := writeLoginStorageState(account.StorageStatePath, storage); err != nil {
		return cfg, accountEmail, err
	}
	status.ProfileDir = account.ProfileDir
	status.PendingStatePath = account.PendingStatePath
	status.StorageStatePath = account.StorageStatePath
	status.ProbePath = account.ProbeJSON
	if err := writeLoginPendingState(account.PendingStatePath, loginPendingState{LoginStatusFile: status}); err != nil {
		return cfg, accountEmail, err
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
	return cfg, accountEmail, nil
}

func applyBatchActiveAccount(cfg AppConfig, email string) (AppConfig, error) {
	account, _, ok := cfg.FindAccount(email)
	if !ok {
		return cfg, fmt.Errorf("account not found")
	}
	account = ensureAccountPaths(cfg, account)
	if !fileExists(account.ProbeJSON) {
		return cfg, fmt.Errorf("probe_json not found for account; cannot activate")
	}
	cfg.ActiveAccount = account.Email
	cfg.ProbeJSON = account.ProbeJSON
	return cfg, nil
}

func (a *App) handleAdminAccountsBatch(w http.ResponseWriter, r *http.Request) {
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
	tasks, err := buildAdminBatchTasks(payload)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	if len(tasks) > adminAccountsBatchLimit {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"detail": fmt.Sprintf("batch too large: %d entries exceed the limit of %d", len(tasks), adminAccountsBatchLimit),
		})
		return
	}

	cfg, _, _ := a.State.Snapshot()
	items := make([]adminBatchItemResult, 0, len(tasks)+1)
	succeeded := 0
	record := func(email string, action string, err error) {
		if err != nil {
			items = append(items, adminBatchItemResult{Email: email, OK: false, Action: action, Detail: err.Error()})
			return
		}
		items = append(items, adminBatchItemResult{Email: email, OK: true, Action: action, Detail: ""})
		succeeded++
	}

	for _, task := range tasks {
		if task.decodeErr != nil {
			record(task.email, task.action, task.decodeErr)
			continue
		}
		switch task.action {
		case adminBatchActionLoginStarted:
			next, err := applyBatchLoginStart(r.Context(), cfg, task.email)
			cfg = next
			record(task.email, task.action, err)
		default:
			next, email, err := applyBatchAccountImport(r.Context(), cfg, task.request)
			cfg = next
			record(firstNonEmpty(email, task.email), task.action, err)
		}
	}

	if activeEmail := strings.TrimSpace(stringValue(payload["active"])); activeEmail != "" {
		next, err := applyBatchActiveAccount(cfg, activeEmail)
		cfg = next
		record(activeEmail, adminBatchActionActivated, err)
	}

	if err := a.State.SaveAndApply(cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"success": false,
			"detail":  err.Error(),
			"items":   items,
		})
		return
	}
	a.invalidateDispatchProbeCache()
	writeJSON(w, http.StatusOK, map[string]any{
		"success":   true,
		"processed": len(items),
		"succeeded": succeeded,
		"failed":    len(items) - succeeded,
		"items":     items,
		"accounts":  a.buildAccountsPayload(),
	})
}
