package main

import (
	"testing"

	transportgrpc "github.com/slchris/qubes-air/console/internal/transport/grpc"
	"github.com/stretchr/testify/require"
)

func TestRelayDesktopUsesFixedService(t *testing.T) {
	target, service, err := parseRelayTarget(true, []string{"work", "desktop"})
	require.NoError(t, err)
	require.Equal(t, "work", target)
	require.Equal(t, transportgrpc.DesktopStreamService, service)
	_, _, err = parseRelayTarget(true, []string{"work", "desktop", "extra"})
	require.Error(t, err)
}
