package scim

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/internal/admin/users"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/kit/errcode"
)

func TestResource(t *testing.T) {
	r := Resource(&users.User{Id: "u1", Email: "a@acme.io", DisplayName: "Ann", Status: identity.StatusActive, CreatedAt: "2026-01-01T00:00:00Z"})
	assert.Equal(t, "a@acme.io", r["userName"])
	assert.Equal(t, true, r["active"])
	assert.Equal(t, "/scim/v2/Users/u1", r["meta"].(map[string]any)["location"])
	assert.Equal(t, false, Resource(&users.User{Status: identity.StatusDisabled})["active"], "disabled is inactive; invited and active are active")
	assert.Equal(t, true, Resource(&users.User{Status: identity.StatusInvited})["active"])
}

func TestBoolAcceptsTheStringsProvidersSend(t *testing.T) {
	for in, want := range map[string]bool{`true`: true, `false`: false, `"True"`: true, `"False"`: false, `"true"`: true} {
		var b Bool
		require.NoError(t, json.Unmarshal([]byte(in), &b), in)
		assert.Equal(t, want, bool(b), in)
	}
	var b Bool
	assert.Error(t, json.Unmarshal([]byte(`"maybe"`), &b))
	assert.Error(t, json.Unmarshal([]byte(`3`), &b))
}

func TestBodyEmailAndDisplay(t *testing.T) {
	var b UserBody
	require.NoError(t, json.Unmarshal([]byte(`{"userName":" a@acme.io ","displayName":"Ann"}`), &b))
	assert.Equal(t, "a@acme.io", b.Email())
	assert.Equal(t, "Ann", b.Display())

	b = UserBody{}
	require.NoError(t, json.Unmarshal([]byte(`{"emails":[{"value":"x@acme.io"},{"value":"y@acme.io","primary":true}],"name":{"givenName":"Ann","familyName":"Lee"}}`), &b))
	assert.Equal(t, "y@acme.io", b.Email(), "the primary one")
	assert.Equal(t, "Ann Lee", b.Display(), "given and family name")

	b = UserBody{}
	require.NoError(t, json.Unmarshal([]byte(`{"emails":[{"value":"only@acme.io"}],"name":{"formatted":"A. Lee","givenName":"Ann"}}`), &b))
	assert.Equal(t, "only@acme.io", b.Email(), "the only one, primary or not")
	assert.Equal(t, "A. Lee", b.Display(), "formatted wins over given/family")

	assert.Empty(t, UserBody{}.Email())
	assert.Empty(t, UserBody{}.Display())
}

func TestChangeOf(t *testing.T) {
	assert.Equal(t, Change{}, UserBody{}.ChangeOf())
	off := Bool(false)
	c := UserBody{DisplayName: "Ann", Active: &off}.ChangeOf()
	require.NotNil(t, c.Display)
	assert.Equal(t, "Ann", *c.Display)
	require.NotNil(t, c.Active)
	assert.False(t, *c.Active)
}

func TestPageOf(t *testing.T) {
	p, prob := PageOf("", "")
	require.Nil(t, prob)
	assert.Equal(t, Page{Limit: 50, Start: 1, Offset: 0}, p)

	p, _ = PageOf("1000", "11")
	assert.EqualValues(t, MaxCount, p.Limit, "a page is capped")
	assert.EqualValues(t, 10, p.Offset)

	p, _ = PageOf("0", "")
	assert.EqualValues(t, 1, p.Limit, "count=0 asks for the total: read one")

	p, _ = PageOf("", "0")
	assert.EqualValues(t, 1, p.Start, "startIndex is 1-based")
	p, _ = PageOf("", "-5")
	assert.EqualValues(t, 1, p.Start)

	for _, c := range [][2]string{{"-1", ""}, {"x", ""}, {"", "x"}} {
		_, prob := PageOf(c[0], c[1])
		require.NotNil(t, prob, c)
		assert.Equal(t, http.StatusBadRequest, prob.Status)
		assert.Equal(t, "invalidValue", prob.Type)
	}
}

func TestExactFilter(t *testing.T) {
	for _, f := range []string{`userName eq "a@acme.io"`, `USERNAME EQ "a@acme.io"`, ` emails.value eq "a@acme.io" `} {
		addr, ok := ExactFilter(f)
		assert.True(t, ok, f)
		assert.Equal(t, "a@acme.io", addr, f)
	}
	for _, f := range []string{``, `displayName co "x"`, `userName eq a@acme.io`, `userName eq "a" and active eq true`} {
		_, ok := ExactFilter(f)
		assert.False(t, ok, f)
	}
	assert.Equal(t, "invalidFilter", FilterProblem().Type)
}

func TestParsePatch(t *testing.T) {
	ops := func(s string) []PatchOp {
		var o []PatchOp
		require.NoError(t, json.Unmarshal([]byte(s), &o))
		return o
	}
	p, prob := ParsePatch(ops(`[{"op":"Replace","path":"active","value":"False"},{"op":"add","path":"displayName","value":"Ann"}]`))
	require.Nil(t, prob)
	require.NotNil(t, p.Active)
	assert.False(t, *p.Active)
	assert.Equal(t, "Ann", *p.Display)

	p, prob = ParsePatch(ops(`[{"op":"replace","value":{"displayName":"Bob","active":true,"externalId":"x","emails":[]}}]`))
	require.Nil(t, prob, "attributes this server does not store are ignored")
	assert.Equal(t, "Bob", *p.Display)
	assert.True(t, *p.Active)

	p, _ = ParsePatch(ops(`[{"op":"replace","path":"name.formatted","value":"Cy"}]`))
	assert.Equal(t, "Cy", *p.Display)

	for name, in := range map[string]string{
		"none":           `[]`,
		"remove":         `[{"op":"remove","path":"active"}]`,
		"bad active":     `[{"op":"replace","path":"active","value":"x"}]`,
		"bad display":    `[{"op":"replace","path":"displayName","value":3}]`,
		"no path scalar": `[{"op":"replace","value":"x"}]`,
	} {
		_, prob := ParsePatch(ops(in))
		require.NotNil(t, prob, name)
		assert.Equal(t, http.StatusBadRequest, prob.Status, name)
	}
}

func TestUserNameIsNotChangeable(t *testing.T) {
	ops := []PatchOp{{Op: "replace", Path: "userName", Value: json.RawMessage(`"A@acme.io"`)}}
	p, prob := ParsePatch(ops)
	require.Nil(t, prob)
	assert.True(t, p.SameName("a@acme.io"), "the same address in another case is the same")
	assert.False(t, p.SameName("b@acme.io"))
	assert.Equal(t, "mutability", NameChanged().Type)

	p, _ = ParsePatch([]PatchOp{{Op: "replace", Path: "userName", Value: json.RawMessage(`5`)}})
	assert.False(t, p.SameName("a@acme.io"), "a value that is not a string equals nothing")
	assert.True(t, Patch{}.SameName("a@acme.io"), "no assertion, nothing to differ")
}

func TestFromError(t *testing.T) {
	boom := FromError(errors.New("boom"))
	assert.Equal(t, http.StatusInternalServerError, boom.Status)
	assert.Equal(t, "internal error", boom.Detail, "nothing about the inside")

	bad := FromError(&crud.ServiceError{Status: http.StatusBadRequest, Code: errcode.ValidationFailed, Message: "bad"})
	assert.Equal(t, "invalidValue", bad.Type)
	assert.Equal(t, "bad", bad.Detail)

	dup := FromError(&crud.ServiceError{Status: http.StatusConflict, Code: errcode.AlreadyMember, Message: "exists"})
	assert.Equal(t, "uniqueness", dup.Type)
	other := FromError(&crud.ServiceError{Status: http.StatusConflict, Code: errcode.LastAdmin, Message: "last"})
	assert.Empty(t, other.Type)

	server := FromError(&crud.ServiceError{Status: http.StatusBadGateway, Code: "x", Message: "secret detail"})
	assert.Equal(t, "internal error", server.Detail, "a 5xx hides its message")

	body := dup.Body()
	assert.Equal(t, "409", body["status"])
	assert.Equal(t, "uniqueness", body["scimType"])
	assert.NotContains(t, other.Body(), "scimType")

	assert.Same(t, bad, FromError(bad), "a problem passes through")
}
