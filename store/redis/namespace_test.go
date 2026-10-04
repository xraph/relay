package redis_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/xraph/grove/hook"
	"github.com/xraph/grove/kv"
	"github.com/xraph/grove/kv/drivers/redisdriver"
	"github.com/xraph/grove/kv/middleware"

	relay "github.com/xraph/relay"
	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
	redisstore "github.com/xraph/relay/store/redis"
)

// Two relay stores can share one redis when their kv stores carry different
// namespace hooks. Every command the store sends has to land under its own
// namespace: records, which go through kv, and also the indexes, the replay
// claim, the delivery queue and the migration markers, which use the raw client
// and which no hook ever sees unless relay resolves their keys itself.
//
// These tests drive the real redis driver against a throwaway container, and
// assert against the physical keyspace through a raw client. A store that
// reads and writes the same wrong key looks correct from its own side, so
// checking only what a store returns would pass the bug this guards against.

const (
	nsA = "app1"
	nsB = "app2"
	// sharedTenant is the tenant id both namespaces use, so that the only thing
	// keeping their data apart is the namespace.
	sharedTenant = "shared-tenant"
)

func namespacedStore(t *testing.T, connStr, ns string) *redisstore.Store {
	t.Helper()
	return openHookedRedisStore(t, connStr, middleware.NewNamespace(ns))
}

// seeded is what seedNamespace created, by id.
type seeded struct {
	enabledEP, disabledEP id.ID
	evt                   *event.Event
	del1, del2            *delivery.Delivery
	attempt               *delivery.Attempt
	dlq                   *dlq.Entry
}

// seedNamespace writes one of everything the store keeps, in tenant sharedTenant.
func seedNamespace(t *testing.T, s *redisstore.Store) *seeded {
	t.Helper()
	ctx := context.Background()
	out := &seeded{}

	enabled := createEndpoint(t, s, sharedTenant)
	out.enabledEP = enabled.ID

	off := &endpoint.Endpoint{
		Entity: entity.New(), ID: id.NewEndpointID(), TenantID: sharedTenant,
		URL: "https://receiver.example/off", Secret: "whsec_off", EventTypes: []string{"*"}, Enabled: false,
	}
	if err := s.CreateEndpoint(ctx, off); err != nil {
		t.Fatalf("create disabled endpoint: %v", err)
	}
	out.disabledEP = off.ID

	out.evt = &event.Event{
		Entity: entity.New(), ID: id.NewEventID(), Type: "invoice.paid", TenantID: sharedTenant,
		Data: map[string]any{"n": 1}, IdempotencyKey: "same-key-in-both-namespaces",
	}
	if err := s.CreateEvent(ctx, out.evt); err != nil {
		t.Fatalf("create event: %v", err)
	}

	due := time.Now().UTC().Add(-time.Minute)
	mk := func() *delivery.Delivery {
		return &delivery.Delivery{
			Entity: entity.New(), ID: id.NewDeliveryID(), EventID: out.evt.ID, EndpointID: enabled.ID,
			EventType: out.evt.Type, TenantID: sharedTenant, State: delivery.StatePending,
			MaxAttempts: 3, NextAttemptAt: due,
		}
	}
	out.del1, out.del2 = mk(), mk()
	if err := s.Enqueue(ctx, out.del1); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := s.EnqueueBatch(ctx, []*delivery.Delivery{out.del2}); err != nil {
		t.Fatalf("enqueue batch: %v", err)
	}

	out.attempt = &delivery.Attempt{
		ID: id.NewAttemptID(), DeliveryID: out.del1.ID, AttemptNum: 1, StatusCode: 500,
		Outcome: delivery.OutcomeRetry, AttemptedAt: time.Now().UTC().Add(-time.Hour),
	}
	if err := s.RecordAttempt(ctx, out.attempt); err != nil {
		t.Fatalf("record attempt: %v", err)
	}

	out.dlq = &dlq.Entry{
		Entity: entity.New(), ID: id.NewDLQID(), DeliveryID: out.del1.ID, EventID: out.evt.ID,
		EndpointID: enabled.ID, EventType: out.evt.Type, TenantID: sharedTenant,
		URL: "https://receiver.example/hook", Payload: []byte(`{"id":"inv_1"}`), Error: "boom",
		AttemptCount: 3, FailedAt: time.Now().UTC().Add(-time.Hour),
	}
	if err := s.Push(ctx, out.dlq); err != nil {
		t.Fatalf("push dlq: %v", err)
	}
	return out
}

func idStrings[T any](items []T, idOf func(T) id.ID) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, idOf(it).String())
	}
	slices.Sort(out)
	return out
}

func wantIDs(ids ...id.ID) []string {
	out := make([]string, 0, len(ids))
	for _, i := range ids {
		out = append(out, i.String())
	}
	slices.Sort(out)
	return out
}

func sameIDs(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func rawExists(t *testing.T, raw *goredis.Client, key string) bool {
	t.Helper()
	n, err := raw.Exists(context.Background(), key).Result()
	if err != nil {
		t.Fatalf("exists %s: %v", key, err)
	}
	return n == 1
}

// TestNamespacedStoresAreIsolated is the main proof. The subtests run in order
// and share the two stores, because each builds on what the one before left.
func TestNamespacedStoresAreIsolated(t *testing.T) {
	ctx := context.Background()
	connStr := startRedis(t)
	raw := rawRedis(t, connStr)
	a := namespacedStore(t, connStr, nsA)
	b := namespacedStore(t, connStr, nsB)

	for name, s := range map[string]*redisstore.Store{nsA: a, nsB: b} {
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("Migrate on namespace %s: %v", name, err)
		}
	}
	sa, sb := seedNamespace(t, a), seedNamespace(t, b)

	t.Run("every key carries its namespace", func(t *testing.T) {
		keys, err := raw.Keys(ctx, "*").Result()
		if err != nil {
			t.Fatal(err)
		}
		if len(keys) == 0 {
			t.Fatal("no keys at all")
		}
		for _, k := range keys {
			if !strings.HasPrefix(k, nsA+":") && !strings.HasPrefix(k, nsB+":") {
				t.Errorf("key %q is outside both namespaces", k)
			}
		}
		for _, k := range []string{
			nsA + ":relay:z:ep:tenant:" + sharedTenant,
			nsA + ":relay:z:del:pending",
			nsA + ":relay:s:ep:tenant:" + sharedTenant + ":enabled",
			nsA + ":relay:u:evt:idem:same-key-in-both-namespaces",
			nsA + ":relay:migrated:ep_all:built",
		} {
			if !rawExists(t, raw, k) {
				t.Errorf("expected key %q", k)
			}
		}
	})

	t.Run("endpoints", func(t *testing.T) {
		for _, c := range []struct {
			name string
			s    *redisstore.Store
			sd   *seeded
			ns   string
		}{{nsA, a, sa, nsA}, {nsB, b, sb, nsB}} {
			epID := func(e *endpoint.Endpoint) id.ID { return e.ID }
			byTenant, err := c.s.ListEndpoints(ctx, sharedTenant, endpoint.ListOpts{})
			if err != nil {
				t.Fatal(err)
			}
			sameIDs(t, c.name+" ListEndpoints(tenant)", idStrings(byTenant, epID), wantIDs(c.sd.enabledEP, c.sd.disabledEP))

			all, err := c.s.ListEndpoints(ctx, "", endpoint.ListOpts{})
			if err != nil {
				t.Fatal(err)
			}
			sameIDs(t, c.name+" ListEndpoints(every tenant)", idStrings(all, epID), wantIDs(c.sd.enabledEP, c.sd.disabledEP))

			resolved, err := c.s.Resolve(ctx, sharedTenant, "invoice.paid")
			if err != nil {
				t.Fatal(err)
			}
			sameIDs(t, c.name+" Resolve", idStrings(resolved, epID), wantIDs(c.sd.enabledEP))
		}
		if _, err := a.GetEndpoint(ctx, sb.enabledEP); !errors.Is(err, relay.ErrEndpointNotFound) {
			t.Errorf("app1 read app2's endpoint: err = %v, want not found", err)
		}
		if err := a.SetEnabled(ctx, sb.disabledEP, true); !errors.Is(err, relay.ErrEndpointNotFound) {
			t.Errorf("app1 enabled app2's endpoint: err = %v, want not found", err)
		}
	})

	t.Run("events and the idempotency key", func(t *testing.T) {
		// Both namespaces took the same idempotency key in seedNamespace, so
		// neither saw the other's. A repeat inside one is still a duplicate.
		dup := &event.Event{
			Entity: entity.New(), ID: id.NewEventID(), Type: "invoice.paid", TenantID: sharedTenant,
			IdempotencyKey: "same-key-in-both-namespaces",
		}
		if err := a.CreateEvent(ctx, dup); !errors.Is(err, relay.ErrDuplicateIdempotencyKey) {
			t.Errorf("repeat idempotency key in one namespace: err = %v, want duplicate", err)
		}
		evID := func(e *event.Event) id.ID { return e.ID }
		for _, c := range []struct {
			name string
			s    *redisstore.Store
			sd   *seeded
		}{{nsA, a, sa}, {nsB, b, sb}} {
			all, err := c.s.ListEvents(ctx, event.ListOpts{})
			if err != nil {
				t.Fatal(err)
			}
			sameIDs(t, c.name+" ListEvents", idStrings(all, evID), wantIDs(c.sd.evt.ID))
			byTenant, err := c.s.ListEventsByTenant(ctx, sharedTenant, event.ListOpts{})
			if err != nil {
				t.Fatal(err)
			}
			sameIDs(t, c.name+" ListEventsByTenant", idStrings(byTenant, evID), wantIDs(c.sd.evt.ID))
		}
		if _, err := a.GetEvent(ctx, sb.evt.ID); !errors.Is(err, relay.ErrEventNotFound) {
			t.Errorf("app1 read app2's event: err = %v, want not found", err)
		}
	})

	t.Run("catalog", func(t *testing.T) {
		reg := func(s *redisstore.Store, name string) {
			et := &catalog.EventType{
				Entity: entity.New(), ID: id.NewEventTypeID(),
				Definition: catalog.WebhookDefinition{Name: name, Group: "g"},
			}
			if err := s.RegisterType(ctx, et); err != nil {
				t.Fatalf("register %s: %v", name, err)
			}
		}
		reg(a, "only.in.a")
		reg(b, "only.in.b")
		if _, err := b.GetType(ctx, "only.in.a"); !errors.Is(err, relay.ErrEventTypeNotFound) {
			t.Errorf("app2 saw app1's event type: err = %v", err)
		}
		types, err := a.ListTypes(ctx, catalog.ListOpts{})
		if err != nil {
			t.Fatal(err)
		}
		if len(types) != 1 || types[0].Definition.Name != "only.in.a" {
			t.Errorf("app1 ListTypes = %d types, want just only.in.a", len(types))
		}
		matched, err := b.MatchTypes(ctx, "only.in.*")
		if err != nil {
			t.Fatal(err)
		}
		if len(matched) != 1 || matched[0].Definition.Name != "only.in.b" {
			t.Errorf("app2 MatchTypes = %d types, want just only.in.b", len(matched))
		}
	})

	t.Run("deliveries and the queue", func(t *testing.T) {
		delID := func(d *delivery.Delivery) id.ID { return d.ID }
		for _, c := range []struct {
			name string
			s    *redisstore.Store
			sd   *seeded
		}{{nsA, a, sa}, {nsB, b, sb}} {
			want := wantIDs(c.sd.del1.ID, c.sd.del2.ID)
			byEP, err := c.s.ListByEndpoint(ctx, c.sd.enabledEP, delivery.ListOpts{})
			if err != nil {
				t.Fatal(err)
			}
			sameIDs(t, c.name+" ListByEndpoint", idStrings(byEP, delID), want)
			byEvt, err := c.s.ListByEvent(ctx, c.sd.evt.ID)
			if err != nil {
				t.Fatal(err)
			}
			sameIDs(t, c.name+" ListByEvent", idStrings(byEvt, delID), want)
			page, err := c.s.ListDeliveries(ctx, delivery.Query{})
			if err != nil {
				t.Fatal(err)
			}
			sameIDs(t, c.name+" ListDeliveries", idStrings(page.Deliveries, delID), want)
			if n, cErr := c.s.CountPending(ctx); cErr != nil || n != 2 {
				t.Errorf("%s CountPending = %d, %v; want 2", c.name, n, cErr)
			}
		}
		if _, err := a.GetDelivery(ctx, sb.del1.ID); !errors.Is(err, relay.ErrDeliveryNotFound) {
			t.Errorf("app1 read app2's delivery: err = %v, want not found", err)
		}

		// Dequeue claims from this namespace's queue and no other.
		got, err := a.Dequeue(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		sameIDs(t, "app1 Dequeue", idStrings(got, delID), wantIDs(sa.del1.ID, sa.del2.ID))
		if n, _ := a.CountPending(ctx); n != 0 {
			t.Errorf("app1 CountPending after its dequeue = %d, want 0", n)
		}
		if n, _ := b.CountPending(ctx); n != 2 {
			t.Errorf("app2 CountPending after app1's dequeue = %d, want 2 (app1 claimed app2's work)", n)
		}
		gotB, err := b.Dequeue(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		sameIDs(t, "app2 Dequeue", idStrings(gotB, delID), wantIDs(sb.del1.ID, sb.del2.ID))
	})

	t.Run("attempts", func(t *testing.T) {
		attID := func(x *delivery.Attempt) id.ID { return x.ID }
		got, err := a.ListAttempts(ctx, sa.del1.ID)
		if err != nil {
			t.Fatal(err)
		}
		sameIDs(t, "app1 ListAttempts", idStrings(got, attID), wantIDs(sa.attempt.ID))
		cross, err := a.ListAttempts(ctx, sb.del1.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(cross) != 0 {
			t.Errorf("app1 listed %d attempts for app2's delivery, want 0", len(cross))
		}
		n, err := a.PurgeAttempts(ctx, time.Now().UTC())
		if err != nil || n != 1 {
			t.Fatalf("app1 PurgeAttempts = %d, %v; want 1", n, err)
		}
		if rawExists(t, raw, nsA+":relay:att:"+sa.attempt.ID.String()) {
			t.Error("app1's purged attempt record is still in redis: the delete missed its namespaced key")
		}
		left, err := b.ListAttempts(ctx, sb.del1.ID)
		if err != nil {
			t.Fatal(err)
		}
		sameIDs(t, "app2 ListAttempts after app1's purge", idStrings(left, attID), wantIDs(sb.attempt.ID))
	})

	t.Run("dlq, replay and the claim", func(t *testing.T) {
		dlqID := func(e *dlq.Entry) id.ID { return e.ID }
		for _, c := range []struct {
			name string
			s    *redisstore.Store
			sd   *seeded
		}{{nsA, a, sa}, {nsB, b, sb}} {
			all, err := c.s.ListDLQ(ctx, dlq.ListOpts{})
			if err != nil {
				t.Fatal(err)
			}
			sameIDs(t, c.name+" ListDLQ", idStrings(all, dlqID), wantIDs(c.sd.dlq.ID))
			byTenant, err := c.s.ListDLQ(ctx, dlq.ListOpts{TenantID: sharedTenant})
			if err != nil {
				t.Fatal(err)
			}
			sameIDs(t, c.name+" ListDLQ(tenant)", idStrings(byTenant, dlqID), wantIDs(c.sd.dlq.ID))
			ep := c.sd.enabledEP
			byEP, err := c.s.ListDLQ(ctx, dlq.ListOpts{EndpointID: &ep})
			if err != nil {
				t.Fatal(err)
			}
			sameIDs(t, c.name+" ListDLQ(endpoint)", idStrings(byEP, dlqID), wantIDs(c.sd.dlq.ID))
			if n, cErr := c.s.CountDLQ(ctx); cErr != nil || n != 1 {
				t.Errorf("%s CountDLQ = %d, %v; want 1", c.name, n, cErr)
			}
		}
		if _, err := a.GetDLQ(ctx, sb.dlq.ID); !errors.Is(err, relay.ErrDLQNotFound) {
			t.Errorf("app1 read app2's DLQ entry: err = %v, want not found", err)
		}

		// The claim reads and writes its record raw, under WATCH.
		at := time.Now().UTC()
		if err := a.MarkReplayed(ctx, sb.dlq.ID, at); !errors.Is(err, relay.ErrDLQNotFound) {
			t.Errorf("app1 claimed app2's entry: err = %v, want not found", err)
		}
		if err := a.MarkReplayed(ctx, sa.dlq.ID, at); err != nil {
			t.Fatalf("app1 claim of its own entry: %v", err)
		}
		if err := a.MarkReplayed(ctx, sa.dlq.ID, at); !errors.Is(err, relay.ErrAlreadyReplayed) {
			t.Errorf("second claim in app1: err = %v, want already replayed", err)
		}
		gotA, err := a.GetDLQ(ctx, sa.dlq.ID)
		if err != nil || gotA.ReplayedAt == nil {
			t.Fatalf("app1 entry after claim: replayed_at set = %v, err %v", gotA != nil && gotA.ReplayedAt != nil, err)
		}
		gotB, err := b.GetDLQ(ctx, sb.dlq.ID)
		if err != nil || gotB.ReplayedAt != nil {
			t.Errorf("app2's entry changed when app1 claimed its own: replayed_at set = %v, err %v",
				gotB != nil && gotB.ReplayedAt != nil, err)
		}
		if relErr := a.ReleaseReplay(ctx, sb.dlq.ID); !errors.Is(relErr, relay.ErrDLQNotFound) {
			t.Errorf("app1 released app2's entry: err = %v, want not found", relErr)
		}
		if relErr := a.ReleaseReplay(ctx, sa.dlq.ID); relErr != nil {
			t.Fatalf("app1 release: %v", relErr)
		}
		if claimErr := a.MarkReplayed(ctx, sa.dlq.ID, at); claimErr != nil {
			t.Errorf("app1 claim after its release: %v", claimErr)
		}
		// The claim wrote the namespaced record, not a second one beside it.
		if rawExists(t, raw, "relay:dlq:"+sa.dlq.ID.String()) {
			t.Error("the claim wrote an un-namespaced DLQ record")
		}

		// Purge removes records and index entries from its own namespace only.
		n, err := a.Purge(ctx, time.Now().UTC())
		if err != nil || n != 1 {
			t.Fatalf("app1 Purge = %d, %v; want 1", n, err)
		}
		if rawExists(t, raw, nsA+":relay:dlq:"+sa.dlq.ID.String()) {
			t.Error("app1's purged DLQ record is still in redis: the delete missed its namespaced key")
		}
		if c, _ := a.CountDLQ(ctx); c != 0 {
			t.Errorf("app1 CountDLQ after purge = %d, want 0", c)
		}
		if c, _ := b.CountDLQ(ctx); c != 1 {
			t.Errorf("app2 CountDLQ after app1's purge = %d, want 1", c)
		}
		if !rawExists(t, raw, nsB+":relay:dlq:"+sb.dlq.ID.String()) {
			t.Error("app2's DLQ record vanished when app1 purged")
		}
	})

	t.Run("deleting an endpoint", func(t *testing.T) {
		if err := a.DeleteEndpoint(ctx, sa.enabledEP); err != nil {
			t.Fatal(err)
		}
		if rawExists(t, raw, nsA+":relay:ep:"+sa.enabledEP.String()) {
			t.Error("a deleted endpoint kept its record, secret included, in redis")
		}
		if _, err := raw.ZScore(ctx, nsA+":relay:z:ep:all", sa.enabledEP.String()).Result(); !errors.Is(err, goredis.Nil) {
			t.Errorf("a deleted endpoint is still in app1's every-tenant index: %v", err)
		}
		if members, _ := raw.SMembers(ctx, nsA+":relay:s:ep:tenant:"+sharedTenant+":enabled").Result(); len(members) != 0 {
			t.Errorf("a deleted endpoint is still in app1's enabled set: %v", members)
		}
		if _, err := b.GetEndpoint(ctx, sb.enabledEP); err != nil {
			t.Errorf("app2 lost its endpoint when app1 deleted its own: %v", err)
		}
		left, err := b.ListEndpoints(ctx, sharedTenant, endpoint.ListOpts{})
		if err != nil || len(left) != 2 {
			t.Errorf("app2 ListEndpoints after app1's delete = %d, %v; want 2", len(left), err)
		}
	})

	t.Run("nothing escaped the namespaces", func(t *testing.T) {
		keys, err := raw.Keys(ctx, "relay:*").Result()
		if err != nil {
			t.Fatal(err)
		}
		if len(keys) != 0 {
			t.Errorf("un-namespaced relay keys in redis: %v", keys)
		}
	})
}

// The wake channel is namespaced too. A relay in one namespace wakes for its
// own enqueues and sleeps through its neighbour's.
func TestNamespacedWakeStaysInItsNamespace(t *testing.T) {
	ctx := context.Background()
	connStr := startRedis(t)
	a := namespacedStore(t, connStr, nsA)
	b := namespacedStore(t, connStr, nsB)

	woke := make(chan struct{}, 8)
	stop, err := a.StartWakeListener(ctx, func() { woke <- struct{}{} })
	if err != nil {
		t.Fatalf("start wake listener: %v", err)
	}
	t.Cleanup(stop)

	enqueue := func(s *redisstore.Store) {
		d := &delivery.Delivery{
			Entity: entity.New(), ID: id.NewDeliveryID(), EventID: id.NewEventID(), EndpointID: id.NewEndpointID(),
			State: delivery.StatePending, NextAttemptAt: time.Now().UTC(),
		}
		if err := s.Enqueue(ctx, d); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}

	enqueue(b)
	select {
	case <-woke:
		t.Fatal("app1's listener woke for an enqueue in app2")
	case <-time.After(400 * time.Millisecond):
	}
	enqueue(a)
	select {
	case <-woke:
	case <-time.After(5 * time.Second):
		t.Fatal("app1's listener never woke for its own enqueue")
	}
}

// legacyDelivery plants a delivery as an older version left it, under a
// namespace: in its endpoint's index only, and with no event type or tenant.
func legacyDelivery(t *testing.T, raw *goredis.Client, ns string, evt *event.Event) string {
	t.Helper()
	ctx := context.Background()
	delID, epID := id.NewDeliveryID().String(), id.NewEndpointID().String()
	created := time.Now().UTC().Add(-time.Hour)
	body, err := json.Marshal(map[string]any{
		"id": delID, "event_id": evt.ID.String(), "endpoint_id": epID, "state": "delivered",
		"next_attempt_at": created, "created_at": created, "updated_at": created,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Set(ctx, ns+":relay:del:"+delID, body, 0).Err(); err != nil {
		t.Fatal(err)
	}
	if err := raw.ZAdd(ctx, ns+":relay:z:del:ep:"+epID, redisZ(created, delID)).Err(); err != nil {
		t.Fatal(err)
	}
	return delID
}

// Migrate builds each namespace's global indexes from that namespace's own
// per-tenant indexes, and fills in old deliveries in that namespace only. The
// markers are namespaced as well, so one namespace being migrated says nothing
// about another.
func TestMigrateBuildsEachNamespacesIndexSeparately(t *testing.T) {
	ctx := context.Background()
	connStr := startRedis(t)
	raw := rawRedis(t, connStr)
	a := namespacedStore(t, connStr, nsA)
	b := namespacedStore(t, connStr, nsB)

	// Before any Migrate: one pre-upgrade endpoint and one pre-upgrade delivery
	// in each namespace.
	epA := createEndpoint(t, a, "tenant-a")
	epB := createEndpoint(t, b, "tenant-b")
	for _, ns := range []string{nsA, nsB} {
		if err := raw.Del(ctx, ns+":relay:z:ep:all", ns+":relay:migrated:ep_all:v1", ns+":relay:migrated:ep_all:built").Err(); err != nil {
			t.Fatal(err)
		}
	}
	evtA := &event.Event{Entity: entity.New(), ID: id.NewEventID(), Type: "type.a", TenantID: "tenant-a"}
	evtB := &event.Event{Entity: entity.New(), ID: id.NewEventID(), Type: "type.b", TenantID: "tenant-b"}
	if err := a.CreateEvent(ctx, evtA); err != nil {
		t.Fatal(err)
	}
	if err := b.CreateEvent(ctx, evtB); err != nil {
		t.Fatal(err)
	}
	delA := legacyDelivery(t, raw, nsA, evtA)
	delB := legacyDelivery(t, raw, nsB, evtB)

	for name, s := range map[string]*redisstore.Store{nsA: a, nsB: b} {
		if _, err := s.ListEndpoints(ctx, "", endpoint.ListOpts{}); !errors.Is(err, redisstore.ErrEndpointIndexNotBuilt) {
			t.Fatalf("%s before Migrate: err = %v, want ErrEndpointIndexNotBuilt", name, err)
		}
	}

	if err := a.Migrate(ctx); err != nil {
		t.Fatalf("migrate app1: %v", err)
	}

	// app1 is built and sees only its own.
	got, err := a.ListEndpoints(ctx, "", endpoint.ListOpts{})
	if err != nil || len(got) != 1 || got[0].ID != epA.ID {
		t.Fatalf("app1 every-tenant list after its Migrate = %v, %v; want just its own endpoint", got, err)
	}
	if members, _ := raw.ZRange(ctx, nsA+":relay:z:ep:all", 0, -1).Result(); !slices.Equal(members, []string{epA.ID.String()}) {
		t.Errorf("app1's global endpoint index = %v, want only its own endpoint", members)
	}
	page, err := a.ListDeliveries(ctx, delivery.Query{})
	if err != nil || len(page.Deliveries) != 1 || page.Deliveries[0].ID.String() != delA || page.Deliveries[0].EventType != "type.a" {
		t.Fatalf("app1 delivery log after its Migrate = %+v, %v; want its old delivery with the event type filled", page, err)
	}

	// app2 is untouched: not built, no global index, its old delivery still
	// without an event type.
	if _, listErr := b.ListEndpoints(ctx, "", endpoint.ListOpts{}); !errors.Is(listErr, redisstore.ErrEndpointIndexNotBuilt) {
		t.Errorf("app2 every-tenant list after app1's Migrate: err = %v, want ErrEndpointIndexNotBuilt", listErr)
	}
	if rawExists(t, raw, nsB+":relay:z:ep:all") || rawExists(t, raw, nsB+":relay:z:del:all") {
		t.Error("app1's Migrate wrote a global index into app2's namespace")
	}
	if body, _ := raw.Get(ctx, nsB+":relay:del:"+delB).Result(); strings.Contains(body, "type.b") {
		t.Error("app1's Migrate filled in an event type on app2's delivery")
	}

	if migErr := b.Migrate(ctx); migErr != nil {
		t.Fatalf("migrate app2: %v", migErr)
	}
	got, err = b.ListEndpoints(ctx, "", endpoint.ListOpts{})
	if err != nil || len(got) != 1 || got[0].ID != epB.ID {
		t.Fatalf("app2 every-tenant list after its Migrate = %v, %v; want just its own endpoint", got, err)
	}
	page, err = b.ListDeliveries(ctx, delivery.Query{})
	if err != nil || len(page.Deliveries) != 1 || page.Deliveries[0].ID.String() != delB || page.Deliveries[0].EventType != "type.b" {
		t.Fatalf("app2 delivery log after its Migrate = %+v, %v", page, err)
	}
	if keys, _ := raw.Keys(ctx, "relay:*").Result(); len(keys) != 0 {
		t.Errorf("un-namespaced relay keys after both Migrates: %v", keys)
	}
}

// suffixHook rewrites keys by appending to them, which no scan can undo.
type suffixHook struct{}

func (suffixHook) BeforeQuery(_ context.Context, qc *hook.QueryContext) (*hook.HookResult, error) {
	if keys, ok := qc.Values["_kv_keys"].([]string); ok {
		out := make([]string, len(keys))
		for i, k := range keys {
			out[i] = k + ":sfx"
		}
		qc.Values["_kv_keys"] = out
		if len(out) > 0 {
			qc.RawQuery = out[0]
		}
	}
	return &hook.HookResult{Decision: hook.Modify}, nil
}

// A namespace that applies to some commands and not others has no right
// answer. Say it is scoped to reads: kv would write a record at "k" and read it
// from "app1:k", so every read after a write misses. Relay cannot pick which
// half is the real key, and the hook is the wrong shape for a store that has to
// find its own records. Likewise a rewrite that is not a prefix, which a scan
// has no way to undo.
//
// The refusal comes from resolving a probe key under every command relay
// uses, so it needs no redis round trip: nothing is written, and nothing can be
// mistaken for replication lag.
func TestMigrateRefusesAKeyMappingItCannotFollow(t *testing.T) {
	connStr := startRedis(t)
	raw := rawRedis(t, connStr)
	allBut := func(skip hook.Operation) []hook.Operation {
		var ops []hook.Operation
		for _, op := range []hook.Operation{
			kv.OpGet, kv.OpSet, kv.OpDelete, kv.OpExists, kv.OpScan,
			kv.OpZAdd, kv.OpZRange, kv.OpZRem, kv.OpZCard,
			kv.OpSAdd, kv.OpSRem, kv.OpSMbrs, kv.OpEval,
		} {
			if op != skip {
				ops = append(ops, op)
			}
		}
		return ops
	}
	for name, c := range map[string]struct {
		h     any
		scope []hook.Scope
	}{
		"reads only (Get)":       {middleware.NewNamespace("app1"), []hook.Scope{{Operations: []hook.Operation{kv.OpGet}}}},
		"writes only (Set)":      {middleware.NewNamespace("app1"), []hook.Scope{{Operations: []hook.Operation{kv.OpSet}}}},
		"deletes only":           {middleware.NewNamespace("app1"), []hook.Scope{{Operations: []hook.Operation{kv.OpDelete}}}},
		"sorted sets only":       {middleware.NewNamespace("app1"), []hook.Scope{{Operations: []hook.Operation{kv.OpZAdd, kv.OpZRange, kv.OpZRem, kv.OpZCard}}}},
		"everything except Scan": {middleware.NewNamespace("app1"), []hook.Scope{{Operations: allBut(kv.OpScan)}}},
		"everything except Eval": {middleware.NewNamespace("app1"), []hook.Scope{{Operations: allBut(kv.OpEval)}}},
		"a suffix, not a prefix": {suffixHook{}, nil},
		"a suffix on reads only": {suffixHook{}, []hook.Scope{{Operations: []hook.Operation{kv.OpGet}}}},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			if err := raw.FlushAll(ctx).Err(); err != nil {
				t.Fatal(err)
			}
			drv := redisdriver.New()
			if err := drv.Open(ctx, connStr); err != nil {
				t.Fatalf("open: %v", err)
			}
			kvs, err := kv.Open(drv, kv.WithHook(c.h, c.scope...))
			if err != nil {
				t.Fatalf("kv open: %v", err)
			}
			t.Cleanup(func() { _ = kvs.Close() })
			s := redisstore.New(kvs)
			if err := s.Migrate(ctx); !errors.Is(err, redisstore.ErrKVKeysInconsistent) {
				t.Fatalf("Migrate: err = %v, want ErrKVKeysInconsistent", err)
			}
			if n, _ := raw.DBSize(ctx).Result(); n != 0 {
				t.Errorf("a refused Migrate left %d keys in redis, want none", n)
			}
		})
	}
}

// The old name still matches, so a caller that checked for it keeps refusing
// the stores that are still refused.
func TestOldErrorNameStillMatches(t *testing.T) {
	if !errors.Is(redisstore.ErrKVKeysInconsistent, redisstore.ErrKVRewritesKeys) {
		t.Fatal("ErrKVRewritesKeys no longer matches ErrKVKeysInconsistent")
	}
}

// A namespaced store leaves no probe keys behind either.
func TestMigrateLeavesNoProbeKeyUnderANamespace(t *testing.T) {
	ctx := context.Background()
	connStr := startRedis(t)
	raw := rawRedis(t, connStr)
	s := namespacedStore(t, connStr, nsA)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	left, err := raw.Keys(ctx, "*relay:probe:*").Result()
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("Migrate left probe keys behind: %v", left)
	}
}
