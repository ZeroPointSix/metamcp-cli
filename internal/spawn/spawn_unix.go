//go:build unix

package spawn

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/ZeroPointSix/metamcp-cli/internal/config"
)

// maxSocketPath is the sun_path limit on macOS (Linux allows 108).
const maxSocketPath = 104

// runtimeDir returns a private (0700, owned by us) directory for sockets,
// locks and logs: $MCLI_RUNTIME_DIR, else $XDG_RUNTIME_DIR/mcli, else
// /tmp/mcli-<uid>.
func runtimeDir() (string, error) {
	dir := os.Getenv(config.EnvRuntime)
	if dir == "" {
		if x := os.Getenv("XDG_RUNTIME_DIR"); x != "" {
			dir = filepath.Join(x, "mcli")
		} else {
			dir = shortDir()
		}
	}
	return dir, ensurePrivateDir(dir)
}

// shortDir is the fallback socket directory, short enough for any id.
func shortDir() string {
	return filepath.Join("/tmp", fmt.Sprintf("mcli-%d", os.Getuid()))
}

// ensurePrivateDir creates dir (0700) or checks that an existing one is a
// directory owned by this user, tightening its permissions if needed.
func ensurePrivateDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return err
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("runtime path %s is not a directory", dir)
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && int(sys.Uid) != os.Getuid() {
		// Someone else pre-created it: refuse rather than talk to their socket.
		return fmt.Errorf("runtime directory %s is owned by uid %d", dir, sys.Uid)
	}
	if st.Mode().Perm()&0o077 != 0 {
		return os.Chmod(dir, 0o700)
	}
	return nil
}

// endpointFor returns the socket path for id. When dir is too deep for a
// Unix socket path (common in sandboxes), the socket moves to /tmp/mcli-<uid>
// while locks and logs stay in dir.
func endpointFor(dir, id string) (string, error) {
	path := filepath.Join(dir, id+".sock")
	if len(path) < maxSocketPath {
		return path, nil
	}
	short := shortDir()
	if err := ensurePrivateDir(short); err != nil {
		return "", fmt.Errorf("socket path %s is too long and %s is unusable: %w", path, short, err)
	}
	return filepath.Join(short, id+".sock"), nil
}

func detachAttrs() *syscall.SysProcAttr {
	// A new session: no controlling terminal, and not killed with the caller's
	// process group.
	return &syscall.SysProcAttr{Setsid: true}
}
