package netguard

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsPublicUnicast(t *testing.T) {
	for _, raw := range []string{
		"8.8.8.8",
		"1.1.1.1",
		"2606:4700:4700::1111",
		"2001:4860:4860::8888",
	} {
		addr := netip.MustParseAddr(raw)
		require.True(t, IsPublicUnicast(addr), raw)
	}

	for _, raw := range []string{
		"0.0.0.0",
		"127.0.0.1",
		"10.1.2.3",
		"172.16.0.1",
		"192.168.1.1",
		"169.254.1.1",
		"100.64.0.1",
		"192.0.0.1",
		"198.18.0.1",
		"240.0.0.1",
		"224.0.0.1",
		"::",
		"::1",
		"fe80::1",
		"fc00::1",
		"ff02::1",
	} {
		addr := netip.MustParseAddr(raw)
		require.False(t, IsPublicUnicast(addr), raw)
	}
}
