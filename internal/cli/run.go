package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ZeroPointSix/metamcp-cli/internal/config"
	"github.com/ZeroPointSix/metamcp-cli/internal/ipc"
	"github.com/ZeroPointSix/metamcp-cli/internal/mcpconn"
	"github.com/ZeroPointSix/metamcp-cli/internal/ops"
	"github.com/ZeroPointSix/metamcp-cli/internal/output"
	"github.com/ZeroPointSix/metamcp-cli/internal/spawn"
)

// ipcSlack lets the daemon report its own timeout before the CLI gives up.
const ipcSlack = 5 * time.Second

// run executes op via the daemon, falling back to direct mode when no daemon
// can be reached. The fallback only happens when the request was never
// delivered: re-running an execute whose outcome is unknown could repeat a write.
func (a *app) run(ctx context.Context, cfg *config.Resolved, op string, args any, timeoutMS int) *output.Result {
	if !a.daemonDisabled() {
		res, err := a.viaDaemon(ctx, cfg, op, args, timeoutMS)
		switch {
		case err == nil:
			return res
		case errors.Is(err, ipc.ErrUnavailable):
			fmt.Fprintf(a.stderr, "mcli: daemon unavailable, running in-process: %v\n", err)
		case errors.Is(err, ipc.ErrBroken) && op == ops.OpSearch:
			fmt.Fprintf(a.stderr, "mcli: daemon connection lost, retrying search in-process: %v\n", err)
		case errors.Is(err, context.DeadlineExceeded):
			return output.Fail(output.New(output.CodeTimeout, "timed out waiting for the daemon"))
		case errors.Is(err, context.Canceled):
			return output.Fail(output.New(output.CodeCancelled, "cancelled"))
		default:
			return output.Fail(output.Newf(output.CodeDaemon, "%v", err))
		}
	}
	return direct(ctx, cfg, op, args, timeoutMS)
}

func (a *app) viaDaemon(ctx context.Context, cfg *config.Resolved, op string, args any, timeoutMS int) (*output.Result, error) {
	t, err := spawn.Locate(spawn.ID(cfg.URL, cfg.APIKey))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ipc.ErrUnavailable, err)
	}
	if err := spawn.Ensure(ctx, t, spawn.Credentials{URL: cfg.URL, APIKey: cfg.APIKey}); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	deadline := ops.Deadline(op, timeoutMS) + ipcSlack
	cctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	resp, err := ipc.Call(cctx, t.Endpoint, &ipc.Request{Op: op, Args: raw, TimeoutMS: timeoutMS})
	if err != nil {
		return nil, err
	}
	if len(resp.Result) == 0 {
		if resp.Error != nil {
			return output.Fail(resp.Error), nil
		}
		return nil, fmt.Errorf("%w: empty daemon response", ipc.ErrBroken)
	}
	var res output.Result
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		return nil, fmt.Errorf("%w: bad daemon response: %v", ipc.ErrBroken, err)
	}
	return &res, nil
}

// direct runs one operation in-process: initialize, call, DELETE.
func direct(ctx context.Context, cfg *config.Resolved, op string, args any, timeoutMS int) *output.Result {
	conn := mcpconn.New(mcpconn.Options{URL: cfg.URL, APIKey: cfg.APIKey})
	// Close sends DELETE even after Ctrl-C/SIGTERM cancelled ctx, so the
	// server does not keep an orphaned session.
	defer conn.Close()
	cctx, cancel := context.WithTimeout(ctx, ops.Deadline(op, timeoutMS))
	defer cancel()
	switch op {
	case ops.OpSearch:
		return ops.Search(cctx, conn, args.(ops.SearchArgs))
	case ops.OpExecute:
		return ops.Execute(cctx, conn, args.(ops.ExecuteArgs))
	}
	return output.Fail(output.Newf(output.CodeInternal, "unknown op %q", op))
}
