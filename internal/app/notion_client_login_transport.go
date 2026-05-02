package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

type loginWreqRequest struct {
	Method           string            `json:"method"`
	URL              string            `json:"url"`
	Headers          map[string]string `json:"headers"`
	Body             string            `json:"body,omitempty"`
	Cookies          []ProbeCookie     `json:"cookies"`
	BrowserProfile   string            `json:"browser_profile,omitempty"`
	Proxy            string            `json:"proxy,omitempty"`
	RequestTimeoutMS int               `json:"request_timeout_ms"`
}

type loginWreqResponse struct {
	Status      int               `json:"status"`
	ContentType string            `json:"content_type"`
	Headers     map[string]string `json:"headers"`
	Body        string            `json:"body"`
	SetCookies  []ProbeCookie     `json:"set_cookies"`
}

type loginHTTPSession struct {
	*http.Client
	ProxyResolver *ProxyResolver
	AccountEmail  string
	Timeout       time.Duration
	Upstream      NotionUpstream
}

func runLoginHelperRequest(ctx context.Context, request loginWreqRequest) (*loginWreqResponse, error) {
	if _, err := exec.LookPath("node"); err != nil {
		return nil, &browserHelperUnavailableError{Message: "node not found"}
	}
	requestPayload, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	stdoutBytes, err := executeHelperSubprocess(ctx, "node", ".cjs", nodeWreqLoginHelperScript(), requestPayload, browserHelperNodeEnv())
	if err != nil {
		return nil, err
	}
	var response loginWreqResponse
	if err := json.Unmarshal(stdoutBytes, &response); err != nil {
		return nil, fmt.Errorf("login helper returned invalid json: %w", err)
	}
	return &response, nil
}

func loginWreqDoRequest(ctx context.Context, session *loginHTTPSession, method string, targetURL string, headers map[string]string, body []byte) (int, http.Header, []byte, error) {
	if session == nil {
		return 0, nil, nil, fmt.Errorf("login session is nil")
	}
	request := buildLoginWreqRequest(session, method, targetURL, headers, body)
	resp, err := runLoginHelperRequest(ctx, request)
	if err != nil {
		return 0, nil, nil, err
	}
	if session.Client != nil {
		applyLoginWreqSetCookies(session.Jar, targetURL, resp.SetCookies)
	}
	return resp.Status, loginWreqHTTPHeader(resp.Headers), []byte(resp.Body), nil
}

func buildLoginWreqRequest(session *loginHTTPSession, method string, targetURL string, headers map[string]string, body []byte) loginWreqRequest {
	cookies := []ProbeCookie{}
	if session != nil && session.Client != nil {
		cookies = probeCookiesFromJar(session.Jar, targetURL)
	}
	proxyValue := ""
	if session != nil && session.ProxyResolver != nil {
		if parsed, parseErr := url.Parse(targetURL); parseErr == nil {
			if proxyURL, _, resolveErr := session.ProxyResolver.ResolveProxyForRequest(session.AccountEmail, parsed); resolveErr == nil && proxyURL != nil {
				proxyValue = proxyURL.String()
			}
		}
	}
	timeoutMS := 60000
	if session != nil && session.Timeout > 0 {
		timeoutMS = int(session.Timeout / time.Millisecond)
	}
	if timeoutMS < 30000 {
		timeoutMS = 30000
	}
	cleanHeaders := make(map[string]string, len(headers))
	for k, v := range headers {
		if strings.TrimSpace(k) == "" {
			continue
		}
		cleanHeaders[k] = v
	}
	return loginWreqRequest{
		Method:           strings.ToUpper(strings.TrimSpace(method)),
		URL:              targetURL,
		Headers:          cleanHeaders,
		Body:             string(body),
		Cookies:          cookies,
		BrowserProfile:   notionWreqDefaultBrowserProfile,
		Proxy:            proxyValue,
		RequestTimeoutMS: timeoutMS,
	}
}

func applyLoginWreqSetCookies(jar http.CookieJar, targetURL string, setCookies []ProbeCookie) {
	if jar == nil || len(setCookies) == 0 {
		return
	}
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return
	}
	items := make([]*http.Cookie, 0, len(setCookies))
	for _, c := range setCookies {
		name := strings.TrimSpace(c.Name)
		if name == "" {
			continue
		}
		items = append(items, &http.Cookie{Name: name, Value: c.Value, Path: "/"})
	}
	if len(items) > 0 {
		jar.SetCookies(parsed, items)
	}
}

func loginWreqHTTPHeader(headers map[string]string) http.Header {
	out := http.Header{}
	for k, v := range headers {
		out.Set(k, v)
	}
	return out
}
