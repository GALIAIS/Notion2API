//go:build !wreq_ffi

package wreq

import (
	"context"
	"errors"
)

var ErrNotLinked = errors.New("wreq: built without wreq_ffi tag; use node-wreq fallback")

type ClientConfig struct {
	Emulation          string `json:"emulation,omitempty"`
	TimeoutSecs        uint64 `json:"timeout_secs,omitempty"`
	CookieStore        *bool  `json:"cookie_store,omitempty"`
	ProxyURL           string `json:"proxy_url,omitempty"`
	AcceptInvalidCerts *bool  `json:"accept_invalid_certs,omitempty"`
}

type RequestSpec struct {
	Method      string     `json:"method"`
	URL         string     `json:"url"`
	Headers     [][]string `json:"headers,omitempty"`
	BodyB64     string     `json:"body_b64,omitempty"`
	TimeoutSecs uint64     `json:"timeout_secs,omitempty"`
}

type Response struct {
	OK       bool       `json:"ok"`
	Status   int        `json:"status"`
	Headers  [][]string `json:"headers"`
	BodyB64  string     `json:"body_b64"`
	FinalURL string     `json:"final_url"`
	Error    string     `json:"error,omitempty"`
}

func (r *Response) Body() ([]byte, error) { return nil, ErrNotLinked }

type Client struct{}

func New(_ ClientConfig) (*Client, error) { return nil, ErrNotLinked }

func (c *Client) Close() error { return nil }

func (c *Client) Do(_ context.Context, _ RequestSpec) (*Response, error) {
	return nil, ErrNotLinked
}

func Version() string { return "unlinked" }
