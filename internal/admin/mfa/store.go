package mfa

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Store is where a person's second factor lives. It belongs to the person's identity (one factor for
// someone in several tenants), not to one membership. Every error is pkg/auth/identity's
// (ErrNotFound, ErrMFAAlreadyOn, ErrMFAEnrollNotRun, ErrMFANotEnrolled, ErrMFAInvalidCode,
// ErrMFALocked), which kernel.IdentityErr maps onto the platform envelope.
//
// Disable and Regenerate check a code and then act in ONE unit of work, and a wrong code must still
// be counted (it is what stops guessing): how that is done is the store's business, not the
// service's.
type Store interface {
	// IdentityOf is the person behind a membership; identity.ErrNotFound when there is none.
	IdentityOf(ctx context.Context, tenant, member uuid.UUID) (uuid.UUID, error)
	// Email is the person's address, the account label an authenticator app shows.
	Email(ctx context.Context, id uuid.UUID) (string, error)
	// Status says whether a confirmed factor exists and how many recovery codes are unused (0 when off).
	Status(ctx context.Context, id uuid.UUID) (on bool, codesLeft int, err error)
	// Begin starts (or restarts) enrolment and returns the secret and the otpauth URI the app scans.
	Begin(ctx context.Context, id uuid.UUID, account string, now time.Time) (secret, uri string, err error)
	// Confirm checks the first code from the app, turns the factor on and returns the recovery codes.
	Confirm(ctx context.Context, id uuid.UUID, code string, now time.Time) ([]string, error)
	// Disable checks a current or recovery code and turns the factor off.
	Disable(ctx context.Context, id uuid.UUID, code string, now time.Time) error
	// Regenerate checks a current or recovery code and replaces every recovery code.
	Regenerate(ctx context.Context, id uuid.UUID, code string, now time.Time) ([]string, error)
}
