package runtime

import "strings"

// ParseBearer strips the "Bearer " prefix from an Authorization header value, trims incidental
// whitespace, and returns ("", false) if the prefix is missing or the token is empty.
func ParseBearer(header string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" {
		return "", false
	}
	return token, true
}
