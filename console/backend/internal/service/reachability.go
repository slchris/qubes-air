package service

import (
	"context"
	"fmt"

	"github.com/slchris/qubes-air/console/internal/models"
)

// AgentReachability adapts AgentProber to orchestrator.ReachabilityProbe so the
// native executor can gate a provision on the agent actually answering.
type AgentReachability struct {
	prober *AgentProber
}

// NewAgentReachability wires the prober as the provision-time gate.
func NewAgentReachability(prober *AgentProber) *AgentReachability {
	return &AgentReachability{prober: prober}
}

// ProbeReachable returns nil when the agent answers, else the probe's reason.
func (a *AgentReachability) ProbeReachable(ctx context.Context, q *models.Qube) error {
	if a.prober == nil {
		return nil
	}
	res := a.prober.Probe(ctx, q)
	if res.Reachable {
		return nil
	}
	reason := res.Reason
	if reason == "" {
		reason = string(res.Status)
	}
	return fmt.Errorf("%s", reason)
}
