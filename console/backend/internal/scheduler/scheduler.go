// Package scheduler chooses which cluster node a qube should run on.
//
// Placement is only a free choice when storage is shared. Proxmox allows a
// clone to target another node ONLY if the source VM is on shared storage
// (qemu-server rejects `target` otherwise), so on Ceph/NFS one template serves
// the whole cluster and a scheduler has something to decide. With node-local
// storage a qube must live wherever its template and disks already are, and the
// scheduler must not be consulted at all.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// NodeCapacity is a point-in-time view of one cluster node.
type NodeCapacity struct {
	Name   string
	Online bool
	// MaxCPU is the core count. CPUUsage is the current load as a fraction
	// (0..1) — Proxmox reports usage, not reservation, so it says nothing about
	// what other guests are entitled to burst to.
	MaxCPU   int
	CPUUsage float64
	// MemUsedBytes/MemTotalBytes are physical memory. Proxmox permits memory
	// overcommit, so a placement that "succeeds" can still leave guests fighting
	// over RAM — which is exactly why this scheduler enforces its own headroom
	// rather than trusting the create call to fail.
	MemUsedBytes  int64
	MemTotalBytes int64
}

// FreeMemBytes reports unused physical memory.
func (n NodeCapacity) FreeMemBytes() int64 {
	free := n.MemTotalBytes - n.MemUsedBytes
	if free < 0 {
		return 0
	}
	return free
}

// Requirements describes what a qube needs.
type Requirements struct {
	MemoryMB int
	VCPU     int
}

// Placement is a scheduling decision, kept with its reasoning so an operator
// can later answer "why did this land here?" without re-deriving it.
type Placement struct {
	Node   string
	Reason string
	// Considered lists every node examined and why it was or was not eligible.
	Considered []Candidate
}

// Candidate records one node's evaluation.
type Candidate struct {
	Node         string
	Eligible     bool
	Reason       string
	FreeMemBytes int64
}

// Errors returned by Select.
var (
	// ErrNoNodes means the cluster reported nothing to schedule onto.
	ErrNoNodes = errors.New("no cluster nodes available")
	// ErrInsufficientCapacity means no node had room. This is deliberately a
	// hard failure: Proxmox would happily overcommit, and discovering that at
	// runtime means guests thrashing rather than a clear refusal up front.
	ErrInsufficientCapacity = errors.New("no node has enough free memory")
)

// DefaultHeadroomFraction is the share of a node's memory kept unused.
//
// Placing a guest into the last few percent of RAM leaves nothing for the
// hypervisor, ZFS/Ceph caches, or the guest's own overhead, so the node starts
// swapping under load. 15% is a pragmatic reserve rather than a tuned value.
const DefaultHeadroomFraction = 0.15

// Scheduler picks a node for a qube.
//
// The policy is intentionally simple and explainable: filter to nodes that can
// fit the request with headroom, then take the one with the most free memory.
// Memory is the binding constraint on a typical Proxmox cluster — CPU is
// time-shared and overcommits gracefully, RAM does not — and a single-criterion
// rule produces a decision an operator can verify at a glance. A weighted
// multi-factor score would be harder to trust and no more correct here.
type Scheduler struct {
	// HeadroomFraction is the share of each node's memory left unused.
	HeadroomFraction float64
}

// New creates a Scheduler with the default headroom.
func New() *Scheduler {
	return &Scheduler{HeadroomFraction: DefaultHeadroomFraction}
}

// Select chooses the best node for req, or reports why none fit.
func (s *Scheduler) Select(_ context.Context, nodes []NodeCapacity, req Requirements) (*Placement, error) {
	if len(nodes) == 0 {
		return nil, ErrNoNodes
	}

	headroom := s.HeadroomFraction
	if headroom <= 0 || headroom >= 1 {
		headroom = DefaultHeadroomFraction
	}
	needBytes := int64(req.MemoryMB) * 1024 * 1024

	considered := make([]Candidate, 0, len(nodes))
	eligible := make([]NodeCapacity, 0, len(nodes))

	for _, n := range nodes {
		c := Candidate{Node: n.Name, FreeMemBytes: n.FreeMemBytes()}
		switch {
		case !n.Online:
			c.Reason = "node is offline"
		case n.MemTotalBytes <= 0:
			c.Reason = "node reported no memory"
		default:
			reserve := int64(float64(n.MemTotalBytes) * headroom)
			usable := n.FreeMemBytes() - reserve
			if usable < needBytes {
				c.Reason = fmt.Sprintf("needs %s, only %s usable after %.0f%% headroom",
					humanBytes(needBytes), humanBytes(maxInt64(usable, 0)), headroom*100)
			} else {
				c.Eligible = true
				c.Reason = fmt.Sprintf("%s free", humanBytes(n.FreeMemBytes()))
				eligible = append(eligible, n)
			}
		}
		considered = append(considered, c)
	}

	if len(eligible) == 0 {
		return &Placement{Considered: considered}, fmt.Errorf("%w: %d MB requested", ErrInsufficientCapacity, req.MemoryMB)
	}

	// Most free memory wins. Ties break on name so the choice is deterministic —
	// a scheduler that picks differently on identical input is impossible to
	// reason about after the fact.
	sort.Slice(eligible, func(a, b int) bool {
		if eligible[a].FreeMemBytes() != eligible[b].FreeMemBytes() {
			return eligible[a].FreeMemBytes() > eligible[b].FreeMemBytes()
		}
		return eligible[a].Name < eligible[b].Name
	})

	best := eligible[0]
	return &Placement{
		Node: best.Name,
		Reason: fmt.Sprintf("most free memory: %s of %s (%d of %d candidates eligible)",
			humanBytes(best.FreeMemBytes()), humanBytes(best.MemTotalBytes),
			len(eligible), len(considered)),
		Considered: considered,
	}, nil
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// humanBytes renders a byte count for an operator-facing reason string.
func humanBytes(b int64) string {
	const gib = 1024 * 1024 * 1024
	if b >= gib {
		return fmt.Sprintf("%.1f GiB", float64(b)/gib)
	}
	return fmt.Sprintf("%d MiB", b/(1024*1024))
}
