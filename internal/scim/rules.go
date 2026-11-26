// Package scim is the pure half of SCIM 2.0 user provisioning (roadmap 115 M12, RFC 7643/7644): the
// resource a member is shown as, the bodies and patch operations an identity provider sends, the
// filter it asks with and the problem a failure is reported as. It reads and writes nothing: the HTTP
// skin over users.Service is internal/httpapi/scim.go, which authenticates, calls the service and turns
// what this package decides into responses. Unit-tested without a database or a server.
package scim

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/metap-org/metap/internal/admin/users"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/kit/errcode"
)

const (
	UserSchema  = "urn:ietf:params:scim:schemas:core:2.0:User"
	ListSchema  = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	ErrorSchema = "urn:ietf:params:scim:api:messages:2.0:Error"
	PatchSchema = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
	MediaType   = "application/scim+json"
	// MaxCount is the most a client may ask for in one page; ExactPage is the page size when walking the
	// matches of an exact-address filter.
	MaxCount  = 200
	ExactPage = 100
)

var filterPattern = regexp.MustCompile(`(?i)^\s*(?:userName|emails\.value)\s+eq\s+"([^"]*)"\s*$`)

// Problem is a failure as SCIM reports it: an HTTP status, a scimType (may be empty) and a detail.
type Problem struct {
	Status int
	Type   string
	Detail string
}

func (p *Problem) Error() string { return p.Detail }

func invalid(detail string) *Problem {
	return &Problem{Status: http.StatusBadRequest, Type: "invalidValue", Detail: detail}
}

// FromError is how a failed in-process call is reported to a SCIM client: a service error keeps its status
// and says what it said (a 5xx says nothing about the inside), a member that already exists is a
// uniqueness error, anything else is internal.
func FromError(err error) *Problem {
	var p *Problem
	if errors.As(err, &p) {
		return p
	}
	var se *crud.ServiceError
	if !errors.As(err, &se) {
		return &Problem{Status: http.StatusInternalServerError, Detail: "internal error"}
	}
	code := int(se.Status)
	typ := ""
	switch {
	case code == http.StatusBadRequest:
		typ = "invalidValue"
	case code == http.StatusConflict && se.Code == errcode.AlreadyMember:
		typ = "uniqueness"
	case code >= 500:
		return &Problem{Status: code, Detail: "internal error"}
	}
	return &Problem{Status: code, Type: typ, Detail: se.Message}
}

// Body is the SCIM JSON of a failure.
func (p *Problem) Body() map[string]any {
	body := map[string]any{"schemas": []string{ErrorSchema}, "status": strconv.Itoa(p.Status), "detail": p.Detail}
	if p.Type != "" {
		body["scimType"] = p.Type
	}
	return body
}

// Resource is a member as a SCIM User.
func Resource(u *users.User) map[string]any {
	return map[string]any{
		"schemas":     []string{UserSchema},
		"id":          u.GetId(),
		"userName":    u.GetEmail(),
		"displayName": u.GetDisplayName(),
		"name":        map[string]any{"formatted": u.GetDisplayName()},
		"emails":      []map[string]any{{"value": u.GetEmail(), "primary": true, "type": "work"}},
		"active":      u.GetStatus() != identity.StatusDisabled,
		"meta":        map[string]any{"resourceType": "User", "created": u.GetCreatedAt(), "location": "/scim/v2/Users/" + u.GetId()},
	}
}

// Bool accepts true/false and the strings "True"/"False" some providers send.
type Bool bool

func (b *Bool) UnmarshalJSON(raw []byte) error {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	switch x := v.(type) {
	case bool:
		*b = Bool(x)
	case string:
		parsed, err := strconv.ParseBool(strings.ToLower(x))
		if err != nil {
			return err
		}
		*b = Bool(parsed)
	default:
		return &json.UnmarshalTypeError{Value: string(raw)}
	}
	return nil
}

type Name struct {
	Formatted  string `json:"formatted"`
	GivenName  string `json:"givenName"`
	FamilyName string `json:"familyName"`
}

// UserBody is what a provider sends to create or replace a user.
type UserBody struct {
	UserName    string `json:"userName"`
	DisplayName string `json:"displayName"`
	Name        *Name  `json:"name"`
	Emails      []struct {
		Value   string `json:"value"`
		Primary bool   `json:"primary"`
	} `json:"emails"`
	Active *Bool `json:"active"`
}

// Email is the address: userName, else the primary email (or the only one).
func (b UserBody) Email() string {
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

// Display is the display name: displayName, else name.formatted, else given and family name.
func (b UserBody) Display() string {
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

// Change is what a body asks to change on an existing member: the two attributes a member has that SCIM can
// change (nil: leave as is).
type Change struct {
	Display *string
	Active  *bool
}

// ChangeOf is what a replace asks for. Pure.
func (b UserBody) ChangeOf() Change {
	var c Change
	if d := b.Display(); d != "" {
		c.Display = &d
	}
	if b.Active != nil {
		a := bool(*b.Active)
		c.Active = &a
	}
	return c
}

// Page is the page a list asks for.
type Page struct {
	Limit  int32 // how many to read (at least 1: count=0 asks for the total only)
	Start  int32 // SCIM's 1-based startIndex
	Offset int32 // Start - 1
}

// PageOf reads count and startIndex (empty: default). Pure.
func PageOf(count, startIndex string) (Page, *Problem) {
	p := Page{Limit: 50, Start: 1}
	if count != "" {
		n, err := strconv.ParseInt(count, 10, 32)
		if err != nil || n < 0 {
			return p, invalid("`count` must be a non-negative integer.")
		}
		p.Limit = int32(min(n, MaxCount))
	}
	if startIndex != "" {
		n, err := strconv.ParseInt(startIndex, 10, 32)
		if err != nil {
			return p, invalid("`startIndex` must be an integer.")
		}
		p.Start = max(int32(n), 1)
	}
	p.Offset = p.Start - 1
	if p.Limit == 0 {
		p.Limit = 1
	}
	return p, nil
}

// ExactFilter is the address of a `userName eq "..."` filter (ok false: the filter is another shape). An
// empty filter is not one. Pure.
func ExactFilter(filter string) (address string, ok bool) {
	m := filterPattern.FindStringSubmatch(filter)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// FilterProblem is the 400 for a filter of any other shape.
func FilterProblem() *Problem {
	return &Problem{Status: http.StatusBadRequest, Type: "invalidFilter",
		Detail: `Only filters of the form userName eq "value" are supported.`}
}

// PatchOp is one operation of a PATCH.
type PatchOp struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

// Patch is what a PATCH asks for: the change, and the userName values it asserts (a user's name is not
// changeable, so each must equal the current one: the caller checks).
type Patch struct {
	Change
	UserNames []string
	// BadUserName is set when a userName value was not a string: it cannot equal anything.
	BadUserName bool
}

// ParsePatch reads the operations: only add and replace, with or without a path. Attributes this server
// does not store (externalId, emails, ...) are accepted and ignored, so a provider that always sends them
// does not fail every sync. Pure.
func ParsePatch(ops []PatchOp) (Patch, *Problem) {
	var p Patch
	if len(ops) == 0 {
		return p, invalid("`Operations` is required.")
	}
	set := func(path string, raw json.RawMessage) *Problem {
		switch strings.ToLower(path) {
		case "active":
			var b Bool
			if err := json.Unmarshal(raw, &b); err != nil {
				return invalid("`active` must be a boolean.")
			}
			a := bool(b)
			p.Active = &a
		case "displayname", "name.formatted":
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				return invalid("`" + path + "` must be a string.")
			}
			p.Display = &s
		case "username":
			var s string
			if json.Unmarshal(raw, &s) != nil {
				p.BadUserName = true
			} else {
				p.UserNames = append(p.UserNames, s)
			}
		}
		return nil
	}
	for _, op := range ops {
		switch strings.ToLower(op.Op) {
		case "replace", "add":
		default:
			return p, invalid("Only `add` and `replace` operations are supported.")
		}
		if op.Path != "" {
			if prob := set(op.Path, op.Value); prob != nil {
				return p, prob
			}
			continue
		}
		// No path: the value is an object of attributes.
		var attrs map[string]json.RawMessage
		if err := json.Unmarshal(op.Value, &attrs); err != nil {
			return p, invalid("An operation without `path` needs an object value.")
		}
		for k, v := range attrs {
			if prob := set(k, v); prob != nil {
				return p, prob
			}
		}
	}
	return p, nil
}

// NameChanged is the 400 for an attempt to change the userName. Pure.
func NameChanged() *Problem {
	return &Problem{Status: http.StatusBadRequest, Type: "mutability", Detail: "`userName` cannot be changed."}
}

// SameName is true when every userName the patch asserts is the member's current address.
func (p Patch) SameName(current string) bool {
	if p.BadUserName {
		return false
	}
	for _, n := range p.UserNames {
		if !strings.EqualFold(n, current) {
			return false
		}
	}
	return true
}
