package runtime

import "github.com/metap-org/metap/pkg/kit/httpx"

// ParseBearer is httpx.BearerToken, kept for callers that import runtime.
func ParseBearer(header string) (string, bool) { return httpx.BearerToken(header) }
