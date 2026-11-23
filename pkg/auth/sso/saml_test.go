package sso

import (
	"testing"

	"github.com/russellhaering/gosaml2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func assertion(nameID string, attrs map[string]string) *types.Assertion {
	a := &types.Assertion{Subject: &types.Subject{NameID: &types.NameID{Value: nameID}}}
	if len(attrs) > 0 {
		a.AttributeStatement = &types.AttributeStatement{}
		for name, v := range attrs {
			a.AttributeStatement.Attributes = append(a.AttributeStatement.Attributes,
				types.Attribute{Name: name, Values: []types.AttributeValue{{Value: v}}})
		}
	}
	return a
}

// With the email format the NameID is the address and the subject (the default, as before).
func TestSAMLEmailNameIDIsTheAddressAndTheSubject(t *testing.T) {
	who, err := (&SamlConfig{}).identityFromAssertion(assertion("ada@acme.io", nil))
	require.NoError(t, err)
	assert.Equal(t, "ada@acme.io", who.Email)
	assert.Equal(t, "ada@acme.io", who.ExternalSubject)
}

// With a persistent NameID the identifier is the subject and the address only ever comes from the
// attribute: a provider that reuses or changes addresses does not move the account (audit 07 #6).
func TestSAMLPersistentNameIDIsTheSubjectNeverTheAddress(t *testing.T) {
	cfg := &SamlConfig{NameIDFormat: NameIDPersistent, EmailAttribute: "mail"}
	who, err := cfg.identityFromAssertion(assertion("x7Qp-opaque", map[string]string{"mail": "ada@acme.io"}))
	require.NoError(t, err)
	assert.Equal(t, "ada@acme.io", who.Email)
	assert.Equal(t, "x7Qp-opaque", who.ExternalSubject)

	// The same person after an address change keeps the subject.
	who, err = cfg.identityFromAssertion(assertion("x7Qp-opaque", map[string]string{"mail": "ada@new.io"}))
	require.NoError(t, err)
	assert.Equal(t, "x7Qp-opaque", who.ExternalSubject)

	// A NameID that happens to contain "@" is not read as the address.
	_, err = cfg.identityFromAssertion(assertion("someone@elsewhere.io", nil))
	assert.Error(t, err, "no address attribute: the NameID is not used as one")
}

func TestSAMLNameIDFormatChoiceAndRequest(t *testing.T) {
	assert.True(t, ValidNameIDFormat(""))
	assert.True(t, ValidNameIDFormat(NameIDEmail))
	assert.True(t, ValidNameIDFormat(NameIDPersistent))
	assert.False(t, ValidNameIDFormat("transient"))
	assert.Equal(t, nameIDEmailURN, (&SamlConfig{}).nameIDURN())
	assert.Equal(t, nameIDPersistentURN, (&SamlConfig{NameIDFormat: NameIDPersistent}).nameIDURN())

	out, err := ServiceProviderMetadata(SamlURLsFor("https://app.example", [16]byte{1}), &SamlConfig{NameIDFormat: NameIDPersistent})
	require.NoError(t, err)
	assert.Contains(t, string(out), nameIDPersistentURN)
	assert.NotContains(t, string(out), nameIDEmailURN)
}
