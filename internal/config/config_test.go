package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ZeroPointSix/metamcp-cli/internal/output"
)

func setup(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sub", "config.json")
	t.Setenv(EnvConfig, path)
	t.Setenv(EnvURL, "")
	t.Setenv(EnvAPIKey, "")
	return path
}

func TestSaveLoadPermissions(t *testing.T) {
	path := setup(t)
	if _, err := Save(&File{URL: "https://h/metamcp/e/mcp", APIKey: "sk_secret_1234"}); err != nil {
		t.Fatal(err)
	}
	f, err := Load()
	if err != nil || f.URL != "https://h/metamcp/e/mcp" || f.APIKey != "sk_secret_1234" {
		t.Fatalf("load: %+v %v", f, err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v, want 0600", st.Mode().Perm())
	}
	dst, _ := os.Stat(filepath.Dir(path))
	if dst.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v, want 0700", dst.Mode().Perm())
	}
}

func TestResolvePrecedence(t *testing.T) {
	setup(t)
	if _, err := Save(&File{URL: "https://file/metamcp/e/mcp", APIKey: "file-key"}); err != nil {
		t.Fatal(err)
	}
	r, _ := Resolve("")
	if r.URL != "https://file/metamcp/e/mcp" || r.URLSource != "file" || r.APIKey != "file-key" || r.KeySource != "file" {
		t.Fatalf("file: %+v", r)
	}
	t.Setenv(EnvURL, "https://env/metamcp/e/mcp")
	t.Setenv(EnvAPIKey, "env-key")
	r, _ = Resolve("")
	if r.URLSource != "env" || r.APIKey != "env-key" || r.KeySource != "env" {
		t.Fatalf("env: %+v", r)
	}
	r, _ = Resolve("https://flag/metamcp/e/mcp")
	if r.URL != "https://flag/metamcp/e/mcp" || r.URLSource != "flag" {
		t.Fatalf("flag: %+v", r)
	}
}

func TestRequire(t *testing.T) {
	setup(t)
	r, _ := Resolve("")
	var e *output.Error
	if err := r.Require(); !errors.As(err, &e) || e.Code != output.CodeConfig || e.Exit != output.ExitUsage {
		t.Fatalf("missing config: %v", err)
	}
	r = &Resolved{URL: "ftp://x", APIKey: "k"}
	if err := r.Require(); !errors.As(err, &e) || e.Code != output.CodeConfig {
		t.Fatalf("bad URL: %v", err)
	}
}

func TestMaskKey(t *testing.T) {
	if MaskKey("sk_abcdef1234") != "****1234" || MaskKey("abc") != "****" || MaskKey("") != "" {
		t.Fatal("unexpected masking")
	}
}
