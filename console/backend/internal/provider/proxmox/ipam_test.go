package proxmox

import (
	"net/netip"
	"strings"
	"testing"
)

func TestPoolHostsExcludesNetworkAndBroadcast(t *testing.T) {
	hosts, err := poolHosts(netip.MustParsePrefix("10.31.0.64/30"))
	if err != nil {
		t.Fatalf("poolHosts: %v", err)
	}
	got := []string{hosts[0].String(), hosts[1].String()}
	if len(hosts) != 2 || got[0] != "10.31.0.65" || got[1] != "10.31.0.66" {
		t.Fatalf("hosts = %v, want [10.31.0.65 10.31.0.66]", got)
	}
}

func TestPoolHostsKeepsPointToPointPair(t *testing.T) {
	hosts, err := poolHosts(netip.MustParsePrefix("10.31.0.64/31"))
	if err != nil {
		t.Fatalf("poolHosts: %v", err)
	}
	if len(hosts) != 2 {
		t.Fatalf("a /31 has no broadcast; got %d hosts", len(hosts))
	}
}

func TestPoolHostsRejectsIPv6(t *testing.T) {
	if _, err := poolHosts(netip.MustParsePrefix("fd00::/64")); err == nil {
		t.Fatal("expected an error for an IPv6 pool")
	}
}

func TestAllocateIPSkipsUsedAddresses(t *testing.T) {
	used := map[string]bool{
		"10.31.0.65": true,
		"10.31.0.66": true,
	}
	addr, err := allocateIP("10.31.0.64/29", "qube-1", func(a netip.Addr) bool {
		return !used[a.String()]
	})
	if err != nil {
		t.Fatalf("allocateIP: %v", err)
	}
	if used[addr.String()] {
		t.Fatalf("allocated an address reported as used: %s", addr)
	}
}

func TestAllocateIPIsDeterministic(t *testing.T) {
	free := func(netip.Addr) bool { return true }
	first, err := allocateIP("10.31.0.64/27", "stable-id", free)
	if err != nil {
		t.Fatal(err)
	}
	second, err := allocateIP("10.31.0.64/27", "stable-id", free)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("same id gave %s then %s", first, second)
	}
}

func TestAllocateIPExhausted(t *testing.T) {
	_, err := allocateIP("10.31.0.64/30", "q", func(netip.Addr) bool { return false })
	if err == nil || !strings.Contains(err.Error(), "no free address") {
		t.Fatalf("err = %v, want exhaustion", err)
	}
}

func TestAllocateIPBadPool(t *testing.T) {
	if _, err := allocateIP("not-a-cidr", "q", func(netip.Addr) bool { return true }); err == nil {
		t.Fatal("expected an error for a malformed pool")
	}
}

func TestIPConfig0(t *testing.T) {
	got := ipconfig0(netip.MustParseAddr("10.31.0.70"), 27, "10.31.0.254")
	if got != "ip=10.31.0.70/27,gw=10.31.0.254" {
		t.Fatalf("ipconfig0 = %q", got)
	}
}
