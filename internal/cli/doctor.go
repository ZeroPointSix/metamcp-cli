package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ZeroPointSix/metamcp-cli/internal/catalog"
	"github.com/ZeroPointSix/metamcp-cli/internal/mcpconn"
	"github.com/ZeroPointSix/metamcp-cli/internal/ops"
	"github.com/ZeroPointSix/metamcp-cli/internal/output"
	"github.com/ZeroPointSix/metamcp-cli/internal/update"
	"github.com/ZeroPointSix/metamcp-cli/internal/version"
)

// updateCheckTimeout keeps an unreachable github.com from slowing doctor.
const updateCheckTimeout = 5 * time.Second

// Probe outcomes reported by doctor.
const (
	probeOK           = "ok"
	probeAuthRequired = "auth_required"
	probeNotFound     = "not_found"
	probeTimeout      = "timeout"
	probeError        = "error"
)

type probeSpec struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

// parseProbe reads "server__tool" or "server__tool={json args}".
func parseProbe(s string) (probeSpec, error) {
	name, raw, hasArgs := strings.Cut(s, "=")
	p := probeSpec{Name: catalog.Normalize(name), Args: map[string]any{}}
	if p.Name == "" {
		return p, output.Newf(output.CodeUsage, "--probe %q: missing tool name", s)
	}
	if hasArgs {
		var args map[string]any
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			return p, output.Newf(output.CodeUsage, "--probe %q: arguments must be a JSON object: %v", s, err)
		}
		// JSON null unmarshals into a nil map without an error; tools expect an
		// object, so treat it like any other non-object argument.
		if args == nil {
			return p, output.Newf(output.CodeUsage, "--probe %q: arguments must be a JSON object", s)
		}
		p.Args = args
	}
	return p, nil
}

type probeReport struct {
	Tool    string `json:"tool"`
	Status  string `json:"status"`
	MS      int64  `json:"ms"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	// From MetaMCP's error contract when the failure carries one.
	ErrorCode        string   `json:"error_code,omitempty"`
	NextAction       string   `json:"next_action,omitempty"`
	AuthorizationURL string   `json:"authorization_url,omitempty"`
	Suggestions      []string `json:"suggestions,omitempty"`
}

// classifyProbe turns a caught child-call failure into a probe outcome.
// Upstream errors carry MetaMCP's error contract as JSON in the message.
func classifyProbe(code, message string) probeReport {
	r := probeReport{Code: code, Message: message, Status: probeError}
	var contract struct {
		Error            string `json:"error"`
		ErrorCode        string `json:"error_code"`
		ErrorClass       string `json:"error_class"`
		NextAction       string `json:"next_action"`
		Recovery         string `json:"recovery"`
		AuthorizationURL string `json:"authorization_url"`
	}
	if json.Unmarshal([]byte(message), &contract) == nil {
		r.ErrorCode, r.AuthorizationURL = contract.ErrorCode, contract.AuthorizationURL
		r.NextAction = contract.Recovery
		if r.NextAction == "" {
			r.NextAction = contract.NextAction
		}
		if contract.Error != "" {
			r.Message = contract.Error
		}
	}
	switch {
	case strings.Contains(message, "AUTH_REAUTHORIZATION_REQUIRED") ||
		contract.ErrorCode == "AUTH_REAUTHORIZATION_REQUIRED" ||
		contract.ErrorClass == "auth_invalid" || contract.ErrorClass == "permission":
		r.Status = probeAuthRequired
	case code == output.CodeToolNotFound ||
		contract.ErrorCode == "UNKNOWN_TOOL" || contract.ErrorClass == "unknown_tool" ||
		strings.Contains(message, "Unknown tool:"):
		r.Status = probeNotFound
	case code == "E_CHILD_TIMEOUT" || code == output.CodeTimeout ||
		contract.ErrorCode == "E_CHILD_TIMEOUT" || contract.ErrorClass == "timeout":
		r.Status = probeTimeout
	}
	return r
}

type doctorScriptResult struct {
	ToolCount    int            `json:"tool_count"`
	Categories   map[string]int `json:"categories"`
	Capabilities map[string]any `json:"capabilities"`
	Probes       []struct {
		Tool    string `json:"tool"`
		OK      bool   `json:"ok"`
		MS      int64  `json:"ms"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"probes"`
}

func (a *app) doctorCommand() *cobra.Command {
	var (
		expect  []string
		probes  []string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "doctor [--expect server,...] [--probe server__tool]...",
		Short: "Check the endpoint: connectivity, auth, server version and which servers it exposes",
		Long: `Check that the configured endpoint works and exposes what you need.

doctor always opens its own MCP session (it does not use the daemon) and:
  - connects and authenticates, and reports the MetaMCP server version and
    protocol version from initialize;
  - checks the facade tools (search_tools, execute) are exposed;
  - lists every server (category) on the endpoint with its tool count;
  - detects sandbox capabilities (tools.search, tools.list declarations);
  - reports whether a newer mcli release exists (MCLI_NO_UPDATE_CHECK=1 skips).

--expect github,VPS fails unless each named server is on the endpoint.
--probe github__get_me calls that tool once with {} (or with the JSON after
"=", e.g. --probe 'VPS__exec={"command":"true"}') and reports ok,
auth_required (the upstream needs re-authorization in MetaMCP), not_found,
timeout or error. Only probe READ-ONLY tools: a probe really runs.

Any missing server or failed probe makes doctor fail with E_DOCTOR (exit 1);
the full report is still in "data". Connection and auth failures keep their
usual codes (exit 2/3/4). That makes it a post-deploy smoke test:
  mcli doctor --expect github,VPS --probe github__get_me --probe VPS__list-profiles || rollback`,
		Example: `  mcli doctor
  mcli doctor --expect github,VPS
  mcli doctor --expect github,VPS --probe github__get_me --probe VPS__list-profiles`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			specs := []probeSpec{}
			for _, s := range probes {
				p, err := parseProbe(s)
				if err != nil {
					return err
				}
				specs = append(specs, p)
			}
			if timeout <= 0 {
				return output.New(output.CodeUsage, "--timeout must be positive")
			}
			wants := make([]string, 0, len(expect))
			for _, e := range expect {
				if e = strings.TrimSpace(e); e != "" {
					wants = append(wants, e)
				}
			}
			if len(expect) > 0 && len(wants) == 0 {
				return output.New(output.CodeUsage, "--expect needs at least one server name")
			}
			cfg, err := a.resolve()
			if err != nil {
				return a.setResult(output.Fail(err))
			}
			ctx := cmd.Context()

			updates := make(chan map[string]any, 1)
			go func() { updates <- checkForUpdate(ctx) }()

			conn := mcpconn.New(mcpconn.Options{URL: cfg.URL, APIKey: cfg.APIKey})
			defer conn.Close()
			started := time.Now()
			if _, err := verifyConn(ctx, conn); err != nil {
				return a.setResult(output.Fail(err))
			}
			connectMS := time.Since(started).Milliseconds()
			st := conn.Status()

			code, err := catalog.Script(catalog.DoctorScript, map[string]any{"probes": specs})
			if err != nil {
				return a.setResult(output.Fail(err))
			}
			started = time.Now()
			ectx, cancel := context.WithTimeout(ctx, ops.Deadline(ops.OpExecute, int(timeout/time.Millisecond)))
			defer cancel()
			res := ops.Execute(ectx, conn, ops.ExecuteArgs{Code: code, TimeoutMS: int(timeout / time.Millisecond), Note: "mcli doctor"})
			if !res.OK {
				return a.setResult(res)
			}
			checksMS := time.Since(started).Milliseconds()
			var got doctorScriptResult
			if err := decode(res, &got); err != nil {
				return a.setResult(output.Fail(err))
			}

			report := map[string]any{
				"endpoint": cfg.URL,
				"server": map[string]any{
					"name":             st.ServerName,
					"version":          st.ServerVersion,
					"protocol_version": st.ProtocolVersion,
				},
				"latency_ms":   map[string]int64{"connect": connectMS, "checks": checksMS},
				"tool_count":   got.ToolCount,
				"servers":      got.Categories,
				"capabilities": got.Capabilities,
			}
			if len(res.Warnings) > 0 {
				report["warnings"] = res.Warnings
			}

			var problems []string
			if len(wants) > 0 {
				have := make([]string, 0, len(got.Categories))
				for c := range got.Categories {
					have = append(have, c)
				}
				var missing []string
				for _, want := range wants {
					if got.Categories[want] > 0 {
						continue
					}
					missing = append(missing, want)
					msg := fmt.Sprintf("server %q is not on this endpoint", want)
					if s := similarNames(want, have); len(s) > 0 {
						msg += fmt.Sprintf(" (did you mean %q?)", s[0])
					}
					problems = append(problems, msg)
				}
				report["expect"] = map[string]any{"servers": wants, "missing": nonNil(missing)}
			}
			if len(specs) > 0 {
				reports := make([]probeReport, 0, len(got.Probes))
				for _, p := range got.Probes {
					r := probeReport{Tool: p.Tool, Status: probeOK, MS: p.MS}
					if !p.OK {
						r = classifyProbe(p.Code, p.Message)
						r.Tool, r.MS = p.Tool, p.MS
						if r.Status == probeNotFound {
							r.Suggestions = a.existingVariants(ctx, conn, p.Tool)
						}
						msg := fmt.Sprintf("probe %s: %s", p.Tool, r.Status)
						if len(r.Suggestions) > 0 {
							msg += fmt.Sprintf(" (did you mean %q?)", r.Suggestions[0])
						}
						problems = append(problems, msg)
					}
					reports = append(reports, r)
				}
				report["probes"] = reports
			}
			report["cli"] = <-updates
			report["problems"] = nonNil(problems)

			out := output.SuccessValue(report)
			if len(problems) > 0 {
				e := output.New(output.CodeDoctor, strings.Join(problems, "; "))
				e.Hint = doctorHint(report)
				out.OK, out.Error = false, e
			}
			return a.setResult(out)
		},
	}
	f := cmd.Flags()
	f.StringSliceVar(&expect, "expect", nil, "servers (categories) that must be on the endpoint, comma-separated")
	f.StringArrayVar(&probes, "probe", nil, "call this read-only tool once (repeatable): server__tool or server__tool={json}")
	f.DurationVar(&timeout, "timeout", 60*time.Second, "server-side timeout for the catalog read and probes")
	return cmd
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func doctorHint(report map[string]any) string {
	var hints []string
	if exp, ok := report["expect"].(map[string]any); ok && len(exp["missing"].([]string)) > 0 {
		hints = append(hints, "Missing servers: check that the URL names the right endpoint (namespace) and that the server is mapped and active in it.")
	}
	if probes, ok := report["probes"].([]probeReport); ok {
		for _, p := range probes {
			if p.Status == probeAuthRequired {
				hints = append(hints, "auth_required: re-authorize that server in MetaMCP (or update its credential); the mcli API key itself was accepted.")
				break
			}
		}
	}
	return strings.Join(hints, " ")
}

// existingVariants returns the "_"/"-" spellings of name that exist.
func (a *app) existingVariants(ctx context.Context, conn *mcpconn.Conn, name string) []string {
	variants := catalog.Variants(name)
	if len(variants) == 0 {
		return nil
	}
	code, err := catalog.Script(catalog.LookupScript, map[string]any{"names": variants})
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, suggestTimeout+15*time.Second)
	defer cancel()
	res := ops.Execute(ctx, conn, ops.ExecuteArgs{Code: code, TimeoutMS: int(suggestTimeout / time.Millisecond), Note: "mcli doctor: tool name suggestions"})
	var existing []string
	if !res.OK || decode(res, &existing) != nil {
		return nil
	}
	return existing
}

// checkForUpdate is doctor's best-effort release check; it never fails doctor.
func checkForUpdate(ctx context.Context) map[string]any {
	info := map[string]any{"version": version.Version}
	if update.Disabled() {
		return info
	}
	ctx, cancel := context.WithTimeout(ctx, updateCheckTimeout)
	defer cancel()
	c, err := update.Run(ctx)
	if err != nil {
		info["update_check_error"] = err.Error()
		return info
	}
	info["latest"] = c.Latest
	info["update_available"] = c.UpdateAvailable
	if c.Upgrade != "" {
		info["upgrade"] = c.Upgrade
	}
	return info
}

func (a *app) versionCommand() *cobra.Command {
	var check bool
	cmd := &cobra.Command{
		Use:   "version [--check]",
		Short: "Print the mcli version; --check compares it with the latest release",
		Long: `Print the mcli version as JSON. --check also looks up the latest GitHub
release ("update_available"); re-running the install script upgrades in place.
MCLI_RELEASES_URL points the check at a mirror.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := map[string]any{"version": version.Version, "build": version.Build()}
			if check {
				ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
				defer cancel()
				c, err := update.Run(ctx)
				if err != nil {
					return a.setResult(output.Fail(err))
				}
				info["latest"] = c.Latest
				info["update_available"] = c.UpdateAvailable
				if c.Upgrade != "" {
					info["upgrade"] = c.Upgrade
				}
			}
			return a.setResult(output.SuccessValue(info))
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "compare with the latest GitHub release")
	return cmd
}
