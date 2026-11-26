package owner

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
)

func TestOldest(t *testing.T) {
	now := time.Now()
	a := identity.Member{ID: uuid.New(), CreatedAt: now}
	b := identity.Member{ID: uuid.New(), CreatedAt: now.Add(-time.Hour)}
	c := identity.Member{ID: uuid.New(), CreatedAt: now.Add(time.Hour)}
	assert.Equal(t, b.ID, oldest([]identity.Member{a, b, c}).ID)
	assert.Equal(t, a.ID, oldest([]identity.Member{a}).ID)
}

func status(t *testing.T, err error) int {
	var se *crud.ServiceError
	require.ErrorAs(t, err, &se)
	return int(se.Status)
}

func TestStoreErr(t *testing.T) {
	assert.Equal(t, http.StatusConflict, status(t, storeErr(identity.ErrRecoveryPending)))
	assert.Equal(t, http.StatusConflict, status(t, storeErr(identity.ErrRecoveryNotReady)))
	assert.Equal(t, http.StatusNotFound, status(t, storeErr(identity.ErrNoRecovery)))
	assert.Equal(t, http.StatusNotFound, status(t, storeErr(identity.ErrNotFound)))
	assert.Equal(t, http.StatusBadRequest, status(t, storeErr(identity.ErrSupportReason)))
	boom := errors.New("boom")
	assert.Same(t, boom, storeErr(boom), "anything else is internal")
}

func TestStepUpErr(t *testing.T) {
	assert.Equal(t, http.StatusBadRequest, status(t, stepUpErr(identity.ErrInvalidCredentials)))
	assert.Equal(t, http.StatusBadRequest, status(t, fieldRequired("password", "x")))
}

func TestRecoveryView(t *testing.T) {
	now := time.Now()
	r := &identity.OwnerRecovery{ID: uuid.New(), NewOwnerMember: uuid.New(), RequestedByEmail: "v@vendor.io", Reason: "lost", EffectiveAt: now, CreatedAt: now}
	v := recoveryView(r)
	assert.Equal(t, r.ID.String(), v.Id)
	assert.Empty(t, v.CompletedAt)
}
