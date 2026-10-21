// Package output defines mcli's stdout contract: exactly one JSON object per
// invocation, with error codes that map onto stable process exit codes.
package output

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Exit codes. Agents branch on these, so they are part of the public contract.
const (
	ExitOK        = 0 // success
	ExitFailure   = 1 // the script or tool failed
	ExitUsage     = 2 // bad usage, missing config, or a local pre-check failed
	ExitAuth      = 3 // authentication failed
	ExitUncertain = 4 // timeout or network failure: the outcome is unknown
	ExitLimit     = 5 // a server-side limit was hit
)

// Error codes produced by mcli itself. Code Mode codes from the server
// (E_RUNTIME, E_TIMEOUT, E_CALL_LIMIT, ...) pass through unchanged.
const (
	CodeUsage          = "E_USAGE"
	CodeConfig         = "E_CONFIG"
	CodeScriptSyntax   = "E_SCRIPT_SYNTAX"
	CodeScriptModule   = "E_SCRIPT_MODULE"
	CodeScriptTooLarge = "E_SCRIPT_TOO_LARGE"
	CodeAuth           = "E_AUTH"
	CodeNetwork        = "E_NETWORK"
	CodeTimeout        = "E_TIMEOUT"
	CodeCancelled      = "E_CANCELLED"
	CodeServer         = "E_SERVER"
	CodeRateLimited    = "E_RATE_LIMITED"
	CodeCapacity       = "E_CAPACITY"
	CodeProtocol       = "E_PROTOCOL"
	CodeNotMetaMCP     = "E_NOT_METAMCP"
	CodeFacade         = "E_FACADE"
	CodeDaemon         = "E_DAEMON"
	CodeInternal       = "E_INTERNAL"
	// CodeDoctor means `mcli doctor` found problems; data holds the report.
	CodeDoctor = "E_DOCTOR"
	// CodeToolNotFound is also produced by the server's Code Mode.
	CodeToolNotFound = "E_TOOL_NOT_FOUND"
)

var exitByCode = map[string]int{
	CodeUsage:          ExitUsage,
	CodeConfig:         ExitUsage,
	CodeScriptSyntax:   ExitUsage,
	CodeScriptModule:   ExitUsage,
	CodeScriptTooLarge: ExitUsage,
	CodeNotMetaMCP:     ExitUsage,

	CodeAuth: ExitAuth,

	CodeNetwork:       ExitUncertain,
	CodeTimeout:       ExitUncertain,
	CodeCancelled:     ExitUncertain,
	CodeServer:        ExitUncertain,
	CodeDaemon:        ExitUncertain,
	"E_CHILD_TIMEOUT": ExitUncertain,

	CodeRateLimited:         ExitLimit,
	CodeCapacity:            ExitLimit,
	"E_CALL_LIMIT":          ExitLimit,
	"E_OUTPUT_LIMIT":        ExitLimit,
	"E_MEMORY_LIMIT":        ExitLimit,
	"E_TOOL_LIST_TOO_LARGE": ExitLimit,
}

// ExitFor maps an error code to its exit code. Unknown codes are failures.
func ExitFor(code string) int {
	if exit, ok := exitByCode[code]; ok {
		return exit
	}
	return ExitFailure
}

// Hints tell an agent what to do next. Exit 4 matters most: never blindly
// re-run a write whose outcome is unknown.
const (
	HintUncertain = "The outcome is unknown: the operation may or may not have run on the server. Inspect the target state before retrying; never blindly re-run a write."
	HintConfig    = "Run `mcli config set --url <endpoint-url>` and `printf '%s' \"$KEY\" | mcli config set --api-key-stdin`, or set MCLI_URL / MCLI_API_KEY."
	HintAuth      = "The API key was rejected. Update it with `printf '%s' \"$KEY\" | mcli config set --api-key-stdin`."
	HintModule    = "Scripts are function bodies: remove import/export and use the global `tools` object (tools.search / tools.list / tools.call / tools.<server>.<tool>). Use top-level await and `return` the result."
	HintLimit     = "A server limit was hit. Split the work into smaller executes, return less data, or retry later if the server was busy."
)

func defaultHint(code string, exit int) string {
	switch {
	case code == CodeConfig:
		return HintConfig
	case code == CodeAuth:
		return HintAuth
	case code == CodeScriptModule:
		return HintModule
	case exit == ExitUncertain:
		return HintUncertain
	case exit == ExitLimit:
		return HintLimit
	}
	return ""
}

// Error is the structured error carried in every failed result.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	TraceID string `json:"trace_id,omitempty"`
	// Line and Column point into the caller's script file (1-based) when known.
	Line   int    `json:"line,omitempty"`
	Column int    `json:"column,omitempty"`
	Hint   string `json:"hint,omitempty"`
	// Suggestions are existing tool names close to a missing one.
	Suggestions []string `json:"suggestions,omitempty"`
	Exit        int      `json:"exit"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// New builds an Error whose exit code and hint follow from its code.
func New(code, message string) *Error {
	exit := ExitFor(code)
	return &Error{Code: code, Message: message, Exit: exit, Hint: defaultHint(code, exit)}
}

// Newf is New with formatting.
func Newf(code, format string, args ...any) *Error {
	return New(code, fmt.Sprintf(format, args...))
}

// Meta summarises a Code Mode execution (from _meta["metamcp/codeMode"]).
type Meta struct {
	TraceID        string `json:"trace_id,omitempty"`
	Partial        bool   `json:"partial"`
	TerminalReason string `json:"terminal_reason,omitempty"`
	ChildSuccesses int    `json:"child_successes"`
	ChildFailures  int    `json:"child_failures"`
	QueueWaitMS    int    `json:"queue_wait_ms,omitempty"`
}

// Result is the single JSON object mcli writes to stdout.
type Result struct {
	OK bool `json:"ok"`
	// Data is the business payload. On success it is always present, even when
	// it is JSON null (a script that returns nothing).
	Data        json.RawMessage   `json:"data,omitempty"`
	Console     string            `json:"console,omitempty"`
	Warnings    []json.RawMessage `json:"warnings,omitempty"`
	Meta        *Meta             `json:"meta,omitempty"`
	Error       *Error            `json:"error,omitempty"`
	Diagnostics []json.RawMessage `json:"diagnostics,omitempty"`
	Trace       json.RawMessage   `json:"trace,omitempty"`
}

// ExitCode derives the process exit code from the result.
func (r *Result) ExitCode() int {
	if r.OK {
		return ExitOK
	}
	if r.Error != nil {
		return r.Error.Exit
	}
	return ExitFailure
}

// Success wraps an already-encoded payload.
func Success(data json.RawMessage) *Result {
	if len(data) == 0 {
		data = json.RawMessage("null")
	}
	return &Result{OK: true, Data: data}
}

// SuccessValue encodes v as the payload.
func SuccessValue(v any) *Result {
	data, err := json.Marshal(v)
	if err != nil {
		return Fail(err)
	}
	return Success(data)
}

// Fail converts any error into a failed result. Errors that are not *Error
// are internal bugs and map to exit 1.
func Fail(err error) *Result {
	var e *Error
	if !errors.As(err, &e) {
		e = New(CodeInternal, err.Error())
	}
	return &Result{OK: false, Error: e}
}

// Print writes r as one JSON document followed by a newline.
func Print(w io.Writer, r *Result) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
