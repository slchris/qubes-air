package scheduler

import (
	"context"
	"fmt"

	"github.com/slchris/qubes-air/console/internal/proxmoxnetwork"
)

// PlacementNodes reads network eligibility afresh for each online node. A node
// whose network cannot be verified is excluded, while other nodes may qualify.
func (p *ProxmoxProvider) PlacementNodes(ctx context.Context, req proxmoxnetwork.Requirement) ([]NodeCapacity, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNetworkUnavailable, err)
	}
	nodes, err := p.Nodes(ctx)
	if err != nil {
		return nil, err
	}
	for i := range nodes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !nodes[i].Online {
			continue
		}
		if err := p.checkNetwork(ctx, nodes[i].Name, req); err != nil {
			nodes[i].NetworkIssue = err.Error()
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nodes, nil
}

func (p *ProxmoxProvider) checkNetwork(ctx context.Context, node string, req proxmoxnetwork.Requirement) error {
	path, err := proxmoxnetwork.Path(node)
	if err != nil {
		return err
	}
	raw, err := p.get(ctx, path)
	if err != nil {
		return err
	}
	return req.Check(raw)
}
