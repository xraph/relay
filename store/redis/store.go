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

// ErrKVRewritesKeys is returned by Migrate when the kv store it was given
// rewrites keys, as a namespace hook does. Relay's redis store reads and writes
// some keys through the raw client and the rest through kv, and relies on both
// meaning the same key. A store that rewrites keys breaks that without any
// error: deletes silently do nothing, a replayed DLQ entry is never removed,
// and replay cannot find records. Hooks that leave keys alone, such as
// encryption and compression, are fine.
var ErrKVRewritesKeys = errors.New(
	"relay/redis: the kv store rewrites keys (for example a namespace hook); " +
		"relay's redis store does not support that")

// Migrate runs data migrations. Redis has no schema, so the only work is
// backfilling indexes that newer code maintains and older code did not.
func (s *Store) Migrate(ctx context.Context) error {
	if err := s.checkKeysAreRaw(ctx); err != nil {
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

// checkKeysAreRaw confirms that kv and the raw client mean the same key by the
// same name, which the rest of this store depends on. It probes both ways: a
// key written through kv must be visible to the raw client, and a key written
// through the raw client must be readable through kv.
//
// It refuses only on positive evidence, never on a mere miss. This check stops
// the app booting, so a false refusal is an outage. Behind a proxy that sends
// reads to replicas, a miss can just be replication lag. So a miss is confirmed
// through the other client first: rewriting shows up as one client seeing the
// key and the other not, while lag or eviction shows up as neither seeing it,
// which proves nothing and passes.
//
// Each direction catches a case the other misses. A hook rewriting keys on
// reads only fails the read probe. A hook rewriting them on writes only fails
// the write probe, and that is the case relay is most exposed to, because its
// raw client reads records that kv wrote.
//
// Only keys are compared, not values. SetRaw never hands its value to kv's
// hooks, so no hook can change it.
func (s *Store) checkKeysAreRaw(ctx context.Context) error {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("relay/redis: probe nonce: %w", err)
	}
	tag := hex.EncodeToString(nonce[:])

	// Write path: kv writes, the raw client deletes. DEL is a write, so it goes
	// to the primary and cannot be fooled by a lagging replica, and it saves a
	// separate EXISTS. The TTL covers the one case where the probe cannot be
	// deleted: when keys are rewritten, the raw client cannot name it.
	written := "relay:probe:write:" + tag
	if err := s.kv.SetRaw(ctx, written, []byte("1"), kv.WithTTL(time.Minute)); err != nil {
		return fmt.Errorf("relay/redis: write key probe: %w", err)
	}
	deleted, err := s.rdb.Del(ctx, written).Result()
	if err != nil {
		return fmt.Errorf("relay/redis: delete key probe: %w", err)
	}
	if deleted == 0 {
		// The raw client found nothing to delete. Either the key was written
		// under another name, or it is simply gone. Look for any key carrying
		// the probe's unique nonce: finding one under a different name is
		// proof of a rewrite, and finding none proves nothing. This only runs
		// when something is already wrong, so a healthy boot never scans.
		found, scanErr := s.keysMatching(ctx, "*"+written)
		if scanErr != nil {
			return fmt.Errorf("relay/redis: look for key probe: %w", scanErr)
		}
		for _, k := range found {
			if k != written {
				_ = s.rdb.Del(ctx, k).Err() //nolint:errcheck // best-effort: the probe expires on its own
				return ErrKVRewritesKeys
			}
		}
	}

	// Read path: the raw client writes, kv reads. A hook scoped to reads alone
	// leaves the write probe above untouched and fails only here.
	read := "relay:probe:read:" + tag
	if err := s.rdb.Set(ctx, read, "1", time.Minute).Err(); err != nil {
		return fmt.Errorf("relay/redis: write read probe: %w", err)
	}
	defer func() { _ = s.rdb.Del(ctx, read).Err() }() //nolint:errcheck // best-effort: the probe expires on its own
	if _, getErr := s.kv.GetRaw(ctx, read); getErr != nil {
		// kv missed it. If the raw client can still see it, kv is reading a
		// different key. If neither can, it is lag or eviction, not rewriting.
		if seen, rawErr := s.rdb.Get(ctx, read).Result(); rawErr == nil && seen == "1" {
			return ErrKVRewritesKeys
		}
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
	recent, err := s.rdb.Exists(ctx, migratedEndpointAllV1).Result()
	if err != nil {
		return fmt.Errorf("relay/redis: check endpoint backfill marker: %w", err)
	}
	if recent == 1 {
		return nil
	}

	copyFrom := func(ctx context.Context, node goredis.UniversalClient) error {
		var cursor uint64
		for {
			keys, next, scanErr := node.Scan(ctx, cursor, zEndpointTenant+"*", 500).Result()
			if scanErr != nil {
				return fmt.Errorf("relay/redis: scan endpoint indexes: %w", scanErr)
			}
			for _, key := range keys {
				members, rangeErr := s.rdb.ZRangeWithScores(ctx, key, 0, -1).Result()
				if rangeErr != nil {
					// A key that matches the pattern but is not a zset is not
					// one of ours. Skip it; failing here would stop the boot.
					if isWrongType(rangeErr) {
						continue
					}
					return fmt.Errorf("relay/redis: read endpoint index %s: %w", key, rangeErr)
				}
				if len(members) == 0 {
					continue
				}
				if addErr := s.rdb.ZAdd(ctx, zEndpointAll, members...).Err(); addErr != nil {
					return fmt.Errorf("relay/redis: backfill endpoint index: %w", addErr)
				}
			}
			cursor = next
			if cursor == 0 {
				return nil
			}
		}
	}

	if cluster, ok := s.rdb.(*goredis.ClusterClient); ok {
		err = cluster.ForEachMaster(ctx, func(ctx context.Context, node *goredis.Client) error {
			return copyFrom(ctx, node)
		})
	} else {
		err = copyFrom(ctx, s.rdb)
	}
	if err != nil {
		return err
	}

	pipe := s.rdb.Pipeline()
	pipe.Set(ctx, endpointIndexBuilt, "1", 0)
	pipe.Set(ctx, migratedEndpointAllV1, "1", endpointBackfillTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("relay/redis: record endpoint backfill: %w", err)
	}
	return nil
}

// keysMatching returns every key matching pattern. On a cluster it scans every
// master, since a plain SCAN walks a single node.
func (s *Store) keysMatching(ctx context.Context, pattern string) ([]string, error) {
	var (
		mu  sync.Mutex
		out []string
	)
	scan := func(ctx context.Context, node goredis.UniversalClient) error {
		var cursor uint64
		for {
			keys, next, err := node.Scan(ctx, cursor, pattern, 500).Result()
			if err != nil {
				return err
			}
			mu.Lock()
			out = append(out, keys...)
			mu.Unlock()
			cursor = next
			if cursor == 0 {
				return nil
			}
		}
	}
	var err error
	if cluster, ok := s.rdb.(*goredis.ClusterClient); ok {
		err = cluster.ForEachMaster(ctx, func(ctx context.Context, node *goredis.Client) error {
			return scan(ctx, node)
		})
	} else {
		err = scan(ctx, s.rdb)
	}
	return out, err
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
	return s.rdb.ZRangeByScore(ctx, key, &goredis.ZRangeBy{
		Min: minStr,
		Max: maxStr,
	}).Result()
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
