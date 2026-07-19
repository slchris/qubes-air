package scheduler

import (
	"context"
	"errors"
	"testing"
)

const gib = int64(1024 * 1024 * 1024)

// infraCluster mirrors the real cluster this was built against, so the tests
// exercise the numbers that actually occur rather than invented ones. Every
// node has 4 cores and ~31 GiB; the load is lopsided, which is precisely why
// scheduling was needed.
func infraCluster() []NodeCapacity {
	mk := func(name string, usedGiB float64, cpu float64) NodeCapacity {
		return NodeCapacity{
			Name: name, Online: true, MaxCPU: 4, CPUUsage: cpu,
			MemUsedBytes:  int64(usedGiB * float64(gib)),
			MemTotalBytes: 31 * gib,
		}
	}
	return []NodeCapacity{
		mk("infra-node1", 24.9, 0.482), // nearly full
		mk("infra-node2", 21.7, 0.395),
		mk("infra-node3", 13.6, 0.204),
		mk("infra-node4", 1.3, 0.014), // nearly empty
		mk("infra-node5", 7.3, 0.058),
		mk("infra-node6", 8.0, 0.157),
	}
}

// TestSelectPicksEmptiestNode — on the real cluster the previously hardcoded
// default (infra-node1) was the WORST choice available. The scheduler must pick
// the node with the most headroom instead.
func TestSelectPicksEmptiestNode(t *testing.T) {
	p, err := New().Select(context.Background(), infraCluster(), Requirements{MemoryMB: 8192, VCPU: 4})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if p.Node != "infra-node4" {
		t.Errorf("want infra-node4 (most free), got %q", p.Node)
	}
	if p.Reason == "" {
		t.Error("a placement must explain itself; an unexplained choice cannot be audited")
	}
	if len(p.Considered) != 6 {
		t.Errorf("every node must be recorded as considered, got %d", len(p.Considered))
	}
}

// TestSelectRejectsFullNode — infra-node1 has ~6.1 GiB free, and with 15%
// headroom (~4.65 GiB reserved) it cannot take an 8 GiB guest. Proxmox would
// permit the overcommit and let the node thrash, so the scheduler has to refuse
// on its own.
func TestSelectRejectsFullNode(t *testing.T) {
	only := []NodeCapacity{infraCluster()[0]} // infra-node1
	_, err := New().Select(context.Background(), only, Requirements{MemoryMB: 8192})
	if !errors.Is(err, ErrInsufficientCapacity) {
		t.Fatalf("want ErrInsufficientCapacity, got %v", err)
	}
}

// TestSelectExplainsRejection — when nothing fits, the operator needs to know
// why each node was ruled out, not just that it failed.
func TestSelectExplainsRejection(t *testing.T) {
	p, err := New().Select(context.Background(), infraCluster(), Requirements{MemoryMB: 64 * 1024})
	if !errors.Is(err, ErrInsufficientCapacity) {
		t.Fatalf("want ErrInsufficientCapacity, got %v", err)
	}
	if p == nil || len(p.Considered) != 6 {
		t.Fatal("the rejection must still report what was considered")
	}
	for _, c := range p.Considered {
		if c.Eligible {
			t.Errorf("%s should not be eligible for 64 GiB", c.Node)
		}
		if c.Reason == "" {
			t.Errorf("%s was rejected without a reason", c.Node)
		}
	}
}

// TestSelectSkipsOfflineNodes — an offline node has free memory on paper.
func TestSelectSkipsOfflineNodes(t *testing.T) {
	nodes := infraCluster()
	nodes[3].Online = false // infra-node4, the emptiest

	p, err := New().Select(context.Background(), nodes, Requirements{MemoryMB: 4096})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if p.Node == "infra-node4" {
		t.Error("must not place onto an offline node despite its free memory")
	}
	if p.Node != "infra-node5" {
		t.Errorf("want the emptiest ONLINE node (infra-node5), got %q", p.Node)
	}
}

// TestSelectIsDeterministic — identical input must always yield the same node.
// A scheduler that varies is impossible to reason about after the fact.
func TestSelectIsDeterministic(t *testing.T) {
	tied := []NodeCapacity{
		{Name: "beta", Online: true, MemUsedBytes: 8 * gib, MemTotalBytes: 31 * gib},
		{Name: "alpha", Online: true, MemUsedBytes: 8 * gib, MemTotalBytes: 31 * gib},
	}
	for i := range 20 {
		p, err := New().Select(context.Background(), tied, Requirements{MemoryMB: 1024})
		if err != nil {
			t.Fatalf("Select: %v", err)
		}
		if p.Node != "alpha" {
			t.Fatalf("ties must break deterministically on name; got %q on run %d", p.Node, i)
		}
	}
}

// TestSelectHeadroomIsEnforced — placing into the last sliver of RAM leaves
// nothing for the hypervisor and its caches.
func TestSelectHeadroomIsEnforced(t *testing.T) {
	// 10 GiB total, 6 GiB used -> 4 GiB free. Headroom reserves 15% of TOTAL
	// (1.5 GiB), leaving 2.5 GiB actually placeable.
	nodes := []NodeCapacity{{Name: "tight", Online: true, MemUsedBytes: 6 * gib, MemTotalBytes: 10 * gib}}

	if _, err := New().Select(context.Background(), nodes, Requirements{MemoryMB: 2048}); err != nil {
		t.Errorf("2 GiB fits within the 2.5 GiB placeable: %v", err)
	}
	// 3 GiB would fit the raw 4 GiB free, which is exactly the naive check that
	// lets a node be filled to the point of thrashing. It must be refused.
	if _, err := New().Select(context.Background(), nodes, Requirements{MemoryMB: 3072}); !errors.Is(err, ErrInsufficientCapacity) {
		t.Errorf("3 GiB must NOT fit once headroom is reserved, got %v", err)
	}
}

func TestSelectNoNodes(t *testing.T) {
	if _, err := New().Select(context.Background(), nil, Requirements{MemoryMB: 1024}); !errors.Is(err, ErrNoNodes) {
		t.Errorf("want ErrNoNodes, got %v", err)
	}
}

// TestFreeMemNeverNegative — Proxmox can report used above total during
// measurement skew; a negative free value would make a full node look infinite.
func TestFreeMemNeverNegative(t *testing.T) {
	n := NodeCapacity{Name: "skewed", Online: true, MemUsedBytes: 33 * gib, MemTotalBytes: 31 * gib}
	if got := n.FreeMemBytes(); got != 0 {
		t.Errorf("want 0, got %d", got)
	}
	if _, err := New().Select(context.Background(), []NodeCapacity{n}, Requirements{MemoryMB: 1}); !errors.Is(err, ErrInsufficientCapacity) {
		t.Errorf("an over-full node must be rejected, got %v", err)
	}
}
