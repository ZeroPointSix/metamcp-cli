package cli

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"time"

	"github.com/ZeroPointSix/metamcp-cli/internal/argcheck"
	"github.com/ZeroPointSix/metamcp-cli/internal/catalog"
	"github.com/ZeroPointSix/metamcp-cli/internal/config"
	"github.com/ZeroPointSix/metamcp-cli/internal/output"
)

// argCheckTimeout bounds the schema lookup behind the argument check; it is
// a catalog read (well under a second on a warm daemon).
const argCheckTimeout = 10 * time.Second

// checkArgs compares literal tool-call arguments in src with the tools'
// schemas. It costs one catalog read, only when src has such calls. When the
// schemas cannot be read it adds a W_ARG_CHECK_SKIPPED warning and lets the
// script run, except under strict, where it returns an error instead: a
// strict check that did not happen must not pass.
func (a *app) checkArgs(ctx context.Context, cfg *config.Resolved, src string, strict bool) ([]json.RawMessage, *output.Error) {
	if v, err := strconv.ParseBool(os.Getenv(config.EnvArgCheck)); err == nil && !v && !strict {
		return nil, nil
	}
	calls := argcheck.Extract(src)
	if len(calls) == 0 {
		return nil, nil
	}
	res := a.runScript(ctx, cfg, catalog.ArgSchemaScript, map[string]any{"names": argcheck.Tools(calls)}, argCheckTimeout, "mcli: argument check")
	var items []struct {
		Name        string          `json:"name"`
		InputSchema json.RawMessage `json:"inputSchema"`
	}
	if !res.OK || decode(res, &items) != nil {
		reason := "unexpected schema lookup result"
		if res.Error != nil {
			reason = res.Error.Error()
		}
		if strict {
			return nil, output.Newf(output.CodeArgCheck, "argument check could not read tool schemas: %s", reason)
		}
		w, _ := json.Marshal(struct {
			Severity string `json:"severity"`
			Code     string `json:"code"`
			Message  string `json:"message"`
		}{"warning", "W_ARG_CHECK_SKIPPED", "Argument names were not checked: could not read tool schemas (" + reason + ")."})
		return []json.RawMessage{w}, nil
	}
	schemas := map[string]argcheck.Schema{}
	for _, it := range items {
		if len(it.InputSchema) > 0 {
			schemas[it.Name] = argcheck.ParseSchema(it.InputSchema)
		}
	}
	var out []json.RawMessage
	for _, w := range argcheck.Check(calls, schemas) {
		if data, err := json.Marshal(w); err == nil {
			out = append(out, data)
		}
	}
	return out, nil
}

// noReturnWarning flags the common slip of printing a result with
// console.log instead of returning it: data is then null and the value only
// exists as text in "console".
func noReturnWarning(res *output.Result, src string) {
	if !res.OK || res.Console == "" || string(res.Data) != "null" || argcheck.HasTopLevelReturn(src) {
		return
	}
	w, _ := json.Marshal(struct {
		Severity string `json:"severity"`
		Code     string `json:"code"`
		Message  string `json:"message"`
	}{"warning", "W_NO_RETURN", "The script returned nothing, so data is null and the output is only text in console. `return` the value to get it as JSON in data."})
	res.Warnings = append(res.Warnings, w)
}
