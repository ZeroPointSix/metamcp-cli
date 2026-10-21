package ops

import (
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ZeroPointSix/metamcp-cli/internal/output"
)

func result(meta mcp.Meta, texts ...string) *mcp.CallToolResult {
	r := &mcp.CallToolResult{Meta: meta}
	for _, t := range texts {
		r.Content = append(r.Content, &mcp.TextContent{Text: t})
	}
	return r
}

var successMeta = mcp.Meta{codeModeMetaKey: map[string]any{"partial": false, "terminal_reason": "success", "trace_id": "trace_1", "child_successes": 2, "child_failures": 0}}

func TestExecuteCompactSuccess(t *testing.T) {
	// Shape observed from a production MetaMCP endpoint.
	r := parseExecute(result(successMeta, "{\n  \"x\": 2\n}", "<console_output>\nhi\n\n</console_output>"))
	if !r.OK || string(r.Data) != `{"x":2}` || r.Console != "hi" {
		t.Fatalf("got %+v data=%s", r, r.Data)
	}
	if r.Meta == nil || r.Meta.TraceID != "trace_1" || r.Meta.ChildSuccesses != 2 {
		t.Fatalf("meta = %+v", r.Meta)
	}
}

func TestExecuteNullAndStringResults(t *testing.T) {
	if r := parseExecute(result(successMeta, "null")); !r.OK || string(r.Data) != "null" {
		t.Fatalf("null result: %+v", r)
	}
	if r := parseExecute(result(successMeta, `"text"`)); !r.OK || string(r.Data) != `"text"` {
		t.Fatalf("string result: %+v", r)
	}
	// A script may legitimately return an object with an "error" key.
	if r := parseExecute(result(successMeta, `{"error":"not really"}`)); !r.OK {
		t.Fatalf("script result misread as failure: %+v", r)
	}
	// Auxiliary resource content is ignored.
	res := result(successMeta, "1")
	res.Content = append(res.Content, &mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "skill://x", Text: "guide"}})
	if r := parseExecute(res); !r.OK || string(r.Data) != "1" {
		t.Fatalf("aux content: %+v", r)
	}
}

const failedEnvelope = `{
  "ok": false, "partial": false, "exit_code": 1, "result": null,
  "diagnostics": [{"severity": "error", "code": "E_RUNTIME", "message": "Error: boom at line 3", "source": "execute",
    "stack": "Error: boom\n    at code-mode-user.js:5:7", "location": {"line": 3, "column": 7}}],
  "trace_id": "trace_18e2", "trace": [],
  "stats": {"duration_ms": 117, "queue_wait_ms": 0, "child_calls": 0, "operations": 0, "child_failures": 0, "output_bytes": 0}
}`

func TestExecuteFailureEnvelope(t *testing.T) {
	// Production failures arrive with isError=false (host-safe results).
	r := parseExecute(result(nil, failedEnvelope))
	if r.OK || r.Error == nil {
		t.Fatalf("want failure, got %+v", r)
	}
	e := r.Error
	if e.Code != "E_RUNTIME" || e.Exit != output.ExitFailure || e.TraceID != "trace_18e2" || e.Line != 3 || e.Column != 7 {
		t.Fatalf("error = %+v", e)
	}
	if len(r.Diagnostics) != 1 || r.Meta == nil || r.Meta.TraceID != "trace_18e2" {
		t.Fatalf("diagnostics/meta not kept: %+v", r)
	}
}

func TestExecuteErrorCodesMapToExits(t *testing.T) {
	cases := map[string]int{
		"E_TIMEOUT":          output.ExitUncertain,
		"E_CHILD_TIMEOUT":    output.ExitUncertain,
		"E_CANCELLED":        output.ExitUncertain,
		"E_CALL_LIMIT":       output.ExitLimit,
		"E_OUTPUT_LIMIT":     output.ExitLimit,
		"E_MEMORY_LIMIT":     output.ExitLimit,
		"E_TOOL_NOT_FOUND":   output.ExitFailure,
		"E_INVALID_ARGUMENT": output.ExitFailure,
	}
	for code, exit := range cases {
		env := `{"ok":false,"partial":false,"result":null,"diagnostics":[{"severity":"error","code":"` + code + `","message":"m"}],"trace_id":"t","trace":[],"stats":{}}`
		r := parseExecute(result(nil, env))
		if r.ExitCode() != exit || r.Error.Code != code {
			t.Errorf("%s: exit %d, want %d", code, r.ExitCode(), exit)
		}
	}
	busy := `{"ok":false,"partial":false,"result":null,"diagnostics":[{"severity":"error","code":"E_RUNTIME","message":"Code Mode default pool is at capacity (4 concurrent executions, 16 queued). Retry later."}],"trace_id":"t","trace":[],"stats":{}}`
	if r := parseExecute(result(nil, busy)); r.Error.Code != output.CodeCapacity || r.ExitCode() != output.ExitLimit {
		t.Errorf("capacity: %+v", r.Error)
	}
}

func TestExecuteWarningEnvelope(t *testing.T) {
	env := `{"ok":true,"partial":false,"result":{"a":1},"diagnostics":[{"severity":"warning","code":"W_TOOL_LIST_DEGRADED","message":"m"}],"console_output":"log","trace_id":"t","trace":[],"stats":{}}`
	r := parseExecute(result(successMeta, env))
	if !r.OK || string(r.Data) != `{"a":1}` || r.Console != "log" || len(r.Warnings) != 1 {
		t.Fatalf("got %+v", r)
	}
}

func TestExecutePartialFailure(t *testing.T) {
	env := `{"ok":false,"partial":true,"result":null,"diagnostics":[{"severity":"error","code":"E_RUNTIME","message":"m"}],"trace_id":"t","trace":[{"tool":"a__b","ok":true}],"stats":{}}`
	r := parseExecute(result(mcp.Meta{codeModeMetaKey: map[string]any{"partial": true, "terminal_reason": "E_RUNTIME", "trace_id": "t", "child_successes": 1}}, env))
	if r.OK || !r.Meta.Partial || r.Meta.ChildSuccesses != 1 || len(r.Trace) == 0 {
		t.Fatalf("got %+v", r)
	}
}

func TestFacadeErrors(t *testing.T) {
	r := parseExecute(result(nil, `{"error":"Facade execution failed. See the MetaMCP audit log for details.","code":"FACADE_EXECUTION_FAILED"}`))
	if r.OK || r.Error.Code != output.CodeFacade {
		t.Fatalf("execute facade error: %+v", r)
	}
	r = parseSearch(result(nil, `{"error":"search backend unavailable"}`))
	if r.OK || r.Error.Code != output.CodeFacade {
		t.Fatalf("search facade error: %+v", r)
	}
}

func TestSearchSuccess(t *testing.T) {
	body := `{"tools":[{"name":"github__merge_pull_request","category":"github"}],"count":1,"total":29,"next_cursor":"Mg==","warnings":[]}`
	r := parseSearch(result(nil, body))
	if !r.OK {
		t.Fatalf("got %+v", r)
	}
	var data map[string]any
	if err := json.Unmarshal(r.Data, &data); err != nil || data["next_cursor"] != "Mg==" {
		t.Fatalf("data = %s", r.Data)
	}
	res := &mcp.CallToolResult{StructuredContent: map[string]any{"matched": true, "tools": []any{}}}
	if r := parseSearch(res); !r.OK || string(r.Data) != `{"matched":true,"tools":[]}` {
		t.Fatalf("structured: %+v", r)
	}
}
