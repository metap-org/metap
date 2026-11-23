package main

import (
	"context"
	"fmt"
	"github.com/metap-org/metap/pkg/permission/policypg"
	"os"

	"github.com/google/uuid"
	"github.com/metap-org/metap/pkg/app"
	"github.com/metap-org/metap/pkg/bootstrap"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
)

// applyBootstrap applies a JSON bootstrap declaration to one tenant. Entity names in grants are
// resolved against the entity files given after the declaration (wildcards need them); a product's
// own bootstrap binary (app.BootstrapMain) is the way to apply code-declared parts.
func applyBootstrap(args []string) error {
	if len(args) < 4 {
		fmt.Fprintln(os.Stderr, "Usage: dev-tools apply-bootstrap <tenantId> <bootstrap.json> [entity.json ...]")
		os.Exit(1)
	}
	ctx := context.Background()
	tenantID, err := parseUUID(args[2])
	if err != nil {
		return err
	}
	decl, err := bootstrap.LoadFile(args[3])
	if err != nil {
		return err
	}
	registry := metadata.NewMetadataRegistry()
	for _, path := range args[4:] {
		def, err := loadEntityFromFile(path)
		if err != nil {
			return err
		}
		if err := registry.Register(def); err != nil {
			return err
		}
	}
	pool, err := connectShared(ctx, 2)
	if err != nil {
		return err
	}
	defer pool.Close()
	router := routerFor(pool)
	permissions := permission.NewPermissionService(policypg.NewPostgresPolicyStore(router))
	production := os.Getenv("NODE_ENV") == "production"
	return app.ApplyBootstrap(ctx, registry, permissions, router, []uuid.UUID{tenantID}, decl, production, "dev-tools")
}
