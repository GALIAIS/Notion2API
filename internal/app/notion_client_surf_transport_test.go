package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRunLoginHelperRequestWithSurf_MapsStatusHeadersBodyAndSetCookies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Method; got != http.MethodPost {
			t.Fatalf("method = %s, want POST", got)
		}
		if got := r.Header.Get("X-Test"); got != "ok" {
			t.Fatalf("X-Test header = %q, want ok", got)
		}
		http.SetCookie(w, &http.Cookie{Name: "token_v2", Value: "new-value", Path: "/"})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	resp, err := runLoginHelperRequestWithSurf(context.Background(), loginTransportRequest{
		Method:           http.MethodPost,
		URL:              server.URL,
		Headers:          map[string]string{"X-Test": "ok"},
		Body:             `{"hello":"world"}`,
		RequestTimeoutMS: 30000,
	})
	if err != nil {
		t.Fatalf("runLoginHelperRequestWithSurf error: %v", err)
	}
	if resp.Status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", resp.Status, http.StatusCreated)
	}
	if !strings.Contains(strings.ToLower(resp.ContentType), "application/json") {
		t.Fatalf("content_type = %q", resp.ContentType)
	}
	if strings.TrimSpace(resp.Body) != `{"ok":true}` {
		t.Fatalf("body = %q", resp.Body)
	}
	if len(resp.SetCookies) == 0 || resp.SetCookies[0].Name != "token_v2" {
		t.Fatalf("set_cookies = %#v", resp.SetCookies)
	}
}

func TestRunLoginHelperRequestWithSurf_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := runLoginHelperRequestWithSurf(ctx, loginTransportRequest{
		Method:           http.MethodGet,
		URL:              "https://example.com",
		RequestTimeoutMS: 30000,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestRunLoginHelperRequestWithSurf_PreservesRedirectSetCookies(t *testing.T) {
	const cookieName = "redirect_token"
	const cookieValue = "set-on-redirect-hop"

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()

	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: cookieValue, Path: "/"})
		http.Redirect(w, r, server.URL+"/final", http.StatusFound)
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("done"))
	})

	resp, err := runLoginHelperRequestWithSurf(context.Background(), loginTransportRequest{
		Method:           http.MethodGet,
		URL:              server.URL + "/start",
		RequestTimeoutMS: 30000,
	})
	if err != nil {
		t.Fatalf("runLoginHelperRequestWithSurf error: %v", err)
	}
	if resp.Status != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Status, http.StatusOK)
	}
	if got := probeCookieValue(resp.SetCookies, cookieName); got != cookieValue {
		t.Fatalf("redirect cookie mismatch: got %q want %q, set_cookies=%#v", got, cookieValue, resp.SetCookies)
	}
}

func TestLoginTransportDoRequest_UsesSurfTransport(t *testing.T) {
	origSurf := loginTransportRunSurfRequest
	origFallback := loginTransportRunFallbackRequest
	defer func() {
		loginTransportRunSurfRequest = origSurf
		loginTransportRunFallbackRequest = origFallback
	}()

	surfHits := 0
	fallbackHits := 0
	loginTransportRunSurfRequest = func(_ context.Context, _ loginTransportRequest) (*loginTransportResponse, error) {
		surfHits++
		return &loginTransportResponse{
			Status:     http.StatusCreated,
			Headers:    map[string]string{"x-transport": "surf"},
			Body:       "surf",
			SetCookies: []ProbeCookie{{Name: "token_v2", Value: "surf"}},
		}, nil
	}
	loginTransportRunFallbackRequest = func(_ context.Context, _ loginTransportRequest) (*loginTransportResponse, error) {
		fallbackHits++
		return &loginTransportResponse{
			Status:     http.StatusAccepted,
			Headers:    map[string]string{"x-transport": "fallback"},
			Body:       "fallback",
			SetCookies: []ProbeCookie{{Name: "token_v2", Value: "fallback"}},
		}, nil
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New error: %v", err)
	}
	session := &loginHTTPSession{
		Client:                 &http.Client{Jar: jar},
		UseSurfHelperTransport: true,
		ProxyResolver:          nil,
		AccountEmail:           "tester@example.com",
		Timeout:                30 * time.Second,
		Upstream:               NotionUpstream{},
	}

	targetURL := "https://example.com/login"
	status, headers, body, err := loginTransportDoRequest(context.Background(), session, http.MethodGet, targetURL, map[string]string{"X-Test": "1"}, nil)
	if err != nil {
		t.Fatalf("loginTransportDoRequest error: %v", err)
	}
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}
	if got := headers.Get("x-transport"); got != "surf" {
		t.Fatalf("x-transport = %q, want %q", got, "surf")
	}
	if got := string(body); got != "surf" {
		t.Fatalf("body = %q, want %q", got, "surf")
	}
	if surfHits != 1 {
		t.Fatalf("surf branch hits mismatch: got %d want 1", surfHits)
	}
	if fallbackHits != 0 {
		t.Fatalf("fallback branch should stay unused, got hits=%d", fallbackHits)
	}
	if got := probeCookieValue(probeCookiesFromJar(session.Jar, targetURL), "token_v2"); got != "surf" {
		t.Fatalf("session jar token_v2 = %q, want %q", got, "surf")
	}
}

func TestLoginTransportDoRequest_SurfPreservesRedirectCookiesInSessionJar(t *testing.T) {
	const cookieName = "redirect_token"
	const cookieValue = "persisted"

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()

	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: cookieValue, Path: "/"})
		http.Redirect(w, r, server.URL+"/final", http.StatusFound)
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New error: %v", err)
	}
	session := &loginHTTPSession{
		Client:                 &http.Client{Jar: jar},
		UseSurfHelperTransport: true,
		Timeout:                30 * time.Second,
	}

	targetURL := server.URL + "/start"
	status, _, _, err := loginTransportDoRequest(context.Background(), session, http.MethodGet, targetURL, nil, nil)
	if err != nil {
		t.Fatalf("loginTransportDoRequest error: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if got := probeCookieValue(probeCookiesFromJar(session.Jar, targetURL), cookieName); got != cookieValue {
		t.Fatalf("session jar redirect cookie mismatch: got %q want %q", got, cookieValue)
	}
}

func TestRunInferenceTranscriptInBrowserWithSurf_ReturnsNDJSON(t *testing.T) {
	line := `{"type":"agent-inference","id":"m1","finishedAt":"2026-05-03T00:00:00Z","value":[{"type":"text","content":"OK"}]}` + "\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Method; got != http.MethodPost {
			t.Fatalf("method = %s, want POST", got)
		}
		if got := strings.TrimSpace(r.Header.Get("Cookie")); got == "" {
			t.Fatalf("expected cookie header to be present")
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload failed: %v", err)
		}
		if got := strings.TrimSpace(stringValue(payload["threadId"])); got != "t1" {
			t.Fatalf("threadId = %q, want t1", got)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(line))
	}))
	defer server.Close()

	client := newBrowserFallbackTestClient(server.URL)
	body, err := runInferenceTranscriptInBrowserWithSurf(context.Background(), client, map[string]any{"threadId": "t1"})
	if err != nil {
		t.Fatalf("runInferenceTranscriptInBrowserWithSurf error: %v", err)
	}
	if body != line {
		t.Fatalf("body mismatch: got %q want %q", body, line)
	}
}

func TestRunInferenceTranscriptInBrowserWithSurf_RejectsHTMLChallenge(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body>cloudflare cookiePart challenge</body></html>"))
	}))
	defer server.Close()

	client := newBrowserFallbackTestClient(server.URL)
	_, err := runInferenceTranscriptInBrowserWithSurf(context.Background(), client, map[string]any{"threadId": "t1"})
	if err == nil || !strings.Contains(err.Error(), "challenge/html content") {
		t.Fatalf("unexpected err: %v", err)
	}
}
