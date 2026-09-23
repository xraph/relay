package sqlite

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// Migrations is the grove migration group for the Relay store (SQLite).
var Migrations = migrate.NewGroup("relay")

func init() {
	Migrations.MustRegister(
		&migrate.Migration{
			Name:    "create_relay_event_types",
			Version: "20240101000001",
			Up: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `
CREATE TABLE IF NOT EXISTS relay_event_types (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL UNIQUE,
    description     TEXT NOT NULL DEFAULT '',
    group_name      TEXT NOT NULL DEFAULT '',
    schema          TEXT,
    schema_version  TEXT NOT NULL DEFAULT '',
    version         TEXT NOT NULL DEFAULT '',
    example         TEXT,
    is_deprecated   INTEGER NOT NULL DEFAULT 0,
    deprecated_at   TEXT,
    scope_app_id    TEXT NOT NULL DEFAULT '',
    metadata        TEXT NOT NULL DEFAULT '{}',
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at      TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_relay_event_types_group ON relay_event_types (group_name);
CREATE INDEX IF NOT EXISTS idx_relay_event_types_created ON relay_event_types (created_at);
`)
				return err
			},
			Down: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `DROP TABLE IF EXISTS relay_event_types`)
				return err
			},
		},
		&migrate.Migration{
			Name:    "create_relay_endpoints",
			Version: "20240101000002",
			Up: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `
CREATE TABLE IF NOT EXISTS relay_endpoints (
    id          TEXT PRIMARY KEY,
    tenant_id   TEXT NOT NULL DEFAULT '',
    url         TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    secret      TEXT NOT NULL DEFAULT '',
    event_types TEXT NOT NULL DEFAULT '[]',
    headers     TEXT NOT NULL DEFAULT '{}',
    enabled     INTEGER NOT NULL DEFAULT 1,
    rate_limit  INTEGER NOT NULL DEFAULT 0,
    metadata    TEXT NOT NULL DEFAULT '{}',
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_relay_endpoints_tenant ON relay_endpoints (tenant_id);
CREATE INDEX IF NOT EXISTS idx_relay_endpoints_tenant_enabled ON relay_endpoints (tenant_id, enabled);
`)
				return err
			},
			Down: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `DROP TABLE IF EXISTS relay_endpoints`)
				return err
			},
		},
		&migrate.Migration{
			Name:    "create_relay_events",
			Version: "20240101000003",
			Up: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `
CREATE TABLE IF NOT EXISTS relay_events (
    id              TEXT PRIMARY KEY,
    type            TEXT NOT NULL DEFAULT '',
    tenant_id       TEXT NOT NULL DEFAULT '',
    data            TEXT,
    idempotency_key TEXT NOT NULL DEFAULT '',
    scope_app_id    TEXT NOT NULL DEFAULT '',
    scope_org_id    TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at      TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_relay_events_tenant ON relay_events (tenant_id);
CREATE INDEX IF NOT EXISTS idx_relay_events_type ON relay_events (type);
CREATE UNIQUE INDEX IF NOT EXISTS idx_relay_events_idempotency ON relay_events (idempotency_key) WHERE idempotency_key != '';
`)
				return err
			},
			Down: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `DROP TABLE IF EXISTS relay_events`)
				return err
			},
		},
		&migrate.Migration{
			Name:    "create_relay_deliveries",
			Version: "20240101000004",
			Up: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `
CREATE TABLE IF NOT EXISTS relay_deliveries (
    id              TEXT PRIMARY KEY,
    event_id        TEXT NOT NULL DEFAULT '',
    endpoint_id     TEXT NOT NULL DEFAULT '',
    state           TEXT NOT NULL DEFAULT 'pending',
    attempt_count   INTEGER NOT NULL DEFAULT 0,
    max_attempts    INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT NOT NULL DEFAULT (datetime('now')),
    last_error      TEXT NOT NULL DEFAULT '',
    last_status_code INTEGER NOT NULL DEFAULT 0,
    last_response   TEXT NOT NULL DEFAULT '',
    last_latency_ms INTEGER NOT NULL DEFAULT 0,
    completed_at    TEXT,
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at      TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_relay_deliveries_pending ON relay_deliveries (next_attempt_at) WHERE state = 'pending';
CREATE INDEX IF NOT EXISTS idx_relay_deliveries_event ON relay_deliveries (event_id);
CREATE INDEX IF NOT EXISTS idx_relay_deliveries_endpoint ON relay_deliveries (endpoint_id);
`)
				return err
			},
			Down: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `DROP TABLE IF EXISTS relay_deliveries`)
				return err
			},
		},
		&migrate.Migration{
			Name:    "create_relay_dlq",
			Version: "20240101000005",
			Up: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `
CREATE TABLE IF NOT EXISTS relay_dlq (
    id              TEXT PRIMARY KEY,
    delivery_id     TEXT NOT NULL DEFAULT '',
    event_id        TEXT NOT NULL DEFAULT '',
    endpoint_id     TEXT NOT NULL DEFAULT '',
    tenant_id       TEXT NOT NULL DEFAULT '',
    event_type      TEXT NOT NULL DEFAULT '',
    url             TEXT NOT NULL DEFAULT '',
    payload         TEXT,
    error           TEXT NOT NULL DEFAULT '',
    attempt_count   INTEGER NOT NULL DEFAULT 0,
    last_status_code INTEGER NOT NULL DEFAULT 0,
    replayed_at     TEXT,
    failed_at       TEXT NOT NULL DEFAULT (datetime('now')),
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at      TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_relay_dlq_tenant ON relay_dlq (tenant_id);
CREATE INDEX IF NOT EXISTS idx_relay_dlq_failed ON relay_dlq (failed_at);
`)
				return err
			},
			Down: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `DROP TABLE IF EXISTS relay_dlq`)
				return err
			},
		},
		// See the postgres migration of the same name.
		&migrate.Migration{
			Name:    "relay_deliveries_event_type_tenant",
			Version: "20260923000001",
			Up: func(ctx context.Context, exec migrate.Executor) error {
				// sqlite has no ADD COLUMN IF NOT EXISTS. Checking first keeps
				// the migration safe to run again, which the backfill test
				// relies on.
				for _, col := range []string{"event_type", "tenant_id"} {
					has, err := sqliteHasColumn(ctx, exec, "relay_deliveries", col)
					if err != nil {
						return err
					}
					if !has {
						if _, err := exec.Exec(ctx, "ALTER TABLE relay_deliveries ADD COLUMN "+col+" TEXT NOT NULL DEFAULT ''"); err != nil {
							return err
						}
					}
				}
				_, err := exec.Exec(ctx, `
UPDATE relay_deliveries
   SET event_type = COALESCE((SELECT type      FROM relay_events WHERE relay_events.id = relay_deliveries.event_id), ''),
       tenant_id  = COALESCE((SELECT tenant_id FROM relay_events WHERE relay_events.id = relay_deliveries.event_id), '')
 WHERE event_type = '';

CREATE INDEX IF NOT EXISTS idx_relay_deliveries_created ON relay_deliveries (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_relay_deliveries_tenant  ON relay_deliveries (tenant_id, created_at DESC);
`)
				return err
			},
			Down: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `
DROP INDEX IF EXISTS idx_relay_deliveries_created;
DROP INDEX IF EXISTS idx_relay_deliveries_tenant;
ALTER TABLE relay_deliveries DROP COLUMN event_type;
ALTER TABLE relay_deliveries DROP COLUMN tenant_id;
`)
				return err
			},
		},
		// One row per HTTP attempt, written by the engine. Listed per
		// delivery by attempt number and purged by age.
		&migrate.Migration{
			Name:    "create_relay_delivery_attempts",
			Version: "20260923000002",
			Up: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `
CREATE TABLE IF NOT EXISTS relay_delivery_attempts (
    id              TEXT PRIMARY KEY,
    delivery_id     TEXT NOT NULL,
    attempt_num     INTEGER NOT NULL,
    status_code     INTEGER NOT NULL DEFAULT 0,
    error           TEXT NOT NULL DEFAULT '',
    response        TEXT NOT NULL DEFAULT '',
    latency_ms      INTEGER NOT NULL DEFAULT 0,
    outcome         TEXT NOT NULL,
    next_attempt_at TEXT,
    attempted_at    TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_relay_attempts_delivery ON relay_delivery_attempts (delivery_id, attempt_num);
CREATE INDEX IF NOT EXISTS idx_relay_attempts_attempted ON relay_delivery_attempts (attempted_at);
`)
				return err
			},
			Down: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `DROP TABLE IF EXISTS relay_delivery_attempts`)
				return err
			},
		},
	)
}

// sqliteHasColumn reports whether table already has col.
func sqliteHasColumn(ctx context.Context, exec migrate.Executor, table, col string) (bool, error) {
	rows, err := exec.Query(ctx, "SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?", table, col)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	var n int
	if rows.Next() {
		if err := rows.Scan(&n); err != nil {
			return false, err
		}
	}
	return n > 0, rows.Err()
}
