package credentials

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/secrets"
)

// AuditEntity is the audit-trail entity credential changes are recorded under (the record is the
// credential). The trail names the credential and where it lives, never its value.
const AuditEntity = kernel.AuditEntityCredentials

// Where a credential is saved (the two homes of the Credentials tab).
const (
	SourceDatabase = "database" // encrypted in metap's own database
	SourceExternal = "external" // the deployment's secret manager (Vault, AWS, GCP)
)

// What a credential is, so a form offers only the ones that fit.
const (
	KindAuthorization = "authorization" // the value of an HTTP Authorization header, e.g. "Bearer <token>"
	KindSecret        = "secret"        // any opaque secret (an API key, a password)
	KindUserPass      = "userpass"      // a username and a password (sent as HTTP Basic by a webhook)
)

const (
	maxValue    = 4096
	maxName     = 64
	maxUsername = 128
)

func fieldInvalid(field, message string) error {
	return &crud.ServiceError{Status: http.StatusBadRequest, Code: errcode.ValidationFailed,
		Message: message, FieldErrors: map[string][]string{field: {message}}}
}

func hasControl(s string) bool {
	for _, c := range s {
		if c < ' ' || c == 0x7f {
			return true
		}
	}
	return false
}

// checkName trims a credential's name and returns it, or the 400. Pure.
func checkName(name string) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", fieldInvalid("name", "A name is required.")
	case utf8.RuneCountInString(name) > maxName:
		return "", fieldInvalid("name", "The name must be at most 64 characters.")
	case hasControl(name):
		return "", fieldInvalid("name", "The name must not contain control characters.")
	}
	return name, nil
}

// checkValue: an Authorization value must be a printable single-line header value; a generic secret
// (and a userpass's password) only needs to be non-empty and bounded. No stored value may look like an
// encoded userpass. Pure.
func checkValue(kind, value string) error {
	switch {
	case value == "":
		return fieldInvalid("value", "A value is required.")
	case len(value) > maxValue:
		return fieldInvalid("value", "The value must be at most 4096 characters.")
	case kind != KindUserPass && secrets.IsReservedCredentialValue(value):
		return fieldInvalid("value", "The value must not start with \"userpass:\" (reserved).")
	}
	if kind == KindAuthorization {
		for _, c := range value {
			if c == '\t' {
				continue
			}
			if c < ' ' || c > '~' {
				return fieldInvalid("value", "An Authorization value must contain only printable ASCII (no newlines, no control characters).")
			}
		}
	}
	return nil
}

// checkUsername: HTTP Basic cannot carry a colon in the username. Pure.
func checkUsername(username string) (string, error) {
	username = strings.TrimSpace(username)
	switch {
	case username == "":
		return "", fieldInvalid("username", "A username is required.")
	case utf8.RuneCountInString(username) > maxUsername:
		return "", fieldInvalid("username", "The username must be at most 128 characters.")
	case strings.ContainsRune(username, ':'):
		return "", fieldInvalid("username", "The username must not contain a colon.")
	case hasControl(username):
		return "", fieldInvalid("username", "The username must not contain control characters.")
	}
	return username, nil
}

func validKind(kind string) bool {
	return kind == KindAuthorization || kind == KindSecret || kind == KindUserPass
}

// checkHome is the refusal when this deployment cannot keep a value in source. Pure.
func checkHome(caps kernel.CredentialCapabilities, source string) error {
	switch source {
	case SourceDatabase:
		if !caps.Database {
			return kernel.Err(http.StatusConflict, errcode.Forbidden, "Storing credentials in the database is not enabled on this deployment.")
		}
	case SourceExternal:
		if !caps.External {
			return kernel.Err(http.StatusConflict, errcode.Forbidden, "No external secret manager (Vault, AWS or GCP) is configured on this deployment.")
		}
	default:
		return fieldInvalid("source", "`source` must be database or external.")
	}
	return nil
}

// view is a credential as the API shows it. Pure.
func view(c Row, used []*CredentialUse) *Credential {
	if used == nil {
		used = []*CredentialUse{}
	}
	return &Credential{Id: c.ID.String(), Name: c.Name, Description: c.Description, Kind: c.Kind, Source: c.Source, Username: c.Username,
		CreatedAt: kernel.FormatTime(&c.CreatedAt), UpdatedAt: kernel.FormatTime(&c.UpdatedAt), UsedBy: used}
}

// inUseError is the 409 naming what still uses a credential. Pure.
func inUseError(uses []*CredentialUse) error {
	names := make([]string, len(uses))
	for i, u := range uses {
		names[i] = u.Name
	}
	return &crud.ServiceError{Status: http.StatusConflict, Code: errcode.RecordReferenced,
		Message:     "This credential is still used by " + strings.Join(names, ", ") + ". Point those at another credential first.",
		FieldErrors: map[string][]string{"usedBy": names}}
}
