package host

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"sync"

	"github.com/trick77/flow/internal/event"
	"github.com/trick77/flow/internal/hostproto"
)

// Serve answers hostproto on l until ctx ends: requests and responses as JSON lines, a subscribe turning the
// connection into a frame stream.
func (h *Host) Serve(ctx context.Context, l net.Listener) error {
	var wg sync.WaitGroup
	defer wg.Wait()
	go func() {
		<-ctx.Done()
		_ = l.Close()
	}()
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.serveConn(ctx, conn)
		}()
	}
}

func (h *Host) serveConn(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64<<10), event.MaxLine)
	enc := json.NewEncoder(conn)
	for sc.Scan() {
		var req hostproto.Request
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			_ = enc.Encode(hostproto.Response{Error: hostproto.CodeInvalid})
			return
		}
		resp := hostproto.Response{ID: req.ID}
		switch req.Op {
		case hostproto.OpHello:
			hello := h.Hello(os.Getpid())
			resp.Hello = &hello
		case hostproto.OpCommand:
			if req.Command == nil {
				resp.Error = hostproto.CodeInvalid
				break
			}
			replied := h.beginReply() // before the command: a host.shutdown waits for this reply
			dup, err := h.Command(*req.Command)
			resp.Duplicate = dup
			var ce *CommandError
			if errors.As(err, &ce) {
				resp.Error = ce.Code
			}
			if req.Command.Kind == event.CmdMessageSend && resp.Error == "" {
				var m event.MessageSend
				if json.Unmarshal(req.Command.Data, &m) == nil {
					resp.Queued = h.Queued(m.MessageID)
				}
			}
			werr := enc.Encode(resp)
			replied()
			if werr != nil {
				return
			}
			continue
		case hostproto.OpSubscribe:
			if enc.Encode(resp) != nil {
				return
			}
			h.stream(ctx, conn, enc)
			return
		default:
			resp.Error = hostproto.CodeInvalid
		}
		if enc.Encode(resp) != nil {
			return
		}
	}
}

// stream sends frames until the subscription or the connection ends.
func (h *Host) stream(ctx context.Context, conn net.Conn, enc *json.Encoder) {
	frames, cancel := h.Subscribe()
	defer cancel()
	gone := make(chan struct{})
	go func() { // a subscriber sends nothing more; a read returning means it left
		_, _ = conn.Read(make([]byte, 1))
		close(gone)
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-gone:
			return
		case f, ok := <-frames:
			if !ok || enc.Encode(f) != nil {
				return
			}
		}
	}
}
