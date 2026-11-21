package tenantauth

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/crewjam/saml"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	xrv "github.com/mattermost/xml-roundtrip-validator"
)

// SAML single sign-on (service-provider initiated, HTTP-Redirect out, HTTP-POST back). A tenant's
// settings live in tenant_auth_configs (provider_kind 'saml'); the identity provider's metadata is
// fetched once, when the admin saves, and the XML is kept with the settings so a sign-in never
// depends on the provider's metadata endpoint being up. Assertions must be signed by a key in that
// metadata; encrypted assertions are not supported.

const ProviderSAML = "saml"

// SamlConfig is one tenant's SAML settings.
type SamlConfig struct {
	// IdpMetadataURL is where the metadata came from (informational, and what a refresh re-reads).
	IdpMetadataURL string `json:"idpMetadataUrl,omitempty"`
	// IdpMetadataXML is the metadata the provider's endpoints and signing keys are taken from.
	IdpMetadataXML string `json:"idpMetadataXml"`
	// EmailAttribute names the attribute that carries the address. Empty: the NameID when it is an
	// address, else the first of the usual attribute names.
	EmailAttribute string `json:"emailAttribute,omitempty"`
	// RoleAttribute and RoleMap work like the OIDC role claim: see RoleMapping.
	RoleAttribute string            `json:"roleAttribute,omitempty"`
	RoleMap       map[string]string `json:"roleMap,omitempty"`
	EmailDomains  []string          `json:"emailDomains,omitempty"`
	DefaultRole   string            `json:"defaultRole,omitempty"`
	Enforced      bool              `json:"enforced,omitempty"`
	// PostLoginRedirect: where the browser lands after a successful sign-in (same origin as the app).
	PostLoginRedirect string `json:"postLoginRedirect,omitempty"`
}

// Mapping is the settings' group-to-role mapping.
func (c *SamlConfig) Mapping() RoleMapping {
	return RoleMapping{Claim: c.RoleAttribute, Map: c.RoleMap}
}

// EmailAllowed reports whether address is in the domain list (everything is allowed when it is empty).
func (c *SamlConfig) EmailAllowed(address string) bool {
	return emailInDomains(c.EmailDomains, address)
}

// SamlURLs are this service's SAML endpoints for one tenant, which the provider is registered with.
type SamlURLs struct{ EntityID, Metadata, ACS string }

// SamlURLsFor builds the endpoints from the service's public origin.
func SamlURLsFor(publicURL string, tenant uuid.UUID) SamlURLs {
	base := strings.TrimRight(publicURL, "/") + "/auth/saml/" + tenant.String()
	return SamlURLs{EntityID: base + "/metadata", Metadata: base + "/metadata", ACS: base + "/acs"}
}

// ParseIdpMetadata reads and sanity-checks a provider's metadata: it must describe an identity
// provider with a sign-in endpoint and at least one signing certificate.
func ParseIdpMetadata(raw []byte) (*saml.EntityDescriptor, error) {
	md, err := parseMetadata(raw)
	if err != nil {
		return nil, fmt.Errorf("not SAML metadata: %w", err)
	}
	if len(md.IDPSSODescriptors) == 0 {
		return nil, errors.New("the metadata does not describe an identity provider")
	}
	idp := md.IDPSSODescriptors[0]
	if IdpRedirectLocation(md) == "" {
		return nil, errors.New("the metadata has no HTTP-Redirect single sign-on endpoint")
	}
	signing := 0
	for _, kd := range idp.KeyDescriptors {
		if kd.Use == "" || kd.Use == "signing" {
			signing++
		}
	}
	if signing == 0 {
		return nil, errors.New("the metadata has no signing certificate")
	}
	return md, nil
}

// parseMetadata reads metadata whose top element is an EntityDescriptor or an EntitiesDescriptor
// (federations publish the latter): in that case the first entity that is an identity provider.
func parseMetadata(raw []byte) (*saml.EntityDescriptor, error) {
	if err := xrv.Validate(bytes.NewReader(raw)); err != nil {
		return nil, err
	}
	entity := &saml.EntityDescriptor{}
	err := xml.Unmarshal(raw, entity)
	if err != nil && strings.Contains(err.Error(), "EntitiesDescriptor") {
		entities := &saml.EntitiesDescriptor{}
		if err := xml.Unmarshal(raw, entities); err != nil {
			return nil, err
		}
		for i := range entities.EntityDescriptors {
			if len(entities.EntityDescriptors[i].IDPSSODescriptors) > 0 {
				return &entities.EntityDescriptors[i], nil
			}
		}
		return nil, errors.New("no entity found with an identity provider description")
	}
	if err != nil {
		return nil, err
	}
	return entity, nil
}

// IdpRedirectLocation is the provider's HTTP-Redirect sign-in endpoint ("" when it has none).
func IdpRedirectLocation(md *saml.EntityDescriptor) string {
	for _, d := range md.IDPSSODescriptors {
		for _, s := range d.SingleSignOnServices {
			if s.Binding == saml.HTTPRedirectBinding {
				return s.Location
			}
		}
	}
	return ""
}

// FetchIdpMetadata downloads metadata from an https URL (plain http only for loopback, for local
// development) and validates it. The caller supplies the HTTP client, so a deployment can put its
// outbound-request guard in front of a tenant-supplied URL.
func FetchIdpMetadata(ctx context.Context, client *http.Client, rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || !(u.Scheme == "https" || (u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"))) {
		return nil, errors.New("the metadata URL must be an https URL")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}}
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the metadata URL answered %d", res.StatusCode)
	}
	buf := make([]byte, 0, 64*1024)
	chunk := make([]byte, 32*1024)
	for {
		n, err := res.Body.Read(chunk)
		buf = append(buf, chunk[:n]...)
		if len(buf) > 1<<20 {
			return nil, errors.New("the metadata is larger than 1 MB")
		}
		if err != nil {
			break
		}
	}
	if _, err := ParseIdpMetadata(buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// ServiceProvider builds the SAML service provider for a tenant from its settings.
func (c *SamlConfig) ServiceProvider(urls SamlURLs) (*saml.ServiceProvider, error) {
	md, err := ParseIdpMetadata([]byte(c.IdpMetadataXML))
	if err != nil {
		return nil, err
	}
	meta, err := url.Parse(urls.Metadata)
	if err != nil {
		return nil, err
	}
	acs, err := url.Parse(urls.ACS)
	if err != nil {
		return nil, err
	}
	return &saml.ServiceProvider{
		EntityID:          urls.EntityID,
		MetadataURL:       *meta,
		AcsURL:            *acs,
		IDPMetadata:       md,
		AuthnNameIDFormat: saml.EmailAddressNameIDFormat,
		// Only answers to a request this service made: an unsolicited (IdP-initiated) response has
		// no state to bind it to a browser, so it is refused.
		AllowIDPInitiated: false,
	}, nil
}

// StartRedirect builds the redirect to the provider and returns it with the request's ID, which
// the response must echo (InResponseTo).
func StartRedirect(sp *saml.ServiceProvider, relayState string) (redirectURL *url.URL, requestID string, err error) {
	binding := saml.HTTPRedirectBinding
	req, err := sp.MakeAuthenticationRequest(sp.GetSSOBindingLocation(binding), binding, saml.HTTPPostBinding)
	if err != nil {
		return nil, "", err
	}
	u, err := req.Redirect(relayState, sp)
	if err != nil {
		return nil, "", err
	}
	return u, req.ID, nil
}

// emailAttributeNames are tried, in order, when no attribute is configured.
var emailAttributeNames = []string{
	"email", "mail", "emailaddress",
	"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress",
	"urn:oid:0.9.2342.19200300.100.1.3",
}

// IdentityFromAssertion turns a verified assertion into who signed in. A SAML assertion carries no
// `email_verified`: the provider the tenant chose signed it, and the tenant's domain proof (checked
// by the caller) is what says the tenant may speak for that address.
func (c *SamlConfig) IdentityFromAssertion(a *saml.Assertion) (*VerifiedIdentity, error) {
	if a == nil || a.Subject == nil || a.Subject.NameID == nil || strings.TrimSpace(a.Subject.NameID.Value) == "" {
		return nil, errors.New("the assertion names no subject")
	}
	attrs := map[string][]string{}
	for _, st := range a.AttributeStatements {
		for _, at := range st.Attributes {
			var values []string
			for _, v := range at.Values {
				values = append(values, strings.TrimSpace(v.Value))
			}
			for _, name := range []string{at.Name, at.FriendlyName} {
				if name != "" {
					attrs[strings.ToLower(name)] = append(attrs[strings.ToLower(name)], values...)
				}
			}
		}
	}
	nameID := strings.TrimSpace(a.Subject.NameID.Value)
	email := ""
	switch {
	case c.EmailAttribute != "":
		if v := attrs[strings.ToLower(c.EmailAttribute)]; len(v) > 0 {
			email = v[0]
		}
	case strings.Contains(nameID, "@"):
		email = nameID
	default:
		for _, name := range emailAttributeNames {
			if v := attrs[name]; len(v) > 0 {
				email = v[0]
				break
			}
		}
	}
	if !strings.Contains(email, "@") {
		return nil, errors.New("the assertion carries no email address")
	}
	who := &VerifiedIdentity{Email: email, EmailVerified: true, ExternalSubject: nameID}
	if c.RoleAttribute != "" {
		who.Roles = attrs[strings.ToLower(c.RoleAttribute)]
	}
	return who, nil
}

// --- storage ---------------------------------------------------------------------------------

// SaveSamlConfig inserts or replaces the tenant's SAML settings.
func SaveSamlConfig(ctx context.Context, db DBTX, tenant uuid.UUID, cfg *SamlConfig, enabled bool) error {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `INSERT INTO tenant_auth_configs (tenant_id, provider_kind, enabled, config) VALUES ($1, 'saml', $2, $3)
		ON CONFLICT (tenant_id, provider_kind) DO UPDATE SET enabled = EXCLUDED.enabled, config = EXCLUDED.config`, tenant, enabled, raw)
	return err
}

// StoredSamlConfig reads the settings whether or not they are enabled; (nil, false, nil) when there are none.
func StoredSamlConfig(ctx context.Context, db DBTX, tenant uuid.UUID) (*SamlConfig, bool, error) {
	var raw []byte
	var enabled bool
	err := db.QueryRow(ctx, `SELECT config, enabled FROM tenant_auth_configs WHERE tenant_id = $1 AND provider_kind = 'saml'`,
		tenant).Scan(&raw, &enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var cfg SamlConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, false, err
	}
	return &cfg, enabled, nil
}

// SamlConfigFor is the tenant's enabled SAML settings, or (nil, nil).
func SamlConfigFor(ctx context.Context, db DBTX, tenant uuid.UUID) (*SamlConfig, error) {
	cfg, enabled, err := StoredSamlConfig(ctx, db, tenant)
	if err != nil || cfg == nil || !enabled {
		return nil, err
	}
	return cfg, nil
}

// DeleteSamlConfig removes the tenant's SAML settings.
func DeleteSamlConfig(ctx context.Context, db DBTX, tenant uuid.UUID) error {
	_, err := db.Exec(ctx, `DELETE FROM tenant_auth_configs WHERE tenant_id = $1 AND provider_kind = 'saml'`, tenant)
	return err
}
