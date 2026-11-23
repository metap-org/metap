package oauthserver

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// RFC 7636 Appendix B's worked example.
const (
	pkceTestVerifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	pkceTestChallenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
)

func TestVerifyPKCEMatchesTheRFCWorkedExample(t *testing.T) {
	require.True(t, VerifyPKCE(pkceTestVerifier, pkceTestChallenge), "expected the RFC worked example to verify")
}

func TestVerifyPKCERejectsAWrongVerifier(t *testing.T) {
	require.False(t, VerifyPKCE("wrong-verifier", pkceTestChallenge), "expected a wrong verifier to fail")
}
