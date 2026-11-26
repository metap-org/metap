// Request and response types of the admin services. Generated once from the gRPC API's
// protobuf messages when it was removed (roadmap 116); hand-maintained from here on.

package admin

import (
	"github.com/metap-org/metap/internal/admin/kernel"
)

// The audit page the user and role histories share lives in kernel (the roles slice cannot import admin).
type (
	AuditEvent             = kernel.AuditEvent
	CredentialCapabilities = kernel.CredentialCapabilities
	CredentialManager      = kernel.CredentialManager
	Empty                  = kernel.Empty
	ListUserAuditResponse  = kernel.ListUserAuditResponse
)
