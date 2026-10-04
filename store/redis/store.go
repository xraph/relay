package redis

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/xraph/grove/hook"
	"github.com/xraph/grove/kv"
	"github.com/xraph/grove/kv/drivers/redisdriver"

	relaystore "github.com/xraph/relay/store"
)

// compile-time interface check
var _ relaystore.Store = (*Store)(nil)

// Store implements store.Store using Redis via Grove KV.
type Store struct {
	kv  *kv.Store
	rdb goredis.UniversalClient

	// scanWindow bounds how many index entries one list call examines when
	// it has to filter in Go. See ListDeliveries.
	scanWindow int
}

// New creates a new Redis store backed by Grove KV.
func New(store *kv.Store) *Store {
	return &Store{
		kv:         store,
		rdb:        redisdriver.UnwrapClient(store),
		scanWindow: defaultScanWindow,
	}
}

// ErrKVKeysInconsistent is returned by Migrate when the kv store maps relay's
// keys in a way the store cannot follow. Relay supports a kv store that
// namespaces its keys, as a namespace hook does, but only if every command
// means the same key. Two things break that:
//
//   - a hook that rewrites keys for some commands and not others, such as a
//     namespace scoped to reads or to writes. A record would be written under one
//     name and read under another, so there is no right answer to give;
//   - a rewrite that is not a prefix, which a scan cannot undo to find the logical
//     key again.
//
// Hooks that leave keys alone, such as encryption and compression, are fine, and
// so is a hook that puts the same prefix on every key.
var ErrKVKeysInconsistent = errors.New(
	"relay/redis: the kv store does not map a key to the same redis key for every command " +
		"(for example a namespace hook scoped to reads or writes only)")

// ErrKVRewritesKeys is the old name for ErrKVKeysInconsistent. It is the same
// error value, so errors.Is matches either, but Migrate no longer returns it for
// a store that namespaces every key.
//
// Deprecated: use ErrKVKeysInconsistent. A namespaced kv store is supported now,
// so "rewrites keys" is no longer what this means.
var ErrKVRewritesKeys = ErrKVKeysInconsistent

// Migrate runs data migrations. Redis has no schema, so the only work is
// backfilling indexes that newer code maintains and older code did not.
func (s *Store) Migrate(ctx context.Context) error {
	if err := s.checkKeyMapping(ctx); err != nil {
		return err
	}
	if err := s.backfillEndpointAll(ctx); err != nil {
		return err
	}
	if err := s.backfillDeliveryFields(ctx); err != nil {
		return err
	}
	return s.backfillDeliveryAll(ctx)
}

// checkKeyMapping confirms that the key mapping the rest of this store depends
// on is one it can follow, in two steps.
//
// The first is local. A probe key must resolve to the same physical key under
// every command relay issues, and the result must end in the probe, so a scan
// can strip what a hook added. A namespace scoped to reads or to writes fails
// here: records would be written under one name and read under another. It
// touches no redis, so it cannot be fooled by replication lag.
//
// The second checks relay's own key resolution against kv, which is the thing
// every raw command now trusts. It probes both ways: a key written through kv
// must be at the physical key relay computes, and a key written at the physical
// key relay computes must be readable through kv. It guards against grove
// changing how it hands keys to hooks, which relay mirrors because grove does not
// export it.
//
// The second step refuses only on positive evidence, never on a mere miss. This
// check stops the app booting, so a false refusal is an outage. Behind a proxy
// that sends reads to replicas, a miss can just be replication lag. So a miss is
// confirmed through the other client first: a disagreement shows up as one
// client seeing the key and the other not, while lag or eviction shows up as
// neither seeing it, which proves nothing and passes.
//
// Only keys are compared, not values. SetRaw never hands its value to kv's
// hooks, so no hook can change it.
func (s *Store) checkKeyMapping(ctx context.Context) error {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("relay/redis: probe nonce: %w", err)
	}
	tag := hex.EncodeToString(nonce[:])

	if err := s.checkMappingIsUniform(ctx, "relay:probe:"+tag); err != nil {
		return err
	}

	// Write path: kv writes, the raw client deletes at the key relay computes.
	// DEL is a write, so it goes to the primary and cannot be fooled by a
	// lagging replica, and it saves a separate EXISTS. The TTL covers the one
	// case where the probe cannot be deleted: when the two disagree, the raw
	// client cannot name it.
	writeKey := "relay:probe:write:" + tag
	written, err := s.physicalKey(ctx, kv.OpSet, writeKey)
	if err != nil {
		return fmt.Errorf("relay/redis: resolve key probe: %w", err)
	}
	if setErr := s.kv.SetRaw(ctx, writeKey, []byte("1"), kv.WithTTL(time.Minute)); setErr != nil {
		return fmt.Errorf("relay/redis: write key probe: %w", setErr)
	}
	deleted, err := s.rdb.Del(ctx, written).Result()
	if err != nil {
		return fmt.Errorf("relay/redis: delete key probe: %w", err)
	}
	if deleted == 0 {
		// The raw client found nothing to delete. Either kv wrote the probe
		// under another name, or it is simply gone. Look for any key carrying
		// the probe's unique nonce: finding one under a different name is
		// proof of a disagreement, and finding none proves nothing. This only
		// runs when something is already wrong, so a healthy boot never scans.
		var found []string
		var mu sync.Mutex
		scanErr := s.scanPhysical(ctx, "*"+writeKey, func(k string) error {
			mu.Lock()
			found = append(found, k)
			mu.Unlock()
			return nil
		})
		if scanErr != nil {
			return fmt.Errorf("relay/redis: look for key probe: %w", scanErr)
		}
		for _, k := range found {
			if k != written {
				_ = s.rdb.Del(ctx, k).Err() //nolint:errcheck // best-effort: the probe expires on its own
				return fmt.Errorf("%w: kv wrote a probe to %q, relay expected %q", ErrKVKeysInconsistent, k, written)
			}
		}
	}

	// Read path: the raw client writes, kv reads. A hook scoped to reads alone
	// leaves the write probe above untouched and fails only here.
	readKey := "relay:probe:read:" + tag
	readAt, err := s.physicalKey(ctx, kv.OpGet, readKey)
	if err != nil {
		return fmt.Errorf("relay/redis: resolve read probe: %w", err)
	}
	if setErr := s.rdb.Set(ctx, readAt, "1", time.Minute).Err(); setErr != nil {
		return fmt.Errorf("relay/redis: write read probe: %w", setErr)
	}
	defer func() { _ = s.rdb.Del(ctx, readAt).Err() }() //nolint:errcheck // best-effort: the probe expires on its own
	if _, getErr := s.kv.GetRaw(ctx, readKey); getErr != nil {
		// kv missed it. If the raw client can still see it, kv is reading a
		// different key. If neither can, it is lag or eviction, not a
		// disagreement.
		if seen, rawErr := s.rdb.Get(ctx, readAt).Result(); rawErr == nil && seen == "1" {
			return fmt.Errorf("%w: kv did not find a probe at %q", ErrKVKeysInconsistent, readAt)
		}
	}
	return nil
}

// checkMappingIsUniform is the local half of checkKeyMapping: key must resolve
// to one physical key under every command relay uses, and that key must end in
// key. Publish and subscribe are left out, since a wake that misses only costs
// a poll interval.
func (s *Store) checkMappingIsUniform(ctx context.Context, key string) error {
	var first string
	var firstOp hook.Operation
	for i, op := range relayOps {
		if op == kv.OpPublish || op == kv.OpSubscr {
			continue
		}
		got, err := s.physicalKey(ctx, op, key)
		if err != nil {
			return fmt.Errorf("relay/redis: resolve probe key for %s: %w", kv.CommandName(op), err)
		}
		if i == 0 {
			first, firstOp = got, op
			continue
		}
		if got != first {
			return fmt.Errorf("%w: %s maps a key to %q but %s maps it to %q",
				ErrKVKeysInconsistent, kv.CommandName(firstOp), first, kv.CommandName(op), got)
		}
	}
	if !strings.HasSuffix(first, key) {
		return fmt.Errorf("%w: a key becomes %q, which is not a prefix added to it", ErrKVKeysInconsistent, first)
	}
	return nil
}

// backfillEndpointAll copies every endpoint into zEndpointAll, the index an
// every-tenant listing reads.
//
// It builds from the per-tenant index keys, not the endpoint records. Each
// relay:z:ep:tenant:* zset already holds its endpoints' ids scored by
// created_at, which is everything the global index needs, so no record is
// decoded. That matters two ways. A stray key under relay:ep:* cannot stop the
// app booting, which it did when this decoded records. And it is one ZRANGE
// per tenant instead of one GET per endpoint.
//
// ZADD is idempotent, so a run that dies partway is safe to repeat; the
// markers are written only after the scan completes. On a cluster it scans
// every master, since a plain SCAN walks a single node.
func (s *Store) backfillEndpointAll(ctx context.Context) error {
	recent, err := s.exists(ctx, migratedEndpointAllV1)
	if err != nil {
		return fmt.Errorf("relay/redis: check endpoint backfill marker: %w", err)
	}
	if recent {
		return nil
	}

	// scanKeys resolves the pattern, so a namespaced store copies only its own
	// tenants' indexes, and it hands back logical keys.
	err = s.scanKeys(ctx, zEndpointTenant+"*", func(key string) error {
		members, rangeErr := s.zRangeAllWithScores(ctx, key)
		if rangeErr != nil {
			// A key that matches the pattern but is not a zset is not
			// one of ours. Skip it; failing here would stop the boot.
			if isWrongType(rangeErr) {
				return nil
			}
			return fmt.Errorf("relay/redis: read endpoint index %s: %w", key, rangeErr)
		}
		if len(members) == 0 {
			return nil
		}
		if addErr := s.zAdd(ctx, zEndpointAll, members...); addErr != nil {
			return fmt.Errorf("relay/redis: backfill endpoint index: %w", addErr)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("relay/redis: scan endpoint indexes: %w", err)
	}

	pipe := s.pipeline(ctx)
	pipe.set(endpointIndexBuilt, "1", 0)
	pipe.set(migratedEndpointAllV1, "1", endpointBackfillTTL)
	if err := pipe.exec(); err != nil {
		return fmt.Errorf("relay/redis: record endpoint backfill: %w", err)
	}
	return nil
}

// isWrongType reports whether err is redis refusing an operation because the
// key holds a different data type.
func isWrongType(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "WRONGTYPE")
}

// Ping checks Redis connectivity.
func (s *Store) Ping(ctx context.Context) error {
	return s.kv.Ping(ctx)
}

// Close closes the KV store.
func (s *Store) Close() error {
	return s.kv.Close()
}

// now returns the current UTC time.
func now() time.Time {
	return time.Now().UTC()
}

// scoreFromTime converts a time.Time to a sorted set score (unix seconds as float64).
func scoreFromTime(t time.Time) float64 {
	return float64(t.UnixNano()) / 1e9
}

// isNotFound checks if an error is a KV not-found sentinel.
func isNotFound(err error) bool {
	return errors.Is(err, kv.ErrNotFound)
}

// isRedisNil checks if an error is a Redis nil (key not found).
func isRedisNil(err error) bool {
	return errors.Is(err, goredis.Nil)
}

// getEntity retrieves and decodes a JSON entity from a KV key.
func (s *Store) getEntity(ctx context.Context, key string, dest any) error {
	raw, err := s.kv.GetRaw(ctx, key)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dest)
}

// setEntity encodes and stores a JSON entity under a KV key.
func (s *Store) setEntity(ctx context.Context, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("relay/redis: marshal entity: %w", err)
	}
	return s.kv.SetRaw(ctx, key, raw)
}

// zRangeByScoreIDs returns all member IDs from a sorted set within a score range.
func (s *Store) zRangeByScoreIDs(ctx context.Context, key string, lo, hi float64) ([]string, error) {
	minStr := "-inf"
	maxStr := "+inf"
	if !math.IsInf(lo, -1) {
		minStr = strconv.FormatFloat(lo, 'f', -1, 64)
	}
	if !math.IsInf(hi, 1) {
		maxStr = strconv.FormatFloat(hi, 'f', -1, 64)
	}
	return s.zRangeByScore(ctx, key, &goredis.ZRangeBy{
		Min: minStr,
		Max: maxStr,
	})
}

// applyPagination applies offset and limit to a slice.
func applyPagination[T any](items []*T, offset, limit int) []*T {
	if offset > 0 && offset < len(items) {
		items = items[offset:]
	} else if offset >= len(items) {
		return nil
	}
	if limit > 0 && limit < len(items) {
		items = items[:limit]
	}
	return items
}
