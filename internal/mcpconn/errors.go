package mcpconn

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"

	"github.com/ZeroPointSix/metamcp-cli/internal/output"
)

// codeRejectedByTransport is go-sdk's internal jsonrpc2.ErrRejected code.
const codeRejectedByTransport = -32005

// classify maps a transport or protocol error onto an mcli error code.
// connecting reports whether the failure happened during initialize, before
// any tool could have run.
func classify(ctx context.Context, err error, rec *statusRecorder, connecting bool) *output.Error {
	var e *output.Error
	if errors.As(err, &e) {
		return e
	}
	switch ctx.Err() {
	case context.DeadlineExceeded:
		return output.Newf(output.CodeTimeout, "request timed out: %v", err)
	case context.Canceled:
		return output.Newf(output.CodeCancelled, "request cancelled: %v", err)
	}

	status := 0
	if rec != nil {
		status = int(rec.status.Load())
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return output.Newf(output.CodeAuth, "endpoint rejected the API key (HTTP %d)", status)
	case status == http.StatusTooManyRequests:
		return output.New(output.CodeRateLimited, "endpoint rate limit exceeded (HTTP 429)")
	case status == http.StatusNotFound && connecting:
		return output.New(output.CodeConfig, "endpoint not found (HTTP 404): check the URL, it should look like https://host/metamcp/<endpoint>/mcp")
	case status >= 500:
		return output.Newf(output.CodeServer, "server error (HTTP %d): %v", status, err)
	case status >= 400:
		return output.Newf(output.CodeProtocol, "unexpected HTTP %d: %v", status, err)
	}

	// go-sdk wraps transport failures (request never reached the server) in
	// a JSON-RPC error with this code; those are network errors, not protocol.
	var wire *jsonrpc.Error
	if errors.As(err, &wire) && wire.Code != codeRejectedByTransport {
		return output.Newf(output.CodeProtocol, "JSON-RPC error %d: %s", wire.Code, wire.Message)
	}
	if connecting {
		e := output.Newf(output.CodeNetwork, "cannot reach endpoint: %v", err)
		// Nothing ran yet; the generic exit-4 hint would be misleading.
		e.Hint = "Check the URL and network connectivity."
		return e
	}
	return output.New(output.CodeNetwork, fmt.Sprintf("connection failed: %v", err))
}
