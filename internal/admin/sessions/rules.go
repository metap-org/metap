package sessions

import (
	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/auth/identity"
)

// view is the sessions as the caller sees them: times in UTC, and exactly the session the request
// itself is using marked current. Pure: no I/O.
func view(rows []identity.Session, currentSessionID string) []Session {
	out := make([]Session, 0, len(rows))
	for _, s := range rows {
		out = append(out, Session{
			ID: s.ID.String(), Current: s.ID.String() == currentSessionID, TenantID: s.TenantID.String(),
			TenantName: s.TenantName, CreatedAt: s.CreatedAt.UTC(), LastSeenAt: s.LastSeenAt.UTC(),
			ExpiresAt: s.ExpiresAt.UTC(), IP: s.IP, UserAgent: s.UserAgent,
		})
	}
	return out
}

// parseSessionID reads a session id from the request; false when it cannot be one (the caller
// answers "not found", the same as for an id that is not theirs).
func parseSessionID(raw string) (uuid.UUID, bool) {
	id, err := uuid.Parse(raw)
	return id, err == nil
}

// keepSession is the session "sign out everywhere else" must keep: the calling one. A caller that is
// not a session (an API key, a service token) has none, so uuid.Nil keeps nothing and ends them all.
func keepSession(currentSessionID string) uuid.UUID {
	keep, _ := uuid.Parse(currentSessionID)
	return keep
}
