package app

import (
	"net/http"
	"strings"
	"time"
)

// diagnosticCheck is one row in the test-area report. Skipped checks carry a
// reason so the console never shows a silent gap.
type diagnosticCheck struct {
	Name       string         `json:"name"`
	Label      string         `json:"label"`
	Status     string         `json:"status"`
	DurationMS int64          `json:"duration_ms"`
	Detail     string         `json:"detail,omitempty"`
	Data       map[string]any `json:"data,omitempty"`
}

const (
	diagnosticStatusPass    = "pass"
	diagnosticStatusFail    = "fail"
	diagnosticStatusSkipped = "skipped"
	diagnosticStatusWarn    = "warn"
)

// diagnosticProbeTool is the tool definition offered to the model during a tool
// availability check. It is deliberately trivial and read-only: the goal is to
// prove the tool protocol round-trips, not to exercise a real capability.
func diagnosticProbeTool() ToolDefinition {
	return ToolDefinition{
		Type:        "function",
		Name:        "notion2api_diagnostic_echo",
		Description: "Echo a short diagnostic token back to the caller. Call this tool to confirm tool routing works.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"token": map[string]any{
					"type":        "string",
					"description": "The exact diagnostic token supplied in the request.",
				},
			},
			"required":             []any{"token"},
			"additionalProperties": false,
		},
		Source: toolSourceClient,
	}
}

func diagnosticCheckResult(name string, label string, startedAt time.Time) diagnosticCheck {
	return diagnosticCheck{
		Name:       name,
		Label:      label,
		Status:     diagnosticStatusPass,
		DurationMS: time.Since(startedAt).Milliseconds(),
	}
}

// runChatDiagnostic verifies the plain text path end to end: account dispatch,
// upstream inference, and visible-text sanitization.
func (a *App) runChatDiagnostic(r *http.Request, cfg AppConfig, entry ModelDefinition, accountEmail string) diagnosticCheck {
	startedAt := time.Now()
	const probe = "Reply with exactly: NOTION2API_CHAT_OK"
	request := PromptRunRequest{
		Prompt:                            probe,
		LatestUserPrompt:                  probe,
		PublicModel:                       entry.ID,
		NotionModel:                       entry.NotionModel,
		PinnedAccountEmail:                accountEmail,
		AllowPinnedAccountFallback:        accountEmail == "",
		SuppressUpstreamThreadPersistence: true,
		SuppressReasoningOutput:           true,
	}
	timed, cancel := cloneRequestWithTimeout(r, adminSyncRequestTimeout(cfg))
	defer cancel()
	result, err := a.runPrompt(timed, request)
	check := diagnosticCheckResult("chat", "对话可用性", startedAt)
	if err != nil {
		check.Status = diagnosticStatusFail
		check.Detail = err.Error()
		return check
	}
	text := sanitizeAssistantVisibleText(result.Text)
	check.Data = map[string]any{
		"account": result.AccountEmail,
		"model":   entry.ID,
		"text":    text,
	}
	if strings.TrimSpace(text) == "" {
		check.Status = diagnosticStatusFail
		check.Detail = "upstream returned an empty answer"
		return check
	}
	if !strings.Contains(strings.ToUpper(text), "NOTION2API_CHAT_OK") {
		// The model answered but ignored the instruction. Chat works; only the
		// instruction-following is imperfect, which is not a transport failure.
		check.Status = diagnosticStatusWarn
		check.Detail = "model answered but did not echo the probe token"
	}
	return check
}

// runToolCallDiagnostic verifies that the model can be steered into emitting a
// structured tool call and that the gateway extracts it. It uses the configured
// planning mode so the result reflects real request behavior.
func (a *App) runToolCallDiagnostic(r *http.Request, cfg AppConfig, entry ModelDefinition, accountEmail string) diagnosticCheck {
	startedAt := time.Now()
	check := diagnosticCheckResult("tool_call", "工具调用可用性", startedAt)
	if !cfg.Tools.Enabled {
		check.Status = diagnosticStatusSkipped
		check.Detail = "tools.enabled is false"
		return check
	}
	const token = "N2A-TOOL-PROBE"
	prompt := "Use the notion2api_diagnostic_echo tool to echo the token " + token + "."
	tool := diagnosticProbeTool()
	turn := toolTurnContext{
		Enabled:      true,
		Tools:        []ToolDefinition{tool},
		Choice:       "required",
		PlanningMode: normalizeToolPlanningMode(cfg.Tools.PlanningMode),
		Settings:     cfg.Tools,
	}
	request := PromptRunRequest{
		Prompt:                            prompt,
		LatestUserPrompt:                  prompt,
		PublicModel:                       entry.ID,
		NotionModel:                       entry.NotionModel,
		PinnedAccountEmail:                accountEmail,
		AllowPinnedAccountFallback:        accountEmail == "",
		SuppressUpstreamThreadPersistence: true,
		SuppressReasoningOutput:           true,
	}
	request = turn.attachToRequest(request)
	timed, cancel := cloneRequestWithTimeout(r, adminSyncRequestTimeout(cfg))
	defer cancel()

	var calls []DetectedToolCall
	if turn.PlanningMode == toolPlanningModeRouter {
		routed, err := a.runToolRouterRound(timed, request, turn)
		if err != nil {
			check.Status = diagnosticStatusFail
			check.Detail = err.Error()
			check.DurationMS = time.Since(startedAt).Milliseconds()
			return check
		}
		calls = routed
	} else {
		result, err := a.runPrompt(timed, turn.applyNativeToolPrompt(request))
		if err != nil {
			check.Status = diagnosticStatusFail
			check.Detail = err.Error()
			check.DurationMS = time.Since(startedAt).Milliseconds()
			return check
		}
		calls = turn.finalizeToolCalls(extractToolCallsFromText(result.Text, turn.Tools, turn.Choice), "diagnostic")
		check.Data = map[string]any{"raw_text": sanitizeAssistantVisibleText(result.Text)}
	}
	check.DurationMS = time.Since(startedAt).Milliseconds()
	if len(calls) == 0 {
		check.Status = diagnosticStatusFail
		check.Detail = "model did not produce a valid tool call"
		return check
	}
	if check.Data == nil {
		check.Data = map[string]any{}
	}
	check.Data["planning_mode"] = turn.PlanningMode
	check.Data["tool_calls"] = toolCallMaps(calls)
	if !strings.Contains(string(calls[0].Arguments), token) {
		check.Status = diagnosticStatusWarn
		check.Detail = "tool call produced but arguments did not carry the probe token"
	}
	return check
}

// runMCPDiagnostic checks every running MCP server by listing its tools. It does
// not invoke tools, since an arbitrary MCP tool may have side effects.
func (a *App) runMCPDiagnostic(cfg AppConfig) diagnosticCheck {
	startedAt := time.Now()
	check := diagnosticCheckResult("mcp", "MCP 服务器", startedAt)
	if len(cfg.MCPServers) == 0 {
		check.Status = diagnosticStatusSkipped
		check.Detail = "no mcp servers configured"
		return check
	}
	servers := a.State.MCP.Status()
	tools := a.State.MCP.Tools()
	down := make([]string, 0, len(servers))
	for _, server := range servers {
		if alive, _ := server["alive"].(bool); !alive {
			down = append(down, stringValue(server["name"]))
		}
	}
	check.DurationMS = time.Since(startedAt).Milliseconds()
	check.Data = map[string]any{
		"servers":    servers,
		"tool_count": len(tools),
	}
	if len(down) > 0 {
		check.Status = diagnosticStatusFail
		check.Detail = "server(s) not running: " + strings.Join(down, ", ")
	}
	return check
}

// runAccountPoolDiagnostic reports dispatch readiness without touching upstream.
func (a *App) runAccountPoolDiagnostic(cfg AppConfig) diagnosticCheck {
	startedAt := time.Now()
	check := diagnosticCheckResult("account_pool", "账号池调度", startedAt)
	now := time.Now()
	eligible := make([]string, 0, len(cfg.Accounts))
	blocked := make([]map[string]any, 0, len(cfg.Accounts))
	for _, account := range cfg.Accounts {
		account = ensureAccountPaths(cfg, account)
		if ok, reason := accountDispatchEligible(cfg, account, now); ok {
			eligible = append(eligible, account.Email)
		} else {
			blocked = append(blocked, map[string]any{"email": account.Email, "reason": reason})
		}
	}
	check.DurationMS = time.Since(startedAt).Milliseconds()
	check.Data = map[string]any{
		"strategy":       normalizeDispatchStrategy(cfg.Dispatch.Strategy),
		"total":          len(cfg.Accounts),
		"eligible":       eligible,
		"blocked":        blocked,
		"free_capacity":  a.State.AvailableDispatchCapacity(eligible),
		"active_account": cfg.ActiveAccount,
	}
	if len(eligible) == 0 {
		check.Status = diagnosticStatusFail
		check.Detail = "no account is currently eligible for dispatch"
	}
	return check
}

// handleAdminDiagnostics runs the selected checks and reports each one
// independently, so a tool failure does not hide a working chat path.
func (a *App) handleAdminDiagnostics(w http.ResponseWriter, r *http.Request) {
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
	entry, err := registry.Resolve(requestedModel(payload, cfg.DefaultPublicModel()), cfg.DefaultPublicModel())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	accountEmail := requestedAccountEmail(r, payload)

	selected := map[string]bool{}
	for _, raw := range sliceValue(payload["checks"]) {
		if name := strings.TrimSpace(stringValue(raw)); name != "" {
			selected[strings.ToLower(name)] = true
		}
	}
	wants := func(name string) bool { return len(selected) == 0 || selected[name] }

	checks := make([]diagnosticCheck, 0, 4)
	if wants("account_pool") {
		checks = append(checks, a.runAccountPoolDiagnostic(cfg))
	}
	if wants("mcp") {
		checks = append(checks, a.runMCPDiagnostic(cfg))
	}
	if wants("chat") {
		checks = append(checks, a.runChatDiagnostic(r, cfg, entry, accountEmail))
	}
	if wants("tool_call") {
		checks = append(checks, a.runToolCallDiagnostic(r, cfg, entry, accountEmail))
	}

	failed := 0
	warned := 0
	for _, check := range checks {
		switch check.Status {
		case diagnosticStatusFail:
			failed++
		case diagnosticStatusWarn:
			warned++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": failed == 0,
		"model":   entry.ID,
		"account": accountEmail,
		"summary": map[string]any{
			"total":  len(checks),
			"failed": failed,
			"warned": warned,
			"passed": len(checks) - failed - warned,
		},
		"checks": checks,
	})
}
