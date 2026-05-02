package app

import "strings"

const (
	defaultResinAccountHeader = "X-Resin-Account"
	resinModeForward          = "forward"
)

type ResinPolicy struct {
	Enabled       bool
	URL           string
	Platform      string
	Mode          string
	AccountHeader string
}

type ProxyPolicy struct {
	Mode     string
	URL      string
	HTTPURL  string
	HTTPSURL string
	Resin    ResinPolicy
}

func normalizeResinMode(raw string) string {
	mode := strings.ToLower(strings.TrimSpace(raw))
	switch mode {
	case "", resinModeForward:
		return mode
	default:
		return mode
	}
}

func (cfg AppConfig) ResolveProxyPolicy() ProxyPolicy {
	mode := cfg.normalizedProxyMode()
	if mode == proxyModeOff && cfg.ResinEnabled {
		mode = proxyModeResinForward
	}
	policy := ProxyPolicy{
		Mode:     mode,
		URL:      strings.TrimSpace(cfg.ProxyURL),
		HTTPURL:  strings.TrimSpace(cfg.ProxyHTTPURL),
		HTTPSURL: strings.TrimSpace(cfg.ProxyHTTPSURL),
		Resin: ResinPolicy{
			Enabled:       cfg.ResinEnabled,
			URL:           strings.TrimSpace(cfg.ResinURL),
			Platform:      strings.TrimSpace(cfg.ResinPlatform),
			Mode:          normalizeResinMode(cfg.ResinMode),
			AccountHeader: defaultResinAccountHeader,
		},
	}
	if policy.Resin.Mode == "" {
		policy.Resin.Mode = resinModeForward
	}
	if policy.Mode == proxyModeResinForward {
		policy.Resin.Enabled = true
	}
	if policy.Mode == proxyModeEnv {
		policy.HTTPURL = firstNonEmpty(resolveProxyURLForSchemeFromEnv("http"), policy.HTTPURL, policy.URL)
		policy.HTTPSURL = firstNonEmpty(resolveProxyURLForSchemeFromEnv("https"), policy.HTTPSURL, policy.URL)
	} else {
		policy.HTTPURL = firstNonEmpty(policy.HTTPURL, policy.URL)
		policy.HTTPSURL = firstNonEmpty(policy.HTTPSURL, policy.URL)
	}
	return policy
}

func (cfg AppConfig) ResolveProxyPolicyForAccount(email string) ProxyPolicy {
	policy := cfg.ResolveProxyPolicy()
	account, _, ok := cfg.FindAccount(email)
	if !ok {
		return policy
	}
	if mode := normalizeProxyMode(account.ProxyMode); mode != "" {
		policy.Mode = mode
	}
	if value := strings.TrimSpace(account.ProxyURL); value != "" {
		policy.URL = value
	}
	if value := strings.TrimSpace(account.ProxyHTTPURL); value != "" {
		policy.HTTPURL = value
	}
	if value := strings.TrimSpace(account.ProxyHTTPSURL); value != "" {
		policy.HTTPSURL = value
	}
	if account.ResinEnabled {
		policy.Resin.Enabled = true
	}
	if value := strings.TrimSpace(account.ResinURL); value != "" {
		policy.Resin.URL = value
	}
	if value := strings.TrimSpace(account.ResinPlatform); value != "" {
		policy.Resin.Platform = value
	}
	if value := normalizeResinMode(account.ResinMode); value != "" {
		policy.Resin.Mode = value
	}
	if policy.Resin.Mode == "" {
		policy.Resin.Mode = resinModeForward
	}
	if policy.Mode == proxyModeResinForward {
		policy.Resin.Enabled = true
	}
	if policy.Mode == proxyModeEnv {
		policy.HTTPURL = firstNonEmpty(resolveProxyURLForSchemeFromEnv("http"), policy.HTTPURL, policy.URL)
		policy.HTTPSURL = firstNonEmpty(resolveProxyURLForSchemeFromEnv("https"), policy.HTTPSURL, policy.URL)
	} else {
		policy.HTTPURL = firstNonEmpty(policy.HTTPURL, policy.URL)
		policy.HTTPSURL = firstNonEmpty(policy.HTTPSURL, policy.URL)
	}
	return policy
}

func (p ProxyPolicy) proxyURLForScheme(scheme string) string {
	if strings.EqualFold(strings.TrimSpace(scheme), "https") {
		return strings.TrimSpace(firstNonEmpty(p.HTTPSURL, p.URL))
	}
	return strings.TrimSpace(firstNonEmpty(p.HTTPURL, p.URL))
}
