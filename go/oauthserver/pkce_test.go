package oauthserver

import "testing"

// RFC 7636 Appendix B's worked example.
const (
	pkceTestVerifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	pkceTestChallenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
)

func TestVerifyPKCEMatchesTheRFCWorkedExample(t *testing.T) {
	if !VerifyPKCE(pkceTestVerifier, pkceTestChallenge) {
		t.Fatal("expected the RFC worked example to verify")
	}
}

func TestVerifyPKCERejectsAWrongVerifier(t *testing.T) {
	if VerifyPKCE("wrong-verifier", pkceTestChallenge) {
		t.Fatal("expected a wrong verifier to fail")
	}
}
