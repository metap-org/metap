// Package kernel is what every administrative slice (internal/admin/<slice>) shares: who is calling,
// the error envelope, and the audit record of an administrative change. It depends on nothing in
// internal/admin, so a slice and the older services in package admin can both use it without an
// import cycle, and there is one definition of each of these things instead of one per package.
package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/internal/access"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/permission"
)

type callerKey struct{}

// WithCaller is ctx carrying the authenticated caller a service acts as.
func WithCaller(ctx context.Context, rc *permission.RequestContext) context.Context {
	return context.WithValue(ctx, callerKey{}, rc)
}

// CallerFrom is the caller WithCaller put in ctx; nil when none.
func CallerFrom(ctx context.Context) *permission.RequestContext {
	rc, _ := ctx.Value(callerKey{}).(*permission.RequestContext)
	return rc
}

type authenticatorKey struct{}

// Authenticator resolves the caller when a service call needs it.
type Authenticator func(ctx context.Context) (*permission.RequestContext, error)

// WithAuthenticator is ctx whose caller is resolved by auth at call time rather than up front: a
// token revoked between two calls made with the same ctx fails the second one.
func WithAuthenticator(ctx context.Context, auth Authenticator) context.Context {
	return context.WithValue(ctx, authenticatorKey{}, auth)
}

// Resolve is the caller in ctx (WithCaller, else WithAuthenticator); nil when there is none.
func Resolve(ctx context.Context) (*permission.RequestContext, error) {
	if rc := CallerFrom(ctx); rc != nil {
		return rc, nil
	}
	if auth, ok := ctx.Value(authenticatorKey{}).(Authenticator); ok && auth != nil {
		return auth(ctx)
	}
	return nil, nil
}

// Err is a business failure with the platform's error envelope.
func Err(status uint16, code, message string) error {
	return crud.NewServiceErrorMsg(status, code, message)
}

// Principal is the caller in ctx when there is one: any signed-in subject will do, with no claim
// about its tenant checked here (a service that needs the tenant takes it from its own source).
func Principal(ctx context.Context) (*permission.RequestContext, error) {
	rc, err := Resolve(ctx)
	if err != nil {
		return nil, err
	}
	if rc == nil {
		return nil, Err(http.StatusUnauthorized, errcode.Unauthorized, "Missing or invalid authorization header.")
	}
	return rc, nil
}

// Caller is an authenticated request's identity: its request context, tenant and own member id
// (uuid.Nil for a subject that is not a person).
type Caller struct {
	RC     *permission.RequestContext
	Tenant uuid.UUID
	Self   uuid.UUID
}

// Authenticated is the caller in ctx when there is one and it carries a tenant.
func Authenticated(ctx context.Context) (Caller, error) {
	rc, err := Resolve(ctx)
	if err != nil {
		return Caller{}, err
	}
	if rc == nil {
		return Caller{}, Err(http.StatusUnauthorized, errcode.Unauthorized, "Missing or invalid authorization header.")
	}
	tenant, err := uuid.Parse(rc.TenantID)
	if err != nil {
		return Caller{}, Err(http.StatusUnauthorized, errcode.Unauthorized, "token is missing required claims")
	}
	self, _ := uuid.Parse(rc.UserID)
	return Caller{RC: rc, Tenant: tenant, Self: self}, nil
}

// At is the authenticated caller, who must also pass level.
func At(ctx context.Context, level access.Level) (Caller, error) {
	c, err := Authenticated(ctx)
	if err != nil {
		return Caller{}, err
	}
	if why := access.Refusal(level, c.RC); why != "" {
		return Caller{}, Err(http.StatusForbidden, errcode.Forbidden, why)
	}
	return c, nil
}

// IdentityErr maps pkg/identity's errors onto the platform error envelope; anything it does not
// know is returned as it is (an internal error the HTTP layer logs).
func IdentityErr(err error) error {
	switch {
	case errors.Is(err, identity.ErrNotFound):
		return Err(http.StatusNotFound, errcode.NotFound, "User not found.")
	case errors.Is(err, identity.ErrAlreadyMember):
		return Err(http.StatusConflict, errcode.AlreadyMember, "This person is already a member of the tenant.")
	case errors.Is(err, identity.ErrWeakPassword):
		return &crud.ServiceError{Status: http.StatusBadRequest, Code: errcode.WeakPassword,
			Message: err.Error(), FieldErrors: map[string][]string{"password": {err.Error()}}}
	case errors.Is(err, identity.ErrMFAInvalidCode):
		return Err(http.StatusUnauthorized, errcode.MFAInvalidCode, err.Error())
	case errors.Is(err, identity.ErrMFALocked):
		return Err(http.StatusTooManyRequests, errcode.AccountLocked, err.Error())
	case errors.Is(err, identity.ErrMFAAlreadyOn):
		return Err(http.StatusConflict, errcode.MFAAlreadyOn, err.Error())
	case errors.Is(err, identity.ErrMFANotEnrolled), errors.Is(err, identity.ErrMFAEnrollNotRun):
		return Err(http.StatusConflict, errcode.MFANotEnrolled, err.Error())
	case errors.Is(err, identity.ErrInvalidEmail):
		return &crud.ServiceError{Status: http.StatusBadRequest, Code: errcode.ValidationFailed,
			Message: err.Error(), FieldErrors: map[string][]string{"email": {err.Error()}}}
	}
	return err
}

// Auditor records the audit entry of an administrative change.
type Auditor interface {
	// Record notes that c changed (action) the administered thing (entity, id): a before/after pair
	// per field. Best-effort: the change has already happened, and a failure to note it must not turn
	// a successful change into a failed response.
	Record(ctx context.Context, c Caller, entity string, id uuid.UUID, action audit.AuditAction, diff map[string][2]any)
}

// StoreAuditor is the Auditor that writes to an audit.Store (nil records nothing).
type StoreAuditor struct {
	Store audit.Store
	Now   func() time.Time
}

func (a StoreAuditor) Record(ctx context.Context, c Caller, entity string, id uuid.UUID, action audit.AuditAction, diff map[string][2]any) {
	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	WriteAudit(ctx, a.Store, c.Tenant, c.Self, entity, id, action, diff, now)
}

// WriteAudit records one audit entry for an administrative change: who (actor, uuid.Nil for none),
// on which administered thing (entity, id), what kind of change, and a before/after pair per field.
// A nil store records nothing.
func WriteAudit(ctx context.Context, store audit.Store, tenant, actor uuid.UUID, entity string, id uuid.UUID,
	action audit.AuditAction, diff map[string][2]any, now time.Time) {
	if store == nil {
		return
	}
	obj := audit.JsonObject{}
	for field, ba := range diff {
		raw, err := json.Marshal(map[string]any{"before": ba[0], "after": ba[1]})
		if err == nil {
			obj[field] = raw
		}
	}
	entry := audit.Entry{TenantID: tenant, Entity: entity, RecordID: id, Action: action, Diff: obj, OccurredAt: now}
	if actor != uuid.Nil {
		entry.ActorUserID = &actor
	}
	_ = store.Record(ctx, tenant, entry)
}
