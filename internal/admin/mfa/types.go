package mfa

// The names are what the OpenAPI document shows, so they stay as they were when these lived in
// package admin.

type MfaStatus struct {
	Enabled           bool  `json:"enabled" required:"false"`
	RecoveryCodesLeft int32 `json:"recoveryCodesLeft" required:"false"`
}

type MfaEnrollment struct {
	Secret     string `json:"secret" required:"false"`
	OtpauthUri string `json:"otpauthUri" required:"false"`
}

type MfaCodeRequest struct {
	Code string `json:"code" required:"false"`
}

type MfaRecoveryCodes struct {
	Codes []string `json:"codes" required:"false"`
}
