package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ZeroPointSix/metamcp-cli/internal/catalog"
	"github.com/ZeroPointSix/metamcp-cli/internal/config"
	"github.com/ZeroPointSix/metamcp-cli/internal/ops"
	"github.com/ZeroPointSix/metamcp-cli/internal/output"
)

const (
	// catalogTimeout bounds the generated catalog scripts.
	catalogTimeout = 60 * time.Second
	// suggestTimeout bounds the extra lookup after E_TOOL_NOT_FOUND.
	suggestTimeout = 20 * time.Second
)

// runScript executes a CLI-generated Code Mode program. It is plain
// JavaScript, so it skips the local transpile step.
func (a *app) runScript(ctx context.Context, cfg *config.Resolved, body string, v any, timeout time.Duration, note string) *output.Result {
	code, err := catalog.Script(body, v)
	if err != nil {
		return output.Fail(err)
	}
	args := ops.ExecuteArgs{Code: code, TimeoutMS: int(timeout / time.Millisecond), Note: note}
	return a.run(ctx, cfg, ops.OpExecute, args, args.TimeoutMS)
}

// decode unmarshals a successful script result.
func decode(res *output.Result, v any) error {
	if err := json.Unmarshal(res.Data, v); err != nil {
		return output.Newf(output.CodeProtocol, "unexpected catalog result: %v", err)
	}
	return nil
}

// listedTool is one tools.list entry.
type listedTool struct {
	Name                 string `json:"name"`
	Category             string `json:"category,omitempty"`
	Summary              string `json:"summary,omitempty"`
	Declaration          string `json:"declaration,omitempty"`
	DeclarationTruncated bool   `json:"declaration_truncated,omitempty"`
}

func (a *app) toolsCommand() *cobra.Command {
	var (
		category     string
		declarations bool
	)
	cmd := &cobra.Command{
		Use:   "tools [--category <server>]",
		Short: "List the endpoint's tools by server (fast; no search ranking)",
		Long: `List tool names from the endpoint's catalog (tools.list in the sandbox).

This reads the cached catalog in a few seconds; "mcli search" ranks tools
on the server and takes much longer. Without --category, every server and
its tool names are listed. With --category, each tool comes with a one-line
summary; add --declarations for TypeScript declarations (newer servers).

Names are logical names, "<server>__<tool>". Copy them exactly: many contain
"-", which needs bracket notation in scripts: tools.VPS["run-script"](args).`,
		Example: `  mcli tools
  mcli tools --category VPS
  mcli tools --category github --declarations`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if declarations && category == "" {
				return output.New(output.CodeUsage, "--declarations needs --category")
			}
			cfg, err := a.resolve()
			if err != nil {
				return a.setResult(output.Fail(err))
			}
			res := a.runScript(cmd.Context(), cfg, catalog.ToolsScript,
				map[string]any{"category": category, "declaration": declarations}, catalogTimeout, "mcli tools")
			if !res.OK {
				return a.setResult(res)
			}
			var got struct {
				Items       []listedTool `json:"items"`
				Categories  []string     `json:"categories"`
				Declaration bool         `json:"declaration"`
			}
			if err := decode(res, &got); err != nil {
				return a.setResult(output.Fail(err))
			}
			if category == "" {
				return a.setResult(output.SuccessValue(groupByCategory(got.Items)))
			}
			if len(got.Items) == 0 {
				return a.setResult(output.Fail(unknownCategory(category, got.Categories)))
			}
			if declarations && !got.Declaration {
				fmt.Fprintln(a.stderr, "mcli: this MetaMCP server does not support tools.list({ declaration: true }); listing summaries only. Use `mcli schema <name>` for full schemas.")
			}
			for i := range got.Items {
				got.Items[i].Category = ""
			}
			return a.setResult(output.SuccessValue(map[string]any{
				"category": category,
				"count":    len(got.Items),
				"tools":    got.Items,
			}))
		},
	}
	cmd.Flags().StringVar(&category, "category", "", "list one server's tools with summaries")
	cmd.Flags().BoolVar(&declarations, "declarations", false, "include TypeScript declarations (needs --category)")
	return cmd
}

type categoryGroup struct {
	Category string   `json:"category"`
	Count    int      `json:"count"`
	Tools    []string `json:"tools"`
}

func groupByCategory(items []listedTool) map[string]any {
	var groups []categoryGroup
	index := map[string]int{}
	for _, t := range items {
		i, ok := index[t.Category]
		if !ok {
			i = len(groups)
			index[t.Category] = i
			groups = append(groups, categoryGroup{Category: t.Category})
		}
		groups[i].Count++
		groups[i].Tools = append(groups[i].Tools, t.Name)
	}
	slices.SortFunc(groups, func(x, y categoryGroup) int { return strings.Compare(x.Category, y.Category) })
	return map[string]any{"count": len(items), "categories": groups}
}

func unknownCategory(category string, have []string) *output.Error {
	e := output.Newf(output.CodeToolNotFound, "no server (category) %q on this endpoint", category)
	if len(have) == 0 {
		e.Hint = "This endpoint exposes no tools; check that upstream servers are mapped and active with `mcli doctor`."
		return e
	}
	e.Suggestions = similarNames(category, have)
	slices.Sort(have)
	if len(e.Suggestions) > 0 {
		e.Hint = fmt.Sprintf("Did you mean %q? ", e.Suggestions[0])
	}
	e.Hint += "Servers on this endpoint: " + strings.Join(have, ", ") + "."
	return e
}

// similarNames returns the names in have that match want ignoring case and
// the "_"/"-" distinction.
func similarNames(want string, have []string) []string {
	fold := func(s string) string { return strings.ToLower(strings.ReplaceAll(s, "-", "_")) }
	var out []string
	for _, h := range have {
		if h != want && fold(h) == fold(want) {
			out = append(out, h)
		}
	}
	return out
}

func (a *app) schemaCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schema <server__tool>...",
		Short: "Print the full input/output schema of one or more tools",
		Long: `Print each tool's full input and output JSON Schema, its summary and, on
newer servers, a TypeScript declaration.

Names are logical names as printed by "mcli search" or "mcli tools"
("VPS__run-script"); "VPS.run-script" is accepted too. When a name does not
exist, the error lists spellings that do (for example "_" swapped for "-").`,
		Example: `  mcli schema VPS__exec
  mcli schema github__get_me github__list_pull_requests`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var names []string
			variants := map[string][]string{}
			for _, arg := range args {
				n := catalog.Normalize(arg)
				if n == "" || slices.Contains(names, n) {
					continue
				}
				names = append(names, n)
				variants[n] = catalog.Variants(n)
			}
			if len(names) == 0 {
				return output.New(output.CodeUsage, "no tool name given; pass server__tool (for example VPS__exec)")
			}
			cfg, err := a.resolve()
			if err != nil {
				return a.setResult(output.Fail(err))
			}
			res := a.runScript(cmd.Context(), cfg, catalog.SchemaScript,
				map[string]any{"names": names, "variants": variants}, catalogTimeout, "mcli schema")
			if !res.OK {
				return a.setResult(res)
			}
			var got struct {
				Items    []json.RawMessage `json:"items"`
				Existing []string          `json:"existing"`
			}
			if err := decode(res, &got); err != nil {
				return a.setResult(output.Fail(err))
			}
			found := map[string]bool{}
			for _, raw := range got.Items {
				var t struct {
					Name string `json:"name"`
				}
				_ = json.Unmarshal(raw, &t)
				found[t.Name] = true
			}
			var missing []string
			for _, n := range names {
				if !found[n] {
					missing = append(missing, n)
				}
			}
			out := output.SuccessValue(map[string]any{"tools": got.Items})
			if len(missing) == 0 {
				return a.setResult(out)
			}
			e := output.Newf(output.CodeToolNotFound, "unknown tool: %s", strings.Join(missing, ", "))
			e.Suggestions = got.Existing
			e.Hint = didYouMean(got.Existing, "Run `mcli tools` or `mcli search <words>` to find exact names.")
			out.OK, out.Error = false, e
			return a.setResult(out)
		},
	}
	return cmd
}

// didYouMean prefixes fallback with a suggestion and how to call it.
func didYouMean(names []string, fallback string) string {
	if len(names) == 0 {
		return fallback
	}
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = fmt.Sprintf("%q", n)
	}
	return fmt.Sprintf("Did you mean %s? Call it as %s or tools.call(%q, args).",
		strings.Join(quoted, " or "), catalog.CallHint(names[0]), names[0])
}

var unknownToolPattern = regexp.MustCompile(`Unknown tool: ([A-Za-z0-9_-]+)`)

// missingTool finds the tool name behind an E_TOOL_NOT_FOUND failure.
func missingTool(res *output.Result) string {
	for _, raw := range res.Diagnostics {
		var d struct {
			Code     string `json:"code"`
			Location struct {
				Tool string `json:"tool"`
			} `json:"location"`
		}
		if json.Unmarshal(raw, &d) == nil && d.Code == output.CodeToolNotFound && d.Location.Tool != "" {
			return d.Location.Tool
		}
	}
	if m := unknownToolPattern.FindStringSubmatch(res.Error.Message); m != nil {
		return m[1]
	}
	return ""
}

// suggestToolNames adds "did you mean" suggestions to a failure that names a
// missing tool by checking which "_"/"-" spellings exist. It triggers on the
// name in the diagnostics/message rather than on the top-level code: older
// servers wrap the same child error in E_RUNTIME. It costs one extra catalog
// read, only on this failure path.
func (a *app) suggestToolNames(ctx context.Context, cfg *config.Resolved, res *output.Result) {
	if res.OK || res.Error == nil {
		return
	}
	name := catalog.Normalize(missingTool(res))
	variants := catalog.Variants(name)
	if len(variants) == 0 {
		return
	}
	lookup := a.runScript(ctx, cfg, catalog.LookupScript, map[string]any{"names": variants}, suggestTimeout, "mcli: tool name suggestions")
	var existing []string
	if !lookup.OK || decode(lookup, &existing) != nil {
		fmt.Fprintln(a.stderr, "mcli: could not look up similar tool names")
		return
	}
	if len(existing) > 0 {
		res.Error.Suggestions = existing
		res.Error.Hint = strings.TrimSpace(didYouMean(existing, "") + " " + res.Error.Hint)
	}
}

// capabilityHint explains failures caused by sandbox APIs the server does
// not have yet (tools.search and tools.list declarations arrived together).
func capabilityHint(res *output.Result) {
	if res.OK || res.Error == nil {
		return
	}
	msg := strings.ToLower(res.Error.Message)
	missingSearch := strings.Contains(msg, "tools.search is not a function")
	missingDecl := strings.Contains(msg, "declaration") &&
		(strings.Contains(msg, "unknown option") || strings.Contains(msg, "unknown argument") ||
			strings.Contains(msg, "not supported") || strings.Contains(msg, "unexpected option"))
	if missingSearch || missingDecl {
		res.Error.Hint = strings.TrimSpace("This MetaMCP server predates tools.search() and tools.list({ declaration }). Use `mcli search`, `mcli tools` or `mcli schema` instead, or tools.list({ names, schema: true }) in scripts; `mcli doctor` shows what the server supports. " + res.Error.Hint)
	}
}
