package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

var (
	accountPathSlugPattern     = regexp.MustCompile(`[^a-z0-9]+`)
	windowsAbsolutePathPattern = regexp.MustCompile(`^[A-Za-z]:[\\/].*`)
)

type ResolvedLoginHelper struct {
	SessionsDir string `json:"sessions_dir"`
	TimeoutSec  int    `json:"timeout_sec"`
}

type ResolvedSessionRefresh struct {
	Enabled          bool `json:"enabled"`
	IntervalSec      int  `json:"interval_sec"`
	StartupCheck     bool `json:"startup_check"`
	RetryOnAuthError bool `json:"retry_on_auth_error"`
	AutoSwitch       bool `json:"auto_switch_account"`
}

type LoginStatusFile struct {
	Success          bool   `json:"success"`
	Status           string `json:"status,omitempty"`
	Email            string `json:"email,omitempty"`
	ProfileDir       string `json:"profile_dir,omitempty"`
	PendingStatePath string `json:"pending_state_path,omitempty"`
	StorageStatePath string `json:"storage_state_path,omitempty"`
	ProbePath        string `json:"probe_path,omitempty"`
	UserID           string `json:"user_id,omitempty"`
	UserName         string `json:"user_name,omitempty"`
	SpaceID          string `json:"space_id,omitempty"`
	SpaceViewID      string `json:"space_view_id,omitempty"`
	SpaceName        string `json:"space_name,omitempty"`
	ClientVersion    string `json:"client_version,omitempty"`
	CurrentURL       string `json:"current_url,omitempty"`
	FinalURL         string `json:"final_url,omitempty"`
	Title            string `json:"title,omitempty"`
	Message          string `json:"message,omitempty"`
	Error            string `json:"error,omitempty"`
	UpdatedAt        string `json:"updated_at,omitempty"`
	LastLoginAt      string `json:"last_login_at,omitempty"`
}

func canonicalEmailKey(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func getAccountEmailKey(account NotionAccount) string {
	if account.emailKey != "" {
		return account.emailKey
	}
	return canonicalEmailKey(account.Email)
}

func accountPathSlug(email string) string {
	clean := canonicalEmailKey(email)
	if clean == "" {
		return "account"
	}
	clean = accountPathSlugPattern.ReplaceAllString(clean, "_")
	clean = strings.Trim(clean, "_")
	if clean == "" {
		return "account"
	}
	return clean
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func resolveConfigRelativePath(configPath string, raw string, fallback string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		value = strings.TrimSpace(fallback)
	}
	if value == "" {
		return ""
	}
	if pathLooksAbsoluteAnyOS(value) {
		return filepath.Clean(value)
	}
	if strings.TrimSpace(configPath) != "" {
		return filepath.Clean(filepath.Join(filepath.Dir(configPath), value))
	}
	return filepath.Clean(value)
}

func pathLooksAbsoluteAnyOS(value string) bool {
	clean := strings.TrimSpace(value)
	if clean == "" {
		return false
	}
	if filepath.IsAbs(clean) {
		return true
	}
	if windowsAbsolutePathPattern.MatchString(clean) {
		return true
	}
	if strings.HasPrefix(clean, `\\`) {
		return true
	}
	if strings.HasPrefix(clean, "/") {
		return true
	}
	return false
}

func isForeignAbsolutePath(value string) bool {
	clean := strings.TrimSpace(value)
	if clean == "" {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.HasPrefix(clean, "/")
	}
	if windowsAbsolutePathPattern.MatchString(clean) {
		return true
	}
	if strings.HasPrefix(clean, `\\`) {
		return true
	}
	return false
}

func fileExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	stat, err := os.Stat(path)
	return err == nil && !stat.IsDir()
}

func dirExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	stat, err := os.Stat(path)
	return err == nil && stat.IsDir()
}

func (cfg AppConfig) FindAccount(email string) (NotionAccount, int, bool) {
	target := canonicalEmailKey(email)
	if target == "" {
		return NotionAccount{}, -1, false
	}
	for i, account := range cfg.Accounts {
		if getAccountEmailKey(account) == target {
			return account, i, true
		}
	}
	return NotionAccount{}, -1, false
}

func (cfg AppConfig) ResolveActiveAccount() (NotionAccount, int, bool) {
	if cfg.ActiveAccount == "" {
		return NotionAccount{}, -1, false
	}
	return cfg.FindAccount(cfg.ActiveAccount)
}

func (cfg AppConfig) ResolveSessionTarget() (probePath string, userName string, spaceName string, activeEmail string) {
	if account, _, ok := cfg.ResolveActiveAccount(); ok {
		account = ensureAccountPaths(cfg, account)
		return strings.TrimSpace(account.ProbeJSON), firstNonEmpty(account.UserName, cfg.UserName), firstNonEmpty(account.SpaceName, cfg.SpaceName), account.Email
	}
	return resolveConfigRelativePath(cfg.ConfigPath, cfg.ProbeJSON, cfg.ProbeJSON), strings.TrimSpace(cfg.UserName), strings.TrimSpace(cfg.SpaceName), ""
}

func (cfg AppConfig) SessionConfigured() bool {
	probePath, _, _, _ := cfg.ResolveSessionTarget()
	return strings.TrimSpace(probePath) != ""
}

func (cfg AppConfig) ResolveLoginHelper() ResolvedLoginHelper {
	return ResolvedLoginHelper{
		SessionsDir: resolveConfigRelativePath(cfg.ConfigPath, cfg.LoginHelper.SessionsDir, "probe_files/notion_accounts"),
		TimeoutSec:  maxInt(cfg.LoginHelper.TimeoutSec, 30),
	}
}

func (cfg AppConfig) ResolveSessionRefresh() ResolvedSessionRefresh {
	return ResolvedSessionRefresh{
		Enabled:          cfg.SessionRefresh.Enabled,
		IntervalSec:      maxInt(cfg.SessionRefresh.IntervalSec, 60),
		StartupCheck:     cfg.SessionRefresh.StartupCheck,
		RetryOnAuthError: cfg.SessionRefresh.RetryOnAuthError,
		AutoSwitch:       cfg.SessionRefresh.AutoSwitch,
	}
}

func (helper ResolvedLoginHelper) ProfileDirFor(email string) string {
	baseDir := strings.TrimSpace(helper.SessionsDir)
	if baseDir == "" {
		baseDir = filepath.Clean("probe_files/notion_accounts")
	}
	return filepath.Join(baseDir, accountPathSlug(email))
}

func (helper ResolvedLoginHelper) PendingStatePath(profileDir string) string {
	return filepath.Join(profileDir, "pending_login.json")
}

func (helper ResolvedLoginHelper) StorageStatePath(profileDir string) string {
	return filepath.Join(profileDir, "storage_state.json")
}

func (helper ResolvedLoginHelper) ProbePath(profileDir string) string {
	return filepath.Join(profileDir, "probe.json")
}

func ensureAccountPaths(cfg AppConfig, account NotionAccount) NotionAccount {
	account.emailKey = canonicalEmailKey(account.Email)
	helper := cfg.ResolveLoginHelper()
	if strings.TrimSpace(account.ProfileDir) == "" || isForeignAbsolutePath(account.ProfileDir) {
		account.ProfileDir = helper.ProfileDirFor(account.Email)
	} else {
		account.ProfileDir = resolveConfigRelativePath(cfg.ConfigPath, account.ProfileDir, account.ProfileDir)
	}
	if strings.TrimSpace(account.StorageStatePath) == "" || isForeignAbsolutePath(account.StorageStatePath) {
		account.StorageStatePath = helper.StorageStatePath(account.ProfileDir)
	} else {
		account.StorageStatePath = resolveConfigRelativePath(cfg.ConfigPath, account.StorageStatePath, account.StorageStatePath)
	}
	if strings.TrimSpace(account.PendingStatePath) == "" || isForeignAbsolutePath(account.PendingStatePath) {
		account.PendingStatePath = helper.PendingStatePath(account.ProfileDir)
	} else {
		account.PendingStatePath = resolveConfigRelativePath(cfg.ConfigPath, account.PendingStatePath, account.PendingStatePath)
	}
	if strings.TrimSpace(account.ProbeJSON) == "" || isForeignAbsolutePath(account.ProbeJSON) {
		account.ProbeJSON = helper.ProbePath(account.ProfileDir)
	} else {
		account.ProbeJSON = resolveConfigRelativePath(cfg.ConfigPath, account.ProbeJSON, account.ProbeJSON)
	}
	return account
}

func (cfg *AppConfig) UpsertAccount(account NotionAccount) (NotionAccount, int) {
	account = ensureAccountPaths(*cfg, account)
	if existing, index, ok := cfg.FindAccount(account.Email); ok {
		if account.ProbeJSON == "" {
			account.ProbeJSON = existing.ProbeJSON
		}
		if account.ProfileDir == "" {
			account.ProfileDir = existing.ProfileDir
		}
		if account.StorageStatePath == "" {
			account.StorageStatePath = existing.StorageStatePath
		}
		if account.PendingStatePath == "" {
			account.PendingStatePath = existing.PendingStatePath
		}
		if account.UserID == "" {
			account.UserID = existing.UserID
		}
		if account.UserName == "" {
			account.UserName = existing.UserName
		}
		if account.SpaceID == "" {
			account.SpaceID = existing.SpaceID
		}
		if account.SpaceViewID == "" {
			account.SpaceViewID = existing.SpaceViewID
		}
		if account.SpaceName == "" {
			account.SpaceName = existing.SpaceName
		}
		if account.PlanType == "" {
			account.PlanType = existing.PlanType
		}
		if account.ClientVersion == "" {
			account.ClientVersion = existing.ClientVersion
		}
		if account.Status == "" {
			account.Status = existing.Status
		}
		if account.LastError == "" {
			account.LastError = existing.LastError
		}
		if account.LastLoginAt == "" {
			account.LastLoginAt = existing.LastLoginAt
		}
		if account.Priority == 0 {
			account.Priority = existing.Priority
		}
		if account.HourlyQuota == 0 {
			account.HourlyQuota = existing.HourlyQuota
		}
		if account.WindowStartedAt == "" {
			account.WindowStartedAt = existing.WindowStartedAt
		}
		if account.WindowRequestCount == 0 {
			account.WindowRequestCount = existing.WindowRequestCount
		}
		if account.CooldownUntil == "" {
			account.CooldownUntil = existing.CooldownUntil
		}
		if account.LastUsedAt == "" {
			account.LastUsedAt = existing.LastUsedAt
		}
		if account.LastSuccessAt == "" {
			account.LastSuccessAt = existing.LastSuccessAt
		}
		if account.LastRefreshAt == "" {
			account.LastRefreshAt = existing.LastRefreshAt
		}
		if account.LastReloginAt == "" {
			account.LastReloginAt = existing.LastReloginAt
		}
		if account.ConsecutiveFailures == 0 {
			account.ConsecutiveFailures = existing.ConsecutiveFailures
		}
		if account.TotalSuccesses == 0 {
			account.TotalSuccesses = existing.TotalSuccesses
		}
		if account.TotalFailures == 0 {
			account.TotalFailures = existing.TotalFailures
		}
		cfg.Accounts[index] = account
		return account, index
	}
	cfg.Accounts = append(cfg.Accounts, account)
	return account, len(cfg.Accounts) - 1
}

func (cfg *AppConfig) DeleteAccount(email string) bool {
	target := canonicalEmailKey(email)
	_, index, ok := cfg.FindAccount(target)
	if !ok {
		return false
	}
	cfg.Accounts = append(cfg.Accounts[:index], cfg.Accounts[index+1:]...)
	if canonicalEmailKey(cfg.ActiveAccount) == target {
		cfg.ActiveAccount = ""
		cfg.ProbeJSON = ""
	}
	return true
}

func readLoginStatusFile(path string) (LoginStatusFile, error) {
	clean := strings.TrimSpace(path)
	if clean == "" {
		return LoginStatusFile{}, fmt.Errorf("empty login status path")
	}
	raw, err := os.ReadFile(clean)
	if err != nil {
		return LoginStatusFile{}, err
	}
	var payload LoginStatusFile
	if err := json.Unmarshal(raw, &payload); err != nil {
		return LoginStatusFile{}, err
	}
	return payload, nil
}
