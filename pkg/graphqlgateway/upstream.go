package graphqlgateway

import (
	"fmt"
	"io"
)

// maxUpstreamResponseBytes bounds one upstream response read into
// memory. Portal responses are small pages and records; this is a ceiling, not a target.
const maxUpstreamResponseBytes = 16 << 20

// readUpstream reads an upstream response body, failing rather than truncating when it is over
// maxUpstreamResponseBytes (a truncated JSON body would fail later with a confusing parse error).
func readUpstream(r io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxUpstreamResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxUpstreamResponseBytes {
		return nil, fmt.Errorf("upstream response is over %d bytes", maxUpstreamResponseBytes)
	}
	return raw, nil
}
