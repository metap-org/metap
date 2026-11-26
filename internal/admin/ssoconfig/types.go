package ssoconfig

// The names and tags are what the OpenAPI document shows, so they stay as they were when these
// lived in package admin.

type SetSsoConfigRequest struct {
	IssuerUrl string `json:"issuerUrl" required:"false"`
	ClientId  string `json:"clientId" required:"false"`
	// Empty keeps the stored secret (required on first setup).
	ClientSecret string `json:"clientSecret" required:"false"`
	// Default: openid, email, profile.
	Scopes []string `json:"scopes" required:"false"`
	// Only these email domains may sign in; empty = any.
	EmailDomains []string `json:"emailDomains" required:"false"`
	// Role a person provisioned on first sign-in starts with; never admin.
	DefaultRole string `json:"defaultRole" required:"false"`
	Enabled     bool   `json:"enabled" required:"false"`
	Enforced    bool   `json:"enforced" required:"false"`
	// Where the browser lands after sign-in (absolute URL); default: the portal root.
	PostLoginRedirect string `json:"postLoginRedirect" required:"false"`
	// The id_token claim that carries groups or roles, and which tenant role each value means.
	// Only the roles named in roleMap follow the provider; never admin.
	RoleClaim string            `json:"roleClaim" required:"false"`
	RoleMap   map[string]string `json:"roleMap" required:"false"`
}

type SsoConfig struct {
	Configured        bool     `json:"configured" required:"false"`
	Enabled           bool     `json:"enabled" required:"false"`
	IssuerUrl         string   `json:"issuerUrl" required:"false"`
	ClientId          string   `json:"clientId" required:"false"`
	HasClientSecret   bool     `json:"hasClientSecret" required:"false"`
	Scopes            []string `json:"scopes" required:"false"`
	EmailDomains      []string `json:"emailDomains" required:"false"`
	DefaultRole       string   `json:"defaultRole" required:"false"`
	Enforced          bool     `json:"enforced" required:"false"`
	PostLoginRedirect string   `json:"postLoginRedirect" required:"false"`
	RoleClaim         string   `json:"roleClaim" required:"false"`
	// Never null.
	RoleMap map[string]string `json:"roleMap" required:"false"`
	// Register this redirect URI at the identity provider.
	RedirectUri string `json:"redirectUri" required:"false"`
	// Every email domain named above and whether the workspace has proved it owns it. Only a
	// verified domain routes a sign-in here or lets an address match or create a member.
	Domains []*SsoDomain `json:"domains" required:"false"`
}

type SsoDomain struct {
	Domain string `json:"domain" required:"false"`
	// "pending" or "verified".
	Status string `json:"status" required:"false"`
	// Publish verifyValue as a TXT record at verifyHost, then ask for a check.
	VerifyHost  string `json:"verifyHost" required:"false"`
	VerifyValue string `json:"verifyValue" required:"false"`
}

// SetSamlConfigRequest sets up SAML single sign-on. Give the provider's metadata as a URL (fetched
// now) or as XML; give neither to keep the metadata already stored.
type SetSamlConfigRequest struct {
	IdpMetadataUrl string `json:"idpMetadataUrl" required:"false"`
	IdpMetadataXml string `json:"idpMetadataXml" required:"false"`
	// The attribute that carries the address; empty uses the NameID when it is an address.
	EmailAttribute string `json:"emailAttribute" required:"false"`
	// "emailAddress" (default) or "persistent". With persistent the provider's opaque identifier is
	// the person's identity, so a changed or reused address does not move the account; the address
	// then has to come from an attribute.
	NameIdFormat string `json:"nameIdFormat" required:"false"`
	// The attribute that lists groups or roles, and which tenant role each value means. Never admin.
	RoleAttribute     string            `json:"roleAttribute" required:"false"`
	RoleMap           map[string]string `json:"roleMap" required:"false"`
	EmailDomains      []string          `json:"emailDomains" required:"false"`
	DefaultRole       string            `json:"defaultRole" required:"false"`
	Enabled           bool              `json:"enabled" required:"false"`
	Enforced          bool              `json:"enforced" required:"false"`
	PostLoginRedirect string            `json:"postLoginRedirect" required:"false"`
}

type SamlConfig struct {
	Configured     bool   `json:"configured" required:"false"`
	Enabled        bool   `json:"enabled" required:"false"`
	IdpMetadataUrl string `json:"idpMetadataUrl" required:"false"`
	IdpEntityId    string `json:"idpEntityId" required:"false"`
	// The provider's sign-in endpoint taken from its metadata.
	IdpSsoUrl         string            `json:"idpSsoUrl" required:"false"`
	EmailAttribute    string            `json:"emailAttribute" required:"false"`
	NameIdFormat      string            `json:"nameIdFormat" required:"false"`
	RoleAttribute     string            `json:"roleAttribute" required:"false"`
	RoleMap           map[string]string `json:"roleMap" required:"false"`
	EmailDomains      []string          `json:"emailDomains" required:"false"`
	DefaultRole       string            `json:"defaultRole" required:"false"`
	Enforced          bool              `json:"enforced" required:"false"`
	PostLoginRedirect string            `json:"postLoginRedirect" required:"false"`
	// Register these at the identity provider.
	EntityId    string `json:"entityId" required:"false"`
	AcsUrl      string `json:"acsUrl" required:"false"`
	MetadataUrl string `json:"metadataUrl" required:"false"`
	// Each domain above and whether the workspace has proved it owns it.
	Domains []*SsoDomain `json:"domains" required:"false"`
}

type VerifySsoDomainRequest struct {
	Domain string `json:"domain" required:"false"`
}

func (x *VerifySsoDomainRequest) GetDomain() string {
	if x != nil {
		return x.Domain
	}
	return ""
}

func (x *SetSamlConfigRequest) GetIdpMetadataUrl() string {
	if x != nil {
		return x.IdpMetadataUrl
	}
	return ""
}

func (x *SetSamlConfigRequest) GetIdpMetadataXml() string {
	if x != nil {
		return x.IdpMetadataXml
	}
	return ""
}

func (x *SetSamlConfigRequest) GetEnabled() bool {
	if x != nil {
		return x.Enabled
	}
	return false
}

func (x *SsoConfig) GetConfigured() bool {
	if x != nil {
		return x.Configured
	}
	return false
}

func (x *SsoConfig) GetEnabled() bool {
	if x != nil {
		return x.Enabled
	}
	return false
}

func (x *SsoConfig) GetIssuerUrl() string {
	if x != nil {
		return x.IssuerUrl
	}
	return ""
}

func (x *SsoConfig) GetClientId() string {
	if x != nil {
		return x.ClientId
	}
	return ""
}

func (x *SsoConfig) GetHasClientSecret() bool {
	if x != nil {
		return x.HasClientSecret
	}
	return false
}

func (x *SsoConfig) GetScopes() []string {
	if x != nil {
		return x.Scopes
	}
	return nil
}

func (x *SsoConfig) GetEmailDomains() []string {
	if x != nil {
		return x.EmailDomains
	}
	return nil
}

func (x *SsoConfig) GetDefaultRole() string {
	if x != nil {
		return x.DefaultRole
	}
	return ""
}

func (x *SsoConfig) GetEnforced() bool {
	if x != nil {
		return x.Enforced
	}
	return false
}

func (x *SsoConfig) GetPostLoginRedirect() string {
	if x != nil {
		return x.PostLoginRedirect
	}
	return ""
}

func (x *SsoConfig) GetRedirectUri() string {
	if x != nil {
		return x.RedirectUri
	}
	return ""
}

func (x *SetSsoConfigRequest) GetIssuerUrl() string {
	if x != nil {
		return x.IssuerUrl
	}
	return ""
}

func (x *SetSsoConfigRequest) GetClientId() string {
	if x != nil {
		return x.ClientId
	}
	return ""
}

func (x *SetSsoConfigRequest) GetClientSecret() string {
	if x != nil {
		return x.ClientSecret
	}
	return ""
}

func (x *SetSsoConfigRequest) GetScopes() []string {
	if x != nil {
		return x.Scopes
	}
	return nil
}

func (x *SetSsoConfigRequest) GetEmailDomains() []string {
	if x != nil {
		return x.EmailDomains
	}
	return nil
}

func (x *SetSsoConfigRequest) GetDefaultRole() string {
	if x != nil {
		return x.DefaultRole
	}
	return ""
}

func (x *SetSsoConfigRequest) GetEnabled() bool {
	if x != nil {
		return x.Enabled
	}
	return false
}

func (x *SetSsoConfigRequest) GetEnforced() bool {
	if x != nil {
		return x.Enforced
	}
	return false
}

func (x *SetSsoConfigRequest) GetPostLoginRedirect() string {
	if x != nil {
		return x.PostLoginRedirect
	}
	return ""
}

func (x *SetSsoConfigRequest) GetRoleClaim() string {
	if x != nil {
		return x.RoleClaim
	}
	return ""
}

func (x *SetSsoConfigRequest) GetRoleMap() map[string]string {
	if x != nil {
		return x.RoleMap
	}
	return nil
}
