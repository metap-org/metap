// SCIM 2.0 user provisioning (roadmap 115 M12, RFC 7643/7644): an identity provider (Entra ID,
// Okta, ...) keeps a tenant's members in step with its own directory — create on join, deactivate
// on leave, update the display name. It is a thin REST skin over UserAdminService: every call runs
// that service in-process with the caller's own bearer token, so authentication (an API key of an
// admin service account, or an admin token), the admin check, the last-admin guards, password and
// invitation handling and the audit trail are exactly those of the admin API, with no second copy
// of the rules to drift.
//
// Supported: Users (list with `userName eq "..."` filter, get, create, replace, patch, delete),
// ServiceProviderConfig, ResourceTypes, Schemas. A created user is invited by email (SCIM carries
// no password a tenant could accept). Not supported: Groups, roles, bulk, sort, etags, other
// filters.
package grpcapi

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"google.golang.org/grpc/codes"
	grpcmd "google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/metap-org/metap/pkg/grpcapi/pb"
	"github.com/metap-org/metap/pkg/identity"
	"github.com/metap-org/metap/pkg/kit/httpx"
	"github.com/metap-org/metap/pkg/kit/standard"
)

const (
	scimUserSchema  = "urn:ietf:params:scim:schemas:core:2.0:User"
	scimListSchema  = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	scimErrorSchema = "urn:ietf:params:scim:api:messages:2.0:Error"
	scimPatchSchema = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
	scimMediaType   = "application/scim+json"
	scimMaxCount    = 200
)

var scimFilter = regexp.MustCompile(`(?i)^\s*(?:userName|emails\.value)\s+eq\s+"([^"]*)"\s*$`)

// SCIMHandler serves the SCIM API; mount it under /scim/v2 with the prefix stripped.
func SCIMHandler(users *UserAdminService) http.Handler {
	h := &scim{users: users}
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

type scim struct{ users *UserAdminService }

func scimWrite(w http.ResponseWriter, code int, v any) {
	w.Header().Set(standard.HeaderContentType, scimMediaType)
	w.WriteHeader(code)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func scimFail(w http.ResponseWriter, code int, scimType, detail string) {
	body := map[string]any{"schemas": []string{scimErrorSchema}, "status": strconv.Itoa(code), "detail": detail}
	if scimType != "" {
		body["scimType"] = scimType
	}
	scimWrite(w, code, body)
}

// grpcToSCIM reports a failed in-process call the way SCIM clients expect.
func scimFromGRPC(w http.ResponseWriter, err error) {
	st, _ := status.FromError(err)
	code := http.StatusInternalServerError
	scimType := ""
	switch st.Code() {
	case codes.Unauthenticated:
		code = http.StatusUnauthorized
	case codes.PermissionDenied:
		code = http.StatusForbidden
	case codes.NotFound:
		code = http.StatusNotFound
	case codes.InvalidArgument:
		code, scimType = http.StatusBadRequest, "invalidValue"
	case codes.AlreadyExists:
		code, scimType = http.StatusConflict, "uniqueness"
	case codes.FailedPrecondition, codes.Aborted:
		code = http.StatusConflict
	}
	detail := st.Message()
	if code == http.StatusInternalServerError {
		detail = "internal error"
	}
	scimFail(w, code, scimType, detail)
}

// ctx carries the caller's bearer token as incoming gRPC metadata, which is where
// UserAdminService authenticates from.
func (h *scim) ctx(w http.ResponseWriter, r *http.Request) (context.Context, bool) {
	token, ok := httpx.BearerToken(r.Header.Get(standard.HeaderAuthorization))
	if !ok {
		w.Header().Set("WWW-Authenticate", "Bearer")
		scimFail(w, http.StatusUnauthorized, "", "A bearer token (an API key) is required.")
		return nil, false
	}
	md := grpcmd.Pairs("authorization", standard.BearerPrefix+token)
	return grpcmd.NewIncomingContext(r.Context(), md), true
}

func scimUser(u *pb.User) map[string]any {
	out := map[string]any{
		"schemas":     []string{scimUserSchema},
		"id":          u.GetId(),
		"userName":    u.GetEmail(),
		"displayName": u.GetDisplayName(),
		"name":        map[string]any{"formatted": u.GetDisplayName()},
		"emails":      []map[string]any{{"value": u.GetEmail(), "primary": true, "type": "work"}},
		"active":      u.GetStatus() != identity.StatusDisabled,
		"meta":        map[string]any{"resourceType": "User", "created": u.GetCreatedAt(), "location": "/scim/v2/Users/" + u.GetId()},
	}
	return out
}

type scimName struct {
	Formatted  string `json:"formatted"`
	GivenName  string `json:"givenName"`
	FamilyName string `json:"familyName"`
}

type scimUserBody struct {
	UserName    string    `json:"userName"`
	DisplayName string    `json:"displayName"`
	Name        *scimName `json:"name"`
	Emails      []struct {
		Value   string `json:"value"`
		Primary bool   `json:"primary"`
	} `json:"emails"`
	Active *scimBool `json:"active"`
}

// scimBool accepts true/false and the strings "True"/"False" some providers send.
type scimBool bool

func (b *scimBool) UnmarshalJSON(raw []byte) error {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	switch x := v.(type) {
	case bool:
		*b = scimBool(x)
	case string:
		parsed, err := strconv.ParseBool(strings.ToLower(x))
		if err != nil {
			return err
		}
		*b = scimBool(parsed)
	default:
		return &json.UnmarshalTypeError{Value: string(raw)}
	}
	return nil
}

func (b scimUserBody) email() string {
	if b.UserName != "" {
		return strings.TrimSpace(b.UserName)
	}
	for _, e := range b.Emails {
		if e.Primary || len(b.Emails) == 1 {
			return strings.TrimSpace(e.Value)
		}
	}
	return ""
}

func (b scimUserBody) display() string {
	switch {
	case b.DisplayName != "":
		return b.DisplayName
	case b.Name != nil && b.Name.Formatted != "":
		return b.Name.Formatted
	case b.Name != nil:
		return strings.TrimSpace(b.Name.GivenName + " " + b.Name.FamilyName)
	}
	return ""
}

func (h *scim) serviceProviderConfig(w http.ResponseWriter, _ *http.Request) {
	scimWrite(w, http.StatusOK, map[string]any{
		"schemas":        []string{"urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"},
		"patch":          map[string]any{"supported": true},
		"bulk":           map[string]any{"supported": false, "maxOperations": 0, "maxPayloadSize": 0},
		"filter":         map[string]any{"supported": true, "maxResults": scimMaxCount},
		"changePassword": map[string]any{"supported": false},
		"sort":           map[string]any{"supported": false},
		"etag":           map[string]any{"supported": false},
		"authenticationSchemes": []map[string]any{{"type": "oauthbearertoken", "name": "API key",
			"description": "An API key (mk_...) of an admin service account, sent as a bearer token.", "primary": true}},
	})
}

func (h *scim) resourceTypes(w http.ResponseWriter, _ *http.Request) {
	scimWrite(w, http.StatusOK, map[string]any{"schemas": []string{scimListSchema}, "totalResults": 1, "startIndex": 1,
		"itemsPerPage": 1, "Resources": []map[string]any{{"id": "User", "name": "User", "endpoint": "/Users", "schema": scimUserSchema}}})
}

func (h *scim) schemas(w http.ResponseWriter, _ *http.Request) {
	scimWrite(w, http.StatusOK, map[string]any{"schemas": []string{scimListSchema}, "totalResults": 1, "startIndex": 1,
		"itemsPerPage": 1, "Resources": []map[string]any{{"id": scimUserSchema, "name": "User",
			"description": "A member of the tenant (userName is the email address)."}}})
}

func (h *scim) list(w http.ResponseWriter, r *http.Request) {
	ctx, ok := h.ctx(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	req := &pb.ListUsersRequest{Limit: 50}
	if raw := q.Get("count"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			scimFail(w, http.StatusBadRequest, "invalidValue", "`count` must be a non-negative integer.")
			return
		}
		req.Limit = int32(min(n, scimMaxCount))
	}
	start := 1
	if raw := q.Get("startIndex"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			scimFail(w, http.StatusBadRequest, "invalidValue", "`startIndex` must be an integer.")
			return
		}
		start = max(n, 1)
	}
	req.Offset = int32(start - 1)
	exact := ""
	if f := q.Get("filter"); f != "" {
		m := scimFilter.FindStringSubmatch(f)
		if m == nil {
			scimFail(w, http.StatusBadRequest, "invalidFilter", `Only filters of the form userName eq "value" are supported.`)
			return
		}
		exact, req.Search, req.Offset = m[1], m[1], 0
	}
	if req.Limit == 0 {
		// count=0 asks for the total only.
		req.Limit = 1
	}
	res, err := h.users.ListUsers(ctx, req)
	if err != nil {
		scimFromGRPC(w, err)
		return
	}
	resources := []map[string]any{}
	for _, u := range res.GetUsers() {
		if exact != "" && !strings.EqualFold(u.GetEmail(), exact) {
			continue
		}
		resources = append(resources, scimUser(u))
	}
	total := int(res.GetTotal())
	if exact != "" {
		total = len(resources)
	}
	scimWrite(w, http.StatusOK, map[string]any{"schemas": []string{scimListSchema}, "totalResults": total,
		"startIndex": start, "itemsPerPage": len(resources), "Resources": resources})
}

func (h *scim) get(w http.ResponseWriter, r *http.Request) {
	ctx, ok := h.ctx(w, r)
	if !ok {
		return
	}
	u, err := h.users.GetUser(ctx, &pb.UserIdRequest{Id: r.PathValue("id")})
	if err != nil {
		scimFromGRPC(w, err)
		return
	}
	scimWrite(w, http.StatusOK, scimUser(u))
}

func (h *scim) decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, httpx.DefaultMaxBodyBytes)).Decode(dst); err != nil {
		scimFail(w, http.StatusBadRequest, "invalidSyntax", "The body is not valid SCIM JSON.")
		return false
	}
	return true
}

func (h *scim) create(w http.ResponseWriter, r *http.Request) {
	ctx, ok := h.ctx(w, r)
	if !ok {
		return
	}
	var body scimUserBody
	if !h.decode(w, r, &body) {
		return
	}
	email := body.email()
	if email == "" {
		scimFail(w, http.StatusBadRequest, "invalidValue", "`userName` is required.")
		return
	}
	res, err := h.users.InviteUser(ctx, &pb.InviteUserRequest{Email: email, DisplayName: body.display()})
	if err != nil {
		scimFromGRPC(w, err)
		return
	}
	u := res.GetUser()
	if body.Active != nil && !bool(*body.Active) {
		if u, err = h.users.DisableUser(ctx, &pb.UserIdRequest{Id: u.GetId()}); err != nil {
			scimFromGRPC(w, err)
			return
		}
	}
	w.Header().Set("Location", "/scim/v2/Users/"+u.GetId())
	scimWrite(w, http.StatusCreated, scimUser(u))
}

// apply sets the display name and the active flag, the two attributes a member has that SCIM can
// change.
func (h *scim) apply(ctx context.Context, id string, display *string, active *bool) (*pb.User, error) {
	var u *pb.User
	var err error
	if display != nil {
		if u, err = h.users.UpdateUser(ctx, &pb.UpdateUserRequest{Id: id, DisplayName: *display}); err != nil {
			return nil, err
		}
	}
	if active != nil {
		if *active {
			u, err = h.users.EnableUser(ctx, &pb.UserIdRequest{Id: id})
		} else {
			u, err = h.users.DisableUser(ctx, &pb.UserIdRequest{Id: id})
		}
		if err != nil {
			return nil, err
		}
	}
	if u == nil {
		return h.users.GetUser(ctx, &pb.UserIdRequest{Id: id})
	}
	return u, nil
}

func (h *scim) replace(w http.ResponseWriter, r *http.Request) {
	ctx, ok := h.ctx(w, r)
	if !ok {
		return
	}
	var body scimUserBody
	if !h.decode(w, r, &body) {
		return
	}
	id := r.PathValue("id")
	cur, err := h.users.GetUser(ctx, &pb.UserIdRequest{Id: id})
	if err != nil {
		scimFromGRPC(w, err)
		return
	}
	if email := body.email(); email != "" && !strings.EqualFold(email, cur.GetEmail()) {
		scimFail(w, http.StatusBadRequest, "mutability", "`userName` cannot be changed.")
		return
	}
	var display *string
	if d := body.display(); d != "" {
		display = &d
	}
	var active *bool
	if body.Active != nil {
		a := bool(*body.Active)
		active = &a
	}
	u, err := h.apply(ctx, id, display, active)
	if err != nil {
		scimFromGRPC(w, err)
		return
	}
	scimWrite(w, http.StatusOK, scimUser(u))
}

type scimPatchOp struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

func (h *scim) patch(w http.ResponseWriter, r *http.Request) {
	ctx, ok := h.ctx(w, r)
	if !ok {
		return
	}
	var body struct {
		Schemas    []string      `json:"schemas"`
		Operations []scimPatchOp `json:"Operations"`
	}
	if !h.decode(w, r, &body) {
		return
	}
	if len(body.Operations) == 0 {
		scimFail(w, http.StatusBadRequest, "invalidValue", "`Operations` is required.")
		return
	}
	id := r.PathValue("id")
	var display *string
	var active *bool
	set := func(path string, raw json.RawMessage) bool {
		switch strings.ToLower(path) {
		case "active":
			var b scimBool
			if err := json.Unmarshal(raw, &b); err != nil {
				scimFail(w, http.StatusBadRequest, "invalidValue", "`active` must be a boolean.")
				return false
			}
			a := bool(b)
			active = &a
		case "displayname", "name.formatted":
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				scimFail(w, http.StatusBadRequest, "invalidValue", "`"+path+"` must be a string.")
				return false
			}
			display = &s
		case "username":
			cur, err := h.users.GetUser(ctx, &pb.UserIdRequest{Id: id})
			var s string
			if err != nil {
				scimFromGRPC(w, err)
				return false
			}
			if json.Unmarshal(raw, &s) != nil || !strings.EqualFold(s, cur.GetEmail()) {
				scimFail(w, http.StatusBadRequest, "mutability", "`userName` cannot be changed.")
				return false
			}
		default:
			// Attributes this server does not store (externalId, emails, ...) are accepted and ignored,
			// so a provider that always sends them does not fail every sync.
		}
		return true
	}
	for _, op := range body.Operations {
		switch strings.ToLower(op.Op) {
		case "replace", "add":
		default:
			scimFail(w, http.StatusBadRequest, "invalidValue", "Only `add` and `replace` operations are supported.")
			return
		}
		if op.Path != "" {
			if !set(op.Path, op.Value) {
				return
			}
			continue
		}
		// No path: the value is an object of attributes.
		var attrs map[string]json.RawMessage
		if err := json.Unmarshal(op.Value, &attrs); err != nil {
			scimFail(w, http.StatusBadRequest, "invalidValue", "An operation without `path` needs an object value.")
			return
		}
		for k, v := range attrs {
			if !set(k, v) {
				return
			}
		}
	}
	u, err := h.apply(ctx, id, display, active)
	if err != nil {
		scimFromGRPC(w, err)
		return
	}
	scimWrite(w, http.StatusOK, scimUser(u))
}

func (h *scim) remove(w http.ResponseWriter, r *http.Request) {
	ctx, ok := h.ctx(w, r)
	if !ok {
		return
	}
	if _, err := h.users.RemoveUser(ctx, &pb.UserIdRequest{Id: r.PathValue("id")}); err != nil {
		scimFromGRPC(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
