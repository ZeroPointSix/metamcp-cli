package argcheck

import (
	"strings"

	"github.com/ZeroPointSix/metamcp-cli/internal/catalog"
)

// Extract finds tool calls whose first (or, for tools.call, second) argument
// is an object literal. It is a small lexer, not a parser: it skips strings,
// template literals and comments, and gives up on a call it cannot read
// rather than guessing.
func Extract(src string) []Call {
	s := &scanner{src: src}
	var calls []Call
	for s.pos < len(s.src) {
		c := s.src[s.pos]
		switch {
		case c == '"' || c == '\'':
			s.skipString(c)
		case c == '`':
			s.skipTemplate()
		case c == '/' && s.peek(1) == '/':
			s.skipLineComment()
		case c == '/' && s.peek(1) == '*':
			s.skipBlockComment()
		case isIdentStart(c):
			start := s.pos
			id := s.ident()
			if id == "tools" && !s.memberBefore(start) {
				save := s.pos
				if call, ok := s.call(); ok {
					call.Line = 1 + strings.Count(s.src[:start], "\n")
					calls = append(calls, call)
				} else {
					s.pos = save
				}
			}
		default:
			s.pos++
		}
	}
	return calls
}

type scanner struct {
	src string
	pos int
}

func (s *scanner) peek(n int) byte {
	if s.pos+n < len(s.src) {
		return s.src[s.pos+n]
	}
	return 0
}

func isIdentStart(c byte) bool {
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool { return isIdentStart(c) || (c >= '0' && c <= '9') }

func (s *scanner) ident() string {
	start := s.pos
	for s.pos < len(s.src) && isIdentPart(s.src[s.pos]) {
		s.pos++
	}
	return s.src[start:s.pos]
}

// memberBefore reports whether the identifier at start is a property access
// (x.tools) or part of a longer identifier.
func (s *scanner) memberBefore(start int) bool {
	i := start - 1
	if i >= 0 && isIdentPart(s.src[i]) {
		return true
	}
	for i >= 0 && (s.src[i] == ' ' || s.src[i] == '\t' || s.src[i] == '\n' || s.src[i] == '\r') {
		i--
	}
	return i >= 0 && s.src[i] == '.' && (i == 0 || s.src[i-1] != '.')
}

func (s *scanner) skipTrivia() {
	for s.pos < len(s.src) {
		switch c := s.src[s.pos]; {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			s.pos++
		case c == '/' && s.peek(1) == '/':
			s.skipLineComment()
		case c == '/' && s.peek(1) == '*':
			s.skipBlockComment()
		default:
			return
		}
	}
}

func (s *scanner) skipLineComment() {
	for s.pos < len(s.src) && s.src[s.pos] != '\n' {
		s.pos++
	}
}

func (s *scanner) skipBlockComment() {
	end := strings.Index(s.src[s.pos+2:], "*/")
	if end < 0 {
		s.pos = len(s.src)
		return
	}
	s.pos += 2 + end + 2
}

// skipString skips a quoted string starting at s.pos and returns its raw
// contents (escapes left as written).
func (s *scanner) skipString(q byte) string {
	s.pos++
	start := s.pos
	for s.pos < len(s.src) {
		switch s.src[s.pos] {
		case '\\':
			s.pos += 2
			continue
		case q:
			v := s.src[start:s.pos]
			s.pos++
			return v
		case '\n':
			return s.src[start:s.pos] // unterminated
		}
		s.pos++
	}
	return s.src[start:]
}

func (s *scanner) skipTemplate() {
	s.pos++
	for s.pos < len(s.src) {
		switch s.src[s.pos] {
		case '\\':
			s.pos += 2
			continue
		case '`':
			s.pos++
			return
		case '$':
			if s.peek(1) == '{' {
				s.pos++
				s.skipBalanced()
				continue
			}
		}
		s.pos++
	}
}

// skipBalanced skips from an opening (, [ or { to just past its match.
func (s *scanner) skipBalanced() {
	depth := 0
	for s.pos < len(s.src) {
		c := s.src[s.pos]
		switch {
		case c == '"' || c == '\'':
			s.skipString(c)
			continue
		case c == '`':
			s.skipTemplate()
			continue
		case c == '/' && s.peek(1) == '/':
			s.skipLineComment()
			continue
		case c == '/' && s.peek(1) == '*':
			s.skipBlockComment()
			continue
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']' || c == '}':
			depth--
			if depth == 0 {
				s.pos++
				return
			}
		}
		s.pos++
	}
}

// member reads ".name" or `["name"]` and returns the name.
func (s *scanner) member() (string, bool) {
	s.skipTrivia()
	if s.pos >= len(s.src) {
		return "", false
	}
	switch s.src[s.pos] {
	case '.':
		s.pos++
		s.skipTrivia()
		if s.pos >= len(s.src) || !isIdentStart(s.src[s.pos]) {
			return "", false
		}
		return s.ident(), true
	case '[':
		s.pos++
		s.skipTrivia()
		if s.pos >= len(s.src) || (s.src[s.pos] != '"' && s.src[s.pos] != '\'') {
			return "", false
		}
		name := s.skipString(s.src[s.pos])
		s.skipTrivia()
		if s.pos >= len(s.src) || s.src[s.pos] != ']' {
			return "", false
		}
		s.pos++
		return name, true
	}
	return "", false
}

func (s *scanner) expect(c byte) bool {
	s.skipTrivia()
	if s.pos < len(s.src) && s.src[s.pos] == c {
		s.pos++
		return true
	}
	return false
}

// call reads what follows "tools": .server.tool({...}) or .call("name", {...}).
func (s *scanner) call() (Call, bool) {
	first, ok := s.member()
	if !ok {
		return Call{}, false
	}
	var name string
	if first == "call" {
		save := s.pos
		if s.expect('(') {
			s.skipTrivia()
			if s.pos < len(s.src) && (s.src[s.pos] == '"' || s.src[s.pos] == '\'') {
				name = catalog.Normalize(s.skipString(s.src[s.pos]))
				if !s.expect(',') {
					return Call{}, false
				}
			}
		}
		if name == "" {
			s.pos = save
		}
	}
	if name == "" {
		second, ok := s.member()
		if !ok || !s.expect('(') {
			return Call{}, false
		}
		name = first + "__" + second
	}
	s.skipTrivia()
	if s.pos >= len(s.src) || s.src[s.pos] != '{' {
		return Call{}, false
	}
	keys, exact, ok := s.object()
	if !ok {
		return Call{}, false
	}
	return Call{Tool: name, Keys: keys, Exact: exact}, true
}

// object reads the top-level keys of the object literal at s.pos.
func (s *scanner) object() (keys []string, exact, ok bool) {
	s.pos++ // {
	exact = true
	for {
		s.skipTrivia()
		if s.pos >= len(s.src) {
			return nil, false, false
		}
		c := s.src[s.pos]
		switch {
		case c == '}':
			s.pos++
			return keys, exact, true
		case c == ',':
			s.pos++
			continue
		case strings.HasPrefix(s.src[s.pos:], "..."):
			exact = false
			s.pos += 3
			s.skipValue()
			continue
		case c == '[':
			exact = false
			s.skipBalanced()
			s.skipValue()
			continue
		case c == '"' || c == '\'':
			keys = append(keys, s.skipString(c))
		case isIdentStart(c) || (c >= '0' && c <= '9'):
			keys = append(keys, s.ident())
		default:
			return nil, false, false
		}
		s.skipTrivia()
		if s.pos >= len(s.src) {
			return nil, false, false
		}
		switch s.src[s.pos] {
		case ':':
			s.pos++
			s.skipValue()
		case ',', '}':
			// shorthand property
		case '(':
			s.skipBalanced() // method parameters
			s.skipTrivia()
			if s.pos < len(s.src) && s.src[s.pos] == '{' {
				s.skipBalanced()
			}
		default:
			// get/set/async modifiers and anything else: drop the key, keep going.
			keys = keys[:len(keys)-1]
			exact = false
			s.skipValue()
		}
	}
}

// skipValue skips to the next "," or "}" at the current nesting level.
func (s *scanner) skipValue() {
	for s.pos < len(s.src) {
		c := s.src[s.pos]
		switch {
		case c == ',' || c == '}':
			return
		case c == '"' || c == '\'':
			s.skipString(c)
		case c == '`':
			s.skipTemplate()
		case c == '/' && s.peek(1) == '/':
			s.skipLineComment()
		case c == '/' && s.peek(1) == '*':
			s.skipBlockComment()
		case c == '(' || c == '[' || c == '{':
			s.skipBalanced()
		default:
			s.pos++
		}
	}
}
