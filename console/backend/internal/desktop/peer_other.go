//go:build !linux

package desktop

import (
	"errors"
	"net"
)

func peerUID(*net.UnixConn) (uint32, error) {
	return 0, errors.New("desktop broker requires Linux peer credentials")
}
