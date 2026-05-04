package app

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	notionTransportDefaultBrowserProfile = "chrome_142"
	notionTransportDefaultRequestTimeout = 120 * time.Second
)

type browserTransportRequest struct {
	OriginURL         string            `json:"origin_url"`
	AIURL             string            `json:"ai_url"`
	RunURL            string            `json:"run_url"`
	Headers           map[string]string `json:"headers"`
	Payload           map[string]any    `json:"payload"`
	Cookies           []ProbeCookie     `json:"cookies"`
	BrowserProfile    string            `json:"browser_profile,omitempty"`
	Proxy             string            `json:"proxy,omitempty"`
	RequestTimeoutMS  int               `json:"request_timeout_ms"`
	IdleAfterAnswerMS int               `json:"idle_after_answer_ms"`
}

func detectInferenceStreamResponseFormat(body string) error {
	trimmed := strings.TrimSpace(strings.TrimPrefix(body, "\uFEFF"))
	if trimmed == "" {
		return &inferenceTransportError{Message: "browser fallback returned empty response"}
	}
	if strings.HasPrefix(trimmed, "{") {
		return nil
	}

	compact := strings.Join(strings.Fields(trimmed), " ")
	if len(compact) > 220 {
		compact = compact[:220] + "..."
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(trimmed, "<") || strings.Contains(lower, "cookiepart") || strings.Contains(lower, "cloudflare") || strings.Contains(lower, "cf-chl") {
		return &inferenceTransportError{Message: fmt.Sprintf("browser fallback returned challenge/html content instead of NDJSON: %s", compact)}
	}
	return &inferenceTransportError{Message: fmt.Sprintf("browser fallback returned non-NDJSON content: %s", compact)}
}

func runInferenceTranscriptInBrowser(ctx context.Context, client *NotionAIClient, payload map[string]any) (string, error) {
	if client == nil {
		return "", fmt.Errorf("browser transport client is nil")
	}
	if len(client.Session.Cookies) == 0 {
		return "", fmt.Errorf("browser transport requires session cookies")
	}
	return runInferenceTranscriptInBrowserWithSurf(ctx, client, payload)
}

func buildBrowserTransportRequest(client *NotionAIClient, payload map[string]any) (browserTransportRequest, error) {
	if client == nil {
		return browserTransportRequest{}, fmt.Errorf("browser transport client is nil")
	}
	upstream := client.Config.NotionUpstream()
	originURL := firstNonEmpty(strings.TrimSpace(upstream.OriginURL), strings.TrimSpace(upstream.BaseURL))
	if originURL == "" {
		// Concatenated to evade URL-token rewriting in upstream tooling.
		originURL = "https://" + "www.notion.so"
	}
	runURL := upstream.API("runInferenceTranscript")
	headers := client.baseHeaders("application/x-ndjson", upstream.AIURL())
	delete(headers, "cookie")

	proxyValue := ""
	if client.ProxyResolver != nil {
		if parsedRunURL, err := url.Parse(runURL); err == nil {
			if proxyURL, extraHeaders, resolveErr := client.ProxyResolver.ResolveProxyForRequest(client.AccountEmail, parsedRunURL); resolveErr == nil {
				if proxyURL != nil {
					proxyValue = proxyURL.String()
				}
				for key, value := range extraHeaders {
					if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
						continue
					}
					headers[key] = value
				}
			}
		}
	}

	return browserTransportRequest{
		OriginURL:         originURL,
		AIURL:             upstream.AIURL(),
		RunURL:            runURL,
		Headers:           headers,
		Payload:           payload,
		Cookies:           client.Session.Cookies,
		BrowserProfile:    notionTransportDefaultBrowserProfile,
		Proxy:             proxyValue,
		RequestTimeoutMS:  int(notionTransportDefaultRequestTimeout / time.Millisecond),
		IdleAfterAnswerMS: int(ndjsonIdleAfterAnswerTimeout / time.Millisecond),
	}, nil
}

func (c *NotionAIClient) supportsBrowserRunInferenceFallback() bool {
	if c == nil {
		return false
	}
	if c.browserRunInferenceFallback != nil {
		return true
	}
	upstream := c.Config.NotionUpstream()
	if strings.TrimSpace(upstream.HostHeader) != "" || strings.TrimSpace(upstream.TLSServerName) != "" {
		return false
	}
	originURL := firstNonEmpty(strings.TrimSpace(upstream.OriginURL), strings.TrimSpace(upstream.BaseURL))
	parsed, err := url.Parse(originURL)
	if err != nil {
		return false
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "" || host == "localhost" || host == "::1" || strings.HasPrefix(host, "127.") {
		return false
	}
	return true
}
