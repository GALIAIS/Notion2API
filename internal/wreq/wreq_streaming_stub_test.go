//go:build !wreq_ffi

package wreq

import (
	"errors"
	"io"
	"testing"
)

func TestWreqStubBeginNotLinked(t *testing.T) {
	client, err := New(ClientConfig{})
	if err == nil || client != nil {
		t.Fatalf("expected stub New to fail with ErrNotLinked")
	}
	if !errors.Is(err, ErrNotLinked) {
		t.Fatalf("expected ErrNotLinked, got %v", err)
	}
}

func TestWreqStubResponseReadEOF(t *testing.T) {
	var r Response
	n, err := r.Read(make([]byte, 16))
	if n != 0 {
		t.Fatalf("expected n=0, got %d", n)
	}
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected io.EOF, got %v", err)
	}
}
