package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	defaultStreamingRequestTimeoutSec  = 900
	dispatchProtocolProbeTimeoutCapSec = 20
)

func requestTimeout(cfg AppConfig) time.Duration {
	return time.Duration(maxInt(cfg.TimeoutSec, 10)) * time.Second
}

func streamRequestTimeout(cfg AppConfig) time.Duration {
	return time.Duration(maxInt(cfg.TimeoutSec, defaultStreamingRequestTimeoutSec)) * time.Second
}

func noEligibleAccountsError() error {
	return fmt.Errorf("no usable accounts available; check disabled state, local artifacts, or login status")
}

func mergeDispatchCandidates(preferred *NotionAccount, candidates []NotionAccount) []NotionAccount {
	out := make([]NotionAccount, 0, len(candidates)+1)
	seen := map[string]struct{}{}
	appendCandidate := func(account NotionAccount) {
		key := canonicalEmailKey(account.Email)
		if key == "" {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, account)
	}
	if preferred != nil {
		appendCandidate(*preferred)
	}
	for _, account := range candidates {
		appendCandidate(account)
	}
	return out
}

func resolveDispatchCandidates(cfg AppConfig, request PromptRunRequest, now time.Time) ([]NotionAccount, error) {
	pinnedEmail := strings.TrimSpace(request.PinnedAccountEmail)
	poolCandidates := pickDispatchCandidates(cfg, now)
	if pinnedEmail == "" {
		if len(poolCandidates) == 0 {
			return nil, noEligibleAccountsError()
		}
		return poolCandidates, nil
	}
	if request.AllowPinnedAccountFallback {
		var preferred *NotionAccount
		if account, _, ok := cfg.FindAccount(pinnedEmail); ok {
			account = ensureAccountPaths(cfg, account)
			if eligible, _ := accountDispatchEligible(cfg, account, now); eligible {
				preferred = &account
			}
		}
		candidates := mergeDispatchCandidates(preferred, poolCandidates)
		if len(candidates) == 0 {
			return nil, noEligibleAccountsError()
		}
		return candidates, nil
	}
	account, _, ok := cfg.FindAccount(pinnedEmail)
	if !ok {
		return nil, fmt.Errorf("account %s not found", pinnedEmail)
	}
	account = ensureAccountPaths(cfg, account)
	if account.Disabled {
		return nil, fmt.Errorf("account %s is disabled", account.Email)
	}
	if !accountHasUsableArtifacts(cfg, account) {
		return nil, fmt.Errorf("account %s has no usable probe/storage artifacts", account.Email)
	}
	return []NotionAccount{account}, nil
}

func shouldPersistDispatchedAccountAsActive(cfg AppConfig, request PromptRunRequest, accountEmail string) bool {
	accountKey := canonicalEmailKey(accountEmail)
	if accountKey == "" {
		return false
	}
	activeKey := canonicalEmailKey(cfg.ActiveAccount)
	if activeKey == "" {
		return true
	}
	if _, _, ok := cfg.ResolveActiveAccount(); !ok {
		return true
	}
	return activeKey == accountKey
}

func dispatchProtocolProbeTimeout(cfg AppConfig) time.Duration {
	seconds := maxInt(minInt(cfg.TimeoutSec, dispatchProtocolProbeTimeoutCapSec), 5)
	return time.Duration(seconds) * time.Second
}

func isDispatchContextAbort(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return ctx != nil && ctx.Err() != nil
}

func (a *App) probeAccountProtocolHealth(ctx context.Context, cfg AppConfig, session SessionInfo) error {
	if a.accountProtocolProbeOverride != nil {
		return a.accountProtocolProbeOverride(ctx, cfg, session)
	}
	probeCtx, cancel := context.WithTimeout(ctx, dispatchProtocolProbeTimeout(cfg))
	defer cancel()
	client := newNotionAIClient(session, cfg, "")
	_, err := client.listInferenceTranscripts(probeCtx)
	if isDispatchContextAbort(probeCtx, err) {
		return nil
	}
	return err
}

func (a *App) loadReadyDispatchSession(ctx context.Context, cfg AppConfig, account NotionAccount) (SessionInfo, error) {
	session, err := loadSessionInfoForAccountRefresh(cfg, account)
	if err != nil {
		return SessionInfo{}, err
	}
	if err := a.probeAccountProtocolHealth(ctx, cfg, session); err != nil {
		return SessionInfo{}, err
	}
	return session, nil
}

func (a *App) loadPrimarySession(ctx context.Context, cfg AppConfig, snapshot SessionInfo, refreshReason string) (SessionInfo, error) {
	if strings.TrimSpace(snapshot.UserID) != "" && strings.TrimSpace(snapshot.SpaceID) != "" && len(snapshot.Cookies) > 0 {
		return snapshot, nil
	}
	if cfg.ResolveSessionRefresh().Enabled {
		if refreshErr := a.State.RefreshSession(ctx, refreshReason); refreshErr == nil {
			_, refreshed, _ := a.State.Snapshot()
			if strings.TrimSpace(refreshed.UserID) != "" && strings.TrimSpace(refreshed.SpaceID) != "" && len(refreshed.Cookies) > 0 {
				return refreshed, nil
			}
		}
	}
	probePath, userName, spaceName, _ := cfg.ResolveSessionTarget()
	if strings.TrimSpace(probePath) == "" {
		return SessionInfo{}, fmt.Errorf("no active notion session configured; login or activate an account first")
	}
	return loadSessionInfo(probePath, userName, spaceName)
}

func (a *App) runPromptActiveFallback(r *http.Request, request PromptRunRequest, onDelta func(string) error) (InferenceResult, error) {
	cfg, snapshotSession, _ := a.State.Snapshot()
	timeout := requestTimeout(cfg)
	if onDelta != nil {
		timeout = streamRequestTimeout(cfg)
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	session, err := a.loadPrimarySession(ctx, cfg, snapshotSession, "client_missing_fallback")
	if err != nil {
		return InferenceResult{}, err
	}
	if err := a.probeAccountProtocolHealth(ctx, cfg, session); err != nil {
		return InferenceResult{}, err
	}

	emittedAny := false
	wrappedDelta := func(delta string) error {
		if delta != "" {
			emittedAny = true
		}
		if onDelta == nil {
			return nil
		}
		return onDelta(delta)
	}

	result, err := a.runPromptWithSession(ctx, cfg, session, "", request, wrappedDelta)
	if err == nil {
		return result, nil
	}
	if cfg.ResolveSessionRefresh().RetryOnAuthError && isSessionRetryableError(err) && !emittedAny {
		if refreshErr := a.State.RefreshSession(ctx, "prompt_retry_fallback"); refreshErr == nil {
			_, refreshed, _ := a.State.Snapshot()
			if strings.TrimSpace(refreshed.UserID) != "" && strings.TrimSpace(refreshed.SpaceID) != "" && len(refreshed.Cookies) > 0 {
				if probeErr := a.probeAccountProtocolHealth(ctx, cfg, refreshed); probeErr != nil {
					return InferenceResult{}, probeErr
				}
				return a.runPromptWithSession(ctx, cfg, refreshed, "", request, wrappedDelta)
			}
		}
	}
	return InferenceResult{}, err
}

func (a *App) runPromptActiveFallbackWithSink(r *http.Request, request PromptRunRequest, sink InferenceStreamSink) (InferenceResult, error) {
	cfg, snapshotSession, _ := a.State.Snapshot()
	timeout := streamRequestTimeout(cfg)
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	session, err := a.loadPrimarySession(ctx, cfg, snapshotSession, "client_missing_fallback")
	if err != nil {
		return InferenceResult{}, err
	}
	if err := a.probeAccountProtocolHealth(ctx, cfg, session); err != nil {
		return InferenceResult{}, err
	}

	emittedAny := false
	wrappedText := func(delta string) error {
		if delta != "" {
			emittedAny = true
		}
		return sink.EmitText(delta)
	}
	wrappedReasoning := func(delta string) error {
		if delta != "" {
			emittedAny = true
		}
		return sink.EmitReasoning(delta)
	}
	wrappedReasoningWarmup := func() error {
		return sink.EmitReasoningWarmup()
	}
	wrappedKeepAlive := func() error {
		return sink.EmitKeepAlive()
	}

	result, err := a.runPromptWithSessionWithSink(ctx, cfg, session, "", request, InferenceStreamSink{
		Text:            wrappedText,
		Reasoning:       wrappedReasoning,
		ReasoningWarmup: wrappedReasoningWarmup,
		KeepAlive:       wrappedKeepAlive,
	})
	if err == nil {
		return result, nil
	}
	if cfg.ResolveSessionRefresh().RetryOnAuthError && isSessionRetryableError(err) && !emittedAny {
		if refreshErr := a.State.RefreshSession(ctx, "prompt_retry_fallback"); refreshErr == nil {
			_, refreshed, _ := a.State.Snapshot()
			if strings.TrimSpace(refreshed.UserID) != "" && strings.TrimSpace(refreshed.SpaceID) != "" && len(refreshed.Cookies) > 0 {
				if probeErr := a.probeAccountProtocolHealth(ctx, cfg, refreshed); probeErr != nil {
					return InferenceResult{}, probeErr
				}
				return a.runPromptWithSessionWithSink(ctx, cfg, refreshed, "", request, InferenceStreamSink{
					Text:            wrappedText,
					Reasoning:       wrappedReasoning,
					ReasoningWarmup: wrappedReasoningWarmup,
					KeepAlive:       wrappedKeepAlive,
				})
			}
		}
	}
	return InferenceResult{}, err
}

func (a *App) runPromptWithAccountPool(r *http.Request, request PromptRunRequest, onDelta func(string) error) (InferenceResult, error) {
	cfg, _, _ := a.State.Snapshot()
	if len(cfg.Accounts) == 0 {
		return a.runPromptActiveFallback(r, request, onDelta)
	}

	timeout := requestTimeout(cfg)
	if onDelta != nil {
		timeout = streamRequestTimeout(cfg)
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	now := time.Now()
	candidates, err := resolveDispatchCandidates(cfg, request, now)
	if err != nil {
		return InferenceResult{}, err
	}

	emittedAny := false
	wrappedDelta := func(delta string) error {
		if delta != "" {
			emittedAny = true
		}
		if onDelta == nil {
			return nil
		}
		return onDelta(delta)
	}

	var lastErr error
	for _, original := range candidates {
		account := markAccountDispatchStart(original, time.Now())
		session, err := a.loadReadyDispatchSession(ctx, cfg, account)
		if err == nil {
			result, runErr := a.runPromptWithSession(ctx, cfg, session, account.Email, request, wrappedDelta)
			if runErr == nil {
				result.AccountEmail = account.Email
				account.UserID = firstNonEmpty(session.UserID, account.UserID)
				account.UserName = firstNonEmpty(session.UserName, account.UserName)
				account.SpaceID = firstNonEmpty(session.SpaceID, account.SpaceID)
				account.SpaceViewID = firstNonEmpty(session.SpaceViewID, account.SpaceViewID)
				account.SpaceName = firstNonEmpty(session.SpaceName, account.SpaceName)
				account.ClientVersion = firstNonEmpty(session.ClientVersion, account.ClientVersion)
				account = markAccountDispatchSuccess(account, time.Now())
				nextCfg := applyAccountUpdate(cfg, account, shouldPersistDispatchedAccountAsActive(cfg, request, account.Email))
				if saveErr := a.State.SaveAndApply(nextCfg); saveErr != nil {
					return InferenceResult{}, saveErr
				}
				return result, nil
			}
			err = runErr
		}
		if isDispatchContextAbort(ctx, err) {
			return InferenceResult{}, err
		}

		retryable := isSessionRetryableError(err)
		if retryable && cfg.ResolveSessionRefresh().Enabled && !emittedAny {
			refreshedCfg, refreshErr := a.State.tryRefreshAccount(ctx, cfg, account)
			if refreshErr == nil {
				if saveErr := a.State.SaveAndApply(refreshedCfg); saveErr == nil {
					cfg = refreshedCfg
					refreshedAccount, _, ok := cfg.FindAccount(account.Email)
					if ok {
						refreshedSession, loadErr := a.loadReadyDispatchSession(ctx, cfg, refreshedAccount)
						if loadErr == nil {
							result, retryErr := a.runPromptWithSession(ctx, cfg, refreshedSession, refreshedAccount.Email, request, wrappedDelta)
							if retryErr == nil {
								result.AccountEmail = refreshedAccount.Email
								refreshedAccount.UserID = firstNonEmpty(refreshedSession.UserID, refreshedAccount.UserID)
								refreshedAccount.UserName = firstNonEmpty(refreshedSession.UserName, refreshedAccount.UserName)
								refreshedAccount.SpaceID = firstNonEmpty(refreshedSession.SpaceID, refreshedAccount.SpaceID)
								refreshedAccount.SpaceViewID = firstNonEmpty(refreshedSession.SpaceViewID, refreshedAccount.SpaceViewID)
								refreshedAccount.SpaceName = firstNonEmpty(refreshedSession.SpaceName, refreshedAccount.SpaceName)
								refreshedAccount.ClientVersion = firstNonEmpty(refreshedSession.ClientVersion, refreshedAccount.ClientVersion)
								refreshedAccount = markAccountDispatchSuccess(refreshedAccount, time.Now())
								nextCfg := applyAccountUpdate(cfg, refreshedAccount, shouldPersistDispatchedAccountAsActive(cfg, request, refreshedAccount.Email))
								if saveErr := a.State.SaveAndApply(nextCfg); saveErr != nil {
									return InferenceResult{}, saveErr
								}
								return result, nil
							}
							err = retryErr
							retryable = isSessionRetryableError(err)
						} else {
							err = loadErr
							retryable = isSessionRetryableError(err)
						}
					}
				} else {
					err = saveErr
					retryable = isSessionRetryableError(err)
				}
			}
		}
		if isDispatchContextAbort(ctx, err) {
			return InferenceResult{}, err
		}

		if retryable {
			reloginCfg, _ := a.State.startAutoRelogin(ctx, cfg, account, "request_auth_failed")
			cfg = reloginCfg
			if updated, _, ok := cfg.FindAccount(account.Email); ok {
				account = updated
			}
		}

		account = markAccountDispatchFailure(account, time.Now(), err, retryable)
		cfg = applyAccountUpdate(cfg, account, false)
		_ = a.State.SaveAndApply(cfg)
		lastErr = fmt.Errorf("%s: %w", account.Email, err)
		if emittedAny {
			return InferenceResult{}, lastErr
		}
	}

	if lastErr != nil {
		return InferenceResult{}, lastErr
	}
	return InferenceResult{}, noEligibleAccountsError()
}

func (a *App) runPromptWithAccountPoolWithSink(r *http.Request, request PromptRunRequest, sink InferenceStreamSink) (InferenceResult, error) {
	cfg, _, _ := a.State.Snapshot()
	if len(cfg.Accounts) == 0 {
		return a.runPromptActiveFallbackWithSink(r, request, sink)
	}

	timeout := streamRequestTimeout(cfg)
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	now := time.Now()
	candidates, err := resolveDispatchCandidates(cfg, request, now)
	if err != nil {
		return InferenceResult{}, err
	}

	emittedAny := false
	wrappedText := func(delta string) error {
		if delta != "" {
			emittedAny = true
		}
		return sink.EmitText(delta)
	}
	wrappedReasoning := func(delta string) error {
		if delta != "" {
			emittedAny = true
		}
		return sink.EmitReasoning(delta)
	}
	wrappedReasoningWarmup := func() error {
		return sink.EmitReasoningWarmup()
	}
	wrappedKeepAlive := func() error {
		return sink.EmitKeepAlive()
	}

	var lastErr error
	for _, original := range candidates {
		account := markAccountDispatchStart(original, time.Now())
		session, err := a.loadReadyDispatchSession(ctx, cfg, account)
		if err == nil {
			result, runErr := a.runPromptWithSessionWithSink(ctx, cfg, session, account.Email, request, InferenceStreamSink{
				Text:            wrappedText,
				Reasoning:       wrappedReasoning,
				ReasoningWarmup: wrappedReasoningWarmup,
				KeepAlive:       wrappedKeepAlive,
			})
			if runErr == nil {
				result.AccountEmail = account.Email
				account.UserID = firstNonEmpty(session.UserID, account.UserID)
				account.UserName = firstNonEmpty(session.UserName, account.UserName)
				account.SpaceID = firstNonEmpty(session.SpaceID, account.SpaceID)
				account.SpaceViewID = firstNonEmpty(session.SpaceViewID, account.SpaceViewID)
				account.SpaceName = firstNonEmpty(session.SpaceName, account.SpaceName)
				account.ClientVersion = firstNonEmpty(session.ClientVersion, account.ClientVersion)
				account = markAccountDispatchSuccess(account, time.Now())
				nextCfg := applyAccountUpdate(cfg, account, shouldPersistDispatchedAccountAsActive(cfg, request, account.Email))
				if saveErr := a.State.SaveAndApply(nextCfg); saveErr != nil {
					return InferenceResult{}, saveErr
				}
				return result, nil
			}
			err = runErr
		}
		if isDispatchContextAbort(ctx, err) {
			return InferenceResult{}, err
		}

		retryable := isSessionRetryableError(err)
		if retryable && cfg.ResolveSessionRefresh().Enabled && !emittedAny {
			refreshedCfg, refreshErr := a.State.tryRefreshAccount(ctx, cfg, account)
			if refreshErr == nil {
				if saveErr := a.State.SaveAndApply(refreshedCfg); saveErr == nil {
					cfg = refreshedCfg
					if refreshedAccount, _, ok := cfg.FindAccount(account.Email); ok {
						refreshedSession, loadErr := a.loadReadyDispatchSession(ctx, cfg, refreshedAccount)
						if loadErr == nil {
							result, retryErr := a.runPromptWithSessionWithSink(ctx, cfg, refreshedSession, refreshedAccount.Email, request, InferenceStreamSink{
								Text:            wrappedText,
								Reasoning:       wrappedReasoning,
								ReasoningWarmup: wrappedReasoningWarmup,
								KeepAlive:       wrappedKeepAlive,
							})
							if retryErr == nil {
								result.AccountEmail = refreshedAccount.Email
								refreshedAccount.UserID = firstNonEmpty(refreshedSession.UserID, refreshedAccount.UserID)
								refreshedAccount.UserName = firstNonEmpty(refreshedSession.UserName, refreshedAccount.UserName)
								refreshedAccount.SpaceID = firstNonEmpty(refreshedSession.SpaceID, refreshedAccount.SpaceID)
								refreshedAccount.SpaceViewID = firstNonEmpty(refreshedSession.SpaceViewID, refreshedAccount.SpaceViewID)
								refreshedAccount.SpaceName = firstNonEmpty(refreshedSession.SpaceName, refreshedAccount.SpaceName)
								refreshedAccount.ClientVersion = firstNonEmpty(refreshedSession.ClientVersion, refreshedAccount.ClientVersion)
								refreshedAccount = markAccountDispatchSuccess(refreshedAccount, time.Now())
								nextCfg := applyAccountUpdate(cfg, refreshedAccount, shouldPersistDispatchedAccountAsActive(cfg, request, refreshedAccount.Email))
								if saveErr := a.State.SaveAndApply(nextCfg); saveErr != nil {
									return InferenceResult{}, saveErr
								}
								return result, nil
							}
							err = retryErr
							retryable = isSessionRetryableError(err)
						} else {
							err = loadErr
							retryable = isSessionRetryableError(err)
						}
					}
				} else {
					err = saveErr
					retryable = isSessionRetryableError(err)
				}
			}
		}
		if isDispatchContextAbort(ctx, err) {
			return InferenceResult{}, err
		}

		if retryable {
			reloginCfg, _ := a.State.startAutoRelogin(ctx, cfg, account, "request_auth_failed")
			cfg = reloginCfg
			if updated, _, ok := cfg.FindAccount(account.Email); ok {
				account = updated
			}
		}

		account = markAccountDispatchFailure(account, time.Now(), err, retryable)
		cfg = applyAccountUpdate(cfg, account, false)
		_ = a.State.SaveAndApply(cfg)
		lastErr = fmt.Errorf("%s: %w", account.Email, err)
		if emittedAny {
			return InferenceResult{}, lastErr
		}
	}

	if lastErr != nil {
		return InferenceResult{}, lastErr
	}
	return InferenceResult{}, noEligibleAccountsError()
}
