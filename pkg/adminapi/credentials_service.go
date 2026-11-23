package adminapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/control"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/jobs/cron"
	"github.com/metap-org/metap/pkg/kit/errcode"
)

// AuditEntityCredentials is the audit-trail entity credential changes are recorded under (the
// record is the credential). The trail names the credential and where it lives, never its value.
const AuditEntityCredentials = "metadata.tenant_credentials"

// Where a credential is saved (the two homes of the Credentials tab).
const (
	CredentialSourceDatabase = "database" // encrypted in metap's own database
	CredentialSourceExternal = "external" // the deployment's secret manager (Vault, AWS, GCP)
)

// What a credential is, so a form offers only the ones that fit.
const (
	CredentialKindAuthorization = "authorization" // the value of an HTTP Authorization header, e.g. "Bearer <token>"
	CredentialKindSecret        = "secret"        // any opaque secret (an API key, a password)
	CredentialKindUserPass      = "userpass"      // a username and a password (sent as HTTP Basic by a webhook)
)

const (
	maxCredentialValue    = 4096
	maxCredentialName     = 64
	maxCredentialUsername = 128
)

// CredentialCapabilities: which homes this deployment can save a credential to.
type CredentialCapabilities struct {
	Database     bool
	External     bool
	ExternalKind string // "vault", "aws", "gcp"; "env" when none is configured
}

// CredentialManager is the store behind the Credentials tab (implemented over secrets.Manager).
// ref is always control.TenantCredentialRef — derived from the credential's own row, never supplied.
type CredentialManager interface {
	Capabilities() CredentialCapabilities
	Put(ctx context.Context, ref, source, value string) error
	Remove(ctx context.Context, ref string) error
	// Get reads a stored value for the service itself (changing a userpass's username keeps its
	// password); it is never sent to a client.
	Get(ctx context.Context, ref string) (string, error)
}

// CredentialsService keeps a tenant's named credentials: the row (name, kind, where) in
// metadata.tenant_credentials, the value in the chosen home. Other features link to a credential
// by id (a cron webhook's `credentialId`); the value is write-only.
type CredentialsService struct {
	d identityDeps
}

func NewCredentialsService(cfg IdentityConfig) *CredentialsService {
	return &CredentialsService{d: identityDeps{cfg: cfg}}
}

func (s *CredentialsService) manager(ctx context.Context) (CredentialManager, error) {
	if s.d.cfg.Credentials == nil {
		return nil, svcErr(ctx, http.StatusNotImplemented, errcode.NotFound, "Credential storage is not available on this service.")
	}
	return s.d.cfg.Credentials, nil
}

func fieldInvalid(ctx context.Context, field, message string) error {
	return sendServiceError(ctx, &crud.ServiceError{Status: http.StatusBadRequest, Code: errcode.ValidationFailed,
		Message: message, FieldErrors: map[string][]string{field: {message}}})
}

func validateCredentialName(ctx context.Context, name string) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", fieldInvalid(ctx, "name", "A name is required.")
	case utf8.RuneCountInString(name) > maxCredentialName:
		return "", fieldInvalid(ctx, "name", "The name must be at most 64 characters.")
	}
	for _, c := range name {
		if c < ' ' || c == 0x7f {
			return "", fieldInvalid(ctx, "name", "The name must not contain control characters.")
		}
	}
	return name, nil
}

// validateCredentialValue: an Authorization value must be a printable single-line header value;
// a generic secret (and a userpass's password) only needs to be non-empty and bounded. No stored
// value may look like an encoded userpass.
func validateCredentialValue(ctx context.Context, kind, value string) error {
	field := "value"
	switch {
	case value == "":
		return fieldInvalid(ctx, field, "A value is required.")
	case len(value) > maxCredentialValue:
		return fieldInvalid(ctx, field, "The value must be at most 4096 characters.")
	case kind != CredentialKindUserPass && control.IsReservedCredentialValue(value):
		return fieldInvalid(ctx, field, "The value must not start with \"userpass:\" (reserved).")
	}
	if kind == CredentialKindAuthorization {
		for _, c := range value {
			if c == '\t' {
				continue
			}
			if c < ' ' || c > '~' {
				return fieldInvalid(ctx, field, "An Authorization value must contain only printable ASCII (no newlines, no control characters).")
			}
		}
	}
	return nil
}

// validateCredentialUsername: HTTP Basic cannot carry a colon in the username.
func validateCredentialUsername(ctx context.Context, username string) (string, error) {
	username = strings.TrimSpace(username)
	switch {
	case username == "":
		return "", fieldInvalid(ctx, "username", "A username is required.")
	case utf8.RuneCountInString(username) > maxCredentialUsername:
		return "", fieldInvalid(ctx, "username", "The username must be at most 128 characters.")
	case strings.ContainsRune(username, ':'):
		return "", fieldInvalid(ctx, "username", "The username must not contain a colon.")
	}
	for _, c := range username {
		if c < ' ' || c == 0x7f {
			return "", fieldInvalid(ctx, "username", "The username must not contain control characters.")
		}
	}
	return username, nil
}

func validCredentialKind(kind string) bool {
	return kind == CredentialKindAuthorization || kind == CredentialKindSecret || kind == CredentialKindUserPass
}

type credentialRow struct {
	id                   uuid.UUID
	name, description    string
	kind, source         string
	username             string
	createdAt, updatedAt time.Time
}

const credentialColumns = `id, name, description, kind, source, username, created_at, updated_at`

func scanCredential(row pgx.Row) (credentialRow, error) {
	var c credentialRow
	err := row.Scan(&c.id, &c.name, &c.description, &c.kind, &c.source, &c.username, &c.createdAt, &c.updatedAt)
	return c, err
}

func (s *CredentialsService) get(ctx context.Context, tenant, id uuid.UUID) (credentialRow, error) {
	c, err := scanCredential(s.d.cfg.Shared.QueryRow(ctx,
		`SELECT `+credentialColumns+` FROM metadata.tenant_credentials WHERE tenant_id = $1 AND id = $2`, tenant, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, svcErr(ctx, http.StatusNotFound, errcode.NotFound, "Credential not found.")
	}
	return c, err
}

// usage maps each credential id to the cron jobs of the tenant that use it.
func (s *CredentialsService) usage(ctx context.Context, tenant uuid.UUID) (map[uuid.UUID][]*CredentialUse, error) {
	jobs, err := cron.ListJobs(ctx, s.d.cfg.Shared, tenant)
	if err != nil {
		return nil, err
	}
	used := map[uuid.UUID][]*CredentialUse{}
	for _, job := range jobs {
		for _, id := range cron.ReferencedCredentials(job.TargetType, job.TargetConfig) {
			used[id] = append(used[id], &CredentialUse{Kind: "cronJob", Id: job.ID.String(), Name: job.Name})
		}
	}
	return used, nil
}

func toCredential(c credentialRow, used []*CredentialUse) *Credential {
	if used == nil {
		used = []*CredentialUse{}
	}
	return &Credential{Id: c.id.String(), Name: c.name, Description: c.description, Kind: c.kind, Source: c.source, Username: c.username,
		CreatedAt: formatTime(&c.createdAt), UpdatedAt: formatTime(&c.updatedAt), UsedBy: used}
}

func (s *CredentialsService) view(ctx context.Context, tenant uuid.UUID, m CredentialManager) (*CredentialsView, error) {
	caps := m.Capabilities()
	out := &CredentialsView{DatabaseAvailable: caps.Database, ExternalAvailable: caps.External, ExternalKind: caps.ExternalKind,
		Credentials: []*Credential{}}
	rows, err := s.d.cfg.Shared.Query(ctx,
		`SELECT `+credentialColumns+` FROM metadata.tenant_credentials WHERE tenant_id = $1 ORDER BY lower(name)`, tenant)
	if err != nil {
		return nil, rpcError(ctx, err)
	}
	defer rows.Close()
	var list []credentialRow
	for rows.Next() {
		c, err := scanCredential(rows)
		if err != nil {
			return nil, rpcError(ctx, err)
		}
		list = append(list, c)
	}
	if err := rows.Err(); err != nil {
		return nil, rpcError(ctx, err)
	}
	used, err := s.usage(ctx, tenant)
	if err != nil {
		return nil, rpcError(ctx, err)
	}
	for _, c := range list {
		out.Credentials = append(out.Credentials, toCredential(c, used[c.id]))
	}
	return out, nil
}

func (s *CredentialsService) ListCredentials(ctx context.Context, _ *Empty) (*CredentialsView, error) {
	c, err := s.d.admin(ctx)
	if err != nil {
		return nil, err
	}
	m, err := s.manager(ctx)
	if err != nil {
		return nil, err
	}
	return s.view(ctx, c.tenant, m)
}

func checkHome(ctx context.Context, caps CredentialCapabilities, source string) error {
	switch source {
	case CredentialSourceDatabase:
		if !caps.Database {
			return svcErr(ctx, http.StatusConflict, errcode.Forbidden, "Storing credentials in the database is not enabled on this deployment.")
		}
	case CredentialSourceExternal:
		if !caps.External {
			return svcErr(ctx, http.StatusConflict, errcode.Forbidden, "No external secret manager (Vault, AWS or GCP) is configured on this deployment.")
		}
	default:
		return fieldInvalid(ctx, "source", "`source` must be database or external.")
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (s *CredentialsService) CreateCredential(ctx context.Context, req *CreateCredentialRequest) (*CredentialsView, error) {
	c, err := s.d.admin(ctx)
	if err != nil {
		return nil, err
	}
	m, err := s.manager(ctx)
	if err != nil {
		return nil, err
	}
	name, err := validateCredentialName(ctx, req.Name)
	if err != nil {
		return nil, err
	}
	if !validCredentialKind(req.Kind) {
		return nil, fieldInvalid(ctx, "kind", "`kind` must be authorization or secret.")
	}
	if err := checkHome(ctx, m.Capabilities(), req.Source); err != nil {
		return nil, err
	}
	username, stored := "", req.Value
	if req.Kind == CredentialKindUserPass {
		if username, err = validateCredentialUsername(ctx, req.Username); err != nil {
			return nil, err
		}
		if err := validateCredentialValue(ctx, req.Kind, req.Value); err != nil {
			return nil, err
		}
		stored = control.EncodeUserPass(username, req.Value)
	} else {
		if req.Username != "" {
			return nil, fieldInvalid(ctx, "username", "Only a username and password credential has a username.")
		}
		if err := validateCredentialValue(ctx, req.Kind, req.Value); err != nil {
			return nil, err
		}
	}
	id := uuid.New()
	// The value first, then the row: a failure leaves an unreferenced secret, never a credential
	// that points at nothing.
	if err := m.Put(ctx, control.TenantCredentialRef(c.tenant, id), req.Source, stored); err != nil {
		return nil, svcErr(ctx, http.StatusConflict, errcode.Forbidden, err.Error())
	}
	_, err = s.d.cfg.Shared.Exec(ctx, `INSERT INTO metadata.tenant_credentials (id, tenant_id, name, description, kind, source, username, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, id, c.tenant, name, strings.TrimSpace(req.Description), req.Kind, req.Source, username, c.self)
	if err != nil {
		_ = m.Remove(ctx, control.TenantCredentialRef(c.tenant, id))
		if isUniqueViolation(err) {
			return nil, fieldInvalid(ctx, "name", "A credential with this name already exists.")
		}
		return nil, rpcError(ctx, err)
	}
	s.d.recordFor(ctx, c, AuditEntityCredentials, id, audit.ActionCreate,
		map[string][2]any{"name": {nil, name}, "kind": {nil, req.Kind}, "source": {nil, req.Source}})
	return s.view(ctx, c.tenant, m)
}

func (s *CredentialsService) UpdateCredential(ctx context.Context, req *UpdateCredentialRequest) (*CredentialsView, error) {
	c, err := s.d.admin(ctx)
	if err != nil {
		return nil, err
	}
	m, err := s.manager(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.Id)
	if err != nil {
		return nil, err
	}
	cur, err := s.get(ctx, c.tenant, id)
	if err != nil {
		return nil, err
	}
	next := cur
	diff := map[string][2]any{}
	if req.Name != nil {
		name, err := validateCredentialName(ctx, *req.Name)
		if err != nil {
			return nil, err
		}
		if name != cur.name {
			diff["name"] = [2]any{cur.name, name}
		}
		next.name = name
	}
	if req.Description != nil {
		next.description = strings.TrimSpace(*req.Description)
	}
	moving := req.Source != nil && *req.Source != cur.source
	if moving {
		if err := checkHome(ctx, m.Capabilities(), *req.Source); err != nil {
			return nil, err
		}
		if req.Value == nil || *req.Value == "" {
			return nil, fieldInvalid(ctx, "value", "A value is required to move a credential: it is never read back.")
		}
		diff["source"] = [2]any{cur.source, *req.Source}
		next.source = *req.Source
	}
	newValue := ""
	if req.Value != nil {
		newValue = *req.Value
	}
	if req.Username != nil && cur.kind != CredentialKindUserPass {
		return nil, fieldInvalid(ctx, "username", "Only a username and password credential has a username.")
	}
	usernameChanged := false
	if cur.kind == CredentialKindUserPass && req.Username != nil {
		username, err := validateCredentialUsername(ctx, *req.Username)
		if err != nil {
			return nil, err
		}
		if username != cur.username {
			usernameChanged = true
			diff["username"] = [2]any{cur.username, username}
			next.username = username
		}
	}
	ref := control.TenantCredentialRef(c.tenant, id)
	if newValue != "" || usernameChanged {
		stored := newValue
		if cur.kind == CredentialKindUserPass {
			password := newValue
			if password == "" {
				// A new username keeps the stored password: read it for this alone.
				existing, gerr := m.Get(ctx, ref)
				_, kept, ok := control.DecodeUserPass(existing)
				if gerr != nil || !ok {
					return nil, fieldInvalid(ctx, "value", "The stored password could not be read: enter the password again.")
				}
				password = kept
			}
			if err := validateCredentialValue(ctx, cur.kind, password); err != nil {
				return nil, err
			}
			stored = control.EncodeUserPass(next.username, password)
		} else if err := validateCredentialValue(ctx, cur.kind, newValue); err != nil {
			return nil, err
		}
		if err := checkHome(ctx, m.Capabilities(), next.source); err != nil {
			return nil, err
		}
		if err := m.Put(ctx, ref, next.source, stored); err != nil {
			return nil, svcErr(ctx, http.StatusConflict, errcode.Forbidden, err.Error())
		}
		if _, ok := diff["source"]; !ok && newValue != "" {
			diff["value"] = [2]any{"set", "replaced"}
		}
	}
	if _, err := s.d.cfg.Shared.Exec(ctx, `UPDATE metadata.tenant_credentials
		SET name = $3, description = $4, source = $5, username = $6, updated_at = now() WHERE tenant_id = $1 AND id = $2`,
		c.tenant, id, next.name, next.description, next.source, next.username); err != nil {
		if isUniqueViolation(err) {
			return nil, fieldInvalid(ctx, "name", "A credential with this name already exists.")
		}
		return nil, rpcError(ctx, err)
	}
	if len(diff) > 0 {
		s.d.recordFor(ctx, c, AuditEntityCredentials, id, audit.ActionUpdate, diff)
	}
	return s.view(ctx, c.tenant, m)
}

func (s *CredentialsService) DeleteCredential(ctx context.Context, req *CredentialIdRequest) (*CredentialsView, error) {
	c, err := s.d.admin(ctx)
	if err != nil {
		return nil, err
	}
	m, err := s.manager(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.Id)
	if err != nil {
		return nil, err
	}
	cur, err := s.get(ctx, c.tenant, id)
	if err != nil {
		return nil, err
	}
	used, err := s.usage(ctx, c.tenant)
	if err != nil {
		return nil, rpcError(ctx, err)
	}
	if uses := used[id]; len(uses) > 0 {
		names := make([]string, len(uses))
		for i, u := range uses {
			names[i] = u.Name
		}
		return nil, sendServiceError(ctx, &crud.ServiceError{Status: http.StatusConflict, Code: errcode.RecordReferenced,
			Message:     "This credential is still used by " + strings.Join(names, ", ") + ". Point those at another credential first.",
			FieldErrors: map[string][]string{"usedBy": names}})
	}
	if err := m.Remove(ctx, control.TenantCredentialRef(c.tenant, id)); err != nil {
		return nil, svcErr(ctx, http.StatusConflict, errcode.Forbidden, err.Error())
	}
	if _, err := s.d.cfg.Shared.Exec(ctx, `DELETE FROM metadata.tenant_credentials WHERE tenant_id = $1 AND id = $2`, c.tenant, id); err != nil {
		return nil, rpcError(ctx, err)
	}
	s.d.recordFor(ctx, c, AuditEntityCredentials, id, audit.ActionDelete,
		map[string][2]any{"name": {cur.name, nil}})
	return s.view(ctx, c.tenant, m)
}
