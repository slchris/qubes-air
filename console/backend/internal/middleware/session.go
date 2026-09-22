package middleware

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

// SessionCookieName is the cookie a browser holds after exchanging its API token
// for a session. HttpOnly + SameSite=Strict means page scripts (and cross-site
// requests) cannot read or replay it, which is the point of not putting the
// token in localStorage.
const SessionCookieName = "qubesair_session"

// DefaultSessionTTL bounds how long a browser session lasts before the operator
// must re-exchange the token.
const DefaultSessionTTL = 12 * time.Hour

// Session is one issued browser session.
type Session struct {
	ID string
	// Subject labels who the session belongs to in audit logs — the configured
	// token's name, never the token itself.
	Subject string
	Scope   Scope
	// Zones is the object-level restriction inherited from the token that
	// created the session. Empty means fleet-wide.
	Zones   []string
	Created time.Time
	Expires time.Time
}

// SessionStore holds sessions in memory.
//
// In-memory is deliberate: a session is a convenience for a browser, not an
// authority. A console restart drops them and the operator exchanges the token
// again; the token itself is still the root credential.
type SessionStore struct {
	mu       sync.Mutex
	sessions map[string]Session
	ttl      time.Duration
	now      func() time.Time
}

// NewSessionStore builds a store with the given TTL (DefaultSessionTTL when
// non-positive).
func NewSessionStore(ttl time.Duration) *SessionStore {
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}
	return &SessionStore{sessions: map[string]Session{}, ttl: ttl, now: time.Now}
}

// Create mints a session for subject/scope/zones and returns it. The zones are
// copied: the session must not share the caller's slice.
func (s *SessionStore) Create(subject string, scope Scope, zones []string) (Session, error) {
	id, err := newSessionID()
	if err != nil {
		return Session{}, err
	}
	now := s.now().UTC()
	sess := Session{
		ID:      id,
		Subject: subject,
		Scope:   scope,
		Zones:   append([]string(nil), zones...),
		Created: now,
		Expires: now.Add(s.ttl),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked(now)
	s.sessions[id] = sess
	return sess, nil
}

// Get returns a live session, or ok=false when it is unknown or expired.
func (s *SessionStore) Get(id string) (Session, bool) {
	if id == "" {
		return Session{}, false
	}
	now := s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return Session{}, false
	}
	if !now.Before(sess.Expires) {
		delete(s.sessions, id)
		return Session{}, false
	}
	return sess, true
}

// Delete removes a session (logout). Idempotent.
func (s *SessionStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

// gcLocked drops expired sessions. Called on Create so the map cannot grow
// without bound on a long-lived process. The caller holds s.mu.
func (s *SessionStore) gcLocked(now time.Time) {
	for id, sess := range s.sessions {
		if !now.Before(sess.Expires) {
			delete(s.sessions, id)
		}
	}
}

// newSessionID returns a 256-bit URL-safe random session id.
func newSessionID() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
