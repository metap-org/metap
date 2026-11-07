// "email" targets — sent via SMTP, auto-appending which record triggered the firing for an
// on_transition/on_record_event-triggered job. Ported from metap-cron-scheduler's
// executor/email.rs.
//
// Divergence from the Rust original: lettre distinguishes a TLS "relay" builder from a
// "builder_dangerous" plaintext one (local dev against Mailhog, which speaks plain SMTP with no
// STARTTLS/auth). Go's net/smtp.SendMail negotiates STARTTLS opportunistically when the server
// advertises it and skips auth entirely when no Auth is passed, which is the same two cases
// (authenticated relay vs. plaintext dev target) reached through stdlib's own single code path
// instead of two explicit builders.
package cronscheduler

import (
	"encoding/json"
	"fmt"
	"net/smtp"
	"strings"

	"github.com/google/uuid"
)

// emailRecipients accepts target_config.to as either a single address or a list of them.
type emailRecipients []string

func (r *emailRecipients) UnmarshalJSON(data []byte) error {
	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		*r = []string{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return err
	}
	*r = many
	return nil
}

type emailConfig struct {
	To      emailRecipients `json:"to"`
	Subject string          `json:"subject"`
	Body    string          `json:"body"`
}

// RunEmail runs one "email" target firing (or step).
func RunEmail(smtpCfg SmtpConfig, triggerEntity *string, triggerRecordID *uuid.UUID, targetConfig json.RawMessage) (json.RawMessage, error) {
	var cfg emailConfig
	if err := json.Unmarshal(targetConfig, &cfg); err != nil {
		return nil, err
	}
	if smtpCfg.Host == "" {
		return nil, fmt.Errorf("email target requires SMTP_HOST to be configured")
	}
	if smtpCfg.From == "" {
		return nil, fmt.Errorf("email target requires SMTP_FROM to be configured")
	}
	if len(cfg.To) == 0 {
		return nil, fmt.Errorf("email target_config.to must not be empty")
	}

	// Auto-append which record actually caused this firing, same intent as RunWebhook injecting
	// jobId/runId into its body — triggerEntity/triggerRecordID are only set for an
	// on_transition/on_record_event-triggered job, so a plain schedule job's email is left as the
	// admin wrote it.
	body := cfg.Body
	if triggerEntity != nil {
		body += fmt.Sprintf("\n\n---\nTriggered by: %s", *triggerEntity)
		if triggerRecordID != nil {
			body += fmt.Sprintf(" (record %s)", triggerRecordID.String())
		}
	}

	addr := fmt.Sprintf("%s:%d", smtpCfg.Host, smtpCfg.Port)
	var auth smtp.Auth
	if smtpCfg.User != "" && smtpCfg.Password != "" {
		auth = smtp.PlainAuth("", smtpCfg.User, smtpCfg.Password, smtpCfg.Host)
	}

	msg := buildMimeMessage(smtpCfg.From, cfg.To, cfg.Subject, body)
	if err := smtp.SendMail(addr, auth, smtpCfg.From, cfg.To, msg); err != nil {
		return nil, err
	}

	return json.Marshal(map[string]any{"to": []string(cfg.To), "subject": cfg.Subject})
}

func buildMimeMessage(from string, to []string, subject, body string) []byte {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("From: %s\r\n", from))
	b.WriteString(fmt.Sprintf("To: %s\r\n", strings.Join(to, ", ")))
	b.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	b.WriteString("\r\n")
	b.WriteString(body)
	return []byte(b.String())
}
