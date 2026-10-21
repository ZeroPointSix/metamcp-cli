package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ZeroPointSix/metamcp-cli/internal/config"
	"github.com/ZeroPointSix/metamcp-cli/internal/daemon"
	"github.com/ZeroPointSix/metamcp-cli/internal/ipc"
	"github.com/ZeroPointSix/metamcp-cli/internal/mcpconn"
	"github.com/ZeroPointSix/metamcp-cli/internal/ops"
	"github.com/ZeroPointSix/metamcp-cli/internal/output"
	"github.com/ZeroPointSix/metamcp-cli/internal/script"
	"github.com/ZeroPointSix/metamcp-cli/internal/spawn"
)

// verifyTimeout covers initialize + tools/list + DELETE, which took ~9s on a
// loaded production endpoint.
const verifyTimeout = 60 * time.Second

func (a *app) configCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show or change the endpoint URL and API key",
	}
	var (
		setURL   string
		keyStdin bool
		noVerify bool
	)
	set := &cobra.Command{
		Use:   "set",
		Short: "Save the endpoint URL and/or API key (the key is read from stdin)",
		Long: `Save the endpoint URL and/or API key to the config file (owner-only permissions).

The API key is only accepted on stdin so it never lands in shell history or
the process list:
  mcli config set --url https://host/metamcp/<endpoint>/mcp
  printf '%s' "$KEY" | mcli config set --api-key-stdin

Once both are known, the endpoint is verified (initialize + tools/list must
show search_tools and execute). Use --no-verify to skip that check.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if setURL == "" && !keyStdin {
				return output.New(output.CodeUsage, "nothing to set: pass --url and/or --api-key-stdin")
			}
			f, err := config.Load()
			if err != nil {
				return a.setResult(output.Fail(err))
			}
			if setURL != "" {
				if err := config.ValidateURL(setURL); err != nil {
					return a.setResult(output.Fail(err))
				}
				f.URL = setURL
			}
			if keyStdin {
				key, err := a.readKey()
				if err != nil {
					return a.setResult(output.Fail(err))
				}
				f.APIKey = key
			}

			key := f.APIKey
			if key == "" {
				key = os.Getenv(config.EnvAPIKey)
			}
			data := map[string]any{"url": f.URL, "api_key": config.MaskKey(f.APIKey), "verified": false}
			if !noVerify && f.URL != "" && key != "" {
				tools, err := verify(cmd.Context(), f.URL, key)
				if err != nil {
					res := output.Fail(err)
					res.Error.Hint = strings.TrimSpace(res.Error.Hint + " Nothing was saved; pass --no-verify to save anyway.")
					return a.setResult(res)
				}
				data["verified"] = true
				data["tools"] = tools
			} else if !noVerify {
				fmt.Fprintln(a.stderr, "mcli: saved without verification (set both URL and API key to verify)")
			}
			path, err := config.Save(f)
			if err != nil {
				return a.setResult(output.Fail(err))
			}
			data["path"] = path
			return a.setResult(output.SuccessValue(data))
		},
	}
	set.Flags().StringVar(&setURL, "url", "", "MCP endpoint URL, e.g. https://host/metamcp/<endpoint>/mcp")
	set.Flags().BoolVar(&keyStdin, "api-key-stdin", false, "read the API key from stdin")
	set.Flags().BoolVar(&noVerify, "no-verify", false, "do not contact the endpoint")

	show := &cobra.Command{
		Use:   "show",
		Short: "Show the effective configuration (the key is masked)",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			cfg, err := config.Resolve(a.flagURL)
			if err != nil {
				return a.setResult(output.Fail(err))
			}
			path, _ := config.Path()
			return a.setResult(output.SuccessValue(map[string]any{
				"path":           path,
				"url":            cfg.URL,
				"url_source":     cfg.URLSource,
				"api_key":        config.MaskKey(cfg.APIKey),
				"api_key_source": cfg.KeySource,
			}))
		},
	}
	cmd.AddCommand(set, show)
	return cmd
}

// readKey reads the API key from stdin, trimming the trailing newline.
func (a *app) readKey() (string, error) {
	if f, ok := a.stdin.(*os.File); ok {
		if st, err := f.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 {
			fmt.Fprint(a.stderr, "Paste the API key and press Enter: ")
			line, err := bufio.NewReader(f).ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				return "", output.Newf(output.CodeUsage, "read API key: %v", err)
			}
			return checkKey(line)
		}
	}
	data, err := io.ReadAll(io.LimitReader(a.stdin, 64<<10))
	if err != nil {
		return "", output.Newf(output.CodeUsage, "read API key: %v", err)
	}
	return checkKey(string(data))
}

func checkKey(s string) (string, error) {
	key := strings.TrimSpace(s)
	if key == "" {
		return "", output.New(output.CodeUsage, "empty API key on stdin")
	}
	if strings.ContainsAny(key, " \t\r\n") {
		return "", output.New(output.CodeUsage, "API key must be a single token")
	}
	return key, nil
}

// verify checks that url is a MetaMCP endpoint accepting key.
func verify(ctx context.Context, url, key string) ([]string, error) {
	conn := mcpconn.New(mcpconn.Options{URL: url, APIKey: key})
	defer conn.Close()
	return verifyConn(ctx, conn)
}

// verifyConn connects conn and checks that it exposes the facade tools.
func verifyConn(ctx context.Context, conn *mcpconn.Conn) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	names, err := conn.ListToolNames(ctx)
	if err != nil {
		return nil, err
	}
	for _, want := range []string{mcpconn.ToolSearch, mcpconn.ToolExecute} {
		if !slices.Contains(names, want) {
			return nil, output.Newf(output.CodeNotMetaMCP,
				"endpoint does not expose %q; is this a MetaMCP endpoint URL (…/metamcp/<endpoint>/mcp) with facade tools enabled?", want)
		}
	}
	return names, nil
}

func (a *app) searchCommand() *cobra.Command {
	var args ops.SearchArgs
	cmd := &cobra.Command{
		Use:   "search <query...>",
		Short: "Find tools by describing the capability you need",
		Long: `Search the endpoint's upstream tools (MetaMCP search_tools).

Returns tool cards: name, category, description and a TypeScript call
signature to use inside "mcli execute". Prefer one broad query ("github")
over several narrow ones. When "signature_truncated" is true, read the full
input schema inside a script with tools.list({ names: [...], schema: true })
before calling the tool; do not guess omitted arguments.`,
		Example: `  mcli search github pull request
  mcli search "send email" --limit 10
  mcli search issues --category linear`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, words []string) error {
			args.Query = strings.TrimSpace(strings.Join(words, " "))
			if args.Query == "" {
				return output.New(output.CodeUsage, "query is empty")
			}
			switch args.Mode {
			case "", "auto", "keyword", "semantic":
			default:
				return output.Newf(output.CodeUsage, "--mode must be auto, keyword or semantic (got %q)", args.Mode)
			}
			if args.Limit < 0 {
				return output.New(output.CodeUsage, "--limit must be positive")
			}
			cfg, err := a.resolve()
			if err != nil {
				return a.setResult(output.Fail(err))
			}
			return a.setResult(a.run(cmd.Context(), cfg, ops.OpSearch, args, 0))
		},
	}
	f := cmd.Flags()
	f.IntVar(&args.Limit, "limit", 0, "maximum results (server default 5)")
	f.StringVar(&args.Category, "category", "", "restrict to one MCP server category")
	f.StringVar(&args.Mode, "mode", "", "auto, keyword or semantic (server default auto)")
	f.StringVar(&args.Cursor, "cursor", "", "pagination cursor from a previous next_cursor")
	f.StringVar(&args.Note, "note", "", "one-line intent recorded in the audit log (no secrets)")
	return cmd
}

func (a *app) executeCommand() *cobra.Command {
	var (
		timeout time.Duration
		note    string
	)
	cmd := &cobra.Command{
		Use:   "execute <file.ts|file.js|->",
		Short: "Run a TypeScript/JavaScript tool script on the server",
		Long: `Run a script in MetaMCP's Code Mode sandbox. "-" reads the script from stdin.

The script is a function body: top-level await, "return" the result. Tools are
reached only through the global "tools" object:
  await tools.<server>.<tool>(args)        // signature from "mcli search"
  await tools.VPS["run-script"](args)      // names with "-" need brackets
  await tools.call("server__tool", args)
  await tools.list({ names: ["server__tool"], schema: true })  // full input schema
  await tools.list({ category: "github" })                      // browse one server
Copy tool names exactly; on E_TOOL_NOT_FOUND the error lists the spellings
that exist ("suggestions"). tools.<server> is a lazy proxy, so
Object.keys(tools.VPS) is empty; enumerate with tools.list({ category }).
Newer MetaMCP releases also offer tools.search(query) and
tools.list({ declaration: true }) inside scripts; older ones reject them
("mcli doctor" shows which the server supports), so discover tools with
"mcli search", "mcli tools" or "mcli schema" first.
Promise.all, loops and map/filter/reduce work. import/export, npm packages,
filesystem, network and timers are not available. TypeScript types are
stripped locally (single file, no bundling); the source limit is 256 KiB and
the transpiled code must fit the server's 64 KiB limit.

Execution is not atomic: child tool calls may complete before a later failure
("meta.partial": true). On exit code 4 the outcome is unknown; inspect the
target before re-running anything that writes. That includes E_NETWORK,
E_SERVER and E_DAEMON during a MetaMCP restart or deploy: the request may have
run before the server went away. (A session the server merely forgot, HTTP
404, is rebuilt and retried once automatically, because nothing ran.)

Long-running upstream work (for example VPS exec) is bounded by the tool's own
arguments (expire_time_ms / kill_time_ms) and by --timeout; raise both rather
than wrapping the call in a retry loop.`,
		Example: `  mcli execute task.ts
  mcli execute task.ts --timeout 2m --note "triage open PRs"
  mcli execute - <<'EOF'
  const me = await tools.github.get_me({});
  const list = await tools.list({ category: "github" });
  return { login: me.login, githubTools: list.length };
  EOF`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, files []string) error {
			if timeout <= 0 {
				return output.New(output.CodeUsage, "--timeout must be positive")
			}
			src, name, err := script.Read(files[0], a.stdin)
			if err != nil {
				return a.setResult(output.Fail(err))
			}
			prepared, err := script.Prepare(src, name)
			if err != nil {
				return a.setResult(output.Fail(err))
			}
			cfg, err := a.resolve()
			if err != nil {
				return a.setResult(output.Fail(err))
			}
			args := ops.ExecuteArgs{Code: prepared.Code, TimeoutMS: int(timeout / time.Millisecond), Note: note}
			res := a.run(cmd.Context(), cfg, ops.OpExecute, args, args.TimeoutMS)
			remapPositions(res, prepared)
			capabilityHint(res)
			a.suggestToolNames(cmd.Context(), cfg, res)
			return a.setResult(res)
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", ops.DefaultExecuteTimeout, "server-side execution timeout")
	cmd.Flags().StringVar(&note, "note", "", "one-line intent recorded in the audit log (no secrets)")
	return cmd
}

// remapPositions rewrites server-reported line/column (relative to the
// submitted code) to positions in the caller's file.
func remapPositions(res *output.Result, p *script.Prepared) {
	if e := res.Error; e != nil && e.Line > 0 {
		if line, col, ok := p.MapPosition(e.Line, e.Column); ok {
			e.Line, e.Column = line, col
			e.Message = fmt.Sprintf("%s (%s:%d: %s)", e.Message, p.Name, line, strings.TrimSpace(p.SourceLine(line)))
		} else {
			e.Line, e.Column = 0, 0
		}
	}
	for i, raw := range res.Diagnostics {
		var d map[string]any
		if json.Unmarshal(raw, &d) != nil {
			continue
		}
		loc, ok := d["location"].(map[string]any)
		if !ok {
			continue
		}
		line, _ := loc["line"].(float64)
		col, _ := loc["column"].(float64)
		if l, c, ok := p.MapPosition(int(line), int(col)); ok {
			loc["line"], loc["column"] = l, c
		} else {
			delete(loc, "line")
			delete(loc, "column")
		}
		if data, err := json.Marshal(d); err == nil {
			res.Diagnostics[i] = data
		}
	}
}

func (a *app) daemonCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Inspect or stop the background session daemon (troubleshooting)",
	}
	target := func() (*spawn.Target, error) {
		cfg, err := a.resolve()
		if err != nil {
			return nil, err
		}
		return spawn.Locate(spawn.ID(cfg.URL, cfg.APIKey))
	}
	status := &cobra.Command{
		Use:   "status",
		Short: "Show whether a daemon serves the current endpoint and key",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			t, err := target()
			if err != nil {
				return a.setResult(output.Fail(err))
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Second)
			defer cancel()
			resp, err := ipc.Call(ctx, t.Endpoint, &ipc.Request{Op: ipc.OpStatus})
			if err != nil {
				return a.setResult(output.SuccessValue(map[string]any{"running": false, "endpoint": t.Endpoint, "log": t.LogPath}))
			}
			var st map[string]any
			_ = json.Unmarshal(resp.Result, &st)
			if st == nil {
				st = map[string]any{}
			}
			st["running"] = true
			st["endpoint"] = t.Endpoint
			st["log"] = t.LogPath
			return a.setResult(output.SuccessValue(st))
		},
	}
	stop := &cobra.Command{
		Use:   "stop",
		Short: "Stop the daemon (it closes its MCP session first)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			t, err := target()
			if err != nil {
				return a.setResult(output.Fail(err))
			}
			stopped, err := spawn.Stop(cmd.Context(), t)
			if err != nil {
				return a.setResult(output.Fail(output.New(output.CodeDaemon, err.Error())))
			}
			return a.setResult(output.SuccessValue(map[string]any{"stopped": stopped}))
		},
	}
	cmd.AddCommand(status, stop)
	return cmd
}

// internalDaemonCommand is the hidden entry point the CLI spawns.
func (a *app) internalDaemonCommand() *cobra.Command {
	var id string
	cmd := &cobra.Command{
		Use:    "__daemon",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			var creds spawn.Credentials
			if err := json.NewDecoder(a.stdin).Decode(&creds); err != nil {
				return fmt.Errorf("read credentials: %w", err)
			}
			if spawn.ID(creds.URL, creds.APIKey) != id {
				return errors.New("credentials do not match daemon id")
			}
			t, err := spawn.Locate(id)
			if err != nil {
				return err
			}
			idle := daemon.DefaultIdle
			if v := os.Getenv(config.EnvIdle); v != "" {
				if d, err := time.ParseDuration(v); err == nil && d > 0 {
					idle = d
				}
			}
			err = daemon.Run(daemon.Config{Endpoint: t.Endpoint, RunLock: t.RunPath, URL: creds.URL, APIKey: creds.APIKey, Idle: idle})
			if err != nil {
				fmt.Fprintf(a.stderr, "daemon: %v\n", err)
				a.exit = 1
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "daemon id")
	return cmd
}
