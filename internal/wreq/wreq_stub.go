//go:build !wreq_ffi

package wreq

import (
	"context"
	"errors"
	"io"
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
	Body        []byte     `json:"-"`
	TimeoutSecs uint64     `json:"timeout_secs,omitempty"`
}

type Response struct {
	Status   int
	Headers  [][]string
	FinalURL string
}

func (r *Response) Read(_ []byte) (int, error) { return 0, io.EOF }
func (r *Response) Body() ([]byte, error)      { return nil, ErrNotLinked }
func (r *Response) Close() error               { return nil }

type Client struct{}

func New(_ ClientConfig) (*Client, error) { return nil, ErrNotLinked }

func (c *Client) Close() error { return nil }

func (c *Client) Begin(_ context.Context, _ RequestSpec) (*Response, error) {
	return nil, ErrNotLinked
}

func (c *Client) Do(_ context.Context, _ RequestSpec) (*Response, error) {
	return nil, ErrNotLinked
}

func Version() string { return "unlinked" }
