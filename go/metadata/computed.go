package metadata

import "strings"

// ExpressionTokens extracts every {fieldName} token from expression, in order of appearance,
// duplicates included. An unterminated '{' is not a token.
func ExpressionTokens(expression string) []string {
	var tokens []string
	i := 0
	for i < len(expression) {
		if expression[i] == '{' {
			if relEnd := strings.IndexByte(expression[i+1:], '}'); relEnd >= 0 {
				end := i + 1 + relEnd
				tokens = append(tokens, expression[i+1:end])
				i = end + 1
				continue
			}
		}
		i++
	}
	return tokens
}

// RenderExpression renders expression by replacing every {fieldName} token with resolve's
// result — empty string if resolve returns ("", false) for that name. An unterminated '{' is
// kept literal in the output.
func RenderExpression(expression string, resolve func(name string) (string, bool)) string {
	var result strings.Builder
	i := 0
	for i < len(expression) {
		if expression[i] == '{' {
			if relEnd := strings.IndexByte(expression[i+1:], '}'); relEnd >= 0 {
				end := i + 1 + relEnd
				name := expression[i+1 : end]
				if v, ok := resolve(name); ok {
					result.WriteString(v)
				}
				i = end + 1
				continue
			}
		}
		result.WriteByte(expression[i])
		i++
	}
	return result.String()
}
