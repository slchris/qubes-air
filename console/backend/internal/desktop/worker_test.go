package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWorkerProcess(t *testing.T) {
	mode := os.Getenv("DESKTOP_TEST_WORKER")
	if mode == "" {
		return
	}
	switch mode {
	case "ok":
		_ = json.NewEncoder(os.Stdout).Encode(Response{OK: true, App: "editor", Display: ":100"})
	case "failure":
		_ = json.NewEncoder(os.Stdout).Encode(failure(69))
		os.Exit(69)
	case "cwd":
		cwd, err := os.Getwd()
		if err != nil || cwd != os.Getenv("DESKTOP_TEST_DIR") {
			_ = json.NewEncoder(os.Stdout).Encode(failure(69))
			os.Exit(69)
		}
		_ = json.NewEncoder(os.Stdout).Encode(Response{OK: true, App: "editor", Display: ":100"})
	case "mismatch":
		_ = json.NewEncoder(os.Stdout).Encode(Response{OK: true, App: "editor", Display: ":100"})
		os.Exit(70)
	case "noise":
		fmt.Print(strings.Repeat("x", MaxResponseBytes+1))
	case "timeout":
		time.Sleep(5 * time.Second)
	case "stdin":
		data := make([]byte, 1)
		if n, _ := os.Stdin.Read(data); n != 0 {
			os.Exit(1)
		}
		_ = json.NewEncoder(os.Stdout).Encode(Response{OK: true, App: "editor", Display: ":100"})
	default:
		fmt.Print("not a reply")
	}
	os.Exit(0)
}

func TestWorkerBoundaries(t *testing.T) {
	program, err := os.Executable()
	require.NoError(t, err)
	for _, tc := range []struct {
		mode string
		code int
	}{
		{"ok", 0}, {"failure", 69}, {"mismatch", 75}, {"noise", 75}, {"bad", 75}, {"stdin", 0},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			w := worker{program: program, args: []string{"-test.run=^TestWorkerProcess$", "--"}, env: []string{"DESKTOP_TEST_WORKER=" + tc.mode}}
			reply := w.run(context.Background(), Request{Operation: "launch", App: "editor"})
			require.Equal(t, tc.code, reply.Code, "%+v", reply)
		})
	}
}

func TestWorkerDeadlineAndInvalidRequest(t *testing.T) {
	program, err := os.Executable()
	require.NoError(t, err)
	w := worker{program: program, args: []string{"-test.run=^TestWorkerProcess$", "--"}, env: []string{"DESKTOP_TEST_WORKER=timeout"}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	require.Equal(t, 75, w.run(ctx, Request{Operation: "launch", App: "editor"}).Code)
	require.Less(t, time.Since(start), time.Second)
	require.Equal(t, 64, w.run(context.Background(), Request{}).Code)
	w.program = "/nonexistent/worker"
	require.Equal(t, 69, w.run(context.Background(), Request{Operation: "menus"}).Code)
}

func TestWorkerHomeIsRequiredWithoutDirectoryFallback(t *testing.T) {
	program, err := os.Executable()
	require.NoError(t, err)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	w := worker{program: program, args: []string{"-test.run=^TestWorkerProcess$", "--"}, dir: dir,
		env: []string{"DESKTOP_TEST_WORKER=cwd", "DESKTOP_TEST_DIR=" + dir}}
	require.True(t, w.run(t.Context(), Request{Operation: "launch", App: "editor"}).OK)
	w.dir = filepath.Join(dir, "unavailable")
	require.Equal(t, 69, w.run(t.Context(), Request{Operation: "launch", App: "editor"}).Code)
}
