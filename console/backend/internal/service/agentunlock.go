// agentunlock.go — pushes a qube's data-disk key to its agent to open the
// encrypted /data.
//
// This runs after bootstrap succeeds, so the agent already holds its real
// identity and the channel is VERIFIED (the agent's certificate CN is pinned to
// agent-<qube>, exactly like the prober). The bootstrap dial deliberately skips
// server verification because a bootstrapping agent has no certificate yet; a
// SECRET must never travel over that unverified channel, so the unlock uses the
// prober's verified path instead.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
	transportgrpc "github.com/slchris/qubes-air/console/internal/transport/grpc"
)

const (
	unlockRelayName    = "console-unlock"
	unlockCertLifetime = 5 * time.Minute
	unlockDataService  = "qubesair.UnlockData"

	// DefaultDataUnlockTimeout bounds one unlock attempt: dial, verified
	// handshake, and the agent's luksFormat/luksOpen/mkfs/mount. Generous because
	// first-boot formatting of a fresh container is the slow case, still short
	// enough that a wedged agent does not pin the bootstrap sweep.
	DefaultDataUnlockTimeout = 60 * time.Second
)

// DataKeyProvider derives a qube's data-disk passphrase.
type DataKeyProvider interface {
	DataKeyFor(ctx context.Context, qubeID string) (string, error)
}

// AgentDataUnlocker opens a qube's encrypted data disk by deriving its key and
// pushing it to qubesair.UnlockData over verified mTLS. The key is derived on
// demand and never stored anywhere but the request; the agent holds it only in
// RAM. Idempotent on the agent, so calling it after every bootstrap is safe.
type AgentDataUnlocker struct {
	ca      CAProvider
	keys    DataKeyProvider
	dialer  AgentDialer
	timeout time.Duration
}

// NewAgentDataUnlocker builds an unlocker. A nil return of ca or keys makes
// Unlock a no-op error, which is how a console with encryption disabled behaves.
func NewAgentDataUnlocker(ca CAProvider, keys DataKeyProvider, agentListen string, timeout time.Duration) *AgentDataUnlocker {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &AgentDataUnlocker{
		ca:      ca,
		keys:    keys,
		dialer:  NewDirectDialer(agentListen),
		timeout: timeout,
	}
}

// UnlockResult is the agent's answer: whether /data is now open, and why not.
type UnlockResult struct {
	Unlocked bool
	Detail   string
}

// Unlock derives the qube's data key and asks its agent to open /data.
func (u *AgentDataUnlocker) Unlock(ctx context.Context, qube *models.Qube) (UnlockResult, error) {
	if u == nil || u.ca == nil || u.keys == nil {
		return UnlockResult{}, errors.New("no data unlocker configured")
	}
	if qube == nil {
		return UnlockResult{}, errors.New("no qube given")
	}
	if strings.TrimSpace(qube.IPAddress) == "" {
		return UnlockResult{}, fmt.Errorf("qube %q has no address to unlock", qube.Name)
	}

	// Derive first: a derivation failure means the console's master secret is
	// unavailable, and there is no point dialing the agent to hand it nothing.
	key, err := u.keys.DataKeyFor(ctx, qube.ID)
	if err != nil {
		return UnlockResult{}, fmt.Errorf("derive data key for %q: %w", qube.Name, err)
	}

	ctx, cancel := context.WithTimeout(ctx, u.timeout)
	defer cancel()

	ca, err := u.ca.CA(ctx)
	if err != nil {
		return UnlockResult{}, fmt.Errorf("no usable CA to reach %q: %w", qube.Name, err)
	}
	bundle, err := ca.IssueAgentCert(unlockRelayName, unlockCertLifetime)
	if err != nil {
		return UnlockResult{}, fmt.Errorf("mint unlock client certificate: %w", err)
	}
	// Pin the peer to agent-<qube>: the key must reach THIS qube's agent and no
	// impostor at its address. Same binding the prober enforces.
	tlsCfg, err := probeTLSConfig(bundle, AgentCommonName(qube.Name))
	if err != nil {
		return UnlockResult{}, fmt.Errorf("unlock client certificate unusable: %w", err)
	}

	addr := u.dialer.Address(qube)
	cli := transportgrpc.NewClient(transportgrpc.ClientConfig{
		RemoteEndpoint: addr,
		RelayName:      unlockRelayName,
		RemoteName:     qube.Name,
		Dialer:         dialFuncFor(u.dialer, qube),
		ReconnectMin:   20 * time.Millisecond,
		ReconnectMax:   200 * time.Millisecond,
		TLS:            tlsCfg.Clone(),
	}, nil)
	go func() { _ = cli.Start(ctx) }()

	out, err := callWhenConnected(ctx, cli, qube.Name, unlockDataService, []byte(key))
	if err != nil {
		return UnlockResult{}, fmt.Errorf("call %s on %q: %w", unlockDataService, qube.Name, err)
	}

	var reply struct {
		Unlocked bool   `json:"unlocked"`
		Detail   string `json:"detail"`
	}
	if err := json.Unmarshal(out, &reply); err != nil {
		return UnlockResult{}, fmt.Errorf("unparseable %s reply from %q: %v (%q)",
			unlockDataService, qube.Name, err, strings.TrimSpace(string(out)))
	}
	return UnlockResult{Unlocked: reply.Unlocked, Detail: reply.Detail}, nil
}

// callWhenConnected calls a service, retrying only while the tunnel is still
// coming up (Call reports ErrNotConnected until Start's first handshake lands).
// Any other error is returned immediately. Mirrors the prober's ping loop.
func callWhenConnected(
	ctx context.Context, cli *transportgrpc.Client, target, service string, in []byte,
) ([]byte, error) {
	const retryEvery = 25 * time.Millisecond
	var lastErr error
	for {
		out, err := cli.Call(ctx, target, service, in)
		if err == nil {
			return out, nil
		}
		lastErr = err
		if !errors.Is(err, transportgrpc.ErrNotConnected) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("tunnel never established: %w", lastErr)
		case <-time.After(retryEvery):
		}
	}
}

// UnlockData is the callback shape the bootstrap monitor's AfterBootstrap hook
// wants. It unlocks only qubes whose spec asks for it, logs the outcome, and
// never returns an error the sweep would have to handle — a failed unlock leaves
// the data safe (still encrypted) and is retried on the next resume.
func (u *AgentDataUnlocker) UnlockData(ctx context.Context, qube *models.Qube) {
	if qube == nil || !qube.Spec.EncryptData {
		return
	}
	res, err := u.Unlock(ctx, qube)
	switch {
	case err != nil:
		log.Printf("unlock: qube %q data disk NOT opened: %v (data stays encrypted; retried on next resume)", qube.Name, err)
	case !res.Unlocked:
		log.Printf("unlock: qube %q data disk NOT opened: %s (data stays encrypted; retried on next resume)", qube.Name, res.Detail)
	default:
		log.Printf("unlock: qube %q data disk opened and mounted (%s)", qube.Name, res.Detail)
	}
}
