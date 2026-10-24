package desktop

import (
	"context"
	"errors"
	"net"
	"time"
)

// XpraSocketPath is the only Xpra endpoint the agent may open. It is not a
// request parameter or environment override, and no TCP fallback exists.
const XpraSocketPath = "/run/qubes-air-desktop/xpra.sock"

// DialXpra authenticates the kernel credentials before returning an opaque
// desktop stream. mTLS caller authorization happens before this local dial.
func DialXpra(ctx context.Context) (net.Conn, error) {
	return dialXpra(ctx, XpraSocketPath, peerUID)
}

func dialXpra(ctx context.Context, path string, peer peerReader) (net.Conn, error) {
	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, errors.New("xpra session unavailable")
	}
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		_ = conn.Close()
		return nil, errors.New("xpra peer credentials unavailable")
	}
	uid, err := peer(unixConn)
	if err != nil || uid != DesktopUID {
		_ = conn.Close()
		return nil, errors.New("xpra peer is not the desktop user")
	}
	return conn, nil
}
