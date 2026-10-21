// Package catalog builds the Code Mode scripts behind `mcli tools`,
// `mcli schema` and `mcli doctor`, and the local tool-name correction.
//
// The catalog is read inside the sandbox with tools.list, which serves the
// server's cached catalog (seconds) instead of search_tools' ranking (~20 s).
package catalog

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
)

// Logical tool names are "<server>__<tool>". Agents often swap "_" and "-"
// inside either part (VPS__run_script for VPS__run-script).
const separator = "__"

// maxVariants bounds the names tried for one misspelt tool.
const maxVariants = 64

// Normalize accepts "server__tool", "server.tool" and "tools.server.tool".
func Normalize(name string) string {
	name = strings.TrimSpace(name)
	if strings.Contains(name, separator) {
		return name
	}
	name = strings.TrimPrefix(name, "tools.")
	if server, tool, ok := strings.Cut(name, "."); ok && server != "" && tool != "" {
		return server + separator + tool
	}
	return name
}

// Variants returns the names that differ from name only by swapping "_" and
// "-" (the "__" between server and tool stays). name itself is excluded.
func Variants(name string) []string {
	server, tool, ok := strings.Cut(name, separator)
	if !ok {
		return nil
	}
	var out []string
	for _, s := range swaps(server) {
		for _, t := range swaps(tool) {
			if v := s + separator + t; v != name && !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
	}
	if len(out) > maxVariants {
		out = out[:maxVariants]
	}
	return out
}

// swaps lists every spelling of s with each "_" or "-" replaced by either.
// Past 6 separators only the two uniform spellings are tried.
func swaps(s string) []string {
	var pos []int
	for i := 0; i < len(s); i++ {
		if s[i] == '_' || s[i] == '-' {
			pos = append(pos, i)
		}
	}
	if len(pos) > 6 {
		return []string{s, strings.ReplaceAll(s, "_", "-"), strings.ReplaceAll(s, "-", "_")}
	}
	out := make([]string, 0, 1<<len(pos))
	b := []byte(s)
	for mask := 0; mask < 1<<len(pos); mask++ {
		for i, p := range pos {
			if mask&(1<<i) != 0 {
				b[p] = '-'
			} else {
				b[p] = '_'
			}
		}
		out = append(out, string(b))
	}
	// Keep the input spelling first so callers can rely on ordering.
	if i := slices.Index(out, s); i > 0 {
		out[0], out[i] = out[i], out[0]
	}
	return out
}

// CallHint shows how to call a logical tool name from a script. Names with
// "-" are not valid identifiers, so they need bracket notation.
func CallHint(name string) string {
	server, tool, ok := strings.Cut(name, separator)
	if !ok {
		return `tools.call("` + name + `", args)`
	}
	return "tools" + access(server) + access(tool) + "(args)"
}

var identifier = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

func access(part string) string {
	if identifier.MatchString(part) {
		return "." + part
	}
	return `["` + part + `"]`
}

// Script returns a Code Mode program that runs body with `args` bound to v.
// The JSON encoding of v is a valid JavaScript expression.
func Script(body string, v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return "const args = " + string(data) + ";\n" + body, nil
}

// LookupScript resolves which of args.names exist.
const LookupScript = `return (await tools.list({ names: args.names })).map((t) => t.name);`

// ToolsScript lists the catalog, optionally one category with summaries and
// declarations. An unknown category returns the categories that do exist.
const ToolsScript = `
let declaration = args.declaration;
let items;
if (args.category) {
  try {
    items = await tools.list({ category: args.category, detail: true, ...(declaration ? { declaration: true } : {}) });
  } catch (e) {
    if (!declaration || e.code !== "E_INVALID_ARGUMENT") throw e;
    declaration = false;
    items = await tools.list({ category: args.category, detail: true });
  }
  if (items.length > 0) return { items, declaration };
}
const all = await tools.list();
return { items: args.category ? [] : all, categories: [...new Set(all.map((t) => t.category))], declaration };
`

// SchemaScript reads full schemas for args.names; for names that do not
// exist it also checks args.variants (name -> spellings).
const SchemaScript = `
const opts = { names: args.names, schema: true, detail: true };
let declaration = true;
let items;
try {
  items = await tools.list({ ...opts, declaration: true });
} catch (e) {
  if (e.code !== "E_INVALID_ARGUMENT" && e.code !== "E_TOOL_LIST_TOO_LARGE") throw e;
  declaration = false;
  items = await tools.list(opts);
}
const found = new Set(items.map((t) => t.name));
const tried = args.names.filter((n) => !found.has(n)).flatMap((n) => args.variants[n] ?? []);
const existing = tried.length ? (await tools.list({ names: tried })).map((t) => t.name) : [];
return { items, existing, declaration };
`

// DoctorScript summarises the catalog, detects sandbox capabilities and
// calls each probe. Probe results are never returned, only their outcome.
const DoctorScript = `
const all = await tools.list();
const categories = {};
for (const t of all) categories[t.category] = (categories[t.category] ?? 0) + 1;
const capabilities = { tools_search: typeof tools.search === "function", list_declaration: false };
if (all.length > 0) {
  try {
    await tools.list({ names: [all[0].name], declaration: true });
    capabilities.list_declaration = true;
  } catch (e) {
    if (e.code !== "E_INVALID_ARGUMENT") throw e;
  }
}
const probes = await Promise.all(args.probes.map(async (p) => {
  const started = Date.now();
  try {
    await tools.call(p.name, p.args);
    return { tool: p.name, ok: true, ms: Date.now() - started };
  } catch (e) {
    const message = e && typeof e.message === "string" ? e.message : String(e);
    return { tool: p.name, ok: false, ms: Date.now() - started, code: (e && e.code) || null, message: message.slice(0, 4000) };
  }
}));
return { tool_count: all.length, categories, capabilities, probes };
`
