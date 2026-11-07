// Tokenizer for the JQL grammar — turns a query string into a flat slice of tokens, validated
// only at the lexical level (quoting, number syntax); field names and operator/type
// compatibility are checked later, by the codegen stage.
package query

import (
	"fmt"
	"strings"
)

// InvalidJqlError is a malformed or disallowed JQL query — a clean 400-shaped error, never a
// panic or a raw internal error.
type InvalidJqlError struct{ Message string }

func (e *InvalidJqlError) Error() string { return e.Message }

func jqlErr[T any](format string, args ...any) (T, error) {
	var zero T
	return zero, &InvalidJqlError{Message: fmt.Sprintf(format, args...)}
}

type jqlTokenKind int

const (
	tokIdent jqlTokenKind = iota
	tokStr
	tokNumber
	tokLParen
	tokRParen
	tokComma
	tokEq
	tokNe
	tokGt
	tokGte
	tokLt
	tokLte
	tokTilde
	tokNotTilde
	tokKwAnd
	tokKwOr
	tokKwNot
	tokKwIn
	tokKwIs
	tokKwEmpty
	tokKwOrder
	tokKwBy
	tokKwAsc
	tokKwDesc
	tokKwTrue
	tokKwFalse
	tokEof
)

type jqlToken struct {
	kind jqlTokenKind
	text string // Ident/Str/Number payload
}

func jqlKeyword(word string) (jqlToken, bool) {
	switch strings.ToUpper(word) {
	case "AND":
		return jqlToken{kind: tokKwAnd}, true
	case "OR":
		return jqlToken{kind: tokKwOr}, true
	case "NOT":
		return jqlToken{kind: tokKwNot}, true
	case "IN":
		return jqlToken{kind: tokKwIn}, true
	case "IS":
		return jqlToken{kind: tokKwIs}, true
	case "EMPTY":
		return jqlToken{kind: tokKwEmpty}, true
	case "ORDER":
		return jqlToken{kind: tokKwOrder}, true
	case "BY":
		return jqlToken{kind: tokKwBy}, true
	case "ASC":
		return jqlToken{kind: tokKwAsc}, true
	case "DESC":
		return jqlToken{kind: tokKwDesc}, true
	case "TRUE":
		return jqlToken{kind: tokKwTrue}, true
	case "FALSE":
		return jqlToken{kind: tokKwFalse}, true
	default:
		return jqlToken{}, false
	}
}

// maxJqlLen is a generous ceiling against pathological/abusive input (e.g. thousands of
// "NOT NOT NOT ...") reaching the recursive-descent parser at all — same defensive posture as
// ListInput.Limit's cap.
const maxJqlLen = 2000

func jqlTokenize(input string) ([]jqlToken, error) {
	if len(input) > maxJqlLen {
		return jqlErr[[]jqlToken]("Query too long (max %d characters)", maxJqlLen)
	}
	runes := []rune(input)
	var tokens []jqlToken
	i := 0
	for i < len(runes) {
		c := runes[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			i++
			continue
		}
		switch {
		case c == '(':
			tokens = append(tokens, jqlToken{kind: tokLParen})
			i++
		case c == ')':
			tokens = append(tokens, jqlToken{kind: tokRParen})
			i++
		case c == ',':
			tokens = append(tokens, jqlToken{kind: tokComma})
			i++
		case c == '=':
			tokens = append(tokens, jqlToken{kind: tokEq})
			i++
		case c == '!':
			if i+1 < len(runes) && runes[i+1] == '=' {
				tokens = append(tokens, jqlToken{kind: tokNe})
				i += 2
			} else if i+1 < len(runes) && runes[i+1] == '~' {
				tokens = append(tokens, jqlToken{kind: tokNotTilde})
				i += 2
			} else {
				return jqlErr[[]jqlToken]("Unexpected '!' (expected '!=' or '!~')")
			}
		case c == '>':
			if i+1 < len(runes) && runes[i+1] == '=' {
				tokens = append(tokens, jqlToken{kind: tokGte})
				i += 2
			} else {
				tokens = append(tokens, jqlToken{kind: tokGt})
				i++
			}
		case c == '<':
			if i+1 < len(runes) && runes[i+1] == '=' {
				tokens = append(tokens, jqlToken{kind: tokLte})
				i += 2
			} else {
				tokens = append(tokens, jqlToken{kind: tokLt})
				i++
			}
		case c == '~':
			tokens = append(tokens, jqlToken{kind: tokTilde})
			i++
		case c == '\'' || c == '"':
			quote := c
			i++
			var s strings.Builder
			closed := false
			for i < len(runes) {
				ch := runes[i]
				if ch == '\\' && i+1 < len(runes) {
					s.WriteRune(runes[i+1])
					i += 2
					continue
				}
				if ch == quote {
					closed = true
					i++
					break
				}
				s.WriteRune(ch)
				i++
			}
			if !closed {
				return jqlErr[[]jqlToken]("Unterminated string literal")
			}
			tokens = append(tokens, jqlToken{kind: tokStr, text: s.String()})
		case isDigit(c) || (c == '-' && i+1 < len(runes) && isDigit(runes[i+1])):
			start := i
			i++
			for i < len(runes) && (isDigit(runes[i]) || runes[i] == '.') {
				i++
			}
			tokens = append(tokens, jqlToken{kind: tokNumber, text: string(runes[start:i])})
		case isAlpha(c) || c == '_':
			start := i
			for i < len(runes) && (isAlnum(runes[i]) || runes[i] == '_') {
				i++
			}
			word := string(runes[start:i])
			if kw, ok := jqlKeyword(word); ok {
				tokens = append(tokens, kw)
			} else {
				tokens = append(tokens, jqlToken{kind: tokIdent, text: word})
			}
		default:
			return jqlErr[[]jqlToken]("Unexpected character '%c'", c)
		}
	}
	tokens = append(tokens, jqlToken{kind: tokEof})
	return tokens, nil
}

// ASCII-only, unlike the Rust original's Unicode-aware is_whitespace/is_alphabetic — every real
// field name in this codebase is ASCII camelCase, so this is a deliberate, harmless narrowing,
// not a functional gap.
func isDigit(c rune) bool { return c >= '0' && c <= '9' }
func isAlpha(c rune) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func isAlnum(c rune) bool { return isAlpha(c) || isDigit(c) }
