package tenants

import (
	"net/http"
	"strings"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/tenancy"
)

// AuditEntity is the audit-trail entity tenant administration records under.
const AuditEntity = "control.tenants"

const (
	maxNameLen  = 200
	maxTierLen  = 40
	defaultTier = "trial"
)

func invalid(fe map[string][]string) error {
	return &crud.ServiceError{Status: http.StatusBadRequest, Code: errcode.ValidationFailed, Message: "Invalid tenant.", FieldErrors: fe}
}

// newTenant checks a creation request and returns the trimmed name and the tier (the default when
// none is given), or the 400 naming every field that is wrong. Pure.
func newTenant(name, tier string) (string, string, error) {
	name, tier = strings.TrimSpace(name), strings.TrimSpace(tier)
	if tier == "" {
		tier = defaultTier
	}
	fe := map[string][]string{}
	if name == "" || len(name) > maxNameLen {
		fe["name"] = []string{"A name of 1 to 200 characters is required."}
	}
	if len(tier) > maxTierLen {
		fe["tier"] = []string{"At most 40 characters."}
	}
	if len(fe) > 0 {
		return "", "", invalid(fe)
	}
	return name, tier, nil
}

// patch checks an update (nil = not asked) and returns the trimmed values and what to audit against
// the tenant as it was, or the 400 for the first bad field. Pure.
func patch(before *tenancy.TenantDetail, name, tier *string) (*string, *string, map[string][2]any, error) {
	var n, t *string
	diff := map[string][2]any{}
	if name != nil {
		v := strings.TrimSpace(*name)
		if v == "" || len(v) > maxNameLen {
			return nil, nil, nil, invalid(map[string][]string{"name": {"A name of 1 to 200 characters is required."}})
		}
		n = &v
		diff["name"] = [2]any{before.Name, v}
	}
	if tier != nil {
		v := strings.TrimSpace(*tier)
		if v == "" || len(v) > maxTierLen {
			return nil, nil, nil, invalid(map[string][]string{"tier": {"A tier of 1 to 40 characters is required."}})
		}
		t = &v
		diff["tier"] = [2]any{before.Tier, v}
	}
	return n, t, diff, nil
}

// view is a tenant as the API shows it. Pure.
func view(d tenancy.TenantDetail, members int32) *Tenant {
	t := &Tenant{
		Id: d.ID.String(), Name: d.Name, Tier: d.Tier, Status: d.Status.String(), CreatedAt: kernel.FormatTime(&d.CreatedAt),
		TrialExpiresAt: kernel.FormatTime(d.TrialExpiresAt), DeletionRequestedAt: kernel.FormatTime(d.DeletionRequestedAt),
		DeletionEffectiveAt: kernel.FormatTime(d.DeletionEffectiveAt), MemberCount: members,
	}
	if d.Strategy.Kind == tenancy.StrategyDedicatedDb {
		t.Strategy = "dedicated_db"
	} else {
		t.Strategy = "schema"
	}
	if d.Product != nil {
		t.Product = *d.Product
	}
	return t
}
