package cronscheduler

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmailRecipientsAcceptsASingleAddress(t *testing.T) {
	var cfg emailConfig
	require.NoError(t, json.Unmarshal([]byte(`{"to":"a@example.com","subject":"s","body":"b"}`), &cfg))
	assert.Equal(t, []string{"a@example.com"}, []string(cfg.To), "got %v", cfg.To)
}

func TestEmailRecipientsAcceptsAList(t *testing.T) {
	var cfg emailConfig
	require.NoError(t, json.Unmarshal([]byte(`{"to":["a@example.com","b@example.com"],"subject":"s","body":"b"}`), &cfg))
	assert.Equal(t, []string{"a@example.com", "b@example.com"}, []string(cfg.To), "got %v", cfg.To)
}

func TestRunEmailFailsCleanlyWithoutSMTPHostConfigured(t *testing.T) {
	targetConfig := []byte(`{"to":"a@example.com","subject":"s","body":"b"}`)
	_, err := RunEmail(SmtpConfig{}, nil, nil, targetConfig)
	assert.Error(t, err, "expected an error when SMTP_HOST is not configured")
}
