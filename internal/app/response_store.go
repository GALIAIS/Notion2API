package app

import (
	"container/heap"
	"strings"
	"time"
)

const responseStoreCleanupInterval = 30 * time.Second

type responseExpiryEntry struct {
	responseID string
	createdAt  time.Time
}

type responseExpiryHeap []responseExpiryEntry

func (h responseExpiryHeap) Len() int {
	return len(h)
}

func (h responseExpiryHeap) Less(i, j int) bool {
	return h[i].createdAt.Before(h[j].createdAt)
}

func (h responseExpiryHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}

func (h *responseExpiryHeap) Push(x any) {
	entry, _ := x.(responseExpiryEntry)
	*h = append(*h, entry)
}

func (h *responseExpiryHeap) Pop() any {
	if h == nil || len(*h) == 0 {
		return responseExpiryEntry{}
	}
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

type responseStore struct {
	ttl         time.Duration
	items       map[string]StoredResponse
	expirations responseExpiryHeap
}

var testHookResponseStorePrunePop func()

func normalizeResponseStoreTTL(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return time.Second
	}
	return ttl
}

func newResponseStore(ttl time.Duration) *responseStore {
	store := &responseStore{
		ttl:         normalizeResponseStoreTTL(ttl),
		items:       map[string]StoredResponse{},
		expirations: responseExpiryHeap{},
	}
	heap.Init(&store.expirations)
	return store
}

func (s *responseStore) setTTL(ttl time.Duration) {
	if s == nil {
		return
	}
	s.ttl = normalizeResponseStoreTTL(ttl)
}

func (s *responseStore) ensureInitialized() {
	if s == nil {
		return
	}
	if s.items == nil {
		s.items = map[string]StoredResponse{}
	}
	if s.expirations == nil {
		s.expirations = responseExpiryHeap{}
		heap.Init(&s.expirations)
	}
}

func (s *responseStore) save(responseID string, record StoredResponse, now time.Time) {
	if s == nil {
		return
	}
	responseID = strings.TrimSpace(responseID)
	if responseID == "" {
		return
	}
	s.ensureInitialized()
	now = now.UTC()
	s.pruneExpired(now)

	createdAt := record.CreatedAt.UTC()
	if createdAt.IsZero() {
		createdAt = now
	}
	record.CreatedAt = createdAt
	record.ConversationID = strings.TrimSpace(record.ConversationID)
	record.ThreadID = strings.TrimSpace(record.ThreadID)
	record.AccountEmail = strings.TrimSpace(record.AccountEmail)

	s.items[responseID] = record
	heap.Push(&s.expirations, responseExpiryEntry{
		responseID: responseID,
		createdAt:  createdAt,
	})
}

func (s *responseStore) get(responseID string, now time.Time) (StoredResponse, bool) {
	if s == nil {
		return StoredResponse{}, false
	}
	responseID = strings.TrimSpace(responseID)
	if responseID == "" {
		return StoredResponse{}, false
	}
	s.ensureInitialized()
	now = now.UTC()
	s.pruneExpired(now)
	record, ok := s.items[responseID]
	if !ok {
		return StoredResponse{}, false
	}
	if now.Sub(record.CreatedAt) > s.ttl {
		delete(s.items, responseID)
		return StoredResponse{}, false
	}
	return record, true
}

func (s *responseStore) replaceAll(records map[string]StoredResponse) {
	if s == nil {
		return
	}
	s.ensureInitialized()
	s.items = map[string]StoredResponse{}
	s.expirations = responseExpiryHeap{}
	heap.Init(&s.expirations)
	for responseID, record := range records {
		cleanID := strings.TrimSpace(responseID)
		if cleanID == "" {
			continue
		}
		createdAt := record.CreatedAt.UTC()
		record.CreatedAt = createdAt
		record.ConversationID = strings.TrimSpace(record.ConversationID)
		record.ThreadID = strings.TrimSpace(record.ThreadID)
		record.AccountEmail = strings.TrimSpace(record.AccountEmail)
		s.items[cleanID] = record
		heap.Push(&s.expirations, responseExpiryEntry{
			responseID: cleanID,
			createdAt:  createdAt,
		})
	}
}

func (s *responseStore) pruneExpired(now time.Time) int {
	if s == nil {
		return 0
	}
	s.ensureInitialized()
	if len(s.items) == 0 || len(s.expirations) == 0 {
		return 0
	}
	now = now.UTC()
	removed := 0
	for len(s.expirations) > 0 {
		top := s.expirations[0]
		if now.Sub(top.createdAt) <= s.ttl {
			break
		}
		entry, _ := heap.Pop(&s.expirations).(responseExpiryEntry)
		if testHookResponseStorePrunePop != nil {
			testHookResponseStorePrunePop()
		}
		current, ok := s.items[entry.responseID]
		if !ok {
			continue
		}
		if !current.CreatedAt.UTC().Equal(entry.createdAt) {
			continue
		}
		delete(s.items, entry.responseID)
		removed++
	}
	return removed
}

func (s *responseStore) deleteByConversationOrThread(conversationID string, threadID string) int {
	if s == nil {
		return 0
	}
	conversationID = strings.TrimSpace(conversationID)
	threadID = strings.TrimSpace(threadID)
	if conversationID == "" && threadID == "" {
		return 0
	}
	s.ensureInitialized()
	removed := 0
	for responseID, record := range s.items {
		if (conversationID != "" && strings.TrimSpace(record.ConversationID) == conversationID) ||
			(threadID != "" && strings.TrimSpace(record.ThreadID) == threadID) {
			delete(s.items, responseID)
			removed++
		}
	}
	return removed
}
