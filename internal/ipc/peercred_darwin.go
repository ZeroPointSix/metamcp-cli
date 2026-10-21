//go:build darwin

package ipc

import (
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// checkPeer rejects connections from other users (LOCAL_PEERCRED).
func checkPeer(conn *net.UnixConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var cred *unix.Xucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
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
