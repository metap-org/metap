package apigateway

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseUpstreams(t *testing.T) {
	ups, err := ParseUpstreams("zones=http://zones:3000|/auth/,/me/ ; scanning=http://scanning:3010")
	require.NoError(t, err)
	require.Len(t, ups, 2)
	assert.Equal(t, Upstream{Name: "zones", BaseURL: "http://zones:3000", Prefixes: []string{"/auth/", "/me/"}}, ups[0])
	assert.Equal(t, Upstream{Name: "scanning", BaseURL: "http://scanning:3010"}, ups[1])

	_, err = ParseUpstreams("")
	assert.Error(t, err)
	_, err = ParseUpstreams("http://no-name")
	assert.Error(t, err)
}
