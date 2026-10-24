package desktop

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRequestContract(t *testing.T) {
	for _, raw := range []string{
		`{"operation":"menus"}`,
		`{"operation":"launch","app":"org.example.Editor.desktop"}`,
	} {
		_, err := decodeRequest(bytes.NewBufferString(raw))
		require.NoError(t, err)
	}
	for _, raw := range []string{
		``, `null`, `[]`, `{"operation":"shell"}`, `{"operation":"menus","app":"x"}`,
		`{"operation":"launch"}`, `{"operation":"launch","app":"../x"}`,
		`{"operation":"launch","app":"a..b"}`, `{"operation":"launch","app":"."}`,
		`{"operation":"launch","app":"x","path":"/bin/true"}`,
		`{"operation":"menus"} {"operation":"launch","app":"x"}`,
		`{"operation":"launch","app":"` + strings.Repeat("a", 114) + `"}`,
		strings.Repeat(" ", MaxRequestBytes+1),
	} {
		_, err := decodeRequest(bytes.NewBufferString(raw))
		require.Error(t, err, raw)
	}
}

func TestResponseContract(t *testing.T) {
	request := Request{Operation: "launch", App: "editor.desktop"}
	require.NoError(t, validateResponse(request, Response{OK: true, App: request.App, Display: ":100"}))
	for _, reply := range []Response{
		{}, {OK: true, App: "other.desktop", Display: ":100"},
		{OK: true, App: request.App, Display: "host:100"},
		{OK: true, App: request.App, Display: ":100", Menu: "unexpected"},
		{OK: false, Code: 0, Error: "missing code"},
	} {
		require.Error(t, validateResponse(request, reply))
	}
}

func TestMenuResponseDoesNotTrustDesktopMetadata(t *testing.T) {
	request := Request{Operation: "menus"}
	require.NoError(t, validateResponse(request, Response{OK: true, Menu: "a.desktop:Name=Editor\na.desktop:Type=Application\na.desktop:Exec=qubes-desktop-run a.desktop\n"}))
	for _, menu := range []string{
		"../a:Name=Editor\n", "a.desktop:Exec=unexpected command\n", "a.desktop:Name=bad\x1b[0m\n",
		"a.desktop:Unknown=field\n", "a.desktop:Icon=/etc/passwd\n", strings.Repeat("x", MaxResponseBytes+1),
	} {
		require.Error(t, validateResponse(request, Response{OK: true, Menu: menu}))
	}
}

func TestMenuRejectsDuplicateIncompleteAndExcessApplications(t *testing.T) {
	request := Request{Operation: "menus"}
	for _, menu := range []string{"a:Name=Editor\n", "a:Name=Editor\na:Name=Other\na:Type=Application\na:Exec=qubes-desktop-run a\n"} {
		require.Error(t, validateResponse(request, Response{OK: true, Menu: menu}))
	}
	var menu strings.Builder
	for i := range MaxApps + 1 {
		fmt.Fprintf(&menu, "app%d:Name=Editor\napp%d:Type=Application\napp%d:Exec=qubes-desktop-run app%d\n", i, i, i, i)
	}
	require.Error(t, validateResponse(request, Response{OK: true, Menu: menu.String()}))
}

func TestWireEncodingHasItsOwnOutputBound(t *testing.T) {
	reply, data := encodeResponse(Response{OK: true, Menu: strings.Repeat("<", MaxResponseBytes/2)})
	require.Equal(t, 75, reply.Code)
	require.LessOrEqual(t, len(data), MaxResponseBytes)
}
