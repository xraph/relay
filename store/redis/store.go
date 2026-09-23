package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
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
}

// New creates a new Redis store backed by Grove KV.
func New(store *kv.Store) *Store {
	return &Store{
		kv:  store,
		rdb: redisdriver.UnwrapClient(store),
	}
}

// Migrate runs data migrations. Redis has no schema, so the only work is
// backfilling indexes that newer code maintains and older code did not.
func (s *Store) Migrate(ctx context.Context) error {
	return s.backfillEndpointAll(ctx)
}

// backfillEndpointAll copies every endpoint into zEndpointAll, the index an
// every-tenant listing reads.
//
// It builds from the per-tenant index keys, not the endpoint records. Each
// relay:z:ep:tenant:* zset already holds its endpoints' ids scored by
// created_at, which is everything the global index needs, so no record is
// decoded. That matters three ways. A stray key under relay:ep:* cannot stop
// the app booting, which it did when this decoded records. Index keys are
// written and read through the raw client, so this stays consistent under a kv
// namespace hook, where records are namespaced but indexes are not. And it is
// one ZRANGE per tenant instead of one GET per endpoint.
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
