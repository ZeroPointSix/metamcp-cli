package ops

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ZeroPointSix/metamcp-cli/internal/output"
)

// MetaMCP clears isError on facade results before they leave the server
// ("host-safe" results), so failures must be recognised from the payload:
//
//   - execute success, no warnings: content[0] is the returned JSON value and an
//     optional second text block wraps console output in <console_output>.
//   - execute failure or warning-bearing success: content[0] is the full Code
//     Mode envelope {ok, partial, result, diagnostics, trace_id, trace, stats}.
//   - any facade failure outside Code Mode: {"error": "...", "code"?: "..."}.
//
// _meta["metamcp/codeMode"] summarises every Code Mode run.

const (
	codeModeMetaKey = "metamcp/codeMode"
	consoleOpen     = "<console_output>\n"
	consoleClose    = "\n</console_output>"
)

func texts(res *mcp.CallToolResult) (first string, console string, found bool) {
	for _, c := range res.Content {
		tc, ok := c.(*mcp.TextContent)
		if !ok {
			continue // auxiliary resources (skills, guidance) are not part of the result
		}
		if strings.HasPrefix(tc.Text, consoleOpen) {
			console = strings.TrimSuffix(strings.TrimPrefix(tc.Text, consoleOpen), consoleClose)
			console = strings.TrimSuffix(console, "\n")
			continue
		}
		if !found {
			first, found = tc.Text, true
		}
	}
	return first, console, found
}

// payload returns the primary JSON payload: structuredContent when present,
// otherwise the first text block (as a JSON string when it is not JSON).
func payload(res *mcp.CallToolResult) (json.RawMessage, string, bool) {
	first, console, found := texts(res)
	if res.StructuredContent != nil {
		if data, err := json.Marshal(res.StructuredContent); err == nil {
			return data, console, true
		}
	}
	if !found {
		return nil, console, false
	}
	if json.Valid([]byte(first)) {
		return compact(first), console, true
	}
	data, _ := json.Marshal(first)
	return data, console, true
}

// compact removes insignificant whitespace without decoding: a round trip
// through any would turn large integers into float64 and reorder keys.
func compact(s string) json.RawMessage {
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(s)); err != nil {
		return json.RawMessage(s)
	}
	return buf.Bytes()
}

// facadeError recognises {"error": "..."} payloads.
func facadeError(data json.RawMessage) (*output.Error, bool) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) != nil {
		return nil, false
	}
	raw, ok := obj["error"]
	if !ok {
		return nil, false
	}
	var msg string
	if json.Unmarshal(raw, &msg) != nil {
		msg = string(raw)
	}
	e := output.New(output.CodeFacade, msg)
	var code string
	if c, ok := obj["code"]; ok && json.Unmarshal(c, &code) == nil && code != "" {
		e.Message = code + ": " + msg
	}
	return e, true
}

func parseSearch(res *mcp.CallToolResult) *output.Result {
	data, _, ok := payload(res)
	if !ok {
		return output.Fail(output.New(output.CodeProtocol, "search_tools returned no content"))
	}
	var probe map[string]json.RawMessage
	_ = json.Unmarshal(data, &probe)
	if _, hasTools := probe["tools"]; !hasTools {
		if e, isErr := facadeError(data); isErr {
			return output.Fail(e)
		}
	}
	if res.IsError {
		return output.Fail(output.New(output.CodeFacade, string(data)))
	}
	return output.Success(data)
}

type envelope struct {
	OK            *bool             `json:"ok"`
	Partial       bool              `json:"partial"`
	Result        json.RawMessage   `json:"result"`
	Diagnostics   []json.RawMessage `json:"diagnostics"`
	ConsoleOutput string            `json:"console_output"`
	TraceID       string            `json:"trace_id"`
	Trace         json.RawMessage   `json:"trace"`
	Stats         json.RawMessage   `json:"stats"`
}

type diagnostic struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	Location *struct {
		Line   int    `json:"line"`
		Column int    `json:"column"`
		Tool   string `json:"tool"`
	} `json:"location"`
}

// childErrorPrefix starts a child failure whose message embeds the child's own
// Code Mode envelope (an external tool that is itself a Code Mode script).
const childErrorPrefix = "ChildCallError: "

// maxNesting bounds how many embedded envelopes flatten unwraps.
const maxNesting = 8

// flatten unwraps error messages of the form "ChildCallError: {<envelope>}"
// down to the innermost error, which is the only part an agent can act on.
// tool is the innermost child tool named on the way, if any.
func flatten(message string) (flat, tool string) {
	flat = message
	for range maxNesting {
		rest, ok := strings.CutPrefix(flat, childErrorPrefix)
		if !ok || !strings.HasPrefix(strings.TrimSpace(rest), "{") {
			return flat, tool
		}
		env, ok := asEnvelope(json.RawMessage(rest))
		if !ok {
			return flat, tool
		}
		var inner *diagnostic
		for _, raw := range env.Diagnostics {
			var d diagnostic
			if json.Unmarshal(raw, &d) == nil && d.Severity != "warning" {
				inner = &d
				break
			}
		}
		if inner == nil || inner.Message == "" {
			return flat, tool
		}
		if inner.Location != nil && inner.Location.Tool != "" {
			tool = inner.Location.Tool
		}
		flat = inner.Message
	}
	return flat, tool
}

// upstreamUnavailable is the message MetaMCP's per-upstream circuit breaker
// and bulkhead use for a call they rejected without forwarding it.
const upstreamUnavailable = "Upstream MCP is temporarily unavailable"

// tidyDiagnostics flattens embedded envelopes in diagnostic messages and
// drops sandbox stack traces: positions are already in "location".
func tidyDiagnostics(diags []json.RawMessage) {
	for i, raw := range diags {
		var d map[string]any
		if json.Unmarshal(raw, &d) != nil {
			continue
		}
		changed := false
		if msg, ok := d["message"].(string); ok {
			if flat, _ := flatten(msg); flat != msg {
				d["message"], changed = flat, true
			}
		}
		if _, ok := d["stack"]; ok {
			delete(d, "stack")
			changed = true
		}
		if !changed {
			continue
		}
		if data, err := json.Marshal(d); err == nil {
			diags[i] = data
		}
	}
}

// asEnvelope reports whether data is a Code Mode diagnostic envelope.
func asEnvelope(data json.RawMessage) (*envelope, bool) {
	var env envelope
	if json.Unmarshal(data, &env) != nil {
		return nil, false
	}
	if env.OK == nil || env.Diagnostics == nil || env.TraceID == "" || env.Stats == nil {
		return nil, false
	}
	return &env, true
}

func codeModeMeta(res *mcp.CallToolResult) *output.Meta {
	raw, ok := res.Meta[codeModeMetaKey]
	if !ok {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var m output.Meta
	if json.Unmarshal(data, &m) != nil {
		return nil
	}
	return &m
}

func parseExecute(res *mcp.CallToolResult) *output.Result {
	meta := codeModeMeta(res)
	data, console, ok := payload(res)
	if !ok {
		if meta == nil {
			return output.Fail(output.New(output.CodeProtocol, "execute returned no content"))
		}
		data = json.RawMessage("null")
	}

	if env, isEnv := asEnvelope(data); isEnv {
		return fromEnvelope(env, meta, console)
	}
	// Without Code Mode metadata, an {"error": ...} payload is a facade-level
	// failure (bad arguments, internal error). With it, the object is simply
	// what the script returned.
	if meta == nil {
		if e, isErr := facadeError(data); isErr {
			return output.Fail(e)
		}
	}
	if res.IsError {
		return output.Fail(output.New(output.CodeFacade, string(data)))
	}
	r := output.Success(data)
	r.Console = console
	r.Meta = meta
	return r
}

func fromEnvelope(env *envelope, meta *output.Meta, console string) *output.Result {
	if env.ConsoleOutput != "" {
		console = env.ConsoleOutput
	}
	if meta == nil {
		meta = &output.Meta{TraceID: env.TraceID, Partial: env.Partial}
	}
	var warnings []json.RawMessage
	var firstErr *diagnostic
	for _, raw := range env.Diagnostics {
		var d diagnostic
		_ = json.Unmarshal(raw, &d)
		if d.Severity == "warning" {
			warnings = append(warnings, raw)
			continue
		}
		if firstErr == nil {
			firstErr = &d
		}
	}

	if *env.OK {
		data := env.Result
		if len(data) == 0 {
			data = json.RawMessage("null")
		}
		r := output.Success(data)
		r.Console, r.Meta, r.Warnings = console, meta, warnings
		return r
	}

	code, message, tool := "E_RUNTIME", "execution failed", ""
	if firstErr != nil {
		code, message = firstErr.Code, firstErr.Message
		if firstErr.Location != nil {
			tool = firstErr.Location.Tool
		}
	}
	if flat, inner := flatten(message); flat != message {
		message = flat
		if inner != "" {
			tool = inner
		}
	}
	switch {
	case code == "E_RUNTIME" && strings.Contains(message, "pool is at capacity"):
		code = output.CodeCapacity
	case strings.Contains(message, upstreamUnavailable):
		code = output.CodeUpstreamUnavailable
	}
	tidyDiagnostics(env.Diagnostics)
	e := output.New(code, message)
	e.TraceID = env.TraceID
	e.Tool = tool
	if firstErr != nil && firstErr.Location != nil {
		e.Line, e.Column = firstErr.Location.Line, firstErr.Location.Column
	}
	if env.Partial {
		e.Hint = strings.TrimSpace(e.Hint + " Some child tool calls completed before the failure (partial=true); see trace before re-running.")
	}
	return &output.Result{
		OK:          false,
		Error:       e,
		Console:     console,
		Meta:        meta,
		Diagnostics: env.Diagnostics,
		Trace:       env.Trace,
	}
}
