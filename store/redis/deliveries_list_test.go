package redis_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
	redisstore "github.com/xraph/relay/store/redis"
)

func enqueueN(t *testing.T, s *redisstore.Store, tenant string, n int) []*delivery.Delivery {
	t.Helper()
	base := time.Now().UTC().Add(-time.Hour)
	out := make([]*delivery.Delivery, n)
	for i := range n {
		d := &delivery.Delivery{Entity: entity.New(), ID: id.NewDeliveryID(), EventID: id.NewEventID(),
			EndpointID: id.NewEndpointID(), EventType: "invoice.paid", TenantID: tenant,
			State: delivery.StatePending, MaxAttempts: 3, NextAttemptAt: base.Add(time.Hour * 2)}
		d.CreatedAt = base.Add(time.Duration(i) * time.Second)
		if err := s.Enqueue(context.Background(), d); err != nil {
			t.Fatal(err)
		}
		out[i] = d
	}
	return out
}

// With a window smaller than the log, a filter that matches nothing cannot
// honestly say "none". It says it stopped, and the cursor carries on until
// the log is exhausted.
func TestListDeliveriesSaysWhenItStoppedShort(t *testing.T) {
	s := openRedisStore(t, startRedis(t))
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	redisstore.SetScanWindow(s, 3)
	enqueueN(t, s, "t-window", 5)

	q := delivery.Query{EventType: "nothing.matches"}
	first, err := s.ListDeliveries(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if first.Complete || first.NextCursor == "" || len(first.Deliveries) != 0 {
		t.Fatalf("first page (complete %v, cursor %q, %d rows), want an incomplete empty page with a cursor",
			first.Complete, first.NextCursor, len(first.Deliveries))
	}
	q.Cursor = first.NextCursor
	for range 10 {
		p, err := s.ListDeliveries(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Deliveries) != 0 {
			t.Fatalf("a filter matching nothing returned %d rows", len(p.Deliveries))
		}
		if p.NextCursor == "" {
			if !p.Complete {
				t.Fatal("the last page has no cursor and still says incomplete")
			}
			return
		}
		q.Cursor = p.NextCursor
	}
	t.Fatal("never reached the end of the log")
}

// Following cursors through incomplete pages still yields every match once.
func TestListDeliveriesAcrossShortWindowsFindsEveryMatch(t *testing.T) {
	s := openRedisStore(t, startRedis(t))
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	redisstore.SetScanWindow(s, 2)
	enqueueN(t, s, "t-other", 3)
	want := enqueueN(t, s, "t-mine", 3)
	enqueueN(t, s, "t-other", 3)

	seen := map[string]int{}
	q := delivery.Query{TenantID: "t-mine", Limit: 2}
	for range 20 {
		p, err := s.ListDeliveries(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range p.Deliveries {
			seen[d.ID.String()]++
		}
		if p.NextCursor == "" {
			break
		}
		q.Cursor = p.NextCursor
	}
	for _, d := range want {
		if seen[d.ID.String()] != 1 {
			t.Errorf("delivery %s seen %d times, want once", d.ID, seen[d.ID.String()])
		}
	}
	if len(seen) != len(want) {
		t.Errorf("saw %d deliveries, want %d", len(seen), len(want))
	}
}

func TestListDeliveriesRefusesBeforeMigrate(t *testing.T) {
	s := openRedisStore(t, startRedis(t))
	if _, err := s.ListDeliveries(context.Background(), delivery.Query{}); !errors.Is(err, redisstore.ErrDeliveryIndexNotBuilt) {
		t.Fatalf("got %v, want ErrDeliveryIndexNotBuilt", err)
	}
}

// A delivery written by an older version is in its endpoint's index only and
// has no event type or tenant. Migrate puts it in the log and fills both.
func TestMigrateBackfillsOldDeliveries(t *testing.T) {
	connStr := startRedis(t)
	s := openRedisStore(t, connStr)
	raw := rawRedis(t, connStr)
	ctx := context.Background()

	evt := &event.Event{Entity: entity.New(), ID: id.NewEventID(), Type: "invoice.paid",
		TenantID: "t-old", Data: map[string]any{"n": 1}}
	if err := s.CreateEvent(ctx, evt); err != nil {
		t.Fatal(err)
	}
	delID, epID := id.NewDeliveryID().String(), id.NewEndpointID().String()
	created := time.Now().UTC().Add(-time.Hour)
	old, _ := json.Marshal(map[string]any{
		"id": delID, "event_id": evt.ID.String(), "endpoint_id": epID, "state": "delivered",
		"next_attempt_at": created, "created_at": created, "updated_at": created,
	})
	if err := raw.Set(ctx, "relay:del:"+delID, old, 0).Err(); err != nil {
		t.Fatal(err)
	}
	if err := raw.ZAdd(ctx, "relay:z:del:ep:"+epID, redisZ(created, delID)).Err(); err != nil {
		t.Fatal(err)
	}

	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := s.ListDeliveries(ctx, delivery.Query{TenantID: "t-old"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Deliveries) != 1 || p.Deliveries[0].ID.String() != delID || p.Deliveries[0].EventType != "invoice.paid" {
		t.Fatalf("got %v, want the old delivery with its event type filled", p.Deliveries)
	}
}

func redisZ(at time.Time, member string) goredis.Z {
	return goredis.Z{Score: float64(at.UnixNano()) / 1e9, Member: member}
}
