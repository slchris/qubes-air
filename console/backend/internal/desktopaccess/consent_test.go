package desktopaccess

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// waitResult is one WaitDecision outcome, collected from a goroutine.
type waitResult struct {
	secret  string
	request Request
	err     error
}

// startWait runs the requester's WaitDecision in the background.
func startWait(ctx context.Context, s *Store, requestID, subject string) <-chan waitResult {
	out := make(chan waitResult, 1)
	go func() {
		secret, request, err := s.WaitDecision(ctx, requestID, subject)
		out <- waitResult{secret: secret, request: request, err: err}
	}()
	return out
}

func receive(t *testing.T, ch <-chan waitResult) waitResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(3 * time.Second):
		t.Fatal("WaitDecision did not return")
		return waitResult{}
	}
}

// grant takes a frame request from subject for qube through approval and
// returns the secret the requester received.
func grant(t *testing.T, s *Store, subject, qube string) (string, Request) {
	t.Helper()
	request, err := s.Request(subject, qube, OperationFrame)
	if err != nil {
		t.Fatal(err)
	}
	waiting := startWait(context.Background(), s, request.ID, subject)
	waitForWaiter(t, s, request.ID)
	if _, err := s.Approve(request.ID); err != nil {
		t.Fatal(err)
	}
	got := receive(t, waiting)
	if got.err != nil || got.secret == "" {
		t.Fatalf("approval delivery = %+v", got)
	}
	return got.secret, request
}

// waitForWaiter returns once the requester has registered, so an approval
// in the test cannot race ahead of the wait it is meant to wake.
func waitForWaiter(t *testing.T, s *Store, requestID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		r := s.records[requestID]
		registered := r != nil && r.waiter
		s.mu.Unlock()
		if registered {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("requester never started waiting")
}

func TestApprovalDeliversGrantOnlyToTheWaitingRequester(t *testing.T) {
	s := NewStore()
	request, err := s.Request("mcp-client", "qube-1", OperationFrame)
	if err != nil {
		t.Fatal(err)
	}
	if request.State != StatePending || !request.ExpiresAt.After(time.Now()) {
		t.Fatalf("request = %+v", request)
	}
	if queue := s.OperatorQueue(); len(queue) != 1 || queue[0].ID != request.ID {
		t.Fatalf("operator queue = %+v", queue)
	}
	waiting := startWait(context.Background(), s, request.ID, "mcp-client")
	waitForWaiter(t, s, request.ID)

	approved, err := s.Approve(request.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := receive(t, waiting)
	if got.err != nil || len(got.secret) != 43 || got.request.State != StateApproved {
		t.Fatalf("delivered = %+v", got)
	}
	if approved.State != StateApproved || time.Until(approved.ExpiresAt) > FrameGrantTTL {
		t.Fatalf("approved = %+v", approved)
	}
	for _, view := range []any{approved, got.request, s.OperatorQueue()} {
		encoded, err := json.Marshal(view)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), got.secret) {
			t.Fatalf("public view carries grant material: %s", encoded)
		}
	}
	if _, _, err := s.WaitDecision(context.Background(), request.ID, "mcp-client"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("second wait = %v, want the grant delivered only once", err)
	}
}

func TestWaitDecisionIsBoundToTheRequester(t *testing.T) {
	s := NewStore()
	request, err := s.Request("mcp-client", "qube-1", OperationFrame)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.WaitDecision(context.Background(), request.ID, "someone-else"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other subject's wait = %v", err)
	}
	if _, _, err := s.WaitDecision(context.Background(), "no-such-request", "mcp-client"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown request wait = %v", err)
	}
}

func TestGrantIsSingleUseAndBoundToSubjectQubeAndOperation(t *testing.T) {
	s := NewStore()
	secret, _ := grant(t, s, "mcp-client", "qube-1")
	lease, err := s.Acquire(secret, "mcp-client", "qube-1", OperationFrame)
	if err != nil {
		t.Fatal(err)
	}
	if !lease.Valid() {
		t.Fatal("fresh lease is not valid")
	}
	if _, err := s.Acquire(secret, "mcp-client", "qube-1", OperationFrame); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("replayed grant = %v", err)
	}
	lease.Complete()
	if lease.Valid() {
		t.Fatal("completed lease is still valid")
	}
	select {
	case <-lease.Done():
	default:
		t.Fatal("completed lease's Done is open")
	}
}

// A grant presented by the wrong subject, for the wrong qube or for another
// operation is refused and burnt: the holder is not the requester it was
// issued to, so the rightful requester cannot use it afterwards either.
func TestMisboundGrantIsRefusedAndBurnt(t *testing.T) {
	for _, tc := range []struct {
		name, subject, qube string
		operation           Operation
	}{
		{"other subject", "other-client", "qube-1", OperationFrame},
		{"other qube", "mcp-client", "qube-2", OperationFrame},
		{"other operation", "mcp-client", "qube-1", "input"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewStore()
			secret, request := grant(t, s, "mcp-client", "qube-1")
			if _, err := s.Acquire(secret, tc.subject, tc.qube, tc.operation); !errors.Is(err, ErrInvalidGrant) {
				t.Fatalf("misbound acquire = %v", err)
			}
			if _, err := s.Acquire(secret, "mcp-client", "qube-1", OperationFrame); !errors.Is(err, ErrInvalidGrant) {
				t.Fatalf("burnt grant still usable: %v", err)
			}
			if _, err := s.Stop(request.ID); !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("burnt grant still live: %v", err)
			}
		})
	}
}

func TestAcquireRejectsMalformedSecrets(t *testing.T) {
	s := NewStore()
	grant(t, s, "mcp-client", "qube-1")
	for _, secret := range []string{"", "not base64!", "c2hvcnQ", strings.Repeat("A", 44)} {
		if _, err := s.Acquire(secret, "mcp-client", "qube-1", OperationFrame); !errors.Is(err, ErrInvalidGrant) {
			t.Errorf("Acquire(%q) = %v", secret, err)
		}
	}
}

func TestInputOperationIsRefused(t *testing.T) {
	s := NewStore()
	if _, err := s.Request("mcp-client", "qube-1", "input"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("input request = %v, want refused until an input consumer exists", err)
	}
	if len(s.OperatorQueue()) != 0 {
		t.Fatal("a refused request reached the operator queue")
	}
}

func TestDenyAnswersTheRequesterAndIsTerminal(t *testing.T) {
	s := NewStore()
	request, err := s.Request("mcp-client", "qube-1", OperationFrame)
	if err != nil {
		t.Fatal(err)
	}
	waiting := startWait(context.Background(), s, request.ID, "mcp-client")
	waitForWaiter(t, s, request.ID)
	if _, err := s.Deny(request.ID); err != nil {
		t.Fatal(err)
	}
	if got := receive(t, waiting); !errors.Is(got.err, ErrDenied) || got.secret != "" {
		t.Fatalf("denied delivery = %+v", got)
	}
	if _, err := s.Approve(request.ID); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("approve after deny = %v", err)
	}
	if _, err := s.Deny("no-such-request"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deny unknown = %v", err)
	}
}

func TestPendingRequestExpiresWithoutDecision(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.now = func() time.Time { return now }
	request, err := s.Request("mcp-client", "qube-1", OperationFrame)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.PendingRequest(request.ID); !ok {
		t.Fatal("fresh request is not pending")
	}
	now = now.Add(ApprovalTTL)
	if _, err := s.Approve(request.ID); !errors.Is(err, ErrExpired) {
		t.Fatalf("approve after the window = %v", err)
	}
	if _, ok := s.PendingRequest(request.ID); ok {
		t.Fatal("expired request is still pending")
	}
	if queue := s.OperatorQueue(); len(queue) != 0 {
		t.Fatalf("expired request still queued: %+v", queue)
	}
}

// The waiter's timer closes the window even when the wall clock says it is
// still open (a clock stepped backwards): the requester must not block
// forever.
func TestWaitDecisionTimesOutByTimerWhateverTheClockSays(t *testing.T) {
	s := NewStore()
	s.approvalTTL = 20 * time.Millisecond
	frozen := time.Now()
	s.now = func() time.Time { return frozen }
	request, err := s.Request("mcp-client", "qube-1", OperationFrame)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.now = func() time.Time { return frozen.Add(-time.Hour) }
	s.mu.Unlock()
	got := receive(t, startWait(context.Background(), s, request.ID, "mcp-client"))
	if !errors.Is(got.err, ErrExpired) {
		t.Fatalf("wait = %+v, want expiry", got)
	}
	if _, err := s.Approve(request.ID); err == nil {
		t.Fatal("request expired by the timer could still be approved")
	}
}

// A sweep that expires a pending request answers its waiter too.
func TestSweepExpiryAnswersTheWaiter(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.mu.Lock()
	s.now = func() time.Time { return now }
	s.mu.Unlock()
	request, err := s.Request("mcp-client", "qube-1", OperationFrame)
	if err != nil {
		t.Fatal(err)
	}
	waiting := startWait(context.Background(), s, request.ID, "mcp-client")
	waitForWaiter(t, s, request.ID)
	s.mu.Lock()
	now = now.Add(ApprovalTTL)
	s.mu.Unlock()
	s.OperatorQueue()
	if got := receive(t, waiting); !errors.Is(got.err, ErrExpired) {
		t.Fatalf("waiter after sweep = %+v", got)
	}
}

func TestWaitDecisionCancellationRevokesTheRequest(t *testing.T) {
	s := NewStore()
	request, err := s.Request("mcp-client", "qube-1", OperationFrame)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	waiting := startWait(ctx, s, request.ID, "mcp-client")
	waitForWaiter(t, s, request.ID)
	cancel()
	if got := receive(t, waiting); !errors.Is(got.err, context.Canceled) || got.secret != "" {
		t.Fatalf("canceled wait = %+v", got)
	}
	if _, err := s.Approve(request.ID); !errors.Is(err, ErrInvalidTransition) && !errors.Is(err, ErrNotFound) {
		t.Fatalf("approve after the requester left = %v", err)
	}
}

func TestStopEndsPendingApprovedAndActiveRequests(t *testing.T) {
	s := NewStore()

	pending, err := s.Request("mcp-client", "qube-1", OperationFrame)
	if err != nil {
		t.Fatal(err)
	}
	waiting := startWait(context.Background(), s, pending.ID, "mcp-client")
	waitForWaiter(t, s, pending.ID)
	if _, err := s.Stop(pending.ID); err != nil {
		t.Fatal(err)
	}
	if got := receive(t, waiting); !errors.Is(got.err, ErrRevoked) {
		t.Fatalf("stopped pending wait = %+v", got)
	}

	secret, approved := grant(t, s, "mcp-client", "qube-1")
	if _, err := s.Stop(approved.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Acquire(secret, "mcp-client", "qube-1", OperationFrame); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("stopped grant acquired: %v", err)
	}

	secret, active := grant(t, s, "mcp-client", "qube-1")
	lease, err := s.Acquire(secret, "mcp-client", "qube-1", OperationFrame)
	if err != nil {
		t.Fatal(err)
	}
	done := lease.Done()
	if _, err := s.Stop(active.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop did not end the active lease")
	}
	if lease.Valid() {
		t.Fatal("stopped lease is still valid")
	}
	if _, err := s.Stop(active.ID); !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("second stop = %v", err)
	}
	if _, err := s.Stop("no-such-request"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stop unknown = %v", err)
	}
}

func TestApprovedGrantExpiresBeforeUse(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.now = func() time.Time { return now }
	secret, _ := grant(t, s, "mcp-client", "qube-1")
	now = now.Add(FrameGrantTTL)
	if _, err := s.Acquire(secret, "mcp-client", "qube-1", OperationFrame); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("expired grant acquired: %v", err)
	}
}

// The lease timer ends an active capture when the grant's lifetime runs out,
// with no call into the store needed to notice.
func TestActiveLeaseEndsOnGrantExpiry(t *testing.T) {
	s := NewStore()
	s.grantTTL = 30 * time.Millisecond
	secret, _ := grant(t, s, "mcp-client", "qube-1")
	lease, err := s.Acquire(secret, "mcp-client", "qube-1", OperationFrame)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-lease.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("grant expiry did not end the active lease")
	}
	if lease.Valid() {
		t.Fatal("expired lease is still valid")
	}
}

// Checking the lease notices expiry by the store clock too, before the timer.
func TestLeaseChecksNoticeExpiry(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.mu.Lock()
	s.now = func() time.Time { return now }
	s.mu.Unlock()
	secret, _ := grant(t, s, "mcp-client", "qube-1")
	lease, err := s.Acquire(secret, "mcp-client", "qube-1", OperationFrame)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	now = now.Add(FrameGrantTTL)
	s.mu.Unlock()
	select {
	case <-lease.Done():
	default:
		t.Fatal("lease past its lifetime is not done")
	}
	if lease.Valid() {
		t.Fatal("lease past its lifetime is valid")
	}
}

func TestGrantCanOnlyBeAcquiredOnceConcurrently(t *testing.T) {
	s := NewStore()
	secret, _ := grant(t, s, "mcp-client", "qube-1")
	var wg sync.WaitGroup
	var mu sync.Mutex
	acquired := 0
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Acquire(secret, "mcp-client", "qube-1", OperationFrame); err == nil {
				mu.Lock()
				acquired++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if acquired != 1 {
		t.Fatalf("successful acquisitions = %d, want exactly one", acquired)
	}
}

// Approve, Deny, Stop and a canceled wait racing on one request leave exactly
// one outcome, and the waiter always gets an answer.
func TestConcurrentDecisionsResolveTheWaiterOnce(t *testing.T) {
	for range 50 {
		s := NewStore()
		request, err := s.Request("mcp-client", "qube-1", OperationFrame)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		waiting := startWait(ctx, s, request.ID, "mcp-client")
		var wg sync.WaitGroup
		for _, act := range []func(){
			func() { _, _ = s.Approve(request.ID) },
			func() { _, _ = s.Deny(request.ID) },
			func() { _, _ = s.Stop(request.ID) },
			cancel,
		} {
			wg.Add(1)
			go func() { defer wg.Done(); act() }()
		}
		wg.Wait()
		got := receive(t, waiting)
		if got.err == nil && got.secret == "" {
			t.Fatalf("waiter got neither a grant nor a reason: %+v", got)
		}
		cancel()
	}
}

func TestRequestRejectsUnsafeInputsAndCapsLiveRequests(t *testing.T) {
	s := NewStore()
	for _, tc := range []struct {
		subject, qube string
		operation     Operation
	}{
		{"", "q1", OperationFrame},
		{"   ", "q1", OperationFrame},
		{"operator\nforged", "q1", OperationFrame},
		{strings.Repeat("s", 129), "q1", OperationFrame},
		{"operator", "../q1", OperationFrame},
		{"operator", "-q1", OperationFrame},
		{"operator", strings.Repeat("q", 129), OperationFrame},
		{"operator", "q1", "exec"},
	} {
		if _, err := s.Request(tc.subject, tc.qube, tc.operation); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("Request(%q, %q, %q) = %v", tc.subject, tc.qube, tc.operation, err)
		}
	}
	for range MaxPendingRequests {
		if _, err := s.Request("operator", "q1", OperationFrame); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Request("operator", "q1", OperationFrame); !errors.Is(err, ErrAtCapacity) {
		t.Fatalf("request over the cap = %v", err)
	}
}

// Terminal requests stop counting against the cap once swept.
func TestCapFreesAsRequestsEnd(t *testing.T) {
	s := NewStore()
	var last Request
	for range MaxPendingRequests {
		r, err := s.Request("operator", "q1", OperationFrame)
		if err != nil {
			t.Fatal(err)
		}
		last = r
	}
	if _, err := s.Deny(last.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Request("operator", "q1", OperationFrame); err != nil {
		t.Fatalf("request after one ended = %v", err)
	}
}
