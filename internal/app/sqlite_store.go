package app

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type SQLiteStore struct {
	db   *sql.DB
	roDB *sql.DB
	path string
}

func observeSQLiteDuration(op string, startedAt time.Time) {
	if startedAt.IsZero() {
		return
	}
	elapsed := time.Since(startedAt)
	observeSQLiteOpDuration(op, elapsed)
}

func openSQLiteStore(cfg AppConfig) (*SQLiteStore, error) {
	path := strings.TrimSpace(cfg.ResolveSQLitePath())
	if path == "" {
		return nil, nil
	}
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create sqlite dir: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &SQLiteStore{db: db, path: path}
	if err := store.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	roDB, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=ro&_journal=WAL", path))
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open sqlite read-only: %w", err)
	}
	readers := maxInt(2, runtime.NumCPU())
	roDB.SetMaxOpenConns(readers)
	roDB.SetMaxIdleConns(readers)
	store.roDB = roDB
	return store, nil
}

func (s *SQLiteStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *SQLiteStore) Close() error {
	if s == nil {
		return nil
	}
	var closeErr error
	if s.roDB != nil {
		if err := s.roDB.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	if s.db != nil {
		if err := s.db.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	return closeErr
}

func (s *SQLiteStore) init() error {
	if s == nil || s.db == nil {
		return nil
	}
	pragmas := []string{
		`PRAGMA journal_mode=WAL;`,
		`PRAGMA busy_timeout=5000;`,
		`PRAGMA synchronous=NORMAL;`,
		`PRAGMA foreign_keys=ON;`,
		`PRAGMA mmap_size=268435456;`,
		`PRAGMA cache_size=-65536;`,
		`PRAGMA temp_store=MEMORY;`,
		`PRAGMA wal_autocheckpoint=1000;`,
	}
	for _, stmt := range pragmas {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("sqlite pragma failed (%s): %w", stmt, err)
		}
	}
	schema := []string{
		`CREATE TABLE IF NOT EXISTS accounts (
			email TEXT PRIMARY KEY,
			position INTEGER NOT NULL,
			active INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL,
			data_json TEXT NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_accounts_position ON accounts(position);`,
		`CREATE TABLE IF NOT EXISTS conversations (
			id TEXT PRIMARY KEY,
			status TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			data_json TEXT NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_conversations_updated_at ON conversations(updated_at DESC);`,
		`CREATE TABLE IF NOT EXISTS responses (
			response_id TEXT PRIMARY KEY,
			created_at TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			conversation_id TEXT NOT NULL DEFAULT '',
			thread_id TEXT NOT NULL DEFAULT '',
			account_email TEXT NOT NULL DEFAULT ''
		);`,
		`CREATE INDEX IF NOT EXISTS idx_responses_created_at ON responses(created_at DESC);`,
		`CREATE TABLE IF NOT EXISTS conversation_sessions (
			id TEXT PRIMARY KEY,
			conversation_id TEXT NOT NULL,
			fingerprint TEXT NOT NULL DEFAULT '',
			thread_id TEXT NOT NULL,
			account_email TEXT NOT NULL DEFAULT '',
			config_id TEXT NOT NULL,
			context_id TEXT NOT NULL,
			original_datetime TEXT NOT NULL,
			model_used TEXT NOT NULL DEFAULT '',
			turn_count INTEGER NOT NULL DEFAULT 0,
			raw_message_count INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'active',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			last_used_at TEXT NOT NULL,
			deleted_at TEXT NOT NULL DEFAULT ''
		);`,
		`CREATE INDEX IF NOT EXISTS idx_conversation_sessions_conversation_id ON conversation_sessions(conversation_id);`,
		`CREATE INDEX IF NOT EXISTS idx_conversation_sessions_thread_id ON conversation_sessions(thread_id);`,
		`CREATE INDEX IF NOT EXISTS idx_conversation_sessions_fingerprint ON conversation_sessions(fingerprint);`,
		`CREATE TABLE IF NOT EXISTS conversation_session_steps (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			step_index INTEGER NOT NULL,
			updated_config_id TEXT NOT NULL,
			response_id TEXT NOT NULL DEFAULT '',
			message_id TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			UNIQUE(session_id, step_index),
			UNIQUE(updated_config_id)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_conversation_session_steps_session_id ON conversation_session_steps(session_id, step_index ASC);`,
		`CREATE TABLE IF NOT EXISTS sillytavern_bindings (
			conversation_id TEXT PRIMARY KEY,
			profile_key TEXT NOT NULL DEFAULT '',
			thread_id TEXT NOT NULL DEFAULT '',
			account_email TEXT NOT NULL DEFAULT '',
			mode TEXT NOT NULL DEFAULT '',
			transcript_json TEXT NOT NULL DEFAULT '[]',
			raw_message_count INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_sillytavern_bindings_profile_key ON sillytavern_bindings(profile_key, updated_at DESC);`,
	}
	for _, stmt := range schema {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("sqlite schema failed: %w", err)
		}
	}
	for _, stmt := range []string{
		`ALTER TABLE responses ADD COLUMN conversation_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE responses ADD COLUMN thread_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE responses ADD COLUMN account_email TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			lower := strings.ToLower(err.Error())
			if !strings.Contains(lower, "duplicate column name") {
				return fmt.Errorf("sqlite migration failed: %w", err)
			}
		}
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_responses_conversation_id ON responses(conversation_id);`); err != nil {
		return fmt.Errorf("sqlite schema failed: %w", err)
	}
	return nil
}

func (s *SQLiteStore) readDB() *sql.DB {
	if s == nil {
		return nil
	}
	if s.roDB != nil {
		return s.roDB
	}
	return s.db
}

func (s *SQLiteStore) SaveAccounts(cfg AppConfig) error {
	startedAt := time.Now()
	defer observeSQLiteDuration("save_accounts", startedAt)
	if s == nil || s.db == nil {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.Exec(`DELETE FROM accounts`); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	activeKey := canonicalEmailKey(cfg.ActiveAccount)
	for i, account := range cfg.Accounts {
		account = ensureAccountPaths(cfg, account)
		body, marshalErr := json.Marshal(account)
		if marshalErr != nil {
			err = marshalErr
			return err
		}
		active := 0
		if getAccountEmailKey(account) == activeKey {
			active = 1
		}
		if _, err = tx.Exec(
			`INSERT INTO accounts(email, position, active, updated_at, data_json) VALUES(?, ?, ?, ?, ?)`,
			account.Email,
			i,
			active,
			now,
			string(body),
		); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (s *SQLiteStore) LoadAccounts() ([]NotionAccount, string, bool, error) {
	startedAt := time.Now()
	defer observeSQLiteDuration("load_accounts", startedAt)
	db := s.readDB()
	if db == nil {
		return nil, "", false, nil
	}
	rows, err := db.Query(`SELECT data_json, active FROM accounts ORDER BY position ASC, email ASC`)
	if err != nil {
		return nil, "", false, err
	}
	defer rows.Close()
	accounts := []NotionAccount{}
	activeAccount := ""
	for rows.Next() {
		var body string
		var active int
		if err := rows.Scan(&body, &active); err != nil {
			return nil, "", false, err
		}
		var account NotionAccount
		if err := json.Unmarshal([]byte(body), &account); err != nil {
			return nil, "", false, err
		}
		accounts = append(accounts, account)
		if active > 0 && activeAccount == "" {
			activeAccount = account.Email
		}
	}
	if err := rows.Err(); err != nil {
		return nil, "", false, err
	}
	return accounts, activeAccount, len(accounts) > 0, nil
}

func (s *SQLiteStore) SaveConversation(entry ConversationEntry) error {
	startedAt := time.Now()
	defer observeSQLiteDuration("save_conversation", startedAt)
	if s == nil || s.db == nil {
		return nil
	}
	body, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO conversations(id, status, created_at, updated_at, data_json)
		 VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   status=excluded.status,
		   created_at=excluded.created_at,
		   updated_at=excluded.updated_at,
		   data_json=excluded.data_json`,
		entry.ID,
		firstNonEmpty(entry.Status, "running"),
		entry.CreatedAt.UTC().Format(time.RFC3339Nano),
		entry.UpdatedAt.UTC().Format(time.RFC3339Nano),
		string(body),
	)
	return err
}

func (s *SQLiteStore) DeleteConversation(id string) error {
	startedAt := time.Now()
	defer observeSQLiteDuration("delete_conversation", startedAt)
	if s == nil || s.db == nil || strings.TrimSpace(id) == "" {
		return nil
	}
	_, err := s.db.Exec(`DELETE FROM conversations WHERE id = ?`, strings.TrimSpace(id))
	return err
}

func (s *SQLiteStore) DeleteResponsesByConversationOrThread(conversationID string, threadID string) error {
	startedAt := time.Now()
	defer observeSQLiteDuration("delete_responses_by_conversation_or_thread", startedAt)
	if s == nil || s.db == nil {
		return nil
	}
	conversationID = strings.TrimSpace(conversationID)
	threadID = strings.TrimSpace(threadID)
	switch {
	case conversationID != "" && threadID != "":
		_, err := s.db.Exec(`DELETE FROM responses WHERE conversation_id = ? OR thread_id = ?`, conversationID, threadID)
		return err
	case conversationID != "":
		_, err := s.db.Exec(`DELETE FROM responses WHERE conversation_id = ?`, conversationID)
		return err
	case threadID != "":
		_, err := s.db.Exec(`DELETE FROM responses WHERE thread_id = ?`, threadID)
		return err
	default:
		return nil
	}
}

func (s *SQLiteStore) LoadConversations() ([]ConversationEntry, error) {
	startedAt := time.Now()
	defer observeSQLiteDuration("load_conversations", startedAt)
	db := s.readDB()
	if db == nil {
		return nil, nil
	}
	rows, err := db.Query(`SELECT data_json FROM conversations ORDER BY updated_at DESC, created_at DESC LIMIT ?`, maxConversationEntries)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ConversationEntry{}
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var entry ConversationEntry
		if err := json.Unmarshal([]byte(body), &entry); err != nil {
			return nil, err
		}
		items = append(items, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *SQLiteStore) SaveResponse(responseID string, payload map[string]any, createdAt time.Time, conversationID string, threadID string, accountEmail string) error {
	startedAt := time.Now()
	defer observeSQLiteDuration("save_response", startedAt)
	if s == nil || s.db == nil || strings.TrimSpace(responseID) == "" {
		return nil
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO responses(response_id, created_at, payload_json, conversation_id, thread_id, account_email)
		 VALUES(?, ?, ?, ?, ?, ?)
		 ON CONFLICT(response_id) DO UPDATE SET
		   created_at=excluded.created_at,
		   payload_json=excluded.payload_json,
		   conversation_id=excluded.conversation_id,
		   thread_id=excluded.thread_id,
		   account_email=excluded.account_email`,
		strings.TrimSpace(responseID),
		createdAt.UTC().Format(time.RFC3339Nano),
		string(body),
		strings.TrimSpace(conversationID),
		strings.TrimSpace(threadID),
		strings.TrimSpace(accountEmail),
	)
	return err
}

func (s *SQLiteStore) DeleteExpiredResponses(ttl time.Duration) error {
	startedAt := time.Now()
	defer observeSQLiteDuration("delete_expired_responses", startedAt)
	if s == nil || s.db == nil || ttl <= 0 {
		return nil
	}
	cutoff := time.Now().UTC().Add(-ttl).Format(time.RFC3339Nano)
	_, err := s.db.Exec(`DELETE FROM responses WHERE created_at < ?`, cutoff)
	return err
}

func (s *SQLiteStore) LoadResponses(ttl time.Duration) (map[string]StoredResponse, error) {
	startedAt := time.Now()
	defer observeSQLiteDuration("load_responses", startedAt)
	db := s.readDB()
	if db == nil {
		return map[string]StoredResponse{}, nil
	}
	if err := s.DeleteExpiredResponses(ttl); err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT response_id, created_at, payload_json, conversation_id, thread_id, account_email FROM responses ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]StoredResponse{}
	for rows.Next() {
		var responseID string
		var createdAtText string
		var body string
		var conversationID string
		var threadID string
		var accountEmail string
		if err := rows.Scan(&responseID, &createdAtText, &body, &conversationID, &threadID, &accountEmail); err != nil {
			return nil, err
		}
		createdAt, err := time.Parse(time.RFC3339Nano, createdAtText)
		if err != nil {
			return nil, err
		}
		payload := map[string]any{}
		if err := json.Unmarshal([]byte(body), &payload); err != nil {
			return nil, err
		}
		out[responseID] = StoredResponse{
			Payload:        payload,
			CreatedAt:      createdAt.UTC(),
			ConversationID: strings.TrimSpace(conversationID),
			ThreadID:       strings.TrimSpace(threadID),
			AccountEmail:   strings.TrimSpace(accountEmail),
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *SQLiteStore) SaveConversationSession(session ConversationSession) error {
	startedAt := time.Now()
	defer observeSQLiteDuration("save_conversation_session", startedAt)
	if s == nil || s.db == nil || strings.TrimSpace(session.ID) == "" {
		return nil
	}
	_, err := s.db.Exec(
		`INSERT INTO conversation_sessions(
			id, conversation_id, fingerprint, thread_id, account_email, config_id, context_id,
			original_datetime, model_used, turn_count, raw_message_count, status,
			created_at, updated_at, last_used_at, deleted_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			conversation_id=excluded.conversation_id,
			fingerprint=excluded.fingerprint,
			thread_id=excluded.thread_id,
			account_email=excluded.account_email,
			config_id=excluded.config_id,
			context_id=excluded.context_id,
			original_datetime=excluded.original_datetime,
			model_used=excluded.model_used,
			turn_count=excluded.turn_count,
			raw_message_count=excluded.raw_message_count,
			status=excluded.status,
			created_at=excluded.created_at,
			updated_at=excluded.updated_at,
			last_used_at=excluded.last_used_at,
			deleted_at=excluded.deleted_at`,
		strings.TrimSpace(session.ID),
		strings.TrimSpace(session.ConversationID),
		strings.TrimSpace(session.Fingerprint),
		strings.TrimSpace(session.ThreadID),
		strings.TrimSpace(session.AccountEmail),
		strings.TrimSpace(session.ConfigID),
		strings.TrimSpace(session.ContextID),
		strings.TrimSpace(session.OriginalDatetime),
		strings.TrimSpace(session.ModelUsed),
		session.TurnCount,
		session.RawMessageCount,
		firstNonEmpty(strings.TrimSpace(session.Status), "active"),
		session.CreatedAt.UTC().Format(time.RFC3339Nano),
		session.UpdatedAt.UTC().Format(time.RFC3339Nano),
		session.LastUsedAt.UTC().Format(time.RFC3339Nano),
		formatSQLiteTime(session.DeletedAt),
	)
	return err
}

func (s *SQLiteStore) SaveConversationSessionStep(step ConversationSessionStep) error {
	startedAt := time.Now()
	defer observeSQLiteDuration("save_conversation_session_step", startedAt)
	if s == nil || s.db == nil || strings.TrimSpace(step.SessionID) == "" || strings.TrimSpace(step.UpdatedConfigID) == "" {
		return nil
	}
	_, err := s.db.Exec(
		`INSERT INTO conversation_session_steps(session_id, step_index, updated_config_id, response_id, message_id, created_at)
		 VALUES(?, ?, ?, ?, ?, ?)
		 ON CONFLICT(session_id, step_index) DO UPDATE SET
			updated_config_id=excluded.updated_config_id,
			response_id=excluded.response_id,
			message_id=excluded.message_id,
			created_at=excluded.created_at`,
		strings.TrimSpace(step.SessionID),
		step.StepIndex,
		strings.TrimSpace(step.UpdatedConfigID),
		strings.TrimSpace(step.ResponseID),
		strings.TrimSpace(step.MessageID),
		step.CreatedAt.UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (s *SQLiteStore) LoadConversationSessionByConversationID(conversationID string) (ConversationSession, bool, error) {
	return s.loadConversationSession(`SELECT id, conversation_id, fingerprint, thread_id, account_email, config_id, context_id, original_datetime, model_used, turn_count, raw_message_count, status, created_at, updated_at, last_used_at, deleted_at FROM conversation_sessions WHERE conversation_id = ? AND status = 'active' AND deleted_at = '' ORDER BY updated_at DESC LIMIT 1`, strings.TrimSpace(conversationID))
}

func (s *SQLiteStore) LoadConversationSessionByThreadID(threadID string) (ConversationSession, bool, error) {
	return s.loadConversationSession(`SELECT id, conversation_id, fingerprint, thread_id, account_email, config_id, context_id, original_datetime, model_used, turn_count, raw_message_count, status, created_at, updated_at, last_used_at, deleted_at FROM conversation_sessions WHERE thread_id = ? AND status = 'active' AND deleted_at = '' ORDER BY updated_at DESC LIMIT 1`, strings.TrimSpace(threadID))
}

func (s *SQLiteStore) LoadConversationSessionByFingerprint(fingerprint string) (ConversationSession, bool, error) {
	return s.loadConversationSession(`SELECT id, conversation_id, fingerprint, thread_id, account_email, config_id, context_id, original_datetime, model_used, turn_count, raw_message_count, status, created_at, updated_at, last_used_at, deleted_at FROM conversation_sessions WHERE fingerprint = ? AND status = 'active' AND deleted_at = '' ORDER BY updated_at DESC LIMIT 1`, strings.TrimSpace(fingerprint))
}

func (s *SQLiteStore) LoadConversationSessionBySessionID(sessionID string) (ConversationSession, bool, error) {
	return s.loadConversationSession(`SELECT id, conversation_id, fingerprint, thread_id, account_email, config_id, context_id, original_datetime, model_used, turn_count, raw_message_count, status, created_at, updated_at, last_used_at, deleted_at FROM conversation_sessions WHERE id = ? ORDER BY updated_at DESC LIMIT 1`, strings.TrimSpace(sessionID))
}

func (s *SQLiteStore) loadConversationSession(query string, arg string) (ConversationSession, bool, error) {
	startedAt := time.Now()
	defer observeSQLiteDuration("load_conversation_session", startedAt)
	db := s.readDB()
	if db == nil || strings.TrimSpace(arg) == "" {
		return ConversationSession{}, false, nil
	}
	row := db.QueryRow(query, arg)
	var (
		session                                                     ConversationSession
		createdAtText, updatedAtText, lastUsedAtText, deletedAtText string
	)
	if err := row.Scan(
		&session.ID,
		&session.ConversationID,
		&session.Fingerprint,
		&session.ThreadID,
		&session.AccountEmail,
		&session.ConfigID,
		&session.ContextID,
		&session.OriginalDatetime,
		&session.ModelUsed,
		&session.TurnCount,
		&session.RawMessageCount,
		&session.Status,
		&createdAtText,
		&updatedAtText,
		&lastUsedAtText,
		&deletedAtText,
	); err != nil {
		if err == sql.ErrNoRows {
			return ConversationSession{}, false, nil
		}
		return ConversationSession{}, false, err
	}
	var err error
	if session.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAtText); err != nil {
		return ConversationSession{}, false, err
	}
	if session.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAtText); err != nil {
		return ConversationSession{}, false, err
	}
	if session.LastUsedAt, err = time.Parse(time.RFC3339Nano, lastUsedAtText); err != nil {
		return ConversationSession{}, false, err
	}
	if strings.TrimSpace(deletedAtText) != "" {
		if session.DeletedAt, err = time.Parse(time.RFC3339Nano, deletedAtText); err != nil {
			return ConversationSession{}, false, err
		}
	}
	session.CreatedAt = session.CreatedAt.UTC()
	session.UpdatedAt = session.UpdatedAt.UTC()
	session.LastUsedAt = session.LastUsedAt.UTC()
	session.DeletedAt = session.DeletedAt.UTC()
	return session, true, nil
}

func (s *SQLiteStore) LoadConversationSessionStepIDs(sessionID string) ([]string, error) {
	startedAt := time.Now()
	defer observeSQLiteDuration("load_conversation_session_step_ids", startedAt)
	db := s.readDB()
	if db == nil || strings.TrimSpace(sessionID) == "" {
		return nil, nil
	}
	rows, err := db.Query(`SELECT updated_config_id FROM conversation_session_steps WHERE session_id = ? ORDER BY step_index ASC`, strings.TrimSpace(sessionID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var updatedConfigID string
		if err := rows.Scan(&updatedConfigID); err != nil {
			return nil, err
		}
		out = append(out, strings.TrimSpace(updatedConfigID))
	}
	return out, rows.Err()
}

func (s *SQLiteStore) MarkConversationSessionStatus(sessionID string, status string) error {
	startedAt := time.Now()
	defer observeSQLiteDuration("mark_conversation_session_status", startedAt)
	if s == nil || s.db == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	status = strings.TrimSpace(status)
	if status == "" {
		status = conversationSessionStatusInvalidated
	}
	_, err := s.db.Exec(
		`UPDATE conversation_sessions SET status = ?, updated_at = ?, last_used_at = ? WHERE id = ?`,
		status,
		time.Now().UTC().Format(time.RFC3339Nano),
		time.Now().UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(sessionID),
	)
	return err
}

func (s *SQLiteStore) DeleteConversationSessionByConversationOrThread(conversationID string, threadID string) error {
	startedAt := time.Now()
	defer observeSQLiteDuration("delete_conversation_session_by_conversation_or_thread", startedAt)
	if s == nil || s.db == nil {
		return nil
	}
	conversationID = strings.TrimSpace(conversationID)
	threadID = strings.TrimSpace(threadID)
	if conversationID == "" && threadID == "" {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	sessionIDs := []string{}
	var rows *sql.Rows
	switch {
	case conversationID != "" && threadID != "":
		rows, err = tx.Query(`SELECT id FROM conversation_sessions WHERE conversation_id = ? OR thread_id = ?`, conversationID, threadID)
	case conversationID != "":
		rows, err = tx.Query(`SELECT id FROM conversation_sessions WHERE conversation_id = ?`, conversationID)
	default:
		rows, err = tx.Query(`SELECT id FROM conversation_sessions WHERE thread_id = ?`, threadID)
	}
	if err != nil {
		return err
	}
	for rows.Next() {
		var sessionID string
		if scanErr := rows.Scan(&sessionID); scanErr != nil {
			_ = rows.Close()
			return scanErr
		}
		sessionIDs = append(sessionIDs, strings.TrimSpace(sessionID))
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	for _, sessionID := range sessionIDs {
		if _, err = tx.Exec(`DELETE FROM conversation_session_steps WHERE session_id = ?`, sessionID); err != nil {
			return err
		}
	}
	switch {
	case conversationID != "" && threadID != "":
		_, err = tx.Exec(`DELETE FROM conversation_sessions WHERE conversation_id = ? OR thread_id = ?`, conversationID, threadID)
	case conversationID != "":
		_, err = tx.Exec(`DELETE FROM conversation_sessions WHERE conversation_id = ?`, conversationID)
	default:
		_, err = tx.Exec(`DELETE FROM conversation_sessions WHERE thread_id = ?`, threadID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) SaveSillyTavernBinding(binding SillyTavernBinding) error {
	startedAt := time.Now()
	defer observeSQLiteDuration("save_sillytavern_binding", startedAt)
	if s == nil || s.db == nil || strings.TrimSpace(binding.ConversationID) == "" {
		return nil
	}
	transcriptJSON, err := json.Marshal(binding.Transcript)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO sillytavern_bindings(
			conversation_id, profile_key, thread_id, account_email, mode, transcript_json, raw_message_count, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(conversation_id) DO UPDATE SET
			profile_key=excluded.profile_key,
			thread_id=excluded.thread_id,
			account_email=excluded.account_email,
			mode=excluded.mode,
			transcript_json=excluded.transcript_json,
			raw_message_count=excluded.raw_message_count,
			updated_at=excluded.updated_at`,
		strings.TrimSpace(binding.ConversationID),
		strings.TrimSpace(binding.ProfileKey),
		strings.TrimSpace(binding.ThreadID),
		strings.TrimSpace(binding.AccountEmail),
		strings.TrimSpace(binding.Mode),
		string(transcriptJSON),
		binding.RawMessageCount,
		formatSQLiteTime(binding.UpdatedAt),
	)
	return err
}

func (s *SQLiteStore) LoadRecentSillyTavernBindings(profileKey string, limit int) ([]SillyTavernBinding, error) {
	startedAt := time.Now()
	defer observeSQLiteDuration("load_recent_sillytavern_bindings", startedAt)
	db := s.readDB()
	if db == nil || strings.TrimSpace(profileKey) == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 12
	}
	rows, err := db.Query(
		`SELECT conversation_id, profile_key, thread_id, account_email, mode, transcript_json, raw_message_count, updated_at
		 FROM sillytavern_bindings
		 WHERE profile_key = ?
		 ORDER BY updated_at DESC
		 LIMIT ?`,
		strings.TrimSpace(profileKey),
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SillyTavernBinding
	for rows.Next() {
		var (
			binding        SillyTavernBinding
			transcriptJSON string
			updatedAt      string
		)
		if err := rows.Scan(
			&binding.ConversationID,
			&binding.ProfileKey,
			&binding.ThreadID,
			&binding.AccountEmail,
			&binding.Mode,
			&transcriptJSON,
			&binding.RawMessageCount,
			&updatedAt,
		); err != nil {
			return nil, err
		}
		if strings.TrimSpace(transcriptJSON) != "" {
			_ = json.Unmarshal([]byte(transcriptJSON), &binding.Transcript)
		}
		if clean := strings.TrimSpace(updatedAt); clean != "" {
			if parsed, parseErr := time.Parse(time.RFC3339Nano, clean); parseErr == nil {
				binding.UpdatedAt = parsed
			}
		}
		out = append(out, binding)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) DeleteSillyTavernBinding(conversationID string) error {
	startedAt := time.Now()
	defer observeSQLiteDuration("delete_sillytavern_binding", startedAt)
	if s == nil || s.db == nil || strings.TrimSpace(conversationID) == "" {
		return nil
	}
	_, err := s.db.Exec(`DELETE FROM sillytavern_bindings WHERE conversation_id = ?`, strings.TrimSpace(conversationID))
	return err
}

func formatSQLiteTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
