package app

import (
	"fmt"
	"net/url"
	"strings"
)

type ProxyResolver struct {
	cfg AppConfig
}

func NewProxyResolver(cfg AppConfig) *ProxyResolver {
	return &ProxyResolver{cfg: normalizeConfig(cfg)}
}

func (r *ProxyResolver) ResolveProxyForRequest(accountEmail string, target *url.URL) (*url.URL, map[string]string, error) {
	if r == nil {
		return nil, nil, nil
	}
	if target == nil {
		return nil, nil, nil
	}
	policy := r.cfg.ResolveProxyPolicyForAccount(accountEmail)
	mode := normalizeProxyMode(policy.Mode)
	if mode == "" {
		mode = proxyModeOff
	}
	headers := map[string]string{}
	switch mode {
	case proxyModeOff:
		return nil, nil, nil
	case proxyModeEnv, proxyModeHTTP, proxyModeHTTPS, proxyModeSOCKS5:
		raw := policy.proxyURLForScheme(target.Scheme)
		if strings.TrimSpace(raw) == "" {
			return nil, nil, nil
		}
		parsed, err := parseProxyURL(raw)
		if err != nil {
			return nil, nil, err
		}
		return parsed, nil, nil
	case proxyModeResinForward:
		proxyURL, stickyAccount, err := resolveResinForwardProxyURL(policy, accountEmail, r.cfg)
		if err != nil {
			return nil, nil, err
		}
		if stickyAccount != "" {
			headers[policy.Resin.AccountHeader] = stickyAccount
		}
		if len(headers) == 0 {
			return proxyURL, nil, nil
		}
		return proxyURL, headers, nil
	default:
		return nil, nil, nil
	}
}

func parseProxyURL(raw string) (*url.URL, error) {
	clean := strings.TrimSpace(raw)
	if clean == "" {
		return nil, nil
	}
	parsed, err := url.Parse(clean)
	if err != nil {
		return nil, fmt.Errorf("parse proxy url %q: %w", clean, err)
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	switch scheme {
	case "http", "https", "socks5", "socks5h":
		return parsed, nil
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q", parsed.Scheme)
	}
}

func resolveResinForwardProxyURL(policy ProxyPolicy, email string, cfg AppConfig) (*url.URL, string, error) {
	if !policy.Resin.Enabled {
		return nil, "", nil
	}
	baseURL, token, err := splitResinURL(policy.Resin.URL)
	if err != nil {
		return nil, "", err
	}
	if token == "" {
		return nil, "", fmt.Errorf("resin token missing")
	}
	platform := strings.TrimSpace(policy.Resin.Platform)
	if platform == "" {
		platform = "Default"
	}
	stickyAccount := resinStickyAccountForEmail(cfg, email)
	if stickyAccount == "" {
		stickyAccount = "account"
	}
	username := fmt.Sprintf("%s.%s", platform, stickyAccount)
	proxyURL := *baseURL
	proxyURL.User = url.UserPassword(username, token)
	return &proxyURL, stickyAccount, nil
}

func splitResinURL(raw string) (*url.URL, string, error) {
	parsed, err := parseProxyURL(raw)
	if err != nil {
		return nil, "", err
	}
	token := strings.Trim(strings.TrimSpace(parsed.Path), "/")
	if token == "" && parsed.User != nil {
		token, _ = parsed.User.Password()
	}
	baseURL := *parsed
	baseURL.Path = ""
	baseURL.RawPath = ""
	baseURL.User = nil
	baseURL.RawQuery = ""
	baseURL.Fragment = ""
	return &baseURL, token, nil
}

func resinStickyAccountForEmail(cfg AppConfig, email string) string {
	if account, _, ok := cfg.FindAccount(email); ok {
		if value := strings.TrimSpace(account.StickyProxyAccount); value != "" {
			return value
		}
		if value := accountPathSlug(account.Email); value != "" {
			return value
		}
	}
	return accountPathSlug(email)
}
