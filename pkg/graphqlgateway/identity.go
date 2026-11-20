// Users, roles and the caller's own account (roadmaps 113, 114), over the token issuer's
// UserAdminService / RoleAdminService / AccountService gRPC — never a REST forward. Authorization (admin role, the
// guards against self-harm and orphaning a tenant's admins) is the service's; these resolvers
// only translate.
package graphqlgateway

import (
	"context"
	"encoding/json"

	"github.com/graphql-go/graphql"
	"google.golang.org/grpc"
	grpcmd "google.golang.org/grpc/metadata"

	"github.com/metap-org/metap/pkg/grpcapi"
	"github.com/metap-org/metap/pkg/grpcapi/pb"
)

// WithIdentity adds the user-administration and account fields, served by client (the token
// issuer's identity services).
func WithIdentity(client *grpcapi.IdentityClient) Option {
	return func(c *buildConfig) { c.identity = client }
}

var (
	scopedRoleType = jsonObject("ScopedRole", map[string]graphql.Output{
		"role": nn(graphql.String), "scopeType": nn(graphql.String), "scopeId": nn(graphql.String),
	})
	scopeMemberType = jsonObject("ScopeMember", map[string]graphql.Output{
		"userId": nn(graphql.ID), "email": nn(graphql.String), "displayName": nn(graphql.String), "role": nn(graphql.String),
	})
	userType = jsonObject("User", map[string]graphql.Output{
		"id": nn(graphql.ID), "email": nn(graphql.String), "displayName": nn(graphql.String),
		"status": nn(graphql.String), "roles": nnList(graphql.String), "authProvider": nn(graphql.String),
		"hasPassword": nn(graphql.Boolean), "lastLoginAt": graphql.String, "createdAt": nn(graphql.String),
		"invitedBy": graphql.ID, "sharedIdentity": nn(graphql.Boolean), "invitationExpiresAt": graphql.String,
		"scopedRoles": nnList(scopedRoleType),
	})
	userPageType = jsonObject("UserPage", map[string]graphql.Output{
		"users": nnList(userType), "total": nn(graphql.Int),
	})
	roleType = jsonObject("Role", map[string]graphql.Output{
		"id": nn(graphql.ID), "name": nn(graphql.String), "label": graphql.String, "description": graphql.String,
		"userCount": nn(graphql.Int), "policyCount": nn(graphql.Int), "builtin": nn(graphql.Boolean),
		"createdAt": graphql.String, "updatedAt": graphql.String,
	})
	roleGrantType = jsonObject("RoleGrant", map[string]graphql.Output{
		"entity": nn(graphql.String), "action": nn(graphql.String),
	})
	rolePolicyType = jsonObject("RolePolicy", map[string]graphql.Output{
		"id": nn(graphql.ID), "entity": nn(graphql.String), "action": nn(graphql.String),
		"roles": nnList(graphql.String), "effect": nn(graphql.String), "field": graphql.String,
		"subject": nn(graphql.String), "condition": jsonScalar,
	})
	rolePermissionsType = jsonObject("RolePermissions", map[string]graphql.Output{
		"role": nn(graphql.String), "grants": nnList(roleGrantType), "advanced": nnList(rolePolicyType),
	})
	roleGrantInput = graphql.NewInputObject(graphql.InputObjectConfig{
		Name: "RoleGrantInput",
		Fields: graphql.InputObjectConfigFieldMap{
			"entity": &graphql.InputObjectFieldConfig{Type: nn(graphql.String)},
			"action": &graphql.InputObjectFieldConfig{Type: nn(graphql.String)},
		},
	})
	inviteResultType = jsonObject("InviteUserResult", map[string]graphql.Output{
		"user": nn(userType), "expiresAt": nn(graphql.String), "emailSent": nn(graphql.Boolean),
		"acceptUrl": graphql.String,
	})
	inviteUserInput = graphql.NewInputObject(graphql.InputObjectConfig{
		Name: "InviteUserInput",
		Fields: graphql.InputObjectConfigFieldMap{
			"email":       &graphql.InputObjectFieldConfig{Type: nn(graphql.String)},
			"displayName": &graphql.InputObjectFieldConfig{Type: graphql.String},
			"roles":       &graphql.InputObjectFieldConfig{Type: graphql.NewList(graphql.NewNonNull(graphql.String))},
		},
	})
	createUserResultType = jsonObject("CreateUserResult", map[string]graphql.Output{
		"user": nn(userType), "createdIdentity": nn(graphql.Boolean),
	})
	userAuditEventType = jsonObject("UserAuditEvent", map[string]graphql.Output{
		"id": nn(graphql.ID), "action": nn(graphql.String), "actorUserId": graphql.ID,
		"occurredAt": nn(graphql.String), "diff": nn(jsonScalar),
	})
	userAuditPageType = jsonObject("UserAuditPage", map[string]graphql.Output{
		"events": nnList(userAuditEventType), "nextCursor": graphql.String,
	})
	meType = jsonObject("Me", map[string]graphql.Output{
		"userId": graphql.ID, "tenantId": nn(graphql.ID), "email": graphql.String,
		"displayName": nn(graphql.String), "roles": nnList(graphql.String),
	})
	myTenantType = jsonObject("MyTenant", map[string]graphql.Output{
		"tenantId": nn(graphql.ID), "name": graphql.String, "current": nn(graphql.Boolean),
	})
	tenantUserType = jsonObject("TenantUserSummary", map[string]graphql.Output{
		"id": nn(graphql.ID), "email": nn(graphql.String), "displayName": nn(graphql.String),
	})
	createUserInput = graphql.NewInputObject(graphql.InputObjectConfig{
		Name: "CreateUserInput",
		Fields: graphql.InputObjectConfigFieldMap{
			"email":       &graphql.InputObjectFieldConfig{Type: nn(graphql.String)},
			"password":    &graphql.InputObjectFieldConfig{Type: graphql.String},
			"displayName": &graphql.InputObjectFieldConfig{Type: graphql.String},
			"roles":       &graphql.InputObjectFieldConfig{Type: graphql.NewList(graphql.NewNonNull(graphql.String))},
		},
	})
)

func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func userMap(u *pb.User) map[string]interface{} {
	roles := u.GetRoles()
	if roles == nil {
		roles = []string{}
	}
	scoped := make([]interface{}, len(u.GetScopedRoles()))
	for i, r := range u.GetScopedRoles() {
		scoped[i] = map[string]interface{}{"role": r.GetRole(), "scopeType": r.GetScopeType(), "scopeId": r.GetScopeId()}
	}
	return map[string]interface{}{
		"scopedRoles": scoped,
		"id":          u.GetId(), "email": u.GetEmail(), "displayName": u.GetDisplayName(), "status": u.GetStatus(),
		"roles": roles, "authProvider": u.GetAuthProvider(), "hasPassword": u.GetHasPassword(),
		"lastLoginAt": nullIfEmpty(u.GetLastLoginAt()), "createdAt": u.GetCreatedAt(),
		"invitedBy": nullIfEmpty(u.GetInvitedBy()), "sharedIdentity": u.GetSharedIdentity(),
		"invitationExpiresAt": nullIfEmpty(u.GetInvitationExpiresAt()),
	}
}

func roleMap(r *pb.Role) map[string]interface{} {
	return map[string]interface{}{
		"id": r.GetId(), "name": r.GetName(), "label": nullIfEmpty(r.GetLabel()),
		"description": nullIfEmpty(r.GetDescription()), "userCount": int(r.GetUserCount()),
		"policyCount": int(r.GetPolicyCount()), "builtin": r.GetBuiltin(),
		"createdAt": nullIfEmpty(r.GetCreatedAt()), "updatedAt": nullIfEmpty(r.GetUpdatedAt()),
	}
}

func rolePermissionsMap(r *pb.RolePermissions) map[string]interface{} {
	grants := make([]interface{}, len(r.GetGrants()))
	for i, g := range r.GetGrants() {
		grants[i] = map[string]interface{}{"entity": g.GetEntity(), "action": g.GetAction()}
	}
	advanced := make([]interface{}, len(r.GetAdvanced()))
	for i, p := range r.GetAdvanced() {
		// Passed through as the service sent it, never decoded here.
		var cond json.RawMessage
		if c := p.GetConditionJson(); c != "" && json.Valid([]byte(c)) {
			cond = json.RawMessage(c)
		}
		roles := p.GetRoles()
		if roles == nil {
			roles = []string{}
		}
		advanced[i] = map[string]interface{}{"id": p.GetId(), "entity": p.GetEntity(), "action": p.GetAction(),
			"roles": roles, "effect": p.GetEffect(), "field": nullIfEmpty(p.GetField()), "subject": p.GetSubject(),
			"condition": cond}
	}
	return map[string]interface{}{"role": r.GetRole(), "grants": grants, "advanced": advanced}
}

// auditPageMap is the UserAuditPage shape, shared by userAudit and roleAudit.
func auditPageMap(res *pb.ListUserAuditResponse) map[string]interface{} {
	events := make([]interface{}, len(res.GetEvents()))
	for i, e := range res.GetEvents() {
		var diff interface{} = map[string]interface{}{}
		if e.GetDiffJson() != "" {
			_ = json.Unmarshal([]byte(e.GetDiffJson()), &diff)
		}
		events[i] = map[string]interface{}{"id": e.GetId(), "action": e.GetAction(),
			"actorUserId": nullIfEmpty(e.GetActorUserId()), "occurredAt": e.GetOccurredAt(), "diff": diff}
	}
	return map[string]interface{}{"events": events, "nextCursor": nullIfEmpty(res.GetNextCursor())}
}

func meMap(m *pb.Me) map[string]interface{} {
	roles := m.GetRoles()
	if roles == nil {
		roles = []string{}
	}
	return map[string]interface{}{
		"userId": nullIfEmpty(m.GetUserId()), "tenantId": m.GetTenantId(), "email": nullIfEmpty(m.GetEmail()),
		"displayName": m.GetDisplayName(), "roles": roles,
	}
}

// identityCall runs one RPC as the GraphQL caller and maps a failure onto the error extensions.
func identityCall[T any](ctx context.Context, fn func(ctx context.Context, opts ...grpc.CallOption) (T, error)) (T, error) {
	var trailer grpcmd.MD
	out, err := fn(grpcapi.CallContext(ctx, requestContextFrom(ctx).ForwardedBearerToken), grpc.Trailer(&trailer))
	if err != nil {
		var zero T
		return zero, gqlError(grpcapi.CallError(err, trailer))
	}
	return out, nil
}

func idArgs() graphql.FieldConfigArgument {
	return graphql.FieldConfigArgument{"id": requiredIDArg()}
}

func addIdentityFields(query, mutation graphql.Fields, c *grpcapi.IdentityClient) {
	addTenantFields(query, mutation, c)
	addAPIKeyFields(query, mutation, c)
	addMFAFields(query, mutation, c)
	addSessionFields(query, mutation, c)
	addResourceFields(query, c)
	asUser := func(call func(ctx context.Context, opts ...grpc.CallOption) (*pb.User, error), ctx context.Context) (interface{}, error) {
		u, err := identityCall(ctx, call)
		if err != nil {
			return nil, err
		}
		return userMap(u), nil
	}

	query["users"] = &graphql.Field{
		Type: nn(userPageType),
		Args: graphql.FieldConfigArgument{
			"search": optStrArg(), "status": optStrArg(), "role": optStrArg(),
			"offset": &graphql.ArgumentConfig{Type: graphql.Int}, "limit": &graphql.ArgumentConfig{Type: graphql.Int},
		},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			req := &pb.ListUsersRequest{Search: argString(p.Args, "search"), Status: argString(p.Args, "status"),
				Role: argString(p.Args, "role"), Offset: int32Arg(p.Args, "offset"), Limit: int32Arg(p.Args, "limit")}
			res, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.ListUsersResponse, error) {
				return c.Admin.ListUsers(ctx, req, o...)
			})
			if err != nil {
				return nil, err
			}
			users := make([]interface{}, len(res.GetUsers()))
			for i, u := range res.GetUsers() {
				users[i] = userMap(u)
			}
			return map[string]interface{}{"users": users, "total": int(res.GetTotal())}, nil
		},
	}
	query["scopeMembers"] = &graphql.Field{
		Type: nnList(scopeMemberType),
		Args: graphql.FieldConfigArgument{"scopeType": strArg(), "scopeId": strArg()},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			req := &pb.ScopeRequest{ScopeType: argString(p.Args, "scopeType"), ScopeId: argString(p.Args, "scopeId")}
			res, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.ListScopeMembersResponse, error) {
				return c.Admin.ListScopeMembers(ctx, req, o...)
			})
			if err != nil {
				return nil, err
			}
			out := make([]interface{}, len(res.GetMembers()))
			for i, m := range res.GetMembers() {
				out[i] = map[string]interface{}{"userId": m.GetUserId(), "email": m.GetEmail(), "displayName": m.GetDisplayName(), "role": m.GetRole()}
			}
			return out, nil
		},
	}
	query["user"] = &graphql.Field{
		Type: userType, Args: idArgs(),
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			return asUser(func(ctx context.Context, o ...grpc.CallOption) (*pb.User, error) {
				return c.Admin.GetUser(ctx, &pb.UserIdRequest{Id: argString(p.Args, "id")}, o...)
			}, p.Context)
		},
	}
	asRole := func(call func(ctx context.Context, opts ...grpc.CallOption) (*pb.Role, error), ctx context.Context) (interface{}, error) {
		r, err := identityCall(ctx, call)
		if err != nil {
			return nil, err
		}
		return roleMap(r), nil
	}
	query["roles"] = &graphql.Field{
		Type: nnList(roleType),
		Args: graphql.FieldConfigArgument{"search": optStrArg()},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			res, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.ListRolesResponse, error) {
				return c.Roles.ListRoles(ctx, &pb.ListRolesRequest{Search: argString(p.Args, "search")}, o...)
			})
			if err != nil {
				return nil, err
			}
			out := make([]interface{}, len(res.GetRoles()))
			for i, r := range res.GetRoles() {
				out[i] = roleMap(r)
			}
			return out, nil
		},
	}
	query["role"] = &graphql.Field{
		Type: roleType,
		Args: graphql.FieldConfigArgument{"name": strArg()},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			return asRole(func(ctx context.Context, o ...grpc.CallOption) (*pb.Role, error) {
				return c.Roles.GetRole(ctx, &pb.RoleNameRequest{Name: argString(p.Args, "name")}, o...)
			}, p.Context)
		},
	}
	query["rolePermissions"] = &graphql.Field{
		Type: nn(rolePermissionsType),
		Args: graphql.FieldConfigArgument{"name": strArg()},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			res, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.RolePermissions, error) {
				return c.Roles.GetRolePermissions(ctx, &pb.RoleNameRequest{Name: argString(p.Args, "name")}, o...)
			})
			if err != nil {
				return nil, err
			}
			return rolePermissionsMap(res), nil
		},
	}
	query["userAudit"] = &graphql.Field{
		Type: nn(userAuditPageType),
		Args: graphql.FieldConfigArgument{
			"id": requiredIDArg(), "cursor": optStrArg(), "limit": &graphql.ArgumentConfig{Type: graphql.Int},
		},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			req := &pb.ListUserAuditRequest{Id: argString(p.Args, "id"), Cursor: argString(p.Args, "cursor"), Limit: int32Arg(p.Args, "limit")}
			res, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.ListUserAuditResponse, error) {
				return c.Admin.ListUserAudit(ctx, req, o...)
			})
			if err != nil {
				return nil, err
			}
			return auditPageMap(res), nil
		},
	}
	query["roleAudit"] = &graphql.Field{
		Type: nn(userAuditPageType),
		Args: graphql.FieldConfigArgument{
			"name": strArg(), "cursor": optStrArg(), "limit": &graphql.ArgumentConfig{Type: graphql.Int},
		},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			req := &pb.ListRoleAuditRequest{Name: argString(p.Args, "name"), Cursor: argString(p.Args, "cursor"), Limit: int32Arg(p.Args, "limit")}
			res, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.ListUserAuditResponse, error) {
				return c.Roles.ListRoleAudit(ctx, req, o...)
			})
			if err != nil {
				return nil, err
			}
			return auditPageMap(res), nil
		},
	}
	query["me"] = &graphql.Field{
		Type: nn(meType),
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			m, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.Me, error) {
				return c.Account.GetMe(ctx, &pb.Empty{}, o...)
			})
			if err != nil {
				return nil, err
			}
			return meMap(m), nil
		},
	}
	query["myTenants"] = &graphql.Field{
		Type: nnList(myTenantType),
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			res, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.ListMyTenantsResponse, error) {
				return c.Account.ListMyTenants(ctx, &pb.Empty{}, o...)
			})
			if err != nil {
				return nil, err
			}
			out := make([]interface{}, len(res.GetTenants()))
			for i, t := range res.GetTenants() {
				out[i] = map[string]interface{}{"tenantId": t.GetTenantId(), "name": nullIfEmpty(t.GetName()), "current": t.GetCurrent()}
			}
			return out, nil
		},
	}
	query["tenantUsers"] = &graphql.Field{
		Type: nnList(tenantUserType),
		Args: graphql.FieldConfigArgument{"search": optStrArg(), "limit": &graphql.ArgumentConfig{Type: graphql.Int}},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			req := &pb.ListTenantUsersRequest{Search: argString(p.Args, "search"), Limit: int32Arg(p.Args, "limit")}
			res, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.ListTenantUsersResponse, error) {
				return c.Account.ListTenantUsers(ctx, req, o...)
			})
			if err != nil {
				return nil, err
			}
			out := make([]interface{}, len(res.GetUsers()))
			for i, u := range res.GetUsers() {
				out[i] = map[string]interface{}{"id": u.GetId(), "email": u.GetEmail(), "displayName": u.GetDisplayName()}
			}
			return out, nil
		},
	}

	mutation["createUser"] = &graphql.Field{
		Type: nn(createUserResultType),
		Args: graphql.FieldConfigArgument{"input": &graphql.ArgumentConfig{Type: nn(createUserInput)}},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			in, _ := p.Args["input"].(map[string]interface{})
			req := &pb.CreateUserRequest{Email: argString(in, "email"), Password: argString(in, "password"),
				DisplayName: argString(in, "displayName")}
			if roles, ok := in["roles"].([]interface{}); ok {
				for _, r := range roles {
					if s, ok := r.(string); ok {
						req.Roles = append(req.Roles, s)
					}
				}
			}
			res, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.CreateUserResponse, error) {
				return c.Admin.CreateUser(ctx, req, o...)
			})
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{"user": userMap(res.GetUser()), "createdIdentity": res.GetCreatedIdentity()}, nil
		},
	}
	inviteResult := func(res *pb.InviteUserResponse) map[string]interface{} {
		return map[string]interface{}{"user": userMap(res.GetUser()), "expiresAt": res.GetExpiresAt(),
			"emailSent": res.GetEmailSent(), "acceptUrl": nullIfEmpty(res.GetAcceptUrl())}
	}
	mutation["inviteUser"] = &graphql.Field{
		Type: nn(inviteResultType),
		Args: graphql.FieldConfigArgument{"input": &graphql.ArgumentConfig{Type: nn(inviteUserInput)}},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			in, _ := p.Args["input"].(map[string]interface{})
			req := &pb.InviteUserRequest{Email: argString(in, "email"), DisplayName: argString(in, "displayName")}
			if roles, ok := in["roles"].([]interface{}); ok {
				for _, r := range roles {
					if s, ok := r.(string); ok {
						req.Roles = append(req.Roles, s)
					}
				}
			}
			res, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.InviteUserResponse, error) {
				return c.Admin.InviteUser(ctx, req, o...)
			})
			if err != nil {
				return nil, err
			}
			return inviteResult(res), nil
		},
	}
	mutation["resendInvitation"] = &graphql.Field{
		Type: nn(inviteResultType), Args: idArgs(),
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			res, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.InviteUserResponse, error) {
				return c.Admin.ResendInvitation(ctx, &pb.UserIdRequest{Id: argString(p.Args, "id")}, o...)
			})
			if err != nil {
				return nil, err
			}
			return inviteResult(res), nil
		},
	}
	mutation["updateUser"] = &graphql.Field{
		Type: nn(userType),
		Args: graphql.FieldConfigArgument{"id": requiredIDArg(), "displayName": strArg()},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			return asUser(func(ctx context.Context, o ...grpc.CallOption) (*pb.User, error) {
				return c.Admin.UpdateUser(ctx, &pb.UpdateUserRequest{Id: argString(p.Args, "id"), DisplayName: argString(p.Args, "displayName")}, o...)
			}, p.Context)
		},
	}
	mutation["disableUser"] = &graphql.Field{
		Type: nn(userType), Args: idArgs(),
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			return asUser(func(ctx context.Context, o ...grpc.CallOption) (*pb.User, error) {
				return c.Admin.DisableUser(ctx, &pb.UserIdRequest{Id: argString(p.Args, "id")}, o...)
			}, p.Context)
		},
	}
	mutation["enableUser"] = &graphql.Field{
		Type: nn(userType), Args: idArgs(),
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			return asUser(func(ctx context.Context, o ...grpc.CallOption) (*pb.User, error) {
				return c.Admin.EnableUser(ctx, &pb.UserIdRequest{Id: argString(p.Args, "id")}, o...)
			}, p.Context)
		},
	}
	mutation["removeUser"] = &graphql.Field{
		Type: nn(graphql.Boolean), Args: idArgs(),
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			_, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.Empty, error) {
				return c.Admin.RemoveUser(ctx, &pb.UserIdRequest{Id: argString(p.Args, "id")}, o...)
			})
			return err == nil, err
		},
	}
	mutation["resetUserPassword"] = &graphql.Field{
		Type: nn(graphql.Boolean),
		Args: graphql.FieldConfigArgument{"id": requiredIDArg(), "password": strArg()},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			_, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.Empty, error) {
				return c.Admin.ResetPassword(ctx, &pb.ResetPasswordRequest{Id: argString(p.Args, "id"), Password: argString(p.Args, "password")}, o...)
			})
			return err == nil, err
		},
	}
	roleMutation := func(assign bool) *graphql.Field {
		return &graphql.Field{
			Type: nn(userType),
			// scopeType + scopeId together make the role hold only on that scope (roadmap 115 M1).
			Args: graphql.FieldConfigArgument{"userId": requiredIDArg(), "role": strArg(), "scopeType": optStrArg(), "scopeId": optStrArg()},
			Resolve: func(p graphql.ResolveParams) (interface{}, error) {
				req := &pb.RoleRequest{Id: argString(p.Args, "userId"), Role: argString(p.Args, "role"),
					ScopeType: argString(p.Args, "scopeType"), ScopeId: argString(p.Args, "scopeId")}
				return asUser(func(ctx context.Context, o ...grpc.CallOption) (*pb.User, error) {
					if assign {
						return c.Admin.AssignRole(ctx, req, o...)
					}
					return c.Admin.RevokeRole(ctx, req, o...)
				}, p.Context)
			},
		}
	}
	mutation["assignUserRole"] = roleMutation(true)
	mutation["revokeUserRole"] = roleMutation(false)
	roleInputArgs := func() graphql.FieldConfigArgument {
		return graphql.FieldConfigArgument{"name": strArg(), "label": optStrArg(), "description": optStrArg()}
	}
	createRoleArgs := roleInputArgs()
	createRoleArgs["copyFrom"] = optStrArg()
	mutation["createRole"] = &graphql.Field{
		Type: nn(roleType), Args: createRoleArgs,
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			req := &pb.CreateRoleRequest{Name: argString(p.Args, "name"), Label: argString(p.Args, "label"),
				Description: argString(p.Args, "description"), CopyFrom: argString(p.Args, "copyFrom")}
			return asRole(func(ctx context.Context, o ...grpc.CallOption) (*pb.Role, error) {
				return c.Roles.CreateRole(ctx, req, o...)
			}, p.Context)
		},
	}
	mutation["updateRole"] = &graphql.Field{
		Type: nn(roleType), Args: roleInputArgs(),
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			req := &pb.UpdateRoleRequest{Name: argString(p.Args, "name"), Label: argString(p.Args, "label"),
				Description: argString(p.Args, "description")}
			return asRole(func(ctx context.Context, o ...grpc.CallOption) (*pb.Role, error) {
				return c.Roles.UpdateRole(ctx, req, o...)
			}, p.Context)
		},
	}
	mutation["setRolePermissions"] = &graphql.Field{
		Type: nn(rolePermissionsType),
		Args: graphql.FieldConfigArgument{
			"role":   strArg(),
			"grants": &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(roleGrantInput)))},
		},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			req := &pb.SetRolePermissionsRequest{Role: argString(p.Args, "role")}
			if grants, ok := p.Args["grants"].([]interface{}); ok {
				for _, g := range grants {
					m, _ := g.(map[string]interface{})
					req.Grants = append(req.Grants, &pb.RoleGrant{Entity: argString(m, "entity"), Action: argString(m, "action")})
				}
			}
			res, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.RolePermissions, error) {
				return c.Roles.SetRolePermissions(ctx, req, o...)
			})
			if err != nil {
				return nil, err
			}
			return rolePermissionsMap(res), nil
		},
	}
	mutation["deleteRole"] = &graphql.Field{
		Type: nn(graphql.Boolean),
		Args: graphql.FieldConfigArgument{"name": strArg()},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			_, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.Empty, error) {
				return c.Roles.DeleteRole(ctx, &pb.RoleNameRequest{Name: argString(p.Args, "name")}, o...)
			})
			return err == nil, err
		},
	}
	mutation["updateMyProfile"] = &graphql.Field{
		Type: nn(meType),
		Args: graphql.FieldConfigArgument{"displayName": strArg()},
		Resolve: func(p graphql.ResolveParams) (interface{}, error) {
			m, err := identityCall(p.Context, func(ctx context.Context, o ...grpc.CallOption) (*pb.Me, error) {
				return c.Account.UpdateProfile(ctx, &pb.UpdateProfileRequest{DisplayName: argString(p.Args, "displayName")}, o...)
			})
			if err != nil {
				return nil, err
			}
			return meMap(m), nil
		},
	}
}
