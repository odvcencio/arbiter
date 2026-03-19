package grpcserver

import (
	"fmt"
	"sync"
	"time"

	"github.com/odvcencio/arbiter/expert"
)

// SessionStore keeps expert sessions in memory.
type SessionStore struct {
	mu       sync.RWMutex
	nextID   uint64
	sessions map[string]*ExpertSession
	ttl      time.Duration
	maxCount int
}

// ExpertSession is one live expert session.
type ExpertSession struct {
	mu         sync.Mutex
	ID         string
	BundleID   string
	Envelope   map[string]any
	Session    *expert.Session
	CreatedAt  time.Time
	LastAccess time.Time
}

// NewSessionStore creates an empty expert-session store.
func NewSessionStore() *SessionStore {
	return &SessionStore{
		sessions: make(map[string]*ExpertSession),
		ttl:      30 * time.Minute,
		maxCount: 10_000,
	}
}

// Create registers a new session and returns it.
func (ss *SessionStore) Create(bundleID string, envelope map[string]any, session *expert.Session) *ExpertSession {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	now := time.Now().UTC()
	ss.pruneExpiredLocked(now)
	ss.evictIfNeededLocked()

	ss.nextID++
	id := fmt.Sprintf("sess_%d", ss.nextID)
	handle := &ExpertSession{
		ID:         id,
		BundleID:   bundleID,
		Envelope:   cloneMap(envelope),
		Session:    session,
		CreatedAt:  now,
		LastAccess: now,
	}
	ss.sessions[id] = handle
	return handle
}

// Get returns a session by ID.
func (ss *SessionStore) Get(id string) (*ExpertSession, bool) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.pruneExpiredLocked(time.Now().UTC())
	handle, ok := ss.sessions[id]
	if ok {
		handle.LastAccess = time.Now().UTC()
	}
	return handle, ok
}

// Delete removes a session by ID.
func (ss *SessionStore) Delete(id string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	delete(ss.sessions, id)
}

func (ss *SessionStore) pruneExpiredLocked(now time.Time) {
	if ss.ttl <= 0 {
		return
	}
	for id, handle := range ss.sessions {
		if now.Sub(handle.LastAccess) > ss.ttl {
			delete(ss.sessions, id)
		}
	}
}

func (ss *SessionStore) evictIfNeededLocked() {
	if ss.maxCount <= 0 || len(ss.sessions) < ss.maxCount {
		return
	}
	var oldestID string
	var oldest time.Time
	for id, handle := range ss.sessions {
		if oldestID == "" || handle.LastAccess.Before(oldest) {
			oldestID = id
			oldest = handle.LastAccess
		}
	}
	if oldestID != "" {
		delete(ss.sessions, oldestID)
	}
}

func cloneMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}
