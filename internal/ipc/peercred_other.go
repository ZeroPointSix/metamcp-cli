//go:build unix && !linux && !darwin

package ipc

import "net"

// checkPeer relies on the 0700 runtime directory and 0600 socket on platforms
// without a supported peer-credential API.
func checkPeer(*net.UnixConn) error { return nil }
