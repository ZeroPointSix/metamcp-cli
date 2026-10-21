// Package ops implements the two operations mcli exposes, on top of a
// connection. Both the in-process (direct) path and the daemon run these.
package ops

import (
	"context"
	"time"

	"github.com/ZeroPointSix/metamcp-cli/internal/mcpconn"
	"github.com/ZeroPointSix/metamcp-cli/internal/output"
)

// Operation names, shared with the IPC protocol.
const (
	OpSearch  = "search"
	OpExecute = "execute"
)

// SearchArgs are passed through to the search_tools facade tool.
type SearchArgs struct {
	Query    string `json:"query"`
	Category string `json:"category,omitempty"`
	Mode     string `json:"mode,omitempty"`
	Limit    int    `json:"limit,omitempty"`
	Cursor   string `json:"cursor,omitempty"`
	Note     string `json:"note,omitempty"`
}

// ExecuteArgs are sent to the execute facade tool. async/action are
// deliberately not exposed.
type ExecuteArgs struct {
	Code      string `json:"code"`
	TimeoutMS int    `json:"timeout_ms,omitempty"`
	Note      string `json:"note,omitempty"`
}

// DefaultExecuteTimeout matches the server's synchronous Code Mode default.
const DefaultExecuteTimeout = 30 * time.Second

// transportSlack is added on top of the script timeout so the server, not the
// client, normally reports a timeout (with a trace id and partial results).
const transportSlack = 15 * time.Second

// searchTimeout bounds a search_tools call, including initialize.
const searchTimeout = 60 * time.Second

// Deadline returns how long the client waits for an operation.
func Deadline(op string, timeoutMS int) time.Duration {
	if op == OpExecute {
		d := DefaultExecuteTimeout
		if timeoutMS > 0 {
			d = time.Duration(timeoutMS) * time.Millisecond
		}
		return d + transportSlack
	}
	return searchTimeout
}

// Search runs search_tools.
func Search(ctx context.Context, c *mcpconn.Conn, args SearchArgs) *output.Result {
	res, err := c.CallTool(ctx, mcpconn.ToolSearch, args)
	if err != nil {
		return output.Fail(err)
	}
	return parseSearch(res)
}

// Execute runs a prepared Code Mode program.
func Execute(ctx context.Context, c *mcpconn.Conn, args ExecuteArgs) *output.Result {
	res, err := c.CallTool(ctx, mcpconn.ToolExecute, args)
	if err != nil {
		return output.Fail(err)
	}
	return parseExecute(res)
}
