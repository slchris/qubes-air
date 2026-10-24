package desktop

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"time"
)

// Only Run constructs the production worker, with a fixed interpreter, script
// and session environment. Request data can add only an operation and app ID.
type worker struct {
	program string
	args    []string
	env     []string
	dir     string
}

func (w worker) run(ctx context.Context, request Request) Response {
	if validateRequest(request) != nil {
		return failure(64)
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	args := append(append([]string{}, w.args...), request.Operation)
	if request.Operation == operationLaunch {
		args = append(args, request.App)
	}
	// #nosec G204 -- the production program/script are fixed in Run; the only
	// request arguments are the allowlisted operation and validated app ID.
	cmd := exec.CommandContext(ctx, w.program, args...)
	cmd.Env = w.env
	cmd.Dir = w.dir
	cmd.WaitDelay = 200 * time.Millisecond
	output := &responseBuffer{cancel: cancel}
	cmd.Stdout = output
	// nil stdin/stderr become /dev/null, so children cannot inherit RPC I/O or
	// expose desktop diagnostics. The GIO launcher also replaces all child fds.
	err := cmd.Run()
	if ctx.Err() != nil || output.overflow {
		return failure(75)
	}
	if cmd.Process == nil {
		return failure(69)
	}
	var reply Response
	if decodeMessage(&output.buffer, MaxResponseBytes, &reply) != nil || validateResponse(request, reply) != nil {
		return failure(75)
	}
	if err != nil {
		var exit *exec.ExitError
		if reply.OK || !errors.As(err, &exit) || exit.ExitCode() != reply.Code {
			return failure(75)
		}
	} else if !reply.OK {
		return failure(75)
	}
	return reply
}

type responseBuffer struct {
	buffer   bytes.Buffer
	cancel   context.CancelFunc
	overflow bool
}

func (w *responseBuffer) Write(data []byte) (int, error) {
	if w.buffer.Len()+len(data) > MaxResponseBytes {
		w.overflow = true
		w.cancel()
		return 0, io.ErrShortBuffer
	}
	return w.buffer.Write(data)
}
