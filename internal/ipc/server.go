package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
)

// Handler resolves a Request to a Response.
//
// For long-lived requests (e.g. "subscribe"), the handler may return a
// Response with a non-nil Stream channel. The server will write each
// []byte from Stream as a newline-terminated frame until Stream closes
// or the client disconnects.
type Handler func(ctx context.Context, req Request) Response

// Serve listens on the given Unix socket path, removing any stale file first.
// It blocks until ctx is canceled.
func Serve(ctx context.Context, sockPath string, log *slog.Logger, h Handler) error {
	if err := os.MkdirAll(filepath.Dir(sockPath), 0o755); err != nil {
		return err
	}
	_ = os.Remove(sockPath)
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		return err
	}
	defer l.Close()
	defer os.Remove(sockPath)
	if err := os.Chmod(sockPath, 0o600); err != nil {
		log.Warn("chmod socket", "err", err)
	}

	go func() {
		<-ctx.Done()
		l.Close()
	}()

	for {
		c, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			log.Warn("accept", "err", err)
			continue
		}
		go handleConn(ctx, c, log, h)
	}
}

func handleConn(ctx context.Context, c net.Conn, log *slog.Logger, h Handler) {
	defer c.Close()
	br := bufio.NewReader(c)
	line, err := br.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return
	}
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		writeResp(c, Response{Error: "bad request: " + err.Error()})
		return
	}
	resp := h(ctx, req)
	writeResp(c, resp)
	if resp.Stream == nil {
		return
	}
	// Streaming mode: ship each frame as one line until the producer closes
	// or the client disconnects (detected via failed write).
	for {
		select {
		case <-ctx.Done():
			return
		case b, ok := <-resp.Stream:
			if !ok {
				return
			}
			b = append(b, '\n')
			if _, err := c.Write(b); err != nil {
				return
			}
		}
	}
}

func writeResp(c net.Conn, r Response) {
	b, _ := json.Marshal(r)
	b = append(b, '\n')
	_, _ = c.Write(b)
}
