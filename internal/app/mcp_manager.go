package app

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"notion2api/internal/mcp"
)

const (
	mcpToolNameSeparator   = "."
	mcpRestartBackoffBase  = 2 * time.Second
	mcpRestartBackoffMax   = 5 * time.Minute
	mcpSupervisorInterval  = 15 * time.Second
	mcpInitializeTimeout   = 20 * time.Second
	mcpDefaultCallTimeout  = 30 * time.Second
	mcpMaxToolResultLength = 200000
)

// mcpServerRuntime is the live state of one configured MCP server. Every field is
// guarded by MCPManager.mu.
type mcpServerRuntime struct {
	config       MCPServerConfig
	client       *mcp.Client
	tools        []mcp.Tool
	status       string
	lastError    string
	startedAt    time.Time
	lastAttempt  time.Time
	restartCount int
}

// MCPManager owns the MCP subprocesses and the aggregated tool catalog. MCP
// tools are executed server-side, unlike client-declared tools which are handed
// back to the caller.
type MCPManager struct {
	mu      sync.Mutex
	servers map[string]*mcpServerRuntime
	cancel  context.CancelFunc
	closed  bool
}

func newMCPManager() *MCPManager {
	return &MCPManager{servers: map[string]*mcpServerRuntime{}}
}

func mcpNamespacedToolName(server string, tool string) string {
	return strings.TrimSpace(server) + mcpToolNameSeparator + strings.TrimSpace(tool)
}

func splitMCPToolName(name string) (string, string, bool) {
	index := strings.Index(name, mcpToolNameSeparator)
	if index <= 0 || index >= len(name)-1 {
		return "", "", false
	}
	return name[:index], name[index+1:], true
}

func mcpCallTimeout(timeoutSec int) time.Duration {
	if timeoutSec <= 0 {
		return mcpDefaultCallTimeout
	}
	return time.Duration(timeoutSec) * time.Second
}

// Apply reconciles the running subprocesses with the configured server list.
// Servers that disappeared or were disabled are stopped; changed definitions are
// restarted so a config edit takes effect without a process restart.
func (m *MCPManager) Apply(ctx context.Context, servers []MCPServerConfig) {
	if m == nil {
		return
	}
	desired := map[string]MCPServerConfig{}
	for _, server := range servers {
		if !server.Enabled {
			continue
		}
		desired[server.Name] = server
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	var stopped []*mcp.Client
	for name, runtime := range m.servers {
		next, keep := desired[name]
		if keep && mcpServerConfigEqual(runtime.config, next) {
			continue
		}
		if runtime.client != nil {
			stopped = append(stopped, runtime.client)
		}
		delete(m.servers, name)
	}
	toStart := make([]MCPServerConfig, 0, len(desired))
	for name, server := range desired {
		if _, running := m.servers[name]; running {
			continue
		}
		m.servers[name] = &mcpServerRuntime{config: server, status: "starting"}
		toStart = append(toStart, server)
	}
	m.mu.Unlock()

	for _, client := range stopped {
		_ = client.Close()
	}
	for _, server := range toStart {
		m.startServer(ctx, server)
	}
}

// mcpServerListEqual reports whether two configured server lists would produce
// the same set of running subprocesses, so a config save that did not touch
// mcp_servers never restarts a healthy server.
func mcpServerListEqual(left []MCPServerConfig, right []MCPServerConfig) bool {
	if len(left) != len(right) {
		return false
	}
	index := make(map[string]MCPServerConfig, len(right))
	for _, server := range right {
		index[server.Name] = server
	}
	for _, server := range left {
		other, ok := index[server.Name]
		if !ok || server.Enabled != other.Enabled || !mcpServerConfigEqual(server, other) {
			return false
		}
	}
	return true
}

func mcpServerConfigEqual(left MCPServerConfig, right MCPServerConfig) bool {	if left.Name != right.Name || left.Command != right.Command || left.TimeoutSec != right.TimeoutSec || left.AutoStart != right.AutoStart {
		return false
	}
	if len(left.Args) != len(right.Args) || len(left.Env) != len(right.Env) {
		return false
	}
	for i := range left.Args {
		if left.Args[i] != right.Args[i] {
			return false
		}
	}
	for key, value := range left.Env {
		if right.Env[key] != value {
			return false
		}
	}
	return true
}

// startServer launches one subprocess and performs the MCP handshake. Failures
// are recorded on the runtime so the admin console can show why a server is down.
func (m *MCPManager) startServer(ctx context.Context, server MCPServerConfig) {
	now := time.Now()
	client, err := mcp.StartStdio(ctx, server.Command, server.Args, server.Env)
	if err != nil {
		m.recordFailure(server.Name, now, fmt.Errorf("start %s: %w", server.Command, err))
		return
	}
	initCtx, cancel := context.WithTimeout(ctx, mcpInitializeTimeout)
	defer cancel()
	if err := client.Initialize(initCtx); err != nil {
		_ = client.Close()
		m.recordFailure(server.Name, now, fmt.Errorf("initialize: %w", err))
		return
	}
	tools, err := client.RefreshTools(initCtx)
	if err != nil {
		_ = client.Close()
		m.recordFailure(server.Name, now, fmt.Errorf("tools/list: %w", err))
		return
	}

	m.mu.Lock()
	runtime, ok := m.servers[server.Name]
	if !ok || m.closed {
		m.mu.Unlock()
		_ = client.Close()
		return
	}
	runtime.client = client
	runtime.tools = tools
	runtime.status = "ready"
	runtime.lastError = ""
	runtime.startedAt = now
	runtime.lastAttempt = now
	runtime.restartCount = 0
	m.mu.Unlock()
	log.Printf("[mcp] server %s ready with %d tool(s)", server.Name, len(tools))
}

func (m *MCPManager) recordFailure(name string, attemptedAt time.Time, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	runtime, ok := m.servers[name]
	if !ok {
		return
	}
	runtime.client = nil
	runtime.tools = nil
	runtime.status = "failed"
	runtime.lastError = strings.TrimSpace(err.Error())
	runtime.lastAttempt = attemptedAt
	runtime.restartCount++
	log.Printf("[mcp] server %s failed (attempt %d): %v", name, runtime.restartCount, err)
}

func mcpRestartBackoff(restarts int) time.Duration {
	if restarts <= 0 {
		return 0
	}
	wait := mcpRestartBackoffBase
	for i := 1; i < restarts && wait < mcpRestartBackoffMax; i++ {
		wait *= 2
	}
	if wait > mcpRestartBackoffMax {
		wait = mcpRestartBackoffMax
	}
	return wait
}

// StartSupervisor restarts dead subprocesses with exponential backoff.
func (m *MCPManager) StartSupervisor(parent context.Context) {
	if m == nil {
		return
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	m.mu.Lock()
	if m.cancel != nil {
		m.mu.Unlock()
		cancel()
		return
	}
	m.cancel = cancel
	m.mu.Unlock()

	go func() {
		ticker := time.NewTicker(mcpSupervisorInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.reviveDeadServers(ctx)
			}
		}
	}()
}

func (m *MCPManager) reviveDeadServers(ctx context.Context) {
	now := time.Now()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	var restart []MCPServerConfig
	for name, runtime := range m.servers {
		alive := runtime.client != nil && runtime.client.Alive()
		if alive {
			continue
		}
		if runtime.client != nil {
			_ = runtime.client.Close()
			runtime.client = nil
			runtime.status = "stopped"
			runtime.lastError = "subprocess exited"
		}
		if wait := mcpRestartBackoff(runtime.restartCount); wait > 0 && now.Sub(runtime.lastAttempt) < wait {
			continue
		}
		runtime.lastAttempt = now
		runtime.status = "starting"
		restart = append(restart, m.servers[name].config)
	}
	m.mu.Unlock()
	for _, server := range restart {
		m.startServer(ctx, server)
	}
}

// Tools returns the aggregated catalog as OpenAI tool definitions. Names are
// namespaced by server so two servers can expose the same tool name.
func (m *MCPManager) Tools() []ToolDefinition {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ToolDefinition, 0, 8)
	names := make([]string, 0, len(m.servers))
	for name := range m.servers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, serverName := range names {
		runtime := m.servers[serverName]
		if runtime.client == nil {
			continue
		}
		for _, tool := range runtime.tools {
			if strings.TrimSpace(tool.Name) == "" {
				continue
			}
			out = append(out, ToolDefinition{
				Type:        "function",
				Name:        mcpNamespacedToolName(serverName, tool.Name),
				Description: tool.Description,
				Parameters:  tool.InputSchema,
				Source:      toolSourceMCP,
			})
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Owns reports whether the namespaced tool name belongs to a live MCP server.
func (m *MCPManager) Owns(name string) bool {
	if m == nil {
		return false
	}
	serverName, toolName, ok := splitMCPToolName(name)
	if !ok {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	runtime, exists := m.servers[serverName]
	if !exists || runtime.client == nil {
		return false
	}
	for _, tool := range runtime.tools {
		if tool.Name == toolName {
			return true
		}
	}
	return false
}

// CallTool executes a namespaced MCP tool and returns its text content. The
// bool reports whether the tool reported an error, which the caller records as
// failed evidence rather than as a transport failure.
func (m *MCPManager) CallTool(ctx context.Context, name string, args map[string]any) (string, bool, error) {
	if m == nil {
		return "", false, fmt.Errorf("mcp is not configured")
	}
	serverName, toolName, ok := splitMCPToolName(name)
	if !ok {
		return "", false, fmt.Errorf("tool %s is not an mcp tool", name)
	}
	m.mu.Lock()
	runtime, exists := m.servers[serverName]
	if !exists {
		m.mu.Unlock()
		return "", false, fmt.Errorf("mcp server %s not configured", serverName)
	}
	client := runtime.client
	timeout := mcpCallTimeout(runtime.config.TimeoutSec)
	m.mu.Unlock()
	if client == nil {
		return "", false, fmt.Errorf("mcp server %s is not running", serverName)
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := client.CallTool(callCtx, toolName, args)
	if err != nil {
		return "", false, err
	}
	text := result.Text()
	if strings.TrimSpace(text) == "" {
		text = string(result.ContentJSON())
	}
	if len(text) > mcpMaxToolResultLength {
		text = text[:mcpMaxToolResultLength] + "\n... [truncated]"
	}
	return text, result.IsError, nil
}

// Status renders the runtime state for the admin console.
func (m *MCPManager) Status() []map[string]any {
	if m == nil {
		return []map[string]any{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.servers))
	for name := range m.servers {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]map[string]any, 0, len(names))
	for _, name := range names {
		runtime := m.servers[name]
		tools := make([]map[string]any, 0, len(runtime.tools))
		for _, tool := range runtime.tools {
			tools = append(tools, map[string]any{
				"name":           tool.Name,
				"qualified_name": mcpNamespacedToolName(name, tool.Name),
				"description":    tool.Description,
				"input_schema":   tool.InputSchema,
			})
		}
		alive := runtime.client != nil && runtime.client.Alive()
		status := runtime.status
		if !alive && status == "ready" {
			status = "stopped"
		}
		out = append(out, map[string]any{
			"name":          name,
			"command":       runtime.config.Command,
			"args":          runtime.config.Args,
			"enabled":       runtime.config.Enabled,
			"timeout_sec":   runtime.config.TimeoutSec,
			"status":        status,
			"alive":         alive,
			"last_error":    runtime.lastError,
			"started_at":    formatRFC3339OrEmpty(runtime.startedAt),
			"restart_count": runtime.restartCount,
			"tool_count":    len(runtime.tools),
			"tools":         tools,
		})
	}
	return out
}

// Reload restarts every configured server, which is the admin-facing recovery
// action when a server is wedged.
func (m *MCPManager) Reload(ctx context.Context, servers []MCPServerConfig) {
	if m == nil {
		return
	}
	m.mu.Lock()
	var stopped []*mcp.Client
	for name, runtime := range m.servers {
		if runtime.client != nil {
			stopped = append(stopped, runtime.client)
		}
		delete(m.servers, name)
	}
	m.mu.Unlock()
	for _, client := range stopped {
		_ = client.Close()
	}
	m.Apply(ctx, servers)
}

func (m *MCPManager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	cancel := m.cancel
	m.cancel = nil
	var stopped []*mcp.Client
	for name, runtime := range m.servers {
		if runtime.client != nil {
			stopped = append(stopped, runtime.client)
		}
		delete(m.servers, name)
	}
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	for _, client := range stopped {
		_ = client.Close()
	}
}
