// Package cli wires the mcli commands: config, search, execute, the catalog
// shortcuts tools and schema, doctor and version, plus the
// troubleshooting `daemon status|stop` and the hidden `__daemon` entry point.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/ZeroPointSix/metamcp-cli/internal/config"
	"github.com/ZeroPointSix/metamcp-cli/internal/output"
	"github.com/ZeroPointSix/metamcp-cli/internal/version"
)

const rootLong = `mcli gives agents and scripts access to a MetaMCP endpoint from the shell.

Two commands do the work:
  search    find upstream tools (returns tool cards with TypeScript signatures)
  execute   run a TypeScript/JavaScript script that calls those tools on the server

Shortcuts and checks:
  tools     list tool names per server from the catalog (seconds; search takes ~20 s)
  schema    print a tool's full input/output schema
  doctor    check connectivity, auth, server version and which servers the
            endpoint exposes (--expect github,VPS fails if one is missing)
  version   print the version; --check compares with the latest release

Setup (once):
  mcli config set --url https://host/metamcp/<endpoint>/mcp
  printf '%s' "$KEY" | mcli config set --api-key-stdin
  (or set MCLI_URL and MCLI_API_KEY)

Typical flow:
  mcli search "github pull request"
  mcli execute task.ts
  mcli execute - <<'EOF'
  const prs = await tools.github.list_pull_requests({ owner: "o", repo: "r" });
  return prs;
  EOF

Script contract (execute):
  - The file is a function body: use top-level await and "return" the result.
  - Call tools via tools.<server>.<tool>(args) or tools.call("server__tool", args);
    read a tool's full input schema with "mcli schema server__tool" or
    tools.list({ names: [...], schema: true }).
  - Copy tool names exactly from search/tools output. Many contain "-" (VPS__run-script),
    which needs bracket notation: tools.VPS["run-script"](args).
  - tools.<server> is a lazy proxy: Object.keys(tools.VPS) is empty. Enumerate a
    server's tools with tools.list({ category: "VPS" }) or "mcli tools --category VPS".
  - No import/export, npm packages, filesystem, network or timers. Types are
    stripped locally; the script runs in MetaMCP's sandbox, not on this machine.
  - console.log output is returned in the "console" field.

Output: stdout carries exactly one JSON object, {"ok": true, "data": ...} or
{"ok": false, "error": {"code", "message", "hint", ...}}. Diagnostics go to stderr.

Exit codes:
  0 success
  1 the script or a tool failed (E_RUNTIME, E_TOOL_NOT_FOUND, ...)
  2 usage, configuration or local script check failed
  3 authentication failed
  4 timeout or network failure: the outcome is UNKNOWN. Inspect the target
    state before retrying; never blindly re-run a write. This includes
    E_NETWORK/E_SERVER/E_DAEMON while the MetaMCP server restarts or deploys.
  5 a server limit was hit (call budget, output size, rate limit, busy)

A background daemon keeps one MCP session alive between commands and exits
after 10 idle minutes (MCLI_DAEMON_IDLE). --no-daemon or MCLI_NO_DAEMON=1 runs
each command in-process instead.`

type app struct {
	stdin          io.Reader
	stdout, stderr io.Writer

	flagURL  string
	noDaemon bool

	// result is set by commands that produce JSON output.
	result *output.Result
	// exit overrides the exit code for commands without a JSON result.
	exit int
}

// Main runs mcli and returns the process exit code.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	a := &app{stdin: stdin, stdout: stdout, stderr: stderr}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := a.rootCommand()
	root.SetArgs(args)
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	err := root.ExecuteContext(ctx)

	if err != nil && a.result == nil {
		var oe *output.Error
		if !errors.As(err, &oe) {
			// Cobra parse errors: unknown command, bad flag, wrong arg count.
			oe = output.New(output.CodeUsage, err.Error())
			oe.Hint = "Run `mcli --help` or `mcli <command> --help`."
		}
		a.result = output.Fail(oe)
	}
	if a.result == nil {
		return a.exit
	}
	if !a.result.OK && a.result.Error != nil {
		fmt.Fprintf(stderr, "mcli: %s\n", a.result.Error.Error())
	}
	if err := output.Print(stdout, a.result); err != nil {
		return output.ExitFailure
	}
	return a.result.ExitCode()
}

func (a *app) rootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "mcli",
		Short:         "Search and call MetaMCP tools from the command line",
		Long:          rootLong,
		Version:       version.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		// Bare `mcli` shows help.
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		e := output.New(output.CodeUsage, err.Error())
		e.Hint = "Run `mcli <command> --help`."
		return e
	})
	pf := root.PersistentFlags()
	pf.StringVar(&a.flagURL, "url", "", "endpoint URL for this invocation (overrides MCLI_URL and the config file)")
	pf.BoolVar(&a.noDaemon, "no-daemon", false, "run in-process without the background daemon (same as MCLI_NO_DAEMON=1)")

	root.AddCommand(a.configCommand(), a.searchCommand(), a.executeCommand(), a.toolsCommand(), a.schemaCommand(),
		a.doctorCommand(), a.versionCommand(), a.daemonCommand(), a.internalDaemonCommand())
	return root
}

// resolve loads the effective configuration and requires URL + key.
func (a *app) resolve() (*config.Resolved, error) {
	cfg, err := config.Resolve(a.flagURL)
	if err != nil {
		return nil, err
	}
	if err := cfg.Require(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (a *app) daemonDisabled() bool {
	if a.noDaemon {
		return true
	}
	v, err := strconv.ParseBool(os.Getenv(config.EnvNoDaemon))
	return err == nil && v
}

// setResult records the JSON result that Main prints to stdout.
func (a *app) setResult(r *output.Result) error {
	a.result = r
	return nil
}
