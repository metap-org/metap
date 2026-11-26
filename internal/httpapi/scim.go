// SCIM 2.0 user provisioning (roadmap 115 M12, RFC 7643/7644): an identity provider (Entra ID,
// Okta, ...) keeps a tenant's members in step with its own directory — create on join, deactivate
// on leave, update the display name. It is a thin REST skin over users.Service: every call runs
// that service in-process with the caller's own bearer token, so authentication (an API key of an
// admin service account, or an admin token), the admin check, the last-admin guards, password and
// invitation handling and the audit trail are exactly those of the admin API, with no second copy
// of the rules to drift.
//
// Supported: Users (list with `userName eq "..."` filter, get, create, replace, patch, delete),
// ServiceProviderConfig, ResourceTypes, Schemas. A created user is invited by email (SCIM carries
// no password a tenant could accept). Not supported: Groups, roles, bulk, sort, etags, other
// filters.
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/metap-org/metap/internal/admin"
	"github.com/metap-org/metap/internal/admin/users"
	"github.com/metap-org/metap/internal/scim"
	"github.com/metap-org/metap/pkg/kit/httpx"
	"github.com/metap-org/metap/pkg/kit/standard"
	"github.com/metap-org/metap/pkg/permission"
)

// SCIMHandler serves the SCIM API; mount it under /scim/v2 with the prefix stripped.
// SCIMAuthenticator authenticates a SCIM request's bearer token; a failure is a *crud.ServiceError.
type SCIMAuthenticator func(r *http.Request) (*permission.RequestContext, error)

func SCIMHandler(svc *users.Service, auth SCIMAuthenticator) http.Handler {
	h := &scimHandler{users: svc, auth: auth}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ServiceProviderConfig", h.serviceProviderConfig)
	mux.HandleFunc("GET /ResourceTypes", h.resourceTypes)
	mux.HandleFunc("GET /Schemas", h.schemas)
	mux.HandleFunc("GET /Users", h.list)
	mux.HandleFunc("POST /Users", h.create)
	mux.HandleFunc("GET /Users/{id}", h.get)
	mux.HandleFunc("PUT /Users/{id}", h.replace)
	mux.HandleFunc("PATCH /Users/{id}", h.patch)
	mux.HandleFunc("DELETE /Users/{id}", h.remove)
	return mux
}

// scimHandler is the HTTP skin: it authenticates, calls users.Service as the caller and writes what
// internal/scim decided. The rules (resources, bodies, patches, filters, problems) are in that package.
type scimHandler struct {
	users *users.Service
	auth  SCIMAuthenticator
}

func scimWrite(w http.ResponseWriter, code int, v any) {
	w.Header().Set(standard.HeaderContentType, scim.MediaType)
	w.WriteHeader(code)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func scimProblem(w http.ResponseWriter, p *scim.Problem) { scimWrite(w, p.Status, p.Body()) }

func scimFail(w http.ResponseWriter, code int, scimType, detail string) {
	scimProblem(w, &scim.Problem{Status: code, Type: scimType, Detail: detail})
}

// scimFromErr reports a failed in-process call the way SCIM clients expect.
func scimFromErr(w http.ResponseWriter, err error) { scimProblem(w, scim.FromError(err)) }

// ctx authenticates the caller's bearer token (an API key of an admin service account) the way the rest
// of the HTTP API does, and carries it as the caller users.Service acts as.
func (h *scimHandler) ctx(w http.ResponseWriter, r *http.Request) (context.Context, bool) {
	if _, ok := httpx.BearerToken(r.Header.Get(standard.HeaderAuthorization)); !ok {
		w.Header().Set("WWW-Authenticate", "Bearer")
		scimFail(w, http.StatusUnauthorized, "", "A bearer token (an API key) is required.")
		return nil, false
	}
	rc, err := h.auth(r)
	if err != nil {
		scimFromErr(w, err)
		return nil, false
	}
	return admin.WithCaller(r.Context(), rc), true
}

func (h *scimHandler) serviceProviderConfig(w http.ResponseWriter, _ *http.Request) {
	scimWrite(w, http.StatusOK, map[string]any{
		"schemas":        []string{"urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"},
		"patch":          map[string]any{"supported": true},
		"bulk":           map[string]any{"supported": false, "maxOperations": 0, "maxPayloadSize": 0},
		"filter":         map[string]any{"supported": true, "maxResults": scim.MaxCount},
		"changePassword": map[string]any{"supported": false},
		"sort":           map[string]any{"supported": false},
		"etag":           map[string]any{"supported": false},
		"authenticationSchemes": []map[string]any{{"type": "oauthbearertoken", "name": "API key",
			"description": "An API key (mk_...) of an admin service account, sent as a bearer token.", "primary": true}},
	})
}

func (h *scimHandler) resourceTypes(w http.ResponseWriter, _ *http.Request) {
	scimWrite(w, http.StatusOK, map[string]any{"schemas": []string{scim.ListSchema}, "totalResults": 1, "startIndex": 1,
		"itemsPerPage": 1, "Resources": []map[string]any{{"id": "User", "name": "User", "endpoint": "/Users", "schema": scim.UserSchema}}})
}

func (h *scimHandler) schemas(w http.ResponseWriter, _ *http.Request) {
	scimWrite(w, http.StatusOK, map[string]any{"schemas": []string{scim.ListSchema}, "totalResults": 1, "startIndex": 1,
		"itemsPerPage": 1, "Resources": []map[string]any{{"id": scim.UserSchema, "name": "User",
			"description": "A member of the tenant (userName is the email address)."}}})
}

func (h *scimHandler) list(w http.ResponseWriter, r *http.Request) {
	ctx, ok := h.ctx(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	page, prob := scim.PageOf(q.Get("count"), q.Get("startIndex"))
	if prob != nil {
		scimProblem(w, prob)
		return
	}
	req := &users.ListUsersRequest{Limit: page.Limit, Offset: page.Offset}
	exact := ""
	if f := q.Get("filter"); f != "" {
		address, ok := scim.ExactFilter(f)
		if !ok {
			scimProblem(w, scim.FilterProblem())
			return
		}
		exact, req.Search, req.Offset = address, address, 0
	}
	resources := []map[string]any{}
	total := 0
	if exact != "" {
		// The search is a substring match, so the exact address can sit beyond the first page when
		// many addresses contain it; an identity provider that does not find its user creates it again.
		// Walk the matches until the address is found or they run out.
		req.Limit = scim.ExactPage
		for {
			res, err := h.users.List(ctx, req)
			if err != nil {
				scimFromErr(w, err)
				return
			}
			for _, u := range res.GetUsers() {
				if strings.EqualFold(u.GetEmail(), exact) {
					resources = append(resources, scim.Resource(u))
				}
			}
			req.Offset += int32(len(res.GetUsers()))
			if len(resources) > 0 || len(res.GetUsers()) == 0 || req.Offset >= res.GetTotal() {
				break
			}
		}
		total = len(resources)
	} else {
		res, err := h.users.List(ctx, req)
		if err != nil {
			scimFromErr(w, err)
			return
		}
		for _, u := range res.GetUsers() {
			resources = append(resources, scim.Resource(u))
		}
		total = int(res.GetTotal())
	}
	scimWrite(w, http.StatusOK, map[string]any{"schemas": []string{scim.ListSchema}, "totalResults": total,
		"startIndex": page.Start, "itemsPerPage": len(resources), "Resources": resources})
}

func (h *scimHandler) get(w http.ResponseWriter, r *http.Request) {
	ctx, ok := h.ctx(w, r)
	if !ok {
		return
	}
	u, err := h.users.Get(ctx, &users.UserIdRequest{Id: r.PathValue("id")})
	if err != nil {
		scimFromErr(w, err)
		return
	}
	scimWrite(w, http.StatusOK, scim.Resource(u))
}

func (h *scimHandler) decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, httpx.DefaultMaxBodyBytes)).Decode(dst); err != nil {
		scimFail(w, http.StatusBadRequest, "invalidSyntax", "The body is not valid SCIM JSON.")
		return false
	}
	return true
}

func (h *scimHandler) create(w http.ResponseWriter, r *http.Request) {
	ctx, ok := h.ctx(w, r)
	if !ok {
		return
	}
	var body scim.UserBody
	if !h.decode(w, r, &body) {
		return
	}
	email := body.Email()
	if email == "" {
		scimFail(w, http.StatusBadRequest, "invalidValue", "`userName` is required.")
		return
	}
	res, err := h.users.Invite(ctx, &users.InviteUserRequest{Email: email, DisplayName: body.Display()})
	if err != nil {
		scimFromErr(w, err)
		return
	}
	u := res.GetUser()
	if body.Active != nil && !bool(*body.Active) {
		if u, err = h.users.Disable(ctx, &users.UserIdRequest{Id: u.GetId()}); err != nil {
			scimFromErr(w, err)
			return
		}
	}
	w.Header().Set("Location", "/scim/v2/Users/"+u.GetId())
	scimWrite(w, http.StatusCreated, scim.Resource(u))
}

// apply sets the display name and the active flag, the two attributes a member has that SCIM can change.
func (h *scimHandler) apply(ctx context.Context, id string, c scim.Change) (*users.User, error) {
	var u *users.User
	var err error
	if c.Display != nil {
		if u, err = h.users.Update(ctx, &users.UpdateUserRequest{Id: id, DisplayName: *c.Display}); err != nil {
			return nil, err
		}
	}
	if c.Active != nil {
		if *c.Active {
			u, err = h.users.Enable(ctx, &users.UserIdRequest{Id: id})
		} else {
			u, err = h.users.Disable(ctx, &users.UserIdRequest{Id: id})
		}
		if err != nil {
			return nil, err
		}
	}
	if u == nil {
		return h.users.Get(ctx, &users.UserIdRequest{Id: id})
	}
	return u, nil
}

func (h *scimHandler) replace(w http.ResponseWriter, r *http.Request) {
	ctx, ok := h.ctx(w, r)
	if !ok {
		return
	}
	var body scim.UserBody
	if !h.decode(w, r, &body) {
		return
	}
	id := r.PathValue("id")
	cur, err := h.users.Get(ctx, &users.UserIdRequest{Id: id})
	if err != nil {
		scimFromErr(w, err)
		return
	}
	if email := body.Email(); email != "" && !strings.EqualFold(email, cur.GetEmail()) {
		scimProblem(w, scim.NameChanged())
		return
	}
	u, err := h.apply(ctx, id, body.ChangeOf())
	if err != nil {
		scimFromErr(w, err)
		return
	}
	scimWrite(w, http.StatusOK, scim.Resource(u))
}

func (h *scimHandler) patch(w http.ResponseWriter, r *http.Request) {
	ctx, ok := h.ctx(w, r)
	if !ok {
		return
	}
	var body struct {
		Schemas    []string       `json:"schemas"`
		Operations []scim.PatchOp `json:"Operations"`
	}
	if !h.decode(w, r, &body) {
		return
	}
	patch, prob := scim.ParsePatch(body.Operations)
	if prob != nil {
		scimProblem(w, prob)
		return
	}
	id := r.PathValue("id")
	if len(patch.UserNames) > 0 || patch.BadUserName {
		cur, err := h.users.Get(ctx, &users.UserIdRequest{Id: id})
		if err != nil {
			scimFromErr(w, err)
			return
		}
		if !patch.SameName(cur.GetEmail()) {
			scimProblem(w, scim.NameChanged())
			return
		}
	}
	u, err := h.apply(ctx, id, patch.Change)
	if err != nil {
		scimFromErr(w, err)
		return
	}
	scimWrite(w, http.StatusOK, scim.Resource(u))
}

func (h *scimHandler) remove(w http.ResponseWriter, r *http.Request) {
	ctx, ok := h.ctx(w, r)
	if !ok {
		return
	}
	if _, err := h.users.Remove(ctx, &users.UserIdRequest{Id: r.PathValue("id")}); err != nil {
		scimFromErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
