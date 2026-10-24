package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"sync"
	"time"
)

const requestTimeout = 10 * time.Second

type peerReader func(*net.UnixConn) (uint32, error)

// Call sends exactly one request to the fixed broker. It never retries: a lost
// reply after dispatch cannot tell us whether an application was started.
func Call(ctx context.Context, request Request) Response {
	return call(ctx, SocketPath, peerUID, request, requestTimeout)
}

func call(ctx context.Context, path string, peer peerReader, request Request, timeout time.Duration) Response {
	if validateRequest(request) != nil {
		return failure(64)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return failure(69)
	}
	defer conn.Close()
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return failure(77)
	}
	uid, err := peer(unixConn)
	if err != nil || uid != DesktopUID {
		return failure(77)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return failure(69)
	}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return failure(75)
	}
	if err := unixConn.CloseWrite(); err != nil {
		return failure(75)
	}
	var reply Response
	if decodeMessage(conn, MaxResponseBytes, &reply) != nil || validateResponse(request, reply) != nil {
		return failure(75)
	}
	return reply
}

type server struct {
	peer       peerReader
	handle     func(context.Context, Request) Response
	timeout    time.Duration
	maxClients int
}

func (s server) serve(ctx context.Context, listener *net.UnixListener) error {
	stop := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stop()
	defer listener.Close()
	slots := make(chan struct{}, s.maxClients)
	var pending sync.WaitGroup
	defer pending.Wait()
	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		select {
		case slots <- struct{}{}:
			pending.Add(1)
			go func() {
				defer pending.Done()
				defer func() { <-slots }()
				s.accept(ctx, conn)
			}()
		default:
			// No goroutine or work queue is allocated for an excess client.
			_ = conn.Close()
		}
	}
}

func (s server) accept(ctx context.Context, conn *net.UnixConn) {
	defer conn.Close()
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if conn.SetDeadline(deadline) != nil {
		return
	}
	uid, err := s.peer(conn)
	if err != nil || (uid != 0 && uid != DesktopUID) {
		_ = json.NewEncoder(conn).Encode(failure(77))
		log.Printf("desktop broker: peer rejected")
		return
	}
	request, err := decodeRequest(conn)
	if err != nil {
		_ = json.NewEncoder(conn).Encode(failure(64))
		log.Printf("desktop broker: uid=%d invalid request", uid)
		return
	}
	started := time.Now()
	reply := s.handle(ctx, request)
	if validateResponse(request, reply) != nil {
		reply = failure(75)
	}
	reply, data := encodeResponse(reply)
	log.Printf("desktop broker: uid=%d operation=%s app=%q code=%d duration=%s",
		uid, request.Operation, request.App, reply.Code, time.Since(started).Round(time.Millisecond))
	_, _ = conn.Write(data)
}
