package sso

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	saml2 "github.com/russellhaering/gosaml2"
	"github.com/russellhaering/gosaml2/types"
	dsig "github.com/russellhaering/goxmldsig"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	xrv "github.com/mattermost/xml-roundtrip-validator"
)

// SAML single sign-on (service-provider initiated, HTTP-Redirect out, HTTP-POST back), on
// gosaml2. A tenant's settings live in tenant_auth_configs (provider_kind 'saml'); the identity
// provider's metadata is fetched once, when the admin saves, and the XML is kept with the settings
// so a sign-in never depends on the provider's metadata endpoint being up. Assertions must be signed
// by a certificate in that metadata; encrypted assertions are not supported.

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

// IdpMetadata is what a service provider needs from a provider's metadata.
type IdpMetadata struct {
	EntityID string
	// RedirectURL is the HTTP-Redirect single sign-on endpoint.
	RedirectURL string
	// Certs are the certificates the provider signs with.
	Certs []*x509.Certificate
}

// entitiesDescriptor is a federation's wrapper around several entities.
type entitiesDescriptor struct {
	XMLName  xml.Name                 `xml:"urn:oasis:names:tc:SAML:2.0:metadata EntitiesDescriptor"`
	Entities []types.EntityDescriptor `xml:"EntityDescriptor"`
}

// ParseIdpMetadata reads and sanity-checks a provider's metadata: it must describe an identity
// provider with an HTTP-Redirect sign-in endpoint and at least one signing certificate. Metadata
// that wraps several entities (a federation's) yields the first identity provider.
func ParseIdpMetadata(raw []byte) (*IdpMetadata, error) {
	if err := xrv.Validate(bytes.NewReader(raw)); err != nil {
		return nil, fmt.Errorf("not SAML metadata: %w", err)
	}
	var entity types.EntityDescriptor
	err := xml.Unmarshal(raw, &entity)
	if err != nil && strings.Contains(err.Error(), "EntitiesDescriptor") {
		var many entitiesDescriptor
		if err := xml.Unmarshal(raw, &many); err != nil {
			return nil, fmt.Errorf("not SAML metadata: %w", err)
		}
		entity, err = firstIdentityProvider(many)
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, fmt.Errorf("not SAML metadata: %w", err)
	}
	idp := entity.IDPSSODescriptor
	if idp == nil {
		return nil, errors.New("the metadata does not describe an identity provider")
	}
	out := &IdpMetadata{EntityID: entity.EntityID}
	for _, s := range idp.SingleSignOnServices {
		if s.Binding == saml2.BindingHttpRedirect {
			out.RedirectURL = s.Location
			break
		}
	}
	if out.RedirectURL == "" {
		return nil, errors.New("the metadata has no HTTP-Redirect single sign-on endpoint")
	}
	for _, kd := range idp.KeyDescriptors {
		if kd.Use != "" && kd.Use != "signing" {
			continue
		}
		for _, c := range kd.KeyInfo.X509Data.X509Certificates {
			der, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(c.Data), ""))
			if err != nil {
				return nil, errors.New("a certificate in the metadata is not valid base64")
			}
			cert, err := x509.ParseCertificate(der)
			if err != nil {
				return nil, errors.New("a certificate in the metadata could not be read")
			}
			out.Certs = append(out.Certs, cert)
		}
	}
	if len(out.Certs) == 0 {
		return nil, errors.New("the metadata has no signing certificate")
	}
	return out, nil
}

func firstIdentityProvider(many entitiesDescriptor) (types.EntityDescriptor, error) {
	for _, e := range many.Entities {
		if e.IDPSSODescriptor != nil {
			return e, nil
		}
	}
	return types.EntityDescriptor{}, errors.New("no entity found with an identity provider description")
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
func (c *SamlConfig) ServiceProvider(urls SamlURLs) (*saml2.SAMLServiceProvider, error) {
	md, err := ParseIdpMetadata([]byte(c.IdpMetadataXML))
	if err != nil {
		return nil, err
	}
	return &saml2.SAMLServiceProvider{
		IdentityProviderSSOURL:      md.RedirectURL,
		IdentityProviderSSOBinding:  saml2.BindingHttpRedirect,
		IdentityProviderIssuer:      md.EntityID,
		ServiceProviderIssuer:       urls.EntityID,
		AudienceURI:                 urls.EntityID,
		AssertionConsumerServiceURL: urls.ACS,
		// Exactly the provider's own signing certificates are trusted.
		IDPCertificateStore:    &dsig.MemoryX509CertificateStore{Roots: md.Certs},
		NameIdFormat:           "urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress",
		AllowMissingAttributes: true,
		Clock:                  dsig.NewRealClock(),
	}, nil
}

// StartRedirect builds the redirect to the provider and returns it with the request's ID, which
// the response must echo (InResponseTo).
func StartRedirect(sp *saml2.SAMLServiceProvider, relayState string) (redirectURL *url.URL, requestID string, err error) {
	doc, err := sp.BuildAuthRequestDocument()
	if err != nil {
		return nil, "", err
	}
	requestID = doc.Root().SelectAttrValue("ID", "")
	if requestID == "" {
		return nil, "", errors.New("the sign-in request has no ID")
	}
	raw, err := sp.BuildAuthURLRedirect(relayState, doc)
	if err != nil {
		return nil, "", err
	}
	redirectURL, err = url.Parse(raw)
	return redirectURL, requestID, err
}

// ServiceProviderMetadata is what an admin registers at the identity provider: this service's
// entity ID and assertion consumer URL for the tenant. It asks for signed assertions and signs
// nothing itself, so it carries no keys.
func ServiceProviderMetadata(urls SamlURLs) ([]byte, error) {
	md := types.EntityDescriptor{
		EntityID: urls.EntityID,
		SPSSODescriptor: &types.SPSSODescriptor{
			AuthnRequestsSigned:        false,
			WantAssertionsSigned:       true,
			ProtocolSupportEnumeration: "urn:oasis:names:tc:SAML:2.0:protocol",
			NameIDFormats:              []string{"urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress"},
			AssertionConsumerServices:  []types.IndexedEndpoint{{Binding: saml2.BindingHttpPost, Location: urls.ACS, Index: 1}},
		},
	}
	out, err := xml.MarshalIndent(md, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), out...), nil
}

// emailAttributeNames are tried, in order, when no attribute is configured.
var emailAttributeNames = []string{
	"email", "mail", "emailaddress",
	"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress",
	"urn:oid:0.9.2342.19200300.100.1.3",
}

// VerifyResponse checks a provider's signed response and returns who signed in. Besides what
// gosaml2 verifies (signature against the provider's certificates, issuer, destination, recipient,
// status), it requires the response to answer the request this sign-in made (InResponseTo), to carry
// exactly one assertion that is current and addressed to this service, and it never trusts a panic
// in parsing hostile XML to stay inside the library.
//
// A SAML assertion carries no `email_verified`: the provider the tenant chose signed it, and the
// tenant's domain proof (checked by the caller) is what says the tenant may speak for that address.
func (c *SamlConfig) VerifyResponse(sp *saml2.SAMLServiceProvider, encoded, requestID string) (who *VerifiedIdentity, err error) {
	defer func() {
		if r := recover(); r != nil {
			who, err = nil, fmt.Errorf("the response could not be parsed")
		}
	}()
	resp, err := sp.ValidateEncodedResponse(encoded)
	if err != nil {
		return nil, err
	}
	if requestID == "" || resp.InResponseTo != requestID {
		return nil, errors.New("the response does not answer this sign-in")
	}
	if len(resp.Assertions) != 1 {
		return nil, errors.New("the response must carry exactly one assertion")
	}
	a := resp.Assertions[0]
	if a.Subject == nil || a.Subject.SubjectConfirmation == nil || a.Subject.SubjectConfirmation.SubjectConfirmationData == nil {
		return nil, errors.New("the assertion has no subject confirmation")
	}
	if got := a.Subject.SubjectConfirmation.SubjectConfirmationData.InResponseTo; got != requestID {
		return nil, errors.New("the assertion does not answer this sign-in")
	}
	if a.Conditions == nil || len(a.Conditions.AudienceRestrictions) == 0 {
		return nil, errors.New("the assertion names no audience")
	}
	warn, err := sp.VerifyAssertionConditions(&a)
	if err != nil {
		return nil, err
	}
	if warn.InvalidTime {
		return nil, errors.New("the assertion is not valid at this time")
	}
	if warn.NotInAudience {
		return nil, errors.New("the assertion is not addressed to this service")
	}
	return c.identityFromAssertion(&a)
}

func (c *SamlConfig) identityFromAssertion(a *types.Assertion) (*VerifiedIdentity, error) {
	if a.Subject == nil || a.Subject.NameID == nil || strings.TrimSpace(a.Subject.NameID.Value) == "" {
		return nil, errors.New("the assertion names no subject")
	}
	attrs := map[string][]string{}
	if a.AttributeStatement != nil {
		for _, at := range a.AttributeStatement.Attributes {
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
