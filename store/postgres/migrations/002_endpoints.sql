-- 002_endpoints.sql: Webhook endpoints table.
CREATE TABLE IF NOT EXISTS relay_endpoints (
    id          TEXT PRIMARY KEY,
    tenant_id   TEXT NOT NULL,
    url         TEXT NOT NULL,
    secret      TEXT NOT NULL,
    event_types TEXT[] NOT NULL DEFAULT '{}',
    headers     JSONB NOT NULL DEFAULT '{}',
    enabled     BOOLEAN NOT NULL DEFAULT TRUE,
    rate_limit  INTEGER NOT NULL DEFAULT 0,
    metadata    JSONB,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_relay_endpoints_tenant ON relay_endpoints (tenant_id);
CREATE INDEX IF NOT EXISTS idx_relay_endpoints_tenant_enabled ON relay_endpoints (tenant_id, enabled);
