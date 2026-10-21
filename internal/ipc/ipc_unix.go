//go:build unix

package ipc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
)

// Dial connects to the daemon socket at path.
func Dial(ctx context.Context, path string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", path)
}

type unixListener struct {
	ln   *net.UnixListener
	path string
}

// Listen binds a Unix socket at path with mode 0600. A stale socket file left
// by a dead daemon is replaced; a live one is an error.
func Listen(path string) (Listener, error) {
	if conn, err := net.Dial("unix", path); err == nil {
		conn.Close()
		return nil, fmt.Errorf("a daemon is already listening on %s", path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	// Create the socket with no group/other permissions from the start.
	old := syscall.Umask(0o177)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	syscall.Umask(old)
	if err != nil {
		return nil, err
	}
	// Close must not unlink by path: a replacement daemon may already own it.
	ln.SetUnlinkOnClose(false)
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return &unixListener{ln: ln, path: path}, nil
}

func (l *unixListener) Accept() (net.Conn, error) {
	conn, err := l.ln.AcceptUnix()
	if err != nil {
		return nil, err
	}
	if err := checkPeer(conn); err != nil {
		conn.Close()
		return nil, &peerError{err}
	}
	return conn, nil
}

// Close unlinks the socket first, then closes the listener, so a CLI that sees
// the socket disappear can immediately start a replacement daemon.
func (l *unixListener) Close() error {
	_ = os.Remove(l.path)
	return l.ln.Close()
}

// peerError marks a rejected connection; Accept callers should continue.
type peerError struct{ err error }

func (e *peerError) Error() string { return "rejected peer: " + e.err.Error() }

// IsPeerRejected reports whether an Accept error only rejected one client.
func IsPeerRejected(err error) bool {
	var pe *peerError
	return errors.As(err, &pe)
}
