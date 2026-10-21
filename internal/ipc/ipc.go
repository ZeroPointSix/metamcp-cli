// Package ipc is the local CLI <-> daemon channel: one newline-delimited JSON
// request and one response per connection, over a Unix socket or a Windows
// named pipe. It never listens on TCP.
package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/ZeroPointSix/metamcp-cli/internal/output"
)

// ProtocolVersion is bumped on any incompatible change to Request/Response.
const ProtocolVersion = 1

// Operations besides ops.OpSearch / ops.OpExecute.
const (
	OpHello    = "hello"
	OpStatus   = "status"
	OpShutdown = "shutdown"
)

// MaxMessageBytes bounds one request or response line. Code is at most 64 KiB
// (JSON escaping can grow it); results are at most 1 MiB server-side.
const MaxMessageBytes = 8 << 20

// Request is one CLI -> daemon message.
type Request struct {
	V         int             `json:"v"`
	Op        string          `json:"op"`
	Args      json.RawMessage `json:"args,omitempty"`
	TimeoutMS int             `json:"timeout_ms,omitempty"`
}

// Response is the daemon's reply. For search/execute, Result holds an
// output.Result (whose own ok may be false); Error is set when the daemon
// itself could not handle the request.
type Response struct {
	V      int             `json:"v"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *output.Error   `json:"error,omitempty"`
}

// Hello identifies a running daemon.
type Hello struct {
	V         int       `json:"v"`
	Build     string    `json:"build"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

// ErrUnavailable means no daemon accepted the connection; the request was not
// delivered, so falling back to direct mode is safe.
var ErrUnavailable = errors.New("daemon unavailable")

// ErrBroken means the request may have been delivered but no response
// arrived. The outcome is unknown.
var ErrBroken = errors.New("daemon connection lost")

// Call sends req to the daemon at endpoint and waits for the response.
func Call(ctx context.Context, endpoint string, req *Request) (*Response, error) {
	conn, err := Dial(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	req.V = ProtocolVersion
	if err := WriteMessage(conn, req); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	var resp Response
	if err := ReadMessage(bufio.NewReader(conn), &resp); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrBroken, err)
	}
	return &resp, nil
}

// WriteMessage writes v as one JSON line.
func WriteMessage(w io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(data, '\n'))
	return err
}

// ReadMessage reads one JSON line into v.
func ReadMessage(r *bufio.Reader, v any) error {
	var line []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			return err
		}
		line = append(line, chunk...)
		if len(line) > MaxMessageBytes {
			return errors.New("message too large")
		}
		if !isPrefix {
			break
		}
	}
	return json.Unmarshal(line, v)
}

// Listener accepts daemon connections from the current user only.
type Listener interface {
	Accept() (net.Conn, error)
	// Close stops accepting and removes the endpoint so that a new daemon can
	// bind it; established connections are unaffected.
	Close() error
}
