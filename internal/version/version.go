// Package version holds build identity shared by the CLI and its daemon.
package version

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"runtime"
	"sync"
)

// Version is set at release time with -ldflags "-X .../version.Version=v1.2.3".
var Version = "dev"

// UserAgent is sent on every HTTP request to the MetaMCP endpoint.
func UserAgent() string {
	return fmt.Sprintf("mcli/%s (%s/%s)", Version, runtime.GOOS, runtime.GOARCH)
}

var (
	buildOnce sync.Once
	buildID   string
)

// Build identifies this exact binary. A CLI only reuses a daemon whose Build
// matches, so upgrading (or rebuilding a dev binary) replaces the old daemon.
// It combines Version with the executable's size and mtime, which is cheap to
// compute and changes whenever the binary is replaced.
func Build() string {
	buildOnce.Do(func() {
		buildID = Version
		exe, err := os.Executable()
		if err != nil {
			return
		}
		st, err := os.Stat(exe)
		if err != nil {
			return
		}
		sum := sha256.Sum256(fmt.Appendf(nil, "%s|%d|%d", exe, st.Size(), st.ModTime().UnixNano()))
		buildID = Version + "+" + hex.EncodeToString(sum[:])[:8]
	})
	return buildID
}
