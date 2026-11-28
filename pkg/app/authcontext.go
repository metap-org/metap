// AUTH_CONTEXT_ENTITY: policies may read, as attributes of the caller, the fields of the record of
// one entity whose userId is the caller's.

package app

import (
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/metap-org/metap/internal/authctx"
	"github.com/metap-org/metap/internal/httpapi"
	"github.com/metap-org/metap/pkg/config"
	"github.com/metap-org/metap/pkg/metadata"
)

// maxAuthContextTTLSeconds also catches a negative setting, which wraps to a huge number.
const maxAuthContextTTLSeconds = 3600

// authContextEntity checks the setting against the entities this service stores; "" is off. A
// service that cannot use what it was told to must not start: policies written against the
// attributes would be judged without them.
func authContextEntity(name string, stored []metadata.EntityDefinition) (string, error) {
	if name == "" {
		return "", nil
	}
	i := slices.IndexFunc(stored, func(e metadata.EntityDefinition) bool { return e.Name == name })
	if i < 0 {
		return "", fmt.Errorf("AUTH_CONTEXT_ENTITY %q is not an entity this service stores", name)
	}
	if !slices.ContainsFunc(stored[i].Fields, func(f metadata.EntityField) bool { return f.Name == "userId" }) {
		return "", fmt.Errorf("AUTH_CONTEXT_ENTITY %q has no userId field to find the caller's record by", name)
	}
	return name, nil
}

// useAuthContext turns the setting on: authentication loads the attributes, and a write through the
// service drops what it cached about the member the record describes. A change made by another
// process shows within the cache's TTL (AUTH_CONTEXT_CACHE_TTL_SECONDS, 0 reads on every request).
func useAuthContext(state *httpapi.AppState, cfg *config.App, entity string) error {
	if entity == "" {
		return nil
	}
	if cfg.AuthContextCacheTTLSeconds > maxAuthContextTTLSeconds {
		return fmt.Errorf("AUTH_CONTEXT_CACHE_TTL_SECONDS must be between 0 and %d", maxAuthContextTTLSeconds)
	}
	ttl := time.Duration(cfg.AuthContextCacheTTLSeconds) * time.Second
	state.AuthContextEntity = entity
	state.ContextAttributesCache = authctx.NewContextAttributesCache(ttl)
	state.Crud = state.Crud.WithContextInvalidation(entity, state.ContextAttributesCache)
	slog.Info("auth context", "entity", entity, "cache_ttl", ttl.String())
	return nil
}
