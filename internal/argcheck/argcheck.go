// Package argcheck finds tool calls with literal arguments in a script and
// compares their top-level argument names with the tools' input schemas.
//
// Many upstream tools ignore arguments they do not know. A misspelt name
// (`{ q: "x" }` for `{ query: "x" }`) then "succeeds" with the wrong
// behaviour, often returning far more data than intended, and nothing tells
// the agent why. The check is static and best-effort: it only looks at
// object literals passed directly to tools.<server>.<tool>(...),
// tools.<server>["tool"](...) or tools.call("server__tool", ...), and only
// reports; it never blocks a script unless the caller asks for that.
package argcheck

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Call is one tool call with an object-literal argument.
type Call struct {
	Tool string   // logical name, server__tool
	Keys []string // top-level keys of the literal, in source order
	// Exact is false when the literal has a spread or computed key, so keys
	// may be missing from Keys.
	Exact bool
	Line  int // 1-based line of the call in the source
}

// Schema is the part of a tool's input schema the check needs.
type Schema struct {
	Properties []string
	Required   []string
	// Open is true when the schema declares additional properties allowed, or
	// lists no properties at all: unknown keys are then not reported.
	Open bool
}

// ParseSchema reads an inputSchema JSON object.
func ParseSchema(raw json.RawMessage) Schema {
	var s struct {
		Properties           map[string]json.RawMessage `json:"properties"`
		Required             []string                   `json:"required"`
		AdditionalProperties json.RawMessage            `json:"additionalProperties"`
	}
	if json.Unmarshal(raw, &s) != nil {
		return Schema{Open: true}
	}
	out := Schema{Required: s.Required}
	for k := range s.Properties {
		out.Properties = append(out.Properties, k)
	}
	slices.Sort(out.Properties)
	ap := strings.TrimSpace(string(s.AdditionalProperties))
	out.Open = len(out.Properties) == 0 || (ap != "" && ap != "false")
	return out
}

// Warning is one finding, shaped like a Code Mode diagnostic so it can sit in
// the result's "warnings" list.
type Warning struct {
	Severity   string `json:"severity"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	Tool       string `json:"tool"`
	Line       int    `json:"line,omitempty"`
	Argument   string `json:"argument"`
	Suggestion string `json:"suggestion,omitempty"`
}

// Warning codes.
const (
	CodeUnknown = "W_UNKNOWN_ARGUMENT"
	CodeMissing = "W_MISSING_ARGUMENT"
)

// Check compares calls with schemas (keyed by logical tool name). Tools
// without a schema are skipped.
func Check(calls []Call, schemas map[string]Schema) []Warning {
	var out []Warning
	for _, c := range calls {
		s, ok := schemas[c.Tool]
		if !ok {
			continue
		}
		if !s.Open {
			for _, k := range c.Keys {
				if slices.Contains(s.Properties, k) {
					continue
				}
				w := Warning{Severity: "warning", Code: CodeUnknown, Tool: c.Tool, Line: c.Line, Argument: k,
					Suggestion: closest(k, s.Properties, c.Keys)}
				w.Message = fmt.Sprintf("%s: unknown argument %q", c.Tool, k)
				if w.Suggestion != "" {
					w.Message += fmt.Sprintf(" (did you mean %q?)", w.Suggestion)
				}
				w.Message += "; the tool may silently ignore it. Arguments: " + strings.Join(s.Properties, ", ")
				out = append(out, w)
			}
		}
		if !c.Exact {
			continue
		}
		for _, r := range s.Required {
			if !slices.Contains(c.Keys, r) {
				out = append(out, Warning{Severity: "warning", Code: CodeMissing, Tool: c.Tool, Line: c.Line, Argument: r,
					Message: fmt.Sprintf("%s: required argument %q is missing", c.Tool, r)})
			}
		}
	}
	return out
}

// closest suggests the property an unknown key most likely meant, skipping
// properties the call already sets.
func closest(key string, props, used []string) string {
	lower := strings.ToLower(key)
	norm := func(s string) string {
		return strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(s))
	}
	best, bestScore := "", 1<<30
	for _, p := range props {
		if slices.Contains(used, p) {
			continue
		}
		lp := strings.ToLower(p)
		var score int
		switch {
		case norm(p) == norm(key):
			score = 0
		case len(lower) >= 1 && strings.HasPrefix(lp, lower), len(lp) >= 3 && strings.HasPrefix(lower, lp):
			score = 1
		default:
			d := distance(lower, lp)
			limit := max(2, min(len(lower), len(lp))/3)
			if d > limit {
				continue
			}
			score = 1 + d
		}
		if score < bestScore {
			best, bestScore = p, score
		}
	}
	return best
}

// distance is the Levenshtein distance between a and b.
func distance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// Tools returns the distinct tool names in calls.
func Tools(calls []Call) []string {
	var out []string
	for _, c := range calls {
		if !slices.Contains(out, c.Tool) {
			out = append(out, c.Tool)
		}
	}
	return out
}
