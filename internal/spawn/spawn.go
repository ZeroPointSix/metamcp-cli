// Package spawn finds or starts the background daemon for an endpoint.
//
// Each (endpoint URL, API key) pair gets its own daemon, identified by a hash
// of both, so changing either naturally selects a new daemon while the old one
// idles out. Credentials reach the daemon through a stdin pipe, never through
// argv (visible in /proc) or the environment.
package spawn

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"

	"github.com/ZeroPointSix/metamcp-cli/internal/config"
	"github.com/ZeroPointSix/metamcp-cli/internal/ipc"
	"github.com/ZeroPointSix/metamcp-cli/internal/version"
)

const (
	readyTimeout = 1500 * time.Millisecond
	pollInterval = 50 * time.Millisecond
	lockTimeout  = 5 * time.Second
	helloTimeout = time.Second
	// stopTimeout covers the daemon draining in-flight calls (5s) and the
	// session DELETE.
	stopTimeout = 15 * time.Second
	maxLogBytes = 1 << 20
)

// Target locates one daemon's endpoint and side files.
type Target struct {
	ID       string
	Endpoint string // Unix socket path or Windows pipe name
	LockPath string // held while a CLI starts the daemon
	RunPath  string // held by the daemon for its whole lifetime
	LogPath  string
}

// Credentials are handed to a new daemon over stdin.
type Credentials struct {
	URL    string `json:"url"`
	APIKey string `json:"api_key"`
}

// ID derives the daemon identity from the endpoint URL and API key.
func ID(url, apiKey string) string {
	sum := sha256.Sum256([]byte(url + "\n" + apiKey))
	return hex.EncodeToString(sum[:])[:12]
}

// Locate returns the target for an id, creating the runtime directory.
func Locate(id string) (*Target, error) {
	dir, err := runtimeDir()
	if err != nil {
		return nil, err
	}
	endpoint, err := endpointFor(dir, id)
	if err != nil {
		return nil, err
	}
	return &Target{
		ID:       id,
		Endpoint: endpoint,
		LockPath: filepath.Join(dir, id+".lock"),
		RunPath:  filepath.Join(dir, id+".run"),
		LogPath:  filepath.Join(dir, id+".log"),
	}, nil
}

// Hello asks a running daemon to identify itself.
func Hello(ctx context.Context, t *Target) (*ipc.Hello, error) {
	ctx, cancel := context.WithTimeout(ctx, helloTimeout)
	defer cancel()
	resp, err := ipc.Call(ctx, t.Endpoint, &ipc.Request{Op: ipc.OpHello})
	if err != nil {
		return nil, err
	}
	var h ipc.Hello
	if err := json.Unmarshal(resp.Result, &h); err != nil {
		return nil, fmt.Errorf("%w: bad hello: %v", ipc.ErrUnavailable, err)
	}
	return &h, nil
}

func compatible(h *ipc.Hello) bool {
	return h.V == ipc.ProtocolVersion && h.Build == version.Build()
}

// Stop asks the daemon to shut down and waits until the process has exited,
// which is after it has closed (DELETEd) its MCP session. The daemon holds the
// run lock for its lifetime; the OS releases it when the process exits.
func Stop(ctx context.Context, t *Target) (bool, error) {
	cctx, cancel := context.WithTimeout(ctx, helloTimeout)
	_, err := ipc.Call(cctx, t.Endpoint, &ipc.Request{Op: ipc.OpShutdown})
	cancel()
	if errors.Is(err, ipc.ErrUnavailable) {
		return false, nil
	}
	run := flock.New(t.RunPath)
	wctx, cancel := context.WithTimeout(ctx, stopTimeout)
	defer cancel()
	locked, err := run.TryLockContext(wctx, pollInterval)
	if err != nil || !locked {
		return true, fmt.Errorf("daemon did not exit within %v (see %s)", stopTimeout, t.LogPath)
	}
	return true, run.Unlock()
}

// Ensure makes sure a daemon built from this exact binary serves t. It returns
// an error wrapping ipc.ErrUnavailable when no daemon could be made ready, in
// which case the caller falls back to direct mode.
func Ensure(ctx context.Context, t *Target, creds Credentials) error {
	if h, err := Hello(ctx, t); err == nil {
		if compatible(h) {
			return nil
		}
		// An older (or newer) build: replace it.
		if _, err := Stop(ctx, t); err != nil {
			return fmt.Errorf("%w: replace daemon: %v", ipc.ErrUnavailable, err)
		}
	}

	lock := flock.New(t.LockPath)
	lctx, cancel := context.WithTimeout(ctx, lockTimeout)
	defer cancel()
	locked, err := lock.TryLockContext(lctx, pollInterval)
	if err != nil || !locked {
		return fmt.Errorf("%w: acquire start lock: %v", ipc.ErrUnavailable, err)
	}
	defer lock.Unlock()

	// Another CLI may have started it while we waited for the lock.
	if h, err := Hello(ctx, t); err == nil && compatible(h) {
		return nil
	}
	if err := start(t, creds); err != nil {
		return fmt.Errorf("%w: start daemon: %v", ipc.ErrUnavailable, err)
	}
	deadline := time.Now().Add(readyTimeout)
	for {
		h, err := Hello(ctx, t)
		if err == nil && compatible(h) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: daemon not ready after %v (see %s)", ipc.ErrUnavailable, readyTimeout, t.LogPath)
		}
		time.Sleep(pollInterval)
	}
}

func start(t *Target, creds Credentials) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	rotateLog(t.LogPath)
	logFile, err := os.OpenFile(t.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()

	cmd := exec.Command(exe, "__daemon", "--id", t.ID)
	cmd.Dir = filepath.Dir(t.LogPath) // do not pin the caller's working directory
	cmd.Stdout = logFile              // never inherit the caller's stdout/stderr:
	cmd.Stderr = logFile              // an agent waiting for EOF would hang
	cmd.Env = daemonEnv()
	cmd.SysProcAttr = detachAttrs()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	werr := json.NewEncoder(stdin).Encode(creds)
	cerr := stdin.Close()
	_ = cmd.Process.Release()
	return errors.Join(werr, cerr)
}

// daemonEnv drops credentials from the inherited environment.
func daemonEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, config.EnvAPIKey+"=") {
			continue
		}
		env = append(env, kv)
	}
	return env
}

func rotateLog(path string) {
	if st, err := os.Stat(path); err == nil && st.Size() > maxLogBytes {
		_ = os.Rename(path, path+".1")
	}
}
