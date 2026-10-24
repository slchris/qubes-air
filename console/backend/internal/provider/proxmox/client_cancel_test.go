package proxmox

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type responseTransport func(*http.Request) (*http.Response, error)

func (f responseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Finish the context before returning a clean EOF, reproducing a response that
// completes while the HTTP transport is still observing request cancellation.
type contextEndingBody struct {
	io.Reader
	finish func()
	closed bool
}

func (b *contextEndingBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if errors.Is(err, io.EOF) {
		b.finish()
	}
	return n, err
}

func (b *contextEndingBody) Close() error {
	b.closed = true
	return nil
}

func TestRequestRawPreservesContextAtResponseEOF(t *testing.T) {
	for _, mode := range []string{"canceled", "deadline"} {
		for _, payload := range []string{"", `{"data":`, `{"data":[]}`} {
			t.Run(mode+"/"+payload, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				finish := cancel
				want := context.Canceled
				if mode == "deadline" {
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
					finish = func() { <-ctx.Done() }
					want = context.DeadlineExceeded
				}
				defer cancel()
				body := &contextEndingBody{Reader: strings.NewReader(payload), finish: finish}
				client := &Client{
					endpoint: "https://provider.example.invalid", token: "test-token",
					http: &http.Client{Transport: responseTransport(func(r *http.Request) (*http.Response, error) {
						return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: body, Request: r}, nil
					})},
				}
				raw, err := client.requestRaw(ctx, http.MethodGet, "/api2/json/nodes/node-a/network", nil)
				require.ErrorIs(t, err, want)
				require.Nil(t, raw, "canceled responses must not reach JSON decoding or network eligibility checks")
				require.True(t, body.closed)
			})
		}
	}
}
