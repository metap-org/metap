// Platform (non-entity) GraphQL fields: users/roles, policies, cron jobs, OAuth clients,
// dashboards, preferences.
// None of these is a MetadataRegistry entity, but every one of them already exists as a REST route
// on every metap HTTP service (pkg/httpserver, cronroutes, oauth2routes, dashboardroutes), with
// the same camelCase JSON shapes inside a {"data": ...} envelope. So each field here is a thin
// forward: call the matching REST route on the platform service with the caller's own token, unwrap
// "data", and map an {"error": {...}} body onto the same GraphQL error extensions the entity
// fields use. Authorization (admin role, platform-admin) is enforced by that REST route, not here.
package graphqlgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/graphql-go/graphql"
)

// platformREST forwards one platform field to a metap REST route.
type platformREST struct {
	base   string
	client *http.Client
}

// call returns the response's "data" value (nil for a 204). notFoundAsNull turns a 404 into a
// plain null, for the nullable single-object fields (cronJob, cronJobWorkflowRun).
func (p *platformREST) call(ctx context.Context, method, path string, query url.Values, body interface{}, notFoundAsNull bool) (interface{}, error) {
	rc := requestContextFrom(ctx)
	if rc.ForwardedBearerToken == "" {
		return nil, fmt.Errorf("no caller token to forward — this should be unreachable")
	}
	target := p.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+rc.ForwardedBearerToken)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode == http.StatusNotFound && notFoundAsNull {
		return nil, nil
	}
	raw, err := readUpstream(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response from %s %s: %w", method, path, err)
	}
	var parsed map[string]interface{} // nosemgrep: go.lang.security.deserialization.unsafe-deserialization-interface.go-unsafe-deserialization-interface
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("parsing response from %s %s (status %d): %w", method, path, resp.StatusCode, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, restError(resp.StatusCode, parsed)
	}
	return parsed["data"], nil
}

func restError(status int, parsed map[string]interface{}) error {
	code, message := "request_failed", ""
	var fieldErrors map[string][]string
	if errObj, ok := parsed["error"].(map[string]interface{}); ok {
		if c, ok := errObj["code"].(string); ok {
			code = c
		}
		if m, ok := errObj["message"].(string); ok {
			message = m
		}
		if fe, ok := errObj["fieldErrors"].(map[string]interface{}); ok {
			fieldErrors = map[string][]string{}
			for k, v := range fe {
				list, _ := v.([]interface{})
				for _, item := range list {
					if s, ok := item.(string); ok {
						fieldErrors[k] = append(fieldErrors[k], s)
					}
				}
			}
		}
	}
	var msg *string
	if message != "" {
		msg = &message
	}
	return newServiceError(uint16(status), code, msg, fieldErrors)
}

// --- object types (default resolver reads each field straight out of the decoded JSON map) ---

func nn(t graphql.Output) graphql.Output { return graphql.NewNonNull(t) }
func nnList(t graphql.Output) graphql.Output {
	return graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(t)))
}

func jsonObject(name string, fields map[string]graphql.Output) *graphql.Object {
	gf := graphql.Fields{}
	for k, t := range fields {
		gf[k] = &graphql.Field{Type: t}
	}
	return graphql.NewObject(graphql.ObjectConfig{Name: name, Fields: gf})
}

var (
	adminUserSummaryType = jsonObject("AdminUserSummary", map[string]graphql.Output{
		"userId": nn(graphql.ID), "roles": nnList(graphql.String),
	})
	createAdminUserResultType = jsonObject("CreateAdminUserResult", map[string]graphql.Output{
		"userId": nn(graphql.ID), "email": nn(graphql.String), "roles": nnList(graphql.String),
	})
	tenantUserSummaryType = jsonObject("TenantUserSummary", map[string]graphql.Output{
		"id": nn(graphql.ID), "email": nn(graphql.String),
	})
	policyType = jsonObject("Policy", map[string]graphql.Output{
		"id": nn(graphql.ID), "tenantId": nn(graphql.ID), "entity": nn(graphql.String),
		"action": nn(graphql.String), "field": graphql.String, "subject": nn(graphql.String),
		"roles": graphql.NewList(graphql.NewNonNull(graphql.String)), "condition": jsonScalar,
		"createdBy": graphql.ID, "effect": nn(graphql.String),
	})
	cronJobType = jsonObject("CronJob", map[string]graphql.Output{
		"id": nn(graphql.ID), "tenantId": nn(graphql.ID), "name": nn(graphql.String),
		"enabled": nn(graphql.Boolean), "triggerType": nn(graphql.String), "triggerConfig": jsonScalar,
		"cronExpr": graphql.String, "timezone": nn(graphql.String), "targetType": nn(graphql.String),
		"targetConfig": nn(jsonScalar), "dispatchMode": nn(graphql.String), "maxAttempts": nn(graphql.Int),
		"retryBackoffSeconds": nn(graphql.Int), "nextRunAt": graphql.String, "lastRunAt": graphql.String,
		"createdAt": nn(graphql.String), "updatedAt": nn(graphql.String), "createdBy": graphql.ID,
	})
	cronJobRunType = jsonObject("CronJobRun", map[string]graphql.Output{
		"id": nn(graphql.ID), "tenantId": nn(graphql.ID), "jobId": nn(graphql.ID),
		"status": nn(graphql.String), "attempt": nn(graphql.Int), "scheduledFor": nn(graphql.String),
		"startedAt": graphql.String, "finishedAt": graphql.String, "error": graphql.String,
		"responseSummary": jsonScalar, "createdAt": nn(graphql.String),
	})
	oauthClientType = jsonObject("OAuthClient", map[string]graphql.Output{
		"id": nn(graphql.ID), "clientId": nn(graphql.String), "name": nn(graphql.String),
		"redirectUris": nnList(graphql.String), "allowedScopes": nnList(graphql.String),
		"isConfidential": nn(graphql.Boolean), "serviceUserId": graphql.ID, "clientSecret": graphql.String,
	})
	dashboardConfigType = jsonObject("DashboardConfig", map[string]graphql.Output{
		"id": nn(graphql.ID), "ownerUserId": graphql.ID, "layout": nn(jsonScalar), "updatedAt": nn(graphql.String),
	})
	preferencesType = jsonObject("Preferences", map[string]graphql.Output{
		"locale": nn(graphql.String),
	})
	platformConfigItemType = jsonObject("PlatformConfigItem", map[string]graphql.Output{
		"key": nn(graphql.String), "value": nn(jsonScalar), "level": nn(graphql.String),
		"tenantOverridable": nn(graphql.Boolean),
	})
	tenantConfigItemType = jsonObject("TenantConfigItem", map[string]graphql.Output{
		"key": nn(graphql.String), "value": nn(jsonScalar), "level": nn(graphql.String),
		"overridden": nn(graphql.Boolean), "public": nn(graphql.Boolean),
	})
	setConfigResultType = jsonObject("SetConfigResult", map[string]graphql.Output{
		"key": nn(graphql.String), "value": nn(jsonScalar), "appliesImmediately": nn(graphql.Boolean),
	})
	setTenantConfigResultType = jsonObject("SetTenantConfigResult", map[string]graphql.Output{
		"key": nn(graphql.String), "value": nn(jsonScalar), "overridden": nn(graphql.Boolean),
	})

	matrixGrantInputType = graphql.NewInputObject(graphql.InputObjectConfig{
		Name: "MatrixGrantInput",
		Fields: graphql.InputObjectConfigFieldMap{
			"role":   &graphql.InputObjectFieldConfig{Type: graphql.String},
			"action": &graphql.InputObjectFieldConfig{Type: graphql.NewNonNull(graphql.String)},
		},
	})
	cronJobInputType       = cronInput("CronJobInput", true)
	cronJobUpdateInputType = cronInput("CronJobUpdateInput", false)
)

// cronInput builds CronJobInput (name/targetType/targetConfig required) or CronJobUpdateInput
// (everything optional — a PATCH).
func cronInput(name string, create bool) *graphql.InputObject {
	req := func(t graphql.Input) graphql.Input {
		if create {
			return graphql.NewNonNull(t)
		}
		return t
	}
	return graphql.NewInputObject(graphql.InputObjectConfig{
		Name: name,
		Fields: graphql.InputObjectConfigFieldMap{
			"name":                &graphql.InputObjectFieldConfig{Type: req(graphql.String)},
			"triggerType":         &graphql.InputObjectFieldConfig{Type: graphql.String},
			"triggerConfig":       &graphql.InputObjectFieldConfig{Type: jsonScalar},
			"cronExpr":            &graphql.InputObjectFieldConfig{Type: graphql.String},
			"timezone":            &graphql.InputObjectFieldConfig{Type: graphql.String},
			"targetType":          &graphql.InputObjectFieldConfig{Type: req(graphql.String)},
			"targetConfig":        &graphql.InputObjectFieldConfig{Type: req(jsonScalar)},
			"dispatchMode":        &graphql.InputObjectFieldConfig{Type: graphql.String},
			"maxAttempts":         &graphql.InputObjectFieldConfig{Type: graphql.Int},
			"retryBackoffSeconds": &graphql.InputObjectFieldConfig{Type: graphql.Int},
			"enabled":             &graphql.InputObjectFieldConfig{Type: graphql.Boolean},
		},
	})
}

// --- argument helpers ---

func argString(args map[string]interface{}, name string) string {
	s, _ := args[name].(string)
	return s
}

// bodyFrom copies the named args that were actually provided (and are non-null) into a REST
// request body — the REST handlers use omitempty/pointer fields, so an absent key and a missing
// one mean the same thing there, but an explicit JSON null would not for a RawMessage field.
func bodyFrom(args map[string]interface{}, names ...string) map[string]interface{} {
	body := map[string]interface{}{}
	for _, n := range names {
		if v, ok := args[n]; ok && v != nil {
			body[n] = v
		}
	}
	return body
}

func withoutNulls(v interface{}) map[string]interface{} {
	m, _ := v.(map[string]interface{})
	out := map[string]interface{}{}
	for k, val := range m {
		if val != nil {
			out[k] = val
		}
	}
	return out
}

func pathID(args map[string]interface{}, name string) (string, error) {
	id, err := parseID(args, name)
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

func listOrEmpty(v interface{}, err error) (interface{}, error) {
	if err != nil {
		return nil, err
	}
	if v == nil {
		return []interface{}{}, nil
	}
	return v, nil
}

func trueOn(_ interface{}, err error) (interface{}, error) {
	if err != nil {
		return nil, err
	}
	return true, nil
}

func requiredIDArg() *graphql.ArgumentConfig {
	return &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.ID)}
}
func strArg() *graphql.ArgumentConfig {
	return &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.String)}
}
func optStrArg() *graphql.ArgumentConfig { return &graphql.ArgumentConfig{Type: graphql.String} }

// addPlatformFields registers every platform query/mutation field, each forwarding to base (the
// REST root of the service that serves the platform routes — the service that hosts them).
func addPlatformFields(queryFields, mutationFields graphql.Fields, base string) {
	p := &platformREST{base: base, client: httpClientForForwarding()}
	get := func(path string) graphql.FieldResolveFn {
		return func(rp graphql.ResolveParams) (interface{}, error) {
			return p.call(rp.Context, http.MethodGet, path, nil, nil, false)
		}
	}
	getList := func(path string) graphql.FieldResolveFn {
		return func(rp graphql.ResolveParams) (interface{}, error) {
			return listOrEmpty(p.call(rp.Context, http.MethodGet, path, nil, nil, false))
		}
	}

	// ---- queries ----
	queryFields["adminUsers"] = &graphql.Field{Type: nnList(adminUserSummaryType), Resolve: getList("/admin/users")}
	queryFields["tenantUsers"] = &graphql.Field{Type: nnList(tenantUserSummaryType), Resolve: getList("/users")}
	queryFields["policies"] = &graphql.Field{
		Type: nnList(policyType),
		Args: graphql.FieldConfigArgument{"entity": optStrArg()},
		Resolve: func(rp graphql.ResolveParams) (interface{}, error) {
			q := url.Values{}
			if e := argString(rp.Args, "entity"); e != "" {
				q.Set("entity", e)
			}
			return listOrEmpty(p.call(rp.Context, http.MethodGet, "/admin/policies", q, nil, false))
		},
	}
	queryFields["cronJobs"] = &graphql.Field{Type: nnList(cronJobType), Resolve: getList("/admin/cron-jobs")}
	queryFields["cronJob"] = &graphql.Field{
		Type: cronJobType,
		Args: graphql.FieldConfigArgument{"id": requiredIDArg()},
		Resolve: func(rp graphql.ResolveParams) (interface{}, error) {
			id, err := pathID(rp.Args, "id")
			if err != nil {
				return nil, err
			}
			return p.call(rp.Context, http.MethodGet, "/admin/cron-jobs/"+id, nil, nil, true)
		},
	}
	queryFields["cronJobRuns"] = &graphql.Field{
		Type: nnList(cronJobRunType),
		Args: graphql.FieldConfigArgument{"id": requiredIDArg(), "limit": &graphql.ArgumentConfig{Type: graphql.Int}},
		Resolve: func(rp graphql.ResolveParams) (interface{}, error) {
			id, err := pathID(rp.Args, "id")
			if err != nil {
				return nil, err
			}
			q := url.Values{}
			if _, ok := rp.Args["limit"]; ok {
				q.Set("limit", strconv.Itoa(int(int32Arg(rp.Args, "limit"))))
			}
			return listOrEmpty(p.call(rp.Context, http.MethodGet, "/admin/cron-jobs/"+id+"/runs", q, nil, false))
		},
	}
	queryFields["cronJobWorkflowRun"] = &graphql.Field{
		Type: jsonScalar,
		Args: graphql.FieldConfigArgument{"jobId": requiredIDArg(), "runId": requiredIDArg()},
		Resolve: func(rp graphql.ResolveParams) (interface{}, error) {
			jobID, err := pathID(rp.Args, "jobId")
			if err != nil {
				return nil, err
			}
			runID, err := pathID(rp.Args, "runId")
			if err != nil {
				return nil, err
			}
			return p.call(rp.Context, http.MethodGet, "/admin/cron-jobs/"+jobID+"/runs/"+runID+"/workflow-run", nil, nil, true)
		},
	}
	queryFields["oauthClients"] = &graphql.Field{Type: nnList(oauthClientType), Resolve: getList("/admin/oauth/clients")}
	queryFields["myDashboard"] = &graphql.Field{Type: dashboardConfigType, Resolve: get("/dashboards/me")}
	queryFields["tenantDefaultDashboard"] = &graphql.Field{Type: dashboardConfigType, Resolve: get("/dashboards/tenant-default")}
	queryFields["myPreferences"] = &graphql.Field{Type: nn(preferencesType), Resolve: get("/preferences")}
	queryFields["platformConfig"] = &graphql.Field{Type: nnList(platformConfigItemType), Resolve: getList("/platform/config")}
	queryFields["tenantConfig"] = &graphql.Field{Type: nnList(tenantConfigItemType), Resolve: getList("/admin/config")}

	// ---- users / roles ----
	mutationFields["createAdminUser"] = &graphql.Field{
		Type: nn(createAdminUserResultType),
		Args: graphql.FieldConfigArgument{
			"email": strArg(), "password": strArg(),
			"roles": &graphql.ArgumentConfig{Type: graphql.NewList(graphql.NewNonNull(graphql.String))},
		},
		Resolve: func(rp graphql.ResolveParams) (interface{}, error) {
			return p.call(rp.Context, http.MethodPost, "/admin/users", nil, bodyFrom(rp.Args, "email", "password", "roles"), false)
		},
	}
	mutationFields["assignUserRole"] = &graphql.Field{
		Type: nn(graphql.Boolean),
		Args: graphql.FieldConfigArgument{"userId": requiredIDArg(), "role": strArg()},
		Resolve: func(rp graphql.ResolveParams) (interface{}, error) {
			userID, err := pathID(rp.Args, "userId")
			if err != nil {
				return nil, err
			}
			return trueOn(p.call(rp.Context, http.MethodPost, "/admin/users/"+userID+"/roles", nil, bodyFrom(rp.Args, "role"), false))
		},
	}
	mutationFields["revokeUserRole"] = &graphql.Field{
		Type: nn(graphql.Boolean),
		Args: graphql.FieldConfigArgument{"userId": requiredIDArg(), "role": strArg()},
		Resolve: func(rp graphql.ResolveParams) (interface{}, error) {
			userID, err := pathID(rp.Args, "userId")
			if err != nil {
				return nil, err
			}
			path := "/admin/users/" + userID + "/roles/" + url.PathEscape(argString(rp.Args, "role"))
			return trueOn(p.call(rp.Context, http.MethodDelete, path, nil, nil, false))
		},
	}
	mutationFields["invalidateUserContextCache"] = &graphql.Field{
		Type: nn(graphql.Boolean),
		Args: graphql.FieldConfigArgument{"userId": requiredIDArg()},
		Resolve: func(rp graphql.ResolveParams) (interface{}, error) {
			userID, err := pathID(rp.Args, "userId")
			if err != nil {
				return nil, err
			}
			return trueOn(p.call(rp.Context, http.MethodPost, "/admin/users/"+userID+"/context/invalidate", nil, nil, false))
		},
	}

	// ---- policies ----
	mutationFields["createPolicy"] = &graphql.Field{
		Type: nn(policyType),
		Args: graphql.FieldConfigArgument{
			"entity": strArg(), "action": strArg(),
			"roles":     &graphql.ArgumentConfig{Type: graphql.NewList(graphql.NewNonNull(graphql.String))},
			"condition": &graphql.ArgumentConfig{Type: jsonScalar},
			"field":     optStrArg(), "subject": optStrArg(), "effect": optStrArg(),
		},
		Resolve: func(rp graphql.ResolveParams) (interface{}, error) {
			body := bodyFrom(rp.Args, "entity", "action", "roles", "condition", "field", "subject", "effect")
			return p.call(rp.Context, http.MethodPost, "/admin/policies", nil, body, false)
		},
	}
	mutationFields["deletePolicy"] = &graphql.Field{
		Type: nn(graphql.Boolean),
		Args: graphql.FieldConfigArgument{"id": requiredIDArg()},
		Resolve: func(rp graphql.ResolveParams) (interface{}, error) {
			id, err := pathID(rp.Args, "id")
			if err != nil {
				return nil, err
			}
			return trueOn(p.call(rp.Context, http.MethodDelete, "/admin/policies/"+id, nil, nil, false))
		},
	}
	mutationFields["seedDefaultPolicies"] = &graphql.Field{
		Type: nnList(policyType),
		Args: graphql.FieldConfigArgument{
			"entity":  strArg(),
			"roles":   &graphql.ArgumentConfig{Type: nnList(graphql.String)},
			"actions": &graphql.ArgumentConfig{Type: graphql.NewList(graphql.NewNonNull(graphql.String))},
		},
		Resolve: func(rp graphql.ResolveParams) (interface{}, error) {
			body := bodyFrom(rp.Args, "entity", "roles", "actions")
			return listOrEmpty(p.call(rp.Context, http.MethodPost, "/admin/policies/seed-defaults", nil, body, false))
		},
	}
	mutationFields["syncPolicyMatrix"] = &graphql.Field{
		Type: nnList(policyType),
		Args: graphql.FieldConfigArgument{
			"entity": strArg(),
			"grants": &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(matrixGrantInputType)))},
		},
		Resolve: func(rp graphql.ResolveParams) (interface{}, error) {
			raw, _ := rp.Args["grants"].([]interface{})
			grants := make([]map[string]interface{}, 0, len(raw))
			for _, g := range raw {
				grant := withoutNulls(g)
				if _, ok := grant["role"]; !ok {
					grant["role"] = nil // a null role is meaningful here (the "everyone" grant)
				}
				grants = append(grants, grant)
			}
			body := map[string]interface{}{"entity": argString(rp.Args, "entity"), "grants": grants}
			return listOrEmpty(p.call(rp.Context, http.MethodPut, "/admin/policies/matrix", nil, body, false))
		},
	}
	mutationFields["explainPermission"] = &graphql.Field{
		Type: nn(jsonScalar),
		Args: graphql.FieldConfigArgument{
			"entity": strArg(), "action": strArg(), "field": optStrArg(),
			"record": &graphql.ArgumentConfig{Type: jsonScalar},
		},
		Resolve: func(rp graphql.ResolveParams) (interface{}, error) {
			body := bodyFrom(rp.Args, "entity", "action", "field", "record")
			return p.call(rp.Context, http.MethodPost, "/admin/policies/explain", nil, body, false)
		},
	}

	// ---- cron jobs ----
	mutationFields["createCronJob"] = &graphql.Field{
		Type: nn(cronJobType),
		Args: graphql.FieldConfigArgument{"input": &graphql.ArgumentConfig{Type: graphql.NewNonNull(cronJobInputType)}},
		Resolve: func(rp graphql.ResolveParams) (interface{}, error) {
			return p.call(rp.Context, http.MethodPost, "/admin/cron-jobs", nil, withoutNulls(rp.Args["input"]), false)
		},
	}
	mutationFields["updateCronJob"] = &graphql.Field{
		Type: nn(cronJobType),
		Args: graphql.FieldConfigArgument{
			"id":    requiredIDArg(),
			"input": &graphql.ArgumentConfig{Type: graphql.NewNonNull(cronJobUpdateInputType)},
		},
		Resolve: func(rp graphql.ResolveParams) (interface{}, error) {
			id, err := pathID(rp.Args, "id")
			if err != nil {
				return nil, err
			}
			return p.call(rp.Context, http.MethodPatch, "/admin/cron-jobs/"+id, nil, withoutNulls(rp.Args["input"]), false)
		},
	}
	mutationFields["deleteCronJob"] = &graphql.Field{
		Type: nn(graphql.Boolean),
		Args: graphql.FieldConfigArgument{"id": requiredIDArg()},
		Resolve: func(rp graphql.ResolveParams) (interface{}, error) {
			id, err := pathID(rp.Args, "id")
			if err != nil {
				return nil, err
			}
			return trueOn(p.call(rp.Context, http.MethodDelete, "/admin/cron-jobs/"+id, nil, nil, false))
		},
	}

	// ---- OAuth clients ----
	mutationFields["createOAuthClient"] = &graphql.Field{
		Type: nn(oauthClientType),
		Args: graphql.FieldConfigArgument{
			"name":           strArg(),
			"redirectUris":   &graphql.ArgumentConfig{Type: nnList(graphql.String)},
			"allowedScopes":  &graphql.ArgumentConfig{Type: graphql.NewList(graphql.NewNonNull(graphql.String))},
			"isConfidential": &graphql.ArgumentConfig{Type: graphql.Boolean},
		},
		Resolve: func(rp graphql.ResolveParams) (interface{}, error) {
			body := bodyFrom(rp.Args, "name", "redirectUris", "allowedScopes", "isConfidential")
			return p.call(rp.Context, http.MethodPost, "/admin/oauth/clients", nil, body, false)
		},
	}
	mutationFields["revokeOAuthClient"] = &graphql.Field{
		Type: nn(graphql.Boolean),
		Args: graphql.FieldConfigArgument{"id": requiredIDArg()},
		Resolve: func(rp graphql.ResolveParams) (interface{}, error) {
			id, err := pathID(rp.Args, "id")
			if err != nil {
				return nil, err
			}
			return trueOn(p.call(rp.Context, http.MethodDelete, "/admin/oauth/clients/"+id, nil, nil, false))
		},
	}

	// ---- dashboards / preferences ----
	putLayout := func(path string) graphql.FieldResolveFn {
		return func(rp graphql.ResolveParams) (interface{}, error) {
			return p.call(rp.Context, http.MethodPut, path, nil, map[string]interface{}{"layout": rp.Args["layout"]}, false)
		}
	}
	layoutArgs := graphql.FieldConfigArgument{"layout": &graphql.ArgumentConfig{Type: graphql.NewNonNull(jsonScalar)}}
	mutationFields["setMyDashboard"] = &graphql.Field{Type: nn(dashboardConfigType), Args: layoutArgs, Resolve: putLayout("/dashboards/me")}
	mutationFields["setTenantDefaultDashboard"] = &graphql.Field{Type: nn(dashboardConfigType), Args: layoutArgs, Resolve: putLayout("/dashboards/tenant-default")}
	mutationFields["setMyPreferences"] = &graphql.Field{
		Type: nn(preferencesType),
		Args: graphql.FieldConfigArgument{"locale": strArg()},
		Resolve: func(rp graphql.ResolveParams) (interface{}, error) {
			return p.call(rp.Context, http.MethodPut, "/preferences", nil, bodyFrom(rp.Args, "locale"), false)
		},
	}

	// ---- config ----
	setConfig := func(prefix string) graphql.FieldResolveFn {
		return func(rp graphql.ResolveParams) (interface{}, error) {
			path := prefix + url.PathEscape(argString(rp.Args, "key"))
			return p.call(rp.Context, http.MethodPut, path, nil, map[string]interface{}{"value": rp.Args["value"]}, false)
		}
	}
	resetConfig := func(prefix string) graphql.FieldResolveFn {
		return func(rp graphql.ResolveParams) (interface{}, error) {
			return p.call(rp.Context, http.MethodDelete, prefix+url.PathEscape(argString(rp.Args, "key")), nil, nil, false)
		}
	}
	setArgs := graphql.FieldConfigArgument{"key": strArg(), "value": &graphql.ArgumentConfig{Type: graphql.NewNonNull(jsonScalar)}}
	keyArgs := graphql.FieldConfigArgument{"key": strArg()}
	mutationFields["setPlatformConfig"] = &graphql.Field{Type: nn(setConfigResultType), Args: setArgs, Resolve: setConfig("/platform/config/")}
	mutationFields["resetPlatformConfig"] = &graphql.Field{Type: nn(setConfigResultType), Args: keyArgs, Resolve: resetConfig("/platform/config/")}
	mutationFields["setTenantConfig"] = &graphql.Field{Type: nn(setTenantConfigResultType), Args: setArgs, Resolve: setConfig("/admin/config/")}
	mutationFields["resetTenantConfig"] = &graphql.Field{Type: nn(setTenantConfigResultType), Args: keyArgs, Resolve: resetConfig("/admin/config/")}
}
