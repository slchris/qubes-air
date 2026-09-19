package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/slchris/qubes-air/console/internal/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetAppMenus_OK — the menu is fetched over the transport addressed to the
// qube's NAME and the appmenus service, and returned verbatim.
func TestGetAppMenus_OK(t *testing.T) {
	fake := &transport.FakeTransport{
		RespFn: func(target, service string, _ []byte) ([]byte, error) {
			if target != "reach-qube" || service != appmenusService {
				return nil, errors.New("unexpected call")
			}
			return []byte("firefox.desktop:Name=Firefox\nfirefox.desktop:Exec=qubes-desktop-run firefox.desktop\n"), nil
		},
	}
	svc, id, cleanup := setupWithTransport(t, fake)
	defer cleanup()

	menu, err := svc.GetAppMenus(context.Background(), id)
	require.NoError(t, err)
	assert.Contains(t, menu, "firefox.desktop:Exec=qubes-desktop-run firefox.desktop")
	assert.Equal(t, 1, fake.CallCount())
	assert.Equal(t, "reach-qube", fake.Calls[0].Target)
	assert.Equal(t, appmenusService, fake.Calls[0].Service)
}

func TestGetAppMenus_TransportError(t *testing.T) {
	fake := &transport.FakeTransport{
		RespFn: func(_, _ string, _ []byte) ([]byte, error) {
			return nil, errors.New("tunnel down")
		},
	}
	svc, id, cleanup := setupWithTransport(t, fake)
	defer cleanup()

	_, err := svc.GetAppMenus(context.Background(), id)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnreachable)
	assert.Contains(t, err.Error(), "tunnel down", "the real cause must survive to the caller")
}

func TestGetAppMenus_NotFound(t *testing.T) {
	svc, _, cleanup := setupWithTransport(t, &transport.FakeTransport{})
	defer cleanup()

	_, err := svc.GetAppMenus(context.Background(), "does-not-exist")
	assert.ErrorIs(t, err, ErrQubeNotFound)
}

// TestLaunchApp_OK — the app id becomes the qrexec service ARGUMENT.
func TestLaunchApp_OK(t *testing.T) {
	fake := &transport.FakeTransport{
		RespFn: func(target, service string, _ []byte) ([]byte, error) {
			if target != "reach-qube" || service != startAppServicePrefix+"firefox.desktop" {
				return nil, errors.New("unexpected call")
			}
			return []byte("qubes.StartApp: launched 'firefox.desktop' on :100\n"), nil
		},
	}
	svc, id, cleanup := setupWithTransport(t, fake)
	defer cleanup()

	out, err := svc.LaunchApp(context.Background(), id, "firefox.desktop")
	require.NoError(t, err)
	assert.Contains(t, out, "launched")
	assert.Equal(t, startAppServicePrefix+"firefox.desktop", fake.Calls[0].Service)
}

// TestLaunchApp_InvalidAppID — a malformed app id is refused BEFORE the
// transport: zero upstream calls, whatever the shape of the call.
func TestLaunchApp_InvalidAppID(t *testing.T) {
	fake := &transport.FakeTransport{}
	svc, id, cleanup := setupWithTransport(t, fake)
	defer cleanup()

	for _, bad := range []string{"../firefox", "a/b", "a b", "a\nb", "x;reboot", "", strings.Repeat("a", MaxAppIDLen+1)} {
		_, err := svc.LaunchApp(context.Background(), id, bad)
		require.ErrorIs(t, err, ErrInvalidAppID, "app %q", bad)
	}
	assert.Zero(t, fake.CallCount(), "an invalid app id must never reach the transport")
}

func TestLaunchApp_NotFound(t *testing.T) {
	svc, _, cleanup := setupWithTransport(t, &transport.FakeTransport{})
	defer cleanup()

	_, err := svc.LaunchApp(context.Background(), "does-not-exist", "firefox.desktop")
	assert.ErrorIs(t, err, ErrQubeNotFound)
}

// TestValidAppID — the allowlist the qrexec service argument must pass.
func TestValidAppID(t *testing.T) {
	// .desktop basenames and the app ids desktop_apps_list emits (slashes
	// flattened to dashes) all fall inside [A-Za-z0-9._+-].
	for _, good := range []string{
		"firefox.desktop", "org.gnome.Terminal", "a+b", "A_b.c-d",
		strings.Repeat("a", MaxAppIDLen),
	} {
		assert.True(t, ValidAppID(good), "app %q", good)
	}
	// Everything else — path traversal, separators, whitespace, shell
	// metacharacters, over-long ids — is refused.
	for _, bad := range []string{
		"", "../firefox", "a/b", "a b", "a\nb", "a\tb", "x;rm", "`reboot`", "$(x)",
		strings.Repeat("a", MaxAppIDLen+1),
	} {
		assert.False(t, ValidAppID(bad), "app %q", bad)
	}
}
