package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestLive runs read-only smoke checks against a real MetaMCP endpoint. It is
// skipped unless MCLI_LIVE_URL and MCLI_LIVE_API_KEY are set:
//
//	MCLI_LIVE_URL=https://host/metamcp/<endpoint>/mcp MCLI_LIVE_API_KEY=... \
//	  go test -run TestLive -v ./test/e2e/
//
// Scripts only search, list catalogs and compute locally; no upstream tool
// with side effects is called (doctor runs without --probe).
func TestLive(t *testing.T) {
	url, key := os.Getenv("MCLI_LIVE_URL"), os.Getenv("MCLI_LIVE_API_KEY")
	if url == "" || key == "" {
		t.Skip("set MCLI_LIVE_URL and MCLI_LIVE_API_KEY to run")
	}
	rt, err := os.MkdirTemp("", "mlive")
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, bin: binary, vars: []string{
		"MCLI_CONFIG=" + filepath.Join(t.TempDir(), "config.json"),
		"MCLI_RUNTIME_DIR=" + rt,
		"MCLI_URL=", "MCLI_API_KEY=", "MCLI_NO_DAEMON=",
	}}
	t.Cleanup(func() {
		e.run("", "daemon", "stop")
		os.RemoveAll(rt)
	})

	t.Run("config verifies endpoint", func(t *testing.T) {
		e.t = t
		e.mustOK("", "config", "set", "--url", url, "--no-verify")
		if r := e.run("mcli-live-wrong-key", "config", "set", "--api-key-stdin"); r.exit != 3 {
			t.Errorf("wrong key: exit %d %s", r.exit, r.stdout)
		}
		data := e.mustOK(key, "config", "set", "--api-key-stdin").json["data"].(map[string]any)
		if data["verified"] != true {
			t.Fatalf("not verified: %v", data)
		}
	})

	t.Run("search", func(t *testing.T) {
		e.t = t
		data := e.mustOK("", "search", "github", "--limit", "2").json["data"].(map[string]any)
		if tools, _ := data["tools"].([]any); len(tools) == 0 {
			t.Errorf("no tools: %v", data)
		}
	})

	t.Run("execute typescript with console", func(t *testing.T) {
		e.t = t
		r := e.mustOK("type N = number;\nconst xs: N[] = [1, 2, 3];\nconsole.log(\"sum\");\nreturn xs.reduce((a, b) => a + b, 0);", "execute", "-")
		if r.json["data"] != float64(6) || r.json["console"] != "sum" {
			t.Errorf("got %s", r.stdout)
		}
	})

	t.Run("catalog read", func(t *testing.T) {
		e.t = t
		r := e.mustOK("const l: any = await tools.list({ category: \"github\" });\nreturn Array.isArray(l) ? l.length : -1;", "execute", "-")
		if n, _ := r.json["data"].(float64); n <= 0 {
			t.Errorf("tools.list returned %v", r.json["data"])
		}
	})

	t.Run("doctor", func(t *testing.T) {
		e.t = t
		data := e.mustOK("", "doctor", "--expect", "github").json["data"].(map[string]any)
		srv := data["server"].(map[string]any)
		if srv["version"] == "" || data["tool_count"].(float64) <= 0 {
			t.Errorf("doctor = %v", data)
		}
		t.Logf("server version=%v capabilities=%v", srv["version"], data["capabilities"])
		r := e.run("", "doctor", "--expect", "mcli-live-no-such-server")
		if r.exit != 1 || errCode(r) != "E_DOCTOR" {
			t.Errorf("missing server: exit %d %s", r.exit, r.stdout)
		}
	})

	t.Run("tools and schema", func(t *testing.T) {
		e.t = t
		data := e.mustOK("", "tools", "--category", "github").json["data"].(map[string]any)
		list, _ := data["tools"].([]any)
		if len(list) == 0 {
			t.Fatalf("no github tools: %v", data)
		}
		name := list[0].(map[string]any)["name"].(string)
		s := e.mustOK("", "schema", name).json["data"].(map[string]any)["tools"].([]any)
		if len(s) != 1 || s[0].(map[string]any)["inputSchema"] == nil {
			t.Errorf("schema %s = %v", name, s)
		}
	})

	t.Run("unknown tool name gets suggestions", func(t *testing.T) {
		e.t = t
		// github__get_me exists; the hyphenated spelling does not. Nothing runs.
		r := e.run("return await tools.github[\"get-me\"]({});", "execute", "-")
		if r.exit != 1 || errCode(r) != "E_TOOL_NOT_FOUND" {
			t.Fatalf("exit %d %s", r.exit, r.stdout)
		}
		if s := r.json["error"].(map[string]any)["suggestions"]; fmt.Sprint(s) != "[github__get_me]" {
			t.Errorf("suggestions = %v", s)
		}
	})

	t.Run("error line maps to source", func(t *testing.T) {
		e.t = t
		r := e.run("type T = { a: number };\n\nconst v: T = { a: 1 };\nthrow new Error(\"live line 4\");", "execute", "-")
		if r.exit != 1 || errCode(r) != "E_RUNTIME" {
			t.Fatalf("exit %d %s", r.exit, r.stdout)
		}
		if line := r.json["error"].(map[string]any)["line"]; line != float64(4) {
			t.Errorf("line = %v, want 4", line)
		}
	})

	t.Run("server timeout is exit 4", func(t *testing.T) {
		e.t = t
		r := e.run("const s = Date.now();\nwhile (Date.now() - s < 3000) {}\nreturn 1;", "execute", "-", "--timeout", "1s")
		if r.exit != 4 || errCode(r) != "E_TIMEOUT" {
			t.Errorf("exit %d %s", r.exit, r.stdout)
		}
	})

	t.Run("module syntax rejected locally", func(t *testing.T) {
		e.t = t
		if r := e.run("import x from \"y\";\nreturn x;", "execute", "-"); r.exit != 2 || errCode(r) != "E_SCRIPT_MODULE" {
			t.Errorf("exit %d %s", r.exit, r.stdout)
		}
	})

	t.Run("daemon reuses one session under concurrency", func(t *testing.T) {
		e.t = t
		var wg sync.WaitGroup
		var mu sync.Mutex
		var failures []string
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := e.run("return 1;", "execute", "-")
				if r.exit != 0 {
					mu.Lock()
					failures = append(failures, r.stdout)
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if len(failures) > 0 {
			t.Fatalf("concurrent executes failed:\n%s", strings.Join(failures, "\n"))
		}
		st := e.mustOK("", "daemon", "status").json["data"].(map[string]any)
		sess := st["session"].(map[string]any)
		if st["running"] != true || sess["connected"] != true || sess["rebuilds"] != float64(0) {
			t.Errorf("status = %v", st)
		}
		t.Logf("protocol=%v served=%v", sess["protocol_version"], st["served"])
	})
}
