// Package pinesrc holds small, regex-level readers of Pine source shared by
// the external engines (internal/pyne, internal/pineforge). It does not
// parse Pine; it only finds input declarations so callers can map hlp-style
// input keys (title or variable name) to the titles engines key on.
package pinesrc

import (
	"regexp"
	"strings"
)

var (
	inputDeclRE = regexp.MustCompile(`(?m)^\s*(?:var\s+|varip\s+)?(?:(?:int|float|bool|string|color)\s+)?([A-Za-z_]\w*)\s*=\s*input(?:\.\w+)?\s*\((.*)$`)
	titleKwRE   = regexp.MustCompile(`^title\s*=\s*"((?:[^"\\]|\\.)*)"$`)
	strLitRE    = regexp.MustCompile(`^"((?:[^"\\]|\\.)*)"$`)
	kwArgRE     = regexp.MustCompile(`^[A-Za-z_]\w*\s*=[^=]`)
)

// SplitArgs splits the text after "(" into top-level arguments, stopping
// at the matching ")", a trailing // comment or the end of the line.
func SplitArgs(s string) []string {
	var out []string
	depth, start := 0, 0
	inStr := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr != 0 {
			if c == '\\' {
				i++
			} else if c == inStr {
				inStr = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			inStr = c
		case '(', '[':
			depth++
		case ']':
			depth--
		case ')':
			if depth == 0 {
				return append(out, strings.TrimSpace(s[start:i]))
			}
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		case '/':
			if i+1 < len(s) && s[i+1] == '/' {
				return append(out, strings.TrimSpace(s[start:i]))
			}
		}
	}
	if rest := strings.TrimSpace(s[start:]); rest != "" {
		out = append(out, rest)
	}
	return out
}

// InputTitle reads an input's title from its argument list: title = "..."
// or, as in every input.* signature and the generic input(), the second
// positional argument when it is a string literal. The first positional
// argument is defval, which can itself be a string (input.string,
// input.timeframe("D", "TF"), input.symbol, input.session, input("x", "T")).
func InputTitle(args string) string {
	pos := 0
	for _, a := range SplitArgs(args) {
		if m := titleKwRE.FindStringSubmatch(a); m != nil {
			return m[1]
		}
		if kwArgRE.MatchString(a) {
			continue
		}
		pos++
		if pos == 2 {
			if m := strLitRE.FindStringSubmatch(a); m != nil {
				return m[1]
			}
		}
	}
	return ""
}

// InputKeys maps lower-cased input titles and the variables inputs are
// assigned to onto the input's title. Inputs without a title are skipped.
func InputKeys(src string) map[string]string {
	byKey := map[string]string{}
	for _, m := range inputDeclRE.FindAllStringSubmatch(src, -1) {
		title := InputTitle(m[2])
		if title == "" {
			continue
		}
		byKey[strings.ToLower(title)] = title
		byKey[strings.ToLower(m[1])] = title
	}
	return byKey
}
