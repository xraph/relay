-- 004_deliveries.sql: Webhook deliveries table.
CREATE TABLE IF NOT EXISTS relay_deliveries (
    id               TEXT PRIMARY KEY,
    event_id         TEXT NOT NULL REFERENCES relay_events(id),
    endpoint_id      TEXT NOT NULL REFERENCES relay_endpoints(id),
    state            TEXT NOT NULL DEFAULT 'pending',
    attempt_count    INTEGER NOT NULL DEFAULT 0,
    max_attempts     INTEGER NOT NULL DEFAULT 5,
    next_attempt_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_error       TEXT NOT NULL DEFAULT '',
    last_status_code INTEGER NOT NULL DEFAULT 0,
    last_response    TEXT NOT NULL DEFAULT '',
    last_latency_ms  INTEGER NOT NULL DEFAULT 0,
    completed_at     TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Critical index for the Dequeue hot path: pending deliveries ready for attempt.
CREATE INDEX IF NOT EXISTS idx_relay_deliveries_pending ON relay_deliveries (next_attempt_at)
    WHERE state = 'pending';

CREATE INDEX IF NOT EXISTS idx_relay_deliveries_endpoint ON relay_deliveries (endpoint_id);
CREATE INDEX IF NOT EXISTS idx_relay_deliveries_event ON relay_deliveries (event_id);
