package pine

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

type tokKind int

const (
	tName tokKind = iota
	tNum
	tStr
	tColor
	tOp
)

// Pos is a 1-based source position.
type Pos struct{ Line, Col int }

type token struct {
	kind  tokKind
	text  string
	num   float64
	isInt bool
	pos   Pos
}

func (t token) is(op string) bool { return t.kind == tOp && t.text == op }

func (t token) isName(n string) bool { return t.kind == tName && t.text == n }

// line is one logical line: tokens plus its block level (indent / 4).
type line struct {
	level int
	toks  []token
}

// operators, longest first.
var ops = []string{":=", "+=", "-=", "*=", "/=", "%=", "==", "!=", "<=", ">=", "=>",
	"+", "-", "*", "/", "%", "<", ">", "=", "?", ":", "(", ")", "[", "]", ",", "."}

// lex turns src into logical lines. It returns the declared //@version (0
// when absent) and every lexical error.
func lex(src string) (lines []line, version int, errs []error) {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	depth := 0 // open ( and [
	var cur *line
	for ln, raw := range strings.Split(src, "\n") {
		lineNo := ln + 1
		trim := strings.TrimSpace(raw)
		if strings.HasPrefix(trim, "//@version=") {
			v, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(trim, "//@version=")))
			if err != nil {
				errs = append(errs, &Error{Line: lineNo, Col: 1, Msg: "bad //@version directive"})
			} else {
				version = v
			}
			continue
		}
		toks, lerrs := lexLine(raw, lineNo)
		errs = append(errs, lerrs...)
		if len(toks) == 0 {
			continue
		}
		indent := 0
		for _, r := range raw {
			if r == ' ' {
				indent++
			} else if r == '\t' {
				indent += 4
			} else {
				break
			}
		}
		cont := false
		switch {
		case cur != nil && depth > 0:
			cont = true
		case cur != nil && indent%4 != 0:
			cont = true
		case cur != nil && endsWithOperator(cur.toks):
			cont = true
		}
		if cont {
			cur.toks = append(cur.toks, toks...)
		} else {
			lines = append(lines, line{level: indent / 4, toks: toks})
			cur = &lines[len(lines)-1]
		}
		for _, t := range toks {
			if t.kind != tOp {
				continue
			}
			switch t.text {
			case "(", "[":
				depth++
			case ")", "]":
				if depth > 0 {
					depth--
				}
			}
		}
	}
	return lines, version, errs
}

// endsWithOperator reports a line that cannot end a statement (binary
// operator or comma), so the next physical line continues it.
func endsWithOperator(toks []token) bool {
	if len(toks) == 0 {
		return false
	}
	t := toks[len(toks)-1]
	if t.kind == tName {
		return t.text == "and" || t.text == "or" || t.text == "not"
	}
	if t.kind != tOp {
		return false
	}
	switch t.text {
	case "+", "-", "*", "/", "%", "<", ">", "<=", ">=", "==", "!=", "?", ":", ",", "(", "[", "=", ":=", "+=", "-=", "*=", "/=", "%=":
		return true
	}
	return false
}

func lexLine(s string, lineNo int) ([]token, []error) {
	var toks []token
	var errs []error
	rs := []rune(s)
	i := 0
	for i < len(rs) {
		r := rs[i]
		col := i + 1
		switch {
		case r == ' ' || r == '\t':
			i++
		case r == '/' && i+1 < len(rs) && rs[i+1] == '/':
			return toks, errs
		case unicode.IsLetter(r) || r == '_':
			j := i
			for j < len(rs) && (unicode.IsLetter(rs[j]) || unicode.IsDigit(rs[j]) || rs[j] == '_' ||
				(rs[j] == '.' && j+1 < len(rs) && (unicode.IsLetter(rs[j+1]) || rs[j+1] == '_'))) {
				j++
			}
			toks = append(toks, token{kind: tName, text: string(rs[i:j]), pos: Pos{lineNo, col}})
			i = j
		case unicode.IsDigit(r) || (r == '.' && i+1 < len(rs) && unicode.IsDigit(rs[i+1])):
			j := i
			isInt := true
			for j < len(rs) && unicode.IsDigit(rs[j]) {
				j++
			}
			if j < len(rs) && rs[j] == '.' {
				isInt = false
				j++
				for j < len(rs) && unicode.IsDigit(rs[j]) {
					j++
				}
			}
			if j < len(rs) && (rs[j] == 'e' || rs[j] == 'E') {
				k := j + 1
				if k < len(rs) && (rs[k] == '+' || rs[k] == '-') {
					k++
				}
				if k < len(rs) && unicode.IsDigit(rs[k]) {
					isInt = false
					j = k
					for j < len(rs) && unicode.IsDigit(rs[j]) {
						j++
					}
				}
			}
			v, err := strconv.ParseFloat(string(rs[i:j]), 64)
			if err != nil {
				errs = append(errs, &Error{Line: lineNo, Col: col, Msg: fmt.Sprintf("bad number %q", string(rs[i:j]))})
			}
			toks = append(toks, token{kind: tNum, text: string(rs[i:j]), num: v, isInt: isInt, pos: Pos{lineNo, col}})
			i = j
		case r == '"' || r == '\'':
			q := r
			j := i + 1
			var b strings.Builder
			closed := false
			for j < len(rs) {
				if rs[j] == '\\' && j+1 < len(rs) {
					switch rs[j+1] {
					case 'n':
						b.WriteRune('\n')
					case 't':
						b.WriteRune('\t')
					default:
						b.WriteRune(rs[j+1])
					}
					j += 2
					continue
				}
				if rs[j] == q {
					closed = true
					j++
					break
				}
				b.WriteRune(rs[j])
				j++
			}
			if !closed {
				errs = append(errs, &Error{Line: lineNo, Col: col, Msg: "unterminated string"})
			}
			toks = append(toks, token{kind: tStr, text: b.String(), pos: Pos{lineNo, col}})
			i = j
		case r == '#':
			j := i + 1
			for j < len(rs) && strings.ContainsRune("0123456789abcdefABCDEF", rs[j]) {
				j++
			}
			if n := j - i - 1; n != 6 && n != 8 {
				errs = append(errs, &Error{Line: lineNo, Col: col, Msg: "bad color literal (use #RRGGBB or #RRGGBBAA)"})
			}
			toks = append(toks, token{kind: tColor, text: string(rs[i:j]), pos: Pos{lineNo, col}})
			i = j
		default:
			matched := false
			for _, op := range ops {
				if strings.HasPrefix(string(rs[i:min(i+len(op), len(rs))]), op) {
					toks = append(toks, token{kind: tOp, text: op, pos: Pos{lineNo, col}})
					i += len([]rune(op))
					matched = true
					break
				}
			}
			if !matched {
				errs = append(errs, &Error{Line: lineNo, Col: col, Msg: fmt.Sprintf("unexpected character %q", string(r))})
				i++
			}
		}
	}
	return toks, errs
}
