package signin

// The pure part of sign-in: the error envelope, how an identity error reads to a person, and the small
// decisions that need no store (which password prompt an invitation gets, which tenant names are
// reserved, which landing page is safe). Unit-tested without a database.

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/kit/errcode"
)

func serviceErr(status uint16, code, message string, fieldErrors map[string][]string) error {
	return &crud.ServiceError{Status: status, Code: code, Message: message, FieldErrors: fieldErrors}
}

// IdentityError maps pkg/identity's sign-in and password errors onto the platform error envelope;
// anything else is returned as it is (an internal error).
func IdentityError(err error) error {
	switch {
	case errors.Is(err, identity.ErrInvalidCredentials):
		return serviceErr(http.StatusUnauthorized, errcode.InvalidCredentials, "Invalid email or password.", nil)
	case errors.Is(err, identity.ErrLocked):
		return serviceErr(http.StatusTooManyRequests, errcode.AccountLocked, err.Error(), nil)
	case errors.Is(err, identity.ErrDisabled):
		return serviceErr(http.StatusForbidden, errcode.AccountDisabled, err.Error(), nil)
	case errors.Is(err, identity.ErrWeakPassword):
		return serviceErr(http.StatusBadRequest, errcode.WeakPassword, err.Error(), map[string][]string{"password": {err.Error()}})
	}
	return err
}

// MFAError maps pkg/identity's second-factor errors onto the platform error envelope.
func MFAError(err error) error {
	switch {
	case errors.Is(err, identity.ErrMFAChallenge):
		return serviceErr(http.StatusUnauthorized, errcode.MFAChallenge, err.Error(), nil)
	case errors.Is(err, identity.ErrMFAInvalidCode):
		return serviceErr(http.StatusUnauthorized, errcode.MFAInvalidCode, err.Error(), nil)
	case errors.Is(err, identity.ErrMFALocked):
		return serviceErr(http.StatusTooManyRequests, errcode.AccountLocked, err.Error(), nil)
	case errors.Is(err, identity.ErrMFAAlreadyOn):
		return serviceErr(http.StatusConflict, errcode.MFAAlreadyOn, err.Error(), nil)
	case errors.Is(err, identity.ErrMFANotEnrolled), errors.Is(err, identity.ErrMFAEnrollNotRun):
		return serviceErr(http.StatusConflict, errcode.MFANotEnrolled, err.Error(), nil)
	}
	return err
}

// InvitationError maps pkg/identity's invitation and password-reset errors onto the platform error
// envelope; the sign-in errors fall through to IdentityError.
func InvitationError(err error) error {
	switch {
	case errors.Is(err, identity.ErrInvitationInvalid):
		return serviceErr(http.StatusBadRequest, errcode.InvitationInvalid, err.Error(), nil)
	case errors.Is(err, identity.ErrInvitationNeedsSignIn):
		return serviceErr(http.StatusConflict, errcode.InvitationNeedsSignIn, err.Error(), nil)
	case errors.Is(err, identity.ErrResetInvalid):
		return serviceErr(http.StatusBadRequest, errcode.ResetTokenInvalid, err.Error(), nil)
	}
	return IdentityError(err)
}

func passwordMode(setsPassword, hasPassword bool) string {
	switch {
	case setsPassword:
		return "set"
	case hasPassword:
		return "confirm"
	default:
		return "provider"
	}
}

// normalizeTenantName folds a name to lower-case letters and digits, so punctuation, spacing and
// case cannot be used to slip past the blocklist.
func normalizeTenantName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// TenantNameBlocked reports whether name is reserved or in the operator's list.
func TenantNameBlocked(name string, extra []string) bool {
	n := normalizeTenantName(name)
	if n == "" {
		return true
	}
	for _, list := range [][]string{reservedTenantNames, extra} {
		for _, b := range list {
			if n == normalizeTenantName(b) {
				return true
			}
		}
	}
	return false
}

func errSignupDisabled() error {
	return serviceErr(http.StatusForbidden, errcode.SignupDisabled, "Self-service signup is not open on this deployment.", nil)
}

// SameOrigin reports whether raw is an absolute http(s) URL on the same scheme, host and port as base.
func SameOrigin(raw, base string) bool {
	u, err := url.Parse(raw)
	b, err2 := url.Parse(base)
	if err != nil || err2 != nil || u.Host == "" || b.Host == "" {
		return false
	}
	return (u.Scheme == "https" || u.Scheme == "http") && u.Scheme == b.Scheme && strings.EqualFold(u.Host, b.Host)
}

func errSSONotSetUp() error {
	return serviceErr(http.StatusNotFound, errcode.NotFound, "Single sign-on is not set up for this workspace.", nil)
}

func errSSOUnavailable() error {
	return serviceErr(http.StatusServiceUnavailable, errcode.SSOFailed, "Single sign-on is not available right now.", nil)
}

func errSSOUnreachable() error {
	return serviceErr(http.StatusBadGateway, errcode.SSOFailed, "The identity provider could not be reached.", nil)
}

func errSSOUnverified() error {
	return serviceErr(http.StatusForbidden, errcode.SSOFailed, "The identity provider's answer could not be verified.", nil)
}

func errSSOState() error {
	return serviceErr(http.StatusBadRequest, errcode.SSOFailed, "The sign-in link is unknown, used or expired. Start again.", nil)
}
