package app

import (
	"net/http"
	"strings"
	"time"
)

// buildMCPPayload reports the configured servers alongside their live runtime
// state, so the console can distinguish "not configured" from "configured but
// down".
func (a *App) buildMCPPayload() map[string]any {
	cfg, _, _ := a.State.Snapshot()
	configured := make([]map[string]any, 0, len(cfg.MCPServers))
	for _, server := range cfg.MCPServers {
		configured = append(configured, map[string]any{
			"name":        server.Name,
			"command":     server.Command,
			"args":        server.Args,
			"enabled":     server.Enabled,
			"timeout_sec": server.TimeoutSec,
			"auto_start":  server.AutoStart,
		})
	}
	tools := a.State.MCP.Tools()
	toolItems := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		toolItems = append(toolItems, map[string]any{
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  tool.Parameters,
			"source":      tool.Source,
		})
	}
	return map[string]any{
		"success":       true,
		"tools_enabled": cfg.Tools.Enabled,
		"planning_mode": cfg.Tools.PlanningMode,
		"configured":    configured,
		"servers":       a.State.MCP.Status(),
		"tools":         toolItems,
	}
}

func (a *App) handleAdminMCP(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthOK(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, a.buildMCPPayload())
}

// handleAdminMCPReload restarts every configured server. This is the recovery
// action for a wedged subprocess and it is intentionally a POST because it kills
// and respawns local processes.
func (a *App) handleAdminMCPReload(w http.ResponseWriter, r *http.Request) {
	if !a.adminAuthOK(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	cfg, _, _ := a.State.Snapshot()
	a.State.MCP.Reload(r.Context(), cfg.MCPServers)
	a.State.MCP.StartSupervisor(r.Context())
	writeJSON(w, http.StatusOK, a.buildMCPPayload())
}

// handleAdminMCPCall invokes one MCP tool directly so an operator can verify a
// server without going through a model round.
func (a *App) handleAdminMCPCall(w http.ResponseWriter, r *http.Request) {
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
	name := strings.TrimSpace(stringValue(payload["name"]))
	if name == "" {
		name = strings.TrimSpace(stringValue(payload["tool"]))
	}
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "name is required"})
		return
	}
	if server := strings.TrimSpace(stringValue(payload["server"])); server != "" && !strings.Contains(name, mcpToolNameSeparator) {
		name = mcpNamespacedToolName(server, name)
	}
	args := mapValue(payload["arguments"])
	if args == nil {
		args = decodeJSONObjectAny(payload["arguments"])
	}
	if args == nil {
		args = map[string]any{}
	}
	if !a.State.MCP.Owns(name) {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"detail": "tool " + name + " is not provided by a running mcp server",
		})
		return
	}
	startedAt := time.Now()
	text, isError, callErr := a.State.MCP.CallTool(r.Context(), name, args)
	elapsed := time.Since(startedAt)
	if callErr != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"success":     false,
			"tool":        name,
			"detail":      callErr.Error(),
			"duration_ms": elapsed.Milliseconds(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":     !isError,
		"tool":        name,
		"arguments":   args,
		"is_error":    isError,
		"result":      text,
		"duration_ms": elapsed.Milliseconds(),
	})
}
