package audit

import (
	"encoding/base64"
	"strings"
	"time"

	"github.com/google/uuid"
)

// EncodeCursor/DecodeCursor: an opaque keyset position, "<RFC3339Nano>|<uuid>" in base64url, so a
// client never builds one itself.
func EncodeCursor(c Cursor) string {
	return base64.RawURLEncoding.EncodeToString([]byte(c.OccurredAt.UTC().Format(time.RFC3339Nano) + "|" + c.ID.String()))
}

func DecodeCursor(raw string) (Cursor, bool) {
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return Cursor{}, false
	}
	at, id, found := strings.Cut(string(b), "|")
	if !found {
		return Cursor{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return Cursor{}, false
	}
	u, err := uuid.Parse(id)
	if err != nil {
		return Cursor{}, false
	}
	return Cursor{OccurredAt: t, ID: u}, true
}
