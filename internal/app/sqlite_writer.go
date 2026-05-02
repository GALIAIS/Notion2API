package app

import (
	"expvar"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const defaultSQLiteWriterQueueSize = 1024

var sqliteWriterFallbackTotalMetric = expvar.NewMap("notion2api_sqlite_writer_fallback_total")

type sqlitePersistOpKind uint8

const (
	sqlitePersistOpSaveResponse sqlitePersistOpKind = iota + 1
	sqlitePersistOpDeleteResponsesByConversationOrThread
)

type sqlitePersistOp struct {
	kind           sqlitePersistOpKind
	responseID     string
	payload        map[string]any
	createdAt      time.Time
	conversationID string
	threadID       string
	accountEmail   string
}

type SQLiteWriter struct {
	store    *SQLiteStore
	queue    chan sqlitePersistOp
	done     chan struct{}
	ttlNanos atomic.Int64

	mu     sync.RWMutex
	closed bool
}

func newSQLiteWriter(store *SQLiteStore, ttl time.Duration) *SQLiteWriter {
	if store == nil {
		return nil
	}
	writer := &SQLiteWriter{
		store: store,
		queue: make(chan sqlitePersistOp, defaultSQLiteWriterQueueSize),
		done:  make(chan struct{}),
	}
	writer.SetTTL(ttl)
	go writer.run()
	return writer
}

func (w *SQLiteWriter) SetTTL(ttl time.Duration) {
	if w == nil {
		return
	}
	if ttl <= 0 {
		ttl = time.Second
	}
	w.ttlNanos.Store(int64(ttl))
}

func (w *SQLiteWriter) EnqueueSaveResponse(responseID string, payload map[string]any, createdAt time.Time, conversationID string, threadID string, accountEmail string) {
	if w == nil || w.store == nil {
		return
	}
	responseID = strings.TrimSpace(responseID)
	if responseID == "" {
		return
	}
	op := sqlitePersistOp{
		kind:           sqlitePersistOpSaveResponse,
		responseID:     responseID,
		payload:        clonePersistPayload(payload),
		createdAt:      createdAt,
		conversationID: strings.TrimSpace(conversationID),
		threadID:       strings.TrimSpace(threadID),
		accountEmail:   strings.TrimSpace(accountEmail),
	}
	if w.tryEnqueue(op) {
		return
	}
	sqliteWriterFallbackTotalMetric.Add("channel_full", 1)
	w.apply(op)
}

func (w *SQLiteWriter) EnqueueDeleteResponsesByConversationOrThread(conversationID string, threadID string) {
	if w == nil || w.store == nil {
		return
	}
	conversationID = strings.TrimSpace(conversationID)
	threadID = strings.TrimSpace(threadID)
	if conversationID == "" && threadID == "" {
		return
	}
	op := sqlitePersistOp{
		kind:           sqlitePersistOpDeleteResponsesByConversationOrThread,
		conversationID: conversationID,
		threadID:       threadID,
	}
	if w.enqueueBlocking(op) {
		return
	}
	sqliteWriterFallbackTotalMetric.Add("writer_unavailable", 1)
	w.apply(op)
}

func (w *SQLiteWriter) Close() {
	if w == nil {
		return
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	close(w.queue)
	w.mu.Unlock()
	<-w.done
}

func (w *SQLiteWriter) tryEnqueue(op sqlitePersistOp) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.closed {
		return false
	}
	select {
	case w.queue <- op:
		return true
	default:
		return false
	}
}

func (w *SQLiteWriter) enqueueBlocking(op sqlitePersistOp) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.closed {
		return false
	}
	select {
	case w.queue <- op:
		return true
	default:
		w.queue <- op
		return true
	}
}

func (w *SQLiteWriter) run() {
	defer close(w.done)
	for op := range w.queue {
		w.apply(op)
	}
}

func (w *SQLiteWriter) apply(op sqlitePersistOp) {
	if w == nil || w.store == nil {
		return
	}
	switch op.kind {
	case sqlitePersistOpSaveResponse:
		if err := w.store.SaveResponse(op.responseID, op.payload, op.createdAt, op.conversationID, op.threadID, op.accountEmail); err != nil {
			log.Printf("[sqlite-writer] save response %s failed: %v", op.responseID, err)
			return
		}
		if err := w.store.DeleteExpiredResponses(w.ttl()); err != nil {
			log.Printf("[sqlite-writer] cleanup responses failed: %v", err)
		}
	case sqlitePersistOpDeleteResponsesByConversationOrThread:
		if err := w.store.DeleteResponsesByConversationOrThread(op.conversationID, op.threadID); err != nil {
			log.Printf("[sqlite-writer] delete responses conversation=%s thread=%s failed: %v", op.conversationID, op.threadID, err)
		}
	}
}

func (w *SQLiteWriter) ttl() time.Duration {
	if w == nil {
		return time.Second
	}
	ttlNanos := w.ttlNanos.Load()
	if ttlNanos <= 0 {
		return time.Second
	}
	return time.Duration(ttlNanos)
}

func clonePersistPayload(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}
