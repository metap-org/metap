package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/metap-org/metap/internal/access"
	"github.com/metap-org/metap/internal/admin"
	"github.com/metap-org/metap/internal/admin/preferences"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/permission"
)

// User, role, tenant and access administration plus the caller's own account (roadmap 113-115),
// served as typed REST operations (roadmap 116; they were gRPC services). Only the token issuer
// serves them (it owns identities): AppState.Identity is nil on a verify-only service.

type adminOut[T any] struct{ Body *T }

type idPath struct {
	ID string `path:"id"`
}

type namePath struct {
	Name string `path:"name"`
}

type bodyIn[B any] struct{ Body B }

type idBodyIn[B any] struct {
	ID   string `path:"id"`
	Body B
}

type nameBodyIn[B any] struct {
	Name string `path:"name"`
	Body B
}

type pageQuery struct {
	Cursor string `query:"cursor"`
	Limit  int32  `query:"limit"`
}

// emptySlices replaces nil slices anywhere in v with empty ones, so a list field is [] in JSON
// rather than null (what the gRPC and GraphQL APIs sent).
func emptySlices(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			emptySlices(v.Elem())
		}
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				emptySlices(v.Field(i))
			}
		}
	case reflect.Slice:
		if v.IsNil() {
			if v.CanSet() {
				v.Set(reflect.MakeSlice(v.Type(), 0, 0))
			}
			return
		}
		for i := range v.Len() {
			emptySlices(v.Index(i))
		}
	}
}

// operationAccess is who each administrative operation lets in, by operation id: the route table
// of this API, in one place (roadmap 120, P4). adminOp refuses to register an operation that is not
// listed, so an operation that says nothing about who may call it is never served. The gate checks
// it before the operation's input is read; the services check it again for in-process callers,
// from the same definition (internal/access), and a test calls every operation as a caller who
// should be refused.
var operationAccess = map[string]access.Level{
	"listPolicies":             access.Admin,         // GET "/admin/policies"
	"createPolicy":             access.Admin,         // POST "/admin/policies"
	"seedDefaultPolicies":      access.Admin,         // POST "/admin/policies/seed-defaults"
	"syncMatrixPolicies":       access.Admin,         // PUT "/admin/policies/matrix"
	"explainPolicy":            access.Admin,         // POST "/admin/policies/explain"
	"deletePolicy":             access.Admin,         // DELETE "/admin/policies/{id}"
	"invalidateUserContext":    access.Admin,         // POST "/admin/users/{userId}/context/invalidate"
	"listPlatformConfig":       access.PlatformAdmin, // GET "/platform/config"
	"setPlatformConfig":        access.PlatformAdmin, // PUT "/platform/config/{key}"
	"resetPlatformConfig":      access.PlatformAdmin, // DELETE "/platform/config/{key}"
	"listTenantConfig":         access.Authenticated, // GET "/admin/config"
	"setTenantConfig":          access.Admin,         // PUT "/admin/config/{key}"
	"resetTenantConfig":        access.Admin,         // DELETE "/admin/config/{key}"
	"getPreferences":           access.Authenticated, // GET "/preferences"
	"updatePreferences":        access.Authenticated, // PUT "/preferences"
	"listCredentials":          access.Admin,         // GET "/admin/credentials"
	"createCredential":         access.Admin,         // POST "/admin/credentials"
	"updateCredential":         access.Admin,         // PATCH "/admin/credentials/{id}"
	"deleteCredential":         access.Admin,         // DELETE "/admin/credentials/{id}"
	"listUsers":                access.Admin,         // GET "/admin/users"
	"createUser":               access.Durable,       // POST "/admin/users"
	"inviteUser":               access.Durable,       // POST "/admin/invitations"
	"getUser":                  access.Admin,         // GET "/admin/users/{id}"
	"updateUser":               access.Admin,         // PATCH "/admin/users/{id}"
	"removeUser":               access.Admin,         // DELETE "/admin/users/{id}"
	"disableUser":              access.Admin,         // POST "/admin/users/{id}/disable"
	"enableUser":               access.Admin,         // POST "/admin/users/{id}/enable"
	"resetUserPassword":        access.Durable,       // POST "/admin/users/{id}/password"
	"resetUserMfa":             access.Durable,       // POST "/admin/users/{id}/mfa/reset"
	"resendInvitation":         access.Durable,       // POST "/admin/users/{id}/invitation/resend"
	"assignRole":               access.Durable,       // POST "/admin/users/{id}/roles"
	"revokeRole":               access.Admin,         // DELETE "/admin/users/{id}/roles/{role}"
	"listUserAudit":            access.Admin,         // GET "/admin/users/{id}/audit"
	"listScopeMembers":         access.Admin,         // GET "/admin/scope-members"
	"listServiceAccounts":      access.Admin,         // GET "/admin/service-accounts"
	"createServiceAccount":     access.Durable,       // POST "/admin/service-accounts"
	"listApiKeys":              access.Admin,         // GET "/admin/users/{id}/api-keys"
	"createApiKey":             access.Durable,       // POST "/admin/users/{id}/api-keys"
	"revokeApiKey":             access.Admin,         // DELETE "/admin/api-keys/{id}"
	"listRoles":                access.Admin,         // GET "/admin/roles"
	"createRole":               access.Admin,         // POST "/admin/roles"
	"getRole":                  access.Admin,         // GET "/admin/roles/{name}"
	"updateRole":               access.Admin,         // PATCH "/admin/roles/{name}"
	"deleteRole":               access.Admin,         // DELETE "/admin/roles/{name}"
	"getRolePermissions":       access.Admin,         // GET "/admin/roles/{name}/permissions"
	"setRolePermissions":       access.Admin,         // PUT "/admin/roles/{name}/permissions"
	"listRoleAudit":            access.Admin,         // GET "/admin/roles/{name}/audit"
	"listPermissionResources":  access.Admin,         // GET "/admin/permission-resources"
	"getMe":                    access.Authenticated, // GET "/me"
	"updateMyProfile":          access.Authenticated, // PATCH "/me"
	"listMyTenants":            access.Authenticated, // GET "/me/tenants"
	"listTenantUsers":          access.Authenticated, // GET "/tenant-users"
	"getMyMfa":                 access.Authenticated, // GET "/me/mfa"
	"beginMfaEnrollment":       access.Authenticated, // POST "/me/mfa/enrollment"
	"confirmMfaEnrollment":     access.Authenticated, // POST "/me/mfa/enrollment/confirm"
	"disableMyMfa":             access.Authenticated, // POST "/me/mfa/disable"
	"regenerateRecoveryCodes":  access.Authenticated, // POST "/me/mfa/recovery-codes"
	"listTenants":              access.PlatformAdmin, // GET "/platform/tenants"
	"createTenant":             access.PlatformAdmin, // POST "/platform/tenants"
	"getTenant":                access.PlatformAdmin, // GET "/platform/tenants/{id}"
	"updateTenant":             access.PlatformAdmin, // PATCH "/platform/tenants/{id}"
	"suspendTenant":            access.PlatformAdmin, // POST "/platform/tenants/{id}/suspend"
	"resumeTenant":             access.PlatformAdmin, // POST "/platform/tenants/{id}/resume"
	"requestTenantDeletion":    access.PlatformAdmin, // POST "/platform/tenants/{id}/deletion"
	"cancelTenantDeletion":     access.PlatformAdmin, // DELETE "/platform/tenants/{id}/deletion"
	"searchAudit":              access.Admin,         // GET "/admin/audit"
	"verifyAuditChain":         access.Admin,         // POST "/admin/audit/verify"
	"listSupportGrants":        access.Admin,         // GET "/admin/support-grants"
	"grantSupportAccess":       access.Admin,         // POST "/admin/support-grants"
	"revokeSupportGrant":       access.Admin,         // POST "/admin/support-grants/{id}/revoke"
	"startSupportSession":      access.PlatformAdmin, // POST "/platform/support-sessions"
	"getOwnerStatus":           access.Admin,         // GET "/admin/owner"
	"transferOwnership":        access.Admin,         // POST "/admin/owner/transfer"
	"requestWorkspaceDeletion": access.Admin,         // POST "/admin/workspace/deletion"
	"cancelOwnerRecovery":      access.Admin,         // POST "/admin/owner/recovery/cancel"
	"getTenantOwner":           access.PlatformAdmin, // GET "/platform/tenants/{id}/owner"
	"requestOwnerRecovery":     access.PlatformAdmin, // POST "/platform/owner-recovery"
	"completeOwnerRecovery":    access.PlatformAdmin, // POST "/platform/owner-recovery/complete"
	"getSsoConfig":             access.Admin,         // GET "/admin/sso"
	"setSsoConfig":             access.Durable,       // PUT "/admin/sso"
	"getSamlConfig":            access.Admin,         // GET "/admin/sso/saml"
	"setSamlConfig":            access.Durable,       // PUT "/admin/sso/saml"
	"deleteSamlConfig":         access.Admin,         // DELETE "/admin/sso/saml"
	"verifySsoDomain":          access.Durable,       // POST "/admin/sso/domains/{domain}/verify"
	"deleteSsoConfig":          access.Admin,         // DELETE "/admin/sso"
}

// AdminOperationAccess lists each administrative operation id with the level it was declared at
// (access.Level's name), for tests and tooling that check the route table against what is served.
func AdminOperationAccess() map[string]string {
	out := make(map[string]string, len(operationAccess))
	for id, level := range operationAccess {
		out[id] = level.String()
	}
	return out
}

// accessOf is the level an operation was declared at; an undeclared operation is a programming error.
func accessOf(operationID string) access.Level {
	level, ok := operationAccess[operationID]
	if !ok {
		panic("httpapi: operation " + operationID + " declares no access (add it to operationAccess)")
	}
	return level
}

// gateAccess refuses, before the operation's input is read, a caller its level does not let in.
func gateAccess(state *AppState, level access.Level) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		rc := callerOf(ctx.Context())
		_, w := humago.Unwrap(ctx)
		if why := access.Refusal(level, rc); why != "" {
			forbidden(why).respond(w)
			return
		}
		if level == access.Durable && state.Identity != nil {
			why, err := admin.SupportSessionRefusal(ctx.Context(), *state.Identity, rc)
			if err != nil {
				writeInternalError(w, err)
				return
			}
			if why != "" {
				forbidden(why).respond(w)
				return
			}
		}
		next(ctx)
	}
}

// adminOp registers one operation that answers with a JSON body.
func adminOp[I, R any](api huma.API, state *AppState, op huma.Operation, call func(context.Context, *I) (*R, error)) {
	op.Middlewares = huma.Middlewares{authed(state), gateAccess(state, accessOf(op.OperationID))}
	huma.Register(api, op, func(ctx context.Context, in *I) (*adminOut[R], error) {
		res, err := call(admin.WithCaller(ctx, callerOf(ctx)), in)
		if err != nil {
			return nil, apiErr(err)
		}
		emptySlices(reflect.ValueOf(res))
		return &adminOut[R]{Body: res}, nil
	})
}

// adminNoContent registers one operation that answers 204.
func adminNoContent[I, R any](api huma.API, state *AppState, op huma.Operation, call func(context.Context, *I) (*R, error)) {
	op.Middlewares = huma.Middlewares{authed(state), gateAccess(state, accessOf(op.OperationID))}
	op.DefaultStatus = http.StatusNoContent
	huma.Register(api, op, func(ctx context.Context, in *I) (*struct{}, error) {
		if _, err := call(admin.WithCaller(ctx, callerOf(ctx)), in); err != nil {
			return nil, apiErr(err)
		}
		return nil, nil
	})
}

func opOf(id, method, path, summary, tag string) huma.Operation {
	return huma.Operation{OperationID: id, Method: method, Path: path, Summary: summary, Tags: []string{tag}}
}

func registerAdminOperations(api huma.API, state *AppState) {
	if state.Identity == nil {
		return
	}
	cfg := *state.Identity
	registerUserOperations(api, state, admin.NewUserAdminService(cfg))
	registerRoleOperations(api, state, admin.NewRoleAdminService(cfg))
	registerAccountOperations(api, state, admin.NewAccountService(cfg))
	registerTenantOperations(api, state, admin.NewTenantAdminService(cfg))
	registerWorkspaceOperations(api, state, cfg)
	registerCredentialOperations(api, state, admin.NewCredentialsService(cfg))
}

// registerCredentialOperations: the Credentials tab (named tenant credentials, stored encrypted in
// the database or in the deployment's secret manager). Values are write-only.
type policiesIn struct {
	Entity string `query:"entity"`
}

type userIDPath struct {
	UserID string `path:"userId"`
}

// registerPolicyOperations: the permission matrix and the advanced policy editor. They need no
// identity database (PermissionService only), so a verify-only service serves them too.
func registerPolicyOperations(api huma.API, state *AppState) {
	const tag = "Policies"
	s := admin.NewPolicyAdminService(state.Permissions, state.ContextAttributesCache, state.Audit)

	adminOp(api, state, opOf("listPolicies", http.MethodGet, "/admin/policies", "The tenant's policies", tag),
		func(ctx context.Context, in *policiesIn) (*admin.PolicyList, error) {
			return s.ListPolicies(ctx, &admin.ListPoliciesRequest{Entity: in.Entity})
		})
	create := opOf("createPolicy", http.MethodPost, "/admin/policies", "Add a policy", tag)
	create.DefaultStatus = http.StatusCreated
	adminOp(api, state, create, func(ctx context.Context, in *bodyIn[admin.CreatePolicyRequest]) (*admin.PolicyOne, error) {
		return s.CreatePolicy(ctx, &in.Body)
	})
	seed := opOf("seedDefaultPolicies", http.MethodPost, "/admin/policies/seed-defaults", "Grant roles every action on an entity", tag)
	seed.DefaultStatus = http.StatusCreated
	adminOp(api, state, seed, func(ctx context.Context, in *bodyIn[admin.SeedDefaultPoliciesRequest]) (*admin.PolicyList, error) {
		return s.SeedDefaultPolicies(ctx, &in.Body)
	})
	adminOp(api, state, opOf("syncMatrixPolicies", http.MethodPut, "/admin/policies/matrix", "Replace an entity's basic policies with the matrix", tag),
		func(ctx context.Context, in *bodyIn[admin.SyncMatrixRequest]) (*admin.PolicyList, error) {
			return s.SyncMatrixPolicies(ctx, &in.Body)
		})
	adminOp(api, state, opOf("explainPolicy", http.MethodPost, "/admin/policies/explain", "Which policies decide an action for the caller", tag),
		func(ctx context.Context, in *bodyIn[admin.ExplainPolicyRequest]) (*admin.ExplainResponse, error) {
			return s.ExplainPolicy(ctx, &in.Body)
		})
	adminNoContent(api, state, opOf("deletePolicy", http.MethodDelete, "/admin/policies/{id}", "Delete a policy", tag),
		func(ctx context.Context, in *idPath) (*admin.Empty, error) {
			return s.DeletePolicy(ctx, &admin.PolicyIDRequest{ID: in.ID})
		})
	adminNoContent(api, state, opOf("invalidateUserContext", http.MethodPost, "/admin/users/{userId}/context/invalidate", "Drop a member's cached context attributes", tag),
		func(ctx context.Context, in *userIDPath) (*admin.Empty, error) {
			return s.InvalidateContext(ctx, &admin.UserIDRequest{UserID: in.UserID})
		})
}

type configKeyPath struct {
	Key string `path:"key"`
}

type configKeyBodyIn struct {
	Key  string `path:"key"`
	Body struct {
		Value json.RawMessage `json:"value" required:"false"`
	}
}

// registerConfigOperations: the tiered configuration (platform admin, tenant admin) and the
// caller's own preferences. None needs the identity database.
func registerConfigOperations(api huma.API, state *AppState) {
	const tag = "Configuration"
	s := admin.NewConfigAdminService(state.Config, state.Router, state.Permissions, state.EffectiveConfig, state.Audit)

	adminOp(api, state, opOf("listPlatformConfig", http.MethodGet, "/platform/config", "The platform-writable configuration keys", tag),
		func(ctx context.Context, _ *struct{}) (*admin.PlatformConfigList, error) {
			return s.ListPlatformConfig(ctx, &admin.Empty{})
		})
	adminOp(api, state, opOf("setPlatformConfig", http.MethodPut, "/platform/config/{key}", "Set a platform-wide value", tag),
		func(ctx context.Context, in *configKeyBodyIn) (*admin.PlatformConfigResult, error) {
			return s.SetPlatformConfig(ctx, &admin.SetConfigRequest{Key: in.Key, Value: in.Body.Value})
		})
	adminOp(api, state, opOf("resetPlatformConfig", http.MethodDelete, "/platform/config/{key}", "Fall back to the declared default", tag),
		func(ctx context.Context, in *configKeyPath) (*admin.PlatformConfigResult, error) {
			return s.ResetPlatformConfig(ctx, &admin.ConfigKeyRequest{Key: in.Key})
		})
	adminOp(api, state, opOf("listTenantConfig", http.MethodGet, "/admin/config", "The tenant's effective configuration", tag),
		func(ctx context.Context, _ *struct{}) (*admin.TenantConfigList, error) {
			return s.ListTenantConfig(ctx, &admin.Empty{})
		})
	adminOp(api, state, opOf("setTenantConfig", http.MethodPut, "/admin/config/{key}", "Set the tenant's own value", tag),
		func(ctx context.Context, in *configKeyBodyIn) (*admin.TenantConfigResult, error) {
			return s.SetTenantConfig(ctx, &admin.SetConfigRequest{Key: in.Key, Value: in.Body.Value})
		})
	adminOp(api, state, opOf("resetTenantConfig", http.MethodDelete, "/admin/config/{key}", "Clear the tenant's own value", tag),
		func(ctx context.Context, in *configKeyPath) (*admin.TenantConfigResult, error) {
			return s.ResetTenantConfig(ctx, &admin.ConfigKeyRequest{Key: in.Key})
		})

	prefs := preferences.New(preferences.NewPgStore(state.Pool), state.Permissions)
	adminOp(api, state, opOf("getPreferences", http.MethodGet, "/preferences", "The caller's preferences", "Preferences"),
		func(ctx context.Context, _ *struct{}) (*preferences.PreferencesResult, error) {
			return prefs.Get(ctx)
		})
	adminOp(api, state, opOf("updatePreferences", http.MethodPut, "/preferences", "Change the caller's preferences", "Preferences"),
		func(ctx context.Context, in *bodyIn[preferences.Preferences]) (*preferences.PreferencesResult, error) {
			return prefs.Update(ctx, &in.Body)
		})
}

func registerCredentialOperations(api huma.API, state *AppState, s *admin.CredentialsService) {
	const tag = "Credentials"
	adminOp(api, state, opOf("listCredentials", http.MethodGet, "/admin/credentials", "The tenant's credentials, where they are used, and where this deployment can store them", tag),
		func(ctx context.Context, _ *struct{}) (*admin.CredentialsView, error) {
			return s.ListCredentials(ctx, &admin.Empty{})
		})
	create := opOf("createCredential", http.MethodPost, "/admin/credentials", "Add a credential (write-only value)", tag)
	create.DefaultStatus = http.StatusCreated
	adminOp(api, state, create, func(ctx context.Context, in *bodyIn[admin.CreateCredentialRequest]) (*admin.CredentialsView, error) {
		return s.CreateCredential(ctx, &in.Body)
	})
	adminOp(api, state, opOf("updateCredential", http.MethodPatch, "/admin/credentials/{id}", "Rename a credential, replace its value, or move it to the other store", tag),
		func(ctx context.Context, in *idBodyIn[admin.UpdateCredentialRequest]) (*admin.CredentialsView, error) {
			in.Body.Id = in.ID
			return s.UpdateCredential(ctx, &in.Body)
		})
	adminOp(api, state, opOf("deleteCredential", http.MethodDelete, "/admin/credentials/{id}", "Remove a credential no job uses", tag),
		func(ctx context.Context, in *idPath) (*admin.CredentialsView, error) {
			return s.DeleteCredential(ctx, &admin.CredentialIdRequest{Id: in.ID})
		})
}

type listUsersIn struct {
	Search string `query:"search"`
	Status string `query:"status"`
	Role   string `query:"role"`
	Offset int32  `query:"offset"`
	Limit  int32  `query:"limit"`
}

type roleBody struct {
	Role      string `json:"role"`
	ScopeType string `json:"scopeType,omitempty"`
	ScopeID   string `json:"scopeId,omitempty"`
}

type revokeRoleIn struct {
	ID        string `path:"id"`
	Role      string `path:"role"`
	ScopeType string `query:"scopeType"`
	ScopeID   string `query:"scopeId"`
}

type userAuditIn struct {
	ID string `path:"id"`
	pageQuery
}

type scopeMembersIn struct {
	ScopeType string `query:"scopeType"`
	ScopeID   string `query:"scopeId"`
}

func registerUserOperations(api huma.API, state *AppState, s *admin.UserAdminService) {
	const tag = "Users"
	id := func(v string) *admin.UserIdRequest { return &admin.UserIdRequest{Id: v} }

	adminOp(api, state, opOf("listUsers", http.MethodGet, "/admin/users", "Members of the caller's tenant", tag),
		func(ctx context.Context, in *listUsersIn) (*admin.ListUsersResponse, error) {
			return s.ListUsers(ctx, &admin.ListUsersRequest{Search: in.Search, Status: in.Status, Role: in.Role, Offset: in.Offset, Limit: in.Limit})
		})
	createUser := opOf("createUser", http.MethodPost, "/admin/users", "Add a member with a password", tag)
	createUser.DefaultStatus = http.StatusCreated
	adminOp(api, state, createUser, func(ctx context.Context, in *bodyIn[admin.CreateUserRequest]) (*admin.CreateUserResponse, error) {
		return s.CreateUser(ctx, &in.Body)
	})
	invite := opOf("inviteUser", http.MethodPost, "/admin/invitations", "Invite someone by email", tag)
	invite.DefaultStatus = http.StatusCreated
	adminOp(api, state, invite, func(ctx context.Context, in *bodyIn[admin.InviteUserRequest]) (*admin.InviteUserResponse, error) {
		return s.InviteUser(ctx, &in.Body)
	})
	adminOp(api, state, opOf("getUser", http.MethodGet, "/admin/users/{id}", "One member", tag),
		func(ctx context.Context, in *idPath) (*admin.User, error) { return s.GetUser(ctx, id(in.ID)) })
	adminOp(api, state, opOf("updateUser", http.MethodPatch, "/admin/users/{id}", "Change a member's display name", tag),
		func(ctx context.Context, in *idBodyIn[admin.UpdateUserRequest]) (*admin.User, error) {
			in.Body.Id = in.ID
			return s.UpdateUser(ctx, &in.Body)
		})
	adminNoContent(api, state, opOf("removeUser", http.MethodDelete, "/admin/users/{id}", "Remove a member from the tenant", tag),
		func(ctx context.Context, in *idPath) (*admin.Empty, error) { return s.RemoveUser(ctx, id(in.ID)) })
	adminOp(api, state, opOf("disableUser", http.MethodPost, "/admin/users/{id}/disable", "Disable a member", tag),
		func(ctx context.Context, in *idPath) (*admin.User, error) { return s.DisableUser(ctx, id(in.ID)) })
	adminOp(api, state, opOf("enableUser", http.MethodPost, "/admin/users/{id}/enable", "Enable a member (also unlocks)", tag),
		func(ctx context.Context, in *idPath) (*admin.User, error) { return s.EnableUser(ctx, id(in.ID)) })
	adminNoContent(api, state, opOf("resetUserPassword", http.MethodPost, "/admin/users/{id}/password", "Set a member's password", tag),
		func(ctx context.Context, in *idBodyIn[admin.ResetPasswordRequest]) (*admin.Empty, error) {
			in.Body.Id = in.ID
			return s.ResetPassword(ctx, &in.Body)
		})
	adminNoContent(api, state, opOf("resetUserMfa", http.MethodPost, "/admin/users/{id}/mfa/reset", "Turn off a member's two-step sign-in", tag),
		func(ctx context.Context, in *idPath) (*admin.Empty, error) { return s.ResetMfa(ctx, id(in.ID)) })
	adminOp(api, state, opOf("resendInvitation", http.MethodPost, "/admin/users/{id}/invitation/resend", "Send an invited member a fresh link", tag),
		func(ctx context.Context, in *idPath) (*admin.InviteUserResponse, error) {
			return s.ResendInvitation(ctx, id(in.ID))
		})
	adminOp(api, state, opOf("assignRole", http.MethodPost, "/admin/users/{id}/roles", "Grant a role, tenant-wide or inside a scope", tag),
		func(ctx context.Context, in *idBodyIn[roleBody]) (*admin.User, error) {
			return s.AssignRole(ctx, &admin.RoleRequest{Id: in.ID, Role: in.Body.Role, ScopeType: in.Body.ScopeType, ScopeId: in.Body.ScopeID})
		})
	adminOp(api, state, opOf("revokeRole", http.MethodDelete, "/admin/users/{id}/roles/{role}", "Take a role away", tag),
		func(ctx context.Context, in *revokeRoleIn) (*admin.User, error) {
			return s.RevokeRole(ctx, &admin.RoleRequest{Id: in.ID, Role: in.Role, ScopeType: in.ScopeType, ScopeId: in.ScopeID})
		})
	adminOp(api, state, opOf("listUserAudit", http.MethodGet, "/admin/users/{id}/audit", "Changes made to a member", tag),
		func(ctx context.Context, in *userAuditIn) (*admin.ListUserAuditResponse, error) {
			return s.ListUserAudit(ctx, &admin.ListUserAuditRequest{Id: in.ID, Cursor: in.Cursor, Limit: in.Limit})
		})
	adminOp(api, state, opOf("listScopeMembers", http.MethodGet, "/admin/scope-members", "Who holds a role inside one scope", tag),
		func(ctx context.Context, in *scopeMembersIn) (*admin.ListScopeMembersResponse, error) {
			return s.ListScopeMembers(ctx, &admin.ScopeRequest{ScopeType: in.ScopeType, ScopeId: in.ScopeID})
		})

	// Service accounts and API keys (roadmap 115 M5).
	adminOp(api, state, opOf("listServiceAccounts", http.MethodGet, "/admin/service-accounts", "The tenant's service accounts", tag),
		func(ctx context.Context, _ *struct{}) (*admin.ListUsersResponse, error) {
			return s.ListServiceAccounts(ctx, &admin.Empty{})
		})
	createSA := opOf("createServiceAccount", http.MethodPost, "/admin/service-accounts", "Add a service account", tag)
	createSA.DefaultStatus = http.StatusCreated
	adminOp(api, state, createSA, func(ctx context.Context, in *bodyIn[admin.CreateServiceAccountRequest]) (*admin.User, error) {
		return s.CreateServiceAccount(ctx, &in.Body)
	})
	adminOp(api, state, opOf("listApiKeys", http.MethodGet, "/admin/users/{id}/api-keys", "A member's API keys", tag),
		func(ctx context.Context, in *idPath) (*admin.ListApiKeysResponse, error) {
			return s.ListApiKeys(ctx, id(in.ID))
		})
	createKey := opOf("createApiKey", http.MethodPost, "/admin/users/{id}/api-keys", "Issue an API key (the secret is shown once)", tag)
	createKey.DefaultStatus = http.StatusCreated
	adminOp(api, state, createKey, func(ctx context.Context, in *idBodyIn[admin.CreateApiKeyRequest]) (*admin.CreateApiKeyResponse, error) {
		in.Body.UserId = in.ID
		return s.CreateApiKey(ctx, &in.Body)
	})
	adminNoContent(api, state, opOf("revokeApiKey", http.MethodDelete, "/admin/api-keys/{id}", "Revoke an API key", tag),
		func(ctx context.Context, in *idPath) (*admin.Empty, error) {
			return s.RevokeApiKey(ctx, &admin.ApiKeyIdRequest{Id: in.ID})
		})
}

type listRolesIn struct {
	Search string `query:"search"`
}

type roleAuditIn struct {
	Name string `path:"name"`
	pageQuery
}

type grantsBody struct {
	Grants []*admin.RoleGrant `json:"grants"`
}

func registerRoleOperations(api huma.API, state *AppState, s *admin.RoleAdminService) {
	const tag = "Roles"
	name := func(v string) *admin.RoleNameRequest { return &admin.RoleNameRequest{Name: v} }

	adminOp(api, state, opOf("listRoles", http.MethodGet, "/admin/roles", "The tenant's roles", tag),
		func(ctx context.Context, in *listRolesIn) (*admin.ListRolesResponse, error) {
			return s.ListRoles(ctx, &admin.ListRolesRequest{Search: in.Search})
		})
	create := opOf("createRole", http.MethodPost, "/admin/roles", "Add a role (optionally copying another's permissions)", tag)
	create.DefaultStatus = http.StatusCreated
	adminOp(api, state, create, func(ctx context.Context, in *bodyIn[admin.CreateRoleRequest]) (*admin.Role, error) {
		return s.CreateRole(ctx, &in.Body)
	})
	adminOp(api, state, opOf("getRole", http.MethodGet, "/admin/roles/{name}", "One role", tag),
		func(ctx context.Context, in *namePath) (*admin.Role, error) { return s.GetRole(ctx, name(in.Name)) })
	adminOp(api, state, opOf("updateRole", http.MethodPatch, "/admin/roles/{name}", "Change a role's label or description", tag),
		func(ctx context.Context, in *nameBodyIn[admin.UpdateRoleRequest]) (*admin.Role, error) {
			in.Body.Name = in.Name
			return s.UpdateRole(ctx, &in.Body)
		})
	adminNoContent(api, state, opOf("deleteRole", http.MethodDelete, "/admin/roles/{name}", "Delete a role", tag),
		func(ctx context.Context, in *namePath) (*admin.Empty, error) {
			return s.DeleteRole(ctx, name(in.Name))
		})
	adminOp(api, state, opOf("getRolePermissions", http.MethodGet, "/admin/roles/{name}/permissions", "What a role may do", tag),
		func(ctx context.Context, in *namePath) (*admin.RolePermissions, error) {
			return s.GetRolePermissions(ctx, name(in.Name))
		})
	adminOp(api, state, opOf("setRolePermissions", http.MethodPut, "/admin/roles/{name}/permissions", "Replace what a role may do", tag),
		func(ctx context.Context, in *nameBodyIn[grantsBody]) (*admin.RolePermissions, error) {
			return s.SetRolePermissions(ctx, &admin.SetRolePermissionsRequest{Role: in.Name, Grants: in.Body.Grants})
		})
	adminOp(api, state, opOf("listRoleAudit", http.MethodGet, "/admin/roles/{name}/audit", "Changes made to a role", tag),
		func(ctx context.Context, in *roleAuditIn) (*admin.ListUserAuditResponse, error) {
			return s.ListRoleAudit(ctx, &admin.ListRoleAuditRequest{Name: in.Name, Cursor: in.Cursor, Limit: in.Limit})
		})
	adminOp(api, state, opOf("listPermissionResources", http.MethodGet, "/admin/permission-resources", "Everything a role can be granted", tag),
		func(ctx context.Context, _ *struct{}) (*admin.ListPermissionResourcesResponse, error) {
			return s.ListPermissionResources(ctx, &admin.Empty{})
		})
}

type tenantUsersIn struct {
	Search string `query:"search"`
	Limit  int32  `query:"limit"`
}

func registerAccountOperations(api huma.API, state *AppState, s *admin.AccountService) {
	const tag = "Account"
	empty := &admin.Empty{}

	adminOp(api, state, opOf("getMe", http.MethodGet, "/me", "The caller", tag),
		func(ctx context.Context, _ *struct{}) (*admin.Me, error) { return s.GetMe(ctx, empty) })
	adminOp(api, state, opOf("updateMyProfile", http.MethodPatch, "/me", "Change the caller's display name", tag),
		func(ctx context.Context, in *bodyIn[admin.UpdateProfileRequest]) (*admin.Me, error) {
			return s.UpdateProfile(ctx, &in.Body)
		})
	adminOp(api, state, opOf("listMyTenants", http.MethodGet, "/me/tenants", "Tenants the caller is a member of", tag),
		func(ctx context.Context, _ *struct{}) (*admin.ListMyTenantsResponse, error) {
			return s.ListMyTenants(ctx, empty)
		})
	adminOp(api, state, opOf("listTenantUsers", http.MethodGet, "/tenant-users", "Active members of the caller's tenant (for pickers)", tag),
		func(ctx context.Context, in *tenantUsersIn) (*admin.ListTenantUsersResponse, error) {
			return s.ListTenantUsers(ctx, &admin.ListTenantUsersRequest{Search: in.Search, Limit: in.Limit})
		})
	adminOp(api, state, opOf("getMyMfa", http.MethodGet, "/me/mfa", "The caller's two-step sign-in status", tag),
		func(ctx context.Context, _ *struct{}) (*admin.MfaStatus, error) { return s.GetMfaStatus(ctx, empty) })
	adminOp(api, state, opOf("beginMfaEnrollment", http.MethodPost, "/me/mfa/enrollment", "Start turning on two-step sign-in", tag),
		func(ctx context.Context, _ *struct{}) (*admin.MfaEnrollment, error) {
			return s.BeginMfaEnrollment(ctx, empty)
		})
	adminOp(api, state, opOf("confirmMfaEnrollment", http.MethodPost, "/me/mfa/enrollment/confirm", "Finish turning on two-step sign-in", tag),
		func(ctx context.Context, in *bodyIn[admin.MfaCodeRequest]) (*admin.MfaRecoveryCodes, error) {
			return s.ConfirmMfaEnrollment(ctx, &in.Body)
		})
	adminNoContent(api, state, opOf("disableMyMfa", http.MethodPost, "/me/mfa/disable", "Turn off two-step sign-in", tag),
		func(ctx context.Context, in *bodyIn[admin.MfaCodeRequest]) (*admin.Empty, error) {
			return s.DisableMfa(ctx, &in.Body)
		})
	adminOp(api, state, opOf("regenerateRecoveryCodes", http.MethodPost, "/me/mfa/recovery-codes", "Replace the caller's recovery codes", tag),
		func(ctx context.Context, in *bodyIn[admin.MfaCodeRequest]) (*admin.MfaRecoveryCodes, error) {
			return s.RegenerateRecoveryCodes(ctx, &in.Body)
		})
}

type listTenantsIn struct {
	Query  string `query:"query"`
	Status string `query:"status"`
	Tier   string `query:"tier"`
	Limit  int32  `query:"limit"`
	Offset int32  `query:"offset"`
}

type deletionBody struct {
	RetentionDays int32 `json:"retentionDays,omitempty"`
}

func registerTenantOperations(api huma.API, state *AppState, s *admin.TenantAdminService) {
	const tag = "Tenants"
	id := func(v string) *admin.TenantIdRequest { return &admin.TenantIdRequest{Id: v} }

	adminOp(api, state, opOf("listTenants", http.MethodGet, "/platform/tenants", "Every tenant (platform admin)", tag),
		func(ctx context.Context, in *listTenantsIn) (*admin.ListTenantsResponse, error) {
			return s.ListTenants(ctx, &admin.ListTenantsRequest{Query: in.Query, Status: in.Status, Tier: in.Tier, Limit: in.Limit, Offset: in.Offset})
		})
	create := opOf("createTenant", http.MethodPost, "/platform/tenants", "Create a tenant with its first admin", tag)
	create.DefaultStatus = http.StatusCreated
	adminOp(api, state, create, func(ctx context.Context, in *bodyIn[admin.CreateTenantRequest]) (*admin.CreateTenantResponse, error) {
		return s.CreateTenant(ctx, &in.Body)
	})
	adminOp(api, state, opOf("getTenant", http.MethodGet, "/platform/tenants/{id}", "One tenant", tag),
		func(ctx context.Context, in *idPath) (*admin.Tenant, error) { return s.GetTenant(ctx, id(in.ID)) })
	adminOp(api, state, opOf("updateTenant", http.MethodPatch, "/platform/tenants/{id}", "Rename a tenant or change its tier", tag),
		func(ctx context.Context, in *idBodyIn[admin.UpdateTenantRequest]) (*admin.Tenant, error) {
			in.Body.Id = in.ID
			return s.UpdateTenant(ctx, &in.Body)
		})
	adminOp(api, state, opOf("suspendTenant", http.MethodPost, "/platform/tenants/{id}/suspend", "Suspend a tenant", tag),
		func(ctx context.Context, in *idPath) (*admin.Tenant, error) {
			return s.SuspendTenant(ctx, id(in.ID))
		})
	adminOp(api, state, opOf("resumeTenant", http.MethodPost, "/platform/tenants/{id}/resume", "Resume a suspended tenant", tag),
		func(ctx context.Context, in *idPath) (*admin.Tenant, error) { return s.ResumeTenant(ctx, id(in.ID)) })
	adminOp(api, state, opOf("requestTenantDeletion", http.MethodPost, "/platform/tenants/{id}/deletion", "Schedule a tenant's deletion", tag),
		func(ctx context.Context, in *idBodyIn[deletionBody]) (*admin.Tenant, error) {
			return s.RequestTenantDeletion(ctx, &admin.RequestTenantDeletionRequest{Id: in.ID, RetentionDays: in.Body.RetentionDays})
		})
	adminOp(api, state, opOf("cancelTenantDeletion", http.MethodDelete, "/platform/tenants/{id}/deletion", "Cancel a scheduled deletion", tag),
		func(ctx context.Context, in *idPath) (*admin.Tenant, error) {
			return s.CancelTenantDeletion(ctx, id(in.ID))
		})
}

type searchAuditIn struct {
	Actor   string   `query:"actor"`
	Entity  string   `query:"entity"`
	Record  string   `query:"record"`
	Actions []string `query:"actions"`
	From    string   `query:"from"`
	To      string   `query:"to"`
	pageQuery
}

type tenantIDBody struct {
	TenantID string `json:"tenantId"`
}

// registerWorkspaceOperations: the tenant's own security surface (audit search, support access,
// ownership, SSO) — roadmap 115 M8-M11.
func registerWorkspaceOperations(api huma.API, state *AppState, cfg admin.IdentityConfig) {
	audit := admin.NewAuditAdminService(cfg)
	support := admin.NewSupportAccessService(cfg)
	owner := admin.NewOwnerService(cfg)
	sso := admin.NewSsoAdminService(cfg)
	empty := &admin.Empty{}

	adminOp(api, state, opOf("searchAudit", http.MethodGet, "/admin/audit", "Search the tenant's audit trail", "Audit"),
		func(ctx context.Context, in *searchAuditIn) (*admin.SearchAuditResponse, error) {
			return audit.SearchAudit(ctx, &admin.SearchAuditRequest{ActorUserId: in.Actor, Entity: in.Entity, RecordId: in.Record,
				Actions: in.Actions, From: in.From, To: in.To, Cursor: in.Cursor, Limit: in.Limit})
		})
	adminOp(api, state, opOf("verifyAuditChain", http.MethodPost, "/admin/audit/verify", "Check the audit trail's hash chain", "Audit"),
		func(ctx context.Context, _ *struct{}) (*admin.AuditChainReport, error) {
			return audit.VerifyAuditChain(ctx, empty)
		})

	adminOp(api, state, opOf("listSupportGrants", http.MethodGet, "/admin/support-grants", "Support access the tenant granted", "Support"),
		func(ctx context.Context, _ *struct{}) (*admin.ListSupportGrantsResponse, error) {
			return support.ListSupportGrants(ctx, empty)
		})
	grant := opOf("grantSupportAccess", http.MethodPost, "/admin/support-grants", "Let platform support in, for a while", "Support")
	grant.DefaultStatus = http.StatusCreated
	adminOp(api, state, grant, func(ctx context.Context, in *bodyIn[admin.GrantSupportAccessRequest]) (*admin.SupportGrant, error) {
		return support.GrantSupportAccess(ctx, &in.Body)
	})
	adminOp(api, state, opOf("revokeSupportGrant", http.MethodPost, "/admin/support-grants/{id}/revoke", "End a support grant early", "Support"),
		func(ctx context.Context, in *idPath) (*admin.SupportGrant, error) {
			return support.RevokeSupportGrant(ctx, &admin.SupportGrantIdRequest{Id: in.ID})
		})
	adminOp(api, state, opOf("startSupportSession", http.MethodPost, "/platform/support-sessions", "Open a support session in a tenant that granted access (platform admin)", "Support"),
		func(ctx context.Context, in *bodyIn[admin.StartSupportSessionRequest]) (*admin.SupportSessionToken, error) {
			return support.StartSupportSession(ctx, &in.Body)
		})

	adminOp(api, state, opOf("getOwnerStatus", http.MethodGet, "/admin/owner", "Who owns the tenant, and any open recovery", "Owner"),
		func(ctx context.Context, _ *struct{}) (*admin.OwnerStatus, error) {
			return owner.GetOwnerStatus(ctx, empty)
		})
	adminOp(api, state, opOf("transferOwnership", http.MethodPost, "/admin/owner/transfer", "Hand ownership to another member", "Owner"),
		func(ctx context.Context, in *bodyIn[admin.TransferOwnershipRequest]) (*admin.OwnerStatus, error) {
			return owner.TransferOwnership(ctx, &in.Body)
		})
	adminOp(api, state, opOf("requestWorkspaceDeletion", http.MethodPost, "/admin/workspace/deletion", "Delete the workspace after the retention period (the owner)", "Owner"),
		func(ctx context.Context, in *bodyIn[admin.RequestWorkspaceDeletionRequest]) (*admin.WorkspaceDeletion, error) {
			return owner.RequestWorkspaceDeletion(ctx, &in.Body)
		})
	adminOp(api, state, opOf("cancelOwnerRecovery", http.MethodPost, "/admin/owner/recovery/cancel", "Stop an owner recovery (the owner)", "Owner"),
		func(ctx context.Context, _ *struct{}) (*admin.OwnerStatus, error) {
			return owner.CancelOwnerRecovery(ctx, empty)
		})
	adminOp(api, state, opOf("getTenantOwner", http.MethodGet, "/platform/tenants/{id}/owner", "A tenant's owner, any open recovery and who could take over (platform admin)", "Owner"),
		func(ctx context.Context, in *idPath) (*admin.TenantOwnerView, error) {
			return owner.PlatformTenantOwner(ctx, &admin.TenantIdOnlyRequest{TenantId: in.ID})
		})
	adminOp(api, state, opOf("requestOwnerRecovery", http.MethodPost, "/platform/owner-recovery", "Start moving ownership of a tenant whose owner is gone (platform admin)", "Owner"),
		func(ctx context.Context, in *bodyIn[admin.RequestOwnerRecoveryRequest]) (*admin.OwnerRecovery, error) {
			return owner.RequestOwnerRecovery(ctx, &in.Body)
		})
	adminOp(api, state, opOf("completeOwnerRecovery", http.MethodPost, "/platform/owner-recovery/complete", "Finish an owner recovery once its wait is over (platform admin)", "Owner"),
		func(ctx context.Context, in *bodyIn[tenantIDBody]) (*admin.OwnerRecovery, error) {
			return owner.CompleteOwnerRecovery(ctx, &admin.TenantIdOnlyRequest{TenantId: in.Body.TenantID})
		})

	adminOp(api, state, opOf("getSsoConfig", http.MethodGet, "/admin/sso", "The tenant's single sign-on setup", "SSO"),
		func(ctx context.Context, _ *struct{}) (*admin.SsoConfig, error) {
			return sso.GetSsoConfig(ctx, empty)
		})
	adminOp(api, state, opOf("setSsoConfig", http.MethodPut, "/admin/sso", "Set up single sign-on", "SSO"),
		func(ctx context.Context, in *bodyIn[admin.SetSsoConfigRequest]) (*admin.SsoConfig, error) {
			return sso.SetSsoConfig(ctx, &in.Body)
		})
	adminOp(api, state, opOf("getSamlConfig", http.MethodGet, "/admin/sso/saml", "The tenant's SAML single sign-on setup", "SSO"),
		func(ctx context.Context, _ *struct{}) (*admin.SamlConfig, error) {
			return sso.GetSamlConfig(ctx, empty)
		})
	adminOp(api, state, opOf("setSamlConfig", http.MethodPut, "/admin/sso/saml", "Set up SAML single sign-on", "SSO"),
		func(ctx context.Context, in *bodyIn[admin.SetSamlConfigRequest]) (*admin.SamlConfig, error) {
			return sso.SetSamlConfig(ctx, &in.Body)
		})
	adminNoContent(api, state, opOf("deleteSamlConfig", http.MethodDelete, "/admin/sso/saml", "Remove SAML single sign-on", "SSO"),
		func(ctx context.Context, _ *struct{}) (*admin.Empty, error) {
			return sso.DeleteSamlConfig(ctx, empty)
		})
	adminOp(api, state, opOf("verifySsoDomain", http.MethodPost, "/admin/sso/domains/{domain}/verify", "Check the DNS record that proves a domain is owned", "SSO"),
		func(ctx context.Context, in *struct {
			Domain string `path:"domain"`
		}) (*admin.SsoConfig, error) {
			return sso.VerifySsoDomain(ctx, &admin.VerifySsoDomainRequest{Domain: in.Domain})
		})
	adminNoContent(api, state, opOf("deleteSsoConfig", http.MethodDelete, "/admin/sso", "Remove single sign-on", "SSO"),
		func(ctx context.Context, _ *struct{}) (*admin.Empty, error) {
			return sso.DeleteSsoConfig(ctx, empty)
		})
}

// scimAuthenticate is SCIM's bearer-token check: the same authentication every route uses.
func scimAuthenticate(state *AppState) SCIMAuthenticator {
	return func(r *http.Request) (*permission.RequestContext, error) {
		rc, authErr := authenticate(r.Context(), state, r)
		if authErr != nil {
			code := authErr.code
			if code == "" {
				code = errcode.Unauthorized
				if authErr.status == http.StatusForbidden {
					code = errcode.Forbidden
				}
			}
			return nil, crud.NewServiceErrorMsg(uint16(authErr.status), code, authErr.message)
		}
		return rc, nil
	}
}
