-- 001_catalog.sql: Event type catalog table.
CREATE TABLE IF NOT EXISTS relay_event_types (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL UNIQUE,
    description   TEXT NOT NULL DEFAULT '',
    "group"       TEXT NOT NULL DEFAULT '',
    schema        JSONB,
    schema_version TEXT NOT NULL DEFAULT '',
    version       TEXT NOT NULL DEFAULT '',
    example       JSONB,
    deprecated    BOOLEAN NOT NULL DEFAULT FALSE,
    deprecated_at TIMESTAMPTZ,
    scope_app_id  TEXT NOT NULL DEFAULT '',
    metadata      JSONB,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_relay_event_types_name ON relay_event_types (name);
CREATE INDEX IF NOT EXISTS idx_relay_event_types_group ON relay_event_types ("group");
