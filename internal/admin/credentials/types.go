package credentials

// The names and tags are what the OpenAPI document shows, so they stay as they were when these
// lived in package admin.

// CredentialsView is the Credentials tab: the tenant's named credentials, and what this
// deployment can store.
type CredentialsView struct {
	Credentials []*Credential `json:"credentials" required:"false"`
	// DatabaseAvailable: credentials can be saved encrypted in metap's database.
	DatabaseAvailable bool `json:"databaseAvailable" required:"false"`
	// ExternalAvailable: an external secret manager takes writes; ExternalKind names it.
	ExternalAvailable bool   `json:"externalAvailable" required:"false"`
	ExternalKind      string `json:"externalKind" required:"false"`
}

// Credential is one named credential; its value is never returned.
type Credential struct {
	Id          string `json:"id" required:"false"`
	Name        string `json:"name" required:"false"`
	Description string `json:"description" required:"false"`
	// Kind: "authorization" (an HTTP Authorization header value), "secret" (anything opaque) or
	// "userpass" (a username and a password).
	Kind string `json:"kind" required:"false"`
	// Username is shown for a "userpass" credential (the password is write-only like any value).
	Username string `json:"username" required:"false"`
	// Source is where the value lives: "database" or "external".
	Source    string           `json:"source" required:"false"`
	CreatedAt string           `json:"createdAt" required:"false"`
	UpdatedAt string           `json:"updatedAt" required:"false"`
	UsedBy    []*CredentialUse `json:"usedBy" required:"false"`
}

// CredentialUse is a thing that links to a credential (today: a cron job).
type CredentialUse struct {
	Kind string `json:"kind" required:"false"`
	Id   string `json:"id" required:"false"`
	Name string `json:"name" required:"false"`
}

type CreateCredentialRequest struct {
	Name        string `json:"name" required:"true" minLength:"1" maxLength:"64"`
	Description string `json:"description,omitempty" required:"false" maxLength:"500"`
	Kind        string `json:"kind" required:"true" enum:"authorization,secret,userpass"`
	// Username is required for "userpass" and refused for the other kinds; Value is then the password.
	Username string `json:"username,omitempty" required:"false" maxLength:"128"`
	// Source is where to save the value: "database" (encrypted in metap) or "external" (Vault, AWS, GCP).
	Source string `json:"source" required:"true" enum:"database,external"`
	Value  string `json:"value" required:"true" minLength:"1" maxLength:"4096"`
}

// UpdateCredentialRequest changes only what is set. A new value replaces the stored one; moving
// to another source needs a value too, since a stored value is never read back.
type UpdateCredentialRequest struct {
	Id          string  `json:"id" required:"false"`
	Name        *string `json:"name,omitempty" required:"false"`
	Description *string `json:"description,omitempty" required:"false"`
	// Username changes a "userpass" credential's username; the password stays unless Value is set.
	Username *string `json:"username,omitempty" required:"false" maxLength:"128"`
	Source   *string `json:"source,omitempty" required:"false" enum:"database,external"`
	Value    *string `json:"value,omitempty" required:"false" maxLength:"4096"`
}

type CredentialIdRequest struct {
	Id string `json:"id" required:"false"`
}
