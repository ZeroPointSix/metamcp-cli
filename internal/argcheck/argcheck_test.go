package argcheck

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestExtract(t *testing.T) {
	src := "// tools.fake.call({ nope: 1 })\n" +
		"const s = \"tools.str.ing({ x: 1 })\";\n" +
		"const a = await tools.memory.search_nodes({ q: \"mcli\" });\n" +
		"const b = await tools.VPS[\"run-script\"]({\n  script: `echo ${x}`, // comment, with comma\n  env: { A: \"1\", B: [1, 2] },\n  fn(x) { return { y: x }; },\n  'quoted-key': 2,\n  short,\n});\n" +
		"const c = await tools.call(\"github__get_me\", {});\n" +
		"const d = await tools.call('VPS.exec', { ...base, command });\n" +
		"const e = await tools.github.list_pull_requests(args);\n" +
		"const f = other.tools.x.y({ z: 1 });\n" +
		"const g = await tools.list({ names: [\"a\"] });\n" +
		"const h = await tools.x.y({ [k]: 1, ok: 2 });\n"
	got := Extract(src)
	want := []Call{
		{Tool: "memory__search_nodes", Keys: []string{"q"}, Exact: true, Line: 3},
		{Tool: "VPS__run-script", Keys: []string{"script", "env", "fn", "quoted-key", "short"}, Exact: true, Line: 4},
		{Tool: "github__get_me", Keys: nil, Exact: true, Line: 11},
		{Tool: "VPS__exec", Keys: []string{"command"}, Exact: false, Line: 12},
		{Tool: "x__y", Keys: []string{"ok"}, Exact: false, Line: 16},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("Extract:\n got %v\nwant %v", got, want)
	}
}

func TestExtractToleratesBrokenSource(t *testing.T) {
	for _, src := range []string{"tools.", "tools.a.b(", "tools.a.b({", "tools.a.b({ x: `", "tools.call(\"a__b\"", "tools[\"a\"]"} {
		_ = Extract(src) // must not panic or loop
	}
}

func schema(t *testing.T, s string) Schema {
	t.Helper()
	return ParseSchema(json.RawMessage(s))
}

func TestCheck(t *testing.T) {
	schemas := map[string]Schema{
		"memory__search_nodes":  schema(t, `{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
		"maton__search_actions": schema(t, `{"type":"object","properties":{"pattern":{},"prefix":{},"maxCount":{},"app":{}}}`),
		"open__tool":            schema(t, `{"type":"object","properties":{"a":{}},"additionalProperties":true}`),
		"free__tool":            schema(t, `{"type":"object"}`),
	}
	calls := []Call{
		{Tool: "memory__search_nodes", Keys: []string{"q"}, Exact: true, Line: 3},
		{Tool: "maton__search_actions", Keys: []string{"query", "max_count"}, Exact: true, Line: 4},
		{Tool: "open__tool", Keys: []string{"zzz"}, Exact: true},
		{Tool: "free__tool", Keys: []string{"anything"}, Exact: true},
		{Tool: "unknown__tool", Keys: []string{"x"}, Exact: true},
		{Tool: "memory__search_nodes", Keys: []string{"query"}, Exact: true},
		{Tool: "memory__search_nodes", Keys: nil, Exact: false},
	}
	var got []string
	for _, w := range Check(calls, schemas) {
		got = append(got, fmt.Sprintf("%s %s %s->%s L%d", w.Code, w.Tool, w.Argument, w.Suggestion, w.Line))
	}
	want := []string{
		"W_UNKNOWN_ARGUMENT memory__search_nodes q->query L3",
		"W_MISSING_ARGUMENT memory__search_nodes query-> L3",
		"W_UNKNOWN_ARGUMENT maton__search_actions query-> L4",
		"W_UNKNOWN_ARGUMENT maton__search_actions max_count->maxCount L4",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("Check:\n got %q\nwant %q", got, want)
	}
}

func TestClosest(t *testing.T) {
	props := []string{"owner", "repo", "pullNumber", "query"}
	cases := map[string]string{"q": "query", "pull_number": "pullNumber", "ownr": "owner", "repository": "repo", "zzzzzz": ""}
	for in, want := range cases {
		if got := closest(in, props, nil); got != want {
			t.Errorf("closest(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestZeroArgumentSchemaIsClosed(t *testing.T) {
	closed := schema(t, `{"type":"object","properties":{},"additionalProperties":false}`)
	if closed.Open {
		t.Fatal("additionalProperties:false with no properties must be closed")
	}
	got := Check([]Call{{Tool: "a__b", Keys: []string{"x"}, Exact: true}}, map[string]Schema{"a__b": closed})
	if len(got) != 1 || got[0].Code != CodeUnknown {
		t.Errorf("warnings = %+v", got)
	}
	if schema(t, `{"type":"object","properties":{}}`).Open == false {
		t.Error("a schema with no properties and no additionalProperties stays open")
	}
}

func TestExtractZeroArgumentAndNestedCalls(t *testing.T) {
	src := "await tools.github.get_me();\n" +
		"await tools.call(\"VPS__exec\");\n" +
		"await tools.a.b({ x: await tools.c.d({ q: 1 }), y: 2 });\n"
	want := []Call{
		{Tool: "github__get_me", Exact: true, Line: 1},
		{Tool: "VPS__exec", Exact: true, Line: 2},
		{Tool: "a__b", Keys: []string{"x", "y"}, Exact: true, Line: 3},
		{Tool: "c__d", Keys: []string{"q"}, Exact: true, Line: 3},
	}
	if got := Extract(src); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("Extract:\n got %v\nwant %v", got, want)
	}
}

func TestHasTopLevelReturn(t *testing.T) {
	cases := map[string]bool{
		"return 1;":            true,
		"if (x) { return 1; }": true,
		"for (const a of b) { if (a) { return a; } }":           true,
		"try { return 1 } catch (e) { return 2 }":               true,
		"items.map(x => { return x.id; }); console.log(items);": false,
		"items.map(function (x) { return x; });":                false,
		"const o = { m() { return 1; } }; console.log(o);":      false,
		"// return 1\nconsole.log('return');":                   false,
		"console.log(`${(() => { return 1 })()}`);":             false,
		"const f = async () => { return 1 }; return await f();": true,
		"x.return = 1;": false,
	}
	for src, want := range cases {
		if got := HasTopLevelReturn(src); got != want {
			t.Errorf("HasTopLevelReturn(%q) = %v, want %v", src, got, want)
		}
	}
}
