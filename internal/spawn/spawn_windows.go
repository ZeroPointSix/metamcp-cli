//go:build windows

package spawn

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"syscall"

	"github.com/ZeroPointSix/metamcp-cli/internal/config"
	"github.com/ZeroPointSix/metamcp-cli/internal/ipc"
)

const (
	detachedProcess       = 0x00000008
	createNewProcessGroup = 0x00000200
	createNoWindow        = 0x08000000
)

// runtimeDir holds locks and logs under the per-user cache directory, which
// Windows ACLs already restrict to the user.
func runtimeDir() (string, error) {
	dir := os.Getenv(config.EnvRuntime)
	if dir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(base, "mcli")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// endpointFor names a per-user pipe: \\.\pipe\mcli-<user SID hash>-<id>.
func endpointFor(_ string, id string) (string, error) {
	sid, err := ipc.CurrentUserSID()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(sid))
	return `\\.\pipe\mcli-` + hex.EncodeToString(sum[:])[:8] + "-" + id, nil
}

func detachAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: detachedProcess | createNewProcessGroup | createNoWindow,
		HideWindow:    true,
	}
}
