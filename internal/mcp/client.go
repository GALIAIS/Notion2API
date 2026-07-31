package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
)

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema,omitempty"`
}

type CallResult struct {
	Content        []map[string]any `json:"content,omitempty"`
	StructuredData any              `json:"structuredContent,omitempty"`
	IsError        bool             `json:"isError,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

var ErrClosed = errors.New("mcp client is closed")

type Client struct {
	cmd     *exec.Cmd
	in      io.WriteCloser
	out     *bufio.Reader
	closeFn func() error

	writeMu sync.Mutex

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan rpcResponse
	closed  bool
	readErr error
	done    chan struct{}

	toolCache ToolCache
}

func StartStdio(ctx context.Context, command string, args []string, env map[string]string) (*Client, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		_ = in.Close()
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		_ = in.Close()
		return nil, err
	}
	c := newClient(in, out, func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Kill()
	})
	c.cmd = cmd
	return c, nil
}

// newClient owns the transport and starts the single read loop; all responses are
// dispatched from that one goroutine so concurrent requests cannot steal each
// other's replies.
func newClient(in io.WriteCloser, out io.Reader, closeFn func() error) *Client {
	c := &Client{
		in:      in,
		out:     bufio.NewReader(out),
		closeFn: closeFn,
		nextID:  1,
		pending: make(map[int64]chan rpcResponse),
		done:    make(chan struct{}),
	}
	go c.readLoop()
	return c
}

func (c *Client) readLoop() {
	for {
		line, err := c.out.ReadBytes('\n')
		if len(line) > 0 {
			var r rpcResponse
			if json.Unmarshal(line, &r) == nil && r.ID != nil {
				c.dispatch(r)
			}
		}
		if err != nil {
			c.fail(err)
			return
		}
	}
}

func (c *Client) dispatch(r rpcResponse) {
	c.mu.Lock()
	ch, ok := c.pending[*r.ID]
	if ok {
		delete(c.pending, *r.ID)
	}
	c.mu.Unlock()
	if ok {
		ch <- r
	}
}

func (c *Client) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.readErr == nil {
		c.readErr = err
	}
	c.pending = make(map[int64]chan rpcResponse)
	select {
	case <-c.done:
	default:
		close(c.done)
	}
}

func (c *Client) deathErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.readErr != nil {
		return fmt.Errorf("mcp transport closed: %w", c.readErr)
	}
	return ErrClosed
}

func (c *Client) request(ctx context.Context, method string, params any, dst any) error {
	c.mu.Lock()
	if c.closed || c.readErr != nil {
		c.mu.Unlock()
		return c.deathErr()
	}
	id := c.nextID
	c.nextID++
	ch := make(chan rpcResponse, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	if err := c.write(req); err != nil {
		return err
	}

	select {
	case r := <-ch:
		return decode(r, dst)
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		// The reply may have landed in the same instant the loop died.
		select {
		case r := <-ch:
			return decode(r, dst)
		default:
		}
		return c.deathErr()
	}
}

func decode(r rpcResponse, dst any) error {
	if r.Error != nil {
		return fmt.Errorf("MCP error %d: %s", r.Error.Code, r.Error.Message)
	}
	if dst != nil && len(r.Result) > 0 {
		return json.Unmarshal(r.Result, dst)
	}
	return nil
}

func (c *Client) notify(method string, params any) error {
	req := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		req["params"] = params
	}
	return c.write(req)
}

func (c *Client) write(req map[string]any) error {
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	b = append(b, '\n')

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return ErrClosed
	}
	_, err = c.in.Write(b)
	return err
}

func (c *Client) Initialize(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "notion2api", "version": "0.1.0"},
	}
	var result map[string]any
	if err := c.request(ctx, "initialize", params, &result); err != nil {
		return err
	}
	return c.notify("notifications/initialized", nil)
}

func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var r struct {
		Tools []Tool `json:"tools"`
	}
	err := c.request(ctx, "tools/list", nil, &r)
	return r.Tools, err
}

func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (CallResult, error) {
	var r CallResult
	err := c.request(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, &r)
	return r, err
}

func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	_ = c.in.Close()
	c.fail(ErrClosed)
	if c.closeFn != nil {
		return c.closeFn()
	}
	return nil
}

func (c *Client) Alive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.closed && c.readErr == nil
}
