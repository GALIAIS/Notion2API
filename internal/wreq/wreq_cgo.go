//go:build wreq_ffi

package wreq

/*
#cgo CFLAGS: -I${SRCDIR}/../../wreq-ffi/include
#cgo LDFLAGS: ${SRCDIR}/../../wreq-ffi/target/release/libwreq_ffi.a -ldl -lm -lpthread

#include <stdlib.h>
#include "wreq_ffi.h"
*/
import "C"

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sync/atomic"
	"unsafe"
)

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

func (r *Response) Body() ([]byte, error) {
	if r.BodyB64 == "" {
		return nil, nil
	}
	return base64.StdEncoding.DecodeString(r.BodyB64)
}

type Client struct {
	handle *C.struct_WreqClient
	closed atomic.Bool
}

func New(cfg ClientConfig) (*Client, error) {
	profile, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("wreq: marshal config: %w", err)
	}
	cProfile := C.CString(string(profile))
	defer C.free(unsafe.Pointer(cProfile))

	handle := C.wreq_client_new(cProfile)
	if handle == nil {
		return nil, errors.New("wreq: wreq_client_new returned NULL (bad config?)")
	}
	c := &Client{handle: handle}
	runtime.SetFinalizer(c, func(c *Client) { _ = c.Close() })
	return c, nil
}

func (c *Client) Close() error {
	if c == nil || !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	C.wreq_client_free(c.handle)
	c.handle = nil
	runtime.SetFinalizer(c, nil)
	return nil
}

func (c *Client) Do(ctx context.Context, spec RequestSpec) (*Response, error) {
	if c == nil || c.handle == nil || c.closed.Load() {
		return nil, errors.New("wreq: client closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	reqJSON, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("wreq: marshal request: %w", err)
	}

	cReq := C.CString(string(reqJSON))
	defer C.free(unsafe.Pointer(cReq))

	cResp := C.wreq_request(c.handle, cReq)
	if cResp == nil {
		return nil, errors.New("wreq: wreq_request returned NULL")
	}
	defer C.wreq_string_free(cResp)

	goResp := C.GoString(cResp)
	var resp Response
	if err := json.Unmarshal([]byte(goResp), &resp); err != nil {
		return nil, fmt.Errorf("wreq: unmarshal response: %w", err)
	}
	if !resp.OK {
		return &resp, fmt.Errorf("wreq: %s", resp.Error)
	}
	return &resp, nil
}

func Version() string {
	return C.GoString(C.wreq_ffi_version())
}
