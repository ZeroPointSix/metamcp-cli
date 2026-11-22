package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func warningCodes(r result) []string {
	var out []string
	list, _ := r.json["warnings"].([]any)
	for _, w := range list {
		if m, ok := w.(map[string]any); ok {
			out = append(out, m["code"].(string))
		}
	}
	return out
}

func TestExecuteEval(t *testing.T) {
	e := newEnv(t)
	r := e.mustOK("", "execute", "-e", "return 1;", "--note", "eval")
	if r.json["data"].(map[string]any)["note"] != "eval" {
		t.Errorf("eval not executed: %s", r.stdout)
	}
	if r := e.run("", "execute", "x.ts", "-e", "return 1;"); r.exit != 2 || errCode(r) != "E_USAGE" {
		t.Errorf("file and -e together: exit %d %s", r.exit, r.stdout)
	}
	if r := e.run("", "execute", "-e", "import x from 'y';"); r.exit != 2 || errCode(r) != "E_SCRIPT_MODULE" {
		t.Errorf("eval goes through local checks: exit %d %s", r.exit, r.stdout)
	}
}

func TestCompactOutputWhenPiped(t *testing.T) {
	e := newEnv(t)
	r := e.mustOK("", "execute", "-e", "return 1;")
	if strings.Count(strings.TrimSpace(r.stdout), "\n") != 0 {
		t.Errorf("piped output should be one line:\n%s", r.stdout)
	}
	e.vars = append(e.vars, "MCLI_OUTPUT=pretty")
	r = e.mustOK("", "execute", "-e", "return 1;")
	if !strings.Contains(r.stdout, "\n  \"ok\": true") {
		t.Errorf("MCLI_OUTPUT=pretty should indent:\n%s", r.stdout)
	}
}

func TestLargeDataGoesToFile(t *testing.T) {
	dir := t.TempDir()
	e := newEnv(t, "MCLI_MAX_OUTPUT=10", "MCLI_OUTPUT_DIR="+dir)
	r := e.mustOK("", "execute", "-e", "return 1;")
	if _, ok := r.json["data"]; ok {
		t.Fatalf("data over the budget should not be on stdout: %s", r.stdout)
	}
	spill := r.json["data_file"].(map[string]any)
	path := spill["path"].(string)
	if filepath.Dir(path) != dir || spill["bytes"].(float64) <= 10 || spill["preview"] == "" {
		t.Errorf("data_file = %v", spill)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil || data["code_len"] == nil {
		t.Errorf("spilled file is not the data JSON: %v\n%s", err, raw)
	}

	// --out always writes, whatever the size.
	small := newEnv(t)
	out := filepath.Join(t.TempDir(), "r.json")
	r = small.mustOK("", "execute", "-e", "return 1;", "--out", out)
	if r.json["data_file"].(map[string]any)["path"] != out {
		t.Errorf("--out path: %s", r.stdout)
	}
	if _, err := os.Stat(out); err != nil {
		t.Error(err)
	}
}

func TestUpstreamOutageIsFlattenedAndRetryable(t *testing.T) {
	e := newEnv(t)
	r := e.run("", "execute", "-e", "return '__UPSTREAM_DOWN__';")
	if r.exit != 5 || errCode(r) != "E_UPSTREAM_UNAVAILABLE" {
		t.Fatalf("exit %d: %s", r.exit, r.stdout)
	}
	errObj := r.json["error"].(map[string]any)
	msg := errObj["message"].(string)
	if !strings.Contains(msg, "Upstream MCP is temporarily unavailable") || strings.Contains(msg, "{") ||
		strings.Count(msg, "ChildCallError") > 1 {
		t.Errorf("message not flattened exactly once: %q", msg)
	}
	if errObj["tool"] != "maton__run_action" || !strings.Contains(errObj["hint"].(string), "Retry") {
		t.Errorf("error = %v", errObj)
	}
	if strings.Contains(r.stdout, "stack") || strings.Contains(r.stdout, "trace_inner") {
		t.Errorf("nested envelope or stack leaked into output: %s", r.stdout)
	}
	if lines := strings.Split(strings.TrimSpace(r.stderr), "\n"); len(lines) != 1 || len(lines[0]) > 260 {
		t.Errorf("stderr should be one short line: %q", r.stderr)
	}
}

func TestArgumentCheck(t *testing.T) {
	e := newEnv(t)
	src := "return await tools.VPS.exec({ comand: \"ls\" });"
	r := e.mustOK("", "execute", "-e", src)
	list, _ := r.json["warnings"].([]any)
	if len(list) != 1 {
		t.Fatalf("warnings = %v", r.json["warnings"])
	}
	w := list[0].(map[string]any)
	if w["code"] != "W_UNKNOWN_ARGUMENT" || w["argument"] != "comand" || w["suggestion"] != "command" || w["line"] != float64(1) {
		t.Errorf("warning = %v", w)
	}

	before := e.srv.Executes.Load()
	r = e.run("", "execute", "-e", src, "--strict-args")
	if r.exit != 2 || errCode(r) != "E_USAGE" || len(warningCodes(r)) != 1 {
		t.Errorf("--strict-args: exit %d %s", r.exit, r.stdout)
	}
	if got := e.srv.Executes.Load() - before; got != 1 {
		t.Errorf("--strict-args ran %d server executes, want only the schema lookup", got)
	}

	if r := e.mustOK("", "execute", "-e", "return await tools.VPS.exec({ command: \"ls\" });"); len(warningCodes(r)) != 0 {
		t.Errorf("correct call warned: %s", r.stdout)
	}

	checks := e.srv.ArgChecks.Load()
	e.mustOK("", "execute", "-e", "return 1;")
	off := newEnv(t, "MCLI_ARG_CHECK=0")
	off.mustOK("", "execute", "-e", src)
	if e.srv.ArgChecks.Load() != checks || off.srv.ArgChecks.Load() != 0 {
		t.Error("argument check should only run for scripts with literal tool calls, and not when disabled")
	}
}

func TestNoReturnWarning(t *testing.T) {
	e := newEnv(t)
	r := e.mustOK("", "execute", "-e", "console.log(JSON.stringify({ x: '__NO_RETURN__' }));")
	if got := warningCodes(r); len(got) != 1 || got[0] != "W_NO_RETURN" || r.json["data"] != nil {
		t.Errorf("warnings %v: %s", got, r.stdout)
	}
	r = e.mustOK("", "execute", "-e", "console.log('__NO_RETURN__'); return null;")
	if got := warningCodes(r); len(got) != 0 {
		t.Errorf("an explicit return null must not warn: %s", r.stdout)
	}
}

func TestOutFileFailureIsAnError(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	nested := filepath.Join(dir, "new", "dir", "r.json")
	if r := e.mustOK("", "execute", "-e", "return 1;", "-o", nested); r.json["data_file"].(map[string]any)["path"] != nested {
		t.Errorf("-o into a new directory: %s", r.stdout)
	}
	blocker := filepath.Join(dir, "file")
	os.WriteFile(blocker, []byte("x"), 0o600)
	r := e.run("", "execute", "-e", "return 1;", "--out", filepath.Join(blocker, "r.json"))
	if r.exit != 2 || errCode(r) != "E_OUT_FILE" || r.json["data"] == nil {
		t.Errorf("unwritable --out: exit %d %s", r.exit, r.stdout)
	}
	if !strings.Contains(r.json["error"].(map[string]any)["hint"].(string), "DID run") {
		t.Errorf("hint must say the script ran: %s", r.stdout)
	}
}

func TestDefaultSpillDirIsPrivateAndPruned(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the default directory is under the per-user cache dir on Windows")
	}
	tmp := t.TempDir()
	e := newEnv(t, "MCLI_MAX_OUTPUT=10", "TMPDIR="+tmp, "MCLI_OUTPUT_DIR=")
	dir := filepath.Join(tmp, fmt.Sprintf("mcli-output-%d", os.Getuid()))
	os.MkdirAll(dir, 0o700)
	old := filepath.Join(dir, "20200101-000000-1.json")
	os.WriteFile(old, []byte("{}"), 0o600)
	stale := time.Now().Add(-48 * time.Hour)
	os.Chtimes(old, stale, stale)
	keep := filepath.Join(dir, "notes.txt")
	os.WriteFile(keep, []byte("x"), 0o600)
	os.Chtimes(keep, stale, stale)

	r := e.mustOK("", "execute", "-e", "return 1;")
	path := r.json["data_file"].(map[string]any)["path"].(string)
	if filepath.Dir(path) != dir {
		t.Errorf("spilled to %s, want %s", path, dir)
	}
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("spill dir mode: %v %v", st.Mode(), err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("spill files older than a day should be pruned")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Error("files mcli did not create must not be pruned")
	}
}

func TestStrictArgsOverridesEnvAndFailsClosed(t *testing.T) {
	src := "return await tools.VPS.exec({ comand: \"ls\" });"
	e := newEnv(t, "MCLI_ARG_CHECK=0")
	if r := e.run("", "execute", "-e", src, "--strict-args"); r.exit != 2 || errCode(r) != "E_USAGE" {
		t.Errorf("--strict-args with MCLI_ARG_CHECK=0: exit %d %s", r.exit, r.stdout)
	}

	f := newEnv(t)
	f.srv.FailArgCheck.Store(true)
	before := f.srv.Executes.Load()
	if r := f.run("", "execute", "-e", src, "--strict-args"); r.exit != 5 || errCode(r) != "E_ARG_CHECK" {
		t.Errorf("strict with failed lookup: exit %d %s", r.exit, r.stdout)
	}
	if got := f.srv.Executes.Load() - before; got != 1 {
		t.Errorf("strict with failed lookup ran %d executes, want only the lookup", got)
	}
	r := f.mustOK("", "execute", "-e", src)
	if got := warningCodes(r); len(got) != 1 || got[0] != "W_ARG_CHECK_SKIPPED" {
		t.Errorf("non-strict failed lookup: %s", r.stdout)
	}
}
