package sqlite_test

import (
	"context"
	"testing"

	"github.com/xraph/grove/drivers/sqlitedriver"

	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
)

// Rows written before event_type and tenant_id existed get them from their
// event when the migration runs, or a tenant filter on the delivery log would
// hide every delivery older than the upgrade.
func TestMigrationBackfillsEventTypeAndTenantOntoOldDeliveries(t *testing.T) {
	s := openSqliteStore(t)
	ctx := context.Background()
	raw := s.DB().Driver().(*sqlitedriver.SqliteDB)

	evt := &event.Event{Entity: entity.New(), ID: id.NewEventID(), Type: "invoice.paid",
		TenantID: "tenant-backfill", Data: map[string]any{"n": 1}}
	if err := s.CreateEvent(ctx, evt); err != nil {
		t.Fatalf("create event: %v", err)
	}
	delID := id.NewDeliveryID()
	// Written the way an older version wrote it: no event_type, no tenant_id.
	if _, err := raw.Exec(ctx,
		"INSERT INTO relay_deliveries (id, event_id, endpoint_id, next_attempt_at) VALUES (?, ?, ?, CURRENT_TIMESTAMP)",
		delID.String(), evt.ID.String(), id.NewEndpointID().String()); err != nil {
		t.Fatalf("insert old-style delivery: %v", err)
	}
	// Forget the migration so Migrate runs it again, as an upgrade would.
	if _, err := raw.Exec(ctx, "DELETE FROM grove_migrations WHERE version = '20260923000001'"); err != nil {
		t.Fatalf("forget migration: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	got, err := s.GetDelivery(ctx, delID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.EventType != "invoice.paid" || got.TenantID != "tenant-backfill" {
		t.Errorf("backfilled (%q, %q), want (%q, %q)", got.EventType, got.TenantID, "invoice.paid", "tenant-backfill")
	}
}
