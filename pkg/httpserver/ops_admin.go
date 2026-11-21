package httpserver

import (
	"context"
	"net/http"
	"reflect"

	"github.com/danielgtaylor/huma/v2"

	"github.com/metap-org/metap/pkg/adminapi"
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

// adminOp registers one operation that answers with a JSON body.
func adminOp[I, R any](api huma.API, state *AppState, op huma.Operation, call func(context.Context, *I) (*R, error)) {
	op.Middlewares = huma.Middlewares{authed(state)}
	huma.Register(api, op, func(ctx context.Context, in *I) (*adminOut[R], error) {
		res, err := call(adminapi.WithCaller(ctx, callerOf(ctx)), in)
		if err != nil {
			return nil, apiErr(err)
		}
		emptySlices(reflect.ValueOf(res))
		return &adminOut[R]{Body: res}, nil
	})
}

// adminNoContent registers one operation that answers 204.
func adminNoContent[I, R any](api huma.API, state *AppState, op huma.Operation, call func(context.Context, *I) (*R, error)) {
	op.Middlewares = huma.Middlewares{authed(state)}
	op.DefaultStatus = http.StatusNoContent
	huma.Register(api, op, func(ctx context.Context, in *I) (*struct{}, error) {
		if _, err := call(adminapi.WithCaller(ctx, callerOf(ctx)), in); err != nil {
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
	registerUserOperations(api, state, adminapi.NewUserAdminService(cfg))
	registerRoleOperations(api, state, adminapi.NewRoleAdminService(cfg))
	registerAccountOperations(api, state, adminapi.NewAccountService(cfg))
	registerTenantOperations(api, state, adminapi.NewTenantAdminService(cfg))
	registerWorkspaceOperations(api, state, cfg)
	registerCredentialOperations(api, state, adminapi.NewCredentialsService(cfg))
}

// registerCredentialOperations: the Credentials tab (named tenant credentials, stored encrypted in
// the database or in the deployment's secret manager). Values are write-only.
func registerCredentialOperations(api huma.API, state *AppState, s *adminapi.CredentialsService) {
	const tag = "Credentials"
	adminOp(api, state, opOf("listCredentials", http.MethodGet, "/admin/credentials", "The tenant's credentials, where they are used, and where this deployment can store them", tag),
		func(ctx context.Context, _ *struct{}) (*adminapi.CredentialsView, error) {
			return s.ListCredentials(ctx, &adminapi.Empty{})
		})
	create := opOf("createCredential", http.MethodPost, "/admin/credentials", "Add a credential (write-only value)", tag)
	create.DefaultStatus = http.StatusCreated
	adminOp(api, state, create, func(ctx context.Context, in *bodyIn[adminapi.CreateCredentialRequest]) (*adminapi.CredentialsView, error) {
		return s.CreateCredential(ctx, &in.Body)
	})
	adminOp(api, state, opOf("updateCredential", http.MethodPatch, "/admin/credentials/{id}", "Rename a credential, replace its value, or move it to the other store", tag),
		func(ctx context.Context, in *idBodyIn[adminapi.UpdateCredentialRequest]) (*adminapi.CredentialsView, error) {
			in.Body.Id = in.ID
			return s.UpdateCredential(ctx, &in.Body)
		})
	adminOp(api, state, opOf("deleteCredential", http.MethodDelete, "/admin/credentials/{id}", "Remove a credential no job uses", tag),
		func(ctx context.Context, in *idPath) (*adminapi.CredentialsView, error) {
			return s.DeleteCredential(ctx, &adminapi.CredentialIdRequest{Id: in.ID})
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

func registerUserOperations(api huma.API, state *AppState, s *adminapi.UserAdminService) {
	const tag = "Users"
	id := func(v string) *adminapi.UserIdRequest { return &adminapi.UserIdRequest{Id: v} }

	adminOp(api, state, opOf("listUsers", http.MethodGet, "/admin/users", "Members of the caller's tenant", tag),
		func(ctx context.Context, in *listUsersIn) (*adminapi.ListUsersResponse, error) {
			return s.ListUsers(ctx, &adminapi.ListUsersRequest{Search: in.Search, Status: in.Status, Role: in.Role, Offset: in.Offset, Limit: in.Limit})
		})
	createUser := opOf("createUser", http.MethodPost, "/admin/users", "Add a member with a password", tag)
	createUser.DefaultStatus = http.StatusCreated
	adminOp(api, state, createUser, func(ctx context.Context, in *bodyIn[adminapi.CreateUserRequest]) (*adminapi.CreateUserResponse, error) {
		return s.CreateUser(ctx, &in.Body)
	})
	invite := opOf("inviteUser", http.MethodPost, "/admin/invitations", "Invite someone by email", tag)
	invite.DefaultStatus = http.StatusCreated
	adminOp(api, state, invite, func(ctx context.Context, in *bodyIn[adminapi.InviteUserRequest]) (*adminapi.InviteUserResponse, error) {
		return s.InviteUser(ctx, &in.Body)
	})
	adminOp(api, state, opOf("getUser", http.MethodGet, "/admin/users/{id}", "One member", tag),
		func(ctx context.Context, in *idPath) (*adminapi.User, error) { return s.GetUser(ctx, id(in.ID)) })
	adminOp(api, state, opOf("updateUser", http.MethodPatch, "/admin/users/{id}", "Change a member's display name", tag),
		func(ctx context.Context, in *idBodyIn[adminapi.UpdateUserRequest]) (*adminapi.User, error) {
			in.Body.Id = in.ID
			return s.UpdateUser(ctx, &in.Body)
		})
	adminNoContent(api, state, opOf("removeUser", http.MethodDelete, "/admin/users/{id}", "Remove a member from the tenant", tag),
		func(ctx context.Context, in *idPath) (*adminapi.Empty, error) { return s.RemoveUser(ctx, id(in.ID)) })
	adminOp(api, state, opOf("disableUser", http.MethodPost, "/admin/users/{id}/disable", "Disable a member", tag),
		func(ctx context.Context, in *idPath) (*adminapi.User, error) { return s.DisableUser(ctx, id(in.ID)) })
	adminOp(api, state, opOf("enableUser", http.MethodPost, "/admin/users/{id}/enable", "Enable a member (also unlocks)", tag),
		func(ctx context.Context, in *idPath) (*adminapi.User, error) { return s.EnableUser(ctx, id(in.ID)) })
	adminNoContent(api, state, opOf("resetUserPassword", http.MethodPost, "/admin/users/{id}/password", "Set a member's password", tag),
		func(ctx context.Context, in *idBodyIn[adminapi.ResetPasswordRequest]) (*adminapi.Empty, error) {
			in.Body.Id = in.ID
			return s.ResetPassword(ctx, &in.Body)
		})
	adminNoContent(api, state, opOf("resetUserMfa", http.MethodPost, "/admin/users/{id}/mfa/reset", "Turn off a member's two-step sign-in", tag),
		func(ctx context.Context, in *idPath) (*adminapi.Empty, error) { return s.ResetMfa(ctx, id(in.ID)) })
	adminOp(api, state, opOf("resendInvitation", http.MethodPost, "/admin/users/{id}/invitation/resend", "Send an invited member a fresh link", tag),
		func(ctx context.Context, in *idPath) (*adminapi.InviteUserResponse, error) {
			return s.ResendInvitation(ctx, id(in.ID))
		})
	adminOp(api, state, opOf("assignRole", http.MethodPost, "/admin/users/{id}/roles", "Grant a role, tenant-wide or inside a scope", tag),
		func(ctx context.Context, in *idBodyIn[roleBody]) (*adminapi.User, error) {
			return s.AssignRole(ctx, &adminapi.RoleRequest{Id: in.ID, Role: in.Body.Role, ScopeType: in.Body.ScopeType, ScopeId: in.Body.ScopeID})
		})
	adminOp(api, state, opOf("revokeRole", http.MethodDelete, "/admin/users/{id}/roles/{role}", "Take a role away", tag),
		func(ctx context.Context, in *revokeRoleIn) (*adminapi.User, error) {
			return s.RevokeRole(ctx, &adminapi.RoleRequest{Id: in.ID, Role: in.Role, ScopeType: in.ScopeType, ScopeId: in.ScopeID})
		})
	adminOp(api, state, opOf("listUserAudit", http.MethodGet, "/admin/users/{id}/audit", "Changes made to a member", tag),
		func(ctx context.Context, in *userAuditIn) (*adminapi.ListUserAuditResponse, error) {
			return s.ListUserAudit(ctx, &adminapi.ListUserAuditRequest{Id: in.ID, Cursor: in.Cursor, Limit: in.Limit})
		})
	adminOp(api, state, opOf("listScopeMembers", http.MethodGet, "/admin/scope-members", "Who holds a role inside one scope", tag),
		func(ctx context.Context, in *scopeMembersIn) (*adminapi.ListScopeMembersResponse, error) {
			return s.ListScopeMembers(ctx, &adminapi.ScopeRequest{ScopeType: in.ScopeType, ScopeId: in.ScopeID})
		})

	// Service accounts and API keys (roadmap 115 M5).
	adminOp(api, state, opOf("listServiceAccounts", http.MethodGet, "/admin/service-accounts", "The tenant's service accounts", tag),
		func(ctx context.Context, _ *struct{}) (*adminapi.ListUsersResponse, error) {
			return s.ListServiceAccounts(ctx, &adminapi.Empty{})
		})
	createSA := opOf("createServiceAccount", http.MethodPost, "/admin/service-accounts", "Add a service account", tag)
	createSA.DefaultStatus = http.StatusCreated
	adminOp(api, state, createSA, func(ctx context.Context, in *bodyIn[adminapi.CreateServiceAccountRequest]) (*adminapi.User, error) {
		return s.CreateServiceAccount(ctx, &in.Body)
	})
	adminOp(api, state, opOf("listApiKeys", http.MethodGet, "/admin/users/{id}/api-keys", "A member's API keys", tag),
		func(ctx context.Context, in *idPath) (*adminapi.ListApiKeysResponse, error) {
			return s.ListApiKeys(ctx, id(in.ID))
		})
	createKey := opOf("createApiKey", http.MethodPost, "/admin/users/{id}/api-keys", "Issue an API key (the secret is shown once)", tag)
	createKey.DefaultStatus = http.StatusCreated
	adminOp(api, state, createKey, func(ctx context.Context, in *idBodyIn[adminapi.CreateApiKeyRequest]) (*adminapi.CreateApiKeyResponse, error) {
		in.Body.UserId = in.ID
		return s.CreateApiKey(ctx, &in.Body)
	})
	adminNoContent(api, state, opOf("revokeApiKey", http.MethodDelete, "/admin/api-keys/{id}", "Revoke an API key", tag),
		func(ctx context.Context, in *idPath) (*adminapi.Empty, error) {
			return s.RevokeApiKey(ctx, &adminapi.ApiKeyIdRequest{Id: in.ID})
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
	Grants []*adminapi.RoleGrant `json:"grants"`
}

func registerRoleOperations(api huma.API, state *AppState, s *adminapi.RoleAdminService) {
	const tag = "Roles"
	name := func(v string) *adminapi.RoleNameRequest { return &adminapi.RoleNameRequest{Name: v} }

	adminOp(api, state, opOf("listRoles", http.MethodGet, "/admin/roles", "The tenant's roles", tag),
		func(ctx context.Context, in *listRolesIn) (*adminapi.ListRolesResponse, error) {
			return s.ListRoles(ctx, &adminapi.ListRolesRequest{Search: in.Search})
		})
	create := opOf("createRole", http.MethodPost, "/admin/roles", "Add a role (optionally copying another's permissions)", tag)
	create.DefaultStatus = http.StatusCreated
	adminOp(api, state, create, func(ctx context.Context, in *bodyIn[adminapi.CreateRoleRequest]) (*adminapi.Role, error) {
		return s.CreateRole(ctx, &in.Body)
	})
	adminOp(api, state, opOf("getRole", http.MethodGet, "/admin/roles/{name}", "One role", tag),
		func(ctx context.Context, in *namePath) (*adminapi.Role, error) { return s.GetRole(ctx, name(in.Name)) })
	adminOp(api, state, opOf("updateRole", http.MethodPatch, "/admin/roles/{name}", "Change a role's label or description", tag),
		func(ctx context.Context, in *nameBodyIn[adminapi.UpdateRoleRequest]) (*adminapi.Role, error) {
			in.Body.Name = in.Name
			return s.UpdateRole(ctx, &in.Body)
		})
	adminNoContent(api, state, opOf("deleteRole", http.MethodDelete, "/admin/roles/{name}", "Delete a role", tag),
		func(ctx context.Context, in *namePath) (*adminapi.Empty, error) {
			return s.DeleteRole(ctx, name(in.Name))
		})
	adminOp(api, state, opOf("getRolePermissions", http.MethodGet, "/admin/roles/{name}/permissions", "What a role may do", tag),
		func(ctx context.Context, in *namePath) (*adminapi.RolePermissions, error) {
			return s.GetRolePermissions(ctx, name(in.Name))
		})
	adminOp(api, state, opOf("setRolePermissions", http.MethodPut, "/admin/roles/{name}/permissions", "Replace what a role may do", tag),
		func(ctx context.Context, in *nameBodyIn[grantsBody]) (*adminapi.RolePermissions, error) {
			return s.SetRolePermissions(ctx, &adminapi.SetRolePermissionsRequest{Role: in.Name, Grants: in.Body.Grants})
		})
	adminOp(api, state, opOf("listRoleAudit", http.MethodGet, "/admin/roles/{name}/audit", "Changes made to a role", tag),
		func(ctx context.Context, in *roleAuditIn) (*adminapi.ListUserAuditResponse, error) {
			return s.ListRoleAudit(ctx, &adminapi.ListRoleAuditRequest{Name: in.Name, Cursor: in.Cursor, Limit: in.Limit})
		})
	adminOp(api, state, opOf("listPermissionResources", http.MethodGet, "/admin/permission-resources", "Everything a role can be granted", tag),
		func(ctx context.Context, _ *struct{}) (*adminapi.ListPermissionResourcesResponse, error) {
			return s.ListPermissionResources(ctx, &adminapi.Empty{})
		})
}

type tenantUsersIn struct {
	Search string `query:"search"`
	Limit  int32  `query:"limit"`
}

func registerAccountOperations(api huma.API, state *AppState, s *adminapi.AccountService) {
	const tag = "Account"
	empty := &adminapi.Empty{}

	adminOp(api, state, opOf("getMe", http.MethodGet, "/me", "The caller", tag),
		func(ctx context.Context, _ *struct{}) (*adminapi.Me, error) { return s.GetMe(ctx, empty) })
	adminOp(api, state, opOf("updateMyProfile", http.MethodPatch, "/me", "Change the caller's display name", tag),
		func(ctx context.Context, in *bodyIn[adminapi.UpdateProfileRequest]) (*adminapi.Me, error) {
			return s.UpdateProfile(ctx, &in.Body)
		})
	adminOp(api, state, opOf("listMyTenants", http.MethodGet, "/me/tenants", "Tenants the caller is a member of", tag),
		func(ctx context.Context, _ *struct{}) (*adminapi.ListMyTenantsResponse, error) {
			return s.ListMyTenants(ctx, empty)
		})
	adminOp(api, state, opOf("listTenantUsers", http.MethodGet, "/tenant-users", "Active members of the caller's tenant (for pickers)", tag),
		func(ctx context.Context, in *tenantUsersIn) (*adminapi.ListTenantUsersResponse, error) {
			return s.ListTenantUsers(ctx, &adminapi.ListTenantUsersRequest{Search: in.Search, Limit: in.Limit})
		})
	adminOp(api, state, opOf("getMyMfa", http.MethodGet, "/me/mfa", "The caller's two-step sign-in status", tag),
		func(ctx context.Context, _ *struct{}) (*adminapi.MfaStatus, error) { return s.GetMfaStatus(ctx, empty) })
	adminOp(api, state, opOf("beginMfaEnrollment", http.MethodPost, "/me/mfa/enrollment", "Start turning on two-step sign-in", tag),
		func(ctx context.Context, _ *struct{}) (*adminapi.MfaEnrollment, error) {
			return s.BeginMfaEnrollment(ctx, empty)
		})
	adminOp(api, state, opOf("confirmMfaEnrollment", http.MethodPost, "/me/mfa/enrollment/confirm", "Finish turning on two-step sign-in", tag),
		func(ctx context.Context, in *bodyIn[adminapi.MfaCodeRequest]) (*adminapi.MfaRecoveryCodes, error) {
			return s.ConfirmMfaEnrollment(ctx, &in.Body)
		})
	adminNoContent(api, state, opOf("disableMyMfa", http.MethodPost, "/me/mfa/disable", "Turn off two-step sign-in", tag),
		func(ctx context.Context, in *bodyIn[adminapi.MfaCodeRequest]) (*adminapi.Empty, error) {
			return s.DisableMfa(ctx, &in.Body)
		})
	adminOp(api, state, opOf("regenerateRecoveryCodes", http.MethodPost, "/me/mfa/recovery-codes", "Replace the caller's recovery codes", tag),
		func(ctx context.Context, in *bodyIn[adminapi.MfaCodeRequest]) (*adminapi.MfaRecoveryCodes, error) {
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

func registerTenantOperations(api huma.API, state *AppState, s *adminapi.TenantAdminService) {
	const tag = "Tenants"
	id := func(v string) *adminapi.TenantIdRequest { return &adminapi.TenantIdRequest{Id: v} }

	adminOp(api, state, opOf("listTenants", http.MethodGet, "/platform/tenants", "Every tenant (platform admin)", tag),
		func(ctx context.Context, in *listTenantsIn) (*adminapi.ListTenantsResponse, error) {
			return s.ListTenants(ctx, &adminapi.ListTenantsRequest{Query: in.Query, Status: in.Status, Tier: in.Tier, Limit: in.Limit, Offset: in.Offset})
		})
	create := opOf("createTenant", http.MethodPost, "/platform/tenants", "Create a tenant with its first admin", tag)
	create.DefaultStatus = http.StatusCreated
	adminOp(api, state, create, func(ctx context.Context, in *bodyIn[adminapi.CreateTenantRequest]) (*adminapi.CreateTenantResponse, error) {
		return s.CreateTenant(ctx, &in.Body)
	})
	adminOp(api, state, opOf("getTenant", http.MethodGet, "/platform/tenants/{id}", "One tenant", tag),
		func(ctx context.Context, in *idPath) (*adminapi.Tenant, error) { return s.GetTenant(ctx, id(in.ID)) })
	adminOp(api, state, opOf("updateTenant", http.MethodPatch, "/platform/tenants/{id}", "Rename a tenant or change its tier", tag),
		func(ctx context.Context, in *idBodyIn[adminapi.UpdateTenantRequest]) (*adminapi.Tenant, error) {
			in.Body.Id = in.ID
			return s.UpdateTenant(ctx, &in.Body)
		})
	adminOp(api, state, opOf("suspendTenant", http.MethodPost, "/platform/tenants/{id}/suspend", "Suspend a tenant", tag),
		func(ctx context.Context, in *idPath) (*adminapi.Tenant, error) {
			return s.SuspendTenant(ctx, id(in.ID))
		})
	adminOp(api, state, opOf("resumeTenant", http.MethodPost, "/platform/tenants/{id}/resume", "Resume a suspended tenant", tag),
		func(ctx context.Context, in *idPath) (*adminapi.Tenant, error) { return s.ResumeTenant(ctx, id(in.ID)) })
	adminOp(api, state, opOf("requestTenantDeletion", http.MethodPost, "/platform/tenants/{id}/deletion", "Schedule a tenant's deletion", tag),
		func(ctx context.Context, in *idBodyIn[deletionBody]) (*adminapi.Tenant, error) {
			return s.RequestTenantDeletion(ctx, &adminapi.RequestTenantDeletionRequest{Id: in.ID, RetentionDays: in.Body.RetentionDays})
		})
	adminOp(api, state, opOf("cancelTenantDeletion", http.MethodDelete, "/platform/tenants/{id}/deletion", "Cancel a scheduled deletion", tag),
		func(ctx context.Context, in *idPath) (*adminapi.Tenant, error) {
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
func registerWorkspaceOperations(api huma.API, state *AppState, cfg adminapi.IdentityConfig) {
	audit := adminapi.NewAuditAdminService(cfg)
	support := adminapi.NewSupportAccessService(cfg)
	owner := adminapi.NewOwnerService(cfg)
	sso := adminapi.NewSsoAdminService(cfg)
	empty := &adminapi.Empty{}

	adminOp(api, state, opOf("searchAudit", http.MethodGet, "/admin/audit", "Search the tenant's audit trail", "Audit"),
		func(ctx context.Context, in *searchAuditIn) (*adminapi.SearchAuditResponse, error) {
			return audit.SearchAudit(ctx, &adminapi.SearchAuditRequest{ActorUserId: in.Actor, Entity: in.Entity, RecordId: in.Record,
				Actions: in.Actions, From: in.From, To: in.To, Cursor: in.Cursor, Limit: in.Limit})
		})
	adminOp(api, state, opOf("verifyAuditChain", http.MethodPost, "/admin/audit/verify", "Check the audit trail's hash chain", "Audit"),
		func(ctx context.Context, _ *struct{}) (*adminapi.AuditChainReport, error) {
			return audit.VerifyAuditChain(ctx, empty)
		})

	adminOp(api, state, opOf("listSupportGrants", http.MethodGet, "/admin/support-grants", "Support access the tenant granted", "Support"),
		func(ctx context.Context, _ *struct{}) (*adminapi.ListSupportGrantsResponse, error) {
			return support.ListSupportGrants(ctx, empty)
		})
	grant := opOf("grantSupportAccess", http.MethodPost, "/admin/support-grants", "Let platform support in, for a while", "Support")
	grant.DefaultStatus = http.StatusCreated
	adminOp(api, state, grant, func(ctx context.Context, in *bodyIn[adminapi.GrantSupportAccessRequest]) (*adminapi.SupportGrant, error) {
		return support.GrantSupportAccess(ctx, &in.Body)
	})
	adminOp(api, state, opOf("revokeSupportGrant", http.MethodPost, "/admin/support-grants/{id}/revoke", "End a support grant early", "Support"),
		func(ctx context.Context, in *idPath) (*adminapi.SupportGrant, error) {
			return support.RevokeSupportGrant(ctx, &adminapi.SupportGrantIdRequest{Id: in.ID})
		})
	adminOp(api, state, opOf("startSupportSession", http.MethodPost, "/platform/support-sessions", "Open a support session in a tenant that granted access (platform admin)", "Support"),
		func(ctx context.Context, in *bodyIn[adminapi.StartSupportSessionRequest]) (*adminapi.SupportSessionToken, error) {
			return support.StartSupportSession(ctx, &in.Body)
		})

	adminOp(api, state, opOf("getOwnerStatus", http.MethodGet, "/admin/owner", "Who owns the tenant, and any open recovery", "Owner"),
		func(ctx context.Context, _ *struct{}) (*adminapi.OwnerStatus, error) {
			return owner.GetOwnerStatus(ctx, empty)
		})
	adminOp(api, state, opOf("transferOwnership", http.MethodPost, "/admin/owner/transfer", "Hand ownership to another member", "Owner"),
		func(ctx context.Context, in *bodyIn[adminapi.TransferOwnershipRequest]) (*adminapi.OwnerStatus, error) {
			return owner.TransferOwnership(ctx, &in.Body)
		})
	adminOp(api, state, opOf("cancelOwnerRecovery", http.MethodPost, "/admin/owner/recovery/cancel", "Stop an owner recovery (the owner)", "Owner"),
		func(ctx context.Context, _ *struct{}) (*adminapi.OwnerStatus, error) {
			return owner.CancelOwnerRecovery(ctx, empty)
		})
	adminOp(api, state, opOf("getTenantOwner", http.MethodGet, "/platform/tenants/{id}/owner", "A tenant's owner, any open recovery and who could take over (platform admin)", "Owner"),
		func(ctx context.Context, in *idPath) (*adminapi.TenantOwnerView, error) {
			return owner.PlatformTenantOwner(ctx, &adminapi.TenantIdOnlyRequest{TenantId: in.ID})
		})
	adminOp(api, state, opOf("requestOwnerRecovery", http.MethodPost, "/platform/owner-recovery", "Start moving ownership of a tenant whose owner is gone (platform admin)", "Owner"),
		func(ctx context.Context, in *bodyIn[adminapi.RequestOwnerRecoveryRequest]) (*adminapi.OwnerRecovery, error) {
			return owner.RequestOwnerRecovery(ctx, &in.Body)
		})
	adminOp(api, state, opOf("completeOwnerRecovery", http.MethodPost, "/platform/owner-recovery/complete", "Finish an owner recovery once its wait is over (platform admin)", "Owner"),
		func(ctx context.Context, in *bodyIn[tenantIDBody]) (*adminapi.OwnerRecovery, error) {
			return owner.CompleteOwnerRecovery(ctx, &adminapi.TenantIdOnlyRequest{TenantId: in.Body.TenantID})
		})

	adminOp(api, state, opOf("getSsoConfig", http.MethodGet, "/admin/sso", "The tenant's single sign-on setup", "SSO"),
		func(ctx context.Context, _ *struct{}) (*adminapi.SsoConfig, error) {
			return sso.GetSsoConfig(ctx, empty)
		})
	adminOp(api, state, opOf("setSsoConfig", http.MethodPut, "/admin/sso", "Set up single sign-on", "SSO"),
		func(ctx context.Context, in *bodyIn[adminapi.SetSsoConfigRequest]) (*adminapi.SsoConfig, error) {
			return sso.SetSsoConfig(ctx, &in.Body)
		})
	adminOp(api, state, opOf("getSamlConfig", http.MethodGet, "/admin/sso/saml", "The tenant's SAML single sign-on setup", "SSO"),
		func(ctx context.Context, _ *struct{}) (*adminapi.SamlConfig, error) {
			return sso.GetSamlConfig(ctx, empty)
		})
	adminOp(api, state, opOf("setSamlConfig", http.MethodPut, "/admin/sso/saml", "Set up SAML single sign-on", "SSO"),
		func(ctx context.Context, in *bodyIn[adminapi.SetSamlConfigRequest]) (*adminapi.SamlConfig, error) {
			return sso.SetSamlConfig(ctx, &in.Body)
		})
	adminNoContent(api, state, opOf("deleteSamlConfig", http.MethodDelete, "/admin/sso/saml", "Remove SAML single sign-on", "SSO"),
		func(ctx context.Context, _ *struct{}) (*adminapi.Empty, error) {
			return sso.DeleteSamlConfig(ctx, empty)
		})
	adminOp(api, state, opOf("verifySsoDomain", http.MethodPost, "/admin/sso/domains/{domain}/verify", "Check the DNS record that proves a domain is owned", "SSO"),
		func(ctx context.Context, in *struct {
			Domain string `path:"domain"`
		}) (*adminapi.SsoConfig, error) {
			return sso.VerifySsoDomain(ctx, &adminapi.VerifySsoDomainRequest{Domain: in.Domain})
		})
	adminNoContent(api, state, opOf("deleteSsoConfig", http.MethodDelete, "/admin/sso", "Remove single sign-on", "SSO"),
		func(ctx context.Context, _ *struct{}) (*adminapi.Empty, error) {
			return sso.DeleteSsoConfig(ctx, empty)
		})
}

// scimAuthenticate is SCIM's bearer-token check: the same authentication every route uses.
func scimAuthenticate(state *AppState) adminapi.SCIMAuthenticator {
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
