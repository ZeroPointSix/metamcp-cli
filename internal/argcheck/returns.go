package argcheck

// controlKeywords open a block that belongs to the enclosing function:
// a return inside it still returns from the script.
var controlKeywords = map[string]bool{
	"if": true, "for": true, "while": true, "switch": true, "catch": true, "with": true,
}

// HasTopLevelReturn reports whether src has a return statement that returns
// from the script itself, not from a function or callback defined in it.
// Strings, template literals and comments are skipped; a block counts as a
// function body when it follows "=>" or a parameter list that is not an
// if/for/while/switch/catch/with condition.
func HasTopLevelReturn(src string) bool {
	s := &scanner{src: src}
	var blocks []bool // true for a function body
	var parens []int  // positions of open "("
	lastParenOpen := -1
	lastParenClose := -1
	inFunction := func() bool {
		for _, f := range blocks {
			if f {
				return true
			}
		}
		return false
	}
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
		case isIdentStart(c):
			start := s.pos
			if s.ident() == "return" && !s.memberBefore(start) && !inFunction() {
				return true
			}
			continue
		case c == '(':
			parens = append(parens, s.pos)
		case c == ')':
			if n := len(parens); n > 0 {
				lastParenOpen, parens = parens[n-1], parens[:n-1]
				lastParenClose = s.pos
			}
		case c == '{':
			blocks = append(blocks, s.functionBody(lastParenOpen, lastParenClose))
		case c == '}':
			if n := len(blocks); n > 0 {
				blocks = blocks[:n-1]
			}
		}
		s.pos++
	}
	return false
}

// functionBody decides whether the "{" at s.pos opens a function body.
func (s *scanner) functionBody(parenOpen, parenClose int) bool {
	i := s.pos - 1
	for i >= 0 && isSpace(s.src[i]) {
		i--
	}
	if i >= 1 && s.src[i] == '>' && s.src[i-1] == '=' {
		return true // arrow function
	}
	if i < 0 || s.src[i] != ')' || i != parenClose {
		return false // plain block, object literal, class body...
	}
	j := parenOpen - 1
	for j >= 0 && isSpace(s.src[j]) {
		j--
	}
	end := j + 1
	for j >= 0 && isIdentPart(s.src[j]) {
		j--
	}
	return !controlKeywords[s.src[j+1:end]]
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
