package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/enetx/g"
	"github.com/enetx/surf"
)

func newSurfStdClient(proxy string) (*http.Client, error) {
	builder := surf.NewClient().Builder().Session().Impersonate().Chrome()
	if strings.TrimSpace(proxy) != "" {
		builder = builder.Proxy(g.String(proxy))
	}
	clientResult := builder.Build()
	if err := clientResult.Err(); err != nil {
		return nil, err
	}
	return clientResult.Unwrap().Std(), nil
}

func loadProbeCookiesIntoJar(jar http.CookieJar, target *url.URL, cookies []ProbeCookie) {
	if jar == nil || target == nil || len(cookies) == 0 {
		return
	}
	items := make([]*http.Cookie, 0, len(cookies))
	for _, c := range cookies {
		name := strings.TrimSpace(c.Name)
		if name == "" {
			continue
		}
		items = append(items, &http.Cookie{
			Name:  name,
			Value: c.Value,
			Path:  "/",
		})
	}
	if len(items) > 0 {
		jar.SetCookies(target, items)
	}
}

func runLoginHelperRequestWithSurf(ctx context.Context, request loginTransportRequest) (*loginTransportResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	stdClient, err := newSurfStdClient(request.Proxy)
	if err != nil {
		return nil, err
	}

	timeout := time.Duration(request.RequestTimeoutMS) * time.Millisecond
	if timeout < 30*time.Second {
		timeout = 30 * time.Second
	}
	stdClient.Timeout = timeout

	parsedTargetURL, err := url.Parse(request.URL)
	if err != nil {
		return nil, err
	}
	loadProbeCookiesIntoJar(stdClient.Jar, parsedTargetURL, request.Cookies)

	var body io.Reader
	if request.Body != "" {
		body = bytes.NewBufferString(request.Body)
	}

	method := strings.ToUpper(strings.TrimSpace(request.Method))
	if method == "" {
		method = http.MethodGet
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, parsedTargetURL.String(), body)
	if err != nil {
		return nil, err
	}
	for k, v := range request.Headers {
		if strings.EqualFold(strings.TrimSpace(k), "cookie") {
			continue
		}
		httpReq.Header.Set(k, v)
	}

	resp, err := stdClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	out := &loginTransportResponse{
		Status:      resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Headers:     map[string]string{},
		Body:        string(respBody),
		SetCookies:  []ProbeCookie{},
	}
	for k, values := range resp.Header {
		if strings.EqualFold(k, "set-cookie") || len(values) == 0 {
			continue
		}
		out.Headers[strings.ToLower(k)] = values[len(values)-1]
	}

	for _, c := range resp.Cookies() {
		name := strings.TrimSpace(c.Name)
		if name == "" {
			continue
		}
		out.SetCookies = append(out.SetCookies, ProbeCookie{
			Name:  name,
			Value: c.Value,
		})
	}

	// Preserve effective cookies after redirects by reading from the jar.
	if stdClient.Jar != nil {
		jarCookies := stdClient.Jar.Cookies(parsedTargetURL)
		if len(jarCookies) > 0 {
			out.SetCookies = out.SetCookies[:0]
			for _, c := range jarCookies {
				name := strings.TrimSpace(c.Name)
				if name == "" {
					continue
				}
				out.SetCookies = append(out.SetCookies, ProbeCookie{
					Name:  name,
					Value: c.Value,
				})
			}
		}
	}

	return out, nil
}

func runInferenceTranscriptInBrowserWithSurf(ctx context.Context, client *NotionAIClient, payload map[string]any) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	request, err := buildBrowserTransportRequest(client, payload)
	if err != nil {
		return "", err
	}
	stdClient, err := newSurfStdClient(request.Proxy)
	if err != nil {
		return "", err
	}

	timeout := time.Duration(request.RequestTimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = notionTransportDefaultRequestTimeout
	}
	stdClient.Timeout = timeout

	parsedRunURL, err := url.Parse(request.RunURL)
	if err != nil {
		return "", err
	}
	loadProbeCookiesIntoJar(stdClient.Jar, parsedRunURL, request.Cookies)

	requestBody, err := json.Marshal(request.Payload)
	if err != nil {
		return "", err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, parsedRunURL.String(), bytes.NewReader(requestBody))
	if err != nil {
		return "", err
	}
	for k, v := range request.Headers {
		if strings.EqualFold(strings.TrimSpace(k), "cookie") {
			continue
		}
		httpReq.Header.Set(k, v)
	}

	resp, err := stdClient.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("browser fallback returned non-success status=%d content_type=%q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	text := string(respBody)
	if err := detectInferenceStreamResponseFormat(text); err != nil {
		return "", err
	}
	return text, nil
}
