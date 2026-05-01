package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	accountCooldownBase        = 2 * time.Minute
	accountCooldownMax         = 30 * time.Minute
	accountAutoReloginInterval = 5 * time.Minute
)

func parseOptionalRFC3339(value string) time.Time {
	clean := strings.TrimSpace(value)
	if clean == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, clean)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func formatRFC3339OrEmpty(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339)
}

func resetAccountUsageWindow(account *NotionAccount, now time.Time) {
	if account == nil || account.HourlyQuota <= 0 {
		account.WindowStartedAt = ""
		account.WindowRequestCount = 0
		return
	}
	startedAt := parseOptionalRFC3339(account.WindowStartedAt)
	if startedAt.IsZero() || now.Sub(startedAt) >= time.Hour {
		account.WindowStartedAt = formatRFC3339OrEmpty(now)
		account.WindowRequestCount = 0
	}
}

func accountRemainingQuota(account NotionAccount, now time.Time) (int, bool) {
	if account.HourlyQuota <= 0 {
		return 0, false
	}
	resetAccountUsageWindow(&account, now)
	remaining := account.HourlyQuota - account.WindowRequestCount
	if remaining < 0 {
		remaining = 0
	}
	return remaining, true
}

func accountCooldownActive(account NotionAccount, now time.Time) bool {
	until := parseOptionalRFC3339(account.CooldownUntil)
	return !until.IsZero() && now.Before(until)
}

func accountHasUsableArtifacts(cfg AppConfig, account NotionAccount) bool {
	account = ensureAccountPaths(cfg, account)
	return fileExists(account.ProbeJSON) || fileExists(account.StorageStatePath)
}

func accountDispatchEligible(cfg AppConfig, account NotionAccount, now time.Time) (bool, string) {
	account = ensureAccountPaths(cfg, account)
	_ = now
	if account.Disabled {
		return false, "disabled"
	}
	if !accountHasUsableArtifacts(cfg, account) {
		return false, "missing_artifacts"
	}
	return true, "ready"
}

func computeAccountCooldown(account NotionAccount, retryable bool) time.Duration {
	failures := account.ConsecutiveFailures
	if failures < 1 {
		failures = 1
	}
	wait := time.Duration(failures) * accountCooldownBase
	if !retryable {
		wait /= 2
	}
	if wait < 30*time.Second {
		wait = 30 * time.Second
	}
	if wait > accountCooldownMax {
		wait = accountCooldownMax
	}
	return wait
}

func markAccountDispatchStart(account NotionAccount, now time.Time) NotionAccount {
	resetAccountUsageWindow(&account, now)
	if account.HourlyQuota > 0 {
		if strings.TrimSpace(account.WindowStartedAt) == "" {
			account.WindowStartedAt = formatRFC3339OrEmpty(now)
		}
		account.WindowRequestCount++
	}
	account.LastUsedAt = formatRFC3339OrEmpty(now)
	if strings.TrimSpace(account.Status) == "" || strings.EqualFold(account.Status, "new") {
		account.Status = "ready"
	}
	return account
}

func markAccountDispatchSuccess(account NotionAccount, now time.Time) NotionAccount {
	account.Status = "ready"
	account.LastError = ""
	account.LastUsedAt = formatRFC3339OrEmpty(now)
	account.LastSuccessAt = formatRFC3339OrEmpty(now)
	account.CooldownUntil = ""
	account.ConsecutiveFailures = 0
	account.TotalSuccesses++
	return account
}

func markAccountDispatchFailure(account NotionAccount, now time.Time, err error, retryable bool) NotionAccount {
	account.TotalFailures++
	account.ConsecutiveFailures++
	account.LastUsedAt = formatRFC3339OrEmpty(now)
	account.LastError = strings.TrimSpace(err.Error())
	if !strings.EqualFold(strings.TrimSpace(account.Status), "pending_code") {
		if retryable {
			account.Status = "expired"
		} else {
			account.Status = "failed"
		}
	}
	account.CooldownUntil = ""
	return account
}

func markAccountReloginPending(account NotionAccount, now time.Time) NotionAccount {
	account.Status = "pending_code"
	account.LastReloginAt = formatRFC3339OrEmpty(now)
	return account
}

func accountReloginRecentlyStarted(account NotionAccount, now time.Time) bool {
	last := parseOptionalRFC3339(account.LastReloginAt)
	return !last.IsZero() && now.Sub(last) < accountAutoReloginInterval
}

func sortDispatchCandidates(cfg AppConfig, accounts []NotionAccount, now time.Time) {
	activeKey := canonicalEmailKey(cfg.ActiveAccount)
	sort.Slice(accounts, func(i, j int) bool {
		left := accounts[i]
		right := accounts[j]
		leftActive := canonicalEmailKey(left.Email) == activeKey
		rightActive := canonicalEmailKey(right.Email) == activeKey
		if leftActive != rightActive {
			return leftActive
		}
		if left.Priority != right.Priority {
			return left.Priority > right.Priority
		}
		leftRemaining, leftLimited := accountRemainingQuota(left, now)
		rightRemaining, rightLimited := accountRemainingQuota(right, now)
		if leftLimited != rightLimited {
			return !leftLimited
		}
		if leftLimited && rightLimited && leftRemaining != rightRemaining {
			return leftRemaining > rightRemaining
		}
		if left.ConsecutiveFailures != right.ConsecutiveFailures {
			return left.ConsecutiveFailures < right.ConsecutiveFailures
		}
		leftUsed := parseOptionalRFC3339(left.LastUsedAt)
		rightUsed := parseOptionalRFC3339(right.LastUsedAt)
		if leftUsed.IsZero() != rightUsed.IsZero() {
			return leftUsed.IsZero()
		}
		if !leftUsed.Equal(rightUsed) {
			return leftUsed.Before(rightUsed)
		}
		return canonicalEmailKey(left.Email) < canonicalEmailKey(right.Email)
	})
}

func pickDispatchCandidates(cfg AppConfig, now time.Time) []NotionAccount {
	candidates := make([]NotionAccount, 0, len(cfg.Accounts))
	for _, account := range cfg.Accounts {
		account = ensureAccountPaths(cfg, account)
		if ok, _ := accountDispatchEligible(cfg, account, now); ok {
			candidates = append(candidates, account)
		}
	}
	sortDispatchCandidates(cfg, candidates, now)
	return candidates
}

func applyAccountUpdate(cfg AppConfig, account NotionAccount, makeActive bool) AppConfig {
	account = ensureAccountPaths(cfg, account)
	cfg.UpsertAccount(account)
	if makeActive {
		cfg.ActiveAccount = account.Email
		cfg.ProbeJSON = account.ProbeJSON
	}
	return cfg
}

func (s *ServerState) startAutoRelogin(ctx context.Context, cfg AppConfig, account NotionAccount, reason string) (AppConfig, error) {
	now := time.Now()
	account = ensureAccountPaths(cfg, account)
	if strings.TrimSpace(account.Email) == "" {
		return cfg, fmt.Errorf("account email missing for auto relogin")
	}
	if accountReloginRecentlyStarted(account, now) {
		return cfg, fmt.Errorf("auto relogin already started recently for %s", account.Email)
	}
	status, err := StartEmailLogin(ctx, cfg, LoginStartRequest{
		Email:            account.Email,
		ProfileDir:       account.ProfileDir,
		PendingPath:      account.PendingStatePath,
		StorageStatePath: account.StorageStatePath,
		AccountEmail:     account.Email,
	})
	account = mergeAccountWithStatus(cfg, account, status)
	account = markAccountReloginPending(account, now)
	if err != nil {
		account.LastError = firstNonEmpty(status.Error, status.Message, err.Error())
		cfg = applyAccountUpdate(cfg, account, false)
		return cfg, fmt.Errorf("auto relogin start failed for %s (%s): %w", account.Email, reason, err)
	}
	account.LastError = ""
	cfg = applyAccountUpdate(cfg, account, false)
	return cfg, fmt.Errorf("verification code required for %s; auto relogin started (%s)", account.Email, reason)
}

func (a *App) runPromptWithSession(ctx context.Context, cfg AppConfig, session SessionInfo, accountEmail string, request PromptRunRequest, onDelta func(string) error) (InferenceResult, error) {
	if a.runPromptWithSessionOverride != nil {
		return a.runPromptWithSessionOverride(ctx, cfg, session, request, onDelta)
	}
	client := newNotionAIClient(session, cfg, accountEmail)
	if onDelta != nil {
		client = newNotionAIStreamingClient(session, cfg, accountEmail)
	}
	execute := func(ctx context.Context, current PromptRunRequest, forward func(string) error) (InferenceResult, error) {
		if forward == nil {
			return client.RunPrompt(ctx, current)
		}
		return client.RunPromptStream(ctx, current, forward)
	}
	return execute(ctx, request, onDelta)
}

func (a *App) runPromptWithSessionWithSink(ctx context.Context, cfg AppConfig, session SessionInfo, accountEmail string, request PromptRunRequest, sink InferenceStreamSink) (InferenceResult, error) {
	if a.runPromptWithSessionSinkOverride != nil {
		return a.runPromptWithSessionSinkOverride(ctx, cfg, session, request, sink)
	}
	if a.runPromptWithSessionOverride != nil {
		return a.runPromptWithSessionOverride(ctx, cfg, session, request, sink.Text)
	}
	client := newNotionAIStreamingClient(session, cfg, accountEmail)
	if sink.Text == nil && sink.Reasoning == nil && sink.ReasoningWarmup == nil && sink.KeepAlive == nil {
		client = newNotionAIClient(session, cfg, accountEmail)
	}
	if sink.Reasoning != nil || sink.ReasoningWarmup != nil || sink.KeepAlive != nil {
		return client.RunPromptStreamWithSink(ctx, request, sink)
	}
	execute := func(ctx context.Context, current PromptRunRequest, forward func(string) error) (InferenceResult, error) {
		if forward == nil {
			return client.RunPrompt(ctx, current)
		}
		return client.RunPromptStreamWithSink(ctx, current, InferenceStreamSink{
			Text:            forward,
			Reasoning:       sink.Reasoning,
			ReasoningWarmup: sink.ReasoningWarmup,
			KeepAlive:       sink.KeepAlive,
		})
	}
	return execute(ctx, request, sink.Text)
}
