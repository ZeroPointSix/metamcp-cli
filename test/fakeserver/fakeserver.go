// Package fakeserver is an in-process stand-in for a MetaMCP endpoint, built
// on the go-sdk server. It reproduces the response shapes of MetaMCP's
// search_tools and execute facade tools (including host-safe results that
// never set isError) and lets tests inject failures and observe sessions.
//
// execute does not run JavaScript. Markers in the submitted code select a
// behaviour:
//
//	__SLEEP_MS_<n>__  wait n ms (honouring cancellation)
//	__THROW__         E_RUNTIME envelope located at the marker's line
//	__LIMIT__         E_CALL_LIMIT envelope
//	__PARTIAL__       failed envelope with partial=true
//	__WARN__          warning-bearing success envelope
//	__MISSING_TOOL__  E_TOOL_NOT_FOUND for VPS__run_script (exists as VPS__run-script)
//	__MISSING_TOOL_RUNTIME__  the same child error wrapped in a top-level E_RUNTIME
//	                         (older servers report unknown tools this way)
//	console.log       adds a <console_output> block
//
// The scripts mcli generates itself (they start with "const args = ") are
// answered from Catalog, as tools.list would.
//
// Otherwise it returns {"code_len", "timeout_ms", "note"} as the script result.
package fakeserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Path is the MCP endpoint path served.
const Path = "/metamcp/test/mcp"

// Version is reported in serverInfo.
const Version = "1.0.81-test"

// Catalog is the fake endpoint's tool catalog (logical names).
var Catalog = []string{
	"VPS__exec",
	"VPS__list-profiles",
	"VPS__run-script",
	"github__get_me",
	"github__list_pull_requests",
}

// Server is a fake MetaMCP endpoint.
type Server struct {
	APIKey string
	HTTP   *httptest.Server

	mu       sync.Mutex
	live     map[string]bool
	expired  map[string]bool
	lastCode string
	reauth   map[string]bool

	Created     atomic.Int64 // sessions created (initialize responses)
	Deleted     atomic.Int64 // DELETE requests for live sessions
	Orphans     atomic.Int64 // session-less POSTs that were not initialize
	Searches    atomic.Int64
	Executes    atomic.Int64
	Cancelled   atomic.Int64 // execute handlers that saw ctx cancellation
	inFlight    atomic.Int64
	MaxInFlight atomic.Int64
	// FailStatus, when non-zero, answers every POST with this HTTP status.
	FailStatus atomic.Int32
}

// New starts a fake server requiring apiKey in X-API-Key.
func New(apiKey string) *Server {
	s := &Server{APIKey: apiKey, live: map[string]bool{}, expired: map[string]bool{}, reauth: map[string]bool{}}
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "fake-metamcp", Version: Version}, nil)
	mcpServer.AddTool(&mcp.Tool{Name: "search_tools", InputSchema: objectSchema()}, s.search)
	mcpServer.AddTool(&mcp.Tool{Name: "execute", InputSchema: objectSchema()}, s.execute)
	mcpServer.AddTool(&mcp.Tool{Name: "external_tools", InputSchema: objectSchema()}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return text(`{"tools":[]}`), nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, &mcp.StreamableHTTPOptions{JSONResponse: true})

	mux := http.NewServeMux()
	mux.Handle(Path, s.wrap(handler))
	s.HTTP = httptest.NewServer(mux)
	return s
}

// URL is the endpoint URL.
func (s *Server) URL() string { return s.HTTP.URL + Path }

// Close stops the server.
func (s *Server) Close() { s.HTTP.Close() }

// ExpireSessions makes every live session answer 404, like a server-side
// SESSION_LIFETIME expiry.
func (s *Server) ExpireSessions() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.live {
		s.expired[id] = true
		delete(s.live, id)
	}
}

// LiveSessions counts sessions neither deleted nor expired.
func (s *Server) LiveSessions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.live)
}

// RequireReauth makes calls to these tools fail like an upstream whose OAuth
// grant needs renewing.
func (s *Server) RequireReauth(names ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range names {
		s.reauth[n] = true
	}
}

// LastCode returns the most recent execute code.
func (s *Server) LastCode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastCode
}

func objectSchema() any { return map[string]any{"type": "object"} }

func (s *Server) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != s.APIKey {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"invalid_api_key"}`)
			return
		}
		if st := s.FailStatus.Load(); st != 0 && r.Method == http.MethodPost {
			w.WriteHeader(int(st))
			io.WriteString(w, `{"error":"injected"}`)
			return
		}
		sid := r.Header.Get("Mcp-Session-Id")
		if sid != "" {
			s.mu.Lock()
			gone := s.expired[sid]
			if r.Method == http.MethodDelete && s.live[sid] {
				delete(s.live, sid)
				s.Deleted.Add(1)
			}
			s.mu.Unlock()
			if gone {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				io.WriteString(w, `{"error":"Session not found"}`)
				return
			}
		} else if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			var msg struct {
				Method string `json:"method"`
			}
			_ = json.Unmarshal(body, &msg)
			if msg.Method != "initialize" {
				s.Orphans.Add(1)
			}
		}
		rec := &headerSpy{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if sid == "" && rec.sessionID != "" {
			s.mu.Lock()
			s.live[rec.sessionID] = true
			s.mu.Unlock()
			s.Created.Add(1)
		}
	})
}

type headerSpy struct {
	http.ResponseWriter
	sessionID string
}

func (h *headerSpy) WriteHeader(code int) {
	h.sessionID = h.Header().Get("Mcp-Session-Id")
	h.ResponseWriter.WriteHeader(code)
}

func (h *headerSpy) Write(b []byte) (int, error) {
	if h.sessionID == "" {
		h.sessionID = h.Header().Get("Mcp-Session-Id")
	}
	return h.ResponseWriter.Write(b)
}

func (h *headerSpy) Flush() {
	if f, ok := h.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func asText(v any) *mcp.CallToolResult {
	data, _ := json.MarshalIndent(v, "", "  ")
	return text(string(data))
}

func (s *Server) search(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	s.Searches.Add(1)
	var args map[string]any
	_ = json.Unmarshal(req.Params.Arguments, &args)
	if args["query"] == "__error__" {
		return asText(map[string]any{"error": "search backend unavailable"}), nil
	}
	return asText(map[string]any{
		"tools": []map[string]any{{
			"name":         "github__list_pull_requests",
			"display_name": "list_pull_requests",
			"category":     "github",
			"description":  "List pull requests in a GitHub repository.",
			"signature":    "tools.github.list_pull_requests(args: { owner: string; repo: string }): Promise<unknown>",
		}},
		"count":       1,
		"total":       1,
		"next_cursor": nil,
		"warnings":    []string{},
		"echo":        args,
	}), nil
}

var sleepMarker = regexp.MustCompile(`__SLEEP_MS_(\d+)__`)

func (s *Server) execute(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	s.Executes.Add(1)
	n := s.inFlight.Add(1)
	defer s.inFlight.Add(-1)
	for {
		m := s.MaxInFlight.Load()
		if n <= m || s.MaxInFlight.CompareAndSwap(m, n) {
			break
		}
	}

	var args struct {
		Code      string `json:"code"`
		TimeoutMS int    `json:"timeout_ms"`
		Note      string `json:"note"`
	}
	_ = json.Unmarshal(req.Params.Arguments, &args)
	s.mu.Lock()
	s.lastCode = args.Code
	s.mu.Unlock()
	traceID := fmt.Sprintf("trace_%d", time.Now().UnixNano())

	if m := sleepMarker.FindStringSubmatch(args.Code); m != nil {
		ms, _ := strconv.Atoi(m[1])
		select {
		case <-time.After(time.Duration(ms) * time.Millisecond):
		case <-ctx.Done():
			s.Cancelled.Add(1)
			return nil, ctx.Err()
		}
	}

	meta := func(partial bool, reason string) mcp.Meta {
		return mcp.Meta{"metamcp/codeMode": map[string]any{
			"partial": partial, "terminal_reason": reason, "child_successes": 0, "child_failures": 0, "trace_id": traceID,
		}}
	}
	fail := func(code, message string, line, col int, partial bool) *mcp.CallToolResult {
		diag := map[string]any{"severity": "error", "code": code, "message": message, "source": "execute"}
		if line > 0 {
			diag["location"] = map[string]any{"line": line, "column": col}
		}
		res := asText(map[string]any{
			"ok": false, "partial": partial, "exit_code": 1, "result": nil,
			"diagnostics": []any{diag}, "trace_id": traceID, "trace": []any{},
			"stats": map[string]any{"duration_ms": 1, "child_calls": 0, "operations": 0, "child_failures": 0, "output_bytes": 0},
		})
		res.Meta = meta(partial, code)
		return res
	}

	if rest, ok := strings.CutPrefix(args.Code, "const args = "); ok {
		line, body, _ := strings.Cut(rest, "\n")
		var in map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSuffix(line, ";")), &in); err != nil {
			return fail("E_RUNTIME", "bad generated script: "+err.Error(), 0, 0, false), nil
		}
		res := asText(s.catalogScript(body, in))
		res.Meta = meta(false, "success")
		return res, nil
	}

	switch {
	case strings.Contains(args.Code, "__MISSING_TOOL__") || strings.Contains(args.Code, "__MISSING_TOOL_RUNTIME__"):
		code := "E_TOOL_NOT_FOUND"
		if strings.Contains(args.Code, "__MISSING_TOOL_RUNTIME__") {
			code = "E_RUNTIME"
		}
		res := fail(code, "ChildCallError: "+unknownTool("VPS__run_script"), 1, 1, false)
		var env map[string]any
		_ = json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &env)
		env["diagnostics"].([]any)[0].(map[string]any)["location"].(map[string]any)["tool"] = "VPS__run_script"
		data, _ := json.Marshal(env)
		res.Content[0] = &mcp.TextContent{Text: string(data)}
		return res, nil
	case strings.Contains(args.Code, "__THROW__"):
		for i, line := range strings.Split(args.Code, "\n") {
			if col := strings.Index(line, "__THROW__"); col >= 0 {
				return fail("E_RUNTIME", "Error: __THROW__", i+1, col+1, false), nil
			}
		}
	case strings.Contains(args.Code, "__LIMIT__"):
		return fail("E_CALL_LIMIT", "operation budget exhausted", 0, 0, false), nil
	case strings.Contains(args.Code, "__PARTIAL__"):
		return fail("E_RUNTIME", "Error: second call failed", 0, 0, true), nil
	case strings.Contains(args.Code, "__WARN__"):
		res := asText(map[string]any{
			"ok": true, "partial": false, "exit_code": 0, "result": map[string]any{"warned": true},
			"diagnostics":    []any{map[string]any{"severity": "warning", "code": "W_TOOL_LIST_DEGRADED", "message": "catalog degraded", "source": "execute"}},
			"console_output": "from envelope", "trace_id": traceID, "trace": []any{},
			"stats": map[string]any{"duration_ms": 1, "child_calls": 0, "operations": 1, "child_failures": 0, "output_bytes": 0},
		})
		res.Meta = meta(false, "success")
		return res, nil
	}

	res := asText(map[string]any{"code_len": len(args.Code), "timeout_ms": args.TimeoutMS, "note": args.Note})
	if strings.Contains(args.Code, "console.log") {
		res.Content = append(res.Content, &mcp.TextContent{Text: "<console_output>\nhello\n\n</console_output>"})
	}
	res.Meta = meta(false, "success")
	return res, nil
}

func unknownTool(name string) string {
	data, _ := json.Marshal(map[string]any{
		"error":       "Unknown tool: " + name + ". The server is synced but this tool is not registered.",
		"error_code":  "UNKNOWN_TOOL",
		"error_class": "unknown_tool",
	})
	return string(data)
}

func category(name string) string {
	c, _, _ := strings.Cut(name, "__")
	return c
}

func strs(v any) []string {
	var out []string
	list, _ := v.([]any)
	for _, x := range list {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func inCatalog(name string) bool {
	for _, n := range Catalog {
		if n == name {
			return true
		}
	}
	return false
}

func entry(name string, schema bool) map[string]any {
	e := map[string]any{"name": name, "category": category(name), "summary": "Summary of " + name}
	if schema {
		e["inputSchema"] = map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}}
		e["declaration"] = "tools." + category(name) + ".x(args: {}): Promise<unknown>"
	}
	return e
}

// catalogScript answers the scripts mcli generates, recognised by their body.
func (s *Server) catalogScript(body string, args map[string]any) any {
	switch {
	case strings.Contains(body, "capabilities"): // doctor
		cats := map[string]int{}
		for _, n := range Catalog {
			cats[category(n)]++
		}
		probes := []any{}
		for _, raw := range args["probes"].([]any) {
			name := raw.(map[string]any)["name"].(string)
			s.mu.Lock()
			reauth := s.reauth[name]
			s.mu.Unlock()
			switch {
			case reauth:
				msg, _ := json.Marshal(map[string]any{
					"error": "Upstream server github requires re-authorization.", "error_code": "AUTH_REAUTHORIZATION_REQUIRED",
					"error_class": "auth_invalid", "recovery": "Authorize this server again in MetaMCP, then retry.",
					"authorization_url": "https://example.test/authorize/github",
				})
				probes = append(probes, map[string]any{"tool": name, "ok": false, "ms": 3, "code": "E_RUNTIME", "message": string(msg)})
			case inCatalog(name):
				probes = append(probes, map[string]any{"tool": name, "ok": true, "ms": 2})
			default:
				probes = append(probes, map[string]any{"tool": name, "ok": false, "ms": 1, "code": "E_TOOL_NOT_FOUND", "message": unknownTool(name)})
			}
		}
		return map[string]any{
			"tool_count": len(Catalog), "categories": cats, "probes": probes,
			"capabilities": map[string]any{"tools_search": true, "list_declaration": true},
		}
	case strings.Contains(body, "args.variants"): // schema
		items, existing := []any{}, []string{}
		for _, n := range strs(args["names"]) {
			if inCatalog(n) {
				items = append(items, entry(n, true))
				continue
			}
			variants, _ := args["variants"].(map[string]any)
			for _, v := range strs(variants[n]) {
				if inCatalog(v) {
					existing = append(existing, v)
				}
			}
		}
		return map[string]any{"items": items, "existing": existing, "declaration": true}
	case strings.Contains(body, "args.declaration"): // tools
		cat, _ := args["category"].(string)
		items, cats := []any{}, []string{}
		for _, n := range Catalog {
			if cat == "" || category(n) == cat {
				items = append(items, entry(n, false))
			}
			if c := category(n); len(cats) == 0 || cats[len(cats)-1] != c {
				cats = append(cats, c)
			}
		}
		if cat != "" && len(items) == 0 {
			return map[string]any{"items": items, "categories": cats}
		}
		return map[string]any{"items": items, "declaration": args["declaration"]}
	default: // name lookup
		found := []string{}
		for _, n := range strs(args["names"]) {
			if inCatalog(n) {
				found = append(found, n)
			}
		}
		return found
	}
}
