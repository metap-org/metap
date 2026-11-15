package standard

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestTimestampLayouts(t *testing.T) {
	ts := time.Date(2026, 11, 15, 9, 8, 7, 123456789, time.FixedZone("x", 7*3600))
	assert.Equal(t, "2026-11-15T02:08:07.123Z", FormatMillis(ts))
	assert.Equal(t, "2026-11-15T02:08:07.123456789Z", ts.UTC().Format(TimestampNanos))
	assert.Equal(t, "2026-11-15T02:08:07Z", ts.UTC().Format(TimestampSeconds))
}

func TestNowRFC3339ParsesBack(t *testing.T) {
	parsed, err := time.Parse(time.RFC3339, NowRFC3339())
	assert.NoError(t, err)
	assert.WithinDuration(t, time.Now(), parsed, 5*time.Second)
}
