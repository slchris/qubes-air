// Package desktopaccess is the consent state machine an MCP client must pass
// before it may read a qube's desktop: a request waits for a person at the
// Console to allow or deny it, and an approval yields one short-lived,
// single-use grant bound to the requester, the qube and the operation.
//
// The store is process-local on purpose. Restarting the Console drops every
// pending request and grant, so no desktop permission outlives the process
// that a person approved it in.
package desktopaccess

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"
)

// Lifetimes and the capacity bound.
const (
	// ApprovalTTL is how long a request waits for a decision. The requester's
	// HTTP call is held open for this long, so it also bounds that call.
	ApprovalTTL = 30 * time.Second
	// FrameGrantTTL is how long an approved frame grant stays usable, and so
	// the longest an active capture may run before its lease ends.
	FrameGrantTTL = 30 * time.Second
	// MaxPendingRequests caps the live requests (pending, approved or active)
	// the store holds, so a caller cannot grow the queue an operator reviews,
	// or the goroutines waiting on it, without bound.
	MaxPendingRequests = 128
)

// Operation is what a grant allows. Frame, one desktop screenshot, is the only
// one: input has no consumer, so the store refuses to issue a grant for it.
type Operation string

// OperationFrame allows reading one desktop frame.
const OperationFrame Operation = "frame"

// Errors the store returns. None of them carries grant material.
var (
	ErrInvalidRequest    = errors.New("invalid desktop access request")
	ErrAtCapacity        = errors.New("too many pending desktop access requests")
	ErrNotFound          = errors.New("desktop access request not found")
	ErrExpired           = errors.New("desktop access request expired without a decision")
	ErrInvalidTransition = errors.New("desktop access request is not in a state that allows this")
	ErrInvalidGrant      = errors.New("desktop access grant is not valid")
	ErrDenied            = errors.New("desktop access request was denied")
	ErrRevoked           = errors.New("desktop access was stopped")
)

// State is where a request is in its life.
type State string

// Request states. Pending, approved and active are live; the rest are
// terminal and the record is dropped at the next sweep.
const (
	StatePending   State = "pending"
	StateApproved  State = "approved"
	StateActive    State = "active"
	StateDenied    State = "denied"
	StateExpired   State = "expired"
	StateRevoked   State = "revoked"
	StateCompleted State = "completed"
)

// Request is the public view of a consent request: approval metadata only.
// The grant secret is handed out once, by WaitDecision, and appears in no
// Request value.
type Request struct {
	ID        string    `json:"id"`
	Subject   string    `json:"subject"`
	QubeID    string    `json:"qube_id"`
	Operation Operation `json:"operation"`
	State     State     `json:"state"`
	ExpiresAt time.Time `json:"expires_at"`
}

type record struct {
	Request
	grantHash  [sha256.Size]byte
	grantUntil time.Time
	activeDone chan struct{}
	timer      *time.Timer
	decision   chan decision
	waiter     bool
	resolved   bool
}

// decision is what the one waiting requester receives: the secret on
// approval, or the reason there is none.
type decision struct {
	secret  string
	request Request
	err     error
}

// Store holds the live consent requests.
type Store struct {
	mu          sync.Mutex
	records     map[string]*record
	now         func() time.Time
	approvalTTL time.Duration
	grantTTL    time.Duration
}

// NewStore returns an empty store with the production lifetimes.
func NewStore() *Store {
	return &Store{
		records:     make(map[string]*record),
		now:         time.Now,
		approvalTTL: ApprovalTTL,
		grantTTL:    FrameGrantTTL,
	}
}

// Request records a pending request by subject for operation on qubeID. The
// caller must have authenticated subject and checked it may address the qube.
func (s *Store) Request(subject, qubeID string, operation Operation) (Request, error) {
	subject = strings.TrimSpace(subject)
	if !safeSubject(subject) || !safeID(qubeID) || operation != OperationFrame {
		return Request{}, ErrInvalidRequest
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.pruneLocked(now)
	if s.liveCountLocked() >= MaxPendingRequests {
		return Request{}, ErrAtCapacity
	}
	r := &record{Request: Request{
		ID: uuid.NewString(), Subject: subject, QubeID: qubeID,
		Operation: operation, State: StatePending, ExpiresAt: now.Add(s.approvalTTL).UTC(),
	}, decision: make(chan decision, 1)}
	s.records[r.ID] = r
	return r.Request, nil
}

// PendingRequest returns the request id while it still awaits a decision.
func (s *Store) PendingRequest(requestID string) (Request, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(s.now())
	r, ok := s.records[requestID]
	if !ok || r.State != StatePending {
		return Request{}, false
	}
	return r.Request, true
}

// OperatorQueue lists pending requests and live grants, soonest expiry first,
// so the Console can show what is waiting and what may be stopped.
func (s *Store) OperatorQueue() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(s.now())
	out := make([]Request, 0, len(s.records))
	for _, r := range s.records {
		if live(r.State) {
			out = append(out, r.Request)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ExpiresAt.Before(out[j].ExpiresAt) })
	return out
}

// Approve turns a pending request into a grant and delivers the secret to the
// requester waiting in WaitDecision. The operator never sees the secret: this
// returns only the metadata.
func (s *Store) Approve(requestID string) (Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[requestID]
	if !ok {
		return Request{}, ErrNotFound
	}
	now := s.now()
	if err := s.checkPendingLocked(r, now); err != nil {
		return r.Request, err
	}
	secret, hash, err := newGrantSecret()
	if err != nil {
		return Request{}, err
	}
	r.grantHash = hash
	r.grantUntil = now.Add(s.grantTTL)
	r.State = StateApproved
	r.ExpiresAt = r.grantUntil.UTC()
	s.resolveLocked(r, secret, nil)
	return r.Request, nil
}

// Deny refuses a pending request; its waiting requester gets ErrDenied.
func (s *Store) Deny(requestID string) (Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[requestID]
	if !ok {
		return Request{}, ErrNotFound
	}
	if err := s.checkPendingLocked(r, s.now()); err != nil {
		return r.Request, err
	}
	s.finishLocked(r, StateDenied, ErrDenied)
	return r.Request, nil
}

// Stop revokes a pending request or a live grant. An active capture sees its
// lease end at once.
func (s *Store) Stop(requestID string) (Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[requestID]
	if !ok {
		return Request{}, ErrNotFound
	}
	if !live(r.State) {
		return r.Request, ErrInvalidTransition
	}
	s.finishLocked(r, StateRevoked, ErrRevoked)
	return r.Request, nil
}

// WaitDecision blocks the requester that created requestID until an operator
// decides, the approval window closes, or ctx ends. The secret travels only
// through this call's return value, exactly once; a second wait is refused.
// When ctx ends first the request is revoked, so an approval that lands after
// the requester has gone yields nothing usable.
func (s *Store) WaitDecision(ctx context.Context, requestID, subject string) (string, Request, error) {
	result, wait, err := s.claimWaiter(requestID, subject)
	if err != nil {
		return "", Request{}, err
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case d := <-result:
		if ctx.Err() != nil {
			return "", d.request, errors.Join(ctx.Err(), s.revoke(requestID))
		}
		return d.secret, d.request, d.err
	case <-timer.C:
		// The window is over by the timer, whatever the wall clock says: a
		// clock stepped backwards must not leave the requester blocked.
		s.expireIfPending(requestID)
		d := <-result
		return d.secret, d.request, d.err
	case <-ctx.Done():
		return "", Request{}, errors.Join(ctx.Err(), s.revoke(requestID))
	}
}

// claimWaiter registers the one waiter a request may have and returns its
// result channel and how long the approval window has left. The wait never
// exceeds a whole window: the request was created at most that long ago, so
// a wall clock stepped backwards cannot stretch it.
func (s *Store) claimWaiter(requestID, subject string) (chan decision, time.Duration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[requestID]
	if !ok || r.Subject != subject {
		return nil, 0, ErrNotFound
	}
	if r.waiter {
		return nil, 0, ErrInvalidTransition
	}
	r.waiter = true
	return r.decision, min(r.ExpiresAt.Sub(s.now()), s.approvalTTL), nil
}

// Acquire consumes a grant exactly once and returns the lease an active
// capture runs under. Every binding is rechecked: the subject presenting the
// grant, the qube and the operation must all be the ones approved. A grant
// presented with the wrong binding is burnt, because whoever presented it
// holds a secret that was issued to someone else.
func (s *Store) Acquire(secret, subject, qubeID string, operation Operation) (*Lease, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil || len(decoded) != grantBytes {
		return nil, ErrInvalidGrant
	}
	want := sha256.Sum256(decoded)
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.grantLocked(want)
	if r == nil || r.State != StateApproved {
		return nil, ErrInvalidGrant
	}
	now := s.now()
	if !now.Before(r.grantUntil) {
		s.finishLocked(r, StateExpired, ErrExpired)
		return nil, ErrInvalidGrant
	}
	if r.Subject != subject || r.QubeID != qubeID || r.Operation != operation {
		s.finishLocked(r, StateRevoked, ErrRevoked)
		return nil, ErrInvalidGrant
	}
	r.State = StateActive
	r.activeDone = make(chan struct{})
	id := r.ID
	r.timer = time.AfterFunc(r.grantUntil.Sub(now), func() { s.expireActive(id) })
	return &Lease{store: s, requestID: id}, nil
}

// grantLocked finds the record whose grant hashes to want, comparing every
// candidate in constant time.
func (s *Store) grantLocked(want [sha256.Size]byte) *record {
	var found *record
	for _, r := range s.records {
		if subtle.ConstantTimeCompare(want[:], r.grantHash[:]) == 1 {
			found = r
		}
	}
	return found
}

// revoke ends a live request on behalf of its requester, whose wait ended.
func (s *Store) revoke(requestID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[requestID]
	if !ok {
		return ErrNotFound
	}
	if !live(r.State) {
		return ErrInvalidTransition
	}
	s.finishLocked(r, StateRevoked, ErrRevoked)
	return nil
}

// Lease is an acquired grant. It ends when the capture completes, when an
// operator stops it, or when the grant's lifetime runs out.
type Lease struct {
	store     *Store
	requestID string
}

// Done is closed once the lease has ended: stopped, expired or completed.
func (l *Lease) Done() <-chan struct{} {
	l.store.mu.Lock()
	defer l.store.mu.Unlock()
	r := l.activeLocked()
	if r == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return r.activeDone
}

// Valid reports whether the lease is still active. Callers check it after the
// capture, before returning its result.
func (l *Lease) Valid() bool {
	l.store.mu.Lock()
	defer l.store.mu.Unlock()
	return l.activeLocked() != nil
}

// Complete ends an active lease normally.
func (l *Lease) Complete() {
	l.store.mu.Lock()
	defer l.store.mu.Unlock()
	if r := l.activeLocked(); r != nil {
		l.store.finishLocked(r, StateCompleted, nil)
	}
}

// activeLocked returns the lease's record while it is active and inside its
// lifetime, expiring it when the lifetime has run out.
func (l *Lease) activeLocked() *record {
	r := l.store.records[l.requestID]
	if r == nil || r.State != StateActive {
		return nil
	}
	if !l.store.now().Before(r.grantUntil) {
		l.store.finishLocked(r, StateExpired, ErrExpired)
		return nil
	}
	return r
}

func (s *Store) expireActive(requestID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.records[requestID]; r != nil && r.State == StateActive {
		s.finishLocked(r, StateExpired, ErrExpired)
	}
}

// expireIfPending ends a request whose approval window the waiter's timer
// has closed.
func (s *Store) expireIfPending(requestID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.records[requestID]; r != nil && r.State == StatePending {
		s.finishLocked(r, StateExpired, ErrExpired)
	}
}

func (s *Store) checkPendingLocked(r *record, now time.Time) error {
	if r.State != StatePending {
		return ErrInvalidTransition
	}
	if !now.Before(r.ExpiresAt) {
		s.finishLocked(r, StateExpired, ErrExpired)
		return ErrExpired
	}
	return nil
}

// finishLocked moves r to a terminal state: it stops the expiry timer, ends
// an active lease and, when nobody has been answered yet, tells the waiting
// requester why there is no grant. Every way out of a live state goes through
// here, so a waiter can never be left without an answer.
func (s *Store) finishLocked(r *record, state State, reason error) {
	r.State = state
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	if r.activeDone != nil {
		close(r.activeDone)
		r.activeDone = nil
	}
	s.resolveLocked(r, "", reason)
}

func (s *Store) resolveLocked(r *record, secret string, err error) {
	if r.resolved {
		return
	}
	r.resolved = true
	r.decision <- decision{secret: secret, request: r.Request, err: err}
}

// pruneLocked expires what has run out and drops terminal records.
func (s *Store) pruneLocked(now time.Time) {
	for id, r := range s.records {
		switch {
		case r.State == StatePending && !now.Before(r.ExpiresAt):
			s.finishLocked(r, StateExpired, ErrExpired)
		case (r.State == StateApproved || r.State == StateActive) && !now.Before(r.grantUntil):
			s.finishLocked(r, StateExpired, ErrExpired)
		}
		if !live(r.State) {
			delete(s.records, id)
		}
	}
}

func (s *Store) liveCountLocked() int {
	count := 0
	for _, r := range s.records {
		if live(r.State) {
			count++
		}
	}
	return count
}

func live(state State) bool {
	return state == StatePending || state == StateApproved || state == StateActive
}

// grantBytes is the grant secret's entropy: 256 bits, sent base64url-encoded.
const grantBytes = 32

func newGrantSecret() (string, [sha256.Size]byte, error) {
	var raw [grantBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", [sha256.Size]byte{}, err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), sha256.Sum256(raw[:]), nil
}

// safeSubject accepts a credential name fit for the queue and the audit
// line: present, bounded, and free of control characters that could forge a
// second line.
func safeSubject(subject string) bool {
	if subject == "" || len(subject) > 128 {
		return false
	}
	for _, r := range subject {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// safeID accepts a qube id: [A-Za-z0-9] then [A-Za-z0-9._-], at most 128 bytes.
func safeID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for i, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || (i > 0 && (r == '-' || r == '_' || r == '.')) {
			continue
		}
		return false
	}
	return true
}
