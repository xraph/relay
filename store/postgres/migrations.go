package postgres

import (
	"context"

	"github.com/xraph/grove/migrate"
)

// Migrations is the grove migration group for the Relay store.
// It can be registered with the grove extension for orchestrated migration
// management (locking, version tracking, rollback support).
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
    schema          JSONB,
    schema_version  TEXT NOT NULL DEFAULT '',
    version         TEXT NOT NULL DEFAULT '',
    example         JSONB,
    is_deprecated   BOOLEAN NOT NULL DEFAULT FALSE,
    deprecated_at   TIMESTAMPTZ,
    scope_app_id    TEXT NOT NULL DEFAULT '',
    metadata        JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
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
    event_types TEXT[] NOT NULL DEFAULT '{}',
    headers     JSONB NOT NULL DEFAULT '{}',
    enabled     BOOLEAN NOT NULL DEFAULT TRUE,
    rate_limit  INT NOT NULL DEFAULT 0,
    metadata    JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_relay_endpoints_tenant ON relay_endpoints (tenant_id);
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
    data            JSONB,
    idempotency_key TEXT NOT NULL DEFAULT '',
    scope_app_id    TEXT NOT NULL DEFAULT '',
    scope_org_id    TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
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
    attempt_count   INT NOT NULL DEFAULT 0,
    max_attempts    INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_error      TEXT NOT NULL DEFAULT '',
    last_status_code INT NOT NULL DEFAULT 0,
    last_response   TEXT NOT NULL DEFAULT '',
    last_latency_ms INT NOT NULL DEFAULT 0,
    completed_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
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
    payload         JSONB,
    error           TEXT NOT NULL DEFAULT '',
    attempt_count   INT NOT NULL DEFAULT 0,
    last_status_code INT NOT NULL DEFAULT 0,
    replayed_at     TIMESTAMPTZ,
    failed_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_relay_dlq_tenant ON relay_dlq (tenant_id);
`)
				return err
			},
			Down: func(ctx context.Context, exec migrate.Executor) error {
				_, err := exec.Exec(ctx, `DROP TABLE IF EXISTS relay_dlq`)
				return err
			},
		},
	)
}
