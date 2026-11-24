package apikeys

// The names and tags are what the OpenAPI document shows, so they stay as they were when these
// lived in package admin.

type CreateApiKeyRequest struct {
	// The member (service account or person) the key acts as.
	UserId string `json:"userId" required:"false"`
	Name   string `json:"name" required:"false"`
	// 0 = does not expire.
	ExpiresInDays int32 `json:"expiresInDays" required:"false"`
}

type ApiKey struct {
	Id     string `json:"id" required:"false"`
	UserId string `json:"userId" required:"false"`
	Name   string `json:"name" required:"false"`
	// First characters of the key, to recognise it in a list.
	Prefix     string `json:"prefix" required:"false"`
	ExpiresAt  string `json:"expiresAt" required:"false"`
	RevokedAt  string `json:"revokedAt" required:"false"`
	LastUsedAt string `json:"lastUsedAt" required:"false"`
	CreatedAt  string `json:"createdAt" required:"false"`
}

type CreateApiKeyResponse struct {
	ApiKey *ApiKey `json:"apiKey" required:"false"`
	Token  string  `json:"token" required:"false"`
}

type ListApiKeysResponse struct {
	ApiKeys []*ApiKey `json:"apiKeys" required:"false"`
}
