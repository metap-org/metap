// db-migrate runs the platform's own migrations (migrations/*.sql, embedded). Nothing else applies
// them: no service migrates when it boots. A product's entity tables are migrated with the
// service's own `migrate` command, which applies these first (docs/migrations.md).
//
//	db-migrate [up]                 apply what is pending
//	db-migrate status               what is pending (reads only)
//	db-migrate down [--steps N]     print the SQL that undoes the newest N
//	db-migrate down --yes           and run it
//
// DATABASE_URL names the database; MIGRATIONS_DIR reads the files from disk instead.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"

	"github.com/metap-org/metap/migrations"
	"github.com/metap-org/metap/pkg/config"
	"github.com/metap-org/metap/pkg/db"
	"github.com/metap-org/metap/pkg/db/migrate"
	"github.com/metap-org/metap/pkg/kit/standard"
)

func main() {
	config.InitLogging()
	if err := run(context.Background(), os.Args[1:]); err != nil {
		slog.Error("db-migrate failed", standard.LogError, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	command := "up"
	if len(args) > 0 {
		command, args = args[0], args[1:]
	}
	flags := flag.NewFlagSet("db-migrate "+command, flag.ContinueOnError)
	steps := flags.Int("steps", 1, "migrations to undo")
	yes := flags.Bool("yes", false, "run the rollback instead of only printing it")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if command != "up" && command != "status" && command != "down" {
		return fmt.Errorf("unknown command %q: use up, status or down", command)
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	pool, err := db.ConnectForMigrate(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connecting to postgres: %w", err)
	}
	defer pool.Close()
	var files fs.FS = migrations.FS
	if dir := os.Getenv("MIGRATIONS_DIR"); dir != "" {
		files = os.DirFS(dir)
	}

	switch command {
	case "status":
		if err := migrate.Require(ctx, pool, files); err != nil {
			return err
		}
		fmt.Println("up to date")
	case "down":
		done, err := migrate.Rollback(ctx, pool, files, *steps, func(targets []migrate.Migration) error {
			for _, m := range targets {
				fmt.Printf("-- ===== %s =====\n%s\n", m.DownFilename, strings.TrimSpace(m.Down))
			}
			if !*yes {
				return errors.New("nothing was run: repeat with --yes to run the SQL above")
			}
			return nil
		})
		if err != nil {
			return err
		}
		slog.Info("rolled back", "versions", done)
	default:
		result, err := migrate.RunFS(ctx, pool, files)
		if err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
		slog.Info("done", "applied", result.Applied)
	}
	return nil
}
