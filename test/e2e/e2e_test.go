// Package e2e drives the real mcli binary (and the daemon it spawns) against
// the fake MetaMCP server.
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ZeroPointSix/metamcp-cli/test/fakeserver"
)

var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mcli-bin")
	if err != nil {
		panic(err)
	}
	binary = filepath.Join(dir, "mcli")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, "../../cmd/mcli")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type env struct {
	t    *testing.T
	srv  *fakeserver.Server
	vars []string
	bin  string
}

func newEnv(t *testing.T, extra ...string) *env {
	t.Helper()
	srv := fakeserver.New("sk_test_key_9876")
	// Keep the runtime dir short: Unix socket paths are limited to ~104 bytes.
	rt, err := os.MkdirTemp("", "mrt")
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, srv: srv, bin: binary, vars: append([]string{
		"MCLI_CONFIG=" + filepath.Join(t.TempDir(), "config.json"),
		"MCLI_RUNTIME_DIR=" + rt,
		"MCLI_URL=" + srv.URL(),
		"MCLI_API_KEY=sk_test_key_9876",
		"MCLI_NO_DAEMON=",
	}, extra...)}
	t.Cleanup(func() {
		e.run("", "daemon", "stop")
		srv.Close()
		os.RemoveAll(rt)
	})
	return e
}

type result struct {
	exit   int
	stdout string
	stderr string
	json   map[string]any
}

func (e *env) command(stdin string, args ...string) *exec.Cmd {
	cmd := exec.Command(e.bin, args...)
	cmd.Env = append(os.Environ(), e.vars...)
	cmd.Stdin = strings.NewReader(stdin)
	return cmd
}

func (e *env) run(stdin string, args ...string) result {
	e.t.Helper()
	cmd := e.command(stdin, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	r := result{stdout: out.String(), stderr: errb.String()}
	if ee, ok := err.(*exec.ExitError); ok {
		r.exit = ee.ExitCode()
	} else if err != nil {
		e.t.Fatalf("run %v: %v", args, err)
	}
	if strings.TrimSpace(r.stdout) != "" {
		if err := json.Unmarshal(out.Bytes(), &r.json); err != nil {
			e.t.Fatalf("stdout of %v is not one JSON object: %v\n%s", args, err, r.stdout)
		}
	}
	return r
}

func (e *env) mustOK(stdin string, args ...string) result {
	e.t.Helper()
	r := e.run(stdin, args...)
	if r.exit != 0 || r.json["ok"] != true {
		e.t.Fatalf("%v: exit %d\nstdout: %s\nstderr: %s", args, r.exit, r.stdout, r.stderr)
	}
	return r
}

func errCode(r result) string {
	e, _ := r.json["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestDaemonReusesOneSession(t *testing.T) {
	e := newEnv(t)
	r := e.mustOK("", "search", "github", "pull", "request", "--limit", "3", "--mode", "keyword")
	echo := r.json["data"].(map[string]any)["echo"].(map[string]any)
	if echo["query"] != "github pull request" || echo["limit"] != float64(3) || echo["mode"] != "keyword" {
		t.Errorf("search args not passed through: %v", echo)
	}
	for i := 0; i < 3; i++ {
		e.mustOK("return 1;", "execute", "-")
	}
	if got := e.srv.Created.Load(); got != 1 {
		t.Fatalf("sessions created = %d, want 1 (daemon should reuse the session)", got)
	}
	st := e.mustOK("", "daemon", "status").json["data"].(map[string]any)
	if st["running"] != true || st["served"] != float64(4) {
		t.Errorf("status = %v", st)
	}
	if sess := st["session"].(map[string]any); sess["connected"] != true || sess["protocol_version"] != "2025-11-25" {
		t.Errorf("session = %v", sess)
	}
	e.mustOK("", "daemon", "stop")
	if e.srv.Deleted.Load() != 1 || e.srv.LiveSessions() != 0 {
		t.Errorf("deleted=%d live=%d: daemon must DELETE its session on stop", e.srv.Deleted.Load(), e.srv.LiveSessions())
	}
	if st := e.mustOK("", "daemon", "status").json["data"].(map[string]any); st["running"] != false {
		t.Errorf("daemon still running after stop: %v", st)
	}
	if e.srv.Orphans.Load() != 0 {
		t.Errorf("orphan POSTs = %d", e.srv.Orphans.Load())
	}
}

func TestDaemonIdleExitDeletesSession(t *testing.T) {
	e := newEnv(t, "MCLI_DAEMON_IDLE=400ms")
	e.mustOK("", "search", "x")
	eventually(t, "idle daemon to DELETE its session", func() bool { return e.srv.Deleted.Load() == 1 })
	eventually(t, "idle daemon to exit", func() bool {
		return e.mustOK("", "daemon", "status").json["data"].(map[string]any)["running"] == false
	})
	// The next command transparently starts a new daemon and session.
	e.mustOK("", "search", "x")
	if e.srv.Created.Load() != 2 {
		t.Errorf("created = %d, want 2", e.srv.Created.Load())
	}
}

func TestNoDaemonMode(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 2; i++ {
		e.mustOK("return 1;", "--no-daemon", "execute", "-")
	}
	if e.srv.Created.Load() != 2 || e.srv.Deleted.Load() != 2 {
		t.Errorf("created=%d deleted=%d: one-shot mode must DELETE every session", e.srv.Created.Load(), e.srv.Deleted.Load())
	}
	e.vars = append(e.vars, "MCLI_NO_DAEMON=1")
	e.mustOK("", "search", "x")
	if st := e.mustOK("", "daemon", "status").json["data"].(map[string]any); st["running"] != false {
		t.Errorf("MCLI_NO_DAEMON=1 still started a daemon")
	}
}

func TestExecuteOutputAndLineMapping(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()

	ok := filepath.Join(dir, "ok.ts")
	os.WriteFile(ok, []byte("const n: number = 2;\nconsole.log(n);\nreturn { n };\n"), 0o600)
	r := e.mustOK("", "execute", ok, "--timeout", "45s", "--note", "e2e")
	data := r.json["data"].(map[string]any)
	if data["timeout_ms"] != float64(45000) || data["note"] != "e2e" {
		t.Errorf("execute args: %v", data)
	}
	if r.json["console"] != "hello" {
		t.Errorf("console = %v", r.json["console"])
	}
	code := e.srv.LastCode()
	if strings.Contains(code, ": number") || !strings.HasPrefix(code, "return await (") {
		t.Errorf("submitted code not transpiled/wrapped: %q", code)
	}

	bad := filepath.Join(dir, "bad.ts")
	os.WriteFile(bad, []byte("type Row = { id: string };\n\nconst rows: Row[] = [];\nconst x = \"__THROW__\";\nreturn rows;\n"), 0o600)
	r = e.run("", "execute", bad)
	if r.exit != 1 || errCode(r) != "E_RUNTIME" {
		t.Fatalf("exit %d: %s", r.exit, r.stdout)
	}
	errObj := r.json["error"].(map[string]any)
	if errObj["line"] != float64(4) {
		t.Errorf("error line = %v, want 4 (mapped back to bad.ts)\n%s", errObj["line"], r.stdout)
	}
	if !strings.Contains(errObj["message"].(string), "bad.ts:4") {
		t.Errorf("message lacks source position: %v", errObj["message"])
	}
	if errObj["trace_id"] == "" || r.json["diagnostics"] == nil {
		t.Errorf("trace/diagnostics missing: %s", r.stdout)
	}
	if !strings.Contains(r.stderr, "E_RUNTIME") {
		t.Errorf("stderr should carry a human-readable error: %q", r.stderr)
	}
}

func TestExitCodes(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		name  string
		stdin string
		args  []string
		exit  int
		code  string
	}{
		{"import rejected locally", "import fs from 'fs';\nreturn 1;", []string{"execute", "-"}, 2, "E_SCRIPT_MODULE"},
		{"syntax error", "const = 1;", []string{"execute", "-"}, 2, "E_SCRIPT_SYNTAX"},
		{"unknown flag", "", []string{"search", "x", "--bogus"}, 2, "E_USAGE"},
		{"missing arg", "", []string{"execute"}, 2, "E_USAGE"},
		{"bad mode", "", []string{"search", "x", "--mode", "fuzzy"}, 2, "E_USAGE"},
		{"call limit", "return '__LIMIT__';", []string{"execute", "-"}, 5, "E_CALL_LIMIT"},
		{"partial failure", "return '__PARTIAL__';", []string{"execute", "-"}, 1, "E_RUNTIME"},
		{"facade error", "", []string{"search", "__error__"}, 1, "E_FACADE"},
	}
	for _, c := range cases {
		r := e.run(c.stdin, c.args...)
		if r.exit != c.exit || errCode(r) != c.code {
			t.Errorf("%s: exit %d code %q, want %d %q\n%s", c.name, r.exit, errCode(r), c.exit, c.code, r.stdout)
		}
	}
	if e.srv.Executes.Load() != 2 {
		t.Errorf("server executes = %d: local pre-checks must not reach the server", e.srv.Executes.Load())
	}

	wrongKey := newEnv(t)
	wrongKey.vars = append(wrongKey.vars, "MCLI_API_KEY=wrong")
	if r := wrongKey.run("", "search", "x"); r.exit != 3 || errCode(r) != "E_AUTH" {
		t.Errorf("wrong key: exit %d %s", r.exit, r.stdout)
	}

	unconfigured := newEnv(t)
	unconfigured.vars = append(unconfigured.vars, "MCLI_URL=", "MCLI_API_KEY=")
	if r := unconfigured.run("", "search", "x"); r.exit != 2 || errCode(r) != "E_CONFIG" {
		t.Errorf("no config: exit %d %s", r.exit, r.stdout)
	}
}

func TestConfigSetAndShow(t *testing.T) {
	e := newEnv(t)
	url := e.srv.URL()
	e.vars = append(e.vars, "MCLI_URL=", "MCLI_API_KEY=")

	r := e.mustOK("", "config", "set", "--url", url)
	if r.json["data"].(map[string]any)["verified"] != false {
		t.Errorf("URL alone cannot be verified: %s", r.stdout)
	}
	if r := e.run("wrong-key\n", "config", "set", "--api-key-stdin"); r.exit != 3 {
		t.Errorf("wrong key should fail verification with exit 3: %d %s", r.exit, r.stdout)
	}
	r = e.mustOK("sk_test_key_9876\n", "config", "set", "--api-key-stdin")
	data := r.json["data"].(map[string]any)
	if data["verified"] != true || data["api_key"] != "****9876" {
		t.Errorf("set key: %s", r.stdout)
	}
	if strings.Contains(r.stdout+r.stderr, "sk_test_key_9876") {
		t.Error("the API key must never be printed")
	}
	show := e.mustOK("", "config", "show").json["data"].(map[string]any)
	if show["url"] != url || show["url_source"] != "file" || show["api_key"] != "****9876" {
		t.Errorf("show: %v", show)
	}
	if r := e.run("", "config", "set", "--url", "not a url"); r.exit != 2 {
		t.Errorf("invalid URL: exit %d", r.exit)
	}
	// Configured from the file alone, commands work.
	e.mustOK("", "search", "x")
}

func TestSessionExpiryIsTransparent(t *testing.T) {
	e := newEnv(t)
	e.mustOK("return 1;", "execute", "-")
	e.srv.ExpireSessions()
	e.mustOK("return 1;", "execute", "-")
	if e.srv.Created.Load() != 2 || e.srv.Executes.Load() != 2 {
		t.Errorf("created=%d executes=%d", e.srv.Created.Load(), e.srv.Executes.Load())
	}
}

func TestConcurrentColdStartSharesOneDaemon(t *testing.T) {
	e := newEnv(t)
	var wg sync.WaitGroup
	errs := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := e.run(fmt.Sprintf("const pause = \"__SLEEP_MS_300__\";\nreturn %d;", i), "execute", "-")
			if r.exit != 0 {
				errs <- r.stdout + r.stderr
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
	if got := e.srv.Created.Load(); got != 1 {
		t.Errorf("sessions created = %d, want 1 (single daemon via start lock)", got)
	}
	if e.srv.MaxInFlight.Load() < 2 {
		t.Errorf("max concurrent executes = %d: daemon should run requests concurrently", e.srv.MaxInFlight.Load())
	}
}

func TestKilledCLICancelsServerCall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process kill semantics differ")
	}
	e := newEnv(t)
	e.mustOK("", "search", "warm-up")
	cmd := e.command("return '__SLEEP_MS_20000__';", "execute", "-")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "execute to reach the server", func() bool { return e.srv.Executes.Load() == 1 })
	cmd.Process.Kill()
	cmd.Wait()
	eventually(t, "server-side cancellation", func() bool { return e.srv.Cancelled.Load() == 1 })
	// The daemon and its session survive.
	e.mustOK("return 1;", "execute", "-")
	if e.srv.Created.Load() != 1 {
		t.Errorf("created = %d, want 1", e.srv.Created.Load())
	}
}

func TestNewBuildReplacesDaemon(t *testing.T) {
	e := newEnv(t)
	e.mustOK("", "search", "x")
	before := e.mustOK("", "daemon", "status").json["data"].(map[string]any)

	// A different binary (new mtime) has a different build id.
	other := filepath.Join(t.TempDir(), filepath.Base(binary))
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, data, 0o755); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	os.Chtimes(other, later, later)
	e.bin = other

	e.mustOK("", "search", "x")
	after := e.mustOK("", "daemon", "status").json["data"].(map[string]any)
	if after["pid"] == before["pid"] || after["build"] == before["build"] {
		t.Fatalf("daemon not replaced: before=%v after=%v", before, after)
	}
	if e.srv.Deleted.Load() != 1 {
		t.Errorf("replaced daemon should DELETE its session; deleted=%d", e.srv.Deleted.Load())
	}
}

func TestHelpIsNotJSON(t *testing.T) {
	e := newEnv(t)
	cmd := e.command("", "--help")
	out, err := cmd.Output()
	if err != nil || !strings.Contains(string(out), "Exit codes:") || !strings.Contains(string(out), "function body") {
		t.Fatalf("help: %v\n%s", err, out)
	}
}

func TestDeepRuntimeDirStillUsesDaemon(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("named pipes have no path length limit")
	}
	e := newEnv(t)
	deep := filepath.Join(t.TempDir(), strings.Repeat("d", 60), strings.Repeat("e", 60))
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	e.vars = append(e.vars, "MCLI_RUNTIME_DIR="+deep)
	for i := 0; i < 2; i++ {
		r := e.mustOK("", "search", "x")
		if strings.Contains(r.stderr, "daemon unavailable") {
			t.Fatalf("fell back to in-process mode: %s", r.stderr)
		}
	}
	if e.srv.Created.Load() != 1 {
		t.Errorf("created = %d, want 1 (one daemon session)", e.srv.Created.Load())
	}
	if st := e.mustOK("", "daemon", "status").json["data"].(map[string]any); st["running"] != true {
		t.Errorf("status = %v", st)
	}
}

func TestDoctor(t *testing.T) {
	e := newEnv(t, "MCLI_NO_UPDATE_CHECK=1")
	r := e.mustOK("", "doctor", "--expect", "github,VPS", "--probe", "github__get_me", "--probe", `VPS__exec={"command":"true"}`)
	data := r.json["data"].(map[string]any)
	if srv := data["server"].(map[string]any); srv["version"] != fakeserver.Version || srv["protocol_version"] != "2025-11-25" {
		t.Errorf("server = %v", srv)
	}
	if servers := data["servers"].(map[string]any); servers["VPS"] != float64(3) || servers["github"] != float64(2) {
		t.Errorf("servers = %v", servers)
	}
	for _, p := range data["probes"].([]any) {
		if p.(map[string]any)["status"] != "ok" {
			t.Errorf("probe = %v", p)
		}
	}
	if strings.Contains(e.srv.LastCode(), "sk_test_key") {
		t.Error("the API key must not reach the script")
	}

	// A missing server, a misspelt probe and an upstream needing re-authorization.
	e.srv.RequireReauth("github__get_me")
	r = e.run("", "doctor", "--expect", "github,vps,openmessage", "--probe", "github__get_me", "--probe", "VPS__list_profiles")
	if r.exit != 1 || errCode(r) != "E_DOCTOR" {
		t.Fatalf("exit %d: %s", r.exit, r.stdout)
	}
	data = r.json["data"].(map[string]any)
	missing := data["expect"].(map[string]any)["missing"].([]any)
	if len(missing) != 2 || missing[0] != "vps" || missing[1] != "openmessage" {
		t.Errorf("missing = %v", missing)
	}
	probes := data["probes"].([]any)
	auth, typo := probes[0].(map[string]any), probes[1].(map[string]any)
	if auth["status"] != "auth_required" || auth["error_code"] != "AUTH_REAUTHORIZATION_REQUIRED" || auth["authorization_url"] == nil {
		t.Errorf("auth probe = %v", auth)
	}
	if typo["status"] != "not_found" || fmt.Sprint(typo["suggestions"]) != "[VPS__list-profiles]" {
		t.Errorf("typo probe = %v", typo)
	}
	msg := r.json["error"].(map[string]any)["message"].(string)
	if !strings.Contains(msg, `did you mean "VPS"`) || !strings.Contains(msg, "openmessage") {
		t.Errorf("message = %s", msg)
	}

	if r := e.run("", "doctor", "--probe", "x={not json"); r.exit != 2 || errCode(r) != "E_USAGE" {
		t.Errorf("bad probe: exit %d %s", r.exit, r.stdout)
	}
	if r := e.run("", "doctor", "--expect", ","); r.exit != 2 || errCode(r) != "E_USAGE" {
		t.Errorf("empty expect: exit %d %s", r.exit, r.stdout)
	}
	wrongKey := newEnv(t)
	wrongKey.vars = append(wrongKey.vars, "MCLI_API_KEY=wrong")
	if r := wrongKey.run("", "doctor"); r.exit != 3 || errCode(r) != "E_AUTH" {
		t.Errorf("wrong key: exit %d %s", r.exit, r.stdout)
	}
}

func TestToolsAndSchema(t *testing.T) {
	e := newEnv(t)
	all := e.mustOK("", "tools").json["data"].(map[string]any)
	cats := all["categories"].([]any)
	if all["count"] != float64(5) || len(cats) != 2 || cats[0].(map[string]any)["category"] != "VPS" {
		t.Errorf("tools = %v", all)
	}
	vps := e.mustOK("", "tools", "--category", "VPS").json["data"].(map[string]any)
	if vps["count"] != float64(3) || vps["tools"].([]any)[0].(map[string]any)["summary"] == nil {
		t.Errorf("tools --category = %v", vps)
	}
	r := e.run("", "tools", "--category", "vps")
	if r.exit != 1 || errCode(r) != "E_TOOL_NOT_FOUND" || fmt.Sprint(r.json["error"].(map[string]any)["suggestions"]) != "[VPS]" {
		t.Errorf("unknown category: exit %d %s", r.exit, r.stdout)
	}
	if r := e.run("", "tools", "--declarations"); r.exit != 2 {
		t.Errorf("--declarations without --category: exit %d", r.exit)
	}

	s := e.mustOK("", "schema", "VPS.exec").json["data"].(map[string]any)["tools"].([]any)
	if len(s) != 1 || s[0].(map[string]any)["inputSchema"] == nil {
		t.Errorf("schema = %v", s)
	}
	if s := e.mustOK("", "schema", "tools.VPS__exec").json["data"].(map[string]any)["tools"].([]any); len(s) != 1 {
		t.Errorf("schema with tools. prefix = %v", s)
	}
	if r := e.run("", "schema", " "); r.exit != 2 || errCode(r) != "E_USAGE" {
		t.Errorf("empty schema name: exit %d %s", r.exit, r.stdout)
	}
	r = e.run("", "schema", "VPS__exec", "VPS__run_script")
	if r.exit != 1 || errCode(r) != "E_TOOL_NOT_FOUND" {
		t.Fatalf("schema typo: exit %d %s", r.exit, r.stdout)
	}
	errObj := r.json["error"].(map[string]any)
	if fmt.Sprint(errObj["suggestions"]) != "[VPS__run-script]" || !strings.Contains(errObj["hint"].(string), `tools.VPS["run-script"](args)`) {
		t.Errorf("schema typo error = %v", errObj)
	}
	if len(r.json["data"].(map[string]any)["tools"].([]any)) != 1 {
		t.Errorf("the tools that exist should still be returned: %s", r.stdout)
	}
}

func TestExecuteSuggestsToolNames(t *testing.T) {
	e := newEnv(t)
	r := e.run("const marker = \"__MISSING_TOOL__\";\nreturn await tools.VPS.run_script({});", "execute", "-")
	if r.exit != 1 || errCode(r) != "E_TOOL_NOT_FOUND" {
		t.Fatalf("exit %d: %s", r.exit, r.stdout)
	}
	errObj := r.json["error"].(map[string]any)
	if fmt.Sprint(errObj["suggestions"]) != "[VPS__run-script]" || !strings.HasPrefix(errObj["hint"].(string), `Did you mean "VPS__run-script"?`) {
		t.Errorf("error = %v", errObj)
	}

	// Some servers wrap the child E_TOOL_NOT_FOUND in a top-level E_RUNTIME;
	// the name in the diagnostics/message must still produce suggestions.
	r = e.run("const marker = \"__MISSING_TOOL_RUNTIME__\";\nreturn await tools.VPS.run_script({});", "execute", "-")
	if r.exit != 1 || errCode(r) != "E_RUNTIME" {
		t.Fatalf("runtime wrapper: exit %d: %s", r.exit, r.stdout)
	}
	errObj = r.json["error"].(map[string]any)
	if fmt.Sprint(errObj["suggestions"]) != "[VPS__run-script]" || !strings.HasPrefix(errObj["hint"].(string), `Did you mean "VPS__run-script"?`) {
		t.Errorf("runtime wrapper error = %v", errObj)
	}
}

func TestVersionCheck(t *testing.T) {
	releases := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ZeroPointSix/metamcp-cli/releases/tag/v99.0.0", http.StatusFound)
	}))
	defer releases.Close()
	e := newEnv(t, "MCLI_RELEASES_URL="+releases.URL+"/releases/latest")
	data := e.mustOK("", "version", "--check").json["data"].(map[string]any)
	if data["latest"] != "v99.0.0" || data["version"] == nil {
		t.Errorf("version --check = %v", data)
	}
	cli := e.mustOK("", "doctor").json["data"].(map[string]any)["cli"].(map[string]any)
	if cli["latest"] != "v99.0.0" {
		t.Errorf("doctor cli = %v", cli)
	}
}
