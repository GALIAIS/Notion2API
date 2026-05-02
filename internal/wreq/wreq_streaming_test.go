package wreq

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func ndjsonLine(payload map[string]any) string {
	raw, _ := json.Marshal(payload)
	return string(raw) + "\n"
}

func TestWreqStreamingLatencyShapeWithHTTPFallback(t *testing.T) {
	firstWriteCh := make(chan time.Time, 1)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		flusher, _ := w.(http.Flusher)
		firstWriteAt := time.Now()
		firstWriteCh <- firstWriteAt
		_, _ = w.Write([]byte(ndjsonLine(map[string]any{
			"type": "agent-inference",
			"value": []map[string]any{
				{"type": "text", "content": "chunk-1"},
			},
		})))
		if flusher != nil {
			flusher.Flush()
		}

		time.Sleep(100 * time.Millisecond)

		_, _ = w.Write([]byte(ndjsonLine(map[string]any{
			"type": "agent-inference",
			"value": []map[string]any{
				{"type": "text", "content": "chunk-2"},
			},
			"finishedAt": time.Now().Format(time.RFC3339Nano),
		})))
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, upstream.URL, strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http do: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	line1, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read first line: %v", err)
	}
	if strings.TrimSpace(line1) == "" {
		t.Fatalf("first line is empty")
	}
	firstReadAt := time.Now()
	firstWriteAt := <-firstWriteCh

	delay := firstReadAt.Sub(firstWriteAt)
	if delay > 50*time.Millisecond {
		t.Fatalf("first chunk delay too high: got %s want <= 50ms", delay)
	}

	line2, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read second line: %v", err)
	}
	if !strings.Contains(line2, "chunk-2") {
		t.Fatalf("unexpected second line: %q", line2)
	}
}
