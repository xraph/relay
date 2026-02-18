-- 003_events.sql: Webhook events table.
CREATE TABLE IF NOT EXISTS relay_events (
    id              TEXT PRIMARY KEY,
    type            TEXT NOT NULL,
    tenant_id       TEXT NOT NULL,
    data            JSONB NOT NULL,
    idempotency_key TEXT,
    scope_app_id    TEXT NOT NULL DEFAULT '',
    scope_org_id    TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_relay_events_idempotency ON relay_events (idempotency_key) WHERE idempotency_key IS NOT NULL AND idempotency_key != '';
CREATE INDEX IF NOT EXISTS idx_relay_events_tenant ON relay_events (tenant_id);
CREATE INDEX IF NOT EXISTS idx_relay_events_type ON relay_events (type);
CREATE INDEX IF NOT EXISTS idx_relay_events_created ON relay_events (created_at DESC);
