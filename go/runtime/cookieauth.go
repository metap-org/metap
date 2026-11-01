package runtime

const (
	SessionCookieName = "metap_session"
	CSRFCookieName    = "metap_csrf"
	// CSRFHeaderName is the request header a browser client must echo the CSRFCookieName
	// cookie's value into for any state-changing (non-GET/HEAD/OPTIONS) cookie-authenticated
	// request — the standard double-submit CSRF defense.
	CSRFHeaderName = "x-csrf-token"
)

// RequiresCSRFCheck reports whether a cookie-authenticated request of this method must carry a
// matching CSRFHeaderName header. Safe methods are exempt because they must not change state.
// Not sufficient on its own for POST /graphql — every GraphQL operation rides the same HTTP
// method regardless of query vs. mutation, so a caller there must always send the CSRF header.
func RequiresCSRFCheck(method string) bool {
	switch method {
	case "GET", "HEAD", "OPTIONS":
		return false
	default:
		return true
	}
}

// CSRFMatches is the double-submit comparison itself: the cookie value must be present,
// non-empty, and equal to the header value. Both-absent and both-empty are rejected explicitly.
func CSRFMatches(cookieValue, headerValue string, cookieOK, headerOK bool) bool {
	if !cookieOK || !headerOK {
		return false
	}
	return cookieValue != "" && cookieValue == headerValue
}
