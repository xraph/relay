package postgres

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// runMigrations executes all SQL migration files in order.
func runMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	// Create migrations tracking table.
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS relay_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	// Read migration files.
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	// Sort by filename to ensure order.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		// Check if already applied.
		var applied bool
		err := pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM relay_migrations WHERE version = $1)`,
			entry.Name(),
		).Scan(&applied)
		if err != nil {
			return fmt.Errorf("check migration %s: %w", entry.Name(), err)
		}
		if applied {
			continue
		}

		// Read and execute.
		sql, readErr := migrationsFS.ReadFile("migrations/" + entry.Name())
		if readErr != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), readErr)
		}

		tx, txErr := pool.Begin(ctx)
		if txErr != nil {
			return fmt.Errorf("begin tx for %s: %w", entry.Name(), txErr)
		}

		if _, execErr := tx.Exec(ctx, string(sql)); execErr != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("execute migration %s: %w", entry.Name(), execErr)
		}

		if _, execErr := tx.Exec(ctx,
			`INSERT INTO relay_migrations (version) VALUES ($1)`,
			entry.Name(),
		); execErr != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("record migration %s: %w", entry.Name(), execErr)
		}

		if commitErr := tx.Commit(ctx); commitErr != nil {
			return fmt.Errorf("commit migration %s: %w", entry.Name(), commitErr)
		}
	}

	return nil
}
