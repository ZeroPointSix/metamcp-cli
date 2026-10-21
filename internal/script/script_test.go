package script

import (
	"errors"
	"strings"
	"testing"

	"github.com/ZeroPointSix/metamcp-cli/internal/output"
)

func code(t *testing.T, err error) string {
	t.Helper()
	var e *output.Error
	if !errors.As(err, &e) {
		t.Fatalf("want *output.Error, got %v", err)
	}
	return e.Code
}

func TestPrepareStripsTypesAndWraps(t *testing.T) {
	src := "\xef\xbb\xbfinterface Card { name: string }\nconst r: { tools: Card[] } = await tools.search(\"github\");\nreturn r.tools.map((c: Card) => c.name);\n"
	p, err := Prepare([]byte(src), "task.ts")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Code, "return await ((async () => {") || !strings.HasSuffix(p.Code, "}))();") {
		t.Fatalf("unexpected wrapping: %q", p.Code)
	}
	for _, typ := range []string{"interface", ": Card", ": string"} {
		if strings.Contains(p.Code, typ) {
			t.Errorf("type syntax %q survived: %q", typ, p.Code)
		}
	}
	if !strings.Contains(p.Code, "await tools.search") {
		t.Errorf("body lost: %q", p.Code)
	}
}

func TestPrepareJavaScriptAndCRLF(t *testing.T) {
	p, err := Prepare([]byte("const a = 1;\r\nreturn a + 1;\r\n"), "task.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Code, "return a + 1") {
		t.Fatalf("got %q", p.Code)
	}
	if _, err := Prepare([]byte("const a: number = 1;"), "task.js"); code(t, err) != output.CodeScriptSyntax {
		t.Fatal("type annotations must be rejected in .js files")
	}
}

func TestPrepareRejectsModules(t *testing.T) {
	for _, src := range []string{
		"import fs from 'fs';\nreturn 1;",
		"const a = 1;\nimport { x } from \"y\";\nreturn a;",
		"export const x = 1;\nreturn x;",
		"export default 1;",
		"import * as m from 'm';",
	} {
		_, err := Prepare([]byte(src), "task.ts")
		if code(t, err) != output.CodeScriptModule {
			t.Errorf("%q: want %s, got %v", src, output.CodeScriptModule, err)
		}
		var e *output.Error
		errors.As(err, &e)
		if e.Exit != output.ExitUsage || e.Hint == "" {
			t.Errorf("%q: want exit 2 with hint, got %+v", src, e)
		}
	}
}

func TestPrepareSyntaxErrorLine(t *testing.T) {
	_, err := Prepare([]byte("const a = 1;\nconst b = ;\nreturn b;"), "task.ts")
	var e *output.Error
	if !errors.As(err, &e) || e.Code != output.CodeScriptSyntax {
		t.Fatalf("got %v", err)
	}
	if e.Line != 2 {
		t.Errorf("line = %d, want 2 (wrapper offset removed)", e.Line)
	}
	if !strings.Contains(e.Message, "task.ts:2:") {
		t.Errorf("message %q lacks file:line", e.Message)
	}
	// An unterminated block points past the user's last line.
	_, err = Prepare([]byte("if (x) {\nreturn 1;"), "task.ts")
	if !errors.As(err, &e) || e.Line != 2 {
		t.Errorf("unterminated block: got %+v", e)
	}
}

func TestPrepareLimits(t *testing.T) {
	if _, err := Prepare([]byte("  \n "), "t.ts"); code(t, err) != output.CodeUsage {
		t.Error("empty script should be a usage error")
	}
	big := "return \"" + strings.Repeat("x", MaxCodeBytes) + "\";"
	if _, err := Prepare([]byte(big), "t.ts"); code(t, err) != output.CodeScriptTooLarge {
		t.Error("oversized code should be rejected locally")
	}
	if _, _, err := Read("-", strings.NewReader(strings.Repeat("a", MaxSourceBytes+1))); code(t, err) != output.CodeScriptTooLarge {
		t.Error("oversized source should be rejected while reading")
	}
	if _, _, err := Read("/nonexistent/task.ts", nil); code(t, err) != output.CodeUsage {
		t.Error("missing file should be a usage error")
	}
}

func TestMapPosition(t *testing.T) {
	src := "type X = { a: number };\n\nconst value: X = { a: 1 };\nconsole.log(value);\nthrow new Error(\"boom\");\n"
	p, err := Prepare([]byte(src), "task.ts")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(p.Code, "\n")
	found := false
	for i, l := range lines {
		col := strings.Index(l, "throw")
		if col < 0 {
			continue
		}
		found = true
		line, c, ok := p.MapPosition(i+1, col+1)
		if !ok || line != 5 {
			t.Fatalf("mapped submitted line %d to %d (ok=%v), want 5", i+1, line, ok)
		}
		if c != 1 {
			t.Errorf("column = %d, want 1", c)
		}
		if got := p.SourceLine(line); !strings.HasPrefix(got, "throw") {
			t.Errorf("SourceLine = %q", got)
		}
	}
	if !found {
		t.Fatalf("throw not found in %q", p.Code)
	}
	if _, _, ok := p.MapPosition(999, 1); ok {
		t.Error("out-of-range line must not map")
	}
}

func TestDecodeVLQ(t *testing.T) {
	cases := map[string][]int{"A": {0}, "C": {1}, "D": {-1}, "gB": {16}, "AAgBC": {0, 0, 16, 1}}
	for in, want := range cases {
		got, err := decodeVLQ(in)
		if err != nil || len(got) != len(want) {
			t.Fatalf("%s: got %v, %v", in, got, err)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s: got %v want %v", in, got, want)
			}
		}
	}
	if _, err := decodeVLQ("g"); err == nil {
		t.Error("truncated VLQ must fail")
	}
}
