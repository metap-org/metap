package credentials

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/crud"
)

func fieldOf(t *testing.T, err error) string {
	var se *crud.ServiceError
	require.ErrorAs(t, err, &se)
	assert.EqualValues(t, http.StatusBadRequest, se.Status)
	for k := range se.FieldErrors {
		return k
	}
	return ""
}

func TestCheckName(t *testing.T) {
	n, err := checkName("  CRM webhook ")
	require.NoError(t, err)
	assert.Equal(t, "CRM webhook", n)
	for _, bad := range []string{"", "   ", strings.Repeat("x", 65), "a\nb"} {
		_, err := checkName(bad)
		assert.Equal(t, "name", fieldOf(t, err), bad)
	}
}

func TestCheckValue(t *testing.T) {
	assert.NoError(t, checkValue(KindAuthorization, "Bearer abc"))
	assert.NoError(t, checkValue(KindSecret, "line1\nline2"), "a generic secret may hold a newline")
	assert.Equal(t, "value", fieldOf(t, checkValue(KindAuthorization, "a\nb")), "a header value is one printable line")
	assert.Equal(t, "value", fieldOf(t, checkValue(KindSecret, "")))
	assert.Equal(t, "value", fieldOf(t, checkValue(KindSecret, strings.Repeat("x", maxValue+1))))
	assert.Equal(t, "value", fieldOf(t, checkValue(KindSecret, "userpass:u:p")), "the encoded form is reserved")
	assert.NoError(t, checkValue(KindUserPass, "userpass:fine-as-a-password"))
}

func TestCheckUsername(t *testing.T) {
	u, err := checkUsername(" bob ")
	require.NoError(t, err)
	assert.Equal(t, "bob", u)
	for _, bad := range []string{"", "a:b", strings.Repeat("x", 129), "a\tb"} {
		_, err := checkUsername(bad)
		assert.Equal(t, "username", fieldOf(t, err), bad)
	}
}

func TestCheckHome(t *testing.T) {
	both := kernel.CredentialCapabilities{Database: true, External: true}
	assert.NoError(t, checkHome(both, SourceDatabase))
	assert.NoError(t, checkHome(both, SourceExternal))
	assert.Equal(t, "source", fieldOf(t, checkHome(both, "cloud")))
	var se *crud.ServiceError
	require.ErrorAs(t, checkHome(kernel.CredentialCapabilities{External: true}, SourceDatabase), &se)
	assert.EqualValues(t, http.StatusConflict, se.Status)
	require.ErrorAs(t, checkHome(kernel.CredentialCapabilities{Database: true}, SourceExternal), &se)
	assert.EqualValues(t, http.StatusConflict, se.Status)
}

func TestKindsAndInUse(t *testing.T) {
	assert.True(t, validKind(KindUserPass))
	assert.False(t, validKind("weird"))
	err := inUseError([]*CredentialUse{{Name: "nightly"}, {Name: "weekly"}})
	var se *crud.ServiceError
	require.ErrorAs(t, err, &se)
	assert.EqualValues(t, http.StatusConflict, se.Status)
	assert.Equal(t, []string{"nightly", "weekly"}, se.FieldErrors["usedBy"])
}
