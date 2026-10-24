package desktop

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCLIClosedInterface(t *testing.T) {
	for _, args := range [][]string{nil, {"launch"}, {"launch", "a", "b"}, {"menus", "x"}, {"launch", "../x"}, {"daemon", "extra"}, {"--socket", "/tmp/x"}} {
		var out, stderr bytes.Buffer
		code := runCLI(context.Background(), args, &out, &stderr, func(context.Context, Request) Response { t.Fatal("unexpected call"); return Response{} }, Run)
		require.Equal(t, 64, code)
		require.Empty(t, out.String())
	}
}

func TestCLIAcknowledgementAndFailure(t *testing.T) {
	for _, reply := range []Response{{OK: true, App: "editor", Display: ":100"}, failure(69), failure(75)} {
		var out, stderr bytes.Buffer
		code := runCLI(context.Background(), []string{"launch", "editor"}, &out, &stderr,
			func(_ context.Context, r Request) Response {
				require.Equal(t, Request{Operation: "launch", App: "editor"}, r)
				return reply
			}, Run)
		require.Equal(t, reply.Code, code)
		if reply.OK {
			require.Equal(t, "qubes.StartApp: accepted 'editor' on :100\n", out.String())
			require.Empty(t, stderr.String())
		} else {
			require.Empty(t, out.String())
			require.Contains(t, stderr.String(), reply.Error)
		}
	}
}

func TestCLIHelpMenusAndDaemon(t *testing.T) {
	var out, stderr bytes.Buffer
	require.Zero(t, runCLI(context.Background(), []string{"-h"}, &out, &stderr, Call, Run))
	out.Reset()
	require.Zero(t, runCLI(context.Background(), []string{"menus"}, &out, &stderr, func(context.Context, Request) Response { return Response{OK: true, Menu: "editor:Name=Editor\n"} }, Run))
	require.Equal(t, "editor:Name=Editor\n", out.String())
	out.Reset()
	require.Equal(t, 69, runCLI(context.Background(), []string{"daemon"}, &out, &stderr, Call, func(context.Context) error { return errors.New("private diagnostic") }))
	require.NotContains(t, stderr.String(), "private diagnostic")
}
