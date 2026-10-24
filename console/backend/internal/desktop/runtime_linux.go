//go:build linux

package desktop

import (
	"context"
	"errors"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
)

// Run serves the fixed socket as the existing desktop user. systemd owns the
// runtime directory and removes it after stopping the unit's entire cgroup.
func Run(ctx context.Context) error {
	if os.Getuid() != DesktopUID || os.Geteuid() != DesktopUID {
		return errors.New("desktop broker requires uid 1000")
	}
	account, err := user.LookupId("1000")
	if err != nil {
		return errors.New("desktop account unavailable")
	}
	env, err := sessionEnvironment(account, os.Getenv("DBUS_SESSION_BUS_ADDRESS"))
	if err != nil {
		return err
	}
	listener, err := listenSocket(SocketPath, DesktopUID)
	if err != nil {
		return err
	}
	w := worker{program: "/usr/bin/python3", args: []string{"-I", WorkerPath}, env: env, dir: DesktopHome}
	s := server{peer: peerUID, handle: w.run, timeout: requestTimeout, maxClients: 4}
	return s.serve(ctx, listener)
}

func sessionEnvironment(account *user.User, bus string) ([]string, error) {
	home := account.HomeDir
	if account.Uid != "1000" || home != DesktopHome {
		return nil, errors.New("invalid desktop home")
	}
	if !strings.HasPrefix(bus, "unix:path=/") || strings.ContainsAny(bus, "\r\n\x00") {
		return nil, errors.New("private desktop D-Bus session unavailable")
	}
	return []string{
		"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8",
		"HOME=" + home, "USER=" + account.Username, "LOGNAME=" + account.Username,
		"XDG_DATA_HOME=" + filepath.Join(home, ".local/share"),
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"XDG_DATA_DIRS=/usr/local/share:/usr/share", "XDG_CURRENT_DESKTOP=Qubes",
		"XDG_RUNTIME_DIR=/run/qubes-air-desktop", "DISPLAY=:100",
		"XAUTHORITY=/run/qubes-air-desktop/Xauthority", "DBUS_SESSION_BUS_ADDRESS=" + bus,
	}, nil
}

func listenSocket(path string, uid uint32) (*net.UnixListener, error) {
	info, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm() != 0o700 || stat.Uid != uid {
		return nil, errors.New("desktop runtime directory must be private and owned by the desktop user")
	}
	// Refuse existing files, symlinks, stale and live sockets. Never unlink an
	// endpoint based on a failed probe: a live or replaced listener may own it.
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("desktop socket already exists or cannot be inspected")
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	listener.SetUnlinkOnClose(false)
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return listener, nil
}
