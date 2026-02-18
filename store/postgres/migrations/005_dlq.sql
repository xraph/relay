-- 005_dlq.sql: Dead letter queue table.
CREATE TABLE IF NOT EXISTS relay_dlq (
    id               TEXT PRIMARY KEY,
    delivery_id      TEXT NOT NULL,
    event_id         TEXT NOT NULL,
    endpoint_id      TEXT NOT NULL,
    event_type       TEXT NOT NULL,
    tenant_id        TEXT NOT NULL,
    url              TEXT NOT NULL,
    payload          JSONB NOT NULL,
    error            TEXT NOT NULL DEFAULT '',
    attempt_count    INTEGER NOT NULL DEFAULT 0,
    last_status_code INTEGER NOT NULL DEFAULT 0,
    replayed_at      TIMESTAMPTZ,
    failed_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_relay_dlq_tenant ON relay_dlq (tenant_id);
CREATE INDEX IF NOT EXISTS idx_relay_dlq_endpoint ON relay_dlq (endpoint_id);
CREATE INDEX IF NOT EXISTS idx_relay_dlq_failed ON relay_dlq (failed_at DESC);
