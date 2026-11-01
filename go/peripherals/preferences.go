// Per-user platform preferences — today just locale, the backend half of i18n: which language a
// user's UI chrome should render in. Stored server-side (not just client localStorage) so it
// follows the user across devices/logins, same reasoning as user_roles living in Postgres rather
// than on the JWT.
package peripherals

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const DefaultLocale = "en"

func GetLocale(ctx context.Context, db DBTX, tenantID, userID uuid.UUID) (string, error) {
	var locale string
	err := db.QueryRow(ctx, "SELECT locale FROM user_preferences WHERE tenant_id = $1 AND user_id = $2",
		tenantID, userID).Scan(&locale)
	if err != nil {
		if err == pgx.ErrNoRows {
			return DefaultLocale, nil
		}
		return "", err
	}
	return locale, nil
}

func SetLocale(ctx context.Context, db DBTX, tenantID, userID uuid.UUID, locale string) error {
	_, err := db.Exec(ctx,
		`INSERT INTO user_preferences (tenant_id, user_id, locale, updated_at) VALUES ($1, $2, $3, now())
		 ON CONFLICT (tenant_id, user_id) DO UPDATE SET locale = $3, updated_at = now()`,
		tenantID, userID, locale,
	)
	return err
}
