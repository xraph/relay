package redis_test

import (
	"context"
	"errors"
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"github.com/xraph/grove/hook"
	"github.com/xraph/grove/kv"
	"github.com/xraph/grove/kv/drivers/redisdriver"
	"github.com/xraph/grove/kv/middleware"

	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
	redisstore "github.com/xraph/relay/store/redis"
)

// These key names are the on-disk format. A test simulating data written by
// an older version has to use them literally, because that data does not know
// about any constant in the code.
const (
	keyEndpointAll = "relay:z:ep:all"
	keyBackfilled  = "relay:migrated:ep_all:v1"    // re-run marker, expires
	keyIndexBuilt  = "relay:migrated:ep_all:built" // permanent: index is usable
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
	if err := raw.Del(ctx, keyBackfilled, keyIndexBuilt).Err(); err != nil {
		t.Fatalf("del markers: %v", err)
	}
	// Before Migrate the every-tenant list must not answer at all: an unbuilt
	// index looks exactly like "no endpoints", which is how an audit reported
	// clean. Refusing is what proves the pre-upgrade state is reproduced.
	if _, err := s.ListEndpoints(ctx, "", endpoint.ListOpts{Limit: 100}); !errors.Is(err, redisstore.ErrEndpointIndexNotBuilt) {
		t.Fatalf("before Migrate, the every-tenant list returned err=%v, want "+
			"ErrEndpointIndexNotBuilt", err)
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

// Before this, the backfill decoded every key under relay:ep:*, so one key
// that was not an endpoint record (a stray string, a hash) failed Migrate on
// every boot, and extension.go returns that error from Register: the app would
// not start. Migrate used to do nothing, so this was a new way to fail.
func TestMigrateSurvivesStrayKeysUnderTheEndpointPrefix(t *testing.T) {
	ctx := context.Background()
	connStr := startRedis(t)
	s := openRedisStore(t, connStr)
	raw := rawRedis(t, connStr)

	if err := raw.Set(ctx, "relay:ep:not-json", "definitely not json", 0).Err(); err != nil {
		t.Fatalf("seed string: %v", err)
	}
	if err := raw.HSet(ctx, "relay:ep:a-hash", "field", "value").Err(); err != nil {
		t.Fatalf("seed hash: %v", err)
	}
	// And a per-tenant index key of the wrong type, which the backfill reads.
	if err := raw.Set(ctx, "relay:z:ep:tenant:bogus", "not a zset", 0).Err(); err != nil {
		t.Fatalf("seed bogus index: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate failed on stray keys, so the app would not boot: %v", err)
	}
}

// ListUnsigned("") on redis walks the global index. If that index was never
// built (DisableMigrate, or a standalone script that skipped Migrate), the
// every-tenant list used to be empty and the audit reported clean, and the
// endpoints it missed were exactly the pre-upgrade ones it exists to find.
// It must refuse rather than answer.
func TestEveryTenantListRefusesBeforeTheIndexIsBuilt(t *testing.T) {
	ctx := context.Background()
	connStr := startRedis(t)
	s := openRedisStore(t, connStr) // no Migrate
	createEndpoint(t, s, "tenant-unbuilt")

	if _, err := s.ListEndpoints(ctx, "", endpoint.ListOpts{Limit: 100}); err == nil {
		t.Fatal("an every-tenant list answered before the global index was built")
	}
	// A named tenant does not use the global index, so it still works.
	if _, err := s.ListEndpoints(ctx, "tenant-unbuilt", endpoint.ListOpts{Limit: 100}); err != nil {
		t.Fatalf("a named-tenant list failed before Migrate: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := s.ListEndpoints(ctx, "", endpoint.ListOpts{Limit: 100}); err != nil {
		t.Fatalf("an every-tenant list failed after Migrate: %v", err)
	}
}

// The re-run marker expires, so the backfill runs again now and then. Without
// that, a rollback to a version that did not maintain the global index would
// leave every endpoint created during it out of the index for good.
func TestBackfillMarkerExpires(t *testing.T) {
	ctx := context.Background()
	connStr := startRedis(t)
	s := openRedisStore(t, connStr)
	raw := rawRedis(t, connStr)

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ttl, err := raw.TTL(ctx, keyBackfilled).Result()
	if err != nil {
		t.Fatalf("ttl: %v", err)
	}
	if ttl <= 0 {
		t.Fatalf("the re-run marker has TTL %v, want it to expire", ttl)
	}
	// The readiness flag is the opposite: once the index exists, it exists.
	built, err := raw.TTL(ctx, keyIndexBuilt).Result()
	if err != nil {
		t.Fatalf("ttl built: %v", err)
	}
	if built != -1 {
		t.Fatalf("the index-built flag has TTL %v, want none (-1)", built)
	}
}

// An id can outlive its record in the global index: an older version's delete
// removed the record without touching an index it did not know about. The
// list skips such ids, but they would sit in the index forever, making every
// every-tenant list slower. It removes them as it finds them.
func TestEveryTenantListDropsIDsWhoseRecordIsGone(t *testing.T) {
	ctx := context.Background()
	connStr := startRedis(t)
	s := openRedisStore(t, connStr)
	raw := rawRedis(t, connStr)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	gone := createEndpoint(t, s, "tenant-gone")
	kept := createEndpoint(t, s, "tenant-kept")
	if err := raw.Del(ctx, "relay:ep:"+gone.ID.String()).Err(); err != nil {
		t.Fatalf("del record: %v", err)
	}

	got, err := s.ListEndpoints(ctx, "", endpoint.ListOpts{Limit: 100})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, ep := range got {
		if ep.ID == gone.ID {
			t.Fatal("the list returned an endpoint whose record is gone")
		}
	}
	if _, err := raw.ZScore(ctx, keyEndpointAll, gone.ID.String()).Result(); !errors.Is(err, goredis.Nil) {
		t.Fatalf("the stale id is still in the global index (err=%v)", err)
	}
	if _, err := raw.ZScore(ctx, keyEndpointAll, kept.ID.String()).Result(); err != nil {
		t.Fatalf("the cleanup removed a live endpoint from the index: %v", err)
	}
}

func openHookedRedisStore(t *testing.T, connStr string, h any) *redisstore.Store {
	t.Helper()
	drv := redisdriver.New()
	if err := drv.Open(context.Background(), connStr); err != nil {
		t.Fatalf("open: %v", err)
	}
	kvs, err := kv.Open(drv, kv.WithHook(h))
	if err != nil {
		t.Fatalf("kv open: %v", err)
	}
	t.Cleanup(func() { _ = kvs.Close() })
	return redisstore.New(kvs)
}

// Relay's redis store reads and writes some keys through the raw client and
// the rest through kv, and relies on both meaning the same key. A kv hook that
// rewrites keys breaks that silently: under a namespace, deletes became no-ops
// (a "deleted" endpoint kept its record and secret), a replayed DLQ record was
// never removed so a second replay double-sent, and the replay claim could not
// find any record at all. So Migrate refuses such a store, once, at startup.
func TestMigrateRefusesAStoreThatRewritesKeys(t *testing.T) {
	connStr := startRedis(t)
	s := openHookedRedisStore(t, connStr, middleware.NewNamespace("app1"))
	if err := s.Migrate(context.Background()); !errors.Is(err, redisstore.ErrKVRewritesKeys) {
		t.Fatalf("Migrate on a namespaced store: err = %v, want ErrKVRewritesKeys", err)
	}
}

// A namespace scoped to reads leaves writes where relay's raw client expects
// them, so a check that only probes writes passes it, and then every read of a
// record looks in the wrong place: GetEndpoint answers "not found" straight
// after a create. The check probes the read path too.
func TestMigrateRefusesAStoreThatRewritesKeysOnReadsOnly(t *testing.T) {
	connStr := startRedis(t)
	drv := redisdriver.New()
	if err := drv.Open(context.Background(), connStr); err != nil {
		t.Fatalf("open: %v", err)
	}
	kvs, err := kv.Open(drv, kv.WithHook(middleware.NewNamespace("app1"),
		hook.Scope{Operations: []hook.Operation{kv.OpGet}}))
	if err != nil {
		t.Fatalf("kv open: %v", err)
	}
	t.Cleanup(func() { _ = kvs.Close() })
	s := redisstore.New(kvs)
	if err := s.Migrate(context.Background()); !errors.Is(err, redisstore.ErrKVRewritesKeys) {
		t.Fatalf("Migrate on a read-scoped namespace: err = %v, want ErrKVRewritesKeys", err)
	}
}

// The case that matters most to relay, whose raw client reads records kv wrote:
// a hook that rewrites keys on writes only. kv writes the record somewhere the
// raw client never looks, and reads through kv do not show it either, so it is
// invisible unless the check goes looking for where the write went.
func TestMigrateRefusesAStoreThatRewritesKeysOnWritesOnly(t *testing.T) {
	connStr := startRedis(t)
	drv := redisdriver.New()
	if err := drv.Open(context.Background(), connStr); err != nil {
		t.Fatalf("open: %v", err)
	}
	kvs, err := kv.Open(drv, kv.WithHook(middleware.NewNamespace("app1"),
		hook.Scope{Operations: []hook.Operation{kv.OpSet}}))
	if err != nil {
		t.Fatalf("kv open: %v", err)
	}
	t.Cleanup(func() { _ = kvs.Close() })
	s := redisstore.New(kvs)
	if err := s.Migrate(context.Background()); !errors.Is(err, redisstore.ErrKVRewritesKeys) {
		t.Fatalf("Migrate on a write-scoped namespace: err = %v, want ErrKVRewritesKeys", err)
	}
}

// The refusal must not catch hooks that leave keys alone. In grove kv v1.6.3
// the encrypt and compress hooks do not change stored bytes at all: encrypt
// implements no hook interface, and compress sets a flag nothing reads. So
// this proves those two hooks are not refused, not that relay's records end
// up encrypted or compressed. They do not.
func TestMigrateAcceptsHooksThatLeaveKeysAlone(t *testing.T) {
	connStr := startRedis(t)
	enc, err := middleware.NewEncrypt([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("encrypt hook: %v", err)
	}
	for name, h := range map[string]any{
		"encrypt":  enc,
		"compress": middleware.NewCompress(middleware.CompressionAlgorithm(0), 1),
	} {
		s := openHookedRedisStore(t, connStr, h)
		if err := s.Migrate(context.Background()); err != nil {
			t.Fatalf("Migrate refused a store with a %s hook, which relay works with: %v", name, err)
		}
	}
}

// The check writes a probe key; on a store relay supports it must not leave it.
func TestMigrateLeavesNoProbeKeyBehind(t *testing.T) {
	ctx := context.Background()
	connStr := startRedis(t)
	s := openRedisStore(t, connStr)
	raw := rawRedis(t, connStr)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	left, err := raw.Keys(ctx, "relay:probe:*").Result()
	if err != nil {
		t.Fatalf("keys: %v", err)
	}
	if len(left) != 0 {
		t.Fatalf("Migrate left probe keys behind: %v", left)
	}
}
