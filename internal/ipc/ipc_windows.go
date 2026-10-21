//go:build windows

package ipc

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// Dial connects to the daemon's named pipe.
func Dial(ctx context.Context, name string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, name)
}

type pipeListener struct{ net.Listener }

// Listen creates a named pipe that only the current user can open. Access
// control is enforced by the pipe's security descriptor, so no per-connection
// peer check is needed.
func Listen(name string) (Listener, error) {
	sid, err := CurrentUserSID()
	if err != nil {
		return nil, err
	}
	ln, err := winio.ListenPipe(name, &winio.PipeConfig{
		// Protected DACL: generic-all for the current user, nobody else.
		SecurityDescriptor: fmt.Sprintf("D:P(A;;GA;;;%s)", sid),
		MessageMode:        false,
		InputBufferSize:    64 << 10,
		OutputBufferSize:   64 << 10,
	})
	if err != nil {
		return nil, err
	}
	return &pipeListener{ln}, nil
}

func (l *pipeListener) Accept() (net.Conn, error) { return l.Listener.Accept() }

// IsPeerRejected is always false on Windows.
func IsPeerRejected(err error) bool { return false }

// CurrentUserSID returns the SID of the user running this process.
func CurrentUserSID() (string, error) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", err
	}
	if user.User.Sid == nil {
		return "", errors.New("no user SID")
	}
	return user.User.Sid.String(), nil
}
