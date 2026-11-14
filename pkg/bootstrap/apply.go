package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/peripherals"
	"github.com/metap-org/metap/pkg/permission"
)

// PolicyAdmin is the part of *permission.PermissionService Apply needs.
type PolicyAdmin interface {
	ListPolicies(ctx context.Context, tenantID uuid.UUID, entity string, hasEntity bool) ([]permission.PolicyRow, error)
	CreatePolicy(ctx context.Context, tenantID uuid.UUID, entity, action string, roles []string, hasRoles bool,
		condition *permission.PolicyCondition, createdBy *uuid.UUID, field string, subject *permission.PolicySubject,
		effect permission.PolicyEffect) (permission.PolicyRow, error)
}

// Identity creates accounts and role assignments.
type Identity interface {
	FindUserByEmail(ctx context.Context, tenantID uuid.UUID, email string) (uuid.UUID, bool, error)
	CreateUser(ctx context.Context, tenantID uuid.UUID, email, password string) (uuid.UUID, error)
	AssignRole(ctx context.Context, tenantID, userID uuid.UUID, role string) error
}

// PasswordSource resolves a User.PasswordRef. ok=false means "not provided".
type PasswordSource func(ctx context.Context, ref string) (password string, ok bool, err error)

// EnvPasswords reads the password from the environment variable named by the ref; an empty value
// counts as not provided.
func EnvPasswords(_ context.Context, ref string) (string, bool, error) {
	v, ok := os.LookupEnv(ref)
	if !ok || v == "" {
		return "", false, nil
	}
	return v, true, nil
}

// Deps are what Apply talks to.
type Deps struct {
	Registry  *metadata.MetadataRegistry
	Policies  PolicyAdmin
	Identity  Identity
	Passwords PasswordSource // default EnvPasswords
}

// Options tune Apply.
type Options struct {
	// Production makes a new user without a resolvable password a hard error. Outside production
	// such a user is skipped with a warning. There is never a default password.
	Production bool
}

// Result reports what an Apply did.
type Result struct {
	PoliciesCreated  int
	PoliciesExisting int
	UsersCreated     []string
	UsersExisting    []string
	UsersSkipped     []string // no password provided (non-production only)
}

// Apply makes tenantID match b, additively. Anything already present is left exactly as it is,
// including edits an admin made after an earlier run: a policy row is only created when no
// identical one exists, and an existing account is never touched (not its password, not its roles).
// Re-running is therefore always safe.
func Apply(ctx context.Context, d Deps, tenantID uuid.UUID, b Bootstrap, opts Options) (Result, error) {
	var res Result
	if err := b.Validate(); err != nil {
		return res, err
	}
	if d.Passwords == nil {
		d.Passwords = EnvPasswords
	}

	// Preflight the accounts first so a missing production secret aborts before anything is written.
	type newUser struct {
		u        User
		password string
	}
	var toCreate []newUser
	for _, u := range b.Users {
		if _, exists, err := d.Identity.FindUserByEmail(ctx, tenantID, u.Email); err != nil {
			return res, err
		} else if exists {
			res.UsersExisting = append(res.UsersExisting, u.Email)
			continue
		}
		pw, ok, err := d.Passwords(ctx, u.PasswordRef)
		if err != nil {
			return res, fmt.Errorf("bootstrap: password for %s: %w", u.Email, err)
		}
		if !ok {
			if opts.Production {
				return res, fmt.Errorf("bootstrap: no password provided for new user %s (ref %q); refusing to continue in production", u.Email, u.PasswordRef)
			}
			slog.Warn("bootstrap: skipping user without a password", "email", u.Email, "ref", u.PasswordRef)
			res.UsersSkipped = append(res.UsersSkipped, u.Email)
			continue
		}
		toCreate = append(toCreate, newUser{u, pw})
	}

	if err := applyGrants(ctx, d, tenantID, b.Grants, &res); err != nil {
		return res, err
	}

	for _, n := range toCreate {
		id, err := d.Identity.CreateUser(ctx, tenantID, n.u.Email, n.password)
		if err != nil {
			return res, fmt.Errorf("bootstrap: create user %s: %w", n.u.Email, err)
		}
		for _, role := range n.u.Roles {
			if err := d.Identity.AssignRole(ctx, tenantID, id, role); err != nil {
				return res, fmt.Errorf("bootstrap: assign %s to %s: %w", role, n.u.Email, err)
			}
		}
		res.UsersCreated = append(res.UsersCreated, n.u.Email)
	}
	return res, nil
}

func applyGrants(ctx context.Context, d Deps, tenantID uuid.UUID, grants []Grant, res *Result) error {
	// Concrete (entity, action, role) triples, deduplicated, in a stable order.
	type triple struct{ entity, action, role string }
	seen := map[triple]bool{}
	var wanted []triple
	defs := d.Registry.Definitions()
	for _, g := range grants {
		actions := expandActions(g.Actions)
		for _, pattern := range g.Entities {
			matched := false
			for _, def := range defs {
				if !entityMatches(pattern, def.Name) {
					continue
				}
				matched = true
				for _, a := range actions {
					t := triple{def.Name, a, g.Role}
					if !seen[t] {
						seen[t] = true
						wanted = append(wanted, t)
					}
				}
			}
			if !matched {
				// A typo'd or not-yet-deployed entity must not silently grant nothing.
				return fmt.Errorf("bootstrap: grant for role %q: entity pattern %q matches no registered entity", g.Role, pattern)
			}
		}
	}

	existing := map[string][]permission.PolicyRow{}
	for _, t := range wanted {
		rows, ok := existing[t.entity]
		if !ok {
			var err error
			rows, err = d.Policies.ListPolicies(ctx, tenantID, t.entity, true)
			if err != nil {
				return err
			}
			existing[t.entity] = rows
		}
		if slices.ContainsFunc(rows, func(r permission.PolicyRow) bool { return isBasicGrant(r, t.action, t.role) }) {
			res.PoliciesExisting++
			continue
		}
		row, err := d.Policies.CreatePolicy(ctx, tenantID, t.entity, t.action, []string{t.role}, true,
			nil, nil, "", nil, permission.EffectAllow)
		if err != nil {
			return fmt.Errorf("bootstrap: grant %s %s on %s: %w", t.role, t.action, t.entity, err)
		}
		existing[t.entity] = append(existing[t.entity], row)
		res.PoliciesCreated++
	}
	return nil
}

// isBasicGrant matches the "role may do action" shape SyncBasicPolicies writes and the admin UI's
// basic tab shows: allow, context subject, no field, no condition, exactly this one role.
func isBasicGrant(r permission.PolicyRow, action, role string) bool {
	return r.Action == action && r.Field == "" && r.Subject == "context" &&
		r.Effect == permission.EffectAllow && r.Condition == nil &&
		r.HasRoles && len(r.Roles) == 1 && r.Roles[0] == role
}

// PgIdentity is the Postgres-backed Identity, over a pool or an open transaction.
type PgIdentity struct{ DB peripherals.DBTX }

func (p PgIdentity) FindUserByEmail(ctx context.Context, tenantID uuid.UUID, email string) (uuid.UUID, bool, error) {
	var id uuid.UUID
	err := p.DB.QueryRow(ctx, "SELECT id FROM users WHERE tenant_id = $1 AND lower(email) = $2",
		tenantID, strings.ToLower(email)).Scan(&id)
	if err == pgx.ErrNoRows {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, err
	}
	return id, true, nil
}

func (p PgIdentity) CreateUser(ctx context.Context, tenantID uuid.UUID, email, password string) (uuid.UUID, error) {
	u, err := peripherals.CreateUser(ctx, p.DB, tenantID, email, password)
	if err != nil {
		return uuid.Nil, err
	}
	return u.ID, nil
}

func (p PgIdentity) AssignRole(ctx context.Context, tenantID, userID uuid.UUID, role string) error {
	return peripherals.AssignRole(ctx, p.DB, tenantID, userID, role, nil)
}

// ScopedIdentity is an Identity that resolves the right database per tenant (a dedicated_db tenant
// keeps its users in its own database), through control.Router.PoolFor.
type ScopedIdentity struct {
	Pool func(ctx context.Context, tenantID uuid.UUID) (peripherals.DBTX, error)
}

func (s ScopedIdentity) at(ctx context.Context, tenantID uuid.UUID) (PgIdentity, error) {
	db, err := s.Pool(ctx, tenantID)
	if err != nil {
		return PgIdentity{}, err
	}
	return PgIdentity{DB: db}, nil
}

func (s ScopedIdentity) FindUserByEmail(ctx context.Context, tenantID uuid.UUID, email string) (uuid.UUID, bool, error) {
	p, err := s.at(ctx, tenantID)
	if err != nil {
		return uuid.Nil, false, err
	}
	return p.FindUserByEmail(ctx, tenantID, email)
}

func (s ScopedIdentity) CreateUser(ctx context.Context, tenantID uuid.UUID, email, password string) (uuid.UUID, error) {
	p, err := s.at(ctx, tenantID)
	if err != nil {
		return uuid.Nil, err
	}
	return p.CreateUser(ctx, tenantID, email, password)
}

func (s ScopedIdentity) AssignRole(ctx context.Context, tenantID, userID uuid.UUID, role string) error {
	p, err := s.at(ctx, tenantID)
	if err != nil {
		return err
	}
	return p.AssignRole(ctx, tenantID, userID, role)
}
