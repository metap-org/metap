// Postgres rewrites an expression when it stores it (pg_get_indexdef returns a canonical form
// that rarely matches what was typed verbatim). NormalizeExpr makes both sides (desired, typed
// by Compile; actual, echoed back by Postgres via Introspect) comparable: lowercase, strip
// double-quotes, strip every ::type[(args)] cast annotation, strip all parentheses, collapse
// whitespace.
//
// Known trade-off: stripping *all* casts means an index whose only change is the outer type cast
// is not detected as needing a rebuild — consistent with this crate's current scope (there is no
// ALTER COLUMN TYPE op either).
package reconciler

import "strings"

func NormalizeExpr(expr string) string {
	noCasts := stripTypeCasts(expr)
	lowercased := strings.ToLower(noCasts)
	var bare strings.Builder
	for _, c := range lowercased {
		if c == '"' || c == '(' || c == ')' {
			continue
		}
		bare.WriteRune(c)
	}
	return strings.Join(strings.Fields(bare.String()), " ")
}

func stripTypeCasts(expr string) string {
	runes := []rune(expr)
	var out strings.Builder
	out.Grow(len(expr))
	i := 0
	for i < len(runes) {
		if runes[i] == ':' && i+1 < len(runes) && runes[i+1] == ':' {
			i += 2
			for i < len(runes) && (isAlnumRune(runes[i]) || runes[i] == '_') {
				i++
			}
			if i < len(runes) && runes[i] == '(' {
				depth := 1
				i++
				for i < len(runes) && depth > 0 {
					switch runes[i] {
					case '(':
						depth++
					case ')':
						depth--
					}
					i++
				}
			}
			continue
		}
		out.WriteRune(runes[i])
		i++
	}
	return out.String()
}

func isAlnumRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}
