//go:build wreq_ffi

package wreq

/*
#cgo CFLAGS: -I${SRCDIR}/../../wreq-ffi/include
#cgo windows LDFLAGS: ${SRCDIR}/../../wreq-ffi/target/release/libwreq_ffi.a
#cgo !windows LDFLAGS: ${SRCDIR}/../../wreq-ffi/target/release/libwreq_ffi.a -ldl -lm -lpthread

#include <stdlib.h>
#include "wreq_ffi.h"

// Keep explicit forward declarations here so Go-side symbol binding remains stable
// even when a local generated header is stale/missing newer prototypes.
typedef struct WreqResponseHandle WreqResponseHandle;
int32_t wreq_request_begin(struct WreqClient *client,
                           const uint8_t *spec_json,
                           size_t spec_len,
                           const uint8_t *body_ptr,
                           size_t body_len,
                           struct WreqResponseHandle **out_handle,
                           uint16_t *out_status,
                           char **out_headers_json,
                           char **out_final_url,
                           char **out_error);
intptr_t wreq_response_read(struct WreqResponseHandle *handle,
                            uint8_t *buf,
                            size_t cap,
                            uint32_t timeout_ms);
void wreq_response_close(struct WreqResponseHandle *handle);
*/
import "C"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
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
	Body        []byte     `json:"-"`
	TimeoutSecs uint64     `json:"timeout_secs,omitempty"`
}

type Response struct {
	Status   int
	Headers  [][]string
	FinalURL string

	handle *C.struct_WreqResponseHandle
	closed atomic.Bool
}

const (
	wreqOK         int32 = 0
	wreqErrNilArg  int32 = -1
	wreqErrTimeout int32 = -9
)

func errorFromCode(where string, code int32, detail string) error {
	if code == wreqOK {
		return nil
	}
	if trimmed := strings.TrimSpace(detail); trimmed != "" {
		return fmt.Errorf("wreq: %s failed (code=%d): %s", where, code, trimmed)
	}
	return fmt.Errorf("wreq: %s failed (code=%d)", where, code)
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

func (c *Client) Begin(ctx context.Context, spec RequestSpec) (*Response, error) {
	if c == nil || c.handle == nil || c.closed.Load() {
		return nil, errors.New("wreq: client closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	specPayload := struct {
		Method      string     `json:"method"`
		URL         string     `json:"url"`
		Headers     [][]string `json:"headers,omitempty"`
		TimeoutSecs uint64     `json:"timeout_secs,omitempty"`
	}{
		Method:      spec.Method,
		URL:         spec.URL,
		Headers:     spec.Headers,
		TimeoutSecs: spec.TimeoutSecs,
	}
	specJSON, err := json.Marshal(specPayload)
	if err != nil {
		return nil, fmt.Errorf("wreq: marshal request: %w", err)
	}

	var specPtr *C.uint8_t
	if len(specJSON) > 0 {
		specPtr = (*C.uint8_t)(unsafe.Pointer(&specJSON[0]))
	}
	var bodyPtr *C.uint8_t
	if len(spec.Body) > 0 {
		bodyPtr = (*C.uint8_t)(unsafe.Pointer(&spec.Body[0]))
	}

	var cHandle *C.struct_WreqResponseHandle
	var cStatus C.uint16_t
	var cHeaders *C.char
	var cFinalURL *C.char
	var cErr *C.char

	code := int32(C.wreq_request_begin(
		c.handle,
		specPtr,
		C.size_t(len(specJSON)),
		bodyPtr,
		C.size_t(len(spec.Body)),
		&cHandle,
		&cStatus,
		&cHeaders,
		&cFinalURL,
		&cErr,
	))

	var detail string
	if cErr != nil {
		detail = C.GoString(cErr)
		C.wreq_string_free(cErr)
	}
	if code != wreqOK {
		if cHeaders != nil {
			C.wreq_string_free(cHeaders)
		}
		if cFinalURL != nil {
			C.wreq_string_free(cFinalURL)
		}
		return nil, errorFromCode("wreq_request_begin", code, detail)
	}
	if cHandle == nil {
		if cHeaders != nil {
			C.wreq_string_free(cHeaders)
		}
		if cFinalURL != nil {
			C.wreq_string_free(cFinalURL)
		}
		return nil, errors.New("wreq: begin returned nil response handle")
	}

	resp := &Response{
		Status: int(cStatus),
		handle: cHandle,
	}
	if cHeaders != nil {
		headersJSON := C.GoString(cHeaders)
		C.wreq_string_free(cHeaders)
		if strings.TrimSpace(headersJSON) != "" {
			if err := json.Unmarshal([]byte(headersJSON), &resp.Headers); err != nil {
				_ = resp.Close()
				return nil, fmt.Errorf("wreq: decode headers json: %w", err)
			}
		}
	}
	if cFinalURL != nil {
		resp.FinalURL = C.GoString(cFinalURL)
		C.wreq_string_free(cFinalURL)
	}

	runtime.SetFinalizer(resp, func(r *Response) { _ = r.Close() })
	return resp, nil
}

func (r *Response) Read(p []byte) (int, error) {
	if r == nil {
		return 0, errors.New("wreq: response is nil")
	}
	if r.handle == nil {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	n := int64(C.wreq_response_read(
		r.handle,
		(*C.uint8_t)(unsafe.Pointer(&p[0])),
		C.size_t(len(p)),
		C.uint32_t(0),
	))
	if n > 0 {
		return int(n), nil
	}
	if n == 0 {
		return 0, io.EOF
	}
	code := int32(n)
	if code == wreqErrTimeout {
		return 0, context.DeadlineExceeded
	}
	if code == wreqErrNilArg {
		return 0, errors.New("wreq: invalid read argument")
	}
	return 0, errorFromCode("wreq_response_read", code, "")
}

func (r *Response) Close() error {
	if r == nil || !r.closed.CompareAndSwap(false, true) {
		return nil
	}
	if r.handle != nil {
		C.wreq_response_close(r.handle)
		r.handle = nil
	}
	runtime.SetFinalizer(r, nil)
	return nil
}

func (r *Response) Body() ([]byte, error) {
	if r == nil {
		return nil, errors.New("wreq: response is nil")
	}
	body, err := io.ReadAll(r)
	if err != nil && !errors.Is(err, io.EOF) {
		_ = r.Close()
		return nil, err
	}
	_ = r.Close()
	return body, nil
}

func (c *Client) Do(ctx context.Context, spec RequestSpec) (*Response, error) {
	resp, err := c.Begin(ctx, spec)
	if err != nil {
		return nil, err
	}
	if _, err := resp.Body(); err != nil {
		return nil, err
	}
	return resp, nil
}

func Version() string {
	return C.GoString(C.wreq_ffi_version())
}
