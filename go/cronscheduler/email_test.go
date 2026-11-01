package cronscheduler

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestEmailRecipientsAcceptsASingleAddress(t *testing.T) {
	var cfg emailConfig
	if err := json.Unmarshal([]byte(`{"to":"a@example.com","subject":"s","body":"b"}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual([]string(cfg.To), []string{"a@example.com"}) {
		t.Errorf("got %v", cfg.To)
	}
}

func TestEmailRecipientsAcceptsAList(t *testing.T) {
	var cfg emailConfig
	if err := json.Unmarshal([]byte(`{"to":["a@example.com","b@example.com"],"subject":"s","body":"b"}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual([]string(cfg.To), []string{"a@example.com", "b@example.com"}) {
		t.Errorf("got %v", cfg.To)
	}
}

func TestRunEmailFailsCleanlyWithoutSMTPHostConfigured(t *testing.T) {
	targetConfig := []byte(`{"to":"a@example.com","subject":"s","body":"b"}`)
	if _, err := RunEmail(SmtpConfig{}, nil, nil, targetConfig); err == nil {
		t.Error("expected an error when SMTP_HOST is not configured")
	}
}
