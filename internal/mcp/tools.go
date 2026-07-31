package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"
)

type ToolCache struct {
	mu    sync.RWMutex
	tools []Tool
}

func (c *ToolCache) Replace(tools []Tool) {
	stored := append([]Tool(nil), tools...)
	c.mu.Lock()
	c.tools = stored
	c.mu.Unlock()
}

func (c *ToolCache) List() []Tool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]Tool(nil), c.tools...)
}

func (c *ToolCache) Find(name string) (Tool, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, tool := range c.tools {
		if tool.Name == name {
			return tool, true
		}
	}
	return Tool{}, false
}

func (c *Client) RefreshTools(ctx context.Context) ([]Tool, error) {
	tools, err := c.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	c.toolCache.Replace(tools)
	return tools, nil
}

func (c *Client) CachedTools() []Tool { return c.toolCache.List() }

func (r CallResult) Text() string {
	var out []string
	for _, block := range r.Content {
		if typ, _ := block["type"].(string); typ != "text" {
			continue
		}
		if text, _ := block["text"].(string); text != "" {
			out = append(out, text)
		}
	}
	return strings.Join(out, "\n")
}

func (r CallResult) ContentJSON() []byte {
	b, _ := json.Marshal(r.Content)
	return b
}

var defaultTimeout = 30 * time.Second

func WithDefaultTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, defaultTimeout)
}
