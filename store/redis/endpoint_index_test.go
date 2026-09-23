package redis_test

import (
	"context"
	"errors"
	"testing"

	goredis "github.com/redis/go-redis/v9"

	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
)

// These key names are the on-disk format. A test simulating data written by
// an older version has to use them literally, because that data does not know
// about any constant in the code.
const (
	keyEndpointAll = "relay:z:ep:all"
	keyBackfilled  = "relay:migrated:ep_all:v1"
)

func rawRedis(t *testing.T, connStr string) *goredis.Client {
	t.Helper()
	opts, err := goredis.ParseURL(connStr)
	if err != nil {
		t.Fatalf("parse redis url: %v", err)
	}
	c := goredis.NewClient(opts)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func createEndpoint(t *testing.T, s interface {
	CreateEndpoint(context.Context, *endpoint.Endpoint) error
}, tenant string) *endpoint.Endpoint {
	t.Helper()
	ep := &endpoint.Endpoint{
		Entity:     entity.New(),
		ID:         id.NewEndpointID(),
		TenantID:   tenant,
		URL:        "https://receiver.example/hook",
		Secret:     "whsec_index",
		EventTypes: []string{"*"},
		Enabled:    true,
	}
	if err := s.CreateEndpoint(context.Background(), ep); err != nil {
		t.Fatalf("create: %v", err)
	}
	return ep
}

func listsAll(t *testing.T, s interface {
	ListEndpoints(context.Context, string, endpoint.ListOpts) ([]*endpoint.Endpoint, error)
}, epID id.ID) bool {
	t.Helper()
	got, err := s.ListEndpoints(context.Background(), "", endpoint.ListOpts{Limit: 100000})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, ep := range got {
		if ep.ID == epID {
			return true
		}
	}
	return false
}

// An endpoint written before the global index existed is only in its
// per-tenant index. Migrate must find it, or upgrading silently drops every
// existing endpoint from the every-tenant list.
func TestEndpointBackfillIndexesPreUpgradeEndpoints(t *testing.T) {
	ctx := context.Background()
	connStr := startRedis(t)
	s := openRedisStore(t, connStr)
	raw := rawRedis(t, connStr)

	ep := createEndpoint(t, s, "tenant-pre-upgrade")
	// Make it look exactly as an older version left it: present in its
	// tenant's index, absent from the global one, and never backfilled.
	if err := raw.ZRem(ctx, keyEndpointAll, ep.ID.String()).Err(); err != nil {
		t.Fatalf("zrem: %v", err)
	}
	if err := raw.Del(ctx, keyBackfilled).Err(); err != nil {
		t.Fatalf("del marker: %v", err)
	}
	if listsAll(t, s, ep.ID) {
		t.Fatal("the simulated pre-upgrade endpoint is already listed; the " +
			"setup is not reproducing the pre-upgrade state")
	}

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !listsAll(t, s, ep.ID) {
		t.Fatal("Migrate did not backfill a pre-upgrade endpoint into the global index")
	}
	if n, _ := raw.Exists(ctx, keyBackfilled).Result(); n != 1 {
		t.Fatal("Migrate did not record that the backfill ran")
	}
}

// The backfill is a one-time migration. Once its marker is set, Migrate must
// not scan the keyspace again on every boot. This pins that it does not: an
// entry hidden after the marker is set stays hidden.
func TestEndpointBackfillRunsOnce(t *testing.T) {
	ctx := context.Background()
	connStr := startRedis(t)
	s := openRedisStore(t, connStr)
	raw := rawRedis(t, connStr)

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	ep := createEndpoint(t, s, "tenant-after-marker")
	if err := raw.ZRem(ctx, keyEndpointAll, ep.ID.String()).Err(); err != nil {
		t.Fatalf("zrem: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if listsAll(t, s, ep.ID) {
		t.Fatal("the second Migrate rescanned the keyspace; the marker should have stopped it")
	}
}

// Deleting must remove the endpoint from the global index too. The list skips
// ids whose entity is gone, so a stale entry would not show up there; it would
// just make every every-tenant listing slower forever. Check the index itself.
func TestEndpointDeleteRemovesItFromTheGlobalIndex(t *testing.T) {
	ctx := context.Background()
	connStr := startRedis(t)
	s := openRedisStore(t, connStr)
	raw := rawRedis(t, connStr)

	ep := createEndpoint(t, s, "tenant-delete")
	if _, err := raw.ZScore(ctx, keyEndpointAll, ep.ID.String()).Result(); err != nil {
		t.Fatalf("a created endpoint is not in the global index: %v", err)
	}
	if err := s.DeleteEndpoint(ctx, ep.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := raw.ZScore(ctx, keyEndpointAll, ep.ID.String()).Result(); !errors.Is(err, goredis.Nil) {
		t.Fatalf("a deleted endpoint is still in the global index (err=%v)", err)
	}
}
