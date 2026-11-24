package apikeys

import (
	"fmt"
	"strings"
	"time"

	"github.com/metap-org/metap/pkg/auth/identity"
)

// MaxLifetimeDays bounds an expiring key's lifetime.
const MaxLifetimeDays = 3650

// maxNameLength is the longest key name.
const maxNameLength = 200

// nameProblem says what is wrong with a key's name, or "" when it is fine.
func nameProblem(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxNameLength {
		return "a key name of 1-200 characters is required"
	}
	return ""
}

// expiry decides when a key issued at now expires: days is what was asked (0 = never), tenantMax the
// tenant's own cap in days (0 = none; roadmap 115 M11: with a cap a key must expire, and no later
// than the cap). It returns the moment, or the sentence that says why the ask is refused. Pure.
func expiry(days, tenantMax int, now time.Time) (*time.Time, string) {
	if days < 0 || days > MaxLifetimeDays {
		return nil, "expiresInDays must be between 0 and 3650"
	}
	if tenantMax > 0 && (days == 0 || days > tenantMax) {
		return nil, fmt.Sprintf("This tenant limits API keys to %d days; set expiresInDays between 1 and %d.", tenantMax, tenantMax)
	}
	if days == 0 {
		return nil, ""
	}
	t := now.Add(time.Duration(days) * 24 * time.Hour)
	return &t, ""
}

func formatTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func view(k identity.APIKey) *ApiKey {
	return &ApiKey{Id: k.ID.String(), UserId: k.MemberID.String(), Name: k.Name, Prefix: k.Prefix,
		ExpiresAt: formatTime(k.ExpiresAt), RevokedAt: formatTime(k.RevokedAt), LastUsedAt: formatTime(k.LastUsedAt),
		CreatedAt: k.CreatedAt.UTC().Format(time.RFC3339)}
}
