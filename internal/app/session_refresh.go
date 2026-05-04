package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

var (
	testHookTryRefreshAccount func(context.Context, AppConfig, NotionAccount) (AppConfig, error)
	testHookSaveAndApply      func(*ServerState, AppConfig) error
)

func sessionRefreshNowISO() string {
	return time.Now().Format(time.RFC3339)
}

func isSessionRetryableError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *notionAPIError
	if errors.As(err, &apiErr) {
		if apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden {
			return true
		}
		message := strings.ToLower(strings.TrimSpace(apiErr.Message))
		return strings.Contains(message, "client version") ||
			strings.Contains(message, "notion-client-version") ||
			strings.Contains(message, "session") ||
			strings.Contains(message, "login")
	}
	var loginErr *notionLoginAPIError
	if errors.As(err, &loginErr) {
		return loginErr.StatusCode == http.StatusUnauthorized || loginErr.StatusCode == http.StatusForbidden
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(message, "notion_user_id missing") ||
		strings.Contains(message, "cookie jar empty") ||
		strings.Contains(message, "unauthorized") ||
		strings.Contains(message, "forbidden") ||
		strings.Contains(message, "session expired") ||
		strings.Contains(message, "client version")
}

func loadSessionInfoForAccountRefresh(cfg AppConfig, account NotionAccount) (SessionInfo, error) {
	account = ensureAccountPaths(cfg, account)
	session, err := loadSessionInfo(account.ProbeJSON, firstNonEmpty(account.UserName, cfg.UserName), firstNonEmpty(account.SpaceName, cfg.SpaceName))
	if err == nil {
		return session, nil
	}
	storage, storageErr := readLoginStorageState(account.StorageStatePath)
	if storageErr != nil {
		return SessionInfo{}, err
	}
	if len(storage.Cookies) == 0 {
		return SessionInfo{}, err
	}
	userName := strings.TrimSpace(account.UserName)
	if userName == "" {
		userName = accountPathSlug(firstNonEmpty(storage.Email, account.Email))
	}
	spaceName := strings.TrimSpace(account.SpaceName)
	if spaceName == "" {
		spaceName = userName + "'s Space"
	}
	if strings.TrimSpace(account.UserID) == "" || strings.TrimSpace(account.SpaceID) == "" {
		return SessionInfo{}, err
	}
	return SessionInfo{
		ProbePath:     account.ProbeJSON,
		ClientVersion: firstNonEmpty(storage.ClientVersion, account.ClientVersion),
		UserID:        strings.TrimSpace(account.UserID),
		UserEmail:     firstNonEmpty(storage.Email, account.Email),
		UserName:      userName,
		SpaceID:       strings.TrimSpace(account.SpaceID),
		SpaceViewID:   strings.TrimSpace(account.SpaceViewID),
		SpaceName:     spaceName,
		Cookies:       storage.Cookies,
	}, nil
}

func buildRefreshedSession(ctx context.Context, cfg AppConfig, account NotionAccount, prior SessionInfo) (SessionInfo, error) {
	upstream := cfg.NotionUpstream()
	resolver := NewProxyResolver(cfg)
	session, err := newNotionLoginSession(helperTimeout(cfg), upstream, resolver, account.Email, cfg)
	if err != nil {
		return SessionInfo{}, err
	}
	restoreProbeCookies(session.Jar, upstream.HomeURL(), prior.Cookies)
	restoreProbeCookies(session.Jar, upstream.LoginURL(), prior.Cookies)

	bootstrap, err := fetchLoginBootstrap(ctx, session, upstream)
	if err != nil {
		return SessionInfo{}, err
	}
	clientVersion := firstNonEmpty(bootstrap.ClientVersion, prior.ClientVersion, account.ClientVersion)
	userID := firstNonEmpty(
		prior.UserID,
		account.UserID,
		probeCookieValue(probeCookiesFromJar(session.Jar, upstream.HomeURL()), "notion_user_id"),
		probeCookieValue(probeCookiesFromJar(session.Jar, upstream.LoginURL()), "notion_user_id"),
	)
	if userID == "" {
		return SessionInfo{}, fmt.Errorf("notion_user_id missing during session refresh")
	}
	spaces, err := getSpacesInitial(ctx, session, upstream, clientVersion, userID)
	if err != nil {
		return SessionInfo{}, err
	}
	cookies := probeCookiesFromJar(session.Jar, upstream.HomeURL())
	if len(cookies) == 0 {
		cookies = probeCookiesFromJar(session.Jar, upstream.LoginURL())
	}
	if len(cookies) == 0 {
		return SessionInfo{}, fmt.Errorf("cookie jar empty after session refresh")
	}
	userName := firstNonEmpty(spaces.UserName, prior.UserName, account.UserName)
	if userName == "" {
		userName = accountPathSlug(firstNonEmpty(spaces.Email, prior.UserEmail, account.Email))
	}
	spaceName := firstNonEmpty(prior.SpaceName, account.SpaceName)
	if spaceName == "" {
		spaceName = userName + "'s Space"
	}
	return SessionInfo{
		ProbePath:     account.ProbeJSON,
		ClientVersion: clientVersion,
		UserID:        userID,
		UserEmail:     firstNonEmpty(spaces.Email, prior.UserEmail, account.Email),
		UserName:      userName,
		SpaceID:       firstNonEmpty(spaces.SpaceID, prior.SpaceID, account.SpaceID),
		SpaceViewID:   firstNonEmpty(spaces.SpaceViewID, prior.SpaceViewID, account.SpaceViewID),
		SpaceName:     spaceName,
		Cookies:       cookies,
	}, nil
}

func writeSessionArtifacts(account NotionAccount, session SessionInfo) error {
	if strings.TrimSpace(account.StorageStatePath) != "" {
		if err := writeLoginStorageState(account.StorageStatePath, loginStorageState{
			Email:         session.UserEmail,
			ClientVersion: session.ClientVersion,
			Cookies:       session.Cookies,
		}); err != nil {
			return err
		}
	}
	if strings.TrimSpace(account.ProbeJSON) != "" {
		if err := writePrettyJSONFile(account.ProbeJSON, probePayload{
			Email:         session.UserEmail,
			UserID:        session.UserID,
			UserName:      session.UserName,
			SpaceID:       session.SpaceID,
			SpaceViewID:   session.SpaceViewID,
			SpaceName:     session.SpaceName,
			ClientVersion: session.ClientVersion,
			Cookies:       session.Cookies,
		}); err != nil {
			return err
		}
	}
	status := loginPendingState{
		LoginStatusFile: LoginStatusFile{
			Success:          true,
			Status:           "ready",
			Email:            session.UserEmail,
			ProfileDir:       account.ProfileDir,
			PendingStatePath: account.PendingStatePath,
			StorageStatePath: account.StorageStatePath,
			ProbePath:        account.ProbeJSON,
			UserID:           session.UserID,
			UserName:         session.UserName,
			SpaceID:          session.SpaceID,
			SpaceViewID:      session.SpaceViewID,
			SpaceName:        session.SpaceName,
			ClientVersion:    session.ClientVersion,
			Title:            "Notion",
			Message:          "session refreshed",
			UpdatedAt:        sessionRefreshNowISO(),
			LastLoginAt:      sessionRefreshNowISO(),
		},
	}
	return writeLoginPendingState(account.PendingStatePath, status)
}

func writeSessionRefreshFailure(account NotionAccount, err error) {
	if err == nil {
		return
	}
	status := loginPendingState{
		LoginStatusFile: LoginStatusFile{
			Success:          false,
			Status:           "expired",
			Email:            account.Email,
			ProfileDir:       account.ProfileDir,
			PendingStatePath: account.PendingStatePath,
			StorageStatePath: account.StorageStatePath,
			ProbePath:        account.ProbeJSON,
			UserID:           account.UserID,
			UserName:         account.UserName,
			SpaceID:          account.SpaceID,
			SpaceViewID:      account.SpaceViewID,
			SpaceName:        account.SpaceName,
			ClientVersion:    account.ClientVersion,
			Message:          strings.TrimSpace(err.Error()),
			Error:            strings.TrimSpace(err.Error()),
			UpdatedAt:        sessionRefreshNowISO(),
			LastLoginAt:      account.LastLoginAt,
		},
	}
	_ = writeLoginPendingState(account.PendingStatePath, status)
}

func (s *ServerState) setSessionRefreshRuntime(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LastSessionRefresh = time.Now()
	if err != nil {
		s.LastSessionRefreshError = strings.TrimSpace(err.Error())
		return
	}
	s.LastSessionRefreshError = ""
}

func (s *ServerState) tryRefreshAccount(ctx context.Context, cfg AppConfig, account NotionAccount) (AppConfig, error) {
	account = ensureAccountPaths(cfg, account)
	prior, err := loadSessionInfoForAccountRefresh(cfg, account)
	if err != nil {
		account.Status = "expired"
		account.LastError = err.Error()
		cfg.UpsertAccount(account)
		writeSessionRefreshFailure(account, err)
		return cfg, err
	}
	refreshedSession, err := buildRefreshedSession(ctx, cfg, account, prior)
	if err != nil {
		account.Status = "expired"
		account.LastError = err.Error()
		cfg.UpsertAccount(account)
		writeSessionRefreshFailure(account, err)
		return cfg, err
	}
	if err := writeSessionArtifacts(account, refreshedSession); err != nil {
		account.Status = "failed"
		account.LastError = err.Error()
		cfg.UpsertAccount(account)
		writeSessionRefreshFailure(account, err)
		return cfg, err
	}
	account.UserID = refreshedSession.UserID
	account.UserName = refreshedSession.UserName
	account.SpaceID = refreshedSession.SpaceID
	account.SpaceViewID = refreshedSession.SpaceViewID
	account.SpaceName = firstNonEmpty(refreshedSession.SpaceName, account.SpaceName)
	account.ClientVersion = refreshedSession.ClientVersion
	account.Status = "ready"
	account.LastError = ""
	account.LastLoginAt = sessionRefreshNowISO()
	account.LastRefreshAt = sessionRefreshNowISO()
	account.CooldownUntil = ""
	account.ConsecutiveFailures = 0
	cfg.UpsertAccount(account)
	cfg.ActiveAccount = account.Email
	cfg.ProbeJSON = account.ProbeJSON
	return cfg, nil
}

func (s *ServerState) RefreshSession(ctx context.Context, reason string) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	cfg, _, _ := s.Snapshot()
	refreshCfg := cfg.ResolveSessionRefresh()
	if !refreshCfg.Enabled {
		return fmt.Errorf("session refresh disabled")
	}
	account, _, ok := cfg.ResolveActiveAccount()
	if !ok {
		return fmt.Errorf("no active account configured for session refresh")
	}

	tryRefresh := s.tryRefreshAccount
	if testHookTryRefreshAccount != nil {
		tryRefresh = testHookTryRefreshAccount
	}
	saveAndApply := s.SaveAndApply
	if testHookSaveAndApply != nil {
		saveAndApply = func(cfg AppConfig) error {
			return testHookSaveAndApply(s, cfg)
		}
	}

	updatedCfg, err := tryRefresh(ctx, cfg, account)
	if err == nil {
		if saveErr := saveAndApply(updatedCfg); saveErr != nil {
			s.setSessionRefreshRuntime(saveErr)
			return saveErr
		}
		if s.DispatchProbeCache != nil {
			s.DispatchProbeCache.invalidateAll()
		}
		s.setSessionRefreshRuntime(nil)
		return nil
	}

	if !refreshCfg.AutoSwitch {
		s.setSessionRefreshRuntime(err)
		_ = saveAndApply(updatedCfg)
		return fmt.Errorf("refresh active account %s failed (%s): %w", account.Email, reason, err)
	}

	lastErr := err
	for _, candidate := range cfg.Accounts {
		if getAccountEmailKey(candidate) == getAccountEmailKey(account) {
			continue
		}
		if !fileExists(ensureAccountPaths(cfg, candidate).ProbeJSON) {
			continue
		}
		nextCfg, nextErr := tryRefresh(ctx, updatedCfg, candidate)
		if nextErr != nil {
			lastErr = nextErr
			updatedCfg = nextCfg
			continue
		}
		if saveErr := saveAndApply(nextCfg); saveErr != nil {
			s.setSessionRefreshRuntime(saveErr)
			return saveErr
		}
		if s.DispatchProbeCache != nil {
			s.DispatchProbeCache.invalidateAll()
		}
		s.setSessionRefreshRuntime(nil)
		return nil
	}

	_ = saveAndApply(updatedCfg)
	s.setSessionRefreshRuntime(lastErr)
	return fmt.Errorf("session refresh failed after trying active account and fallbacks (%s): %w", reason, lastErr)
}

func (s *ServerState) StartSessionRefreshLoop(parent context.Context) {
	cfg, _, _ := s.Snapshot()
	refreshCfg := cfg.ResolveSessionRefresh()
	if !refreshCfg.Enabled {
		return
	}
	if refreshCfg.StartupCheck {
		go func() {
			ctx, cancel := context.WithTimeout(parent, helperTimeout(cfg))
			defer cancel()
			_ = s.RefreshSession(ctx, "startup_check")
		}()
	}
	go func() {
		for {
			currentCfg, _, _ := s.Snapshot()
			currentRefresh := currentCfg.ResolveSessionRefresh()
			wait := time.Duration(currentRefresh.IntervalSec) * time.Second
			if wait <= 0 {
				wait = 15 * time.Minute
			}
			timer := time.NewTimer(wait)
			select {
			case <-parent.Done():
				timer.Stop()
				return
			case <-timer.C:
				if !currentRefresh.Enabled {
					continue
				}
				ctx, cancel := context.WithTimeout(parent, helperTimeout(currentCfg))
				_ = s.RefreshSession(ctx, "periodic_check")
				cancel()
			}
		}
	}()
}
