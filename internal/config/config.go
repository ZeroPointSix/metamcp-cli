// Package config stores the endpoint URL and API key and resolves the
// effective values with precedence flag > environment > config file.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/ZeroPointSix/metamcp-cli/internal/output"
)

// Environment variables recognised by mcli.
const (
	EnvURL      = "MCLI_URL"
	EnvAPIKey   = "MCLI_API_KEY"
	EnvConfig   = "MCLI_CONFIG" // overrides the config file path
	EnvNoDaemon = "MCLI_NO_DAEMON"
	EnvIdle     = "MCLI_DAEMON_IDLE"
	EnvRuntime  = "MCLI_RUNTIME_DIR"
)

// File is the on-disk configuration (0600).
type File struct {
	URL    string `json:"url,omitempty"`
	APIKey string `json:"api_key,omitempty"`
}

// Resolved is the effective configuration for one invocation.
type Resolved struct {
	URL       string
	APIKey    string
	URLSource string // "flag", "env", "file" or ""
	KeySource string
}

// Path returns the config file location.
func Path() (string, error) {
	if p := os.Getenv(EnvConfig); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate config directory: %w", err)
	}
	return filepath.Join(dir, "mcli", "config.json"), nil
}

// Load reads the config file. A missing file is an empty config.
func Load() (*File, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &File{}, nil
	}
	if err != nil {
		return nil, output.Newf(output.CodeConfig, "read %s: %v", path, err)
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, output.Newf(output.CodeConfig, "parse %s: %v", path, err)
	}
	return &f, nil
}

// Save writes the config atomically (temp file + rename) with owner-only
// permissions on both the directory and the file.
func Save(f *File) (string, error) {
	path, err := Path()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	_ = os.Chmod(dir, 0o700)
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return "", fmt.Errorf("write config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return "", fmt.Errorf("write config: %w", err)
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return "", fmt.Errorf("write config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", fmt.Errorf("write config: %w", err)
	}
	return path, nil
}

// Resolve merges flag, environment and file values.
func Resolve(flagURL string) (*Resolved, error) {
	f, err := Load()
	if err != nil {
		return nil, err
	}
	r := &Resolved{}
	switch {
	case flagURL != "":
		r.URL, r.URLSource = flagURL, "flag"
	case os.Getenv(EnvURL) != "":
		r.URL, r.URLSource = os.Getenv(EnvURL), "env"
	case f.URL != "":
		r.URL, r.URLSource = f.URL, "file"
	}
	switch {
	case os.Getenv(EnvAPIKey) != "":
		r.APIKey, r.KeySource = os.Getenv(EnvAPIKey), "env"
	case f.APIKey != "":
		r.APIKey, r.KeySource = f.APIKey, "file"
	}
	return r, nil
}

// Require fails with E_CONFIG unless both URL and API key are present and the
// URL is well formed.
func (r *Resolved) Require() error {
	var missing []string
	if r.URL == "" {
		missing = append(missing, "endpoint URL")
	}
	if r.APIKey == "" {
		missing = append(missing, "API key")
	}
	if len(missing) > 0 {
		return output.Newf(output.CodeConfig, "missing %s", strings.Join(missing, " and "))
	}
	return ValidateURL(r.URL)
}

// ValidateURL checks that s is an absolute http(s) URL.
func ValidateURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return output.Newf(output.CodeConfig, "invalid endpoint URL %q: expected https://host/metamcp/<endpoint>/mcp", s)
	}
	return nil
}

// MaskKey shows only the last four characters of a key.
func MaskKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 4 {
		return "****"
	}
	return "****" + key[len(key)-4:]
}
