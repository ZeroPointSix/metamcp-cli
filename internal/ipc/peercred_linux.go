//go:build linux

package ipc

import (
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// checkPeer rejects connections from other users (SO_PEERCRED).
func checkPeer(conn *net.UnixConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return err
	}
	if credErr != nil {
		return credErr
	}
	if int(cred.Uid) != os.Getuid() {
		return fmt.Errorf("peer uid %d != %d", cred.Uid, os.Getuid())
	}
	return nil
}
