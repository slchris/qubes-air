package proxmox

import (
	"fmt"
	"hash/fnv"
	"net/netip"
	"strings"
)

// poolHosts returns the usable IPv4 addresses of prefix, in order.
//
// It is IPv4-only on purpose: the adapter writes ipconfig0, which takes an IPv4
// address/netmask, and a pool that silently accepted IPv6 would produce configs
// PVE cannot honor.
func poolHosts(prefix netip.Prefix) ([]netip.Addr, error) {
	prefix = prefix.Masked()
	if !prefix.Addr().Is4() {
		return nil, fmt.Errorf("proxmox: ip_pool %s is not IPv4", prefix)
	}
	var hosts []netip.Addr
	// The network address is usable for /31 and /32; only for smaller prefixes
	// is it reserved.
	start := prefix.Addr()
	if prefix.Bits() < 31 {
		start = start.Next()
	}
	for a := start; prefix.Contains(a); a = a.Next() {
		if !a.IsValid() {
			break
		}
		hosts = append(hosts, a)
	}
	// Drop the broadcast address for any prefix that has one. A /31 is a
	// point-to-point pair with no network/broadcast, so leave it alone.
	if prefix.Bits() <= 30 && len(hosts) >= 2 {
		hosts = hosts[:len(hosts)-1]
	}
	if len(hosts) == 0 {
		return nil, fmt.Errorf("proxmox: ip_pool %s has no usable addresses", prefix)
	}
	return hosts, nil
}

// allocateIP picks an address from pool for id.
//
// The start position is derived from id rather than always being the first free
// address: two qubes provisioned together would otherwise both begin at the
// same address, and one would win the ARP race. isFree is consulted for each
// candidate in turn, so an address already in use (by another qube or a host
// outside the console) is skipped instead of handed out twice.
func allocateIP(pool, id string, isFree func(netip.Addr) bool) (netip.Addr, error) {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(pool))
	if err != nil {
		return netip.Addr{}, fmt.Errorf("proxmox: bad ip_pool %q: %w", pool, err)
	}
	hosts, err := poolHosts(prefix)
	if err != nil {
		return netip.Addr{}, err
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(id))
	// #nosec G115 -- the modulo result is < len(hosts), which fits in int.
	start := int(h.Sum64() % uint64(len(hosts)))
	for i := range hosts {
		candidate := hosts[(start+i)%len(hosts)]
		if isFree(candidate) {
			return candidate, nil
		}
	}
	return netip.Addr{}, fmt.Errorf("proxmox: ip_pool %s has no free address", prefix)
}

// ipconfig0 renders the PVE cloud-init network string for a static address.
func ipconfig0(addr netip.Addr, prefixLen int, gateway string) string {
	return fmt.Sprintf("ip=%s/%d,gw=%s", addr, prefixLen, gateway)
}
