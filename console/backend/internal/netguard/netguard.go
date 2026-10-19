// Package netguard classifies destination addresses for outbound requests that
// must reach the public internet only.
package netguard

import "net/netip"

// specialUsePrefixes are IPv4 ranges that netip calls global unicast but that
// no public webhook should ever target.
var specialUsePrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),  // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"), // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),   // reserved
}

// IsPublicUnicast reports whether addr is a globally routable unicast address
// suitable for an outbound webhook. Loopback, private, link-local, multicast,
// unspecified and special-use addresses are refused.
func IsPublicUnicast(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() || addr.IsUnspecified() {
		return false
	}
	for _, prefix := range specialUsePrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}
