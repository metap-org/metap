package graphqlgateway

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadUpstreamFailsInsteadOfTruncating(t *testing.T) {
	ok, err := readUpstream(strings.NewReader(`{"data":{}}`))
	require.NoError(t, err)
	assert.Equal(t, `{"data":{}}`, string(ok))

	_, err = readUpstream(strings.NewReader(strings.Repeat("x", maxUpstreamResponseBytes+1)))
	require.Error(t, err)
}
