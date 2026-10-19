package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/stretchr/testify/require"
)

type alertWebhookOutboxStub struct {
	deliveries []repository.MonitoringAlertWebhookDelivery
	delivered  []int64
	failed     []webhookFailure
}

type webhookFailure struct {
	id          int64
	nextAttempt time.Time
	description string
}

type alertWebhookTargetStub struct {
	target  string
	enabled bool
	err     error
}

func (s alertWebhookTargetStub) WebhookTarget(context.Context) (string, bool, error) {
	return s.target, s.enabled, s.err
}

func (s *alertWebhookOutboxStub) ListDue(context.Context, int) ([]repository.MonitoringAlertWebhookDelivery, error) {
	return s.deliveries, nil
}
func (s *alertWebhookOutboxStub) MarkDelivered(_ context.Context, id int64) error {
	s.delivered = append(s.delivered, id)
	return nil
}
func (s *alertWebhookOutboxStub) MarkFailed(_ context.Context, id int64, next time.Time, description string) error {
	s.failed = append(s.failed, webhookFailure{id: id, nextAttempt: next, description: description})
	return nil
}
func (s *alertWebhookOutboxStub) PruneDelivered(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func TestAlertWebhookDispatcherDeliversPersistentTransitionPayload(t *testing.T) {
	var got map[string]any
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		require.Equal(t, "Qubes-Air-Alert-Webhook/1", r.Header.Get("User-Agent"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	stub := &alertWebhookOutboxStub{deliveries: []repository.MonitoringAlertWebhookDelivery{{
		ID: 11, Event: repository.MonitoringAlertEvent{
			ID: 7, AlertID: "zone-disconnected:z1", ResourceID: "z1", EventType: "raised",
			Severity: "warning", Message: "Zone is disconnected", Source: "zone-status",
			OccurredAt: time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC),
		},
	}}}
	dispatcher := NewAlertWebhookDispatcher(stub, alertWebhookTargetStub{target: server.URL + "/alerts", enabled: true}, []string{server.Listener.Addr().String()})
	dispatcher.client = server.Client() // Trust only this ephemeral test server's certificate.
	dispatcher.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	dispatcher.dispatchDue(context.Background())
	require.Equal(t, []int64{11}, stub.delivered)
	require.Empty(t, stub.failed)
	require.Equal(t, float64(7), got["event_id"])
	require.Equal(t, "raised", got["event_type"])
}

func TestAlertWebhookDispatcherRetriesFailuresWithoutLeakingURL(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	stub := &alertWebhookOutboxStub{deliveries: []repository.MonitoringAlertWebhookDelivery{{
		ID: 5, Attempts: 2, Event: repository.MonitoringAlertEvent{ID: 1},
	}}}
	dispatcher := NewAlertWebhookDispatcher(stub, alertWebhookTargetStub{target: server.URL + "/secret-token", enabled: true}, []string{server.Listener.Addr().String()})
	dispatcher.client = server.Client()
	dispatcher.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	dispatcher.now = func() time.Time { return now }
	dispatcher.dispatchDue(context.Background())
	require.Empty(t, stub.delivered)
	require.Len(t, stub.failed, 1)
	require.Equal(t, now.Add(20*time.Second), stub.failed[0].nextAttempt)
	require.EqualError(t, errors.New(stub.failed[0].description), "endpoint returned HTTP 503")
	require.NotContains(t, stub.failed[0].description, "secret-token")
}

func TestAlertWebhookDispatcherNeverFollowsRedirect(t *testing.T) {
	secondRequests := 0
	second := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { secondRequests++ }))
	defer second.Close()
	first := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second.URL, http.StatusFound)
	}))
	defer first.Close()
	stub := &alertWebhookOutboxStub{deliveries: []repository.MonitoringAlertWebhookDelivery{{
		ID: 8, Event: repository.MonitoringAlertEvent{ID: 2},
	}}}
	dispatcher := NewAlertWebhookDispatcher(stub,
		alertWebhookTargetStub{target: first.URL, enabled: true}, []string{first.Listener.Addr().String()})
	dispatcher.client = first.Client()
	dispatcher.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	dispatcher.dispatchDue(context.Background())
	require.Equal(t, 0, secondRequests)
	require.Empty(t, stub.delivered)
	require.Len(t, stub.failed, 1)
	require.Equal(t, "endpoint returned HTTP 302", stub.failed[0].description)
}

func TestAlertWebhookDispatcherRejectsUnlistedHostWithoutNetworkRequest(t *testing.T) {
	stub := &alertWebhookOutboxStub{deliveries: []repository.MonitoringAlertWebhookDelivery{{
		ID: 2, Event: repository.MonitoringAlertEvent{ID: 1},
	}}}
	dispatcher := NewAlertWebhookDispatcher(stub,
		alertWebhookTargetStub{target: "https://internal.example.test/private", enabled: true}, []string{"hooks.example.test"})
	now := time.Now()
	dispatcher.dispatchDue(context.Background())
	require.Empty(t, stub.delivered)
	require.Len(t, stub.failed, 1)
	require.Equal(t, "webhook host is not allowlisted", stub.failed[0].description)
	require.GreaterOrEqual(t, stub.failed[0].nextAttempt.Sub(now), 4*time.Second)
}

func TestAlertWebhookDispatcherBacksOffWhenEncryptedTargetCannotLoad(t *testing.T) {
	stub := &alertWebhookOutboxStub{deliveries: []repository.MonitoringAlertWebhookDelivery{{
		ID: 3, Event: repository.MonitoringAlertEvent{ID: 2},
	}}}
	dispatcher := NewAlertWebhookDispatcher(stub,
		alertWebhookTargetStub{err: errors.New("ciphertext failed to decrypt")}, []string{"hooks.example.test"})
	dispatcher.now = func() time.Time { return time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC) }
	dispatcher.dispatchDue(context.Background())
	require.Empty(t, stub.delivered)
	require.Len(t, stub.failed, 1)
	require.Equal(t, "configured endpoint unavailable", stub.failed[0].description)
	require.Equal(t, time.Date(2026, 9, 24, 12, 0, 5, 0, time.UTC), stub.failed[0].nextAttempt)
}

func TestAlertWebhookBackoffIsExponentialAndBounded(t *testing.T) {
	require.Equal(t, 5*time.Second, alertWebhookBackoff(1))
	require.Equal(t, 10*time.Second, alertWebhookBackoff(2))
	require.Equal(t, alertWebhookMaxBackoff, alertWebhookBackoff(20))
}
