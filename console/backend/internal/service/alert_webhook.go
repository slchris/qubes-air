package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"time"

	"github.com/slchris/qubes-air/console/internal/netguard"
	"github.com/slchris/qubes-air/console/internal/repository"
)

const (
	alertWebhookPollInterval = 5 * time.Second
	alertWebhookRequestLimit = 25
	alertWebhookTimeout      = 10 * time.Second
	alertWebhookMaxBackoff   = time.Hour
	alertWebhookRetention    = 90 * 24 * time.Hour
)

// errNonPublicWebhookAddress is safe to store in the outbox: it names no URL
// and no internal address.
var errNonPublicWebhookAddress = errors.New("webhook host resolves to a non-public address")

type alertWebhookOutbox interface {
	ListDue(context.Context, int) ([]repository.MonitoringAlertWebhookDelivery, error)
	MarkDelivered(context.Context, int64) error
	MarkFailed(context.Context, int64, time.Time, string) error
	PruneDelivered(context.Context, time.Time) (int64, error)
}

// AlertWebhookTargetProvider supplies the decrypted endpoint to the background dispatcher.
type AlertWebhookTargetProvider interface {
	WebhookTarget(context.Context) (string, bool, error)
}

// AlertWebhookDispatcher sends the durable alert-event outbox to configured
// HTTPS destinations. The host allowlist is operator-managed application config.
type AlertWebhookDispatcher struct {
	outbox       alertWebhookOutbox
	target       AlertWebhookTargetProvider
	allowedHosts []string
	client       *http.Client
	now          func() time.Time
}

// NewAlertWebhookDispatcher builds the retrying webhook sender.
func NewAlertWebhookDispatcher(outbox alertWebhookOutbox, target AlertWebhookTargetProvider, allowedHosts []string) *AlertWebhookDispatcher {
	transport := &http.Transport{Proxy: nil, DialContext: safeWebhookDialContext}
	return &AlertWebhookDispatcher{
		outbox:       outbox,
		target:       target,
		allowedHosts: append([]string(nil), allowedHosts...),
		client: &http.Client{
			Timeout:   alertWebhookTimeout,
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		now: time.Now,
	}
}

// Run polls until ctx is canceled. Events are queued transactionally with the
// state transition and are retried with bounded exponential backoff.
func (d *AlertWebhookDispatcher) Run(ctx context.Context) {
	d.dispatchDue(ctx)
	ticker := time.NewTicker(alertWebhookPollInterval)
	defer ticker.Stop()
	retentionTicker := time.NewTicker(24 * time.Hour)
	defer retentionTicker.Stop()
	d.pruneDelivered(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.dispatchDue(ctx)
		case <-retentionTicker.C:
			d.pruneDelivered(ctx)
		}
	}
}

func (d *AlertWebhookDispatcher) pruneDelivered(ctx context.Context) {
	removed, err := d.outbox.PruneDelivered(ctx, d.now().UTC().Add(-alertWebhookRetention))
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("alert-webhook: could not prune delivered outbox records: %v", err)
		}
		return
	}
	if removed > 0 {
		log.Printf("alert-webhook: removed %d delivered outbox record(s) older than 90 days", removed)
	}
}

func (d *AlertWebhookDispatcher) dispatchDue(ctx context.Context) {
	deliveries, err := d.outbox.ListDue(ctx, alertWebhookRequestLimit)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("alert-webhook: could not read outbox: %v", err)
		}
		return
	}
	if len(deliveries) == 0 {
		return
	}
	target, enabled, err := d.target.WebhookTarget(ctx)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("alert-webhook: could not load configured endpoint: %v", err)
			for _, delivery := range deliveries {
				d.scheduleRetry(ctx, delivery, "configured endpoint unavailable")
			}
		}
		return
	}
	if !enabled {
		return
	}
	for _, delivery := range deliveries {
		if ctx.Err() != nil {
			return
		}
		dispatchErr := d.send(ctx, delivery, target)
		if dispatchErr == nil {
			if err := d.outbox.MarkDelivered(ctx, delivery.ID); err != nil {
				log.Printf("alert-webhook: could not mark delivery %d complete: %v", delivery.ID, err)
			}
			continue
		}
		d.scheduleRetry(ctx, delivery, dispatchErr.Error())
	}
}

func (d *AlertWebhookDispatcher) scheduleRetry(ctx context.Context, delivery repository.MonitoringAlertWebhookDelivery, description string) {
	attempt := delivery.Attempts + 1
	next := d.now().UTC().Add(alertWebhookBackoff(attempt))
	if err := d.outbox.MarkFailed(ctx, delivery.ID, next, description); err != nil {
		log.Printf("alert-webhook: delivery %d failed and retry state could not be saved: %v", delivery.ID, err)
		return
	}
	log.Printf("alert-webhook: delivery %d failed (attempt %d); retry scheduled", delivery.ID, attempt)
}

func (d *AlertWebhookDispatcher) send(ctx context.Context, delivery repository.MonitoringAlertWebhookDelivery, target string) error {
	if err := validateDispatcherTarget(target, d.allowedHosts); err != nil {
		return err
	}
	payload, err := json.Marshal(struct {
		EventID int64 `json:"event_id"`
		repository.MonitoringAlertEvent
	}{EventID: delivery.Event.ID, MonitoringAlertEvent: delivery.Event})
	if err != nil {
		return fmt.Errorf("encode event")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build request")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "Qubes-Air-Alert-Webhook/1")
	response, err := d.client.Do(request)
	if err != nil {
		if errors.Is(err, errNonPublicWebhookAddress) {
			return errNonPublicWebhookAddress
		}
		return fmt.Errorf("request failed")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("endpoint returned HTTP %d", response.StatusCode)
	}
	return nil
}

func validateDispatcherTarget(raw string, allowedHosts []string) error {
	target, err := url.ParseRequestURI(raw)
	if err != nil || target.Scheme != "https" || target.Hostname() == "" || target.User != nil || target.Fragment != "" {
		return errors.New("invalid webhook URL")
	}
	if !slices.Contains(allowedHosts, webhookAllowlistKey(target)) {
		return errors.New("webhook host is not allowlisted")
	}
	return nil
}

// safeWebhookDialContext resolves the destination itself, refuses every
// non-public address, and dials one of the addresses it just validated. It
// never resolves twice, so a DNS answer that changes between validation and
// connection cannot move the destination inside the network. TLS still sees
// the original URL hostname, so certificate verification and SNI are
// unchanged.
func safeWebhookDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("invalid webhook address")
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve webhook host: %w", err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("webhook host has no addresses")
	}
	for _, ip := range ips {
		parsed, ok := netip.AddrFromSlice(ip.IP)
		if !ok || !netguard.IsPublicUnicast(parsed) {
			return nil, errNonPublicWebhookAddress
		}
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	var lastErr error
	for _, ip := range ips {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("connect webhook host: %w", lastErr)
}

func alertWebhookBackoff(attempt int) time.Duration {
	backoff := 5 * time.Second
	for i := 1; i < attempt && backoff < alertWebhookMaxBackoff; i++ {
		backoff *= 2
	}
	if backoff > alertWebhookMaxBackoff {
		return alertWebhookMaxBackoff
	}
	return backoff
}
