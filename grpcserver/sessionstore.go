package grpcserver

import (
	"fmt"
	"sync"

	"github.com/odvcencio/arbiter/expert"
)

// SessionStore keeps expert sessions in memory.
type SessionStore struct {
	mu       sync.RWMutex
	nextID   uint64
	sessions map[string]*ExpertSession
}

// ExpertSession is one live expert session.
type ExpertSession struct {
	ID       string
	BundleID string
	Envelope map[string]any
	Session  *expert.Session
}

// NewSessionStore creates an empty expert-session store.
func NewSessionStore() *SessionStore {
	return &SessionStore{sessions: make(map[string]*ExpertSession)}
}

// Create registers a new session and returns it.
func (ss *SessionStore) Create(bundleID string, envelope map[string]any, session *expert.Session) *ExpertSession {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	ss.nextID++
	id := fmt.Sprintf("sess_%d", ss.nextID)
	handle := &ExpertSession{
		ID:       id,
		BundleID: bundleID,
		Envelope: cloneMap(envelope),
		Session:  session,
	}
	ss.sessions[id] = handle
	return handle
}

// Get returns a session by ID.
func (ss *SessionStore) Get(id string) (*ExpertSession, bool) {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	handle, ok := ss.sessions[id]
	return handle, ok
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
