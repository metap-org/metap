package users

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/permission"
)

func status(t *testing.T, err error) int {
	var se *crud.ServiceError
	require.ErrorAs(t, err, &se)
	return int(se.Status)
}

func TestView(t *testing.T) {
	now := time.Now()
	inviter := uuid.New()
	m := identity.Member{ID: uuid.New(), Email: "a@x.io", Status: identity.StatusInvited, CreatedAt: now, InvitedBy: &inviter}
	inv := &identity.Invitation{ExpiresAt: now.Add(time.Hour)}
	scoped := []permission.ScopedRole{{Role: "analyst", ScopeType: "zone", ScopeID: "z1"}}

	u := view(m, nil, scoped, true, true, inv)
	assert.Equal(t, []string{}, u.Roles, "no roles is an empty list, not null")
	assert.True(t, u.IsOwner)
	assert.True(t, u.SharedIdentity)
	assert.Equal(t, inviter.String(), u.InvitedBy)
	assert.NotEmpty(t, u.InvitationExpiresAt)
	require.Len(t, u.ScopedRoles, 1)
	assert.Equal(t, "z1", u.ScopedRoles[0].ScopeId)

	plain := view(m, []string{"viewer"}, nil, false, false, nil)
	assert.Empty(t, plain.InvitationExpiresAt)
	assert.Empty(t, plain.ScopedRoles)
}

func TestOtherAdmins(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	admins := []uuid.UUID{a, b}
	others, was := otherAdmins(admins, a)
	assert.True(t, was)
	assert.Equal(t, []uuid.UUID{b}, others)
	assert.Equal(t, []uuid.UUID{a, b}, admins, "the input is not modified")
	_, was = otherAdmins(admins, c)
	assert.False(t, was)
}

func TestScopedHelpers(t *testing.T) {
	a := []*ScopedRole{{Role: "r", ScopeType: "zone", ScopeId: "1"}}
	b := []*ScopedRole{{Role: "r", ScopeType: "zone", ScopeId: "1"}}
	assert.True(t, sameScoped(a, b))
	b[0].ScopeId = "2"
	assert.False(t, sameScoped(a, b))
	assert.Equal(t, []string{"r@zone:1"}, scopedForAudit(a))
	assert.Equal(t, []*ScopedRole{{Role: "r", ScopeType: "zone", ScopeId: "1"}}, scopedView([]permission.ScopedRole{{Role: "r", ScopeType: "zone", ScopeID: "1"}}))
}

func TestErrors(t *testing.T) {
	assert.Equal(t, http.StatusBadRequest, status(t, passwordRequired()))
	assert.Equal(t, http.StatusConflict, status(t, selfModification("disable")))
	assert.Equal(t, http.StatusConflict, status(t, lastAdmin()))
	assert.Equal(t, http.StatusConflict, status(t, sharedPerson("password")))
	assert.Equal(t, http.StatusBadRequest, status(t, scopeInvalid(errors.New("bad scope"))))
}
