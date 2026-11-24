package sessions

import "time"

// Session is one of the caller's login sessions. The name and tags are what the OpenAPI document
// shows, so they stay as they were.
type Session struct {
	ID         string    `json:"id" format:"uuid"`
	Current    bool      `json:"current" doc:"The session this request is using"`
	TenantID   string    `json:"tenantId" format:"uuid"`
	TenantName string    `json:"tenantName"`
	CreatedAt  time.Time `json:"createdAt"`
	LastSeenAt time.Time `json:"lastSeenAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
	IP         string    `json:"ip"`
	UserAgent  string    `json:"userAgent"`
}
