// Package script turns a local TypeScript/JavaScript file into the code string
// MetaMCP's Code Mode `execute` tool accepts.
//
// Contract: a script is a function body. It may use top-level await, returns
// its result with `return`, and reaches tools only through the global `tools`
// object. The server wraps the code as `(async () => { "use strict"; <code> })()`,
// so imports, exports and modules are not available.
//
// Pipeline: wrap the source as `(async () => {\n<src>\n})`, strip types with
// esbuild's Transform API (no bundling), then submit
// `return await (<transpiled>)();`. A source map translates server-reported
// line numbers back to the caller's file.
package script

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/evanw/esbuild/pkg/api"

	"github.com/ZeroPointSix/metamcp-cli/internal/output"
)

const (
	// MaxSourceBytes bounds the file mcli is willing to read.
	MaxSourceBytes = 256 << 10
	// MaxCodeBytes is the server's Code Mode input limit.
	MaxCodeBytes = 65536

	wrapPrefix   = "(async () => {\n"
	wrapSuffix   = "\n})"
	submitPrefix = "return await ("
	submitSuffix = ")();"
	// wrapLines is how many lines wrapPrefix adds before the caller's line 1.
	wrapLines = 1
)

var moduleSyntax = regexp.MustCompile(`^\s*(import\s*[\w*{'"]|import\s+type\b|export\b)`)

// Prepared is a script ready to submit.
type Prepared struct {
	Name string
	Code string
	src  []string
	smap *sourceMap
}

// Read loads a script from path, or from stdin when path is "-".
func Read(path string, stdin io.Reader) ([]byte, string, error) {
	var (
		data []byte
		err  error
		name = path
	)
	if path == "-" {
		name = "stdin.ts"
		data, err = io.ReadAll(io.LimitReader(stdin, MaxSourceBytes+1))
	} else {
		var f *os.File
		f, err = os.Open(path)
		if err == nil {
			defer f.Close()
			data, err = io.ReadAll(io.LimitReader(f, MaxSourceBytes+1))
		}
	}
	if err != nil {
		return nil, name, output.Newf(output.CodeUsage, "read script: %v", err)
	}
	if len(data) > MaxSourceBytes {
		return nil, name, output.Newf(output.CodeScriptTooLarge, "script exceeds the %d KiB source limit", MaxSourceBytes>>10)
	}
	return data, name, nil
}

func loaderFor(name string) api.Loader {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".js", ".mjs", ".cjs":
		return api.LoaderJS
	default:
		// TypeScript is a superset of JavaScript; stdin and unknown extensions
		// are treated as TypeScript.
		return api.LoaderTS
	}
}

// Prepare validates and transpiles a script.
func Prepare(src []byte, name string) (*Prepared, error) {
	src = bytes.TrimPrefix(src, []byte("\xef\xbb\xbf"))
	text := strings.ReplaceAll(string(src), "\r\n", "\n")
	if strings.TrimSpace(text) == "" {
		return nil, output.New(output.CodeUsage, "script is empty")
	}
	lines := strings.Split(text, "\n")

	result := api.Transform(wrapPrefix+text+wrapSuffix, api.TransformOptions{
		Loader:         loaderFor(name),
		Sourcefile:     filepath.Base(name),
		Sourcemap:      api.SourceMapExternal,
		SourcesContent: api.SourcesContentExclude,
		LogLevel:       api.LogLevelSilent,
	})
	if len(result.Errors) > 0 {
		return nil, syntaxError(result.Errors[0], lines, name)
	}

	body := strings.TrimRight(string(result.Code), " \t\r\n")
	body = strings.TrimSuffix(body, ";")
	code := submitPrefix + body + submitSuffix
	if len(code) > MaxCodeBytes {
		return nil, output.Newf(output.CodeScriptTooLarge,
			"transpiled script is %d bytes; the server accepts at most %d", len(code), MaxCodeBytes)
	}

	p := &Prepared{Name: name, Code: code, src: lines}
	if sm, err := parseSourceMap(result.Map); err == nil {
		p.smap = sm
	}
	return p, nil
}

func syntaxError(msg api.Message, lines []string, name string) error {
	line, col := 0, 0
	if msg.Location != nil {
		line = msg.Location.Line - wrapLines
		col = msg.Location.Column + 1
	}
	if line < 1 || line > len(lines) {
		// The error points into the wrapper, e.g. an unbalanced brace at EOF.
		line, col = len(lines), 0
	}
	src := lines[line-1]
	if moduleSyntax.MatchString(src) {
		e := output.Newf(output.CodeScriptModule, "%s:%d: import/export is not supported in mcli scripts", name, line)
		e.Line = line
		return e
	}
	e := output.Newf(output.CodeScriptSyntax, "%s:%d:%d: %s", name, line, col, msg.Text)
	e.Line, e.Column = line, col
	return e
}

// MapPosition translates a 1-based line/column in the submitted code to a
// 1-based line/column in the caller's file. ok is false when the position does
// not correspond to user code (for example, the generated wrapper).
func (p *Prepared) MapPosition(line, col int) (int, int, bool) {
	if p == nil || p.smap == nil || line < 1 {
		return 0, 0, false
	}
	genCol := col - 1
	if line == 1 {
		// The submitted code prepends submitPrefix to the transpiled first line.
		genCol -= len(submitPrefix)
	}
	srcLine, srcCol, ok := p.smap.lookup(line-1, max(genCol, 0))
	if !ok {
		return 0, 0, false
	}
	userLine := srcLine + 1 - wrapLines
	if userLine < 1 || userLine > len(p.src) {
		return 0, 0, false
	}
	return userLine, srcCol + 1, true
}

// SourceLine returns the caller's source text at a 1-based line.
func (p *Prepared) SourceLine(line int) string {
	if p == nil || line < 1 || line > len(p.src) {
		return ""
	}
	return p.src[line-1]
}
