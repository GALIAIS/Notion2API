package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"expvar"
	"fmt"
	"io"
	"log"
	"net/http"
	_ "net/http/pprof"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type StoredResponse struct {
	Payload        map[string]any
	CreatedAt      time.Time
	ConversationID string
	ThreadID       string
	AccountEmail   string
}

type snapshotBundle struct {
	Config        AppConfig
	Session       SessionInfo
	ModelRegistry ModelRegistry
	DispatchOrder []NotionAccount
}

type ServerState struct {
	mu                         sync.RWMutex
	refreshMu                  sync.Mutex
	Config                     AppConfig
	Session                    SessionInfo
	Client                     *NotionAIClient
	Store                      *SQLiteStore
	ModelRegistry              ModelRegistry
	ResponseStore              *responseStore
	Conversations              *ConversationStore
	AdminTokens                map[string]time.Time
	AdminLoginAttempts         map[string]AdminLoginAttempt
	DispatchProbeCache         *probeCache
	LastSessionRefresh         time.Time
	LastSessionRefreshError    string
	responseStoreCleanupCancel context.CancelFunc
	sqliteWriter               *SQLiteWriter
	snap                       atomic.Pointer[snapshotBundle]
	slots                      atomic.Pointer[map[string]*accountSlot]
	cachedHealthzStaticJSON    atomic.Pointer[[]byte]
	cachedModelsListJSON       atomic.Pointer[[]byte]
	cachedModelByIDJSON        atomic.Pointer[map[string][]byte]
}

type accountDispatchState struct {
	MaxConcurrency int
	InFlight       int
}

type accountSlot struct {
	max      atomic.Int32
	inflight atomic.Int32
}

type healthzStaticPayload struct {
	OK                   bool   `json:"ok"`
	DefaultModel         string `json:"default_model"`
	ModelCount           int    `json:"model_count"`
	UserEmail            string `json:"user_email"`
	SpaceID              string `json:"space_id"`
	ActiveAccount        string `json:"active_account"`
	SessionRefreshEnable bool   `json:"session_refresh_enabled"`
}

type publicModelPayload struct {
	ID          string `json:"id"`
	Object      string `json:"object"`
	Created     int    `json:"created"`
	OwnedBy     string `json:"owned_by"`
	Name        string `json:"name"`
	Family      string `json:"family"`
	Group       string `json:"group"`
	Beta        bool   `json:"beta"`
	NotionModel string `json:"notion_model"`
}

type publicModelsListPayload struct {
	Object string               `json:"object"`
	Data   []publicModelPayload `json:"data"`
}

type App struct {
	State                            *ServerState
	runPromptOverride                func(*http.Request, PromptRunRequest) (InferenceResult, error)
	runPromptStreamOverride          func(*http.Request, PromptRunRequest, func(string) error) (InferenceResult, error)
	runPromptStreamSinkOverride      func(*http.Request, PromptRunRequest, InferenceStreamSink) (InferenceResult, error)
	runPromptWithSessionOverride     func(context.Context, AppConfig, SessionInfo, PromptRunRequest, func(string) error) (InferenceResult, error)
	runPromptWithSessionSinkOverride func(context.Context, AppConfig, SessionInfo, PromptRunRequest, InferenceStreamSink) (InferenceResult, error)
	accountProtocolProbeOverride     func(context.Context, AppConfig, SessionInfo) error
}

const (
	ephemeralConversationCleanupInterval  = time.Minute
	ephemeralConversationCleanupBatchSize = 24
	sillyTavernQuietConversationTTL       = 10 * time.Minute
	corsAllowOrigin                       = "*"
	corsAllowHeaders                      = "Authorization, Content-Type, X-Admin-Token"
	corsAllowMethods                      = "GET, POST, PUT, DELETE, OPTIONS"
)

var errRequestTooLarge = errors.New("request body too large")
var responseStorePruneTotalMetric = expvar.NewMap("notion2api_response_store_prune_total")
var testHookResponseStoreCleanupInterval time.Duration

type continuationTarget struct {
	Conversation ConversationEntry
	Session      *conversationContinuationState
}

type panicSafeResponseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *panicSafeResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *panicSafeResponseWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *panicSafeResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}

func normalizeAccountMaxConcurrency(raw int) int {
	if raw <= 0 {
		return 1
	}
	return raw
}

func clampSlotInFlight(slot *accountSlot, max int32) int32 {
	if slot == nil {
		return 0
	}
	if max <= 0 {
		max = 1
	}
	for {
		current := slot.inflight.Load()
		if current < 0 {
			if slot.inflight.CompareAndSwap(current, 0) {
				return 0
			}
			continue
		}
		if current <= max {
			return current
		}
		if slot.inflight.CompareAndSwap(current, max) {
			return max
		}
	}
}

func (s *ServerState) rebuildAccountSlotsLocked() {
	if s == nil {
		return
	}
	var previous map[string]*accountSlot
	if loaded := s.slots.Load(); loaded != nil {
		previous = *loaded
	}
	next := make(map[string]*accountSlot, len(s.Config.Accounts))
	for _, account := range s.Config.Accounts {
		emailKey := getAccountEmailKey(account)
		if emailKey == "" {
			continue
		}
		maxConcurrency := int32(normalizeAccountMaxConcurrency(account.MaxConcurrency))
		if existing := previous[emailKey]; existing != nil {
			existing.max.Store(maxConcurrency)
			clampSlotInFlight(existing, maxConcurrency)
			next[emailKey] = existing
			continue
		}
		slot := &accountSlot{}
		slot.max.Store(maxConcurrency)
		next[emailKey] = slot
	}
	s.slots.Store(&next)
	syncDispatchSlotInflightFromSlots(next)
}

func (s *ServerState) loadAccountSlots() map[string]*accountSlot {
	if s == nil {
		return nil
	}
	loaded := s.slots.Load()
	if loaded == nil {
		return nil
	}
	return *loaded
}

func (s *ServerState) TryAcquireAccountDispatchSlot(email string) bool {
	emailKey := canonicalEmailKey(email)
	if emailKey == "" {
		return false
	}
	slot := s.loadAccountSlots()[emailKey]
	if slot == nil {
		return false
	}
	for {
		maxConcurrency := slot.max.Load()
		if maxConcurrency <= 0 {
			maxConcurrency = 1
		}
		inflight := slot.inflight.Load()
		if inflight >= maxConcurrency {
			return false
		}
		if slot.inflight.CompareAndSwap(inflight, inflight+1) {
			setDispatchSlotInflight(emailKey, int(inflight+1))
			return true
		}
	}
}

func (s *ServerState) ReleaseAccountDispatchSlot(email string) {
	emailKey := canonicalEmailKey(email)
	if emailKey == "" {
		return
	}
	slot := s.loadAccountSlots()[emailKey]
	if slot == nil {
		return
	}
	for {
		inflight := slot.inflight.Load()
		if inflight <= 0 {
			setDispatchSlotInflight(emailKey, 0)
			return
		}
		if slot.inflight.CompareAndSwap(inflight, inflight-1) {
			setDispatchSlotInflight(emailKey, int(inflight-1))
			return
		}
	}
}

func (s *ServerState) RemainingAccountDispatchSlots(email string) int {
	emailKey := canonicalEmailKey(email)
	if emailKey == "" {
		return 0
	}
	slot := s.loadAccountSlots()[emailKey]
	if slot == nil {
		return 0
	}
	maxConcurrency := slot.max.Load()
	if maxConcurrency <= 0 {
		maxConcurrency = 1
	}
	inflight := slot.inflight.Load()
	remaining := int(maxConcurrency - inflight)
	if remaining < 0 {
		return 0
	}
	return remaining
}

func (s *ServerState) AvailableDispatchCapacity(emails []string) int {
	slots := s.loadAccountSlots()
	if len(slots) == 0 {
		return 0
	}
	total := 0
	seen := map[string]struct{}{}
	for _, email := range emails {
		emailKey := canonicalEmailKey(email)
		if emailKey == "" {
			continue
		}
		if _, exists := seen[emailKey]; exists {
			continue
		}
		seen[emailKey] = struct{}{}
		slot := slots[emailKey]
		if slot == nil {
			continue
		}
		maxConcurrency := slot.max.Load()
		if maxConcurrency <= 0 {
			maxConcurrency = 1
		}
		inflight := slot.inflight.Load()
		remaining := int(maxConcurrency - inflight)
		if remaining > 0 {
			total += remaining
		}
	}
	return total
}

func (s *ServerState) AccountDispatchSnapshot() map[string]accountDispatchState {
	slots := s.loadAccountSlots()
	out := make(map[string]accountDispatchState, len(slots))
	for key, slot := range slots {
		if slot == nil {
			continue
		}
		maxConcurrency := int(slot.max.Load())
		if maxConcurrency <= 0 {
			maxConcurrency = 1
		}
		inflight := int(slot.inflight.Load())
		if inflight < 0 {
			inflight = 0
		}
		if inflight > maxConcurrency {
			inflight = maxConcurrency
		}
		out[key] = accountDispatchState{
			MaxConcurrency: maxConcurrency,
			InFlight:       inflight,
		}
	}
	return out
}

func maxFloat(a float64, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func formatTimeOrEmpty(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339)
}

func validateConfiguredAPIKey(cfg AppConfig) error {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return fmt.Errorf("api key is required")
	}
	return nil
}

func newServerState(cfg AppConfig) (*ServerState, error) {
	cfg = normalizeConfig(cfg)
	if err := validateConfiguredAPIKey(cfg); err != nil {
		return nil, err
	}
	store, err := openSQLiteStore(cfg)
	if err != nil {
		return nil, err
	}
	state := &ServerState{
		Conversations:      newConversationStore(),
		AdminTokens:        map[string]time.Time{},
		AdminLoginAttempts: map[string]AdminLoginAttempt{},
		DispatchProbeCache: newProbeCache(),
		Store:              store,
	}
	state.ResponseStore = newResponseStore(time.Duration(maxInt(cfg.Responses.StoreTTLSeconds, 1)) * time.Second)
	persistedAccountsLoaded := false
	if store != nil {
		accounts, activeAccount, ok, loadErr := store.LoadAccounts()
		if loadErr != nil {
			_ = store.Close()
			return nil, loadErr
		}
		if ok {
			cfg.Accounts = accounts
			cfg.ActiveAccount = strings.TrimSpace(activeAccount)
			if cfg.ActiveAccount != "" {
				if account, _, found := cfg.FindAccount(cfg.ActiveAccount); found {
					cfg.ProbeJSON = account.ProbeJSON
				}
			} else if len(cfg.Accounts) > 0 {
				cfg.ProbeJSON = ""
			}
			persistedAccountsLoaded = true
		}
	}
	if err := state.ApplyConfig(cfg); err != nil {
		if store != nil {
			_ = store.Close()
		}
		return nil, err
	}
	if store != nil {
		if responsesPersistenceEnabled(state.Config) {
			responses, loadErr := store.LoadResponses(time.Duration(state.Config.Responses.StoreTTLSeconds) * time.Second)
			if loadErr != nil {
				_ = store.Close()
				return nil, loadErr
			}
			if state.ResponseStore == nil {
				state.ResponseStore = newResponseStore(time.Duration(maxInt(state.Config.Responses.StoreTTLSeconds, 1)) * time.Second)
			}
			state.ResponseStore.replaceAll(responses)
		}
		if conversationSnapshotsPersistenceEnabled(state.Config) {
			conversations, loadErr := store.LoadConversations()
			if loadErr != nil {
				_ = store.Close()
				return nil, loadErr
			}
			state.Conversations = newConversationStoreFromEntries(conversations)
		}
		if !persistedAccountsLoaded && (len(state.Config.Accounts) > 0 || strings.TrimSpace(state.Config.ActiveAccount) != "") {
			if saveErr := store.SaveAccounts(state.Config); saveErr != nil {
				_ = store.Close()
				return nil, saveErr
			}
		}
		state.sqliteWriter = newSQLiteWriter(store, time.Duration(maxInt(state.Config.Responses.StoreTTLSeconds, 1))*time.Second)
	}
	state.startResponseStoreCleanupLoop(context.Background())
	return state, nil
}

func (s *ServerState) ApplyConfig(cfg AppConfig) error {
	cfg = normalizeConfig(cfg)
	if err := validateConfiguredAPIKey(cfg); err != nil {
		return err
	}
	registry := buildModelRegistry(cfg)
	probePath, userName, spaceName, activeEmail := cfg.ResolveSessionTarget()
	session := SessionInfo{}
	var client *NotionAIClient
	if strings.TrimSpace(probePath) != "" {
		loadedSession, err := loadSessionInfo(probePath, userName, spaceName)
		if err != nil {
			log.Printf("[startup] session bootstrap skipped for probe=%s active=%s: %v", probePath, activeEmail, err)
		} else {
			session = loadedSession
			client = newNotionAIClient(loadedSession, cfg, activeEmail)
			if activeEmail != "" {
				cfg.ProbeJSON = loadedSession.ProbePath
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Config = cfg
	s.Session = session
	s.ModelRegistry = registry
	s.Client = client
	if s.sqliteWriter != nil {
		s.sqliteWriter.SetTTL(time.Duration(maxInt(cfg.Responses.StoreTTLSeconds, 1)) * time.Second)
	}
	s.rebuildAccountSlotsLocked()
	s.updateSnapshotBundleLocked()
	s.rebuildStaticJSONCachesLocked()
	return nil
}

func (s *ServerState) Snapshot() (AppConfig, SessionInfo, ModelRegistry) {
	if s == nil {
		return AppConfig{}, SessionInfo{}, ModelRegistry{}
	}
	if snap := s.snap.Load(); snap != nil {
		return snap.Config, snap.Session, snap.ModelRegistry
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Config, s.Session, s.ModelRegistry
}

func (s *ServerState) updateSnapshotBundleLocked() {
	if s == nil {
		return
	}
	now := time.Now()
	dispatchOrder := buildDispatchCandidateOrder(s.Config, now)
	bundle := &snapshotBundle{
		Config:        s.Config,
		Session:       s.Session,
		ModelRegistry: s.ModelRegistry,
		DispatchOrder: dispatchOrder,
	}
	s.snap.Store(bundle)
}

func (s *ServerState) SaveAndApply(cfg AppConfig) error {
	cfg = normalizeConfig(cfg)
	if err := validateConfiguredAPIKey(cfg); err != nil {
		return err
	}
	current, _, _ := s.Snapshot()
	if strings.TrimSpace(cfg.ConfigPath) != "" {
		if strings.TrimSpace(current.ConfigPath) != strings.TrimSpace(cfg.ConfigPath) || !persistedConfigEqual(current, cfg) {
			if err := saveConfigFile(cfg); err != nil {
				return err
			}
		}
	}
	if err := s.ApplyConfig(cfg); err != nil {
		return err
	}
	if s.Store != nil {
		if err := s.Store.SaveAccounts(cfg); err != nil {
			return err
		}
	}
	s.mu.Lock()
	if s.ResponseStore == nil {
		s.ResponseStore = newResponseStore(time.Duration(maxInt(cfg.Responses.StoreTTLSeconds, 1)) * time.Second)
	} else {
		s.ResponseStore.setTTL(time.Duration(maxInt(cfg.Responses.StoreTTLSeconds, 1)) * time.Second)
	}
	s.updateSnapshotBundleLocked()
	s.mu.Unlock()
	if canonicalEmailKey(current.ActiveAccount) != canonicalEmailKey(cfg.ActiveAccount) && s.DispatchProbeCache != nil {
		s.DispatchProbeCache.invalidateAll()
	}
	return nil
}

func (s *ServerState) conversationPersistenceStore() *SQLiteStore {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.Store == nil || !sqliteBackedConversationStorageAvailable(s.Config) {
		return nil
	}
	return s.Store
}

func (s *ServerState) saveResponse(responseID string, payload map[string]any, conversationID string, threadID string) {
	s.saveResponseWithAccount(responseID, payload, conversationID, threadID, "")
}

func (s *ServerState) saveResponseWithAccount(responseID string, payload map[string]any, conversationID string, threadID string, accountEmail string) {
	now := time.Now().UTC()
	s.mu.Lock()
	store := s.ResponseStore
	if store == nil {
		store = newResponseStore(time.Duration(maxInt(s.Config.Responses.StoreTTLSeconds, 1)) * time.Second)
		s.ResponseStore = store
	}
	store.save(responseID, StoredResponse{
		Payload:        payload,
		CreatedAt:      now,
		ConversationID: strings.TrimSpace(conversationID),
		ThreadID:       strings.TrimSpace(threadID),
		AccountEmail:   strings.TrimSpace(accountEmail),
	}, now)
	sqliteWriter := s.sqliteWriter
	sqliteStore := s.Store
	ttl := time.Duration(maxInt(s.Config.Responses.StoreTTLSeconds, 1)) * time.Second
	storeEnabled := sqliteStore != nil && responsesPersistenceEnabled(s.Config)
	s.mu.Unlock()
	if storeEnabled {
		if sqliteWriter != nil {
			sqliteWriter.EnqueueSaveResponse(responseID, payload, now, conversationID, threadID, accountEmail)
			return
		}
		if err := sqliteStore.SaveResponse(responseID, payload, now, conversationID, threadID, accountEmail); err != nil {
			log.Printf("[sqlite] save response %s failed: %v", responseID, err)
			return
		}
		if err := sqliteStore.DeleteExpiredResponses(ttl); err != nil {
			log.Printf("[sqlite] cleanup responses failed: %v", err)
		}
	}
}

func (s *ServerState) getResponse(responseID string) (map[string]any, bool) {
	record, ok := s.getStoredResponse(responseID)
	if !ok {
		return nil, false
	}
	return record.Payload, true
}

func (s *ServerState) getStoredResponse(responseID string) (StoredResponse, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ResponseStore == nil {
		return StoredResponse{}, false
	}
	return s.ResponseStore.get(responseID, time.Now().UTC())
}

func (s *ServerState) loadConversationContinuationStateByConversationID(conversationID string) (*conversationContinuationState, error) {
	store := s.conversationPersistenceStore()
	s.mu.RLock()
	enabled := continuationSessionsPersistenceEnabled(s.Config)
	s.mu.RUnlock()
	if store == nil || !enabled || strings.TrimSpace(conversationID) == "" {
		return nil, nil
	}
	session, ok, err := store.LoadConversationSessionByConversationID(conversationID)
	if err != nil || !ok {
		return nil, err
	}
	updatedConfigIDs, err := store.LoadConversationSessionStepIDs(session.ID)
	if err != nil {
		return nil, err
	}
	return &conversationContinuationState{
		Session:          session,
		UpdatedConfigIDs: updatedConfigIDs,
	}, nil
}

func (s *ServerState) loadConversationContinuationStateByThreadID(threadID string) (*conversationContinuationState, error) {
	store := s.conversationPersistenceStore()
	s.mu.RLock()
	enabled := continuationSessionsPersistenceEnabled(s.Config)
	s.mu.RUnlock()
	if store == nil || !enabled || strings.TrimSpace(threadID) == "" {
		return nil, nil
	}
	session, ok, err := store.LoadConversationSessionByThreadID(threadID)
	if err != nil || !ok {
		return nil, err
	}
	updatedConfigIDs, err := store.LoadConversationSessionStepIDs(session.ID)
	if err != nil {
		return nil, err
	}
	return &conversationContinuationState{
		Session:          session,
		UpdatedConfigIDs: updatedConfigIDs,
	}, nil
}

func (s *ServerState) loadConversationContinuationStateByFingerprint(fingerprint string) (*conversationContinuationState, error) {
	store := s.conversationPersistenceStore()
	s.mu.RLock()
	enabled := continuationSessionsPersistenceEnabled(s.Config)
	s.mu.RUnlock()
	if store == nil || !enabled || strings.TrimSpace(fingerprint) == "" {
		return nil, nil
	}
	session, ok, err := store.LoadConversationSessionByFingerprint(fingerprint)
	if err != nil || !ok {
		return nil, err
	}
	updatedConfigIDs, err := store.LoadConversationSessionStepIDs(session.ID)
	if err != nil {
		return nil, err
	}
	return &conversationContinuationState{
		Session:          session,
		UpdatedConfigIDs: updatedConfigIDs,
	}, nil
}

func (s *ServerState) deleteConversationSessionByConversationOrThread(conversationID string, threadID string) {
	store := s.conversationPersistenceStore()
	s.mu.RLock()
	enabled := continuationSessionsPersistenceEnabled(s.Config)
	s.mu.RUnlock()
	if store == nil || !enabled {
		return
	}
	if err := store.DeleteConversationSessionByConversationOrThread(conversationID, threadID); err != nil {
		log.Printf("[sqlite] delete continuation session conversation=%s thread=%s failed: %v", conversationID, threadID, err)
	}
}

func (s *ServerState) invalidateConversationSession(sessionID string, status string) {
	store := s.conversationPersistenceStore()
	s.mu.RLock()
	enabled := continuationSessionsPersistenceEnabled(s.Config)
	s.mu.RUnlock()
	if store == nil || !enabled || strings.TrimSpace(sessionID) == "" {
		return
	}
	if err := store.MarkConversationSessionStatus(sessionID, status); err != nil {
		log.Printf("[sqlite] update continuation session status session=%s status=%s failed: %v", sessionID, status, err)
	}
}

func (s *ServerState) Close() error {
	s.mu.RLock()
	store := s.Store
	cancelCleanup := s.responseStoreCleanupCancel
	sqliteWriter := s.sqliteWriter
	s.mu.RUnlock()
	if cancelCleanup != nil {
		cancelCleanup()
	}
	if sqliteWriter != nil {
		sqliteWriter.Close()
	}
	if store == nil {
		return nil
	}
	return store.Close()
}

func (s *ServerState) startResponseStoreCleanupLoop(parent context.Context) {
	if s == nil {
		return
	}
	if parent == nil {
		parent = context.Background()
	}
	interval := responseStoreCleanupInterval
	if testHookResponseStoreCleanupInterval > 0 {
		interval = testHookResponseStoreCleanupInterval
	}
	ctx, cancel := context.WithCancel(parent)
	s.mu.Lock()
	s.responseStoreCleanupCancel = cancel
	s.mu.Unlock()
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.runResponseStoreCleanupOnce(time.Now().UTC())
			}
		}
	}()
}

func (s *ServerState) runResponseStoreCleanupOnce(now time.Time) int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ResponseStore == nil {
		return 0
	}
	removed := s.ResponseStore.pruneExpired(now)
	if removed > 0 {
		responseStorePruneTotalMetric.Add("expired_entries", int64(removed))
	}
	return removed
}

func buildPublicModelPayload(entry ModelDefinition) publicModelPayload {
	return publicModelPayload{
		ID:          entry.ID,
		Object:      "model",
		Created:     0,
		OwnedBy:     "notion2api",
		Name:        entry.Name,
		Family:      entry.Family,
		Group:       entry.Group,
		Beta:        entry.Beta,
		NotionModel: entry.NotionModel,
	}
}

func buildPublicModelsListPayload(registry ModelRegistry) publicModelsListPayload {
	items := make([]publicModelPayload, 0, len(registry.Entries))
	for _, entry := range registry.Entries {
		if !entry.Enabled {
			continue
		}
		items = append(items, buildPublicModelPayload(entry))
	}
	return publicModelsListPayload{
		Object: "list",
		Data:   items,
	}
}

func cloneBytes(src []byte) []byte {
	if len(src) == 0 {
		return nil
	}
	dst := make([]byte, len(src))
	copy(dst, src)
	return dst
}

func cloneBytesMap(src map[string][]byte) map[string][]byte {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string][]byte, len(src))
	for key, value := range src {
		dst[key] = cloneBytes(value)
	}
	return dst
}

func (s *ServerState) rebuildStaticJSONCachesLocked() {
	healthPayload := healthzStaticPayload{
		OK:                   true,
		DefaultModel:         s.Config.DefaultPublicModel(),
		ModelCount:           len(s.ModelRegistry.Entries),
		UserEmail:            s.Session.UserEmail,
		SpaceID:              s.Session.SpaceID,
		ActiveAccount:        s.Config.ActiveAccount,
		SessionRefreshEnable: s.Config.ResolveSessionRefresh().Enabled,
	}
	healthBody, err := json.Marshal(healthPayload)
	if err == nil {
		healthBodyCopy := cloneBytes(healthBody)
		s.cachedHealthzStaticJSON.Store(&healthBodyCopy)
	} else {
		s.cachedHealthzStaticJSON.Store(nil)
	}

	modelsPayload := buildPublicModelsListPayload(s.ModelRegistry)
	modelsBody, err := json.Marshal(modelsPayload)
	if err == nil {
		modelsBodyCopy := cloneBytes(modelsBody)
		s.cachedModelsListJSON.Store(&modelsBodyCopy)
	} else {
		s.cachedModelsListJSON.Store(nil)
	}

	modelByID := make(map[string][]byte, len(s.ModelRegistry.Entries))
	for _, entry := range s.ModelRegistry.Entries {
		if !entry.Enabled {
			continue
		}
		body, marshalErr := json.Marshal(buildPublicModelPayload(entry))
		if marshalErr != nil {
			continue
		}
		modelByID[normalizeLookupKey(entry.ID)] = cloneBytes(body)
	}
	modelByIDCopy := cloneBytesMap(modelByID)
	s.cachedModelByIDJSON.Store(&modelByIDCopy)
}

func writeJSONBytes(w http.ResponseWriter, status int, body []byte) {
	applyCORSHeaders(w)
	w.Header().Set("X-Notion2API", "1")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func appendHealthzRuntimeFields(body []byte, sessionReady bool, lastRefresh time.Time, lastRefreshError string) []byte {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[len(trimmed)-1] != '}' {
		trimmed = []byte(`{"ok":true}`)
	}
	trimmed = bytes.TrimSuffix(trimmed, []byte("}"))
	tail := map[string]any{
		"session_ready":              sessionReady,
		"last_session_refresh":       formatTimeOrEmpty(lastRefresh),
		"last_session_refresh_error": lastRefreshError,
	}
	tailBody, err := json.Marshal(tail)
	if err != nil {
		return body
	}
	tailBody = bytes.TrimPrefix(tailBody, []byte("{"))
	out := make([]byte, 0, len(trimmed)+1+len(tailBody))
	out = append(out, trimmed...)
	if len(trimmed) > 1 {
		out = append(out, ',')
	}
	out = append(out, tailBody...)
	return out
}

func applyCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", corsAllowOrigin)
	w.Header().Set("Access-Control-Allow-Headers", corsAllowHeaders)
	w.Header().Set("Access-Control-Allow-Methods", corsAllowMethods)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	applyCORSHeaders(w)
	w.Header().Set("X-Notion2API", "1")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeOpenAIError(w http.ResponseWriter, status int, message string, errorType string, code string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errorType,
			"param":   nil,
			"code":    code,
		},
	})
}

func writeInvalidBodyError(w http.ResponseWriter, err error) {
	if errors.Is(err, errRequestTooLarge) {
		writeOpenAIError(w, http.StatusRequestEntityTooLarge, "request body exceeds configured limit", "invalid_request_error", "request_too_large")
		return
	}
	writeOpenAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", nilString())
}

func nilString() string {
	return ""
}

func decodeBodyWithLimit(w http.ResponseWriter, r *http.Request, maxBytes int64) (map[string]any, error) {
	raw, err := decodeBodyRawWithLimit(w, r, maxBytes)
	if err != nil {
		return nil, err
	}
	return decodeBodyMapFromRaw(raw)
}

func decodeBodyMapFromRaw(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("invalid json: %w", err)
	}
	if payload == nil {
		payload = map[string]any{}
	}
	return payload, nil
}

func decodeBodyRawWithLimit(w http.ResponseWriter, r *http.Request, maxBytes int64) ([]byte, error) {
	if maxBytes > 0 && w != nil {
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	}
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, errRequestTooLarge
		}
		return nil, fmt.Errorf("invalid json: %w", err)
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return []byte("{}"), nil
	}
	var raw json.RawMessage
	if err := json.Unmarshal(trimmed, &raw); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, errRequestTooLarge
		}
		return nil, fmt.Errorf("invalid json: %w", err)
	}
	normalized := bytes.TrimSpace(raw)
	if len(normalized) == 0 {
		return []byte("{}"), nil
	}
	return normalized, nil
}

func (a *App) decodeBody(w http.ResponseWriter, r *http.Request) (map[string]any, error) {
	raw, err := a.decodeBodyRaw(w, r)
	if err != nil {
		return nil, err
	}
	return decodeBodyMapFromRaw(raw)
}

func (a *App) decodeBodyRaw(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	maxBytes := int64(0)
	if a != nil && a.State != nil {
		cfg, _, _ := a.State.Snapshot()
		maxBytes = cfg.Limits.MaxRequestBodyBytes
	}
	return decodeBodyRawWithLimit(w, r, maxBytes)
}

func decodeTypedBodyFromRaw[T any](raw []byte) (T, error) {
	var typed T
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&typed); err != nil {
		return typed, fmt.Errorf("invalid json: %w", err)
	}
	return typed, nil
}

func (a *App) authOK(w http.ResponseWriter, r *http.Request) bool {
	cfg, _, _ := a.State.Snapshot()
	expected := strings.TrimSpace(cfg.APIKey)
	if expected == "" {
		writeOpenAIError(w, http.StatusServiceUnavailable, "server api key is not configured", "server_error", "api_key_required")
		return false
	}
	if strings.TrimSpace(r.Header.Get("Authorization")) == "Bearer "+expected {
		return true
	}
	writeOpenAIError(w, http.StatusUnauthorized, "invalid api key", "authentication_error", "invalid_api_key")
	return false
}

func (a *App) serveHealthz(w http.ResponseWriter) {
	a.State.mu.RLock()
	sessionReady := a.State.Client != nil
	lastRefresh := a.State.LastSessionRefresh
	lastRefreshError := a.State.LastSessionRefreshError
	cached := a.State.cachedHealthzStaticJSON.Load()
	a.State.mu.RUnlock()
	if cached != nil {
		body := appendHealthzRuntimeFields(*cached, sessionReady, lastRefresh, lastRefreshError)
		writeJSONBytes(w, http.StatusOK, body)
		return
	}
	cfg, session, registry := a.State.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                         true,
		"default_model":              cfg.DefaultPublicModel(),
		"model_count":                len(registry.Entries),
		"user_email":                 session.UserEmail,
		"space_id":                   session.SpaceID,
		"active_account":             cfg.ActiveAccount,
		"session_ready":              sessionReady,
		"session_refresh_enabled":    cfg.ResolveSessionRefresh().Enabled,
		"last_session_refresh":       formatTimeOrEmpty(lastRefresh),
		"last_session_refresh_error": lastRefreshError,
	})
}

func (a *App) serveModels(w http.ResponseWriter) {
	cached := a.State.cachedModelsListJSON.Load()
	if cached != nil {
		writeJSONBytes(w, http.StatusOK, *cached)
		return
	}
	_, _, registry := a.State.Snapshot()
	writeJSON(w, http.StatusOK, buildPublicModelsListPayload(registry))
}

func (a *App) serveModelByID(w http.ResponseWriter, path string) {
	cfg, _, registry := a.State.Snapshot()
	modelID := strings.TrimSpace(strings.TrimPrefix(path, "/v1/models/"))
	entry, err := registry.Resolve(modelID, cfg.DefaultPublicModel())
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, "model not found", "invalid_request_error", "model_not_found")
		return
	}
	if cached := a.State.cachedModelByIDJSON.Load(); cached != nil {
		if body, ok := (*cached)[normalizeLookupKey(entry.ID)]; ok && len(body) > 0 {
			writeJSONBytes(w, http.StatusOK, body)
			return
		}
	}
	writeJSON(w, http.StatusOK, buildPublicModelPayload(entry))
}

func (a *App) serveResponseByID(w http.ResponseWriter, path string) {
	responseID := strings.TrimSpace(strings.TrimPrefix(path, "/v1/responses/"))
	payload, ok := a.State.getResponse(responseID)
	if !ok {
		writeOpenAIError(w, http.StatusNotFound, "response not found", "invalid_request_error", "response_not_found")
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func requestedModel(payload map[string]any, fallback string) string {
	modelID := strings.TrimSpace(stringValue(payload["model"]))
	if modelID == "" {
		return fallback
	}
	return modelID
}

func parseBoolField(value any) (bool, bool) {
	switch raw := value.(type) {
	case bool:
		return raw, true
	case string:
		clean := strings.TrimSpace(strings.ToLower(raw))
		switch clean {
		case "true", "1", "yes", "on":
			return true, true
		case "false", "0", "no", "off":
			return false, true
		}
	}
	return false, false
}

func requestedWebSearch(payload map[string]any, fallback bool) bool {
	if value, ok := parseBoolField(payload["use_web_search"]); ok {
		return value
	}
	if meta := mapValue(payload["metadata"]); meta != nil {
		if value, ok := parseBoolField(meta["use_web_search"]); ok {
			return value
		}
		if value, ok := parseBoolField(meta["notion_use_web_search"]); ok {
			return value
		}
	}
	for _, rawTool := range sliceValue(payload["tools"]) {
		tool := mapValue(rawTool)
		toolType := strings.TrimSpace(stringValue(tool["type"]))
		if strings.Contains(toolType, "web_search") {
			return true
		}
	}
	return fallback
}

func firstRequestValue(r *http.Request, keys ...string) string {
	if r != nil {
		for _, key := range keys {
			if value := strings.TrimSpace(r.Header.Get(key)); value != "" {
				return value
			}
		}
	}
	return ""
}

func requestedConversationID(r *http.Request, payload map[string]any) string {
	if conversationID := firstRequestValue(r, "X-Conversation-ID", "X-Notion-Conversation-ID"); conversationID != "" {
		return conversationID
	}
	if conversationID := strings.TrimSpace(stringValue(payload["conversation_id"])); conversationID != "" {
		return conversationID
	}
	if conversationID := strings.TrimSpace(stringValue(payload["conversation"])); conversationID != "" {
		return conversationID
	}
	if meta := mapValue(payload["metadata"]); meta != nil {
		for _, key := range []string{"conversation_id", "notion_conversation_id"} {
			if conversationID := strings.TrimSpace(stringValue(meta[key])); conversationID != "" {
				return conversationID
			}
		}
	}
	return ""
}

func requestedThreadID(r *http.Request, payload map[string]any) string {
	if threadID := firstRequestValue(r, "X-Thread-ID", "X-Notion-Thread-ID"); threadID != "" {
		return threadID
	}
	for _, key := range []string{"thread_id", "thread", "notion_thread_id"} {
		if threadID := strings.TrimSpace(stringValue(payload[key])); threadID != "" {
			return threadID
		}
	}
	if meta := mapValue(payload["metadata"]); meta != nil {
		for _, key := range []string{"thread_id", "notion_thread_id"} {
			if threadID := strings.TrimSpace(stringValue(meta[key])); threadID != "" {
				return threadID
			}
		}
	}
	return ""
}

func requestedAccountEmail(r *http.Request, payload map[string]any) string {
	if accountEmail := firstRequestValue(r, "X-Account-Email", "X-Notion-Account-Email"); accountEmail != "" {
		return accountEmail
	}
	for _, key := range []string{"account_email", "notion_account_email"} {
		if accountEmail := strings.TrimSpace(stringValue(payload[key])); accountEmail != "" {
			return accountEmail
		}
	}
	if meta := mapValue(payload["metadata"]); meta != nil {
		for _, key := range []string{"account_email", "notion_account_email"} {
			if accountEmail := strings.TrimSpace(stringValue(meta[key])); accountEmail != "" {
				return accountEmail
			}
		}
	}
	return ""
}

func preferActiveAccountForRequest(cfg AppConfig, request *PromptRunRequest) {
	if request == nil || strings.TrimSpace(request.PinnedAccountEmail) != "" {
		return
	}
	if account, _, ok := cfg.ResolveActiveAccount(); ok {
		if email := strings.TrimSpace(account.Email); email != "" {
			request.PinnedAccountEmail = email
			request.AllowPinnedAccountFallback = true
		}
	}
}

func resolveRequestPromptForContinuation(normalized NormalizedInput) string {
	return firstNonEmpty(strings.TrimSpace(normalized.DisplayPrompt), strings.TrimSpace(normalized.Prompt))
}

func forceFreshThreadPerRequest(cfg AppConfig) bool {
	return cfg.Features.ForceFreshThreadPerRequest
}

func latestReplayPrompt(latestPrompt string, attachments []InputAttachment, fallback string) string {
	clean := strings.TrimSpace(latestPrompt)
	if clean == "" && len(attachments) > 0 {
		clean = defaultUploadedAttachmentPrompt
	}
	return firstNonEmpty(clean, strings.TrimSpace(fallback))
}

func buildFreshThreadReplayPromptFromConversation(conversation ConversationEntry, latestPrompt string, attachments []InputAttachment, fallback string) string {
	segments := conversationMessageSegments(&conversation)
	if len(segments) == 0 {
		return latestReplayPrompt(latestPrompt, attachments, fallback)
	}
	cleanLatest := latestReplayPrompt(latestPrompt, attachments, "")
	if cleanLatest != "" {
		last := segments[len(segments)-1]
		if last.Role != "user" || collapseWhitespace(last.Text) != collapseWhitespace(cleanLatest) {
			segments = append(segments, conversationPromptSegment{
				Role: "user",
				Text: cleanLatest,
			})
		}
	}
	if prompt := buildConversationTranscriptPrompt(segments); strings.TrimSpace(prompt) != "" {
		return prompt
	}
	return latestReplayPrompt(latestPrompt, attachments, fallback)
}

func buildFreshThreadReplayPromptFromStoredResponse(previousResponsePrompt string, latestPrompt string, attachments []InputAttachment, fallback string) string {
	cleanPrevious := strings.TrimSpace(previousResponsePrompt)
	if cleanPrevious == "" {
		return latestReplayPrompt(latestPrompt, attachments, fallback)
	}
	parts := []string{
		"Continue the conversation using the transcript below. Reply as the assistant to the final [user] message only. Do not mention or repeat the role labels in your reply.",
		cleanPrevious,
	}
	if cleanLatest := latestReplayPrompt(latestPrompt, attachments, ""); cleanLatest != "" {
		parts = append(parts, formatPromptSection("user", cleanLatest))
	}
	if prompt := strings.TrimSpace(strings.Join(parts, "\n\n")); prompt != "" {
		return prompt
	}
	return latestReplayPrompt(latestPrompt, attachments, fallback)
}

func setConversationIDHeader(w http.ResponseWriter, conversationID string) {
	if w == nil {
		return
	}
	if conversationID = strings.TrimSpace(conversationID); conversationID != "" {
		w.Header().Set("X-Conversation-ID", conversationID)
	}
}

func setThreadIDHeader(w http.ResponseWriter, threadID string) {
	if w == nil {
		return
	}
	if threadID = strings.TrimSpace(threadID); threadID != "" {
		w.Header().Set("X-Notion-Thread-ID", threadID)
	}
}

func attachConversationResponseMetadata(payload map[string]any, conversationID string, threadID string) {
	if payload == nil {
		return
	}
	conversationID = strings.TrimSpace(conversationID)
	threadID = strings.TrimSpace(threadID)
	if conversationID != "" {
		payload["conversation_id"] = conversationID
	}
	if threadID != "" {
		payload["thread_id"] = threadID
	}
	if trace := mapValue(payload["notion_trace"]); trace != nil {
		if conversationID != "" {
			trace["conversation_id"] = conversationID
		}
		if threadID != "" {
			trace["thread_id"] = threadID
		}
	}
}

func (a *App) resolveContinuationConversation(r *http.Request, payload map[string]any, previousResponseID string, hiddenPrompt string, segments []conversationPromptSegment) (continuationTarget, bool) {
	explicitConversationID := requestedConversationID(r, payload)
	explicitThreadID := requestedThreadID(r, payload)
	return a.resolveContinuationConversationWithExplicit(previousResponseID, hiddenPrompt, segments, explicitConversationID, explicitThreadID)
}

func (a *App) resolveContinuationConversationWithExplicit(previousResponseID string, hiddenPrompt string, segments []conversationPromptSegment, explicitConversationID string, explicitThreadID string) (continuationTarget, bool) {
	rawCount := sessionRawMessageCount(segments)
	validateState := func(state *conversationContinuationState) bool {
		if state == nil {
			return true
		}
		if shouldInvalidateConversationSession(state.Session, rawCount) {
			a.State.invalidateConversationSession(state.Session.ID, conversationSessionStatusStale)
			return false
		}
		return true
	}
	if explicitConversationID != "" {
		if entry, ok := a.State.conversations().Get(explicitConversationID); ok && strings.TrimSpace(entry.ThreadID) != "" {
			state, err := a.State.loadConversationContinuationStateByConversationID(entry.ID)
			if err == nil && !validateState(state) {
				return continuationTarget{Conversation: entry}, true
			}
			if err == nil {
				return continuationTarget{Conversation: entry, Session: state}, true
			}
			return continuationTarget{Conversation: entry}, true
		}
		if state, err := a.State.loadConversationContinuationStateByConversationID(explicitConversationID); err == nil && state != nil {
			if !validateState(state) {
				entry := ConversationEntry{
					ID:           strings.TrimSpace(state.Session.ConversationID),
					ThreadID:     strings.TrimSpace(state.Session.ThreadID),
					AccountEmail: strings.TrimSpace(state.Session.AccountEmail),
				}
				return continuationTarget{Conversation: entry}, true
			}
			entry := ConversationEntry{
				ID:           strings.TrimSpace(state.Session.ConversationID),
				ThreadID:     strings.TrimSpace(state.Session.ThreadID),
				AccountEmail: strings.TrimSpace(state.Session.AccountEmail),
			}
			return continuationTarget{Conversation: entry, Session: state}, true
		}
		return continuationTarget{}, false
	}
	if previousResponseID != "" {
		if stored, ok := a.State.getStoredResponse(previousResponseID); ok {
			if stored.ConversationID != "" {
				if entry, found := a.State.conversations().Get(stored.ConversationID); found && strings.TrimSpace(entry.ThreadID) != "" {
					if strings.TrimSpace(entry.AccountEmail) == "" {
						entry.AccountEmail = strings.TrimSpace(stored.AccountEmail)
					}
					state, err := a.State.loadConversationContinuationStateByConversationID(entry.ID)
					if err == nil && !validateState(state) {
						return continuationTarget{}, false
					}
					if err == nil {
						return continuationTarget{Conversation: entry, Session: state}, true
					}
					return continuationTarget{Conversation: entry}, true
				}
			}
			if stored.ThreadID != "" {
				target := continuationTarget{Conversation: ConversationEntry{
					ThreadID:     stored.ThreadID,
					AccountEmail: strings.TrimSpace(stored.AccountEmail),
				}}
				if state, err := a.State.loadConversationContinuationStateByThreadID(stored.ThreadID); err == nil {
					if !validateState(state) {
						return continuationTarget{}, false
					}
					target.Session = state
				}
				return target, true
			}
		}
	}
	if explicitThreadID != "" {
		if entry, ok := a.State.conversations().FindByThreadID(explicitThreadID); ok {
			state, err := a.State.loadConversationContinuationStateByThreadID(explicitThreadID)
			if err == nil && !validateState(state) {
				return continuationTarget{}, false
			}
			if err == nil {
				return continuationTarget{Conversation: entry, Session: state}, true
			}
			return continuationTarget{Conversation: entry}, true
		}
		target := continuationTarget{Conversation: ConversationEntry{
			ThreadID: explicitThreadID,
		}}
		if state, err := a.State.loadConversationContinuationStateByThreadID(explicitThreadID); err == nil {
			if !validateState(state) {
				return continuationTarget{}, false
			}
			target.Session = state
		}
		return target, true
	}
	fingerprint := canonicalConversationFingerprint(hiddenPrompt, segments)
	if state, err := a.State.loadConversationContinuationStateByFingerprint(fingerprint); err == nil && state != nil {
		if !validateState(state) {
			return continuationTarget{}, false
		}
		if rawCount >= state.Session.RawMessageCount && strings.TrimSpace(state.Session.ThreadID) != "" {
			entry := ConversationEntry{
				ID:           strings.TrimSpace(state.Session.ConversationID),
				ThreadID:     strings.TrimSpace(state.Session.ThreadID),
				AccountEmail: strings.TrimSpace(state.Session.AccountEmail),
			}
			if existing, ok := a.State.conversations().Get(entry.ID); ok {
				entry = existing
			}
			return continuationTarget{Conversation: entry, Session: state}, true
		}
	}
	if history := continuationHistorySegments(segments); len(history) > 0 {
		if entry, ok := a.State.conversations().FindContinuationBySegments(history); ok {
			state, err := a.State.loadConversationContinuationStateByConversationID(entry.ID)
			if err == nil && !validateState(state) {
				return continuationTarget{}, false
			}
			if err == nil {
				return continuationTarget{Conversation: entry, Session: state}, true
			}
			return continuationTarget{Conversation: entry}, true
		}
	}
	return continuationTarget{}, false
}

func (a *App) startConversationTurn(existingConversationID string, preferredConversationID string, source string, transport string, displayPrompt string, request PromptRunRequest) string {
	if existingConversationID != "" && (strings.TrimSpace(request.UpstreamThreadID) != "" || request.ForceLocalConversationContinue) {
		if conversationID, err := a.continueConversation(existingConversationID, source, transport, displayPrompt, request); err == nil {
			return conversationID
		}
	}
	return a.beginConversation(preferredConversationID, source, transport, displayPrompt, request)
}

func (a *App) markEphemeralConversationRequest(request *PromptRunRequest) {
	if request == nil {
		return
	}
	if request.ClientProfile != sillyTavernClientProfile {
		return
	}
	if request.EphemeralConversation {
		return
	}
	switch request.ClientMode {
	case sillyTavernModeQuiet:
		request.EphemeralConversation = true
		request.EphemeralReason = "sillytavern_quiet"
	case sillyTavernModeImpersona:
		request.EphemeralConversation = true
		request.EphemeralReason = "sillytavern_impersonate"
	default:
		return
	}
}

func (a *App) cleanupExpiredEphemeralConversations() {
	if a == nil || a.State == nil {
		return
	}
	expired := a.State.conversations().ListExpiredEphemeral(time.Now().UTC(), ephemeralConversationCleanupBatchSize)
	for _, entry := range expired {
		if err := a.deleteConversation(entry.ID); err != nil {
			log.Printf("[cleanup] delete expired ephemeral conversation=%s thread=%s reason=%s failed: %v", entry.ID, entry.ThreadID, entry.EphemeralReason, err)
			continue
		}
		log.Printf("[cleanup] deleted expired ephemeral conversation=%s thread=%s reason=%s", entry.ID, entry.ThreadID, entry.EphemeralReason)
	}
}

func (a *App) StartEphemeralConversationCleanupLoop(parent context.Context) {
	if a == nil || a.State == nil {
		return
	}
	go func() {
		a.cleanupExpiredEphemeralConversations()
		timer := time.NewTimer(ephemeralConversationCleanupInterval)
		defer timer.Stop()
		for {
			select {
			case <-parent.Done():
				return
			case <-timer.C:
				a.cleanupExpiredEphemeralConversations()
				timer.Reset(ephemeralConversationCleanupInterval)
			}
		}
	}()
}

func includeUsageInStream(payload map[string]any) bool {
	options := mapValue(payload["stream_options"])
	includeUsage, _ := options["include_usage"].(bool)
	return includeUsage
}

func decodeChatCompletionsRequestBodyFromRaw(raw []byte) (chatCompletionsRequestBody, map[string]any, error) {
	typed, err := decodeTypedBodyFromRaw[chatCompletionsRequestBody](raw)
	if err == nil {
		return normalizeTypedChatCompletionsRequestBody(typed), nil, nil
	}
	payload, mapErr := decodeBodyMapFromRaw(raw)
	if mapErr != nil {
		return chatCompletionsRequestBody{}, nil, mapErr
	}
	return extractChatCompletionsRequestBody(payload), payload, nil
}

func decodeResponsesRequestBodyFromRaw(raw []byte) (responsesRequestBody, map[string]any, error) {
	typed, err := decodeTypedBodyFromRaw[responsesRequestBody](raw)
	if err == nil {
		return normalizeTypedResponsesRequestBody(typed), nil, nil
	}
	payload, mapErr := decodeBodyMapFromRaw(raw)
	if mapErr != nil {
		return responsesRequestBody{}, nil, mapErr
	}
	return extractResponsesRequestBody(payload), payload, nil
}

func maybeSillyTavernByTypedMessages(rawMessages any) bool {
	items := sliceValue(rawMessages)
	if len(items) == 0 {
		return false
	}
	systemPrompts := make([]string, 0, len(items))
	for _, raw := range items {
		msg := mapValue(raw)
		if msg == nil {
			continue
		}
		if strings.TrimSpace(strings.ToLower(stringValue(msg["role"]))) != "system" {
			continue
		}
		text := collapseWhitespace(flattenContent(msg["content"]))
		if text != "" {
			systemPrompts = append(systemPrompts, text)
		}
	}
	if len(systemPrompts) == 0 {
		return false
	}
	if looksLikeSillyTavernImpersonate(systemPrompts) || looksLikeSillyTavernQuiet(systemPrompts, nil) {
		return true
	}
	for _, prompt := range systemPrompts {
		lower := strings.ToLower(collapseWhitespace(prompt))
		if strings.Contains(lower, "fictional chat between") ||
			strings.Contains(lower, "[start a new chat]") ||
			strings.Contains(lower, "[continue your last message without repeating its original content.]") {
			return true
		}
	}
	return false
}

func rawMayNeedSillyTavernPayloadFallback(raw []byte) bool {
	return bytes.Contains(raw, []byte(`"continue_prefill"`)) || bytes.Contains(raw, []byte(`"show_thoughts"`))
}

func chatCompletionInitialFlushDelayForRequest(request PromptRunRequest) time.Duration {
	if request.ClientProfile == sillyTavernClientProfile || request.StreamReasoningWarmup {
		return 0
	}
	return chatCompletionInitialFlushDelay
}

func applyInferenceResultOutputPolicy(result InferenceResult, request PromptRunRequest) InferenceResult {
	result.Text = sanitizeAssistantVisibleText(result.Text)
	result.Reasoning = sanitizeAssistantVisibleText(result.Reasoning)
	if request.SuppressReasoningOutput {
		result.Reasoning = ""
	}
	return result
}

func (a *App) runPrompt(r *http.Request, request PromptRunRequest) (InferenceResult, error) {
	if a.runPromptOverride != nil {
		return a.runPromptOverride(r, request)
	}
	return a.runPromptWithAccountPool(r, request, nil)
}

func (a *App) runPromptStream(r *http.Request, request PromptRunRequest, onDelta func(string) error) (InferenceResult, error) {
	if a.runPromptStreamOverride != nil {
		return a.runPromptStreamOverride(r, request, onDelta)
	}
	return a.runPromptWithAccountPool(r, request, onDelta)
}

func (a *App) runPromptStreamWithSink(r *http.Request, request PromptRunRequest, sink InferenceStreamSink) (InferenceResult, error) {
	if a.runPromptStreamSinkOverride != nil {
		return a.runPromptStreamSinkOverride(r, request, sink)
	}
	if a.runPromptStreamOverride != nil {
		return a.runPromptStreamOverride(r, request, sink.Text)
	}
	return a.runPromptWithAccountPoolWithSink(r, request, sink)
}

func (a *App) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	raw, err := a.decodeBodyRaw(w, r)
	if err != nil {
		writeInvalidBodyError(w, err)
		return
	}
	typed, payload, err := decodeChatCompletionsRequestBodyFromRaw(raw)
	if err != nil {
		writeInvalidBodyError(w, err)
		return
	}
	if payload == nil && (typed.likelySillyTavernByEnvelope() || maybeSillyTavernByTypedMessages(typed.Messages) || rawMayNeedSillyTavernPayloadFallback(raw)) {
		payload, err = decodeBodyMapFromRaw(raw)
		if err != nil {
			writeInvalidBodyError(w, err)
			return
		}
	}
	if payload != nil && (typed.likelySillyTavernByEnvelope() || isLikelySillyTavernPayload(payload)) {
		a.handleSillyTavernChatCompletionsPayload(w, r, payload)
		return
	}
	messages := sliceValue(typed.Messages)
	if len(messages) == 0 {
		writeOpenAIError(w, http.StatusBadRequest, "messages must be an array", "invalid_request_error", nilString())
		return
	}
	normalized, err := normalizeChatInputFromParts(messages, typed.Attachments)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", nilString())
		return
	}
	if normalized.Prompt == "" {
		writeOpenAIError(w, http.StatusBadRequest, "messages must contain text or supported attachments", "invalid_request_error", nilString())
		return
	}
	cfg, _, registry := a.State.Snapshot()
	requestedModelID := requestedModelFromTyped(typed.Model, cfg.DefaultPublicModel())
	useWebSearch := requestedWebSearchFromTyped(typed.UseWebSearch, typed.Metadata, typed.Tools, cfg.Features.UseWebSearch)
	preferredConversationID := requestedConversationIDFromTyped(r, typed.ConversationID, typed.Conversation, typed.Metadata)
	explicitThreadID := requestedThreadIDFromTyped(r, typed.ThreadID, typed.Thread, typed.NotionThreadID, typed.Metadata)
	requestedAccount := requestedAccountEmailFromTyped(r, typed.AccountEmail, typed.NotionAccountEmail, typed.Metadata)
	entry, err := registry.Resolve(requestedModelID, cfg.DefaultPublicModel())
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "model_not_found")
		return
	}
	hiddenPrompt := strings.TrimSpace(normalized.HiddenPrompt)
	promptText := normalized.Prompt
	latestPrompt := resolveRequestPromptForContinuation(normalized)
	originalFingerprint := canonicalConversationFingerprint(hiddenPrompt, normalized.Segments)
	originalRawMessageCount := sessionRawMessageCount(normalized.Segments)
	request := PromptRunRequest{
		Prompt:             promptText,
		LatestUserPrompt:   latestPrompt,
		HiddenPrompt:       hiddenPrompt,
		PublicModel:        entry.ID,
		NotionModel:        entry.NotionModel,
		UseWebSearch:       useWebSearch,
		Attachments:        normalized.Attachments,
		SessionFingerprint: originalFingerprint,
		RawMessageCount:    originalRawMessageCount,
	}
	freshThreadMode := forceFreshThreadPerRequest(cfg)
	conversation := ConversationEntry{}
	if matched, ok := a.resolveContinuationConversationWithExplicit("", hiddenPrompt, normalized.Segments, preferredConversationID, explicitThreadID); ok {
		conversation = matched.Conversation
		request.PinnedAccountEmail = firstNonEmpty(strings.TrimSpace(conversation.AccountEmail), requestedAccount)
		if freshThreadMode {
			request.ForceLocalConversationContinue = strings.TrimSpace(conversation.ID) != ""
			request.Prompt = buildFreshThreadReplayPromptFromConversation(conversation, latestPrompt, normalized.Attachments, promptText)
		} else {
			request.UpstreamThreadID = strings.TrimSpace(conversation.ThreadID)
			request.continuationDraft = buildContinuationDraft(matched.Session)
			if matched.Session != nil && (request.RawMessageCount == matched.Session.Session.RawMessageCount || request.ForceSessionRepeatTurn) {
				request.SessionRepeatTurn = true
			}
			request.Prompt = latestPrompt
		}
	} else {
		request.PinnedAccountEmail = requestedAccount
	}
	request.ConversationID = firstNonEmpty(strings.TrimSpace(conversation.ID), preferredConversationID)
	conversationID := a.startConversationTurn(conversation.ID, preferredConversationID, "api", "chat_completions", resolveRequestPromptForContinuation(normalized), request)
	setConversationIDHeader(w, conversationID)
	stream := typed.Stream
	if stream {
		includeUsage := false
		if typed.StreamIncludeUsage != nil {
			includeUsage = *typed.StreamIncludeUsage
		}
		a.writeChatCompletionLiveStream(w, r, request, entry.ID, includeUsage, conversationID)
		return
	}
	result, err := a.runPrompt(r, request)
	if err != nil {
		a.failConversation(conversationID, err)
		a.writeUpstreamError(w, err)
		return
	}
	result = applyInferenceResultOutputPolicy(result, request)
	responsePayload := buildChatCompletion(result, entry.ID, cfg.DebugUpstream)
	attachConversationResponseMetadata(responsePayload, conversationID, result.ThreadID)
	setThreadIDHeader(w, result.ThreadID)
	a.markConversationEnvelope(conversationID, "", stringValue(responsePayload["id"]))
	a.completeConversation(conversationID, result)
	a.persistConversationSession(conversationID, request, result)
	writeJSON(w, http.StatusOK, responsePayload)
}

func (a *App) handleSillyTavernChatCompletions(w http.ResponseWriter, r *http.Request) {
	payload, err := a.decodeBody(w, r)
	if err != nil {
		writeInvalidBodyError(w, err)
		return
	}
	a.handleSillyTavernChatCompletionsPayload(w, r, payload)
}

func (a *App) handleSillyTavernChatCompletionsPayload(w http.ResponseWriter, r *http.Request, payload map[string]any) {
	ctx, err := buildSillyTavernContext(payload)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", nilString())
		return
	}
	if ctx.Normalized.Prompt == "" {
		writeOpenAIError(w, http.StatusBadRequest, "messages must contain text or supported attachments", "invalid_request_error", nilString())
		return
	}

	cfg, _, registry := a.State.Snapshot()
	entry, err := registry.Resolve(requestedModel(payload, cfg.DefaultPublicModel()), cfg.DefaultPublicModel())
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "model_not_found")
		return
	}

	originalFingerprint := canonicalConversationFingerprint(ctx.StableHidden, ctx.RequestSegments)
	originalRawMessageCount := sessionRawMessageCount(ctx.RequestSegments)
	request := PromptRunRequest{
		Prompt:             ctx.Normalized.Prompt,
		LatestUserPrompt:   ctx.LatestPrompt,
		HiddenPrompt:       ctx.RequestHidden,
		PublicModel:        entry.ID,
		NotionModel:        entry.NotionModel,
		ClientProfile:      sillyTavernClientProfile,
		ClientMode:         ctx.Mode,
		ClientSessionKey:   ctx.ProfileKey,
		UseWebSearch:       requestedWebSearch(payload, cfg.Features.UseWebSearch),
		Attachments:        ctx.Normalized.Attachments,
		SessionFingerprint: originalFingerprint,
		RawMessageCount:    originalRawMessageCount,
	}
	request.SuppressReasoningOutput = !sillyTavernWantsReasoning(payload)
	if streamEnabled, _ := payload["stream"].(bool); streamEnabled && !request.SuppressReasoningOutput {
		request.StreamReasoningWarmup = true
	}
	a.markEphemeralConversationRequest(&request)
	freshThreadMode := forceFreshThreadPerRequest(cfg)

	preferredConversationID := requestedConversationID(r, payload)
	conversation := ConversationEntry{}
	if matched, ok := a.resolveSillyTavernContinuation(r, payload, ctx); ok {
		request.SuppressUpstreamThreadPersistence = matched.SuppressPersist
		conversation = matched.Target.Conversation
		request.PinnedAccountEmail = firstNonEmpty(strings.TrimSpace(conversation.AccountEmail), requestedAccountEmail(r, payload))
		if freshThreadMode {
			request.ForceLocalConversationContinue = strings.TrimSpace(conversation.ID) != ""
			request.Prompt = buildFreshThreadReplayPromptFromConversation(conversation, ctx.LatestPrompt, ctx.Normalized.Attachments, request.Prompt)
		} else {
			request.UpstreamThreadID = strings.TrimSpace(conversation.ThreadID)
			request.continuationDraft = buildContinuationDraft(matched.Target.Session)
			request.ForceSessionRepeatTurn = matched.ForceRepeatTurn
			if request.UpstreamThreadID != "" {
				if ctx.Mode == sillyTavernModeContinue {
					request.Prompt = sillyTavernContinuationPrompt(payload)
				} else {
					request.Prompt = ctx.LatestPrompt
				}
			}
		}
	} else {
		request.PinnedAccountEmail = requestedAccountEmail(r, payload)
		preferActiveAccountForRequest(cfg, &request)
		if ctx.Mode == sillyTavernModeQuiet || ctx.Mode == sillyTavernModeImpersona {
			request.SuppressUpstreamThreadPersistence = true
		}
	}

	if request.continuationDraft != nil && (request.RawMessageCount == request.continuationDraft.RawMessageCount || request.ForceSessionRepeatTurn) {
		request.SessionRepeatTurn = true
	}

	request.ConversationID = firstNonEmpty(strings.TrimSpace(conversation.ID), preferredConversationID)
	conversationID := a.startConversationTurn(conversation.ID, preferredConversationID, "sillytavern", "chat_completions", ctx.DisplayPrompt, request)
	setConversationIDHeader(w, conversationID)

	stream, _ := payload["stream"].(bool)
	if stream {
		a.writeChatCompletionLiveStream(w, r, request, entry.ID, includeUsageInStream(payload), conversationID)
		return
	}

	result, err := a.runPrompt(r, request)
	if err != nil {
		a.failConversation(conversationID, err)
		a.writeUpstreamError(w, err)
		return
	}
	result = applyInferenceResultOutputPolicy(result, request)
	responsePayload := buildChatCompletion(result, entry.ID, cfg.DebugUpstream)
	attachConversationResponseMetadata(responsePayload, conversationID, result.ThreadID)
	setThreadIDHeader(w, result.ThreadID)
	a.markConversationEnvelope(conversationID, "", stringValue(responsePayload["id"]))
	a.completeConversation(conversationID, result)
	a.persistConversationSession(conversationID, request, result)
	if !request.SuppressUpstreamThreadPersistence {
		a.persistSillyTavernBinding(conversationID, ctx.ProfileKey, ctx.Mode)
	}
	writeJSON(w, http.StatusOK, responsePayload)
}

func (a *App) handleResponses(w http.ResponseWriter, r *http.Request) {
	raw, err := a.decodeBodyRaw(w, r)
	if err != nil {
		writeInvalidBodyError(w, err)
		return
	}
	typed, _, err := decodeResponsesRequestBodyFromRaw(raw)
	if err != nil {
		writeInvalidBodyError(w, err)
		return
	}
	stream := typed.Stream
	var previousResponse map[string]any
	previousResponseID := strings.TrimSpace(typed.PreviousResponseID)
	if previousResponseID != "" {
		var ok bool
		previousResponse, ok = a.State.getResponse(previousResponseID)
		if !ok {
			writeOpenAIError(w, http.StatusNotFound, "response not found", "invalid_request_error", "response_not_found")
			return
		}
	}
	normalized, err := normalizeResponsesInputFromParts(typed.Input, typed.Attachments, previousResponse)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", nilString())
		return
	}
	if normalized.Prompt == "" {
		writeOpenAIError(w, http.StatusBadRequest, "input must contain text or supported attachments", "invalid_request_error", nilString())
		return
	}
	cfg, _, registry := a.State.Snapshot()
	requestedModelID := requestedModelFromTyped(typed.Model, cfg.DefaultPublicModel())
	useWebSearch := requestedWebSearchFromTyped(typed.UseWebSearch, typed.Metadata, typed.Tools, cfg.Features.UseWebSearch)
	preferredConversationID := requestedConversationIDFromTyped(r, typed.ConversationID, typed.Conversation, typed.Metadata)
	explicitThreadID := requestedThreadIDFromTyped(r, typed.ThreadID, typed.Thread, typed.NotionThreadID, typed.Metadata)
	requestedAccount := requestedAccountEmailFromTyped(r, typed.AccountEmail, typed.NotionAccountEmail, typed.Metadata)
	entry, err := registry.Resolve(requestedModelID, cfg.DefaultPublicModel())
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "model_not_found")
		return
	}
	hiddenPrompt := strings.TrimSpace(normalized.HiddenPrompt)
	promptText := normalized.Prompt
	latestPrompt := resolveRequestPromptForContinuation(normalized)
	originalFingerprint := canonicalConversationFingerprint(hiddenPrompt, normalized.Segments)
	originalRawMessageCount := sessionRawMessageCount(normalized.Segments)
	request := PromptRunRequest{
		Prompt:             promptText,
		LatestUserPrompt:   latestPrompt,
		HiddenPrompt:       hiddenPrompt,
		PublicModel:        entry.ID,
		NotionModel:        entry.NotionModel,
		UseWebSearch:       useWebSearch,
		Attachments:        normalized.Attachments,
		SessionFingerprint: originalFingerprint,
		RawMessageCount:    originalRawMessageCount,
	}
	freshThreadMode := forceFreshThreadPerRequest(cfg)
	conversation := ConversationEntry{}
	if matched, ok := a.resolveContinuationConversationWithExplicit(previousResponseID, hiddenPrompt, normalized.Segments, preferredConversationID, explicitThreadID); ok {
		conversation = matched.Conversation
		request.PinnedAccountEmail = firstNonEmpty(strings.TrimSpace(conversation.AccountEmail), requestedAccount)
		if freshThreadMode {
			request.ForceLocalConversationContinue = strings.TrimSpace(conversation.ID) != ""
			request.Prompt = buildFreshThreadReplayPromptFromConversation(conversation, latestPrompt, normalized.Attachments, promptText)
		} else {
			request.UpstreamThreadID = strings.TrimSpace(conversation.ThreadID)
			request.continuationDraft = buildContinuationDraft(matched.Session)
			if matched.Session != nil && (request.RawMessageCount == matched.Session.Session.RawMessageCount || request.ForceSessionRepeatTurn) {
				request.SessionRepeatTurn = true
			}
			request.Prompt = latestPrompt
		}
	} else {
		request.PinnedAccountEmail = requestedAccount
	}
	if freshThreadMode && strings.TrimSpace(conversation.ID) == "" {
		request.Prompt = buildFreshThreadReplayPromptFromStoredResponse(normalized.PreviousResponsePrompt, latestPrompt, normalized.Attachments, request.Prompt)
	}
	request.ConversationID = firstNonEmpty(strings.TrimSpace(conversation.ID), preferredConversationID)
	conversationID := a.startConversationTurn(conversation.ID, preferredConversationID, "api", "responses", resolveRequestPromptForContinuation(normalized), request)
	setConversationIDHeader(w, conversationID)
	if stream {
		a.writeResponsesLiveStream(w, r, request, entry.ID, cfg.DebugUpstream, conversationID)
		return
	}
	result, err := a.runPrompt(r, request)
	if err != nil {
		a.failConversation(conversationID, err)
		a.writeUpstreamError(w, err)
		return
	}
	result = applyInferenceResultOutputPolicy(result, request)
	responsePayload := buildResponsesOutputWithIDs(
		result,
		entry.ID,
		cfg.DebugUpstream,
		"resp_"+strings.ReplaceAll(randomUUID(), "-", ""),
		"msg_"+strings.ReplaceAll(randomUUID(), "-", ""),
		time.Now().Unix(),
	)
	attachConversationResponseMetadata(responsePayload, conversationID, result.ThreadID)
	setThreadIDHeader(w, result.ThreadID)
	responseID := stringValue(responsePayload["id"])
	if responseID != "" {
		a.State.saveResponseWithAccount(responseID, responsePayload, conversationID, result.ThreadID, result.AccountEmail)
	}
	a.markConversationEnvelope(conversationID, responseID, "")
	a.completeConversation(conversationID, result)
	a.persistConversationSession(conversationID, request, result)
	writeJSON(w, http.StatusOK, responsePayload)
}

func (a *App) writeUpstreamError(w http.ResponseWriter, err error) {
	message := err.Error()
	lower := strings.ToLower(message)
	if isDispatchCapacityExceededError(err) {
		writeOpenAIError(w, http.StatusTooManyRequests, message, "rate_limit_error", "dispatch_capacity_exceeded")
		return
	}
	if strings.Contains(lower, "context deadline exceeded") || strings.Contains(lower, "timeout") {
		writeOpenAIError(w, http.StatusGatewayTimeout, message, "api_timeout_error", "upstream_timeout")
		return
	}
	writeOpenAIError(w, http.StatusBadGateway, message, "api_error", "upstream_error")
}

func prepareOpenAISSEHeaders(w http.ResponseWriter) {
	applyCORSHeaders(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
}

var (
	chatCompletionInitialFlushDelay = 1500 * time.Millisecond
)

func writeSSEDone(w http.ResponseWriter, flusher http.Flusher) {
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func (a *App) writeChatCompletionStream(w http.ResponseWriter, r *http.Request, result InferenceResult, modelID string, includeUsage bool, conversationID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOpenAIError(w, http.StatusInternalServerError, "streaming is not supported by this response writer", "api_error", "stream_unsupported")
		return
	}
	prepareOpenAISSEHeaders(w)

	completionID := "chatcmpl-" + strings.ReplaceAll(randomUUID(), "-", "")
	created := time.Now().Unix()
	assistantText := sanitizeAssistantVisibleText(result.Text)
	reasoningText := sanitizeAssistantVisibleText(result.Reasoning)

	chunks := []map[string]any{
		buildChatStreamChunk(completionID, created, modelID, []map[string]any{
			buildChatStreamDeltaChoice(0, map[string]any{"role": "assistant"}),
		}, nil),
	}
	cfg, _, _ := a.State.Snapshot()
	for _, part := range splitTextChunks(reasoningText, cfg.StreamChunkRunes) {
		if part == "" {
			continue
		}
		chunks = append(chunks, buildChatStreamChunk(completionID, created, modelID, []map[string]any{
			buildChatStreamReasoningChoice(0, part),
		}, nil))
	}
	for _, part := range splitTextChunks(assistantText, cfg.StreamChunkRunes) {
		chunks = append(chunks, buildChatStreamChunk(completionID, created, modelID, []map[string]any{
			buildChatStreamDeltaChoice(0, map[string]any{"content": part}),
		}, nil))
	}
	finalUsage := map[string]any{}
	if includeUsage {
		finalUsage = buildUsage(result.Prompt, assistantText, reasoningText)
	}
	chunks = append(chunks, buildChatStreamChunk(completionID, created, modelID, []map[string]any{
		buildChatStreamFinishChoice(0, "stop"),
	}, finalUsage))

	for _, chunk := range chunks {
		if err := writeSSEData(w, flusher, chunk); err != nil {
			return
		}
		select {
		case <-r.Context().Done():
			return
		default:
		}
	}
	writeSSEDone(w, flusher)
}

func (a *App) writeChatCompletionLiveStream(w http.ResponseWriter, r *http.Request, request PromptRunRequest, modelID string, includeUsage bool, conversationID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOpenAIError(w, http.StatusInternalServerError, "streaming is not supported by this response writer", "api_error", "stream_unsupported")
		return
	}

	completionID := "chatcmpl-" + strings.ReplaceAll(randomUUID(), "-", "")
	created := time.Now().Unix()
	var emittedVisibleText strings.Builder
	var emittedReasoning strings.Builder
	warmupSent := false
	const reasoningHeartbeat = "\u200b"
	var writeMu sync.Mutex
	headersSent := false
	safeWriteData := func(payload any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return writeSSEData(w, flusher, payload)
	}
	safeWriteDone := func() {
		writeMu.Lock()
		defer writeMu.Unlock()
		writeSSEDone(w, flusher)
	}
	startStream := func() error {
		writeMu.Lock()
		defer writeMu.Unlock()
		if headersSent {
			return nil
		}
		headersSent = true
		prepareOpenAISSEHeaders(w)
		a.markConversationEnvelope(conversationID, "", completionID)
		return writeSSEData(w, flusher, buildChatStreamChunk(completionID, created, modelID, []map[string]any{
			buildChatStreamDeltaChoice(0, map[string]any{"role": "assistant"}),
		}, nil))
	}
	emitContent := func(part string) error {
		if part == "" {
			return nil
		}
		if err := startStream(); err != nil {
			return err
		}
		emittedVisibleText.WriteString(part)
		return safeWriteData(buildChatStreamChunk(completionID, created, modelID, []map[string]any{
			buildChatStreamDeltaChoice(0, map[string]any{"content": part}),
		}, nil))
	}
	emitReasoning := func(part string) error {
		if part == "" || request.SuppressReasoningOutput {
			return nil
		}
		if err := startStream(); err != nil {
			return err
		}
		emittedReasoning.WriteString(part)
		return safeWriteData(buildChatStreamChunk(completionID, created, modelID, []map[string]any{
			buildChatStreamReasoningChoice(0, part),
		}, nil))
	}
	emitReasoningWarmup := func() error {
		if !request.StreamReasoningWarmup || request.SuppressReasoningOutput {
			return nil
		}
		if warmupSent {
			return nil
		}
		if err := startStream(); err != nil {
			return err
		}
		warmupSent = true
		return safeWriteData(buildChatStreamChunk(completionID, created, modelID, []map[string]any{
			buildChatStreamReasoningChoice(0, reasoningHeartbeat),
		}, nil))
	}
	emitKeepAlive := func() error {
		if err := startStream(); err != nil {
			return err
		}
		return safeWriteData(buildChatStreamChunk(completionID, created, modelID, []map[string]any{
			buildChatStreamHeartbeatChoice(0),
		}, nil))
	}
	stopProactiveFlush := make(chan struct{})
	defer close(stopProactiveFlush)
	if chatCompletionInitialFlushDelayForRequest(request) <= 0 {
		_ = startStream()
		_ = emitReasoningWarmup()
	} else {
		go func() {
			timer := time.NewTimer(chatCompletionInitialFlushDelayForRequest(request))
			defer timer.Stop()
			for {
				select {
				case <-r.Context().Done():
					return
				case <-stopProactiveFlush:
					return
				case <-timer.C:
					if err := startStream(); err == nil {
						_ = emitKeepAlive()
					}
					return
				}
			}
		}()
	}
	result, err := a.runPromptStreamWithSink(r, request, InferenceStreamSink{
		Text: func(delta string) error {
			if delta == "" {
				return nil
			}
			a.pushConversationDelta(conversationID, delta)
			return emitContent(delta)
		},
		Reasoning:       emitReasoning,
		ReasoningWarmup: emitReasoningWarmup,
		KeepAlive:       emitKeepAlive,
	})
	if err != nil {
		partialText := sanitizeAssistantVisibleText(emittedVisibleText.String())
		if !headersSent {
			a.failConversation(conversationID, err)
			a.writeUpstreamError(w, err)
			return
		}
		if strings.TrimSpace(partialText) != "" {
			partialResult := InferenceResult{
				Prompt: request.Prompt,
				Text:   partialText,
			}
			partialResult = applyInferenceResultOutputPolicy(partialResult, request)
			a.completeConversation(conversationID, partialResult)
			a.persistConversationSession(conversationID, request, partialResult)
			if request.ClientProfile == sillyTavernClientProfile && !request.SuppressUpstreamThreadPersistence {
				a.persistSillyTavernBinding(conversationID, request.ClientSessionKey, request.ClientMode)
			}
			finalUsage := map[string]any{}
			if includeUsage {
				finalUsage = buildUsage(request.Prompt, partialText, emittedReasoning.String())
			}
			_ = safeWriteData(buildChatStreamChunk(completionID, created, modelID, []map[string]any{
				buildChatStreamFinishChoice(0, "stop"),
			}, finalUsage))
			safeWriteDone()
			return
		}
		a.failConversation(conversationID, err)
		_ = safeWriteData(map[string]any{
			"error": map[string]any{
				"message": err.Error(),
				"type":    "api_error",
				"param":   nil,
				"code":    "upstream_error",
			},
		})
		safeWriteDone()
		return
	}
	result = applyInferenceResultOutputPolicy(result, request)
	a.completeConversation(conversationID, result)
	a.persistConversationSession(conversationID, request, result)
	if request.ClientProfile == sillyTavernClientProfile && !request.SuppressUpstreamThreadPersistence {
		a.persistSillyTavernBinding(conversationID, request.ClientSessionKey, request.ClientMode)
	}

	assistantText := result.Text
	reasoningText := result.Reasoning
	finalUsage := map[string]any{}
	if includeUsage {
		finalUsage = buildUsage(result.Prompt, assistantText, reasoningText)
	}
	if remainingReasoning := textDeltaSuffix(emittedReasoning.String(), reasoningText); remainingReasoning != "" {
		if err := emitReasoning(remainingReasoning); err != nil {
			return
		}
	}
	if remainingText := textDeltaSuffix(emittedVisibleText.String(), assistantText); remainingText != "" {
		if err := emitContent(remainingText); err != nil {
			return
		}
	}
	if err := startStream(); err != nil {
		return
	}
	_ = safeWriteData(buildChatStreamChunk(completionID, created, modelID, []map[string]any{
		buildChatStreamFinishChoice(0, "stop"),
	}, finalUsage))
	safeWriteDone()
}

func (a *App) writeResponsesLiveStream(w http.ResponseWriter, r *http.Request, request PromptRunRequest, modelID string, includeTrace bool, conversationID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOpenAIError(w, http.StatusInternalServerError, "streaming is not supported by this response writer", "api_error", "stream_unsupported")
		return
	}

	responseID := "resp_" + strings.ReplaceAll(randomUUID(), "-", "")
	outputItemID := "msg_" + strings.ReplaceAll(randomUUID(), "-", "")
	createdAt := time.Now().Unix()
	inProgressResponse := buildResponsesInProgressObject(responseID, modelID, createdAt)
	attachConversationResponseMetadata(inProgressResponse, conversationID, "")
	inProgressItem := buildResponsesMessageItem(outputItemID, "", "in_progress")
	sequenceNumber := 0
	var writeMu sync.Mutex
	safeWriteEvent := func(eventType string, payload map[string]any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		if payload == nil {
			payload = map[string]any{}
		}
		payload["sequence_number"] = sequenceNumber
		sequenceNumber++
		return writeSSEEvent(w, flusher, eventType, payload)
	}
	safeWriteDone := func() {
		writeMu.Lock()
		defer writeMu.Unlock()
		writeSSEDone(w, flusher)
	}
	headersSent := false
	startStream := func() error {
		writeMu.Lock()
		defer writeMu.Unlock()
		if headersSent {
			return nil
		}
		headersSent = true
		prepareOpenAISSEHeaders(w)
		a.markConversationEnvelope(conversationID, responseID, "")
		initialEvents := []struct {
			name    string
			payload map[string]any
		}{
			{name: "response.created", payload: buildResponsesCreatedEvent(inProgressResponse)},
			{name: "response.in_progress", payload: buildResponsesInProgressEvent(inProgressResponse)},
			{name: "response.output_item.added", payload: buildResponsesOutputItemAddedEvent(responseID, inProgressItem)},
			{name: "response.content_part.added", payload: buildResponsesContentPartAddedEvent(responseID, outputItemID)},
		}
		for _, event := range initialEvents {
			payload := event.payload
			if payload == nil {
				payload = map[string]any{}
			}
			payload["sequence_number"] = sequenceNumber
			sequenceNumber++
			if err := writeSSEEvent(w, flusher, event.name, payload); err != nil {
				return err
			}
		}
		return nil
	}
	var emittedVisibleText strings.Builder
	var emittedReasoning strings.Builder
	warmupSent := false
	const reasoningHeartbeat = "\u200b"
	reasoningPhaseStarted := false
	reasoningPhaseDone := false
	emitTextDelta := func(part string) error {
		if part == "" {
			return nil
		}
		if err := startStream(); err != nil {
			return err
		}
		if reasoningPhaseStarted && !reasoningPhaseDone {
			reasoningPhaseDone = true
			if sanitizeAssistantVisibleText(emittedReasoning.String()) != "" {
				if err := safeWriteEvent("response.reasoning.done", buildResponsesReasoningDoneEvent(
					responseID,
					outputItemID,
					"",
				)); err != nil {
					return err
				}
			}
		}
		reasoningPhaseStarted = false
		emittedVisibleText.WriteString(part)
		return safeWriteEvent("response.output_text.delta", buildResponsesOutputTextDeltaEvent(responseID, outputItemID, part))
	}
	emitReasoningDelta := func(part string) error {
		if part == "" || request.SuppressReasoningOutput {
			return nil
		}
		if err := startStream(); err != nil {
			return err
		}
		reasoningPhaseStarted = true
		emittedReasoning.WriteString(part)
		return safeWriteEvent("response.reasoning.delta", buildResponsesReasoningDeltaEvent(responseID, outputItemID, part))
	}
	emitReasoningWarmup := func() error {
		if !request.StreamReasoningWarmup || request.SuppressReasoningOutput {
			return nil
		}
		if warmupSent {
			return nil
		}
		if err := startStream(); err != nil {
			return err
		}
		warmupSent = true
		reasoningPhaseStarted = true
		return safeWriteEvent("response.reasoning.delta", buildResponsesReasoningDeltaEvent(responseID, outputItemID, reasoningHeartbeat))
	}
	emitKeepAlive := func() error {
		if err := startStream(); err != nil {
			return err
		}
		return safeWriteEvent("response.in_progress", buildResponsesInProgressEvent(inProgressResponse))
	}

	if request.StreamReasoningWarmup {
		_ = emitReasoningWarmup()
	}

	result, err := a.runPromptStreamWithSink(r, request, InferenceStreamSink{
		Text: func(delta string) error {
			if delta == "" {
				return nil
			}
			a.pushConversationDelta(conversationID, delta)
			return emitTextDelta(delta)
		},
		Reasoning:       emitReasoningDelta,
		ReasoningWarmup: emitReasoningWarmup,
		KeepAlive:       emitKeepAlive,
	})
	if err != nil {
		partialText := sanitizeAssistantVisibleText(emittedVisibleText.String())
		partialReasoning := sanitizeAssistantVisibleText(emittedReasoning.String())
		if !headersSent {
			a.failConversation(conversationID, err)
			a.writeUpstreamError(w, err)
			return
		}
		if strings.TrimSpace(partialText) != "" {
			partialResult := InferenceResult{
				Prompt:    request.Prompt,
				Text:      partialText,
				Reasoning: partialReasoning,
			}
			partialResult = applyInferenceResultOutputPolicy(partialResult, request)
			completedResponse := buildResponsesOutputWithIDs(partialResult, modelID, includeTrace, responseID, outputItemID, createdAt)
			attachConversationResponseMetadata(completedResponse, conversationID, "")
			a.State.saveResponseWithAccount(responseID, completedResponse, conversationID, "", "")
			a.completeConversation(conversationID, partialResult)
			a.persistConversationSession(conversationID, request, partialResult)
			streamCompletedItem := buildResponsesStreamTerminalItem(outputItemID, "completed")
			streamCompletedResponse := buildResponsesStreamCompletedResponse(completedResponse, outputItemID)
			finalEvents := []struct {
				name    string
				payload map[string]any
			}{
				{name: "response.output_text.done", payload: buildResponsesOutputTextDoneEvent(responseID, outputItemID, "")},
				{name: "response.content_part.done", payload: buildResponsesContentPartDoneEvent(responseID, outputItemID, "")},
			}
			if partialReasoning != "" && !reasoningPhaseDone {
				reasoningPhaseDone = true
				finalEvents = append(finalEvents, struct {
					name    string
					payload map[string]any
				}{name: "response.reasoning.done", payload: buildResponsesReasoningDoneEvent(responseID, outputItemID, "")})
			}
			finalEvents = append(finalEvents,
				struct {
					name    string
					payload map[string]any
				}{name: "response.output_item.done", payload: buildResponsesOutputItemDoneEvent(responseID, streamCompletedItem)},
				struct {
					name    string
					payload map[string]any
				}{name: "response.completed", payload: buildResponsesCompletedEvent(streamCompletedResponse)},
			)
			for _, event := range finalEvents {
				if err := safeWriteEvent(event.name, event.payload); err != nil {
					return
				}
			}
			safeWriteDone()
			return
		}
		a.failConversation(conversationID, err)
		failedResponse := buildResponsesFailedObject(responseID, modelID, createdAt, err.Error())
		_ = safeWriteEvent("response.failed", buildResponsesFailedEvent(failedResponse))
		safeWriteDone()
		return
	}

	result = applyInferenceResultOutputPolicy(result, request)
	finalText := result.Text
	if strings.TrimSpace(result.Text) == "" && strings.TrimSpace(finalText) != "" {
		result.Text = finalText
	} else if strings.TrimSpace(result.Text) != finalText {
		result.Text = finalText
	}
	if remainingReasoning := textDeltaSuffix(emittedReasoning.String(), result.Reasoning); remainingReasoning != "" {
		if err := emitReasoningDelta(remainingReasoning); err != nil {
			return
		}
	}
	if remainingText := textDeltaSuffix(emittedVisibleText.String(), finalText); remainingText != "" {
		if err := emitTextDelta(remainingText); err != nil {
			return
		}
	}
	completedResponse := buildResponsesOutputWithIDs(result, modelID, includeTrace, responseID, outputItemID, createdAt)
	attachConversationResponseMetadata(completedResponse, conversationID, result.ThreadID)
	a.State.saveResponseWithAccount(responseID, completedResponse, conversationID, result.ThreadID, result.AccountEmail)
	a.completeConversation(conversationID, result)
	a.persistConversationSession(conversationID, request, result)
	streamCompletedItem := buildResponsesStreamTerminalItem(outputItemID, "completed")
	streamCompletedResponse := buildResponsesStreamCompletedResponse(completedResponse, outputItemID)
	if err := startStream(); err != nil {
		return
	}
	finalEvents := []struct {
		name    string
		payload map[string]any
	}{
		{name: "response.output_text.done", payload: buildResponsesOutputTextDoneEvent(responseID, outputItemID, "")},
		{name: "response.content_part.done", payload: buildResponsesContentPartDoneEvent(responseID, outputItemID, "")},
	}
	if result.Reasoning != "" && !reasoningPhaseDone {
		reasoningPhaseDone = true
		finalEvents = append(finalEvents, struct {
			name    string
			payload map[string]any
		}{name: "response.reasoning.done", payload: buildResponsesReasoningDoneEvent(responseID, outputItemID, "")})
	}
	finalEvents = append(finalEvents, struct {
		name    string
		payload map[string]any
	}{name: "response.output_item.done", payload: buildResponsesOutputItemDoneEvent(responseID, streamCompletedItem)})
	for _, event := range finalEvents {
		if err := safeWriteEvent(event.name, event.payload); err != nil {
			return
		}
	}
	if err := safeWriteEvent("response.completed", buildResponsesCompletedEvent(streamCompletedResponse)); err != nil {
		return
	}
	safeWriteDone()
}

func writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, eventType string, payload any) error {
	if _, err := fmt.Fprintf(w, "event: %s\n", eventType); err != nil {
		return err
	}
	return writeSSEData(w, flusher, payload)
}

func writeSSEData(w http.ResponseWriter, flusher http.Flusher, payload any) error {
	if _, err := fmt.Fprintf(w, "data: %s\n\n", marshalJSON(payload)); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func writeSSEComment(w http.ResponseWriter, flusher http.Flusher, comment string) error {
	if strings.TrimSpace(comment) == "" {
		comment = "keepalive"
	}
	if _, err := fmt.Fprintf(w, ": %s\n\n", comment); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func (a *App) writeResponsesStream(w http.ResponseWriter, r *http.Request, result InferenceResult, modelID string, includeTrace bool, conversationID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOpenAIError(w, http.StatusInternalServerError, "streaming is not supported by this response writer", "api_error", "stream_unsupported")
		return
	}
	prepareOpenAISSEHeaders(w)

	responseID := "resp_" + strings.ReplaceAll(randomUUID(), "-", "")
	outputItemID := "msg_" + strings.ReplaceAll(randomUUID(), "-", "")
	createdAt := time.Now().Unix()
	inProgressResponse := buildResponsesInProgressObject(responseID, modelID, createdAt)
	assistantText := sanitizeAssistantVisibleText(result.Text)
	completedResponse := buildResponsesOutputWithIDs(result, modelID, includeTrace, responseID, outputItemID, createdAt)
	attachConversationResponseMetadata(inProgressResponse, conversationID, "")
	attachConversationResponseMetadata(completedResponse, conversationID, result.ThreadID)
	a.State.saveResponseWithAccount(responseID, completedResponse, conversationID, result.ThreadID, result.AccountEmail)
	streamCompletedItem := buildResponsesStreamTerminalItem(outputItemID, "completed")
	streamCompletedResponse := buildResponsesStreamCompletedResponse(completedResponse, outputItemID)
	inProgressItem := buildResponsesMessageItem(outputItemID, "", "in_progress")
	cfg, _, _ := a.State.Snapshot()
	sequenceNumber := 0
	writeEvent := func(eventType string, payload map[string]any) error {
		if payload == nil {
			payload = map[string]any{}
		}
		payload["sequence_number"] = sequenceNumber
		sequenceNumber++
		return writeSSEEvent(w, flusher, eventType, payload)
	}

	events := []struct {
		name    string
		payload map[string]any
	}{
		{name: "response.created", payload: buildResponsesCreatedEvent(inProgressResponse)},
		{name: "response.in_progress", payload: buildResponsesInProgressEvent(inProgressResponse)},
		{name: "response.output_item.added", payload: buildResponsesOutputItemAddedEvent(responseID, inProgressItem)},
		{name: "response.content_part.added", payload: buildResponsesContentPartAddedEvent(responseID, outputItemID)},
	}

	for _, event := range events {
		if err := writeEvent(event.name, event.payload); err != nil {
			return
		}
		select {
		case <-r.Context().Done():
			return
		default:
		}
	}

	for _, part := range splitTextChunks(assistantText, cfg.StreamChunkRunes) {
		if err := writeEvent("response.output_text.delta", buildResponsesOutputTextDeltaEvent(responseID, outputItemID, part)); err != nil {
			return
		}
		select {
		case <-r.Context().Done():
			return
		default:
		}
	}

	finalEvents := []struct {
		name    string
		payload map[string]any
	}{
		{name: "response.output_text.done", payload: buildResponsesOutputTextDoneEvent(responseID, outputItemID, "")},
		{name: "response.content_part.done", payload: buildResponsesContentPartDoneEvent(responseID, outputItemID, "")},
		{name: "response.output_item.done", payload: buildResponsesOutputItemDoneEvent(responseID, streamCompletedItem)},
	}
	for _, event := range finalEvents {
		if err := writeEvent(event.name, event.payload); err != nil {
			return
		}
		select {
		case <-r.Context().Done():
			return
		default:
		}
	}
	if err := writeEvent("response.completed", buildResponsesCompletedEvent(streamCompletedResponse)); err != nil {
		return
	}
	select {
	case <-r.Context().Done():
		return
	default:
	}
	writeSSEDone(w, flusher)
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	startedAt := time.Now()
	statusCode := http.StatusOK
	defer func() {
		observeRequestDuration(r.URL.Path, r.Method, statusCode, time.Since(startedAt))
	}()
	safeWriter := &panicSafeResponseWriter{ResponseWriter: w}
	applyCORSHeaders(safeWriter)
	defer func() {
		if recovered := recover(); recovered != nil {
			stack := strings.TrimSpace(string(debug.Stack()))
			log.Printf("[panic] %s %s remote=%s panic=%v\n%s", r.Method, r.URL.Path, r.RemoteAddr, recovered, stack)
			cfg, _, _ := a.State.Snapshot()
			message := "internal server panic"
			if cfg.DebugUpstream {
				message = fmt.Sprintf("internal server panic: %v", recovered)
			}
			contentType := strings.ToLower(strings.TrimSpace(safeWriter.Header().Get("Content-Type")))
			if !safeWriter.wroteHeader {
				writeOpenAIError(safeWriter, http.StatusInternalServerError, message, "api_error", "internal_panic")
				return
			}
			if strings.Contains(contentType, "text/event-stream") {
				payload := map[string]any{
					"error": map[string]any{
						"message": message,
						"type":    "api_error",
						"code":    "internal_panic",
					},
				}
				if encoded, err := json.Marshal(payload); err == nil {
					_, _ = fmt.Fprintf(safeWriter, "event: error\ndata: %s\n\n", encoded)
				}
				_, _ = fmt.Fprint(safeWriter, "data: [DONE]\n\n")
				safeWriter.Flush()
			}
		}
	}()

	if r.Method == http.MethodOptions {
		safeWriter.WriteHeader(http.StatusNoContent)
		statusCode = safeWriter.status
		return
	}

	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && path == "/":
		a.serveIndex(safeWriter)
		statusCode = safeWriter.status
		return
	case strings.HasPrefix(path, "/admin"):
		a.handleAdmin(safeWriter, r)
		statusCode = safeWriter.status
		return
	case r.Method == http.MethodGet && path == "/healthz":
		a.serveHealthz(safeWriter)
		statusCode = safeWriter.status
		return
	}

	if !a.authOK(safeWriter, r) {
		statusCode = safeWriter.status
		return
	}

	switch {
	case r.Method == http.MethodGet && path == "/v1/models":
		a.serveModels(safeWriter)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/v1/models/"):
		a.serveModelByID(safeWriter, path)
	case r.Method == http.MethodGet && path == "/debug/vars":
		expvar.Handler().ServeHTTP(safeWriter, r)
	case r.Method == http.MethodGet && path == "/metrics":
		writePrometheusMetrics(safeWriter)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/v1/responses/"):
		a.serveResponseByID(safeWriter, path)
	case r.Method == http.MethodPost && path == "/v1/st/chat/completions":
		a.handleSillyTavernChatCompletions(safeWriter, r)
	case r.Method == http.MethodPost && path == "/v1/chat/completions":
		a.handleChatCompletions(safeWriter, r)
	case r.Method == http.MethodPost && path == "/v1/responses":
		a.handleResponses(safeWriter, r)
	default:
		writeOpenAIError(safeWriter, http.StatusNotFound, "route not found", "invalid_request_error", "not_found")
	}
	statusCode = safeWriter.status
}

func Main() {
	cfg := parseCLI()
	state, err := newServerState(cfg)
	if err != nil {
		log.Fatalf("init state failed: %v", err)
	}
	app := &App{State: state}
	state.StartSessionRefreshLoop(context.Background())
	app.StartEphemeralConversationCleanupLoop(context.Background())
	if cfg.Debug.PprofEnabled {
		go func(addr string) {
			log.Printf("[pprof] listening on http://%s/debug/pprof/ (local debug endpoint; avoid public exposure)", addr)
			if err := http.ListenAndServe(addr, nil); err != nil {
				log.Printf("[pprof] server stopped: %v", err)
			}
		}(cfg.Debug.PprofAddr)
	}
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	server := &http.Server{
		Addr:              addr,
		Handler:           app,
		ReadHeaderTimeout: 15 * time.Second,
	}
	log.Printf("[notion2api-go] listening on http://%s default_model=%s", addr, cfg.DefaultPublicModel())
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
